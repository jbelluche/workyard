package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/remote"
)

var BuiltinExcludes = []string{
	".git",
	"node_modules",
	".next",
	".nuxt",
	".turbo",
	".vite",
	"dist",
	"build",
	"coverage",
	".cache",
	".DS_Store",
	"*.log",
	".env",
	".env.local",
	".env.*.local",
}

type Options struct {
	Worker     string
	RunID      string
	RemoteRoot string
	DryRun     bool
	Delete     bool
	Verbose    bool
}

type Result struct {
	OK               bool      `json:"ok"`
	Worker           string    `json:"worker"`
	Project          string    `json:"project"`
	RunID            string    `json:"runId"`
	RemoteRunPath    string    `json:"remoteRunPath"`
	RemoteSourcePath string    `json:"remoteSourcePath"`
	DryRun           bool      `json:"dryRun"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	Stats            Stats     `json:"stats"`
}

type Stats struct {
	FilesTransferred int   `json:"filesTransferred"`
	BytesTransferred int64 `json:"bytesTransferred"`
}

type Metadata struct {
	Project         string    `json:"project"`
	RunID           string    `json:"runId"`
	SourcePath      string    `json:"sourcePath"`
	SyncedAt        time.Time `json:"syncedAt"`
	LocalRoot       string    `json:"localRoot"`
	GitBranch       string    `json:"gitBranch,omitempty"`
	GitCommit       string    `json:"gitCommit,omitempty"`
	Dirty           bool      `json:"dirty"`
	WorkyardVersion string    `json:"workyardVersion"`
}

func Run(ctx context.Context, loaded config.Loaded, opts Options, version string) (Result, error) {
	if err := remote.ValidateWorker(opts.Worker); err != nil {
		return Result{}, err
	}
	if opts.RunID == "" {
		return Result{}, fmt.Errorf("--run is required")
	}
	started := time.Now().UTC()
	home, err := remote.Home(ctx, opts.Worker)
	if err != nil {
		return Result{}, err
	}
	paths, err := remote.BuildPaths(home, opts.RemoteRoot, loaded.Config.Name, opts.RunID)
	if err != nil {
		return Result{}, err
	}
	if err := remote.GuardDestination(paths.Source); err != nil {
		return Result{}, err
	}
	binDir := path.Join(home, ".workyard", "bin")
	if err := prepareRemoteLayout(ctx, opts.Worker, paths, binDir); err != nil {
		return Result{}, err
	}
	excludeFile, err := writeExcludeFile(loaded.Config)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(excludeFile)

	args := []string{"-az", "--stats", "--exclude-from", excludeFile}
	if opts.Delete {
		args = append(args, "--delete", "--delete-excluded")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--", loaded.Config.Root+"/", opts.Worker+":"+paths.Source+"/")
	cmd := exec.CommandContext(ctx, "rsync", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("rsync failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !opts.DryRun {
		meta := Metadata{
			Project:         paths.Project,
			RunID:           paths.RunID,
			SourcePath:      paths.Source,
			SyncedAt:        time.Now().UTC(),
			LocalRoot:       loaded.Config.Root,
			GitBranch:       gitOutput(loaded.Config.Root, "rev-parse", "--abbrev-ref", "HEAD"),
			GitCommit:       gitOutput(loaded.Config.Root, "rev-parse", "HEAD"),
			Dirty:           gitDirty(loaded.Config.Root),
			WorkyardVersion: version,
		}
		data, _ := json.MarshalIndent(meta, "", "  ")
		data = append(data, '\n')
		if _, err := remote.Run(ctx, opts.Worker, []string{"sh", "-lc", "cat > " + remote.ShellQuote(paths.Sync)}, data, 20*time.Second); err != nil {
			return Result{}, err
		}
	}
	return Result{
		OK:               true,
		Worker:           opts.Worker,
		Project:          paths.Project,
		RunID:            paths.RunID,
		RemoteRunPath:    paths.RunRoot,
		RemoteSourcePath: paths.Source,
		DryRun:           opts.DryRun,
		StartedAt:        started,
		FinishedAt:       time.Now().UTC(),
		Stats:            parseStats(stdout.String()),
	}, nil
}

func prepareRemoteLayout(ctx context.Context, worker string, paths remote.Paths, binDir string) error {
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", remotePrepareScript(paths, binDir)}, nil, 20*time.Second)
	return err
}

func remotePrepareScript(paths remote.Paths, binDir string) string {
	workyardDir := path.Join(paths.Home, ".workyard")
	runsDir := path.Join(workyardDir, "runs")
	projectDir := path.Dir(paths.RunRoot)
	protected := []string{
		workyardDir,
		runsDir,
		projectDir,
		paths.RunRoot,
		paths.Source,
		paths.Logs,
		paths.DaemonDir,
		binDir,
	}
	create := []string{paths.Source, paths.Logs, paths.DaemonDir, binDir}
	return strings.Join([]string{
		"set -eu",
		"for p in " + shellList(protected) + "; do if [ -L \"$p\" ]; then printf 'refusing symlink path: %s\\n' \"$p\" >&2; exit 1; fi; done",
		"mkdir -p " + shellList(create),
		"chmod go-rwx " + shellList(protected),
		"for p in " + shellList(protected) + "; do if [ -L \"$p\" ]; then printf 'refusing symlink path: %s\\n' \"$p\" >&2; exit 1; fi; done",
	}, "\n")
}

func shellList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, remote.ShellQuote(value))
	}
	return strings.Join(quoted, " ")
}

func writeExcludeFile(cfg config.Config) (string, error) {
	f, err := os.CreateTemp("", "workyard-excludes-*.txt")
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, item := range append(BuiltinExcludes, cfg.Sync.Exclude...) {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
			continue
		}
		if cfg.Sync.IncludeEnvFiles && isEnvExclude(item) {
			continue
		}
		seen[item] = true
		if _, err := fmt.Fprintln(f, item); err != nil {
			_ = f.Close()
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func isEnvExclude(item string) bool {
	item = strings.TrimSpace(item)
	return item == ".env" || strings.HasPrefix(item, ".env.")
}

func parseStats(out string) Stats {
	var stats Stats
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Number of regular files transferred:") {
			stats.FilesTransferred = parseIntAfterColon(line)
		}
		if strings.HasPrefix(line, "Total transferred file size:") {
			stats.BytesTransferred = int64(parseIntAfterColon(line))
		}
	}
	return stats
}

func parseIntAfterColon(line string) int {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return 0
	}
	value := strings.TrimSpace(line[idx+1:])
	value = strings.TrimSuffix(value, " bytes")
	value = strings.ReplaceAll(value, ",", "")
	out, _ := strconv.Atoi(value)
	return out
}

func gitOutput(root string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitDirty(root string) bool {
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}
