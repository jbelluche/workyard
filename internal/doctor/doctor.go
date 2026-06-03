package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/remote"
)

const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
)

type Options struct {
	Project      string
	Worker       string
	RemoteRoot   string
	RemoteBinary string
	Version      string
	CheckProject bool
	Timeout      time.Duration
}

type Report struct {
	OK          bool      `json:"ok"`
	GeneratedAt time.Time `json:"generatedAt"`
	Checks      []Check   `json:"checks"`
}

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Runner interface {
	LookPath(name string) (string, error)
	Run(ctx context.Context, name string, args []string, timeout time.Duration) (CommandResult, error)
}

type SystemRunner struct{}

func (SystemRunner) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (SystemRunner) Run(ctx context.Context, name string, args []string, timeout time.Duration) (CommandResult, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			res.ExitCode = exit.ExitCode()
		} else {
			res.ExitCode = 1
		}
	}
	return res, err
}

func Run(ctx context.Context, opts Options, runner Runner) Report {
	if runner == nil {
		runner = SystemRunner{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 8 * time.Second
	}
	report := Report{OK: true, GeneratedAt: time.Now().UTC()}
	version := strings.TrimSpace(opts.Version)
	if version == "" {
		version = "unknown"
	}
	report.add(Check{Name: "workyard.version", Status: StatusPass, Required: true, Message: "workyard CLI is available", Detail: "version " + version})
	report.add(commandInstalled(runner, "rsync", true, "Install rsync before running workyard sync"))
	report.add(commandInstalled(runner, "ssh", true, "Install OpenSSH before connecting to a worker"))
	report.add(tailscaleInstalled(runner))
	report.add(tailscaleConnected(ctx, runner, opts.Timeout))
	if opts.CheckProject {
		report.add(projectConfig(opts.Project))
	}
	if strings.TrimSpace(opts.Worker) != "" {
		worker := strings.TrimSpace(opts.Worker)
		ssh := workerSSH(ctx, runner, worker, opts.Timeout)
		report.add(ssh)
		if ssh.Status == StatusPass {
			home, err := workerHome(ctx, runner, worker, opts.Timeout)
			if err != nil {
				report.add(Check{Name: "worker.home", Status: StatusFail, Required: true, Message: "worker home directory could not be read", Detail: err.Error(), Hint: "Check SSH access and the worker shell environment"})
			} else {
				report.add(workerBinary(ctx, runner, worker, home, opts.RemoteBinary, version, opts.Timeout))
				report.add(workerDaemonPing(ctx, runner, worker, home, opts.RemoteBinary, opts.Timeout))
				report.add(workerRunRootPermissions(ctx, runner, worker, home, opts.RemoteRoot, opts.Timeout))
				report.add(workerPortRangeAvailable(ctx, runner, worker, configuredPortRange(opts.Project), opts.Timeout))
			}
		}
	}
	report.OK = reportOK(report.Checks)
	return report
}

func (r *Report) add(check Check) {
	r.Checks = append(r.Checks, check)
}

func reportOK(checks []Check) bool {
	for _, check := range checks {
		if check.Required && check.Status == StatusFail {
			return false
		}
	}
	return true
}

func commandInstalled(runner Runner, name string, required bool, hint string) Check {
	path, err := runner.LookPath(name)
	if err != nil {
		return Check{Name: name + ".installed", Status: StatusFail, Required: required, Message: name + " is not installed or not on PATH", Hint: hint}
	}
	return Check{Name: name + ".installed", Status: StatusPass, Required: required, Message: name + " is installed", Detail: path}
}

func tailscaleInstalled(runner Runner) Check {
	path, err := runner.LookPath("tailscale")
	if err != nil {
		return Check{
			Name:     "tailscale.installed",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale is not installed or not on PATH",
			Hint:     "Install Tailscale and make sure the tailscale CLI is available in PATH",
		}
	}
	return Check{Name: "tailscale.installed", Status: StatusPass, Required: true, Message: "tailscale is installed", Detail: path}
}

func tailscaleConnected(ctx context.Context, runner Runner, timeout time.Duration) Check {
	if _, err := runner.LookPath("tailscale"); err != nil {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale status could not be checked because tailscale is not installed",
			Hint:     "Install Tailscale and log in",
		}
	}
	res, err := runner.Run(ctx, "tailscale", []string{"status", "--json"}, timeout)
	if err != nil {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale status failed",
			Detail:   trimOutput(res.Stdout, res.Stderr),
			Hint:     "Start Tailscale and log in to your tailnet",
		}
	}
	var status tailscaleStatus
	if err := json.Unmarshal([]byte(res.Stdout), &status); err != nil {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale status output was not valid JSON",
			Detail:   firstLine(res.Stdout),
			Hint:     "Run tailscale status --json to inspect the local Tailscale state",
		}
	}
	if status.BackendState != "Running" {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale is not connected",
			Detail:   "backend state: " + emptyDefault(status.BackendState, "unknown"),
			Hint:     "Start Tailscale and confirm this machine is logged in",
		}
	}
	if status.Self == nil {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale is running but did not report this node",
			Hint:     "Run tailscale status and confirm this machine appears in the tailnet",
		}
	}
	if !status.Self.Online {
		return Check{
			Name:     "tailscale.connected",
			Status:   StatusFail,
			Required: true,
			Message:  "tailscale is running but this node is offline",
			Detail:   tailscaleSelfDetail(*status.Self),
			Hint:     "Reconnect Tailscale and confirm this machine is online",
		}
	}
	return Check{
		Name:     "tailscale.connected",
		Status:   StatusPass,
		Required: true,
		Message:  "tailscale is running and connected",
		Detail:   tailscaleSelfDetail(*status.Self),
	}
}

