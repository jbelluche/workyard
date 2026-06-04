package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	osuser "os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jackbelluche/workyard/internal/bootstrap"
	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/doctor"
	"github.com/jackbelluche/workyard/internal/monitor"
	"github.com/jackbelluche/workyard/internal/output"
	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/jackbelluche/workyard/internal/runid"
	"github.com/jackbelluche/workyard/internal/syncer"
	watcher "github.com/jackbelluche/workyard/internal/watch"
	"github.com/jackbelluche/workyard/internal/worker"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const Version = "0.1.0"

type options struct {
	json         bool
	quiet        bool
	verbose      bool
	project      string
	worker       string
	run          string
	remoteRoot   string
	remoteBinary string
	socket       string
	stateDir     string
}

type daemonLaunchResult struct {
	PID    int
	Socket string
	Log    string
}

type printedError struct {
	err      error
	exitCode int
}

func (e printedError) Error() string { return e.err.Error() }

func Execute() error {
	return ExecuteContext(context.Background())
}

func ExecuteContext(ctx context.Context) error {
	opts := &options{}
	root := newRoot(opts)
	if err := root.ExecuteContext(ctx); err != nil {
		var pe printedError
		if errors.As(err, &pe) {
			return pe
		}
		if opts.json {
			_ = output.WriteErrorJSON(os.Stdout, err)
		} else {
			output.HumanError(os.Stderr, err)
		}
		return err
	}
	return nil
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var pe printedError
	if errors.As(err, &pe) && pe.exitCode > 0 {
		return pe.exitCode
	}
	ce := output.AsCommandError(err)
	if ce != nil && ce.ExitCode > 0 {
		return ce.ExitCode
	}
	return 1
}

func newRoot(opts *options) *cobra.Command {
	root := &cobra.Command{
		Use:           "workyard",
		Short:         "Agent-first remote development runner",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.worker == "" {
				return nil
			}
			resolved, err := resolveWorkerTarget(opts.stateDir, opts.worker)
			if err != nil {
				return output.NewError("WORKER_CONFIG_INVALID", err.Error(), "Run workyard workers config show")
			}
			opts.worker = resolved
			return nil
		},
	}
	root.PersistentFlags().BoolVar(&opts.json, "json", false, "emit machine-readable JSON")
	root.PersistentFlags().BoolVar(&opts.quiet, "quiet", false, "suppress progress output")
	root.PersistentFlags().BoolVar(&opts.verbose, "verbose", false, "emit diagnostic detail")
	root.PersistentFlags().StringVar(&opts.project, "project", ".", "project path")
	root.PersistentFlags().StringVar(&opts.worker, "worker", "", "SSH worker host")
	root.PersistentFlags().StringVar(&opts.run, "run", "", "run id")
	root.PersistentFlags().StringVar(&opts.remoteRoot, "remote-root", "", "remote Workyard runs root")
	root.PersistentFlags().StringVar(&opts.remoteBinary, "remote-binary", "", "remote workyard binary path")
	root.PersistentFlags().StringVar(&opts.socket, "socket", "", "daemon Unix socket path")
	root.PersistentFlags().StringVar(&opts.stateDir, "state-dir", "", "worker state directory")

	root.AddCommand(initCommand(opts))
	root.AddCommand(doctorCommand(opts))
	root.AddCommand(configCommand(opts))
	root.AddCommand(servicesCommand(opts))
	root.AddCommand(deployCommand(opts))
	root.AddCommand(syncCommand(opts))
	root.AddCommand(installCommand(opts))
	root.AddCommand(daemonCommand(opts))
	root.AddCommand(daemonctlCommand(opts))
	root.AddCommand(controlCommand(opts, "setup"))
	root.AddCommand(controlCommand(opts, "build"))
	root.AddCommand(controlCommand(opts, "start"))
	root.AddCommand(controlCommand(opts, "stop"))
	root.AddCommand(controlCommand(opts, "restart"))
	root.AddCommand(controlCommand(opts, "status"))
	root.AddCommand(logsCommand(opts))
	root.AddCommand(eventsCommand(opts))
	root.AddCommand(controlCommand(opts, "inspect"))
	root.AddCommand(waitCommand(opts))
	root.AddCommand(controlCommand(opts, "urls"))
	root.AddCommand(controlCommand(opts, "probe"))
	root.AddCommand(watchCommand(opts))
	root.AddCommand(openCommand(opts))
	root.AddCommand(runsCommand(opts))
	root.AddCommand(workersCommand(opts))
	root.AddCommand(cleanupCommand(opts))
	root.AddCommand(serverCommand(opts))
	root.AddCommand(versionCommand(opts))
	return root
}

func installCommand(opts *options) *cobra.Command {
	var artifactDir string
	var localBinary string
	cmd := &cobra.Command{
		Use:     "install",
		Aliases: []string{"upgrade"},
		Short:   "Install or upgrade the Workyard binary on a worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.worker == "" {
				return output.NewError("WORKER_REQUIRED", "--worker is required for install", "")
			}
			platform, err := remote.DetectPlatform(cmd.Context(), opts.worker)
			if err != nil {
				return output.NewError("WORKER_PLATFORM_FAILED", err.Error(), "Check SSH access and worker OS/architecture")
			}
			binary := localBinary
			if binary == "" {
				if artifactDir == "" {
					artifactDir = "dist"
				}
				binary = filepath.Join(artifactDir, platform.ArtifactName())
			}
			res, err := remote.InstallBinary(cmd.Context(), opts.worker, platform, remote.InstallOptions{
				LocalBinary:     binary,
				RemoteBinary:    opts.remoteBinary,
				ExpectedVersion: Version,
			})
			if err != nil {
				return output.NewError("WORKER_INSTALL_FAILED", err.Error(), "Build the matching artifact first, for example GOOS="+platform.OS+" GOARCH="+platform.Arch+" go build -o "+binary+" ./cmd/workyard")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "installed %s to %s:%s (%s)\n", res.LocalBinary, res.Worker, res.RemoteBinary, res.InstalledVersion)
			return nil
		},
	}
	cmd.Flags().StringVar(&artifactDir, "artifact-dir", "dist", "directory containing workyard-<os>-<arch> artifacts")
	cmd.Flags().StringVar(&localBinary, "local-binary", "", "specific local binary to upload")
	return cmd
}

func runsCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "runs", Short: "Manage locally registered Workyard runs"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered runs",
		RunE: func(cmd *cobra.Command, args []string) error {
			store := registry.New(registry.DefaultPath(opts.stateDir))
			runs, err := store.List()
			if err != nil {
				return output.NewError("REGISTRY_READ_FAILED", err.Error(), "")
			}
			if opts.worker != "" {
				filtered := runs[:0]
				for _, ref := range runs {
					if ref.Worker == opts.worker {
						filtered = append(filtered, ref)
					}
				}
				runs = filtered
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "path": store.Path(), "runs": runs})
			}
			rows := make([][]string, 0, len(runs))
			for _, ref := range runs {
				rows = append(rows, []string{ref.Worker, ref.Project, ref.RunID, formatTime(ref.UpdatedAt)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"WORKER", "PROJECT", "RUN", "UPDATED"}, rows)
		},
	}
	remove := &cobra.Command{
		Use:   "remove <worker> <project> <run>",
		Short: "Remove a run from the local monitor registry",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := registry.New(registry.DefaultPath(opts.stateDir))
			removed, err := store.Remove(args[0], args[1], args[2])
			if err != nil {
				return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removed": removed})
			}
			if removed {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "removed")
			} else {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "not found")
			}
			return nil
		},
	}
	var olderThan time.Duration
	prune := &cobra.Command{
		Use:   "prune",
		Short: "Prune stale runs from the local monitor registry",
		RunE: func(cmd *cobra.Command, args []string) error {
			if olderThan <= 0 {
				return output.NewError("PRUNE_AGE_REQUIRED", "--older-than must be greater than zero", "")
			}
			store := registry.New(registry.DefaultPath(opts.stateDir))
			removed, err := store.Prune(time.Now().Add(-olderThan))
			if err != nil {
				return output.NewError("REGISTRY_PRUNE_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removed": removed, "removedCount": len(removed)})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %d stale run(s)\n", len(removed))
			return nil
		},
	}
	prune.Flags().DurationVar(&olderThan, "older-than", 168*time.Hour, "remove registry entries not updated within this duration")
	root.AddCommand(list, remove, prune)
	return root
}

func workersCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "workers", Short: "Discover and manage registered Workyard workers"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered workers",
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := workerListRows(opts)
			if err != nil {
				return err
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"ok":         true,
					"configPath": registry.DefaultWorkersPath(opts.stateDir),
					"runsPath":   registry.DefaultPath(opts.stateDir),
					"workers":    rows,
				})
			}
			tableRows := make([][]string, 0, len(rows))
			for _, row := range rows {
				tableRows = append(tableRows, []string{row.Name, row.SSHTarget, row.Source, fmt.Sprint(row.RunCount), formatTime(row.UpdatedAt)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"NAME", "SSH TARGET", "SOURCE", "RUNS", "UPDATED"}, tableRows)
		},
	}
	var addUser string
	var addName string
	var addSSHTarget string
	add := &cobra.Command{
		Use:   "add <tailscale-device-or-host>",
		Short: "Register a Tailscale device or SSH host as a worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			worker, err := buildWorkerConfig(cmd.Context(), opts, args[0], addName, addUser, addSSHTarget)
			if err != nil {
				return err
			}
			if err := remote.ValidateWorker(worker.EffectiveSSHTarget()); err != nil {
				return output.NewError("WORKER_INVALID", err.Error(), "")
			}
			store := registry.NewWorkerStore(registry.DefaultWorkersPath(opts.stateDir))
			if err := store.Upsert(worker); err != nil {
				return output.NewError("WORKER_CONFIG_WRITE_FAILED", err.Error(), "")
			}
			stored, ok, err := store.Resolve(worker.Name)
			if err != nil {
				return output.NewError("WORKER_CONFIG_READ_FAILED", err.Error(), "")
			}
			if ok {
				worker = stored
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "path": store.Path(), "worker": workerWithTarget(worker)})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "registered %s as %s\n", worker.Name, worker.EffectiveSSHTarget())
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\n", store.Path())
			return nil
		},
	}
	add.Flags().StringVar(&addUser, "user", "", "SSH username for the worker (defaults to this machine's user)")
	add.Flags().StringVar(&addName, "name", "", "local Workyard worker name")
	add.Flags().StringVar(&addSSHTarget, "ssh-target", "", "explicit SSH target override")

	discover := &cobra.Command{
		Use:   "discover",
		Short: "Show tracked and untracked Tailscale devices",
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := discoverWorkerRows(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"ok":         true,
					"configPath": registry.DefaultWorkersPath(opts.stateDir),
					"workers":    rows,
				})
			}
			tableRows := make([][]string, 0, len(rows))
			for _, row := range rows {
				tableRows = append(tableRows, []string{row.Name, row.Host, fmt.Sprint(row.Online), fmt.Sprint(row.Tracked), row.SSHTarget, firstString(row.TailscaleIPs)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"NAME", "HOST", "ONLINE", "TRACKED", "SSH TARGET", "IP"}, tableRows)
		},
	}
	remove := &cobra.Command{
		Use:   "remove <worker>",
		Short: "Remove a registered worker and its local run references",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workerStore := registry.NewWorkerStore(registry.DefaultWorkersPath(opts.stateDir))
			runStore := registry.New(registry.DefaultPath(opts.stateDir))
			ref, ok, err := workerStore.Resolve(args[0])
			if err != nil {
				return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
			}
			target := args[0]
			if ok {
				target = ref.EffectiveSSHTarget()
			}
			removedWorker, err := workerStore.Remove(args[0])
			if err != nil {
				return output.NewError("WORKER_CONFIG_REMOVE_FAILED", err.Error(), "")
			}
			count, err := runStore.RemoveWorker(target)
			if err != nil {
				return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
			}
			if ok && target != args[0] {
				extra, err := runStore.RemoveWorker(args[0])
				if err != nil {
					return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
				}
				count += extra
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removedWorker": removedWorker, "removedRunCount": count})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed worker=%t runRefs=%d\n", removedWorker, count)
			return nil
		},
	}
	var setupConfig string
	var setupDryRun bool
	var setupArtifactDir string
	var setupLocalBinary string
	var setupAskSudoPassword bool
	setup := &cobra.Command{
		Use:   "setup <worker>",
		Short: "Bootstrap a reachable machine as a Workyard worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sudoPassword string
			if setupAskSudoPassword && !setupDryRun {
				var err error
				sudoPassword, err = readHiddenPassword(cmd.ErrOrStderr(), "Sudo password for "+args[0]+": ")
				if err != nil {
					return output.NewError("SUDO_PASSWORD_FAILED", err.Error(), "Run without --ask-sudo-password or enable passwordless sudo on the worker")
				}
			}
			report, err := bootstrap.Run(cmd.Context(), bootstrap.Options{
				Worker:         args[0],
				ConfigPath:     setupConfig,
				ConfigRequired: cmd.Flags().Changed("config"),
				StateDir:       opts.stateDir,
				RemoteRoot:     opts.remoteRoot,
				RemoteBinary:   opts.remoteBinary,
				Version:        Version,
				ArtifactDir:    setupArtifactDir,
				LocalBinary:    setupLocalBinary,
				SudoPassword:   sudoPassword,
				DryRun:         setupDryRun,
			})
			if err != nil {
				return output.NewError("WORKER_SETUP_FAILED", err.Error(), "Check workyard.bootstrap.yaml")
			}
			if opts.json {
				if err := output.WriteJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				printBootstrapReport(cmd.OutOrStdout(), report)
			}
			if !report.OK {
				return printedError{err: errors.New("worker setup failed"), exitCode: 1}
			}
			return nil
		},
	}
	setup.Flags().StringVar(&setupConfig, "config", bootstrap.DefaultConfigName, "worker bootstrap config file")
	setup.Flags().BoolVar(&setupDryRun, "dry-run", false, "show setup steps without changing the worker")
	setup.Flags().StringVar(&setupArtifactDir, "artifact-dir", "dist", "directory containing or receiving workyard-<os>-<arch> artifacts")
	setup.Flags().StringVar(&setupLocalBinary, "local-binary", "", "specific local binary to upload")
	setup.Flags().BoolVar(&setupAskSudoPassword, "ask-sudo-password", false, "prompt for a sudo password for package and Docker setup")
	configCmd := &cobra.Command{Use: "config", Short: "Inspect worker configuration"}
	configShow := &cobra.Command{
		Use:   "show",
		Short: "Show registered workers and their current Tailscale status",
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, tailscaleErr, err := registeredWorkerStatusRows(cmd.Context(), opts)
			if err != nil {
				return err
			}
			if opts.json {
				res := map[string]any{
					"ok":          true,
					"path":        registry.DefaultWorkersPath(opts.stateDir),
					"workers":     rows,
					"workerCount": len(rows),
				}
				if tailscaleErr != nil {
					res["tailscaleError"] = tailscaleErr.Error()
				}
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "config: %s\n", registry.DefaultWorkersPath(opts.stateDir))
			if tailscaleErr != nil {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "tailscale: %s\n", tailscaleErr)
			}
			tableRows := make([][]string, 0, len(rows))
			for _, row := range rows {
				tableRows = append(tableRows, []string{row.Name, row.SSHTarget, optionalBool(row.OnlineKnown, row.Online), fmt.Sprint(row.InTailscale), formatTime(row.UpdatedAt)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"NAME", "SSH TARGET", "ONLINE", "TAILSCALE", "UPDATED"}, tableRows)
		},
	}
	configCmd.AddCommand(configShow)
	root.AddCommand(list, add, discover, remove, setup, configCmd)
	return root
}

type workerListRow struct {
	Name      string    `json:"name"`
	Host      string    `json:"host,omitempty"`
	User      string    `json:"user,omitempty"`
	SSHTarget string    `json:"sshTarget"`
	Source    string    `json:"source"`
	RunCount  int       `json:"runCount"`
	UpdatedAt time.Time `json:"updatedAt,omitempty,omitzero"`
}

type workerDiscoveryRow struct {
	Name         string   `json:"name"`
	Host         string   `json:"host"`
	DNSName      string   `json:"dnsName,omitempty"`
	TailscaleIPs []string `json:"tailscaleIPs,omitempty"`
	OS           string   `json:"os,omitempty"`
	Online       bool     `json:"online"`
	Self         bool     `json:"self,omitempty"`
	Tracked      bool     `json:"tracked"`
	SSHTarget    string   `json:"sshTarget,omitempty"`
	Source       string   `json:"source"`
}

type workerStatusRow struct {
	Name         string    `json:"name"`
	Host         string    `json:"host"`
	User         string    `json:"user"`
	SSHTarget    string    `json:"sshTarget"`
	Source       string    `json:"source,omitempty"`
	DNSName      string    `json:"dnsName,omitempty"`
	TailscaleIPs []string  `json:"tailscaleIPs,omitempty"`
	Online       bool      `json:"online"`
	OnlineKnown  bool      `json:"onlineKnown"`
	InTailscale  bool      `json:"inTailscale"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty,omitzero"`
}

type tailscaleStatusOutput struct {
	BackendState string                         `json:"BackendState"`
	Self         *tailscalePeerStatus           `json:"Self"`
	Peer         map[string]tailscalePeerStatus `json:"Peer"`
}

type tailscalePeerStatus struct {
	DNSName      string   `json:"DNSName"`
	HostName     string   `json:"HostName"`
	OS           string   `json:"OS"`
	Online       bool     `json:"Online"`
	TailscaleIPs []string `json:"TailscaleIPs"`
}

type tailscaleDevice struct {
	Name         string
	Host         string
	DNSName      string
	OS           string
	Online       bool
	Self         bool
	TailscaleIPs []string
}

