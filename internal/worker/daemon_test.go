package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestManagedSocketPathRejectsOutsideStateDir(t *testing.T) {
	stateDir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "workyard.sock")
	if _, err := managedSocketPath(stateDir, outside); err == nil {
		t.Fatal("expected socket outside state dir to be rejected")
	}
}

func TestRemoveStaleSocketRejectsRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workyard.sock")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeStaleSocket(path); err == nil {
		t.Fatal("expected regular file removal to be rejected")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected regular file to remain: %v", err)
	}
}

func TestAcquireDaemonLockRejectsSecondLock(t *testing.T) {
	stateDir := t.TempDir()
	first, err := acquireDaemonLock(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseDaemonLock(first)
	second, err := acquireDaemonLock(stateDir)
	if err == nil {
		releaseDaemonLock(second)
		t.Fatal("expected second daemon lock to be rejected")
	}
}

func TestDaemonRecoversRunningServicesFromState(t *testing.T) {
	stateDir := t.TempDir()
	runRoot := filepath.Join(stateDir, "runs", "fixture", "run-1")
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := startSleepProcess(t)
	defer cleanupProcess(cmd)

	st := RunState{
		Project:  "fixture",
		RunID:    "run-1",
		Worker:   "localhost",
		Services: map[string]ServiceState{"web": runningTestState("web", cmd.Process.Pid)},
	}
	if err := saveState(runRoot, st); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{opts: DaemonOptions{StateDir: stateDir}, processes: map[string]*os.Process{}}
	if err := d.recoverServices(); err != nil {
		t.Fatal(err)
	}
	recovered, err := loadState(runRoot, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	web := recovered.Services["web"]
	if web.PID != cmd.Process.Pid || web.Status != "running" || !web.Healthy {
		t.Fatalf("unexpected recovered service: %#v", web)
	}
	if _, ok := d.processes[serviceKey(runRoot, "web")]; !ok {
		t.Fatal("expected daemon to remember recovered process")
	}
}

func TestDaemonShutdownStopsRecoveredServices(t *testing.T) {
	stateDir := t.TempDir()
	runRoot := filepath.Join(stateDir, "runs", "fixture", "run-1")
	if err := os.MkdirAll(runRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := startSleepProcess(t)
	defer cleanupProcess(cmd)
	if err := saveState(runRoot, RunState{
		Project:  "fixture",
		RunID:    "run-1",
		Worker:   "localhost",
		Services: map[string]ServiceState{"web": runningTestState("web", cmd.Process.Pid)},
	}); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{opts: DaemonOptions{StateDir: stateDir}, processes: map[string]*os.Process{}}
	d.shutdownServices(100 * time.Millisecond)
	_ = cmd.Wait()

	st, err := loadState(runRoot, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	web := st.Services["web"]
	if web.Status != "stopped" || web.PID != 0 || web.Healthy {
		t.Fatalf("unexpected stopped service state: %#v", web)
	}
}

func TestDaemonDispatchShutdownClosesChannel(t *testing.T) {
	d := &Daemon{shutdown: make(chan struct{})}
	res := d.dispatch(Request{Action: "shutdown"})
	if !res.OK {
		t.Fatalf("expected shutdown response ok: %#v", res)
	}
	select {
	case <-d.shutdown:
	case <-time.After(time.Second):
		t.Fatal("expected shutdown channel to close")
	}
	res = d.dispatch(Request{Action: "shutdown"})
	if !res.OK {
		t.Fatalf("expected repeated shutdown response ok: %#v", res)
	}
}

func TestServeShutdownReturnsNil(t *testing.T) {
	stateDir, err := os.MkdirTemp("/tmp", "workyard-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stateDir)
	socket, err := managedSocketPath(stateDir, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, DaemonOptions{StateDir: stateDir, AllowRoot: true})
	}()

	deadline := time.Now().Add(2 * time.Second)
	var lastErr error
	for {
		if _, err := Call(socket, Request{Action: "ping"}); err == nil {
			break
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			select {
			case err := <-errCh:
				t.Fatalf("daemon exited before becoming ready: %v", err)
			default:
			}
			t.Fatalf("daemon did not become ready: %v", lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := Call(socket, Request{Action: "shutdown"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("expected shutdown response ok: %#v", res)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned error on shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func startSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func runningTestState(name string, pid int) ServiceState {
	return ServiceState{
		Name:      name,
		Status:    "running",
		Healthy:   true,
		PID:       pid,
		Process:   currentProcessID(pid),
		StartedAt: time.Now().UTC(),
		Logs:      serviceLogPaths(name),
	}
}

func cleanupProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}
