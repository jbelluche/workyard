package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/remote"
)

const MarkerFileName = ".workyard-mirror.json"

var DefaultExcludes = []string{
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

type DestinationCheck struct {
	OK             bool    `json:"ok"`
	Worker         string  `json:"worker"`
	RemotePath     string  `json:"remotePath"`
	ResolvedPath   string  `json:"resolvedPath"`
	State          string  `json:"state"`
	Marker         *Marker `json:"marker,omitempty"`
	NonEmptyReason string  `json:"nonEmptyReason,omitempty"`
}

type Marker struct {
	Name            string    `json:"name"`
	LocalRoot       string    `json:"localRoot"`
	Worker          string    `json:"worker"`
	RemotePath      string    `json:"remotePath"`
	WorkyardVersion string    `json:"workyardVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SyncOptions struct {
	Version string
	DryRun  bool
}

type SyncResult struct {
	OK               bool      `json:"ok"`
	Name             string    `json:"name"`
	Worker           string    `json:"worker"`
	LocalRoot        string    `json:"localRoot"`
	RemotePath       string    `json:"remotePath"`
	ResolvedPath     string    `json:"resolvedPath"`
	DryRun           bool      `json:"dryRun"`
	StartedAt        time.Time `json:"startedAt"`
	FinishedAt       time.Time `json:"finishedAt"`
	FilesTransferred int       `json:"filesTransferred"`
	BytesTransferred int64     `json:"bytesTransferred"`
}

func CheckDestination(ctx context.Context, profile Profile) (DestinationCheck, error) {
	home, err := remote.Home(ctx, profile.Worker)
	if err != nil {
		return DestinationCheck{}, err
	}
	resolved := ResolveRemotePath(home, profile.RemotePath)
	if err := ValidateResolvedRemotePath(home, resolved); err != nil {
		return DestinationCheck{}, err
	}
	script := destinationCheckScript(resolved)
	out, err := remote.Run(ctx, profile.Worker, []string{"sh", "-lc", script}, nil, 10*time.Second)
	if err != nil {
		return DestinationCheck{}, err
	}
	state := strings.TrimSpace(out.Stdout)
	check := DestinationCheck{
		Worker:       profile.Worker,
		RemotePath:   profile.RemotePath,
		ResolvedPath: resolved,
		State:        state,
	}
	switch state {
	case "missing", "empty":
		check.OK = true
	case "marker-only":
		marker, err := ReadMarker(ctx, profile.Worker, resolved)
		if err != nil {
			return DestinationCheck{}, err
		}
		check.Marker = &marker
		check.OK = marker.Name == profile.Name && marker.LocalRoot == profile.LocalRoot
		if !check.OK {
			check.NonEmptyReason = "destination contains a marker for a different mirror"
		}
	case "non-empty":
		check.NonEmptyReason = "destination contains existing files"
	case "symlink":
		check.NonEmptyReason = "destination is a symlink"
	case "not-directory":
		check.NonEmptyReason = "destination exists but is not a directory"
	default:
		check.NonEmptyReason = "destination state is unknown"
	}
	return check, nil
}

func Sync(ctx context.Context, profile Profile, opts SyncOptions) (SyncResult, error) {
	started := time.Now().UTC()
	home, err := remote.Home(ctx, profile.Worker)
	if err != nil {
		return SyncResult{}, err
	}
	resolved := ResolveRemotePath(home, profile.RemotePath)
	if err := ValidateResolvedRemotePath(home, resolved); err != nil {
		return SyncResult{}, err
	}
	if err := prepareDestination(ctx, profile.Worker, resolved); err != nil {
		return SyncResult{}, err
	}
	excludeFile, err := writeExcludeFile(profile)
	if err != nil {
		return SyncResult{}, err
	}
	defer os.Remove(excludeFile)

	args := []string{"-az", "--stats", "--exclude-from", excludeFile}
	if profile.Delete {
		args = append(args, "--delete")
	}
	if opts.DryRun {
		args = append(args, "--dry-run")
	}
	args = append(args, "--", profile.LocalRoot+string(filepath.Separator), profile.Worker+":"+resolved+"/")
	cmd := exec.CommandContext(ctx, "rsync", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return SyncResult{}, fmt.Errorf("rsync mirror failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if !opts.DryRun {
		if err := WriteMarker(ctx, profile, resolved, opts.Version); err != nil {
			return SyncResult{}, err
		}
	}
	stats := parseStats(stdout.String())
	return SyncResult{
		OK:               true,
		Name:             profile.Name,
		Worker:           profile.Worker,
		LocalRoot:        profile.LocalRoot,
		RemotePath:       profile.RemotePath,
		ResolvedPath:     resolved,
		DryRun:           opts.DryRun,
		StartedAt:        started,
		FinishedAt:       time.Now().UTC(),
		FilesTransferred: stats.files,
		BytesTransferred: stats.bytes,
	}, nil
}

func WriteMarker(ctx context.Context, profile Profile, resolvedPath, version string) error {
	now := time.Now().UTC()
	existing, err := ReadMarker(ctx, profile.Worker, resolvedPath)
	if err == nil && !existing.CreatedAt.IsZero() {
		existing.UpdatedAt = now
		existing.WorkyardVersion = version
		existing.LocalRoot = profile.LocalRoot
		existing.RemotePath = profile.RemotePath
		data, _ := json.MarshalIndent(existing, "", "  ")
		data = append(data, '\n')
		_, err = remote.Run(ctx, profile.Worker, []string{"sh", "-lc", "cat > " + remote.ShellQuote(path.Join(resolvedPath, MarkerFileName))}, data, 10*time.Second)
		return err
	}
	marker := Marker{
		Name:            profile.Name,
		LocalRoot:       profile.LocalRoot,
		Worker:          profile.Worker,
		RemotePath:      profile.RemotePath,
		WorkyardVersion: version,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	data, _ := json.MarshalIndent(marker, "", "  ")
	data = append(data, '\n')
	_, err = remote.Run(ctx, profile.Worker, []string{"sh", "-lc", "cat > " + remote.ShellQuote(path.Join(resolvedPath, MarkerFileName))}, data, 10*time.Second)
	return err
}

func ReadMarker(ctx context.Context, worker, resolvedPath string) (Marker, error) {
	out, err := remote.Run(ctx, worker, []string{"cat", path.Join(resolvedPath, MarkerFileName)}, nil, 10*time.Second)
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal([]byte(out.Stdout), &marker); err != nil {
		return Marker{}, err
	}
	return marker, nil
}

func DeleteRemote(ctx context.Context, profile Profile) (string, error) {
	home, err := remote.Home(ctx, profile.Worker)
	if err != nil {
		return "", err
	}
	resolved := ResolveRemotePath(home, profile.RemotePath)
	if err := ValidateResolvedRemotePath(home, resolved); err != nil {
		return "", err
	}
	marker, err := ReadMarker(ctx, profile.Worker, resolved)
	if err != nil {
		return "", fmt.Errorf("refusing to delete remote path without a Workyard mirror marker: %w", err)
	}
	if marker.Name != profile.Name || marker.LocalRoot != profile.LocalRoot {
		return "", fmt.Errorf("refusing to delete remote path; marker belongs to mirror %q from %s", marker.Name, marker.LocalRoot)
	}
	script := strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolved),
		"marker=\"$dest/" + MarkerFileName + "\"",
		"if [ -L \"$dest\" ] || [ -L \"$marker\" ]; then printf 'refusing symlink mirror path\\n' >&2; exit 1; fi",
		"if [ ! -f \"$marker\" ]; then printf 'missing mirror marker\\n' >&2; exit 1; fi",
		"rm -rf -- \"$dest\"",
	}, "\n")
	if _, err := remote.Run(ctx, profile.Worker, []string{"sh", "-lc", script}, nil, 30*time.Second); err != nil {
		return "", err
	}
	return resolved, nil
}

func ResolveRemotePath(home, value string) string {
	value = strings.TrimSpace(value)
	if value == "~" {
		return path.Clean(home)
	}
	if strings.HasPrefix(value, "~/") {
		return path.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return path.Join(home, value)
}

func ValidateResolvedRemotePath(home, resolved string) error {
	resolved = path.Clean(strings.TrimSpace(resolved))
	home = path.Clean(strings.TrimSpace(home))
	if resolved == "" || resolved == "." || resolved == "/" || resolved == home {
		return fmt.Errorf("remote path %q is too broad", resolved)
	}
	if strings.Contains(resolved, "\x00") || strings.ContainsAny(resolved, "\r\n") {
		return fmt.Errorf("remote path contains invalid characters")
	}
	parts := strings.Split(strings.Trim(resolved, "/"), "/")
	if len(parts) < 3 {
		return fmt.Errorf("remote path %q is suspiciously short", resolved)
	}
	return nil
}

func prepareDestination(ctx context.Context, worker, resolved string) error {
	script := strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolved),
		"if [ -L \"$dest\" ]; then printf 'refusing symlink mirror destination\\n' >&2; exit 1; fi",
		"if [ -e \"$dest\" ] && [ ! -d \"$dest\" ]; then printf 'mirror destination is not a directory\\n' >&2; exit 1; fi",
		"mkdir -p \"$dest\"",
		"chmod go-rwx \"$dest\"",
	}, "\n")
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, nil, 20*time.Second)
	return err
}

func destinationCheckScript(resolved string) string {
	return strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolved),
		"marker=\"$dest/" + MarkerFileName + "\"",
		"if [ ! -e \"$dest\" ]; then printf 'missing\\n'; exit 0; fi",
		"if [ -L \"$dest\" ]; then printf 'symlink\\n'; exit 0; fi",
		"if [ ! -d \"$dest\" ]; then printf 'not-directory\\n'; exit 0; fi",
		"first=$(find \"$dest\" -mindepth 1 -maxdepth 1 ! -name " + remote.ShellQuote(MarkerFileName) + " -print -quit)",
		"if [ -n \"$first\" ]; then printf 'non-empty\\n'; exit 0; fi",
		"if [ -f \"$marker\" ]; then printf 'marker-only\\n'; exit 0; fi",
		"printf 'empty\\n'",
	}, "\n")
}

func writeExcludeFile(profile Profile) (string, error) {
	f, err := os.CreateTemp("", "workyard-mirror-excludes-*.txt")
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	items := append([]string{}, DefaultExcludes...)
	if !profile.IncludeGit {
		items = append(items, ".git")
	}
	items = append(items, MarkerFileName)
	items = append(items, profile.Exclude...)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" || seen[item] {
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

type rsyncStats struct {
	files int
	bytes int64
}

func parseStats(out string) rsyncStats {
	var stats rsyncStats
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Number of regular files transferred:") {
			stats.files = parseIntAfterColon(line)
		}
		if strings.HasPrefix(line, "Total transferred file size:") {
			stats.bytes = int64(parseIntAfterColon(line))
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
