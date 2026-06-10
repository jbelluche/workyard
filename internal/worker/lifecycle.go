package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackbelluche/workyard/internal/command"
	"github.com/jackbelluche/workyard/internal/config"
)

func (d *Daemon) start(req Request) Response {
	loaded, err := config.Load(sourceRoot(req.RunRoot))
	if err != nil {
		return errorResponse("CONFIG_LOAD_FAILED", err.Error(), "Run workyard sync again and confirm workyard.yaml exists in the synced source")
	}
	names, err := selectServices(loaded.Config, req.Services, false)
	if err != nil {
		return errorResponse("SERVICE_SELECTION_FAILED", err.Error(), "")
	}
	rng, err := parsePortRange(loaded.Config.Worker.PortRange)
	if err != nil {
		return errorResponse("PORT_RANGE_INVALID", err.Error(), "")
	}
	for _, name := range names {
		svc := loaded.Config.Services[name]
		d.mu.Lock()
		st, err := loadState(req.RunRoot, loaded.Config.Name, req.RunID, workerName(req.Worker))
		if err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
		}
		if existing, ok := st.Services[name]; ok && existing.PID > 0 && processIdentityMatches(existing.Process) && isRunningStatus(existing.Status) {
			st.Services[name] = refreshURL(existing, st.Worker)
			_ = saveState(req.RunRoot, st)
			d.mu.Unlock()
			continue
		}
		assigned := 0
		if svc.Port.Default > 0 {
			assigned, err = allocatePort(st, name, svc.Port.Default, rng)
			if err != nil {
				d.mu.Unlock()
				return errorResponse("PORT_ALLOCATE_FAILED", err.Error(), "")
			}
		}
		cwd, err := config.ServicePath(sourceRoot(req.RunRoot), svc)
		if err != nil {
			d.mu.Unlock()
			return errorResponse("SERVICE_PATH_INVALID", err.Error(), "")
		}
		preparing := ServiceState{
			Name:           name,
			Status:         "preparing",
			Healthy:        false,
			StartCommand:   svc.StartCommand,
			Cwd:            cwd,
			ConfiguredPort: svc.Port.Default,
			AssignedPort:   assigned,
			PortEnv:        svc.Port.Env,
			HealthURL:      runtimeHealthURL(svc.Health.URL, svc.Port.Default, assigned),
			Logs:           serviceLogPaths(name),
		}
		preparing = refreshURL(preparing, st.Worker)
		st.Services[name] = preparing
		if err := saveState(req.RunRoot, st); err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
		}
		d.mu.Unlock()

		if svc.BeforeStart != nil {
			if err := runLifecycleCommand(req.RunRoot, lifecycleRun{
				Name:        "beforeStart",
				Service:     name,
				Command:     *svc.BeforeStart,
				Cwd:         cwd,
				Env:         serviceLifecycleEnv(svc, assigned),
				EventPrefix: "service.beforeStart",
			}); err != nil {
				d.mu.Lock()
				st, loadErr := loadState(req.RunRoot, loaded.Config.Name, req.RunID, workerName(req.Worker))
				if loadErr == nil {
					failed := st.Services[name]
					failed.Status = "failed"
					failed.Healthy = false
					st.Services[name] = failed
					_ = saveState(req.RunRoot, st)
				}
				d.mu.Unlock()
				return errorResponse("BEFORE_START_FAILED", err.Error(), "Run workyard logs "+name+" --tail 200")
			}
		}

		d.mu.Lock()
		st, err = loadState(req.RunRoot, loaded.Config.Name, req.RunID, workerName(req.Worker))
		if err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
		}
		state, err := d.startService(req.RunRoot, st, name, svc, assigned)
		if err != nil {
			d.mu.Unlock()
			appendEvent(req.RunRoot, Event{Type: "service.start_failed", Service: name, Message: err.Error()})
			return errorResponse("SERVICE_START_FAILED", fmt.Sprintf("%s: %s", name, err), "Run workyard logs "+name+" --tail 200")
		}
		st.Services[name] = state
		if err := saveState(req.RunRoot, st); err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
		}
		d.mu.Unlock()

		healthState := waitInitialHealth(state, startHealthTimeout(svc.Health.Timeout, req.Timeout))
		appendEvent(req.RunRoot, healthEvent(healthState))

		d.mu.Lock()
		st, err = loadState(req.RunRoot, loaded.Config.Name, req.RunID, workerName(req.Worker))
		if err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
		}
		current := st.Services[name]
		if current.Process == state.Process {
			current.Status = healthState.Status
			current.Healthy = healthState.Healthy
			current.PID = healthState.PID
			current.Process = healthState.Process
			current.StoppedAt = healthState.StoppedAt
			st.Services[name] = current
		}
		if err := saveState(req.RunRoot, st); err != nil {
			d.mu.Unlock()
			return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
		}
		d.mu.Unlock()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := loadState(req.RunRoot, loaded.Config.Name, req.RunID, workerName(req.Worker))
	if err != nil {
		return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
	}
	if err := saveState(req.RunRoot, st); err != nil {
		return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
	}
	return Response{OK: true, Project: st.Project, RunID: st.RunID, Worker: st.Worker, Services: sortedStates(st)}
}

