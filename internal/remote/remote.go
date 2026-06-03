package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/runid"
)

var workerTargetRE = regexp.MustCompile(`^([A-Za-z0-9._%+-]+@)?[A-Za-z0-9._-]+$`)

type Paths struct {
	Home      string `json:"home"`
	RunRoot   string `json:"runRoot"`
	Source    string `json:"source"`
	Logs      string `json:"logs"`
	State     string `json:"state"`
	Sync      string `json:"sync"`
	DaemonDir string `json:"daemonDir"`
	Socket    string `json:"socket"`
	Binary    string `json:"binary"`
	Project   string `json:"project"`
	RunID     string `json:"runId"`
}

func Home(ctx context.Context, worker string) (string, error) {
	out, err := Run(ctx, worker, []string{"sh", "-lc", "printf %s \"$HOME\""}, nil, 8*time.Second)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(out.Stdout)
	if home == "" || strings.Contains(home, "\x00") {
		return "", errors.New("remote home directory was empty or invalid")
	}
	return home, nil
}

func ValidateWorker(worker string) error {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return errors.New("worker is required")
	}
	if strings.HasPrefix(worker, "-") {
		return fmt.Errorf("worker %q must not start with '-'", worker)
	}
	if strings.ContainsAny(worker, "\x00\r\n\t /\\:;|&<>`'\"") {
		return fmt.Errorf("worker %q contains unsupported characters", worker)
	}
	if !workerTargetRE.MatchString(worker) {
		return fmt.Errorf("worker %q must look like host or user@host", worker)
	}
	return nil
}

func BuildPaths(home, remoteRoot, projectName, run string) (Paths, error) {
	project, err := runid.ProjectName(projectName)
	if err != nil {
		return Paths{}, err
	}
	runSafe, err := runid.Validate(run)
	if err != nil {
		return Paths{}, err
	}
	base := ""
	if remoteRoot == "" {
		base = path.Join(home, ".workyard", "runs")
	} else {
		base = normalizeRoot(home, remoteRoot)
		if !isUnder(base, path.Join(home, ".workyard", "runs")) {
			return Paths{}, fmt.Errorf("remote root must stay under %s", path.Join(home, ".workyard", "runs"))
		}
	}
	runRoot := path.Join(base, project, runSafe)
	return Paths{
		Home:      home,
		RunRoot:   runRoot,
		Source:    path.Join(runRoot, "source"),
		Logs:      path.Join(runRoot, "logs"),
		State:     path.Join(runRoot, "state.json"),
		Sync:      path.Join(runRoot, "sync.json"),
		DaemonDir: path.Join(home, ".workyard", "daemon"),
		Socket:    path.Join(home, ".workyard", "daemon", "workyard.sock"),
		Binary:    path.Join(home, ".workyard", "bin", "workyard"),
		Project:   project,
		RunID:     runSafe,
	}, nil
}

func normalizeRoot(home, root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return path.Join(home, ".workyard", "runs")
	}
	if root == "~" {
		return home
	}
	if strings.HasPrefix(root, "~/") {
		return path.Join(home, strings.TrimPrefix(root, "~/"))
	}
	if strings.HasPrefix(root, "/") {
		return path.Clean(root)
	}
	return path.Join(home, root)
}

func isUnder(candidate, root string) bool {
	candidate = path.Clean(candidate)
	root = path.Clean(root)
	return candidate == root || strings.HasPrefix(candidate, root+"/")
}

func GuardDestination(dest string) error {
	dest = path.Clean(dest)
	if dest == "" || dest == "/" || dest == "." {
		return fmt.Errorf("refusing suspicious remote destination %q", dest)
	}
	if !strings.HasSuffix(dest, "/source") {
		return fmt.Errorf("remote destination must end in /source: %s", dest)
	}
	if len(strings.Split(strings.Trim(dest, "/"), "/")) < 5 {
		return fmt.Errorf("remote destination is suspiciously short: %s", dest)
	}
	return nil
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func Run(ctx context.Context, worker string, argv []string, stdin []byte, timeout time.Duration) (Result, error) {
	if err := ValidateWorker(worker); err != nil {
		return Result{}, err
	}
	if len(argv) == 0 {
		return Result{}, errors.New("remote command argv is empty")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	remoteCommand := quoteArgs(argv)
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", worker, remoteCommand)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exit.ExitCode()
		} else {
			res.ExitCode = 1
		}
		return res, fmt.Errorf("ssh %s failed: %w: %s", worker, err, strings.TrimSpace(res.Stderr))
	}
	return res, nil
}

func Stream(ctx context.Context, worker string, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := ValidateWorker(worker); err != nil {
		return err
	}
	if len(argv) == 0 {
		return errors.New("remote command argv is empty")
	}
	remoteCommand := quoteArgs(argv)
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", worker, remoteCommand)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh %s failed: %w", worker, err)
	}
	return nil
}

func EnsureDaemon(ctx context.Context, workerHost string, paths Paths, overrideBinary string) error {
	binary := paths.Binary
	if overrideBinary != "" {
		binary = overrideBinary
	}
	ping := []string{binary, "daemonctl", "ping", "--socket", paths.Socket, "--json"}
	if _, err := Run(ctx, workerHost, ping, nil, 5*time.Second); err == nil {
		return nil
	}
	binDir := path.Join(paths.Home, ".workyard", "bin")
	script := strings.Join([]string{
		"mkdir -p " + ShellQuote(paths.DaemonDir) + " " + ShellQuote(binDir),
		"nohup " + ShellQuote(binary) + " daemon --foreground --state-dir " + ShellQuote(path.Join(paths.Home, ".workyard")) + " --socket " + ShellQuote(paths.Socket) + " > " + ShellQuote(path.Join(paths.DaemonDir, "daemon.log")) + " 2>&1 &",
	}, " && ")
	if _, err := Run(ctx, workerHost, []string{"sh", "-lc", script}, nil, 10*time.Second); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		if _, err := Run(ctx, workerHost, ping, nil, 3*time.Second); err == nil {
			return nil
		} else {
			last = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return last
}

func ShellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z') &&
			!(r >= 'A' && r <= 'Z') &&
			!(r >= '0' && r <= '9') &&
			!strings.ContainsRune("@%_+=:,./-", r)
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func quoteArgs(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = ShellQuote(arg)
	}
	return strings.Join(parts, " ")
}
