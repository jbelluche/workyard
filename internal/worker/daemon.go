package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type DaemonOptions struct {
	StateDir  string
	Socket    string
	AllowRoot bool
}

type Daemon struct {
	opts         DaemonOptions
	mu           sync.Mutex
	processes    map[string]*os.Process
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

func Serve(ctx context.Context, opts DaemonOptions) error {
	if os.Geteuid() == 0 && !opts.AllowRoot {
		return errors.New("refusing to run workyard daemon as root without --allow-root")
	}
	if opts.StateDir == "" {
		opts.StateDir = defaultStateDir()
	}
	stateDir, err := filepath.Abs(opts.StateDir)
	if err != nil {
		return err
	}
	opts.StateDir = stateDir
	socket, err := managedSocketPath(opts.StateDir, opts.Socket)
	if err != nil {
		return err
	}
	opts.Socket = socket
	if err := os.MkdirAll(filepath.Dir(opts.Socket), 0o700); err != nil {
		return err
	}
	opts.Socket, err = managedSocketPath(opts.StateDir, opts.Socket)
	if err != nil {
		return err
	}
	lock, err := acquireDaemonLock(opts.StateDir)
	if err != nil {
		return err
	}
	defer releaseDaemonLock(lock)
	if err := removeStaleSocket(opts.Socket); err != nil {
		return err
	}
	ln, err := net.Listen("unix", opts.Socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		_ = removeSocket(opts.Socket)
	}()
	if err := os.Chmod(opts.Socket, 0o600); err != nil {
		return err
	}
	d := &Daemon{opts: opts, processes: map[string]*os.Process{}, shutdown: make(chan struct{})}
	if err := d.recoverServices(); err != nil {
		return err
	}
	defer d.shutdownServices(5 * time.Second)
	go func() {
		select {
		case <-ctx.Done():
		case <-d.shutdown:
		}
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-d.shutdown:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go d.handle(conn)
	}
}

func (d *Daemon) recoverServices() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	runRoots, err := discoverRunRoots(d.opts.StateDir)
	if err != nil {
		return err
	}
	for _, runRoot := range runRoots {
		st, err := loadState(runRoot, "", "", "")
		if err != nil {
			continue
		}
		changed := false
		for name, svc := range st.Services {
			if svc.PID <= 0 || !isRunningStatus(svc.Status) {
				continue
			}
			if processIdentityMatches(svc.Process) {
				if proc, err := os.FindProcess(svc.PID); err == nil {
					d.processes[serviceKey(runRoot, name)] = proc
				}
				svc = recoveredState(svc)
				svc = refreshURL(svc, st.Worker)
				st.Services[name] = svc
				appendEvent(runRoot, Event{Type: "service.recovered", Service: name, Message: "recovered running service after daemon start", PID: svc.PID})
				changed = true
				continue
			}
			svc.Status = "exited"
			svc.Healthy = false
			svc.PID = 0
			svc.Process = ProcessID{}
			if svc.StoppedAt.IsZero() {
				svc.StoppedAt = time.Now().UTC()
			}
			st.Services[name] = svc
			appendEvent(runRoot, Event{Type: "service.stale_recovered", Service: name, Message: "cleared stale service pid during daemon recovery"})
			changed = true
		}
		if changed {
			_ = saveState(runRoot, st)
		}
	}
	return nil
}

func recoveredState(svc ServiceState) ServiceState {
	if svc.HealthURL == "" {
		if svc.Status == "starting" {
			svc.Status = "running"
		}
		return svc
	}
	if healthOK(svc.HealthURL, 1200*time.Millisecond) {
		svc.Healthy = true
		svc.Status = "healthy"
		return svc
	}
	svc.Healthy = false
	if svc.Status == "healthy" {
		svc.Status = "unhealthy"
	}
	return svc
}

func (d *Daemon) shutdownServices(grace time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	runRoots, err := discoverRunRoots(d.opts.StateDir)
	if err != nil {
		return
	}
	signaled := map[int]bool{}
	for _, runRoot := range runRoots {
		st, err := loadState(runRoot, "", "", "")
		if err != nil {
			continue
		}
		changed := false
		for name, svc := range st.Services {
			if svc.PID <= 0 || !isRunningStatus(svc.Status) {
				continue
			}
			if !processIdentityMatches(svc.Process) {
				continue
			}
			if !signaled[svc.PID] {
				appendEvent(runRoot, Event{Type: "service.daemon_shutdown", Service: name, Message: "stopping service during daemon shutdown", PID: svc.PID})
				terminateProcessGroup(svc.PID, grace)
				signaled[svc.PID] = true
			}
			svc.Status = "stopped"
			svc.Healthy = false
			svc.PID = 0
			svc.Process = ProcessID{}
			svc.StoppedAt = time.Now().UTC()
			st.Services[name] = svc
			delete(d.processes, serviceKey(runRoot, name))
			changed = true
		}
		if changed {
			_ = saveState(runRoot, st)
		}
	}
}