func workerListRows(opts *options) ([]workerListRow, error) {
	workerStore := registry.NewWorkerStore(registry.DefaultWorkersPath(opts.stateDir))
	runStore := registry.New(registry.DefaultPath(opts.stateDir))
	registered, err := workerStore.List()
	if err != nil {
		return nil, output.NewError("WORKER_CONFIG_READ_FAILED", err.Error(), "")
	}
	runWorkers, err := runStore.Workers()
	if err != nil {
		return nil, output.NewError("REGISTRY_READ_FAILED", err.Error(), "")
	}
	runCounts := map[string]registry.WorkerRef{}
	for _, ref := range runWorkers {
		runCounts[ref.Worker] = ref
	}
	seen := map[string]bool{}
	rows := make([]workerListRow, 0, len(registered)+len(runWorkers))
	for _, worker := range registered {
		target := worker.EffectiveSSHTarget()
		runCount := 0
		updated := worker.UpdatedAt
		if ref, ok := runCounts[target]; ok {
			runCount = ref.RunCount
			if ref.UpdatedAt.After(updated) {
				updated = ref.UpdatedAt
			}
		}
		rows = append(rows, workerListRow{
			Name:      worker.Name,
			Host:      worker.Host,
			User:      worker.User,
			SSHTarget: target,
			Source:    firstNonEmpty(worker.Source, "manual"),
			RunCount:  runCount,
			UpdatedAt: updated,
		})
		seen[target] = true
		seen[worker.Name] = true
	}
	for _, ref := range runWorkers {
		if seen[ref.Worker] {
			continue
		}
		rows = append(rows, workerListRow{
			Name:      workerDisplayName(ref.Worker),
			SSHTarget: ref.Worker,
			Source:    "runs",
			RunCount:  ref.RunCount,
			UpdatedAt: ref.UpdatedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func discoverWorkerRows(ctx context.Context, opts *options) ([]workerDiscoveryRow, error) {
	devices, err := discoverTailscaleDevices(ctx)
	if err != nil {
		return nil, output.NewError("TAILSCALE_DISCOVER_FAILED", err.Error(), "Run tailscale status --json to inspect the local Tailscale state")
	}
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(opts.stateDir))
	registered, err := store.List()
	if err != nil {
		return nil, output.NewError("WORKER_CONFIG_READ_FAILED", err.Error(), "")
	}
	rows := mergeWorkerDiscovery(devices, registered)
	return rows, nil
}

func registeredWorkerStatusRows(ctx context.Context, opts *options) ([]workerStatusRow, error, error) {
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(opts.stateDir))
	registered, err := store.List()
	if err != nil {
		return nil, nil, output.NewError("WORKER_CONFIG_READ_FAILED", err.Error(), "")
	}
	devices, tailscaleErr := discoverTailscaleDevices(ctx)
	deviceByKey := tailscaleDeviceMap(devices)
	rows := make([]workerStatusRow, 0, len(registered))
	for _, worker := range registered {
		row := workerStatusRow{
			Name:      worker.Name,
			Host:      worker.Host,
			User:      worker.User,
			SSHTarget: worker.EffectiveSSHTarget(),
			Source:    firstNonEmpty(worker.Source, "manual"),
			DNSName:   worker.DNSName,
			UpdatedAt: worker.UpdatedAt,
		}
		if device, ok := lookupTailscaleDevice(deviceByKey, worker.Name, worker.Host, worker.DNSName); ok {
			row.InTailscale = true
			row.OnlineKnown = true
			row.Online = device.Online
			row.DNSName = firstNonEmpty(row.DNSName, device.DNSName)
			row.TailscaleIPs = device.TailscaleIPs
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, tailscaleErr, nil
}

func buildWorkerConfig(ctx context.Context, opts *options, raw, name, sshUser, sshTarget string) (registry.WorkerConfig, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return registry.WorkerConfig{}, output.NewError("WORKER_REQUIRED", "worker host is required", "")
	}
	inputUser, inputHost, hasInputUser := splitWorkerTarget(raw)
	host := inputHost
	if host == "" {
		host = raw
	}
	devices, _ := discoverTailscaleDevices(ctx)
	device, foundDevice := findTailscaleDevice(devices, host)
	if foundDevice {
		host = workerSSHHost(firstNonEmpty(device.Host, host), device.DNSName)
	}
	if sshTarget != "" {
		targetUser, targetHost, hasTargetUser := splitWorkerTarget(sshTarget)
		if hasTargetUser {
			inputUser = targetUser
			hasInputUser = true
		}
		if targetHost != "" {
			host = targetHost
		}
	}
	if sshUser == "" {
		if hasInputUser {
			sshUser = inputUser
		} else {
			sshUser = currentUsername()
		}
	}
	if sshUser == "" {
		return registry.WorkerConfig{}, output.NewError("WORKER_USER_REQUIRED", "could not infer SSH username", "Pass --user <ssh-user>")
	}
	if name == "" {
		if foundDevice {
			name = defaultWorkerName(device, host)
		} else {
			name = workerDisplayName(host)
		}
	}
	worker := registry.WorkerConfig{
		Name:   name,
		Host:   host,
		User:   sshUser,
		Source: "manual",
	}
	if foundDevice {
		worker.Source = "tailscale"
		worker.DNSName = device.DNSName
		worker.TailscaleIPs = device.TailscaleIPs
	}
	if sshTarget != "" {
		worker.SSHTarget = sshTarget
	}
	return worker, nil
}

func mergeWorkerDiscovery(devices []tailscaleDevice, registered []registry.WorkerConfig) []workerDiscoveryRow {
	registeredByKey := map[string]registry.WorkerConfig{}
	for _, worker := range registered {
		for _, key := range workerKeys(worker.Name, worker.Host, worker.DNSName, worker.EffectiveSSHTarget()) {
			registeredByKey[key] = worker
		}
	}
	seenRegistered := map[string]bool{}
	rows := make([]workerDiscoveryRow, 0, len(devices)+len(registered))
	for _, device := range devices {
		row := workerDiscoveryRow{
			Name:         device.Name,
			Host:         device.Host,
			DNSName:      device.DNSName,
			TailscaleIPs: device.TailscaleIPs,
			OS:           device.OS,
			Online:       device.Online,
			Self:         device.Self,
			Source:       "tailscale",
		}
		if worker, ok := lookupRegisteredWorker(registeredByKey, device.Name, device.Host, device.DNSName); ok {
			row.Tracked = true
			row.SSHTarget = worker.EffectiveSSHTarget()
			seenRegistered[worker.Name] = true
		}
		rows = append(rows, row)
	}
	for _, worker := range registered {
		if seenRegistered[worker.Name] {
			continue
		}
		rows = append(rows, workerDiscoveryRow{
			Name:      worker.Name,
			Host:      worker.Host,
			DNSName:   worker.DNSName,
			Tracked:   true,
			SSHTarget: worker.EffectiveSSHTarget(),
			Source:    firstNonEmpty(worker.Source, "manual"),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Tracked != rows[j].Tracked {
			return rows[i].Tracked && !rows[j].Tracked
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

func resolveWorkerTarget(stateDir, value string) (string, error) {
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	worker, ok, err := store.Resolve(value)
	if err != nil {
		return "", err
	}
	if ok {
		return worker.EffectiveSSHTarget(), nil
	}
	return value, nil
}

func discoverTailscaleDevices(ctx context.Context) ([]tailscaleDevice, error) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tailscale", "status", "--json")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var status tailscaleStatusOutput
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		return nil, fmt.Errorf("decode tailscale status: %w", err)
	}
	var devices []tailscaleDevice
	if status.Self != nil {
		devices = append(devices, tailscaleDeviceFromPeer(*status.Self, true))
	}
	for _, peer := range status.Peer {
		devices = append(devices, tailscaleDeviceFromPeer(peer, false))
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	return devices, nil
}

func tailscaleDeviceFromPeer(peer tailscalePeerStatus, self bool) tailscaleDevice {
	dns := strings.TrimSuffix(strings.TrimSpace(peer.DNSName), ".")
	host := strings.TrimSpace(peer.HostName)
	if host == "" {
		host = dns
	}
	name := workerDisplayName(host)
	if name == "" && len(peer.TailscaleIPs) > 0 {
		name = peer.TailscaleIPs[0]
	}
	return tailscaleDevice{
		Name:         name,
		Host:         host,
		DNSName:      dns,
		OS:           peer.OS,
		Online:       peer.Online,
		Self:         self,
		TailscaleIPs: append([]string(nil), peer.TailscaleIPs...),
	}
}

func findTailscaleDevice(devices []tailscaleDevice, value string) (tailscaleDevice, bool) {
	deviceMap := tailscaleDeviceMap(devices)
	return lookupTailscaleDevice(deviceMap, value)
}

func tailscaleDeviceMap(devices []tailscaleDevice) map[string]tailscaleDevice {
	out := map[string]tailscaleDevice{}
	for _, device := range devices {
		for _, key := range workerKeys(device.Name, device.Host, device.DNSName) {
			out[key] = device
		}
	}
	return out
}

func lookupTailscaleDevice(devices map[string]tailscaleDevice, values ...string) (tailscaleDevice, bool) {
	for _, key := range workerKeys(values...) {
		if device, ok := devices[key]; ok {
			return device, true
		}
	}
	return tailscaleDevice{}, false
}

func lookupRegisteredWorker(workers map[string]registry.WorkerConfig, values ...string) (registry.WorkerConfig, bool) {
	for _, key := range workerKeys(values...) {
		if worker, ok := workers[key]; ok {
			return worker, true
		}
	}
	return registry.WorkerConfig{}, false
}

func workerKeys(values ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSuffix(strings.TrimSpace(value), ".")
		if value == "" {
			continue
		}
		candidates := []string{value}
		if strings.Contains(value, "@") {
			_, host, ok := splitWorkerTarget(value)
			if ok {
				candidates = append(candidates, host)
			}
		}
		if strings.Contains(value, ".") {
			candidates = append(candidates, strings.Split(value, ".")[0])
		}
		for _, candidate := range candidates {
			key := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(candidate), "."))
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

func splitWorkerTarget(value string) (string, string, bool) {
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
		if value := strings.TrimSpace(current.Username); value != "" {
			if strings.Contains(value, "\\") {
				parts := strings.Split(value, "\\")
				return parts[len(parts)-1]
			}
			return value
		}
	}
	return ""
}

func workerWithTarget(worker registry.WorkerConfig) map[string]any {
	return map[string]any{
		"name":         worker.Name,
		"host":         worker.Host,
		"user":         worker.User,
		"sshTarget":    worker.EffectiveSSHTarget(),
		"source":       worker.Source,
		"dnsName":      worker.DNSName,
		"tailscaleIPs": worker.TailscaleIPs,
		"registeredAt": worker.RegisteredAt,
		"updatedAt":    worker.UpdatedAt,
	}
}

func workerDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		_, host, ok := splitWorkerTarget(value)
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

func defaultWorkerName(device tailscaleDevice, host string) string {
	for _, value := range []string{device.Name, workerDisplayName(device.DNSName), workerDisplayName(host)} {
		if name := sanitizeWorkerName(value); name != "" {
			return name
		}
	}
	return sanitizeWorkerName(firstString(device.TailscaleIPs))
}

func sanitizeWorkerName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '.' {
			break
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func workerSSHHost(host, dnsName string) string {
	host = strings.TrimSpace(host)
	if host != "" && !strings.ContainsAny(host, " \t/\\@:;|&<>`'\"") {
		return host
	}
	return firstNonEmpty(strings.TrimSuffix(strings.TrimSpace(dnsName), "."), host)
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format(time.RFC3339)
}

func optionalBool(known, value bool) string {
	if !known {
		return "unknown"
	}
	if value {
		return "true"
	}
	return "false"
}

func firstString(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return values[0]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cleanupCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "cleanup", Aliases: []string{"clean"}, Short: "Safely clean remote Workyard runs and logs"}
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Truncate remote log files for the selected run",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, paths, err := cleanupPaths(cmd, opts)
			if err != nil {
				return err
			}
			_ = loaded
			res, err := remote.CleanupLogs(cmd.Context(), opts.worker, paths)
			if err != nil {
				return output.NewError("REMOTE_LOG_CLEANUP_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "cleaned logs at %s:%s\n", res.Worker, res.RemoteLogPath)
			return nil
		},
	}
	stopFirst := true
	noStop := false
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Stop and remove the selected remote run directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, paths, err := cleanupPaths(cmd, opts)
			if err != nil {
				return err
			}
			if noStop {
				stopFirst = false
			}
			if stopFirst {
				oldOut := cmd.OutOrStdout()
				cmd.SetOut(io.Discard)
				err := remoteControl(cmd, opts, loaded, "stop", paths.RunID, nil, controlExtra{All: true})
				cmd.SetOut(oldOut)
				if err != nil {
					return err
				}
			}
			res, err := remote.CleanupRun(cmd.Context(), opts.worker, paths)
			if err != nil {
				return output.NewError("REMOTE_RUN_CLEANUP_FAILED", err.Error(), "")
			}
			store := registry.New(registry.DefaultPath(opts.stateDir))
			_, _ = store.Remove(opts.worker, paths.Project, paths.RunID)
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed run at %s:%s\n", res.Worker, res.RemoteRunPath)
			return nil
		},
	}
	runCmd.Flags().BoolVar(&stopFirst, "stop", true, "stop services before removing the run")
	runCmd.Flags().BoolVar(&noStop, "no-stop", false, "skip stopping services before removing the run")
	root.AddCommand(logsCmd, runCmd)
	return root
}

func cleanupPaths(cmd *cobra.Command, opts *options) (config.Loaded, remote.Paths, error) {
	if opts.worker == "" {
		return config.Loaded{}, remote.Paths{}, output.NewError("WORKER_REQUIRED", "--worker is required for remote cleanup", "")
	}
	loaded, err := config.Load(opts.project)
	if err != nil {
		return config.Loaded{}, remote.Paths{}, output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
	}
	run := opts.run
	if run == "" {
		run = runid.Default(loaded.Config.Root)
	}
	run, err = runid.Validate(run)
	if err != nil {
		return config.Loaded{}, remote.Paths{}, output.NewError("RUN_ID_INVALID", err.Error(), "")
	}
	home, err := remote.Home(cmd.Context(), opts.worker)
	if err != nil {
		return config.Loaded{}, remote.Paths{}, output.NewError("SSH_FAILED", err.Error(), "Check Tailscale/SSH connectivity to the worker")
	}
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return config.Loaded{}, remote.Paths{}, output.NewError("REMOTE_PATH_INVALID", err.Error(), "")
	}
	return loaded, paths, nil
}

func doctorCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check local Workyard dependencies and connectivity",
		RunE: func(cmd *cobra.Command, args []string) error {
			report := doctor.Run(cmd.Context(), doctor.Options{
				Project:      opts.project,
				Worker:       opts.worker,
				RemoteRoot:   opts.remoteRoot,
				RemoteBinary: opts.remoteBinary,
				Version:      Version,
				CheckProject: true,
				Timeout:      8 * time.Second,
			}, doctor.SystemRunner{})
			if opts.json {
				if err := output.WriteJSON(cmd.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				printDoctorReport(cmd.OutOrStdout(), report)
			}
			if !report.OK {
				return printedError{err: errors.New("doctor found required check failures"), exitCode: 1}
			}
			return nil
		},
	}
	return cmd
}

func printDoctorReport(w io.Writer, report doctor.Report) {
	_, _ = fmt.Fprintln(w, "workyard doctor")
	rows := make([][]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		rows = append(rows, []string{check.Name, strings.ToUpper(check.Status), check.Message})
	}
	_ = output.WriteTable(w, []string{"CHECK", "STATUS", "MESSAGE"}, rows)
	for _, check := range report.Checks {
		if check.Detail != "" {
			_, _ = fmt.Fprintf(w, "  %s detail: %s\n", check.Name, check.Detail)
		}
		if check.Hint != "" && check.Status != doctor.StatusPass {
			_, _ = fmt.Fprintf(w, "  %s hint: %s\n", check.Name, check.Hint)
		}
	}
	if report.OK {
		_, _ = fmt.Fprintln(w, "\nok: required checks passed")
		return
	}
	_, _ = fmt.Fprintln(w, "\nfailed: one or more required checks failed")
}

func printBootstrapReport(w io.Writer, report bootstrap.Report) {
	_, _ = fmt.Fprintf(w, "workyard workers setup %s\n", report.WorkerName)
	if report.ConfigFound {
		_, _ = fmt.Fprintf(w, "config: %s\n", report.ConfigPath)
	}
	if report.DryRun {
		_, _ = fmt.Fprintln(w, "dry run: no worker changes were made")
	}
	rows := make([][]string, 0, len(report.Steps))
	for _, step := range report.Steps {
		rows = append(rows, []string{step.Name, strings.ToUpper(step.Status), step.Message})
	}
	_ = output.WriteTable(w, []string{"CHECK", "STATUS", "MESSAGE"}, rows)
	for _, step := range report.Steps {
		if step.Detail != "" {
			_, _ = fmt.Fprintf(w, "  %s detail: %s\n", step.Name, step.Detail)
		}
		if step.Hint != "" && step.Status != bootstrap.StatusPass && step.Status != bootstrap.StatusSkip && step.Status != bootstrap.StatusPlan {
			_, _ = fmt.Fprintf(w, "  %s hint: %s\n", step.Name, step.Hint)
		}
	}
	if report.DoctorReport != nil && !report.DoctorReport.OK {
		_, _ = fmt.Fprintln(w)
		printDoctorReport(w, *report.DoctorReport)
	}
	if report.OK {
		_, _ = fmt.Fprintln(w, "\nok: worker setup completed")
		return
	}
	_, _ = fmt.Fprintln(w, "\nfailed: worker setup did not complete")
}

func readHiddenPassword(w io.Writer, prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("stdin is not a terminal")
	}
	if _, err := fmt.Fprint(w, prompt); err != nil {
		return "", err
	}
	passwordBytes, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(w)
	if err != nil {
		return "", err
	}
	defer zeroBytes(passwordBytes)
	password := string(passwordBytes)
	if password == "" {
		return "", errors.New("sudo password must not be empty")
	}
	if strings.ContainsAny(password, "\x00\r\n") {
		return "", errors.New("sudo password contains unsupported control characters")
	}
	return password, nil
}

func zeroBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func serverCommand(opts *options) *cobra.Command {
	var listen string
	var refreshInterval time.Duration
	var open bool
	var autoStartDaemon bool
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Run the local Workyard monitor server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if listen == "" {
				listen = "127.0.0.1:3099"
			}
			if refreshInterval <= 0 {
				refreshInterval = 3 * time.Second
			}
			if opts.json {
				if err := output.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"ok":              true,
					"listen":          listen,
					"refreshInterval": refreshInterval.String(),
					"statePath":       registry.DefaultPath(opts.stateDir),
				}); err != nil {
					return err
				}
			} else if !opts.quiet {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "workyard server listening on http://%s\n", listen)
			}
			return monitor.Serve(cmd.Context(), monitor.ServerOptions{
				Listen:          listen,
				RefreshInterval: refreshInterval,
				StateDir:        opts.stateDir,
				Socket:          opts.socket,
				Version:         Version,
				Open:            open,
				AutoStartDaemon: autoStartDaemon,
			})
		},
	}
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:3099", "loopback address for the monitor server")
	cmd.Flags().DurationVar(&refreshInterval, "refresh-interval", 3*time.Second, "worker polling interval")
	cmd.Flags().BoolVar(&open, "open", false, "open the dashboard in a browser")
	cmd.Flags().BoolVar(&autoStartDaemon, "auto-start-daemon", true, "start a private remote worker daemon when needed")
	return cmd
}

