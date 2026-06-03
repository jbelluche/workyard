package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
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