func discoverRunRoots(stateDir string) ([]string, error) {
	if stateDir == "" {
		stateDir = defaultStateDir()
	}
	runsRoot, err := filepath.Abs(filepath.Join(stateDir, "runs"))
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(runsRoot)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var roots []string
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		projectRoot := filepath.Join(runsRoot, project.Name())
		runs, err := os.ReadDir(projectRoot)
		if err != nil {
			continue
		}
		for _, run := range runs {
			if !run.IsDir() || run.Type()&os.ModeSymlink != 0 {
				continue
			}
			runRoot := filepath.Join(projectRoot, run.Name())
			if _, err := os.Stat(statePath(runRoot)); err == nil {
				roots = append(roots, runRoot)
			}
		}
	}
	return roots, nil
}

func managedSocketPath(stateDir, socket string) (string, error) {
	if stateDir == "" {
		stateDir = defaultStateDir()
	}
	stateDirAbs, err := filepath.Abs(stateDir)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(stateDirAbs); err == nil {
		stateDirAbs = real
	}
	daemonDir := filepath.Join(stateDirAbs, "daemon")
	if socket == "" {
		socket = filepath.Join(daemonDir, "workyard.sock")
	}
	socketAbs, err := filepath.Abs(socket)
	if err != nil {
		return "", err
	}
	if realDir, err := filepath.EvalSymlinks(filepath.Dir(socketAbs)); err == nil {
		socketAbs = filepath.Join(realDir, filepath.Base(socketAbs))
	}
	rel, err := filepath.Rel(daemonDir, socketAbs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("daemon socket must be under %s", daemonDir)
	}
	return socketAbs, nil
}

func acquireDaemonLock(stateDir string) (*os.File, error) {
	lockPath := filepath.Join(stateDir, "daemon", "daemon.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("workyard daemon is already running for %s", stateDir)
	}
	if err := file.Truncate(0); err != nil {
		releaseDaemonLock(file)
		return nil, err
	}
	if _, err := fmt.Fprintf(file, "%d\n", os.Getpid()); err != nil {
		releaseDaemonLock(file)
		return nil, err
	}
	return file, nil
}

func releaseDaemonLock(file *os.File) {
	if file == nil {
		return
	}
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func removeStaleSocket(socket string) error {
	info, err := os.Lstat(socket)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove non-socket file at %s", socket)
	}
	conn, err := net.Dial("unix", socket)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("workyard daemon is already running at %s", socket)
	}
	return os.Remove(socket)
}

func removeSocket(socket string) error {
	info, err := os.Lstat(socket)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil
	}
	return os.Remove(socket)
}

func (d *Daemon) handle(conn net.Conn) {
	defer conn.Close()
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(errorResponse("BAD_REQUEST", err.Error(), ""))
		return
	}
	res := d.dispatch(req)
	_ = json.NewEncoder(conn).Encode(res)
}

func (d *Daemon) dispatch(req Request) Response {
	if req.Action == "shutdown" {
		d.shutdownOnce.Do(func() {
			close(d.shutdown)
		})
		return Response{OK: true, Message: "daemon stopping"}
	}
	validated, err := d.validateRequest(req)
	if err != nil {
		return errorResponse("RUN_ROOT_INVALID", err.Error(), "Use a Workyard-managed run under ~/.workyard/runs/<project>/<run>")
	}
	req = validated
	if req.Action == "ping" {
		return Response{OK: true, Message: "pong"}
	}
	switch req.Action {
	case "setup":
		return d.setup(req)
	case "build":
		return d.build(req)
	case "start":
		return d.start(req)
	case "stop":
		return d.stop(req)
	case "restart":
		stop := d.stop(req)
		if !stop.OK {
			return stop
		}
		return d.start(req)
	case "status":
		return d.status(req)
	case "logs":
		return d.logs(req)
	case "events":
		return d.events(req)
	case "inspect":
		return d.inspect(req)
	case "wait":
		return d.wait(req)
	case "urls":
		return d.urls(req)
	case "probe":
		return d.probe(req)
	default:
		return errorResponse("UNKNOWN_ACTION", fmt.Sprintf("unknown daemon action %q", req.Action), "")
	}
}

func errorResponse(code, message, hint string) Response {
	return Response{OK: false, Error: &Error{Code: code, Message: message, Hint: hint}}
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".workyard"
	}
	return filepath.Join(home, ".workyard")
}