func watchCommand(opts *options) *cobra.Command {
	var once bool
	var pollInterval time.Duration
	cmd := &cobra.Command{
		Use:   "watch [service...]",
		Short: "Watch local files, sync changes, and optionally restart services",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.worker == "" {
				return output.NewError("WORKER_REQUIRED", "--worker is required for watch", "")
			}
			loaded, err := config.Load(opts.project)
			if err != nil {
				return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
			}
			run := opts.run
			if run == "" {
				run = runid.Default(loaded.Config.Root)
			}
			run, err = runid.Validate(run)
			if err != nil {
				return output.NewError("RUN_ID_INVALID", err.Error(), "")
			}
			specs, err := watchSpecs(loaded.Config, args)
			if err != nil {
				return output.NewError("WATCH_CONFIG_INVALID", err.Error(), "")
			}
			if pollInterval <= 0 {
				pollInterval = 500 * time.Millisecond
			}
			snapshot, err := watcher.Snapshot(loaded.Config.Root, specs)
			if err != nil {
				return err
			}
			if !opts.quiet {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "watching %d files for %d service(s)\n", len(snapshot), len(specs))
			}
			changes, watchErrs, err := watcher.Changes(cmd.Context(), loaded.Config.Root, specs, pollInterval)
			if err != nil {
				return err
			}
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case err, ok := <-watchErrs:
					if !ok {
						return nil
					}
					if err != nil && opts.verbose {
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "watch warning: %s\n", err)
					}
					continue
				case _, ok := <-changes:
					if !ok {
						return nil
					}
				}
				next, err := watcher.Snapshot(loaded.Config.Root, specs)
				if err != nil {
					return err
				}
				if !watcher.Changed(snapshot, next) {
					continue
				}
				snapshot = next
				debounce := watchDebounce(specs)
				if debounce > 0 {
					time.Sleep(debounce)
					next, err = watcher.Snapshot(loaded.Config.Root, specs)
					if err != nil {
						return err
					}
					snapshot = next
				}
				res, err := syncer.Run(cmd.Context(), loaded, syncer.Options{
					Worker:     opts.worker,
					RunID:      run,
					RemoteRoot: opts.remoteRoot,
					Delete:     true,
				}, Version)
				if err != nil {
					return output.NewError("WATCH_SYNC_FAILED", err.Error(), "Run workyard sync with --verbose")
				}
				rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
					Worker:           res.Worker,
					Project:          res.Project,
					RunID:            res.RunID,
					RemoteRoot:       opts.remoteRoot,
					RemoteRunPath:    res.RemoteRunPath,
					RemoteSourcePath: res.RemoteSourcePath,
					RemoteBinary:     opts.remoteBinary,
					LocalRoot:        loaded.Config.Root,
					ConfigPath:       loaded.Config.Path,
				})
				restarted := []string{}
				for _, spec := range specs {
					action := spec.Watch.Action
					if action == "" {
						action = "sync-restart"
					}
					if action != "sync-restart" {
						continue
					}
					oldOut := cmd.OutOrStdout()
					cmd.SetOut(io.Discard)
					if err := remoteControl(cmd, opts, loaded, "restart", run, []string{spec.Service}, controlExtra{}); err != nil {
						cmd.SetOut(oldOut)
						return err
					}
					cmd.SetOut(oldOut)
					restarted = append(restarted, spec.Service)
				}
				if opts.json {
					if err := output.WriteJSON(cmd.OutOrStdout(), map[string]any{
						"ok":                true,
						"project":           res.Project,
						"runId":             res.RunID,
						"worker":            res.Worker,
						"remoteSourcePath":  res.RemoteSourcePath,
						"restartedServices": restarted,
					}); err != nil {
						return err
					}
				} else if !opts.quiet {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "synced %s; restarted %s\n", res.RemoteSourcePath, strings.Join(restarted, ","))
				}
				if once {
					return nil
				}
			}
		},
	}
	cmd.Flags().BoolVar(&once, "once", false, "exit after handling one detected change")
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", 500*time.Millisecond, "file polling interval")
	return cmd
}

func initCommand(opts *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a starter workyard.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := filepath.Abs(opts.project)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(root, 0o750); err != nil {
				return err
			}
			path := filepath.Join(root, config.FileName)
			if _, err := os.Stat(path); err == nil && !force {
				return output.NewError("CONFIG_EXISTS", "workyard.yaml already exists", "Pass --force to overwrite it")
			}
			cfg := config.DefaultConfig(filepath.Base(root))
			if err := config.Write(path, cfg); err != nil {
				return err
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "configPath": path, "created": true})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "created %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing workyard.yaml")
	return cmd
}

func configCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "config", Short: "Inspect Workyard config"}
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate workyard.yaml",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := config.Load(opts.project)
			if err != nil {
				return output.NewError("CONFIG_INVALID", err.Error(), "Run workyard init or fix workyard.yaml")
			}
			if opts.json {
				warnings := loaded.Warnings
				if warnings == nil {
					warnings = []string{}
				}
				type checkResponse struct {
					OK         bool     `json:"ok"`
					ConfigPath string   `json:"configPath"`
					Warnings   []string `json:"warnings"`
				}
				return output.WriteJSON(cmd.OutOrStdout(), checkResponse{OK: true, ConfigPath: loaded.Config.Path, Warnings: warnings})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "ok: %s\n", loaded.Config.Path)
			for _, warning := range loaded.Warnings {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "warning: %s\n", warning)
			}
			return nil
		},
	}
	root.AddCommand(check)
	return root
}

func servicesCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "services",
		Short: "List configured services",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := config.Load(opts.project)
			if err != nil {
				return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
			}
			names := config.ServiceNames(loaded.Config.Services)
			if opts.json {
				var services []map[string]any
				for _, name := range names {
					svc := loaded.Config.Services[name]
					services = append(services, map[string]any{
						"name":         name,
						"path":         svc.Path,
						"startCommand": svc.StartCommand,
						"port":         svc.Port.Default,
					})
				}
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "services": services})
			}
			rows := make([][]string, 0, len(names))
			for _, name := range names {
				svc := loaded.Config.Services[name]
				rows = append(rows, []string{name, svc.Path, svc.StartCommand, fmt.Sprint(svc.Port.Default)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"SERVICE", "PATH", "START COMMAND", "PORT"}, rows)
		},
	}
}

type deployOptions struct {
	install     bool
	fresh       bool
	skipDoctor  bool
	skipSetup   bool
	skipBuild   bool
	skipWait    bool
	timeout     string
	artifactDir string
	localBinary string
}

type deployStep struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

func deployCommand(opts *options) *cobra.Command {
	var d deployOptions
	cmd := &cobra.Command{
		Use:   "deploy [project-path|workyard.yaml] [service...]",
		Short: "Deploy a project to a worker",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, opts, d, args)
		},
	}
	cmd.Flags().BoolVar(&d.install, "install", false, "install or upgrade the remote worker binary before deploying")
	cmd.Flags().BoolVar(&d.fresh, "fresh", false, "remove the existing managed run before deploying")
	cmd.Flags().BoolVar(&d.skipDoctor, "skip-doctor", false, "skip preflight doctor checks")
	cmd.Flags().BoolVar(&d.skipSetup, "skip-setup", false, "skip project setup")
	cmd.Flags().BoolVar(&d.skipBuild, "skip-build", false, "skip project build")
	cmd.Flags().BoolVar(&d.skipWait, "skip-wait", false, "skip waiting for healthy services")
	cmd.Flags().StringVar(&d.timeout, "timeout", "60s", "health wait timeout")
	cmd.Flags().StringVar(&d.artifactDir, "artifact-dir", "dist", "directory containing workyard-<os>-<arch> artifacts")
	cmd.Flags().StringVar(&d.localBinary, "local-binary", "", "specific local binary to upload when --install is set")
	return cmd
}

func runDeploy(cmd *cobra.Command, opts *options, d deployOptions, args []string) error {
	if opts.worker == "" {
		return output.NewError("WORKER_REQUIRED", "--worker is required for deploy", "")
	}
	project, services, err := deployProjectAndServices(opts.project, args)
	if err != nil {
		return output.NewError("DEPLOY_ARGS_INVALID", err.Error(), "")
	}
	loaded, err := config.Load(project)
	if err != nil {
		return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
	}
	for _, name := range services {
		if _, ok := loaded.Config.Services[name]; !ok {
			return output.NewError("SERVICE_SELECTION_FAILED", fmt.Sprintf("unknown service %q", name), "")
		}
	}
	waitServices := services
	if len(waitServices) == 0 {
		waitServices = config.ServiceNames(loaded.Config.Services)
	}
	run := opts.run
	if run == "" {
		run = runid.Default(loaded.Config.Root)
	}
	run, err = runid.Validate(run)
	if err != nil {
		return output.NewError("RUN_ID_INVALID", err.Error(), "")
	}
	steps := []deployStep{}
	step := func(name, message string) {
		steps = append(steps, deployStep{Name: name, OK: true, Message: message})
		if !opts.quiet && !opts.json {
			if message == "" {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "ok: %s\n", name)
			} else {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "ok: %s - %s\n", name, message)
			}
		}
	}
	if !opts.quiet && !opts.json {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "deploying %s to %s run=%s\n", loaded.Config.Name, opts.worker, run)
	}
	home, err := remote.Home(cmd.Context(), opts.worker)
	if err != nil {
		return output.NewError("SSH_FAILED", err.Error(), "Check Tailscale/SSH connectivity to the worker")
	}
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return output.NewError("REMOTE_PATH_INVALID", err.Error(), "")
	}
	if d.install {
		res, err := deployInstall(cmd, opts, d, paths)
		if err != nil {
			return err
		}
		message := res.InstalledVersion
		if res.DaemonRestarted {
			message += "; daemon restarted"
		}
		step("install", message)
	}
	if !d.skipDoctor {
		report := doctor.Run(cmd.Context(), doctor.Options{
			Project:      loaded.Config.Root,
			Worker:       opts.worker,
			RemoteRoot:   opts.remoteRoot,
			RemoteBinary: opts.remoteBinary,
			Version:      Version,
			CheckProject: true,
			Timeout:      8 * time.Second,
		}, doctor.SystemRunner{})
		if !report.OK {
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": false, "failedStep": "doctor", "doctor": report, "steps": steps})
			}
			printDoctorReport(cmd.OutOrStdout(), report)
			return printedError{err: errors.New("doctor found required check failures"), exitCode: 1}
		}
		step("doctor", "required checks passed")
	}
	if d.fresh {
		removed, err := deployFresh(cmd, opts, paths, run)
		if err != nil {
			return err
		}
		if removed {
			step("fresh", "removed existing run")
		} else {
			step("fresh", "no existing run")
		}
	}
	syncRes, err := syncer.Run(cmd.Context(), loaded, syncer.Options{
		Worker:     opts.worker,
		RunID:      run,
		RemoteRoot: opts.remoteRoot,
		Delete:     true,
		Verbose:    opts.verbose,
	}, Version)
	if err != nil {
		return output.NewError("SYNC_FAILED", err.Error(), "Check SSH access and run workyard sync with --verbose")
	}
	rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
		Worker:           syncRes.Worker,
		Project:          syncRes.Project,
		RunID:            syncRes.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    syncRes.RemoteRunPath,
		RemoteSourcePath: syncRes.RemoteSourcePath,
		RemoteBinary:     opts.remoteBinary,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
	step("sync", syncRes.RemoteSourcePath)
	if !d.skipSetup {
		res, err := remoteDaemonCall(cmd.Context(), opts, paths, "setup", nil, controlExtra{})
		if err != nil {
			return output.NewError("SETUP_FAILED", err.Error(), "Run workyard events --json or logs setup")
		}
		step("setup", res.Message)
	}
	if !d.skipBuild {
		res, err := remoteDaemonCall(cmd.Context(), opts, paths, "build", nil, controlExtra{})
		if err != nil {
			return output.NewError("BUILD_FAILED", err.Error(), "Run workyard events --json or logs build")
		}
		step("build", res.Message)
	}
	stopExtra := controlExtra{}
	if len(services) == 0 {
		stopExtra.All = true
	}
	if res, err := remoteDaemonCall(cmd.Context(), opts, paths, "stop", services, stopExtra); err == nil {
		step("stop", serviceStatusSummary(res.Services))
	} else if opts.verbose && !opts.json {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: stop before start skipped: %s\n", err)
	}
	startRes, err := remoteDaemonCall(cmd.Context(), opts, paths, "start", services, controlExtra{Timeout: d.timeout})
	if err != nil {
		return output.NewError("START_FAILED", err.Error(), "Run workyard inspect --json or logs <service>")
	}
	step("start", serviceStatusSummary(startRes.Services))
	if !d.skipWait && len(waitServices) > 0 {
		res, err := remoteDaemonCall(cmd.Context(), opts, paths, "wait", waitServices, controlExtra{Healthy: true, Timeout: d.timeout})
		if err != nil {
			return output.NewError("WAIT_FAILED", err.Error(), "Run workyard inspect --json")
		}
		step("wait", res.Message)
	}
	urlsRes, err := remoteDaemonCall(cmd.Context(), opts, paths, "urls", services, controlExtra{})
	if err != nil {
		return output.NewError("URLS_FAILED", err.Error(), "Run workyard status --json")
	}
	step("urls", fmt.Sprintf("%d url(s)", len(urlsRes.URLs)))
	if opts.json {
		return output.WriteJSON(cmd.OutOrStdout(), map[string]any{
			"ok":               true,
			"worker":           opts.worker,
			"project":          paths.Project,
			"runId":            paths.RunID,
			"remoteRunPath":    paths.RunRoot,
			"remoteSourcePath": paths.Source,
			"services":         urlsRes.Services,
			"urls":             urlsRes.URLs,
			"steps":            steps,
		})
	}
	printDeployURLs(cmd.OutOrStdout(), urlsRes.URLs)
	return nil
}

func deployInstall(cmd *cobra.Command, opts *options, d deployOptions, paths remote.Paths) (remote.InstallResult, error) {
	platform, err := remote.DetectPlatform(cmd.Context(), opts.worker)
	if err != nil {
		return remote.InstallResult{}, output.NewError("WORKER_PLATFORM_FAILED", err.Error(), "Check SSH access and worker OS/architecture")
	}
	binary := d.localBinary
	if binary == "" {
		binary = filepath.Join(d.artifactDir, platform.ArtifactName())
	}
	res, err := remote.InstallBinary(cmd.Context(), opts.worker, platform, remote.InstallOptions{
		LocalBinary:     binary,
		RemoteBinary:    opts.remoteBinary,
		ExpectedVersion: Version,
	})
	if err != nil {
		return remote.InstallResult{}, output.NewError("WORKER_INSTALL_FAILED", err.Error(), "Build the matching artifact first, for example GOOS="+platform.OS+" GOARCH="+platform.Arch+" go build -o "+binary+" ./cmd/workyard")
	}
	paths.Binary = res.RemoteBinary
	if err := remote.RestartDaemon(cmd.Context(), opts.worker, paths, ""); err != nil {
		return remote.InstallResult{}, output.NewError("WORKER_DAEMON_RESTART_FAILED", err.Error(), "Check ~/.workyard/daemon/daemon.log on the worker")
	}
	res.DaemonRestarted = true
	return res, nil
}

func deployFresh(cmd *cobra.Command, opts *options, paths remote.Paths, run string) (bool, error) {
	exists, err := remoteRunExists(cmd.Context(), opts.worker, paths)
	if err != nil {
		return false, output.NewError("REMOTE_RUN_CHECK_FAILED", err.Error(), "")
	}
	if !exists {
		return false, nil
	}
	if !opts.quiet && !opts.json {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "fresh: removing existing run %s\n", paths.RunRoot)
	}
	if _, err := remoteDaemonCall(cmd.Context(), opts, paths, "stop", nil, controlExtra{All: true}); err != nil {
		if !opts.quiet {
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "warning: stop before fresh cleanup failed: %s\n", err)
		}
	}
	if _, err := remote.CleanupRun(cmd.Context(), opts.worker, paths); err != nil {
		return false, output.NewError("REMOTE_RUN_CLEANUP_FAILED", err.Error(), "")
	}
	store := registry.New(registry.DefaultPath(opts.stateDir))
	_, _ = store.Remove(opts.worker, paths.Project, run)
	return true, nil
}

func remoteRunExists(ctx context.Context, worker string, paths remote.Paths) (bool, error) {
	script := strings.Join([]string{
		"set -eu",
		"run=" + remote.ShellQuote(paths.RunRoot),
		"if [ -L \"$run\" ]; then printf 'refusing symlink run path\\n' >&2; exit 2; fi",
		"if [ -d \"$run\" ]; then printf 'exists\\n'; else printf 'missing\\n'; fi",
	}, "\n")
	res, err := remote.Run(ctx, worker, []string{"sh", "-lc", script}, nil, 15*time.Second)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(res.Stdout) == "exists", nil
}

