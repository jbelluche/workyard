package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	ID              string    `json:"id,omitempty"`
	Name            string    `json:"name"`
	LocalRoot       string    `json:"localRoot"`
	Worker          string    `json:"worker"`
	RemotePath      string    `json:"remotePath"`
	DestinationID   string    `json:"destinationId,omitempty"`
	WorkyardVersion string    `json:"workyardVersion"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SyncOptions struct {
	Version string
	DryRun  bool
	Verbose bool
}

type SyncResult struct {
	OK               bool      `json:"ok"`
	ID               string    `json:"id"`
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
	Changed          []Change  `json:"changed,omitempty"`
}

type Change struct {
	Path string `json:"path"`
	Code string `json:"code"`
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
		check.OK = MarkerMatches(marker, profile)
		if !check.OK {
			check.NonEmptyReason = "destination contains a marker for a different mirror"
		}
	case "non-empty":
		if marker, err := ReadMarker(ctx, profile.Worker, resolved); err == nil {
			check.Marker = &marker
			check.OK = MarkerMatches(marker, profile)
			if !check.OK {
				check.NonEmptyReason = "destination marker belongs to a different mirror"
			}
		} else {
			check.NonEmptyReason = "destination contains existing files"
		}
	case "symlink":
		check.NonEmptyReason = "destination is a symlink"
	case "not-directory":
		check.NonEmptyReason = "destination exists but is not a directory"
	default:
		check.NonEmptyReason = "destination state is unknown"
	}
	return check, nil
}

func ReadyDestination(ctx context.Context, profile Profile) (DestinationCheck, error) {
	check, err := CheckDestination(ctx, profile)
	if err != nil {
		return DestinationCheck{}, err
	}
	switch check.State {
	case "marker-only":
		if check.OK && check.Marker != nil {
			return check, nil
		}
		if check.Marker != nil {
			return check, fmt.Errorf("destination marker belongs to mirror %q from %s", check.Marker.Name, check.Marker.LocalRoot)
		}
		return check, fmt.Errorf("destination marker is not readable")
	case "non-empty":
		marker, err := ReadMarker(ctx, profile.Worker, check.ResolvedPath)
		if err != nil {
			return check, fmt.Errorf("destination is not synced yet; missing matching %s marker: %w", MarkerFileName, err)
		}
		check.Marker = &marker
		check.OK = MarkerMatches(marker, profile)
		if !check.OK {
			return check, fmt.Errorf("destination marker belongs to mirror %q from %s", marker.Name, marker.LocalRoot)
		}
		return check, nil
	case "missing":
		return check, fmt.Errorf("destination does not exist yet")
	case "empty":
		return check, fmt.Errorf("destination is empty and has no mirror marker")
	default:
		if check.NonEmptyReason != "" {
			return check, fmt.Errorf("%s", check.NonEmptyReason)
		}
		return check, fmt.Errorf("destination is not ready: %s", check.State)
	}
}

func EnsureDestination(ctx context.Context, profile Profile) (string, error) {
	home, err := remote.Home(ctx, profile.Worker)
	if err != nil {
		return "", err
	}
	resolved := ResolveRemotePath(home, profile.RemotePath)
	if err := ValidateResolvedRemotePath(home, resolved); err != nil {
		return "", err
	}
	if err := prepareDestination(ctx, profile.Worker, resolved); err != nil {
		return "", err
	}
	return resolved, nil
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

	args := []string{"-az", "--stats", "--itemize-changes", "--exclude-from", excludeFile}
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
		ID:               profile.ID,
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
		Changed:          parseChanges(stdout.String()),
	}, nil
}

func WriteMarker(ctx context.Context, profile Profile, resolvedPath, version string) error {
	now := time.Now().UTC()
	destinationID, err := destinationIdentity(ctx, profile.Worker, resolvedPath)
	if err != nil {
		return err
	}
	existing, err := ReadMarker(ctx, profile.Worker, resolvedPath)
	if err == nil && !existing.CreatedAt.IsZero() {
		existing.ID = profile.ID
		existing.Name = profile.Name
		existing.Worker = profile.Worker
		existing.UpdatedAt = now
		existing.WorkyardVersion = version
		existing.LocalRoot = profile.LocalRoot
		existing.RemotePath = profile.RemotePath
		existing.DestinationID = destinationID
		data, _ := json.MarshalIndent(existing, "", "  ")
		data = append(data, '\n')
		return writeMarkerData(ctx, profile.Worker, resolvedPath, data)
	}
	marker := Marker{
		ID:              profile.ID,
		Name:            profile.Name,
		LocalRoot:       profile.LocalRoot,
		Worker:          profile.Worker,
		RemotePath:      profile.RemotePath,
		DestinationID:   destinationID,
		WorkyardVersion: version,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	data, _ := json.MarshalIndent(marker, "", "  ")
	data = append(data, '\n')
	return writeMarkerData(ctx, profile.Worker, resolvedPath, data)
}

func ReadMarker(ctx context.Context, worker, resolvedPath string) (Marker, error) {
	marker, _, _, err := readMarker(ctx, worker, resolvedPath)
	return marker, err
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
	marker, markerPath, _, err := readMarker(ctx, profile.Worker, resolved)
	if err != nil {
		return "", fmt.Errorf("refusing to delete remote path without a Workyard mirror marker: %w", err)
	}
	if !MarkerMatches(marker, profile) {
		return "", fmt.Errorf("refusing to delete remote path; marker belongs to mirror %q from %s", marker.Name, marker.LocalRoot)
	}
	if marker.DestinationID != "" {
		currentID, err := destinationIdentity(ctx, profile.Worker, resolved)
		if err != nil {
			return "", fmt.Errorf("refusing to delete remote path; could not verify destination identity: %w", err)
		}
		if currentID != marker.DestinationID {
			return "", fmt.Errorf("refusing to delete remote path; destination identity changed since the marker was written")
		}
	}
	script := strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolved),
		"marker=" + remote.ShellQuote(markerPath),
		"if [ -L \"$dest\" ] || [ -L \"$marker\" ]; then printf 'refusing symlink mirror path\\n' >&2; exit 1; fi",
		"if [ ! -f \"$marker\" ]; then printf 'missing mirror marker\\n' >&2; exit 1; fi",
		"rm -rf -- \"$dest\"",
		"rm -f -- \"$marker\"",
	}, "\n")
	if _, err := remote.Run(ctx, profile.Worker, []string{"sh", "-lc", script}, nil, 30*time.Second); err != nil {
		return "", err
	}
	return resolved, nil
}

func MarkerMatches(marker Marker, profile Profile) bool {
	if marker.ID != "" && profile.ID != "" {
		if marker.ID == profile.ID {
			return true
		}
	}
	return marker.Name == profile.Name && marker.LocalRoot == profile.LocalRoot
}

func readMarker(ctx context.Context, worker, resolvedPath string) (Marker, string, bool, error) {
	paths := []struct {
		path   string
		legacy bool
	}{
		{path: markerPath(resolvedPath)},
		{path: legacyMarkerPath(resolvedPath), legacy: true},
	}
	var lastErr error
	for _, candidate := range paths {
		out, err := remote.Run(ctx, worker, []string{"cat", candidate.path}, nil, 10*time.Second)
		if err != nil {
			lastErr = err
			continue
		}
		var marker Marker
		if err := json.Unmarshal([]byte(out.Stdout), &marker); err != nil {
			return Marker{}, candidate.path, candidate.legacy, err
		}
		return marker, candidate.path, candidate.legacy, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("marker not found")
	}
	return Marker{}, "", false, lastErr
}

func writeMarkerData(ctx context.Context, worker, resolvedPath string, data []byte) error {
	marker := markerPath(resolvedPath)
	legacy := legacyMarkerPath(resolvedPath)
	script := strings.Join([]string{
		"set -eu",
		"marker=" + remote.ShellQuote(marker),
		"marker_dir=$(dirname \"$marker\")",
		"legacy=" + remote.ShellQuote(legacy),
		"if [ -L \"$marker_dir\" ] || [ -L \"$marker\" ] || [ -L \"$legacy\" ]; then printf 'refusing symlink mirror marker path\\n' >&2; exit 1; fi",
		"mkdir -p \"$marker_dir\"",
		"chmod go-rwx \"$marker_dir\"",
		"tmp=\"$marker.$$\"",
		"cat > \"$tmp\"",
		"chmod go-rwx \"$tmp\"",
		"mv -f \"$tmp\" \"$marker\"",
		"if [ -f \"$legacy\" ]; then rm -f -- \"$legacy\"; fi",
	}, "\n")
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, data, 10*time.Second)
	return err
}

func markerPath(resolvedPath string) string {
	clean := path.Clean(resolvedPath)
	sum := sha256.Sum256([]byte(clean))
	return path.Join(path.Dir(clean), ".workyard-mirrors", fmt.Sprintf("%s-%x.json", path.Base(clean), sum[:6]))
}

func legacyMarkerPath(resolvedPath string) string {
	return path.Join(resolvedPath, MarkerFileName)
}

func destinationIdentity(ctx context.Context, worker, resolvedPath string) (string, error) {
	script := strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolvedPath),
		"(stat -c '%d:%i' \"$dest\" 2>/dev/null || stat -f '%d:%i' \"$dest\" 2>/dev/null)",
	}, "\n")
	out, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, nil, 10*time.Second)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(out.Stdout)
	if id == "" {
		return "", fmt.Errorf("empty destination identity")
	}
	return id, nil
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
	sidecarMarker := markerPath(resolved)
	return strings.Join([]string{
		"set -eu",
		"dest=" + remote.ShellQuote(resolved),
		"marker=" + remote.ShellQuote(sidecarMarker),
		"legacy_marker=\"$dest/" + MarkerFileName + "\"",
		"if [ ! -e \"$dest\" ]; then printf 'missing\\n'; exit 0; fi",
		"if [ -L \"$dest\" ]; then printf 'symlink\\n'; exit 0; fi",
		"if [ ! -d \"$dest\" ]; then printf 'not-directory\\n'; exit 0; fi",
		"first=$(find \"$dest\" -mindepth 1 -maxdepth 1 ! -name " + remote.ShellQuote(MarkerFileName) + " -print -quit)",
		"if [ -n \"$first\" ]; then printf 'non-empty\\n'; exit 0; fi",
		"if [ -f \"$marker\" ] || [ -f \"$legacy_marker\" ]; then printf 'marker-only\\n'; exit 0; fi",
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
	items = append(items, PresetExcludes(profile.Presets)...)
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

func parseChanges(out string) []Change {
	var changes []Change
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		idx := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
		if idx < 2 {
			continue
		}
		code := line[:idx]
		if !strings.ContainsRune("><ch.*", rune(code[0])) || !strings.ContainsRune("fdLDS.", rune(code[1])) {
			continue
		}
		pathValue := strings.TrimSpace(line[idx+1:])
		if pathValue == "" || strings.HasPrefix(pathValue, "Number of ") || strings.HasPrefix(pathValue, "Total ") {
			continue
		}
		changes = append(changes, Change{Code: code, Path: pathValue})
	}
	return changes
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
