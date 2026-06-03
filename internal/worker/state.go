package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func statePath(runRoot string) string {
	return filepath.Join(runRoot, "state.json")
}

func sourceRoot(runRoot string) string {
	return filepath.Join(runRoot, "source")
}

func logsDir(runRoot string) string {
	return filepath.Join(runRoot, "logs")
}

func serviceLogPaths(name string) LogPaths {
	return LogPaths{
		Stdout: filepath.ToSlash(filepath.Join("logs", name+".stdout.log")),
		Stderr: filepath.ToSlash(filepath.Join("logs", name+".stderr.log")),
		Events: filepath.ToSlash(filepath.Join("logs", name+".events.jsonl")),
	}
}

func loadState(runRoot, project, runID, workerName string) (RunState, error) {
	st := RunState{Project: project, RunID: runID, Worker: workerName, Services: map[string]ServiceState{}}
	data, err := os.ReadFile(statePath(runRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if st.Project == "" {
		st.Project = project
	}
	if st.RunID == "" {
		st.RunID = runID
	}
	if workerName != "" {
		st.Worker = workerName
	} else if st.Worker == "" {
		st.Worker = workerName
	}
	if st.Services == nil {
		st.Services = map[string]ServiceState{}
	}
	return st, nil
}

func saveState(runRoot string, st RunState) error {
	st.UpdatedAt = time.Now().UTC()
	if err := os.MkdirAll(filepath.Dir(statePath(runRoot)), 0o700); err != nil {
		return err
	}
	tmp := statePath(runRoot) + ".tmp"
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, statePath(runRoot))
}

func sortedStates(st RunState) []ServiceState {
	names := make([]string, 0, len(st.Services))
	for name := range st.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ServiceState, 0, len(names))
	for _, name := range names {
		out = append(out, st.Services[name])
	}
	return out
}

func serviceKey(runRoot, service string) string {
	return strings.Join([]string{runRoot, service}, "\x00")
}