func remoteDaemonCall(ctx context.Context, opts *options, paths remote.Paths, action string, services []string, extra controlExtra) (worker.Response, error) {
	if err := remote.EnsureDaemon(ctx, opts.worker, paths, opts.remoteBinary); err != nil {
		return worker.Response{}, err
	}
	binary := paths.Binary
	if opts.remoteBinary != "" {
		binary = opts.remoteBinary
	}
	argv := []string{binary, "daemonctl", action, "--socket", paths.Socket, "--run-root", paths.RunRoot, "--project-name", paths.Project, "--run-id", paths.RunID, "--worker-name", opts.worker, "--json"}
	if extra.All {
		argv = append(argv, "--all")
	}
	if extra.Tail > 0 {
		argv = append(argv, "--tail", fmt.Sprint(extra.Tail))
	}
	if extra.MaxBytes > 0 {
		argv = append(argv, "--max-bytes", fmt.Sprint(extra.MaxBytes))
	}
	if extra.Stream != "" {
		argv = append(argv, "--stream", extra.Stream)
	}
	if extra.Healthy {
		argv = append(argv, "--healthy")
	}
	if extra.Status != "" {
		argv = append(argv, "--status", extra.Status)
	}
	if extra.Timeout != "" {
		argv = append(argv, "--timeout", extra.Timeout)
	}
	argv = append(argv, services...)
	out, err := remote.Run(ctx, opts.worker, argv, nil, remoteTimeout(action, extra.Timeout))
	if err != nil {
		return worker.Response{}, err
	}
	var res worker.Response
	if err := json.Unmarshal([]byte(out.Stdout), &res); err != nil {
		return worker.Response{}, fmt.Errorf("decode %s response: %w", action, err)
	}
	if !res.OK {
		if res.Error != nil {
			return res, fmt.Errorf("%s: %s", res.Error.Code, res.Error.Message)
		}
		return res, fmt.Errorf("%s failed", action)
	}
	return res, nil
}

func deployProjectAndServices(defaultProject string, args []string) (string, []string, error) {
	project := defaultProject
	if project == "" {
		project = "."
	}
	if len(args) == 0 {
		return project, nil, nil
	}
	first := args[0]
	if strings.HasSuffix(first, ".yaml") || strings.HasSuffix(first, ".yml") {
		if _, err := os.Stat(first); err != nil {
			return "", nil, fmt.Errorf("project path %q does not exist", first)
		}
		return first, args[1:], nil
	}
	if stat, err := os.Stat(first); err == nil {
		if stat.IsDir() {
			return first, args[1:], nil
		}
		return first, args[1:], nil
	} else if looksLikeDeployProjectPath(first) {
		return "", nil, fmt.Errorf("project path %q does not exist", first)
	}
	return project, args, nil
}

func looksLikeDeployProjectPath(value string) bool {
	return value == "." ||
		value == ".." ||
		strings.HasPrefix(value, "."+string(os.PathSeparator)) ||
		strings.HasPrefix(value, ".."+string(os.PathSeparator)) ||
		strings.HasPrefix(value, "~"+string(os.PathSeparator)) ||
		filepath.IsAbs(value) ||
		strings.ContainsRune(value, os.PathSeparator)
}

func serviceStatusSummary(services []worker.ServiceState) string {
	if len(services) == 0 {
		return ""
	}
	parts := make([]string, 0, len(services))
	for _, svc := range services {
		parts = append(parts, svc.Name+":"+svc.Status)
	}
	return strings.Join(parts, ", ")
}

func printDeployURLs(w io.Writer, urls []worker.PreviewURL) {
	if len(urls) == 0 {
		_, _ = fmt.Fprintln(w, "no urls")
		return
	}
	rows := make([][]string, 0, len(urls))
	for _, url := range urls {
		rows = append(rows, []string{url.Service, url.URL, fmt.Sprint(url.Healthy)})
	}
	_ = output.WriteTable(w, []string{"SERVICE", "URL", "HEALTHY"}, rows)
}

func syncCommand(opts *options) *cobra.Command {
	var dryRun bool
	var deleteRemote bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync the project to a remote worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, err := config.Load(opts.project)
			if err != nil {
				return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
			}
			run := opts.run
			if run == "" {
				run = runid.Default(loaded.Config.Root)
			}
			run, err = runid.Validate(run)
			if err != nil {
				return output.NewError("RUN_ID_INVALID", err.Error(), "")
			}
			res, err := syncer.Run(cmd.Context(), loaded, syncer.Options{
				Worker:     opts.worker,
				RunID:      run,
				RemoteRoot: opts.remoteRoot,
				DryRun:     dryRun,
				Delete:     deleteRemote,
				Verbose:    opts.verbose,
			}, Version)
			if err != nil {
				return output.NewError("SYNC_FAILED", err.Error(), "Check SSH access and run workyard sync with --verbose")
			}
			rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
				Worker:           res.Worker,
				Project:          res.Project,
				RunID:            res.RunID,
				RemoteRoot:       opts.remoteRoot,
				RemoteRunPath:    res.RemoteRunPath,
				RemoteSourcePath: res.RemoteSourcePath,
				RemoteBinary:     opts.remoteBinary,
				LocalRoot:        loaded.Config.Root,
				ConfigPath:       loaded.Config.Path,
			})
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "synced %s to %s:%s\n", res.Project, res.Worker, res.RemoteSourcePath)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would sync without changing remote files")
	cmd.Flags().BoolVar(&deleteRemote, "delete", true, "delete files removed locally inside the remote source directory")
	return cmd
}

func daemonCommand(opts *options) *cobra.Command {
	var allowRoot bool
	var foreground bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the private worker daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			_ = foreground
			if opts.json {
				_ = output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "message": "daemon starting"})
			} else if !opts.quiet {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "workyard daemon starting")
			}
			return worker.Serve(cmd.Context(), worker.DaemonOptions{StateDir: opts.stateDir, Socket: opts.socket, AllowRoot: allowRoot})
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", true, "run in the foreground")
	cmd.Flags().BoolVar(&allowRoot, "allow-root", false, "allow daemon to run as root")
	cmd.AddCommand(daemonStartCommand(opts, &allowRoot))
	cmd.AddCommand(daemonStopCommand(opts))
	cmd.AddCommand(daemonStatusCommand(opts))
	return cmd
}

func daemonStartCommand(opts *options, allowRoot *bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the private worker daemon in the background",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return startLocalDaemon(cmd, opts, *allowRoot)
		},
	}
	return cmd
}

func daemonStopCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the private worker daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return stopLocalDaemon(cmd, opts)
		},
	}
	return cmd
}

func daemonStatusCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check whether the private worker daemon is running",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			socket := daemonSocket(opts)
			res, err := worker.Call(socket, worker.Request{Action: "ping"})
			if err != nil {
				if opts.json {
					return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": false, "running": false, "socket": socket, "error": err.Error()})
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon stopped (%s)\n", socket)
				return nil
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "socket": socket, "message": res.Message})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon running (%s)\n", socket)
			return nil
		},
	}
	return cmd
}

func startLocalDaemon(cmd *cobra.Command, opts *options, allowRoot bool) error {
	socket := daemonSocket(opts)
	if res, err := worker.Call(socket, worker.Request{Action: "ping"}); err == nil {
		if opts.json {
			return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "socket": socket, "message": res.Message})
		}
		if !opts.quiet {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon already running (%s)\n", socket)
		}
		return nil
	}
	result, err := launchLocalDaemon(cmd.Context(), opts, allowRoot)
	if err != nil {
		return err
	}
	if opts.json {
		return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "pid": result.PID, "socket": result.Socket, "log": result.Log})
	}
	if !opts.quiet {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon started pid=%d socket=%s log=%s\n", result.PID, result.Socket, result.Log)
	}
	return nil
}

func launchLocalDaemon(ctx context.Context, opts *options, allowRoot bool) (daemonLaunchResult, error) {
	socket := daemonSocket(opts)
	stateDir := daemonStateDir(opts)
	logPath := filepath.Join(stateDir, "daemon", "daemon.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return daemonLaunchResult{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return daemonLaunchResult{}, err
	}
	defer logFile.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return daemonLaunchResult{}, err
	}
	defer devNull.Close()
	exe, err := os.Executable()
	if err != nil {
		return daemonLaunchResult{}, err
	}
	argv := []string{"daemon", "--foreground", "--quiet"}
	if opts.stateDir != "" {
		argv = append(argv, "--state-dir", opts.stateDir)
	}
	if opts.socket != "" {
		argv = append(argv, "--socket", opts.socket)
	}
	if allowRoot {
		argv = append(argv, "--allow-root")
	}
	child := exec.Command(exe, argv...)
	child.Stdin = devNull
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return daemonLaunchResult{}, err
	}
	if err := waitForDaemon(ctx, socket, 5*time.Second); err != nil {
		return daemonLaunchResult{}, output.NewError("DAEMON_START_FAILED", err.Error(), "Inspect "+logPath)
	}
	return daemonLaunchResult{PID: child.Process.Pid, Socket: socket, Log: logPath}, nil
}

func waitForDaemon(ctx context.Context, socket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if _, err := worker.Call(socket, worker.Request{Action: "ping"}); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if last != nil {
		return last
	}
	return fmt.Errorf("daemon did not become ready")
}

func stopLocalDaemon(cmd *cobra.Command, opts *options) error {
	socket := daemonSocket(opts)
	res, err := worker.Call(socket, worker.Request{Action: "shutdown"})
	if err != nil {
		if opts.json {
			return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": false, "socket": socket, "message": "daemon was not running"})
		}
		if !opts.quiet {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon was not running (%s)\n", socket)
		}
		return nil
	}
	if err := waitForDaemonStopped(socket, 5*time.Second); err != nil {
		return output.NewError("DAEMON_STOP_FAILED", err.Error(), "")
	}
	if opts.json {
		return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": false, "socket": socket, "message": res.Message})
	}
	if !opts.quiet {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s (%s)\n", res.Message, socket)
	}
	return nil
}

func waitForDaemonStopped(socket string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := worker.Call(socket, worker.Request{Action: "ping"}); err != nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not stop")
}

