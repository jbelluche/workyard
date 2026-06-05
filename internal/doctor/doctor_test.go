package doctor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	paths map[string]string
	runs  map[string]fakeRun
}

type fakeRun struct {
	result CommandResult
	err    error
}

func (f fakeRunner) LookPath(name string) (string, error) {
	path, ok := f.paths[name]
	if !ok {
		return "", errors.New("not found")
	}
	return path, nil
}

func (f fakeRunner) Run(ctx context.Context, name string, args []string, timeout time.Duration) (CommandResult, error) {
	run, ok := f.runs[name+" "+strings.Join(args, " ")]
	if !ok {
		if name == "ssh" && len(args) >= 4 {
			command := args[len(args)-1]
			switch {
			case command == `printf '%s' "$HOME"`:
				return CommandResult{Stdout: "/home/jack"}, nil
			case strings.Contains(command, "/home/jack/.workyard/bin/workyard version --json"):
				return CommandResult{Stdout: `{"ok":true,"version":"test"}`}, nil
			case strings.Contains(command, "/home/jack/.workyard/bin/workyard daemonctl ping"):
				return CommandResult{Stdout: `{"ok":true,"message":"pong"}`}, nil
			case strings.Contains(command, "stat -c %a"):
				return CommandResult{Stdout: "700\n"}, nil
			case strings.HasPrefix(command, "python3 - "):
				return CommandResult{Stdout: "3100\n"}, nil
			}
		}
		return CommandResult{}, errors.New("unexpected command: " + name + " " + strings.Join(args, " "))
	}
	return run.result, run.err
}

func TestRunPassesRequiredChecks(t *testing.T) {
	report := Run(context.Background(), Options{Version: "test", Worker: "jack@jack-rasp-five"}, passingRunner())
	if !report.OK {
		t.Fatalf("expected report to pass: %#v", report.Checks)
	}
	assertCheck(t, report, "rsync.installed", StatusPass)
	assertCheck(t, report, "ssh.installed", StatusPass)
	assertCheck(t, report, "tailscale.installed", StatusPass)
	assertCheck(t, report, "tailscale.connected", StatusPass)
	assertCheck(t, report, "worker.ssh", StatusPass)
	assertCheck(t, report, "worker.binary", StatusPass)
	assertCheck(t, report, "worker.daemon", StatusPass)
	assertCheck(t, report, "worker.runRoot", StatusPass)
	assertCheck(t, report, "worker.ports", StatusPass)
}

func TestRunFailsWhenRsyncMissing(t *testing.T) {
	runner := passingRunner()
	delete(runner.paths, "rsync")
	report := Run(context.Background(), Options{Version: "test"}, runner)
	if report.OK {
		t.Fatalf("expected missing rsync to fail required checks")
	}
	assertCheck(t, report, "rsync.installed", StatusFail)
}

func TestRunFailsWhenTailscaleDisconnected(t *testing.T) {
	runner := passingRunner()
	runner.runs["tailscale status --json"] = fakeRun{result: CommandResult{Stdout: `{"BackendState":"Stopped","Self":{"Online":false}}`}}
	report := Run(context.Background(), Options{Version: "test"}, runner)
	if report.OK {
		t.Fatalf("expected disconnected tailscale to fail required checks")
	}
	assertCheck(t, report, "tailscale.connected", StatusFail)
}

func TestProjectConfigWarningDoesNotFailRequiredChecks(t *testing.T) {
	report := Run(context.Background(), Options{Project: t.TempDir(), Version: "test", CheckProject: true}, passingRunner())
	if !report.OK {
		t.Fatalf("expected project warning not to fail required checks: %#v", report.Checks)
	}
	assertCheck(t, report, "workyard.config", StatusWarn)
}

func TestWorkerSSHRejectsInvalidTarget(t *testing.T) {
	report := Run(context.Background(), Options{Version: "test", Worker: "-bad"}, passingRunner())
	if report.OK {
		t.Fatalf("expected invalid worker to fail required checks")
	}
	assertCheck(t, report, "worker.ssh", StatusFail)
}

func TestRunChecksLocalWorkerWithoutSSH(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	report := Run(context.Background(), Options{Version: "test", Worker: "localhost"}, passingRunner())
	if !report.OK {
		t.Fatalf("expected local worker report to pass required checks: %#v", report.Checks)
	}
	assertCheck(t, report, "worker.local", StatusPass)
	assertCheck(t, report, "worker.runRoot", StatusPass)
	assertCheck(t, report, "worker.ports", StatusPass)
	assertCheck(t, report, "worker.daemon", StatusWarn)
	for _, check := range report.Checks {
		if check.Name == "worker.ssh" {
			t.Fatalf("local worker should not run SSH checks: %#v", report.Checks)
		}
	}
}

func passingRunner() fakeRunner {
	return fakeRunner{
		paths: map[string]string{
			"rsync":     "/usr/bin/rsync",
			"ssh":       "/usr/bin/ssh",
			"tailscale": "/usr/local/bin/tailscale",
		},
		runs: map[string]fakeRun{
			"tailscale status --json": {
				result: CommandResult{Stdout: `{"BackendState":"Running","Self":{"Online":true,"DNSName":"mac.tailnet.ts.net.","HostName":"mac","TailscaleIPs":["100.64.0.1"]}}`},
			},
			`ssh -o BatchMode=yes -- jack@jack-rasp-five printf '%s' "$HOME"`: {
				result: CommandResult{Stdout: "/home/jack"},
			},
		},
	}
}

func assertCheck(t *testing.T, report Report, name, status string) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			if check.Status != status {
				t.Fatalf("check %s status=%s, want %s; %#v", name, check.Status, status, check)
			}
			return
		}
	}
	t.Fatalf("check %s not found in %#v", name, report.Checks)
}
