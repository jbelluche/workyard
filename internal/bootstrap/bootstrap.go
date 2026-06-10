package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/doctor"
	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
)

const (
	StatusPass = "pass"
	StatusWarn = "warn"
	StatusFail = "fail"
	StatusSkip = "skip"
	StatusPlan = "plan"
)

type Options struct {
	Worker         string
	ConfigPath     string
	ConfigRequired bool
	StateDir       string
	RemoteRoot     string
	RemoteBinary   string
	Version        string
	ArtifactDir    string
	LocalBinary    string
	SudoPassword   string
	DryRun         bool
}

type Report struct {
	OK           bool           `json:"ok"`
	WorkerName   string         `json:"workerName"`
	Worker       string         `json:"worker"`
	ConfigPath   string         `json:"configPath,omitempty"`
	ConfigFound  bool           `json:"configFound"`
	DryRun       bool           `json:"dryRun,omitempty"`
	GeneratedAt  time.Time      `json:"generatedAt"`
	Steps        []Step         `json:"steps"`
	DoctorReport *doctor.Report `json:"doctor,omitempty"`
}

type Step struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Required bool   `json:"required"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type resolvedWorker struct {
	Name   string
	Host   string
	User   string
	Target string
	Spec   WorkerSpec
	Source string
}

func Run(ctx context.Context, opts Options) (Report, error) {
	if strings.TrimSpace(opts.ConfigPath) == "" {
		opts.ConfigPath = DefaultConfigName
	}
	if strings.TrimSpace(opts.ArtifactDir) == "" {
		opts.ArtifactDir = "dist"
	}
	cfg, found, err := LoadConfig(opts.ConfigPath, opts.ConfigRequired)
	if err != nil {
		return Report{}, err
	}
	resolved, err := ResolveWorker(opts.Worker, cfg, registry.DefaultWorkersPath(opts.StateDir))
	if err != nil {
		return Report{}, err
	}
	report := Report{
		OK:          true,
		WorkerName:  resolved.Name,
		Worker:      resolved.Target,
		ConfigPath:  opts.ConfigPath,
		ConfigFound: found,
		DryRun:      opts.DryRun,
		GeneratedAt: time.Now().UTC(),
	}
	report.add(Step{Name: "worker.resolve", Status: StatusPass, Required: true, Message: "worker target resolved", Detail: resolved.Target})
	if opts.DryRun {
		addPlannedSteps(&report, resolved)
		report.finalize()
		return report, nil
	}
	if err := remote.ValidateWorker(resolved.Target); err != nil {
		report.add(Step{Name: "worker.validate", Status: StatusFail, Required: true, Message: "worker SSH target is invalid", Detail: err.Error(), Hint: "Use a target like user@host"})
		report.finalize()
		return report, nil
	}
	home, err := remote.Home(ctx, resolved.Target)
	if err != nil {
		report.add(Step{Name: "ssh", Status: StatusFail, Required: true, Message: "worker SSH connection failed", Detail: err.Error(), Hint: "Confirm passwordless SSH works with ssh -o BatchMode=yes -- " + resolved.Target})
		report.finalize()
		return report, nil
	}
	report.add(Step{Name: "ssh", Status: StatusPass, Required: true, Message: "worker SSH connection succeeded", Detail: resolved.Target + ":" + home})
	ensureTailscale(ctx, &report, resolved.Target, resolved.Spec)
	if !report.OKSoFar() {
		report.finalize()
		return report, nil
	}
	if err := prepareWorkyardDirs(ctx, resolved.Target, home, opts.RemoteRoot); err != nil {
		report.add(Step{Name: "directories", Status: StatusFail, Required: true, Message: "failed to prepare private Workyard directories", Detail: err.Error(), Hint: "Inspect ~/.workyard on the worker"})
		report.finalize()
		return report, nil
	}
	report.add(Step{Name: "directories", Status: StatusPass, Required: true, Message: "private Workyard directories are ready", Detail: path.Join(home, ".workyard")})

	sudo := sudoAuth{Password: opts.SudoPassword}
	ensurePackages(ctx, &report, resolved.Target, resolved.Spec, sudo)
	if !report.OKSoFar() {
		report.finalize()
		return report, nil
	}
	ensureDocker(ctx, &report, resolved.Target, resolved.Spec, sudo)
	if !report.OKSoFar() {
		report.finalize()
		return report, nil
	}
	var platform remote.Platform
	if boolDefault(resolved.Spec.Workyard.Install, true) {
		platform, err = remote.DetectPlatform(ctx, resolved.Target)
		if err != nil {
			report.add(Step{Name: "workyard.platform", Status: StatusFail, Required: true, Message: "failed to detect worker platform", Detail: err.Error(), Hint: "Check SSH access and worker OS/architecture"})
			report.finalize()
			return report, nil
		}
		report.add(Step{Name: "workyard.platform", Status: StatusPass, Required: true, Message: "worker platform detected", Detail: platform.OS + "/" + platform.Arch})
		localBinary, err := ensureLocalBinary(ctx, platform, opts)
		if err != nil {
			report.add(Step{Name: "workyard.build", Status: StatusFail, Required: true, Message: "failed to build matching worker binary", Detail: err.Error(), Hint: "Run GOOS=" + platform.OS + " GOARCH=" + platform.Arch + " go build -o " + filepath.Join(opts.ArtifactDir, platform.ArtifactName()) + " ./cmd/workyard"})
			report.finalize()
			return report, nil
		}
		report.add(Step{Name: "workyard.build", Status: StatusPass, Required: true, Message: "matching worker binary is available", Detail: localBinary})
		install, err := remote.InstallBinary(ctx, resolved.Target, platform, remote.InstallOptions{
			LocalBinary:     localBinary,
			RemoteBinary:    opts.RemoteBinary,
			ExpectedVersion: opts.Version,
		})
		if err != nil {
			report.add(Step{Name: "workyard.install", Status: StatusFail, Required: true, Message: "failed to install worker binary", Detail: err.Error(), Hint: "Check ~/.workyard/bin permissions on the worker"})
			report.finalize()
			return report, nil
		}
		report.add(Step{Name: "workyard.install", Status: StatusPass, Required: true, Message: "worker binary installed", Detail: install.RemoteBinary + " version " + install.InstalledVersion})
	} else {
		report.add(Step{Name: "workyard.install", Status: StatusSkip, Required: false, Message: "worker binary install disabled by bootstrap config"})
	}
	if boolDefault(resolved.Spec.Workyard.Daemon, true) {
		paths, err := remote.DaemonPaths(home, opts.RemoteBinary)
		if err != nil {
			report.add(Step{Name: "workyard.daemon", Status: StatusFail, Required: true, Message: "failed to resolve daemon paths", Detail: err.Error()})
			report.finalize()
			return report, nil
		}
		if boolDefault(resolved.Spec.Workyard.Install, true) {
			err = remote.RestartDaemon(ctx, resolved.Target, paths, "")
		} else {
			err = remote.EnsureDaemon(ctx, resolved.Target, paths, "")
		}
		if err != nil {
			report.add(Step{Name: "workyard.daemon", Status: StatusFail, Required: true, Message: "worker daemon did not start", Detail: err.Error(), Hint: "Inspect ~/.workyard/daemon/daemon.log on the worker"})
			report.finalize()
			return report, nil
		}
		report.add(Step{Name: "workyard.daemon", Status: StatusPass, Required: true, Message: "worker daemon is running", Detail: paths.Socket})
	} else {
		report.add(Step{Name: "workyard.daemon", Status: StatusSkip, Required: false, Message: "daemon setup disabled by bootstrap config"})
	}
	if boolDefault(resolved.Spec.Register, true) {
		if err := registerWorker(opts.StateDir, resolved); err != nil {
			report.add(Step{Name: "registry", Status: StatusFail, Required: true, Message: "failed to register worker locally", Detail: err.Error(), Hint: "Check " + registry.DefaultWorkersPath(opts.StateDir)})
			report.finalize()
			return report, nil
		}
		report.add(Step{Name: "registry", Status: StatusPass, Required: true, Message: "worker registered locally", Detail: registry.DefaultWorkersPath(opts.StateDir)})
	} else {
		report.add(Step{Name: "registry", Status: StatusSkip, Required: false, Message: "local worker registration disabled by bootstrap config"})
	}
	if boolDefault(resolved.Spec.Checks.Doctor, true) {
		doctorReport := doctor.Run(ctx, doctor.Options{
			Worker:       resolved.Target,
			RemoteRoot:   opts.RemoteRoot,
			RemoteBinary: opts.RemoteBinary,
			Version:      opts.Version,
			CheckProject: false,
			Timeout:      8 * time.Second,
		}, doctor.SystemRunner{})
		report.DoctorReport = &doctorReport
		if !doctorReport.OK {
			report.add(Step{Name: "doctor", Status: StatusFail, Required: true, Message: "final worker doctor failed", Hint: "Run workyard --worker " + resolved.Target + " doctor"})
		} else {
			report.add(Step{Name: "doctor", Status: StatusPass, Required: true, Message: "final worker doctor passed"})
		}
	} else {
		report.add(Step{Name: "doctor", Status: StatusSkip, Required: false, Message: "final doctor disabled by bootstrap config"})
	}
	report.finalize()
	return report, nil
}

func ResolveWorker(name string, cfg Config, workerConfigPath string) (resolvedWorker, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return resolvedWorker{}, errors.New("worker name is required")
	}
	if cfg.Workers != nil {
		if spec, ok := cfg.Workers[name]; ok {
			return resolveFromSpec(name, spec)
		}
	}
	store := registry.NewWorkerStore(workerConfigPath)
	if worker, ok, err := store.Resolve(name); err != nil {
		return resolvedWorker{}, err
	} else if ok {
		return resolvedWorker{
			Name:   worker.Name,
			Host:   worker.Host,
			User:   worker.User,
			Target: worker.EffectiveSSHTarget(),
			Source: "registry",
		}, nil
	}
	user, host, hasUser := splitTarget(name)
	if !hasUser {
		host = name
		user = currentUsername()
	}
	if user == "" {
		return resolvedWorker{}, errors.New("could not infer SSH username; add worker to bootstrap config or local registry")
	}
	return resolvedWorker{Name: workerDisplayName(host), Host: host, User: user, Target: user + "@" + host, Source: "argument"}, nil
}

func resolveFromSpec(name string, spec WorkerSpec) (resolvedWorker, error) {
	target := strings.TrimSpace(spec.SSH.Target)
	user := strings.TrimSpace(spec.SSH.User)
	host := strings.TrimSpace(spec.SSH.Host)
	if target != "" {
		targetUser, targetHost, hasUser := splitTarget(target)
		if hasUser {
			if user == "" {
				user = targetUser
			}
			if host == "" {
				host = targetHost
			}
		} else if host == "" {
			host = target
		}
	}
	if host == "" {
		return resolvedWorker{}, fmt.Errorf("worker %q must set ssh.host or ssh.target", name)
	}
	if user == "" {
		user = currentUsername()
	}
	if user == "" {
		return resolvedWorker{}, fmt.Errorf("worker %q must set ssh.user", name)
	}
	if target == "" {
		target = user + "@" + host
	}
	return resolvedWorker{Name: name, Host: host, User: user, Target: target, Spec: spec, Source: "config"}, nil
}

func addPlannedSteps(report *Report, resolved resolvedWorker) {
	steps := []Step{
		{Name: "ssh", Status: StatusPlan, Required: true, Message: "would verify passwordless SSH", Detail: resolved.Target},
		{Name: "directories", Status: StatusPlan, Required: true, Message: "would create ~/.workyard directories and set private permissions"},
	}
	if packagesInstallEnabled(resolved.Spec) {
		steps = append(steps, Step{Name: "packages", Status: StatusPlan, Required: true, Message: "would install apt packages", Detail: packageDetail(packageList(resolved.Spec))})
	} else {
		steps = append(steps, Step{Name: "rsync", Status: StatusPlan, Required: true, Message: "would verify rsync is installed"})
	}
	if dockerSetupEnabled(resolved.Spec) {
		steps = append(steps, Step{Name: "docker", Status: StatusPlan, Required: boolDefault(resolved.Spec.Docker.Required, false), Message: "would ensure Docker is installed and running", Detail: packageDetail(dockerPackages(resolved.Spec))})
	}
	if boolDefault(resolved.Spec.Tailscale.RequireConnected, false) {
		steps = append(steps, Step{Name: "tailscale", Status: StatusPlan, Required: true, Message: "would verify worker Tailscale is connected"})
	}
	if boolDefault(resolved.Spec.Workyard.Install, true) {
		steps = append(steps, Step{Name: "workyard.install", Status: StatusPlan, Required: true, Message: "would build/upload matching worker binary"})
	}
	if boolDefault(resolved.Spec.Workyard.Daemon, true) {
		steps = append(steps, Step{Name: "workyard.daemon", Status: StatusPlan, Required: true, Message: "would start or restart the worker daemon"})
	}
	if boolDefault(resolved.Spec.Register, true) {
		steps = append(steps, Step{Name: "registry", Status: StatusPlan, Required: true, Message: "would register worker locally"})
	}
	if boolDefault(resolved.Spec.Checks.Doctor, true) {
		steps = append(steps, Step{Name: "doctor", Status: StatusPlan, Required: true, Message: "would run final worker doctor"})
	}
	for _, step := range steps {
		report.add(step)
	}
}

func (r *Report) add(step Step) {
	r.Steps = append(r.Steps, step)
}

func (r Report) OKSoFar() bool {
	for _, step := range r.Steps {
		if step.Required && step.Status == StatusFail {
			return false
		}
	}
	return true
}

func (r *Report) finalize() {
	r.OK = r.OKSoFar()
}

func prepareWorkyardDirs(ctx context.Context, worker, home, remoteRoot string) error {
	paths, err := remote.BuildPaths(home, remoteRoot, "bootstrap", "bootstrap")
	if err != nil {
		return err
	}
	runsRoot := path.Dir(path.Dir(paths.RunRoot))
	workyardDir := path.Join(home, ".workyard")
	binDir := path.Join(workyardDir, "bin")
	daemonDir := path.Join(workyardDir, "daemon")
	script := strings.Join([]string{
		"set -eu",
		"for p in " + quoteList([]string{workyardDir, binDir, runsRoot, daemonDir}) + "; do if [ -L \"$p\" ]; then printf 'refusing symlink path: %s\\n' \"$p\" >&2; exit 1; fi; done",
		"mkdir -p " + quoteList([]string{workyardDir, binDir, runsRoot, daemonDir}),
		"chmod go-rwx " + quoteList([]string{workyardDir, binDir, runsRoot, daemonDir}),
	}, "\n")
	_, err = remote.Run(ctx, worker, []string{"sh", "-lc", script}, nil, 20*time.Second)
	return err
}

func ensureTailscale(ctx context.Context, report *Report, worker string, spec WorkerSpec) {
	if !boolDefault(spec.Tailscale.RequireConnected, false) {
		return
	}
	res, err := remote.Run(ctx, worker, []string{"tailscale", "status", "--json"}, nil, 10*time.Second)
	if err != nil {
		report.add(Step{Name: "tailscale", Status: StatusFail, Required: true, Message: "worker Tailscale is not connected or not installed", Detail: err.Error(), Hint: "Install Tailscale on the worker and log in"})
		return
	}
	var status struct {
		BackendState string `json:"BackendState"`
		Self         *struct {
			Online bool `json:"Online"`
		} `json:"Self"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &status); err != nil {
		report.add(Step{Name: "tailscale", Status: StatusFail, Required: true, Message: "worker Tailscale status was not valid JSON", Detail: err.Error(), Hint: "Run tailscale status --json on the worker"})
		return
	}
	if status.BackendState != "Running" || status.Self == nil || !status.Self.Online {
		report.add(Step{Name: "tailscale", Status: StatusFail, Required: true, Message: "worker Tailscale is not connected", Detail: "backend state: " + status.BackendState, Hint: "Start Tailscale on the worker and confirm it appears online"})
		return
	}
	report.add(Step{Name: "tailscale", Status: StatusPass, Required: true, Message: "worker Tailscale is connected"})
}