func daemonctlCommand(opts *options) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "daemonctl <action> [service...]",
		Short:  "Talk to the local worker daemon",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			action := args[0]
			services := args[1:]
			req := worker.Request{
				Action:   action,
				RunRoot:  daemonctlRunRoot,
				Project:  daemonctlProject,
				RunID:    daemonctlRun,
				Worker:   daemonctlWorker,
				Services: services,
				All:      daemonctlAll,
				Tail:     daemonctlTail,
				MaxBytes: daemonctlMaxBytes,
				Stream:   daemonctlStream,
				Healthy:  daemonctlHealthy,
				Status:   daemonctlStatus,
				Timeout:  daemonctlTimeout,
			}
			socket := opts.socket
			if socket == "" {
				socket = defaultSocket(opts.stateDir)
			}
			res, err := worker.Call(socket, req)
			if err != nil {
				if res.Error == nil {
					res = worker.Response{OK: false, Error: &worker.Error{Code: "DAEMONCTL_FAILED", Message: err.Error()}}
				}
				if printDaemonResponse(cmd.OutOrStdout(), res, opts.json, action) != nil {
					return err
				}
				return printedError{err: err, exitCode: 1}
			}
			return printDaemonResponse(cmd.OutOrStdout(), res, opts.json, action)
		},
	}
	cmd.Flags().StringVar(&daemonctlRunRoot, "run-root", "", "remote run root")
	cmd.Flags().StringVar(&daemonctlProject, "project-name", "", "project name")
	cmd.Flags().StringVar(&daemonctlRun, "run-id", "", "run id")
	cmd.Flags().StringVar(&daemonctlWorker, "worker-name", "", "worker name")
	cmd.Flags().BoolVar(&daemonctlAll, "all", false, "operate on all services")
	cmd.Flags().IntVar(&daemonctlTail, "tail", 200, "log/event lines to read")
	cmd.Flags().Int64Var(&daemonctlMaxBytes, "max-bytes", 128*1024, "maximum bytes to read")
	cmd.Flags().StringVar(&daemonctlStream, "stream", "both", "stdout, stderr, or both")
	cmd.Flags().BoolVar(&daemonctlHealthy, "healthy", false, "wait for healthy state")
	cmd.Flags().StringVar(&daemonctlStatus, "status", "", "wait for a service status")
	cmd.Flags().StringVar(&daemonctlTimeout, "timeout", "60s", "wait timeout")
	return cmd
}

var (
	daemonctlRunRoot  string
	daemonctlProject  string
	daemonctlRun      string
	daemonctlWorker   string
	daemonctlAll      bool
	daemonctlTail     int
	daemonctlMaxBytes int64
	daemonctlStream   string
	daemonctlHealthy  bool
	daemonctlStatus   string
	daemonctlTimeout  string
)

func controlCommand(opts *options, action string) *cobra.Command {
	use := action + " [service...]"
	if action == "status" || action == "inspect" || action == "urls" {
		use = action
	}
	var all bool
	cmd := &cobra.Command{
		Use:   use,
		Short: action + " services",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControl(cmd, opts, action, args, controlExtra{All: all})
		},
	}
	if action == "stop" {
		cmd.Flags().BoolVar(&all, "all", false, "stop all services")
	}
	return cmd
}

func logsCommand(opts *options) *cobra.Command {
	var tail int
	var maxBytes int64
	var stream string
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <service>",
		Short: "Read bounded service logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if follow {
				return followLogs(cmd, opts, args[0], controlExtra{Tail: tail, MaxBytes: maxBytes, Stream: stream})
			}
			return runControl(cmd, opts, "logs", args, controlExtra{Tail: tail, MaxBytes: maxBytes, Stream: stream})
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "lines to read")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 128*1024, "maximum bytes to read")
	cmd.Flags().StringVar(&stream, "stream", "both", "stdout, stderr, or both")
	cmd.Flags().BoolVar(&follow, "follow", false, "follow log output")
	return cmd
}

func followLogs(cmd *cobra.Command, opts *options, target string, extra controlExtra) error {
	loaded, err := config.Load(opts.project)
	if err != nil {
		return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
	}
	run := opts.run
	if run == "" {
		run = runid.Default(loaded.Config.Root)
	}
	run, err = runid.Validate(run)
	if err != nil {
		return output.NewError("RUN_ID_INVALID", err.Error(), "")
	}
	relPaths, err := followLogPaths(loaded.Config, target, extra.Stream)
	if err != nil {
		return output.NewError("LOG_TARGET_INVALID", err.Error(), "")
	}
	if opts.worker != "" {
		return followRemoteLogs(cmd, opts, loaded, run, relPaths, extra.Tail)
	}
	return followLocalLogs(cmd, opts, loaded, run, relPaths, extra.Tail)
}

func followRemoteLogs(cmd *cobra.Command, opts *options, loaded config.Loaded, run string, relPaths []string, tailLines int) error {
	home, err := remote.Home(cmd.Context(), opts.worker)
	if err != nil {
		return output.NewError("SSH_FAILED", err.Error(), "Check Tailscale/SSH connectivity to the worker")
	}
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return output.NewError("REMOTE_PATH_INVALID", err.Error(), "")
	}
	files := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		files = append(files, path.Join(paths.RunRoot, filepath.ToSlash(rel)))
	}
	script := followTailScript(paths.Logs, files, tailLines)
	if !opts.quiet {
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "following %d log file(s) on %s\n", len(files), opts.worker)
	}
	err = remote.Stream(cmd.Context(), opts.worker, []string{"sh", "-lc", script}, nil, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if cmd.Context().Err() != nil {
		return nil
	}
	return err
}

func followLocalLogs(cmd *cobra.Command, opts *options, loaded config.Loaded, run string, relPaths []string, tailLines int) error {
	home, _ := os.UserHomeDir()
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return output.NewError("LOCAL_PATH_INVALID", err.Error(), "")
	}
	files := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		files = append(files, filepath.Join(paths.RunRoot, filepath.FromSlash(rel)))
	}
	if err := os.MkdirAll(paths.Logs, 0o700); err != nil {
		return err
	}
	for _, file := range files {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		_ = f.Close()
	}
	argv := append([]string{"-n", fmt.Sprint(defaultTail(tailLines)), "-f", "--"}, files...)
	tail := exec.CommandContext(cmd.Context(), "tail", argv...)
	tail.Stdout = cmd.OutOrStdout()
	tail.Stderr = cmd.ErrOrStderr()
	err = tail.Run()
	if cmd.Context().Err() != nil {
		return nil
	}
	return err
}

