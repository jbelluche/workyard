package remote

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
)

type CleanupResult struct {
	OK            bool   `json:"ok"`
	Worker        string `json:"worker"`
	RemoteRunPath string `json:"remoteRunPath"`
	RemoteLogPath string `json:"remoteLogPath,omitempty"`
	Action        string `json:"action"`
}

func CleanupLogs(ctx context.Context, worker string, paths Paths) (CleanupResult, error) {
	if err := validateManagedRun(paths); err != nil {
		return CleanupResult{}, err
	}
	script := strings.Join([]string{
		"set -eu",
		"run=" + ShellQuote(paths.RunRoot),
		"logs=" + ShellQuote(paths.Logs),
		"if [ -L \"$run\" ] || [ -L \"$logs\" ]; then printf 'refusing symlink cleanup path\\n' >&2; exit 1; fi",
		"mkdir -p \"$logs\"",
		"find \"$logs\" -type f \\( -name '*.log' -o -name '*.jsonl' \\) -exec sh -c ': > \"$1\"' sh {} \\;",
		"chmod go-rwx \"$logs\"",
	}, "\n")
	if _, err := Run(ctx, worker, []string{"sh", "-lc", script}, nil, 20*time.Second); err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{OK: true, Worker: worker, RemoteRunPath: paths.RunRoot, RemoteLogPath: paths.Logs, Action: "logs"}, nil
}

func CleanupRun(ctx context.Context, worker string, paths Paths) (CleanupResult, error) {
	if err := validateManagedRun(paths); err != nil {
		return CleanupResult{}, err
	}
	script := strings.Join([]string{
		"set -eu",
		"run=" + ShellQuote(paths.RunRoot),
		"if [ -L \"$run\" ]; then printf 'refusing symlink run path\\n' >&2; exit 1; fi",
		"rm -rf -- \"$run\"",
	}, "\n")
	if _, err := Run(ctx, worker, []string{"sh", "-lc", script}, nil, 30*time.Second); err != nil {
		return CleanupResult{}, err
	}
	return CleanupResult{OK: true, Worker: worker, RemoteRunPath: paths.RunRoot, Action: "run"}, nil
}

func validateManagedRun(paths Paths) error {
	if strings.TrimSpace(paths.Home) == "" || strings.TrimSpace(paths.RunRoot) == "" {
		return fmt.Errorf("remote paths are incomplete")
	}
	runsRoot := path.Join(paths.Home, ".workyard", "runs")
	if !isUnder(paths.RunRoot, runsRoot) {
		return fmt.Errorf("run path must stay under %s", runsRoot)
	}
	rel := strings.TrimPrefix(path.Clean(paths.RunRoot), path.Clean(runsRoot)+"/")
	parts := strings.Split(rel, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("run path must be exactly under %s/<project>/<run>", runsRoot)
	}
	if paths.Logs != path.Join(paths.RunRoot, "logs") {
		return fmt.Errorf("logs path must be inside the run root")
	}
	return nil
}