func ensurePackages(ctx context.Context, report *Report, worker string, spec WorkerSpec, sudo sudoAuth) {
	if packagesInstallEnabled(spec) {
		packages := packageList(spec)
		if err := aptInstall(ctx, worker, packages, sudo); err != nil {
			report.add(Step{Name: "packages", Status: StatusFail, Required: true, Message: "failed to install required apt packages", Detail: err.Error(), Hint: sudoManualHint(sudo, "sudo apt-get update && sudo apt-get install -y "+packageInstallArgsString(packages))})
			return
		}
		report.add(Step{Name: "packages", Status: StatusPass, Required: true, Message: "required apt packages installed", Detail: packageDetail(packages)})
		return
	}
	if commandExists(ctx, worker, "rsync") {
		report.add(Step{Name: "rsync", Status: StatusPass, Required: true, Message: "rsync is installed"})
		return
	}
	report.add(Step{Name: "rsync", Status: StatusFail, Required: true, Message: "rsync is not installed on the worker", Hint: "Install rsync on the worker or set packages.install: true in " + DefaultConfigName})
}

func ensureDocker(ctx context.Context, report *Report, worker string, spec WorkerSpec, sudo sudoAuth) {
	if !dockerSetupEnabled(spec) {
		return
	}
	required := boolDefault(spec.Docker.Required, false) || boolDefault(spec.Docker.Install, false)
	if boolDefault(spec.Docker.Install, false) {
		packages := dockerPackages(spec)
		if err := aptInstall(ctx, worker, packages, sudo); err != nil {
			report.add(Step{Name: "docker", Status: StatusFail, Required: required, Message: "failed to install Docker with apt", Detail: err.Error(), Hint: sudoManualHint(sudo, "sudo apt-get update && sudo apt-get install -y "+packageInstallArgsString(packages))})
			return
		}
		if err := startDocker(ctx, worker, sudo); err != nil {
			report.add(Step{Name: "docker", Status: StatusWarn, Required: false, Message: "docker installed but could not be started", Detail: err.Error(), Hint: sudoManualHint(sudo, "sudo systemctl enable --now docker")})
		} else {
			report.add(Step{Name: "docker", Status: StatusPass, Required: required, Message: "docker is installed and running", Detail: packageDetail(packages)})
		}
		if boolDefault(spec.Docker.AddUserToGroup, false) {
			ensureDockerGroup(ctx, report, worker, sudo)
		}
		return
	}
	if commandExists(ctx, worker, "docker") {
		step := Step{Name: "docker", Status: StatusPass, Required: required, Message: "docker is installed"}
		if err := startDocker(ctx, worker, sudo); err != nil {
			step.Status = StatusWarn
			step.Message = "docker is installed but could not be started"
			step.Detail = err.Error()
			step.Hint = sudoManualHint(sudo, "sudo systemctl enable --now docker")
		} else if composeRequired(spec) && !dockerComposeExists(ctx, worker) {
			step.Status = StatusWarn
			step.Message = "docker is installed but compose plugin was not found"
			step.Hint = "Install the docker compose plugin on the worker"
		}
		report.add(step)
		if boolDefault(spec.Docker.AddUserToGroup, false) {
			ensureDockerGroup(ctx, report, worker, sudo)
		}
		return
	}
	if !boolDefault(spec.Docker.Install, false) {
		report.add(Step{Name: "docker", Status: StatusFail, Required: required, Message: "docker is not installed on the worker", Hint: "Set docker.install: true in " + DefaultConfigName + " or install Docker manually"})
		return
	}
	if boolDefault(spec.Docker.AddUserToGroup, false) {
		ensureDockerGroup(ctx, report, worker, sudo)
	}
}

