package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type InstallOptions struct {
	LocalBinary     string
	RemoteBinary    string
	ExpectedVersion string
}

type InstallResult struct {
	OK               bool     `json:"ok"`
	Worker           string   `json:"worker"`
	Platform         Platform `json:"platform"`
	LocalBinary      string   `json:"localBinary"`
	RemoteBinary     string   `json:"remoteBinary"`
	InstalledVersion string   `json:"installedVersion"`
	DaemonRestarted  bool     `json:"daemonRestarted,omitempty"`
	BytesCopied      int64    `json:"bytesCopied"`
}

func DetectPlatform(ctx context.Context, worker string) (Platform, error) {
	res, err := Run(ctx, worker, []string{"sh", "-lc", "uname -s; uname -m"}, nil, 8*time.Second)
	if err != nil {
		return Platform{}, err
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) < 2 {
		return Platform{}, fmt.Errorf("remote uname output was incomplete: %q", strings.TrimSpace(res.Stdout))
	}
	return NormalizePlatform(lines[0], lines[1])
}

func NormalizePlatform(osName, machine string) (Platform, error) {
	osName = strings.ToLower(strings.TrimSpace(osName))
	machine = strings.ToLower(strings.TrimSpace(machine))
	switch osName {
	case "linux":
		osName = "linux"
	case "darwin":
		osName = "darwin"
	default:
		return Platform{}, fmt.Errorf("unsupported worker OS %q", osName)
	}
	switch machine {
	case "aarch64", "arm64":
		machine = "arm64"
	case "x86_64", "amd64":
		machine = "amd64"
	default:
		return Platform{}, fmt.Errorf("unsupported worker architecture %q", machine)
	}
	return Platform{OS: osName, Arch: machine}, nil
}

func (p Platform) ArtifactName() string {
	return "workyard-" + p.OS + "-" + p.Arch
}

// EnsureArtifact returns a local binary matching the platform: the explicit
// localBinary when given, the prebuilt artifact in artifactDir when present,
// or a fresh cross-compiled build from the repo checkout when the Go
// toolchain is available. version is stamped into builds so remote install
// verification matches the running CLI.
func EnsureArtifact(ctx context.Context, platform Platform, artifactDir, localBinary, version string) (string, error) {
	if strings.TrimSpace(localBinary) != "" {
		if _, err := os.Stat(localBinary); err != nil {
			return "", err
		}
		return localBinary, nil
	}
	repoRoot, repoErr := FindRepoRoot()
	dir := strings.TrimSpace(artifactDir)
	if dir == "" {
		dir = "dist"
	}
	if !filepath.IsAbs(dir) && repoErr == nil {
		dir = filepath.Join(repoRoot, dir)
	}
	binary := filepath.Join(dir, platform.ArtifactName())
	if info, err := os.Stat(binary); err == nil && !info.IsDir() {
		if repoErr != nil || artifactFresh(repoRoot, binary) {
			return binary, nil
		}
	}
	if repoErr != nil {
		return "", fmt.Errorf("artifact %s not found and no Workyard checkout to build from: %v", binary, repoErr)
	}
	if _, err := exec.LookPath("go"); err != nil {
		return "", fmt.Errorf("artifact %s not found and the Go toolchain is unavailable to build it", binary)
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		return "", err
	}
	args := []string{"build"}
	if strings.TrimSpace(version) != "" {
		args = append(args, "-ldflags", "-X github.com/jackbelluche/workyard/internal/cli.Version="+version)
	}
	args = append(args, "-o", binary, "./cmd/workyard")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOOS="+platform.OS, "GOARCH="+platform.Arch, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build for %s/%s failed: %w: %s", platform.OS, platform.Arch, err, strings.TrimSpace(stderr.String()))
	}
	return binary, nil
}

func artifactFresh(repoRoot, binary string) bool {
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		return false
	}
	builtAt := info.ModTime()
	for _, rel := range []string{"go.mod", "go.sum"} {
		if newerThan(filepath.Join(repoRoot, rel), builtAt) {
			return false
		}
	}
	for _, rel := range []string{"cmd", "internal"} {
		root := filepath.Join(repoRoot, rel)
		stale := false
		if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			if newerThan(path, builtAt) {
				stale = true
				return fs.SkipAll
			}
			return nil
		}); stale {
			return false
		} else if err != nil {
			return false
		}
	}
	return true
}