type tailscaleStatus struct {
	BackendState   string         `json:"BackendState"`
	Self           *tailscaleSelf `json:"Self"`
	CurrentTailnet *tailnetStatus `json:"CurrentTailnet"`
}

type tailscaleSelf struct {
	DNSName      string   `json:"DNSName"`
	HostName     string   `json:"HostName"`
	Online       bool     `json:"Online"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type tailnetStatus struct {
	Name string `json:"Name"`
}

func tailscaleSelfDetail(self tailscaleSelf) string {
	parts := []string{}
	if self.DNSName != "" {
		parts = append(parts, "dns: "+strings.TrimSuffix(self.DNSName, "."))
	}
	if self.HostName != "" {
		parts = append(parts, "host: "+self.HostName)
	}
	if len(self.TailscaleIPs) > 0 {
		parts = append(parts, "ip: "+self.TailscaleIPs[0])
	}
	return strings.Join(parts, ", ")
}

func projectConfig(project string) Check {
	loaded, err := config.Load(project)
	if err != nil {
		return Check{
			Name:     "workyard.config",
			Status:   StatusWarn,
			Required: false,
			Message:  "workyard.yaml was not found or is invalid",
			Detail:   err.Error(),
			Hint:     "Run workyard init or pass --project to an existing Workyard project",
		}
	}
	detail := loaded.Config.Path
	if len(loaded.Warnings) > 0 {
		return Check{
			Name:     "workyard.config",
			Status:   StatusWarn,
			Required: false,
			Message:  "workyard.yaml is valid with warnings",
			Detail:   detail + ": " + strings.Join(loaded.Warnings, "; "),
		}
	}
	return Check{Name: "workyard.config", Status: StatusPass, Required: false, Message: "workyard.yaml is valid", Detail: detail}
}

func workerSSH(ctx context.Context, runner Runner, worker string, timeout time.Duration) Check {
	if err := remote.ValidateWorker(worker); err != nil {
		return Check{
			Name:     "worker.ssh",
			Status:   StatusFail,
			Required: true,
			Message:  "worker SSH target is invalid",
			Detail:   err.Error(),
			Hint:     "Use a normal SSH target such as jack@jack-rasp-five",
		}
	}
	if _, err := runner.LookPath("ssh"); err != nil {
		return Check{
			Name:     "worker.ssh",
			Status:   StatusFail,
			Required: true,
			Message:  "worker SSH connectivity could not be checked because ssh is not installed",
			Hint:     "Install OpenSSH before checking a worker",
		}
	}
	res, err := sshRun(ctx, runner, worker, "printf '%s' \"$HOME\"", timeout)
	if err != nil {
		return Check{
			Name:     "worker.ssh",
			Status:   StatusFail,
			Required: true,
			Message:  "worker SSH connection failed",
			Detail:   trimOutput(res.Stdout, res.Stderr),
			Hint:     "Check Tailscale connectivity and SSH access to " + worker,
		}
	}
	home := strings.TrimSpace(res.Stdout)
	if home == "" || strings.Contains(home, "\x00") {
		return Check{
			Name:     "worker.ssh",
			Status:   StatusFail,
			Required: true,
			Message:  "worker SSH connected but returned an invalid home directory",
			Hint:     "Check the worker shell environment",
		}
	}
	return Check{Name: "worker.ssh", Status: StatusPass, Required: true, Message: "worker SSH connection succeeded", Detail: worker + ":" + home}
}

func workerHome(ctx context.Context, runner Runner, worker string, timeout time.Duration) (string, error) {
	res, err := sshRun(ctx, runner, worker, "printf '%s' \"$HOME\"", timeout)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(res.Stdout)
	if home == "" || strings.Contains(home, "\x00") {
		return "", fmt.Errorf("invalid home directory")
	}
	return home, nil
}

func workerBinary(ctx context.Context, runner Runner, worker, home, remoteBinary, expectedVersion string, timeout time.Duration) Check {
	binary := workerBinaryPath(home, remoteBinary)
	res, err := sshRun(ctx, runner, worker, remote.ShellQuote(binary)+" version --json", timeout)
	if err != nil {
		return Check{
			Name:     "worker.binary",
			Status:   StatusFail,
			Required: true,
			Message:  "worker binary is missing, not executable, or failed to run",
			Detail:   trimOutput(res.Stdout, res.Stderr),
			Hint:     "Run workyard --worker " + worker + " install",
		}
	}
	var out struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || !out.OK || strings.TrimSpace(out.Version) == "" {
		return Check{
			Name:     "worker.binary",
			Status:   StatusFail,
			Required: true,
			Message:  "worker binary returned an invalid version response",
			Detail:   firstLine(res.Stdout),
			Hint:     "Reinstall the worker binary with workyard --worker " + worker + " install",
		}
	}
	if expectedVersion != "" && out.Version != expectedVersion {
		return Check{
			Name:     "worker.binary",
			Status:   StatusFail,
			Required: true,
			Message:  "worker binary version does not match the local CLI",
			Detail:   "worker " + out.Version + ", local " + expectedVersion,
			Hint:     "Upgrade the worker with workyard --worker " + worker + " install",
		}
	}
	return Check{Name: "worker.binary", Status: StatusPass, Required: true, Message: "worker binary is installed", Detail: binary + " version " + out.Version}
}

func workerDaemonPing(ctx context.Context, runner Runner, worker, home, remoteBinary string, timeout time.Duration) Check {
	binary := workerBinaryPath(home, remoteBinary)
	socket := path.Join(home, ".workyard", "daemon", "workyard.sock")
	res, err := sshRun(ctx, runner, worker, remote.ShellQuote(binary)+" daemonctl ping --socket "+remote.ShellQuote(socket)+" --json", timeout)
	if err != nil {
		return Check{
			Name:     "worker.daemon",
			Status:   StatusWarn,
			Required: false,
			Message:  "worker daemon did not respond to ping",
			Detail:   trimOutput(res.Stdout, res.Stderr),
			Hint:     "Run a Workyard command that auto-starts the private daemon, or inspect ~/.workyard/daemon/daemon.log on the worker",
		}
	}
	return Check{Name: "worker.daemon", Status: StatusPass, Required: false, Message: "worker daemon responded to ping", Detail: firstLine(res.Stdout)}
}

func workerRunRootPermissions(ctx context.Context, runner Runner, worker, home, remoteRoot string, timeout time.Duration) Check {
	runsRoot, err := runsRootPath(home, remoteRoot)
	if err != nil {
		return Check{Name: "worker.runRoot", Status: StatusFail, Required: true, Message: "worker run root is invalid", Detail: err.Error(), Hint: "Use a remote root under ~/.workyard/runs"}
	}
	script := strings.Join([]string{
		"set -eu",
		"root=" + remote.ShellQuote(runsRoot),
		"if [ -L \"$root\" ]; then printf 'symlink\\n' >&2; exit 1; fi",
		"mkdir -p \"$root\"",
		"test -d \"$root\"",
		"test -w \"$root\"",
		"(stat -c %a \"$root\" 2>/dev/null || stat -f %Lp \"$root\")",
	}, "\n")
	res, err := sshRun(ctx, runner, worker, script, timeout)
	if err != nil {
		return Check{Name: "worker.runRoot", Status: StatusFail, Required: true, Message: "worker run root is not writable", Detail: trimOutput(res.Stdout, res.Stderr), Hint: "Create a private writable run root under ~/.workyard/runs"}
	}
	mode := strings.TrimSpace(firstLine(res.Stdout))
	if !privateMode(mode) {
		return Check{Name: "worker.runRoot", Status: StatusFail, Required: true, Message: "worker run root permissions are too broad", Detail: runsRoot + " mode " + emptyDefault(mode, "unknown"), Hint: "Run chmod go-rwx " + remote.ShellQuote(runsRoot)}
	}
	return Check{Name: "worker.runRoot", Status: StatusPass, Required: true, Message: "worker run root is private and writable", Detail: runsRoot + " mode " + mode}
}

func workerPortRangeAvailable(ctx context.Context, runner Runner, worker, rawRange string, timeout time.Duration) Check {
	start, end, err := parsePortRange(rawRange)
	if err != nil {
		return Check{Name: "worker.ports", Status: StatusFail, Required: true, Message: "worker port range is invalid", Detail: err.Error(), Hint: "Set worker.portRange to a range like 3100-3999"}
	}
	script := fmt.Sprintf(`python3 - %d %d <<'PY'
import socket
import sys
start = int(sys.argv[1])
end = int(sys.argv[2])
for port in range(start, end + 1):
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    try:
        sock.bind(("127.0.0.1", port))
        print(port)
        sys.exit(0)
    except OSError:
        pass
    finally:
        sock.close()
sys.exit(2)
PY`, start, end)
	res, err := sshRun(ctx, runner, worker, script, timeout)
	if err != nil {
		return Check{Name: "worker.ports", Status: StatusFail, Required: true, Message: "no available port was found in the worker range", Detail: trimOutput(res.Stdout, res.Stderr), Hint: "Free a port in " + rawRange + " or adjust worker.portRange"}
	}
	return Check{Name: "worker.ports", Status: StatusPass, Required: true, Message: "worker port range has at least one available port", Detail: strings.TrimSpace(res.Stdout)}
}

func sshRun(ctx context.Context, runner Runner, worker, command string, timeout time.Duration) (CommandResult, error) {
	return runner.Run(ctx, "ssh", []string{"-o", "BatchMode=yes", "--", worker, command}, timeout)
}

func workerBinaryPath(home, remoteBinary string) string {
	if strings.TrimSpace(remoteBinary) == "" {
		return path.Join(home, ".workyard", "bin", "workyard")
	}
	return expandRemotePath(home, remoteBinary)
}

func runsRootPath(home, remoteRoot string) (string, error) {
	paths, err := remote.BuildPaths(home, remoteRoot, "doctor", "doctor")
	if err != nil {
		return "", err
	}
	return path.Dir(path.Dir(paths.RunRoot)), nil
}

func expandRemotePath(home, value string) string {
	value = strings.TrimSpace(value)
	if value == "~" {
		return home
	}
	if strings.HasPrefix(value, "~/") {
		return path.Join(home, strings.TrimPrefix(value, "~/"))
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	return path.Join(home, value)
}

func privateMode(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	mode, err := strconv.ParseInt(raw, 8, 64)
	if err != nil {
		return false
	}
	return mode&0o077 == 0
}

func configuredPortRange(project string) string {
	loaded, err := config.Load(project)
	if err == nil && strings.TrimSpace(loaded.Config.Worker.PortRange) != "" {
		return loaded.Config.Worker.PortRange
	}
	return "3100-3999"
}

func parsePortRange(raw string) (int, int, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "3100-3999"
	}
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("port range must look like 3100-3999")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, err
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, err
	}
	if start < 1 || end > 65535 || start > end {
		return 0, 0, fmt.Errorf("invalid port range %q", raw)
	}
	return start, end, nil
}

func trimOutput(stdout, stderr string) string {
	out := strings.TrimSpace(stdout)
	err := strings.TrimSpace(stderr)
	switch {
	case out != "" && err != "":
		return firstLine(out) + " / " + firstLine(err)
	case out != "":
		return firstLine(out)
	default:
		return firstLine(err)
	}
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	line := strings.Split(value, "\n")[0]
	if len(line) > 240 {
		return line[:240]
	}
	return line
}

func emptyDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