func ensureDockerGroup(ctx context.Context, report *Report, worker string, sudo sudoAuth) {
	if err := addUserToDockerGroup(ctx, worker, sudo); err != nil {
		report.add(Step{Name: "docker.group", Status: StatusWarn, Required: false, Message: "could not add SSH user to docker group", Detail: err.Error(), Hint: sudoManualHint(sudo, "sudo usermod -aG docker $USER")})
	} else {
		report.add(Step{Name: "docker.group", Status: StatusWarn, Required: false, Message: "SSH user added to docker group", Hint: "Open a new SSH session before running docker without sudo"})
	}
}

func ensureLocalBinary(ctx context.Context, platform remote.Platform, opts Options) (string, error) {
	return remote.EnsureArtifact(ctx, platform, opts.ArtifactDir, opts.LocalBinary, opts.Version)
}

func registerWorker(stateDir string, resolved resolvedWorker) error {
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	worker := registry.WorkerConfig{
		Name:      resolved.Name,
		Host:      resolved.Host,
		User:      resolved.User,
		SSHTarget: "",
		Source:    "bootstrap",
	}
	for _, key := range []string{resolved.Name, resolved.Target, resolved.Host} {
		existing, ok, err := store.Resolve(key)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if existing.Source != "" {
			worker.Source = existing.Source
		}
		worker.DNSName = existing.DNSName
		worker.TailscaleIPs = append([]string(nil), existing.TailscaleIPs...)
		break
	}
	if resolved.Target != resolved.User+"@"+resolved.Host {
		worker.SSHTarget = resolved.Target
	}
	return store.Upsert(worker)
}