func newerThan(path string, cutoff time.Time) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.ModTime().After(cutoff)
}

// FindRepoRoot walks up from the working directory to the Workyard checkout.
func FindRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isWorkyardRepoRoot(dir) {
			return dir, nil
		}
		next := filepath.Dir(dir)
		if next == dir {
			break
		}
		dir = next
	}
	return "", fmt.Errorf("could not find Workyard repository root from current directory")
}

func isWorkyardRepoRoot(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "cmd", "workyard", "main.go")); err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "module github.com/jackbelluche/workyard")
}

func InstallBinary(ctx context.Context, worker string, platform Platform, opts InstallOptions) (InstallResult, error) {
	if opts.LocalBinary == "" {
		return InstallResult{}, fmt.Errorf("local binary path is required")
	}
	data, err := os.ReadFile(opts.LocalBinary)
	if err != nil {
		return InstallResult{}, err
	}
	home, err := Home(ctx, worker)
	if err != nil {
		return InstallResult{}, err
	}
	dest, err := installDestination(home, opts.RemoteBinary)
	if err != nil {
		return InstallResult{}, err
	}
	script := installScript(home, dest)
	if _, err := Run(ctx, worker, []string{"sh", "-lc", script}, data, 60*time.Second); err != nil {
		return InstallResult{}, err
	}
	version, err := installedVersion(ctx, worker, dest)
	if err != nil {
		return InstallResult{}, err
	}
	if opts.ExpectedVersion != "" && version != opts.ExpectedVersion {
		return InstallResult{}, fmt.Errorf("installed version %q does not match expected version %q", version, opts.ExpectedVersion)
	}
	return InstallResult{
		OK:               true,
		Worker:           worker,
		Platform:         platform,
		LocalBinary:      opts.LocalBinary,
		RemoteBinary:     dest,
		InstalledVersion: version,
		BytesCopied:      int64(len(data)),
	}, nil
}

func installDestination(home, override string) (string, error) {
	root := path.Join(home, ".workyard", "bin")
	dest := path.Join(root, "workyard")
	if strings.TrimSpace(override) != "" {
		dest = normalizeRoot(home, override)
	}
	if !isUnder(dest, root) {
		return "", fmt.Errorf("remote binary must be under %s", root)
	}
	if path.Base(dest) == "." || path.Base(dest) == "/" {
		return "", fmt.Errorf("remote binary path is invalid: %s", dest)
	}
	return dest, nil
}

func installScript(home, dest string) string {
	workyardDir := path.Join(home, ".workyard")
	binDir := path.Dir(dest)
	tmpPrefix := path.Join(binDir, "."+path.Base(dest)+".upload")
	return strings.Join([]string{
		"set -eu",
		"for p in " + quoteList([]string{workyardDir, binDir}) + "; do if [ -L \"$p\" ]; then printf 'refusing symlink path: %s\\n' \"$p\" >&2; exit 1; fi; done",
		"mkdir -p " + ShellQuote(binDir),
		"chmod go-rwx " + quoteList([]string{workyardDir, binDir}),
		"if [ -L " + ShellQuote(dest) + " ]; then printf 'refusing symlink binary: %s\\n' " + ShellQuote(dest) + " >&2; exit 1; fi",
		"tmp=" + ShellQuote(tmpPrefix) + ".$$",
		"trap 'rm -f \"$tmp\"' EXIT",
		"cat > \"$tmp\"",
		"chmod 700 \"$tmp\"",
		"mv \"$tmp\" " + ShellQuote(dest),
	}, "\n")
}

func installedVersion(ctx context.Context, worker, binary string) (string, error) {
	res, err := Run(ctx, worker, []string{binary, "version", "--json"}, nil, 10*time.Second)
	if err != nil {
		return "", err
	}
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return "", fmt.Errorf("decode version response: %w", err)
	}
	if !out.OK || strings.TrimSpace(out.Version) == "" {
		return "", fmt.Errorf("version response was not ok")
	}
	return strings.TrimSpace(out.Version), nil
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, ShellQuote(value))
	}
	return strings.Join(quoted, " ")
}
