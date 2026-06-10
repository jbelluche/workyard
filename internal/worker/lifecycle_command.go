package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackbelluche/workyard/internal/command"
	"github.com/jackbelluche/workyard/internal/config"
)

type lifecycleRun struct {
	Name        string
	Service     string
	Command     config.LifecycleCommand
	Cwd         string
	Env         []string
	Timeout     time.Duration
	StdoutRel   string
	StderrRel   string
	EventPrefix string
}

func runLifecycleCommand(runRoot string, run lifecycleRun) error {
	if run.Command.Command == "" {
		return nil
	}
	if run.Timeout <= 0 {
		run.Timeout = run.Command.Timeout
	}
	if run.Timeout <= 0 {
		run.Timeout = 2 * time.Minute
	}
	if run.Timeout > 30*time.Minute {
		appendEvent(runRoot, Event{Type: "lifecycle.timeout_capped", Service: run.Service, Message: fmt.Sprintf("%s timeout %s capped to the 30m maximum", run.Name, run.Timeout)})
		run.Timeout = 30 * time.Minute
	}
	if run.Cwd == "" {
		run.Cwd = sourceRoot(runRoot)
	}
	if run.StdoutRel == "" || run.StderrRel == "" {
		name := run.Name
		if run.Service != "" {
			name = run.Service + "." + run.Name
		}
		run.StdoutRel = filepath.ToSlash(filepath.Join("logs", name+".stdout.log"))
		run.StderrRel = filepath.ToSlash(filepath.Join("logs", name+".stderr.log"))
	}
	argv, err := command.Parse(run.Command.Command, run.Command.Shell)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(logsDir(runRoot), 0o700); err != nil {
		return err
	}
	stdout, err := os.OpenFile(filepath.Join(runRoot, run.StdoutRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(filepath.Join(runRoot, run.StderrRel), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer stderr.Close()

	prefix := run.EventPrefix
	if prefix == "" {
		prefix = "lifecycle." + run.Name
	}
	appendEvent(runRoot, Event{Type: prefix + ".start", Service: run.Service, Message: run.Name + " started"})
	ctx, cancel := context.WithTimeout(context.Background(), run.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = run.Cwd
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = run.Env
	if err := cmd.Start(); err != nil {
		appendEvent(runRoot, Event{Type: prefix + ".failed", Service: run.Service, Message: err.Error()})
		return err
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		msg := fmt.Sprintf("%s timed out after %s", run.Name, run.Timeout)
		appendEvent(runRoot, Event{Type: prefix + ".timeout", Service: run.Service, Message: msg})
		return errors.New(msg)
	}
	if err != nil {
		msg := fmt.Sprintf("%s failed: %s", run.Name, err)
		appendEvent(runRoot, Event{Type: prefix + ".failed", Service: run.Service, Message: msg})
		return errors.New(msg)
	}
	appendEvent(runRoot, Event{Type: prefix + ".ok", Service: run.Service, Message: run.Name + " completed"})
	return nil
}

func projectLifecycleEnv() []string {
	env := minimalEnv()
	env = appendOrReplaceEnv(env, "WORKYARD", "1")
	return env
}

func serviceLifecycleEnv(svc config.Service, assigned int) []string {
	return serviceEnv(svc, assigned)
}