func packagesInstallEnabled(spec WorkerSpec) bool {
	return boolDefault(spec.Packages.Install, len(spec.Packages.Apt) > 0)
}

func packageList(spec WorkerSpec) []AptPackage {
	return mergePackages(append([]AptPackage{{Name: "rsync"}}, spec.Packages.Apt...))
}

func dockerPackages(spec WorkerSpec) []AptPackage {
	packages := []AptPackage{{Name: "docker.io", Version: strings.TrimSpace(spec.Docker.Version)}}
	if composeRequired(spec) {
		packages = append(packages, AptPackage{Name: "docker-compose-plugin", Version: strings.TrimSpace(spec.Docker.ComposeVersion)})
	}
	return mergePackages(packages)
}

func mergePackages(packages []AptPackage) []AptPackage {
	byName := map[string]AptPackage{}
	for _, pkg := range packages {
		pkg.Name = strings.TrimSpace(pkg.Name)
		pkg.Version = strings.TrimSpace(pkg.Version)
		if pkg.Name == "" {
			continue
		}
		existing, ok := byName[pkg.Name]
		if !ok || pkg.Version != "" || existing.Version == "" {
			byName[pkg.Name] = pkg
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AptPackage, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

func packageInstallArgs(packages []AptPackage) []string {
	args := make([]string, 0, len(packages))
	for _, pkg := range packages {
		if strings.TrimSpace(pkg.Version) == "" {
			args = append(args, pkg.Name)
			continue
		}
		args = append(args, pkg.Name+"="+pkg.Version)
	}
	return args
}

func packageInstallArgsString(packages []AptPackage) string {
	return strings.Join(packageInstallArgs(packages), " ")
}

func packageDetail(packages []AptPackage) string {
	return strings.Join(packageInstallArgs(packages), ", ")
}

func dockerSetupEnabled(spec WorkerSpec) bool {
	return boolDefault(spec.Docker.Required, false) || boolDefault(spec.Docker.Install, false) || boolDefault(spec.Docker.ComposePlugin, false) || boolDefault(spec.Docker.AddUserToGroup, false)
}

func composeRequired(spec WorkerSpec) bool {
	return boolDefault(spec.Docker.ComposePlugin, true)
}

func commandExists(ctx context.Context, worker, command string) bool {
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", "command -v " + remote.ShellQuote(command) + " >/dev/null 2>&1"}, nil, 8*time.Second)
	return err == nil
}

func dockerComposeExists(ctx context.Context, worker string) bool {
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", "docker compose version >/dev/null 2>&1"}, nil, 8*time.Second)
	return err == nil
}

type sudoAuth struct {
	Password string
}

func (s sudoAuth) hasPassword() bool {
	return s.Password != ""
}

func (s sudoAuth) stdin() []byte {
	if !s.hasPassword() {
		return nil
	}
	return []byte(s.Password + "\n")
}

func (s sudoAuth) prelude(context string) []string {
	if !s.hasPassword() {
		return []string{
			"sudo_cmd() { sudo -n \"$@\"; }",
			"if ! sudo_cmd true >/dev/null 2>&1; then printf 'passwordless sudo is required for " + context + "\\n' >&2; exit 3; fi",
		}
	}
	return []string{
		"IFS= read -r WORKYARD_SUDO_PASSWORD || { printf 'sudo password was not provided\\n' >&2; exit 3; }",
		"sudo_cmd() { printf '%s\\n' \"$WORKYARD_SUDO_PASSWORD\" | sudo -S -p '' \"$@\"; }",
		"if ! sudo_cmd true >/dev/null 2>&1; then printf 'sudo authentication failed for " + context + "\\n' >&2; exit 3; fi",
	}
}

func (s sudoAuth) command(args ...string) string {
	return "sudo_cmd " + quoteList(args)
}

func sudoManualHint(s sudoAuth, command string) string {
	if s.hasPassword() {
		return "Check the sudo password or run manually on the worker: " + command
	}
	return "Run manually on the worker: " + command
}

func aptInstall(ctx context.Context, worker string, packages []AptPackage, sudo sudoAuth) error {
	if len(packages) == 0 {
		return nil
	}
	args := packageInstallArgs(packages)
	lines := []string{
		"set -eu",
		"if ! command -v apt-get >/dev/null 2>&1; then printf 'apt-get not found\\n' >&2; exit 2; fi",
	}
	lines = append(lines, sudo.prelude("package install")...)
	lines = append(lines,
		sudo.command("apt-get", "update"),
		sudo.command(append([]string{"env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y"}, args...)...),
	)
	script := strings.Join(lines, "\n")
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, sudo.stdin(), 5*time.Minute)
	return err
}

func startDocker(ctx context.Context, worker string, sudo sudoAuth) error {
	lines := []string{
		"set -eu",
	}
	lines = append(lines, sudo.prelude("docker service management")...)
	lines = append(lines,
		"if command -v systemctl >/dev/null 2>&1; then",
		"  "+sudo.command("systemctl", "enable", "--now", "docker")+" >/dev/null 2>&1 || "+sudo.command("systemctl", "start", "docker")+" >/dev/null 2>&1 || true",
		"fi",
		"docker version >/dev/null 2>&1 || "+sudo.command("docker", "version")+" >/dev/null 2>&1",
	)
	script := strings.Join(lines, "\n")
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, sudo.stdin(), 30*time.Second)
	return err
}

func addUserToDockerGroup(ctx context.Context, worker string, sudo sudoAuth) error {
	lines := []string{
		"set -eu",
	}
	lines = append(lines, sudo.prelude("docker group setup")...)
	lines = append(lines,
		"current_user=$(id -un)",
		"if ! getent group docker >/dev/null 2>&1; then "+sudo.command("groupadd", "docker")+"; fi",
		"sudo_cmd usermod -aG docker \"$current_user\"",
	)
	script := strings.Join(lines, "\n")
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, sudo.stdin(), 20*time.Second)
	return err
}

func quoteList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, remote.ShellQuote(value))
	}
	return strings.Join(quoted, " ")
}

func splitTarget(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", value, false
	}
	return parts[0], parts[1], true
}

func currentUsername() string {
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if current, err := osuser.Current(); err == nil && current != nil {
		value := strings.TrimSpace(current.Username)
		if strings.Contains(value, "\\") {
			parts := strings.Split(value, "\\")
			return parts[len(parts)-1]
		}
		return value
	}
	return ""
}

func workerDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		_, host, ok := splitTarget(value)
		if ok {
			value = host
		}
	}
	value = strings.TrimSuffix(value, ".")
	if strings.Contains(value, ".") {
		return strings.Split(value, ".")[0]
	}
	return value
}