func followLogPaths(cfg config.Config, target, stream string) ([]string, error) {
	target = strings.TrimSpace(target)
	if target == "" || strings.ContainsAny(target, `/\`) {
		return nil, fmt.Errorf("invalid log target %q", target)
	}
	var base string
	if _, ok := cfg.Services[target]; ok {
		base = target
	} else if target == "setup" || target == "build" {
		base = target
	} else {
		parts := strings.Split(target, ".")
		if len(parts) == 2 {
			if _, ok := cfg.Services[parts[0]]; ok && (parts[1] == "beforeStart" || parts[1] == "onClose") {
				base = target
			}
		}
	}
	if base == "" {
		return nil, fmt.Errorf("unknown service or lifecycle log target %q", target)
	}
	if stream == "" {
		stream = "both"
	}
	switch stream {
	case "stdout":
		return []string{filepath.ToSlash(filepath.Join("logs", base+".stdout.log"))}, nil
	case "stderr":
		return []string{filepath.ToSlash(filepath.Join("logs", base+".stderr.log"))}, nil
	case "both":
		return []string{
			filepath.ToSlash(filepath.Join("logs", base+".stdout.log")),
			filepath.ToSlash(filepath.Join("logs", base+".stderr.log")),
		}, nil
	default:
		return nil, fmt.Errorf("stream must be stdout, stderr, or both")
	}
}

func followTailScript(logDir string, files []string, tailLines int) string {
	quotedFiles := make([]string, 0, len(files))
	for _, file := range files {
		quotedFiles = append(quotedFiles, remote.ShellQuote(file))
	}
	return strings.Join([]string{
		"set -eu",
		"logs=" + remote.ShellQuote(logDir),
		"if [ -L \"$logs\" ]; then printf 'refusing symlink log directory\\n' >&2; exit 1; fi",
		"mkdir -p \"$logs\"",
		"touch " + strings.Join(quotedFiles, " "),
		"chmod go-rwx \"$logs\" " + strings.Join(quotedFiles, " "),
		"tail -n " + fmt.Sprint(defaultTail(tailLines)) + " -f -- " + strings.Join(quotedFiles, " "),
	}, "\n")
}

func defaultTail(value int) int {
	if value > 0 {
		return value
	}
	return 200
}

func eventsCommand(opts *options) *cobra.Command {
	var tail int
	var maxBytes int64
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Read lifecycle events",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControl(cmd, opts, "events", args, controlExtra{Tail: tail, MaxBytes: maxBytes})
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "events to read")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 128*1024, "maximum bytes to read")
	return cmd
}

func waitCommand(opts *options) *cobra.Command {
	var healthy bool
	var status string
	var timeout string
	cmd := &cobra.Command{
		Use:   "wait <service...>",
		Short: "Wait for service state",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControl(cmd, opts, "wait", args, controlExtra{Healthy: healthy, Status: status, Timeout: timeout})
		},
	}
	cmd.Flags().BoolVar(&healthy, "healthy", false, "wait until healthy")
	cmd.Flags().StringVar(&status, "status", "", "wait for specific status")
	cmd.Flags().StringVar(&timeout, "timeout", "60s", "wait timeout")
	return cmd
}

func openCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "open <service>",
		Short: "Open a service preview URL",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var buf strings.Builder
			oldOut := cmd.OutOrStdout()
			oldJSON := opts.json
			cmd.SetOut(&buf)
			opts.json = true
			err := runControl(cmd, opts, "urls", args, controlExtra{})
			cmd.SetOut(oldOut)
			opts.json = oldJSON
			if err != nil {
				return err
			}
			var res worker.Response
			if err := json.Unmarshal([]byte(buf.String()), &res); err != nil {
				return err
			}
			if len(res.URLs) == 0 {
				return output.NewError("URL_NOT_FOUND", "no preview URL found", "")
			}
			return exec.Command("open", res.URLs[0].URL).Run()
		},
	}
}

func versionCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print Workyard version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "version": Version})
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), Version)
			return nil
		},
	}
}

type controlExtra struct {
	All      bool
	Tail     int
	MaxBytes int64
	Stream   string
	Healthy  bool
	Status   string
	Timeout  string
}

func runControl(cmd *cobra.Command, opts *options, action string, services []string, extra controlExtra) error {
	loaded, err := config.Load(opts.project)
	if err != nil {
		return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "")
	}
	run := opts.run
	if run == "" {
		run = runid.Default(loaded.Config.Root)
	}
	run, err = runid.Validate(run)
	if err != nil {
		return output.NewError("RUN_ID_INVALID", err.Error(), "")
	}
	if opts.worker != "" {
		return remoteControl(cmd, opts, loaded, action, run, services, extra)
	}
	return localControl(cmd, opts, loaded, action, run, services, extra)
}

func watchSpecs(cfg config.Config, services []string) ([]watcher.Spec, error) {
	names := services
	if len(names) == 0 {
		names = config.ServiceNames(cfg.Services)
	}
	var specs []watcher.Spec
	for _, name := range names {
		svc, ok := cfg.Services[name]
		if !ok {
			return nil, fmt.Errorf("unknown service %q", name)
		}
		if svc.Watch == nil {
			if len(services) > 0 {
				return nil, fmt.Errorf("service %q does not configure watch", name)
			}
			continue
		}
		specs = append(specs, watcher.Spec{Service: name, Watch: *svc.Watch})
	}
	if len(specs) == 0 {
		return nil, fmt.Errorf("no services configure watch")
	}
	return specs, nil
}

func watchDebounce(specs []watcher.Spec) time.Duration {
	var max time.Duration
	for _, spec := range specs {
		if spec.Watch.Debounce > max {
			max = spec.Watch.Debounce
		}
	}
	if max == 0 {
		return 750 * time.Millisecond
	}
	return max
}

func localControl(cmd *cobra.Command, opts *options, loaded config.Loaded, action, run string, services []string, extra controlExtra) error {
	home, _ := os.UserHomeDir()
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return err
	}
	if actionNeedsLocalSource(action) {
		if err := prepareLocalSource(cmd.Context(), loaded, paths); err != nil {
			return output.NewError("LOCAL_SYNC_FAILED", err.Error(), "Check rsync and the local Workyard run directory")
		}
	}
	rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
		Worker:           "localhost",
		Project:          paths.Project,
		RunID:            paths.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    paths.RunRoot,
		RemoteSourcePath: paths.Source,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
	socket := opts.socket
	if socket == "" {
		socket = defaultSocket(opts.stateDir)
	}
	if action != "stop" {
		if err := ensureLocalDaemonRunning(cmd.Context(), opts); err != nil {
			return err
		}
	}
	res, err := worker.Call(socket, worker.Request{
		Action:   action,
		RunRoot:  paths.RunRoot,
		Project:  paths.Project,
		RunID:    paths.RunID,
		Worker:   "localhost",
		Services: services,
		All:      extra.All,
		Tail:     extra.Tail,
		MaxBytes: extra.MaxBytes,
		Stream:   extra.Stream,
		Healthy:  extra.Healthy,
		Status:   extra.Status,
		Timeout:  extra.Timeout,
	})
	if err != nil {
		return output.NewError("DAEMONCTL_FAILED", err.Error(), "Run workyard daemon start")
	}
	return printDaemonResponse(cmd.OutOrStdout(), res, opts.json || action == "open", action)
}

func actionNeedsLocalSource(action string) bool {
	switch action {
	case "setup", "build", "start", "restart":
		return true
	default:
		return false
	}
}

func ensureLocalDaemonRunning(ctx context.Context, opts *options) error {
	if _, err := worker.Call(daemonSocket(opts), worker.Request{Action: "ping"}); err == nil {
		return nil
	}
	_, err := launchLocalDaemon(ctx, opts, false)
	return err
}

func prepareLocalSource(ctx context.Context, loaded config.Loaded, paths remote.Paths) error {
	source := filepath.FromSlash(paths.Source)
	logs := filepath.FromSlash(paths.Logs)
	if err := guardLocalManagedPaths(paths); err != nil {
		return err
	}
	if err := os.MkdirAll(source, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return err
	}
	if err := guardLocalManagedPaths(paths); err != nil {
		return err
	}
	excludeFile, err := writeLocalExcludeFile(loaded.Config)
	if err != nil {
		return err
	}
	defer os.Remove(excludeFile)

	var stderr bytes.Buffer
	args := []string{"-az", "--delete", "--delete-excluded", "--exclude-from", excludeFile, "--", loaded.Config.Root + string(filepath.Separator), source + string(filepath.Separator)}
	rsync := exec.CommandContext(ctx, "rsync", args...)
	rsync.Stderr = &stderr
	if err := rsync.Run(); err != nil {
		return fmt.Errorf("rsync local source failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func guardLocalManagedPaths(paths remote.Paths) error {
	for _, value := range []string{
		filepath.FromSlash(path.Join(paths.Home, ".workyard")),
		filepath.FromSlash(path.Join(paths.Home, ".workyard", "runs")),
		filepath.FromSlash(path.Dir(paths.RunRoot)),
		filepath.FromSlash(paths.RunRoot),
		filepath.FromSlash(paths.Source),
		filepath.FromSlash(paths.Logs),
	} {
		info, err := os.Lstat(value)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink path: %s", value)
		}
	}
	return nil
}

func writeLocalExcludeFile(cfg config.Config) (string, error) {
	f, err := os.CreateTemp("", "workyard-local-excludes-*.txt")
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, item := range append(syncer.BuiltinExcludes, cfg.Sync.Exclude...) {
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

func remoteControl(cmd *cobra.Command, opts *options, loaded config.Loaded, action, run string, services []string, extra controlExtra) error {
	ctx := cmd.Context()
	home, err := remote.Home(ctx, opts.worker)
	if err != nil {
		return output.NewError("SSH_FAILED", err.Error(), "Check Tailscale/SSH connectivity to the worker")
	}
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, run)
	if err != nil {
		return output.NewError("REMOTE_PATH_INVALID", err.Error(), "")
	}
	rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
		Worker:           opts.worker,
		Project:          paths.Project,
		RunID:            paths.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    paths.RunRoot,
		RemoteSourcePath: paths.Source,
		RemoteBinary:     opts.remoteBinary,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
	if action == "setup" || action == "build" || action == "start" || action == "restart" || action == "status" || action == "inspect" || action == "urls" || action == "logs" || action == "events" || action == "wait" || action == "probe" || action == "stop" {
		if err := remote.EnsureDaemon(ctx, opts.worker, paths, opts.remoteBinary); err != nil {
			return output.NewError("DAEMON_START_FAILED", err.Error(), "Copy the linux arm64 workyard binary to ~/.workyard/bin/workyard on the worker")
		}
	}
	binary := paths.Binary
	if opts.remoteBinary != "" {
		binary = opts.remoteBinary
	}
	argv := []string{binary, "daemonctl", action, "--socket", paths.Socket, "--run-root", paths.RunRoot, "--project-name", paths.Project, "--run-id", paths.RunID, "--worker-name", opts.worker}
	if opts.json {
		argv = append(argv, "--json")
	}
	if extra.All {
		argv = append(argv, "--all")
	}
	if extra.Tail > 0 {
		argv = append(argv, "--tail", fmt.Sprint(extra.Tail))
	}
	if extra.MaxBytes > 0 {
		argv = append(argv, "--max-bytes", fmt.Sprint(extra.MaxBytes))
	}
	if extra.Stream != "" {
		argv = append(argv, "--stream", extra.Stream)
	}
	if extra.Healthy {
		argv = append(argv, "--healthy")
	}
	if extra.Status != "" {
		argv = append(argv, "--status", extra.Status)
	}
	if extra.Timeout != "" {
		argv = append(argv, "--timeout", extra.Timeout)
	}
	argv = append(argv, services...)
	res, err := remote.Run(ctx, opts.worker, argv, nil, remoteTimeout(action, extra.Timeout))
	if res.Stdout != "" {
		_, _ = io.WriteString(cmd.OutOrStdout(), res.Stdout)
	}
	if res.Stderr != "" && opts.verbose {
		_, _ = io.WriteString(cmd.ErrOrStderr(), res.Stderr)
	}
	if err != nil {
		return printedError{err: err, exitCode: 1}
	}
	return nil
}

func printDaemonResponse(w io.Writer, res worker.Response, jsonOut bool, action string) error {
	if jsonOut {
		if action == "logs" {
			for _, entry := range res.Entries {
				if err := output.WriteJSONLine(w, entry); err != nil {
					return err
				}
			}
			if !res.OK && res.Error != nil {
				return output.WriteJSONLine(w, res)
			}
			return nil
		}
		if action == "events" {
			for _, ev := range res.Events {
				if err := output.WriteJSONLine(w, ev); err != nil {
					return err
				}
			}
			if !res.OK && res.Error != nil {
				return output.WriteJSONLine(w, res)
			}
			return nil
		}
		return output.WriteJSON(w, res)
	}
	if !res.OK {
		if res.Error != nil {
			return output.NewError(res.Error.Code, res.Error.Message, res.Error.Hint)
		}
		return output.NewError("DAEMON_FAILED", "daemon request failed", "")
	}
	switch action {
	case "logs":
		for _, entry := range res.Entries {
			_, _ = fmt.Fprintln(w, entry.Line)
		}
	case "events":
		for _, ev := range res.Events {
			_, _ = fmt.Fprintf(w, "%s %s %s\n", ev.Time.Format(time.RFC3339), ev.Type, ev.Message)
		}
	case "urls":
		for _, u := range res.URLs {
			_, _ = fmt.Fprintf(w, "%s %s\n", u.Service, u.URL)
		}
	case "probe":
		_, _ = fmt.Fprintln(w, res.Message)
	case "setup", "build":
		_, _ = fmt.Fprintln(w, res.Message)
	default:
		rows := make([][]string, 0, len(res.Services))
		for _, svc := range res.Services {
			rows = append(rows, []string{svc.Name, svc.Status, fmt.Sprint(svc.Healthy), fmt.Sprint(svc.PID), fmt.Sprint(svc.AssignedPort), svc.URL})
		}
		return output.WriteTable(w, []string{"SERVICE", "STATUS", "HEALTHY", "PID", "PORT", "URL"}, rows)
	}
	return nil
}

func defaultSocket(stateDir string) string {
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".workyard")
	}
	return filepath.Join(stateDir, "daemon", "workyard.sock")
}

func daemonSocket(opts *options) string {
	if opts.socket != "" {
		return opts.socket
	}
	return defaultSocket(opts.stateDir)
}

func daemonStateDir(opts *options) string {
	if opts.stateDir != "" {
		return opts.stateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".workyard")
}

func rememberRun(w io.Writer, opts *options, loaded config.Loaded, ref registry.RunRef) {
	if ref.Worker == "" {
		return
	}
	if ref.LocalRoot == "" {
		ref.LocalRoot = loaded.Config.Root
	}
	if ref.ConfigPath == "" {
		ref.ConfigPath = loaded.Config.Path
	}
	store := registry.New(registry.DefaultPath(opts.stateDir))
	if err := store.Upsert(ref); err != nil && opts.verbose {
		_, _ = fmt.Fprintf(w, "warning: failed to update local monitor registry: %s\n", err)
	}
}

func remoteTimeout(action, timeout string) time.Duration {
	if (action == "wait" || action == "start" || action == "restart") && timeout != "" {
		d, err := time.ParseDuration(timeout)
		if err == nil {
			return d + 10*time.Second
		}
	}
	if action == "setup" || action == "build" || action == "start" || action == "restart" {
		return 90 * time.Second
	}
	return 30 * time.Second
}

func workerNameForURL(workerHost string) string {
	if strings.Contains(workerHost, "@") {
		parts := strings.Split(workerHost, "@")
		return parts[len(parts)-1]
	}
	return workerHost
}
