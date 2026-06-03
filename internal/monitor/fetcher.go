package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/jackbelluche/workyard/internal/worker"
)

type DefaultFetcher struct {
	StateDir        string
	Socket          string
	AutoStartDaemon bool
}

func (f DefaultFetcher) Fetch(ctx context.Context, ref registry.RunRef) (runData, error) {
	if isLocalWorker(ref.Worker) {
		return f.fetchLocal(ctx, ref)
	}
	return f.fetchRemote(ctx, ref)
}

func (f DefaultFetcher) fetchRemote(ctx context.Context, ref registry.RunRef) (runData, error) {
	home, err := remote.Home(ctx, ref.Worker)
	if err != nil {
		return runData{}, err
	}
	paths, err := remote.BuildPaths(home, ref.RemoteRoot, ref.Project, ref.RunID)
	if err != nil {
		return runData{}, err
	}
	if f.AutoStartDaemon {
		if err := remote.EnsureDaemon(ctx, ref.Worker, paths, ref.RemoteBinary); err != nil {
			return runData{}, err
		}
	}
	status, err := f.remoteCall(ctx, ref, paths, "status", nil)
	if err != nil {
		return runData{}, err
	}
	var res worker.Response
	if err := json.Unmarshal([]byte(status), &res); err != nil {
		return runData{}, fmt.Errorf("decode status response: %w", err)
	}
	if !res.OK {
		return runData{}, responseError(res)
	}
	eventsOut, err := f.remoteCall(ctx, ref, paths, "events", []string{"--tail", "80", "--max-bytes", "65536"})
	if err != nil {
		return runData{Response: res}, nil
	}
	events, err := parseEventsJSONL(eventsOut)
	if err != nil {
		return runData{Response: res}, nil
	}
	return runData{Response: res, Events: events}, nil
}

func (f DefaultFetcher) remoteCall(ctx context.Context, ref registry.RunRef, paths remote.Paths, action string, extra []string) (string, error) {
	binary := paths.Binary
	if ref.RemoteBinary != "" {
		binary = ref.RemoteBinary
	}
	argv := []string{
		binary,
		"daemonctl",
		action,
		"--socket", paths.Socket,
		"--run-root", paths.RunRoot,
		"--project-name", paths.Project,
		"--run-id", paths.RunID,
		"--worker-name", ref.Worker,
		"--json",
	}
	argv = append(argv, extra...)
	res, err := remote.Run(ctx, ref.Worker, argv, nil, 20*time.Second)
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

func (f DefaultFetcher) fetchLocal(ctx context.Context, ref registry.RunRef) (runData, error) {
	socket := f.Socket
	if socket == "" {
		socket = filepath.Join(defaultStateDir(f.StateDir), "daemon", "workyard.sock")
	}
	runRoot := ref.RemoteRunPath
	if runRoot == "" {
		return runData{}, fmt.Errorf("local run root is required")
	}
	res, err := worker.Call(socket, worker.Request{
		Action:  "status",
		RunRoot: runRoot,
		Project: ref.Project,
		RunID:   ref.RunID,
		Worker:  ref.Worker,
	})
	if err != nil {
		return runData{}, err
	}
	eventsRes, err := worker.Call(socket, worker.Request{
		Action:   "events",
		RunRoot:  runRoot,
		Project:  ref.Project,
		RunID:    ref.RunID,
		Worker:   ref.Worker,
		Tail:     80,
		MaxBytes: 65536,
	})
	if err != nil {
		return runData{Response: res}, nil
	}
	select {
	case <-ctx.Done():
		return runData{}, ctx.Err()
	default:
	}
	return runData{Response: res, Events: eventsRes.Events}, nil
}

func parseEventsJSONL(raw string) ([]worker.Event, error) {
	var events []worker.Event
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev worker.Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.Type != "" {
			events = append(events, ev)
			continue
		}
		var res worker.Response
		if err := json.Unmarshal([]byte(line), &res); err == nil && !res.OK {
			return nil, responseError(res)
		}
	}
	return events, scanner.Err()
}

func responseError(res worker.Response) error {
	if res.Error != nil {
		return fmt.Errorf("%s: %s", res.Error.Code, res.Error.Message)
	}
	return fmt.Errorf("daemon request failed")
}

func isLocalWorker(name string) bool {
	name = strings.TrimSpace(strings.ToLower(name))
	return name == "" || name == "localhost" || name == "127.0.0.1" || name == "::1"
}

func defaultStateDir(stateDir string) string {
	if stateDir != "" {
		return stateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".workyard")
}