func (d *Daemon) startService(runRoot string, st RunState, name string, svc config.Service, assigned int) (ServiceState, error) {
	source := sourceRoot(runRoot)
	cwd, err := config.ServicePath(source, svc)
	if err != nil {
		return ServiceState{}, err
	}
	argv, err := command.Parse(svc.StartCommand, svc.Shell)
	if err != nil {
		return ServiceState{}, err
	}
	argv = applyRuntimeArgs(argv, assigned)
	if err := os.MkdirAll(logsDir(runRoot), 0o700); err != nil {
		return ServiceState{}, err
	}
	logPaths := serviceLogPaths(name)
	stdoutRel := logPaths.Stdout
	stderrRel := logPaths.Stderr
	stdout, err := os.OpenFile(filepath.Join(runRoot, stdoutRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return ServiceState{}, err
	}
	stderr, err := os.OpenFile(filepath.Join(runRoot, stderrRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		_ = stdout.Close()
		return ServiceState{}, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = serviceEnv(svc, assigned)
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		_ = stderr.Close()
		return ServiceState{}, err
	}
	state := ServiceState{
		Name:           name,
		Status:         "starting",
		PID:            cmd.Process.Pid,
		Process:        currentProcessID(cmd.Process.Pid),
		StartedAt:      time.Now().UTC(),
		StartCommand:   svc.StartCommand,
		Cwd:            cwd,
		ConfiguredPort: svc.Port.Default,
		AssignedPort:   assigned,
		PortEnv:        svc.Port.Env,
		HealthURL:      runtimeHealthURL(svc.Health.URL, svc.Port.Default, assigned),
		Logs:           logPaths,
	}
	state = refreshURL(state, st.Worker)
	d.processes[serviceKey(runRoot, name)] = cmd.Process
	appendEvent(runRoot, Event{Type: "service.start", Service: name, Message: "started " + name, PID: cmd.Process.Pid})
	go d.waitForExit(runRoot, name, cmd, stdout, stderr)
	return state, nil
}

func (d *Daemon) waitForExit(runRoot, name string, cmd *exec.Cmd, stdout, stderr *os.File) {
	err := cmd.Wait()
	_ = stdout.Close()
	_ = stderr.Close()
	exitCode := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = 1
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st, loadErr := loadState(runRoot, "", "", "")
	if loadErr == nil {
		svc := st.Services[name]
		if svc.Status != "stopped" {
			svc.Status = "exited"
			svc.ExitCode = &exitCode
		}
		svc.Healthy = false
		svc.PID = 0
		svc.Process = ProcessID{}
		if svc.StoppedAt.IsZero() {
			svc.StoppedAt = time.Now().UTC()
		}
		st.Services[name] = svc
		_ = saveState(runRoot, st)
	}
	delete(d.processes, serviceKey(runRoot, name))
	appendEvent(runRoot, Event{Type: "service.exit", Service: name, Message: fmt.Sprintf("%s exited with code %d", name, exitCode), ExitCode: &exitCode})
}

func (d *Daemon) stop(req Request) Response {
	loaded, loadConfigErr := config.Load(sourceRoot(req.RunRoot))
	d.mu.Lock()

	st, err := loadState(req.RunRoot, req.Project, req.RunID, workerName(req.Worker))
	if err != nil {
		d.mu.Unlock()
		return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
	}
	names, err := selectStateServices(st, req.Services, req.All)
	if err != nil {
		d.mu.Unlock()
		return errorResponse("SERVICE_SELECTION_FAILED", err.Error(), "")
	}
	stoppedStates := map[string]ServiceState{}
	for _, name := range names {
		svc := st.Services[name]
		if svc.PID > 0 && processIdentityMatches(svc.Process) {
			appendEvent(req.RunRoot, Event{Type: "service.stop", Service: name, Message: "stopping " + name, PID: svc.PID})
			terminateProcessGroup(svc.PID, 5*time.Second)
		} else if svc.PID > 0 && isRunningStatus(svc.Status) {
			appendEvent(req.RunRoot, Event{Type: "service.stale_pid", Service: name, Message: "refusing to signal unverified process identity", PID: svc.PID})
		}
		svc.Status = "stopped"
		svc.Healthy = false
		svc.PID = 0
		svc.Process = ProcessID{}
		svc.StoppedAt = time.Now().UTC()
		st.Services[name] = svc
		stoppedStates[name] = svc
		delete(d.processes, serviceKey(req.RunRoot, name))
	}
	if err := saveState(req.RunRoot, st); err != nil {
		d.mu.Unlock()
		return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
	}
	result := Response{OK: true, Project: st.Project, RunID: st.RunID, Worker: st.Worker, Services: sortedStates(st)}
	d.mu.Unlock()

	if loadConfigErr == nil {
		for _, name := range names {
			serviceConfig, ok := loaded.Config.Services[name]
			if !ok || serviceConfig.OnClose == nil {
				continue
			}
			serviceState := stoppedStates[name]
			cwd := serviceState.Cwd
			if cwd == "" {
				cwd, _ = config.ServicePath(sourceRoot(req.RunRoot), serviceConfig)
			}
			if err := runLifecycleCommand(req.RunRoot, lifecycleRun{
				Name:        "onClose",
				Service:     name,
				Command:     *serviceConfig.OnClose,
				Cwd:         cwd,
				Env:         serviceLifecycleEnv(serviceConfig, serviceState.AssignedPort),
				EventPrefix: "service.onClose",
			}); err != nil {
				appendEvent(req.RunRoot, Event{Type: "service.onClose.best_effort_failed", Service: name, Message: err.Error()})
			}
		}
	}
	return result
}

func (d *Daemon) status(req Request) Response {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, err := loadState(req.RunRoot, req.Project, req.RunID, workerName(req.Worker))
	if err != nil {
		return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
	}
	st = refreshState(st)
	if err := saveState(req.RunRoot, st); err != nil {
		return errorResponse("STATE_SAVE_FAILED", err.Error(), "")
	}
	return Response{OK: true, Project: st.Project, RunID: st.RunID, Worker: st.Worker, Services: sortedStates(st)}
}

func refreshState(st RunState) RunState {
	for name, svc := range st.Services {
		if svc.PID > 0 && !processIdentityMatches(svc.Process) && isRunningStatus(svc.Status) {
			svc.Status = "exited"
			svc.Healthy = false
			svc.PID = 0
			svc.Process = ProcessID{}
			svc.StoppedAt = time.Now().UTC()
		}
		if svc.PID > 0 && processIdentityMatches(svc.Process) && svc.HealthURL != "" {
			if healthOK(svc.HealthURL, 1200*time.Millisecond) {
				svc.Status = "healthy"
				svc.Healthy = true
			} else if svc.Status == "healthy" {
				svc.Status = "unhealthy"
				svc.Healthy = false
			}
		}
		st.Services[name] = refreshURL(svc, st.Worker)
	}
	return st
}

func selectServices(cfg config.Config, requested []string, allowAll bool) ([]string, error) {
	if len(requested) == 0 || allowAll {
		return config.ServiceNames(cfg.Services), nil
	}
	var out []string
	for _, name := range requested {
		if _, ok := cfg.Services[name]; !ok {
			return nil, fmt.Errorf("unknown service %q (configured services: %s)", name, strings.Join(config.ServiceNames(cfg.Services), ", "))
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func selectStateServices(st RunState, requested []string, all bool) ([]string, error) {
	if all || len(requested) == 0 {
		names := make([]string, 0, len(st.Services))
		for name := range st.Services {
			names = append(names, name)
		}
		sort.Strings(names)
		return names, nil
	}
	var out []string
	for _, name := range requested {
		if _, ok := st.Services[name]; !ok {
			return nil, fmt.Errorf("service %q has no state", name)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func serviceEnv(svc config.Service, assigned int) []string {
	env := minimalEnv()
	for key, value := range svc.Env {
		env = appendOrReplaceEnv(env, key, value)
	}
	if assigned > 0 {
		portValue := strconv.Itoa(assigned)
		if svc.Port.Env != "" {
			env = appendOrReplaceEnv(env, svc.Port.Env, portValue)
		}
		env = appendOrReplaceEnv(env, "WORKYARD_PORT", portValue)
	}
	env = appendOrReplaceEnv(env, "WORKYARD", "1")
	return env
}

func minimalEnv() []string {
	var env []string
	for _, key := range []string{"HOME", "USER", "LOGNAME", "PATH", "SHELL", "TMPDIR", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	if !envContains(env, "PATH") {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
	}
	return env
}

func envContains(env []string, key string) bool {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}
	return false
}

func appendOrReplaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func applyRuntimeArgs(argv []string, assigned int) []string {
	if assigned <= 0 {
		return argv
	}
	port := strconv.Itoa(assigned)
	out := make([]string, len(argv))
	for i, arg := range argv {
		arg = strings.ReplaceAll(arg, "${WORKYARD_PORT}", port)
		arg = strings.ReplaceAll(arg, "${PORT}", port)
		out[i] = arg
	}
	return out
}

func runtimeHealthURL(raw string, configured, assigned int) string {
	if raw == "" || configured == 0 || assigned == 0 || configured == assigned {
		return raw
	}
	return strings.Replace(raw, ":"+strconv.Itoa(configured), ":"+strconv.Itoa(assigned), 1)
}

func waitInitialHealth(state ServiceState, timeout time.Duration) ServiceState {
	if state.HealthURL == "" {
		time.Sleep(400 * time.Millisecond)
		if processIdentityMatches(state.Process) {
			state.Status = "running"
			state.Healthy = true
		} else {
			state.Status = "exited"
			state.Healthy = false
		}
		return state
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processIdentityMatches(state.Process) {
			state.Status = "exited"
			state.Healthy = false
			return state
		}
		if healthOK(state.HealthURL, 1500*time.Millisecond) {
			state.Status = "healthy"
			state.Healthy = true
			return state
		}
		time.Sleep(300 * time.Millisecond)
	}
	state.Status = "unhealthy"
	state.Healthy = false
	return state
}

func startHealthTimeout(configured time.Duration, raw string) time.Duration {
	if raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return configured
}

func healthOK(raw string, timeout time.Duration) bool {
	code, err := probeURL(raw, timeout)
	return err == nil && code >= 200 && code < 300
}

func healthEvent(state ServiceState) Event {
	switch state.Status {
	case "healthy":
		return Event{Type: "health.ok", Service: state.Name, Message: "health check passed"}
	case "unhealthy":
		return Event{Type: "health.failed", Service: state.Name, Message: "health check failed"}
	default:
		return Event{Type: "service." + state.Status, Service: state.Name, Message: state.Status}
	}
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func terminateProcessGroup(pid int, grace time.Duration) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

func refreshURL(state ServiceState, workerName string) ServiceState {
	if state.AssignedPort > 0 {
		host := urlHost(workerName)
		if host == "" {
			host = "localhost"
		}
		state.URL = fmt.Sprintf("http://%s:%d", host, state.AssignedPort)
	}
	return state
}

func workerName(value string) string {
	if value != "" {
		return value
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "localhost"
	}
	return host
}

func urlHost(value string) string {
	if strings.Contains(value, "@") {
		parts := strings.Split(value, "@")
		return parts[len(parts)-1]
	}
	return value
}
