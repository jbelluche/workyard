package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	osuser "os/user"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackbelluche/workyard/internal/bootstrap"
	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/doctor"
	"github.com/jackbelluche/workyard/internal/mirror"
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

// Version is stamped at build time via:
//
//	-ldflags "-X github.com/jackbelluche/workyard/internal/cli.Version=<version>"
var Version = "0.1.0"

const (
	commandGroupPrimary     = "primary"
	commandGroupConfig      = "config"
	commandGroupLifecycle   = "lifecycle"
	commandGroupInspection  = "inspection"
	commandGroupWorkers     = "workers"
	commandGroupMaintenance = "maintenance"
	commandGroupUtility     = "utility"
)

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
	cobra.EnableCommandSorting = false
	root := &cobra.Command{
		Use:           "workyard",
		Short:         "Remote development runner",
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
	_ = root.RegisterFlagCompletionFunc("worker", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return workerCompletions(opts.stateDir), cobra.ShellCompDirectiveNoFileComp
	})

	root.AddGroup(
		&cobra.Group{ID: commandGroupPrimary, Title: "Primary Workflows"},
		&cobra.Group{ID: commandGroupConfig, Title: "Project Configuration"},
		&cobra.Group{ID: commandGroupLifecycle, Title: "Lifecycle Steps"},
		&cobra.Group{ID: commandGroupInspection, Title: "Runtime Inspection"},
		&cobra.Group{ID: commandGroupWorkers, Title: "Worker Management"},
		&cobra.Group{ID: commandGroupMaintenance, Title: "Run Maintenance"},
		&cobra.Group{ID: commandGroupUtility, Title: "Utility"},
	)
	root.SetHelpCommandGroupID(commandGroupUtility)
	root.SetCompletionCommandGroupID(commandGroupUtility)

	root.AddCommand(groupCommand(deployCommand(opts), commandGroupPrimary))
	root.AddCommand(groupCommand(watchCommand(opts), commandGroupPrimary))
	root.AddCommand(groupCommand(mirrorCommand(opts), commandGroupPrimary))

	root.AddCommand(groupCommand(initCommand(opts), commandGroupConfig))
	root.AddCommand(groupCommand(configCommand(opts), commandGroupConfig))
	root.AddCommand(groupCommand(servicesCommand(opts), commandGroupConfig))

	root.AddCommand(groupCommand(syncCommand(opts), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "setup"), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "build"), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "start"), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "stop"), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "restart"), commandGroupLifecycle))
	root.AddCommand(groupCommand(waitCommand(opts), commandGroupLifecycle))
	root.AddCommand(groupCommand(controlCommand(opts, "probe"), commandGroupLifecycle))

	root.AddCommand(groupCommand(controlCommand(opts, "status"), commandGroupInspection))
	root.AddCommand(groupCommand(controlCommand(opts, "inspect"), commandGroupInspection))
	root.AddCommand(groupCommand(logsCommand(opts), commandGroupInspection))
	root.AddCommand(groupCommand(eventsCommand(opts), commandGroupInspection))
	root.AddCommand(groupCommand(controlCommand(opts, "urls"), commandGroupInspection))
	root.AddCommand(groupCommand(openCommand(opts), commandGroupInspection))
	root.AddCommand(groupCommand(uiCommand(opts), commandGroupInspection))

	root.AddCommand(groupCommand(workersCommand(opts), commandGroupWorkers))
	root.AddCommand(groupCommand(installCommand(opts), commandGroupWorkers))
	root.AddCommand(groupCommand(doctorCommand(opts), commandGroupWorkers))
	root.AddCommand(groupCommand(daemonCommand(opts), commandGroupWorkers))

	root.AddCommand(groupCommand(runsCommand(opts), commandGroupMaintenance))
	root.AddCommand(groupCommand(cleanupCommand(opts), commandGroupMaintenance))

	root.AddCommand(groupCommand(updateCommand(opts), commandGroupUtility))
	root.AddCommand(groupCommand(versionCommand(opts), commandGroupUtility))
	root.AddCommand(daemonctlCommand(opts))
	root.AddCommand(portcheckCommand(opts))
	return root
}

// portcheckCommand is a hidden helper doctor runs on workers (over SSH) to
// test port availability without depending on python3 being installed.
func portcheckCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:    "portcheck <start> <end>",
		Short:  "Print the first free loopback port in a range",
		Hidden: true,
		Args:   cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			start, err1 := strconv.Atoi(args[0])
			end, err2 := strconv.Atoi(args[1])
			if err1 != nil || err2 != nil || start <= 0 || end > 65535 || end < start {
				return output.NewError("PORT_RANGE_INVALID", "portcheck requires numeric <start> <end> within 1-65535", "")
			}
			for port := start; port <= end; port++ {
				ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if err != nil {
					continue
				}
				_ = ln.Close()
				if opts.json {
					return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "port": port})
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), port)
				return nil
			}
			return output.NewError("PORT_UNAVAILABLE", fmt.Sprintf("no free port in %d-%d", start, end), "")
		},
	}
}

func groupCommand(cmd *cobra.Command, groupID string) *cobra.Command {
	cmd.GroupID = groupID
	return cmd
}

func installCommand(opts *options) *cobra.Command {
	var artifactDir string
	var localBinary string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install or upgrade the Workyard binary on a worker",
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.worker == "" {
				return output.NewError("WORKER_REQUIRED", "--worker is required for install", "")
			}
			if registry.IsLocalWorker(opts.worker) {
				return output.NewError("WORKER_LOCAL_INSTALL_UNSUPPORTED", "install is only needed for remote workers", "Use the local install script to update this machine")
			}
			platform, err := remote.DetectPlatform(cmd.Context(), opts.worker)
			if err != nil {
				return output.NewError("WORKER_PLATFORM_FAILED", err.Error(), "Check SSH access and worker OS/architecture")
			}
			binary, err := remote.EnsureArtifact(cmd.Context(), platform, artifactDir, localBinary, Version)
			if err != nil {
				return output.NewError("WORKER_ARTIFACT_MISSING", err.Error(), "Build it with GOOS="+platform.OS+" GOARCH="+platform.Arch+" go build -o dist/"+platform.ArtifactName()+" ./cmd/workyard or pass --local-binary")
			}
			res, err := remote.InstallBinary(cmd.Context(), opts.worker, platform, remote.InstallOptions{
				LocalBinary:     binary,
				RemoteBinary:    opts.remoteBinary,
				ExpectedVersion: Version,
			})
			if err != nil {
				return output.NewError("WORKER_INSTALL_FAILED", err.Error(), "Check SSH access to the worker and rerun with --verbose")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			output.Successf(cmd.OutOrStdout(), "installed %s to %s:%s (%s)", res.LocalBinary, res.Worker, res.RemoteBinary, res.InstalledVersion)
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
		Short: "Remove a run from the local monitor registry (accepts registered worker names)",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := registry.New(registry.DefaultPath(opts.stateDir))
			removed, err := store.Remove(args[0], args[1], args[2])
			if err != nil {
				return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
			}
			if !removed {
				if resolved, rerr := resolveWorkerTarget(opts.stateDir, args[0]); rerr == nil && resolved != args[0] {
					removed, err = store.Remove(resolved, args[1], args[2])
					if err != nil {
						return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
					}
				}
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removed": removed})
			}
			if removed {
				output.Successf(cmd.OutOrStdout(), "removed")
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "not found"))
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
			output.Successf(cmd.OutOrStdout(), "removed %d stale run(s)", len(removed))
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
			output.Successf(cmd.OutOrStdout(), "registered %s as %s", worker.Name, worker.EffectiveSSHTarget())
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
			if registry.IsLocalWorker(args[0]) {
				return output.NewError("WORKER_RESERVED", registry.LocalWorkerName+" is a built-in worker and cannot be removed", "Use workyard runs remove or workyard runs prune to clean local run references")
			}
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
			output.Successf(cmd.OutOrStdout(), "removed worker=%t runRefs=%d", removedWorker, count)
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
				output.Warningf(cmd.OutOrStdout(), "tailscale: %s", tailscaleErr)
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
	rows := make([]workerListRow, 0, len(registered)+len(runWorkers)+1)
	localRunCount := 0
	var localUpdated time.Time
	if ref, ok := runCounts[registry.LocalWorkerName]; ok {
		localRunCount = ref.RunCount
		localUpdated = ref.UpdatedAt
	}
	rows = append(rows, workerListRow{
		Name:      registry.LocalWorkerName,
		Host:      registry.LocalWorkerName,
		SSHTarget: "local",
		Source:    "builtin",
		RunCount:  localRunCount,
		UpdatedAt: localUpdated,
	})
	seen[registry.LocalWorkerName] = true
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
	rows := make([]workerStatusRow, 0, len(registered)+1)
	rows = append(rows, workerStatusRow{
		Name:        registry.LocalWorkerName,
		Host:        registry.LocalWorkerName,
		User:        currentUsername(),
		SSHTarget:   "local",
		Source:      "builtin",
		Online:      true,
		OnlineKnown: true,
		InTailscale: false,
	})
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
	if registry.IsLocalWorker(raw) || registry.IsLocalWorker(name) || registry.IsLocalWorker(sshTarget) {
		return registry.WorkerConfig{}, output.NewError("WORKER_RESERVED", registry.LocalWorkerName+" is reserved as the built-in local worker", "Use --worker localhost directly for local runs")
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
	if registry.IsLocalWorker(value) {
		return registry.LocalWorkerName, nil
	}
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
	root := &cobra.Command{Use: "cleanup", Aliases: []string{"clean"}, Short: "Safely clean Workyard runs and logs"}
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Truncate log files for the selected run",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, paths, err := cleanupPaths(cmd, opts)
			if err != nil {
				return err
			}
			_ = loaded
			var res remote.CleanupResult
			if registry.IsLocalWorker(opts.worker) {
				res, err = cleanupLocalLogs(paths)
			} else {
				res, err = remote.CleanupLogs(cmd.Context(), opts.worker, paths)
			}
			if err != nil {
				return output.NewError("LOG_CLEANUP_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			if registry.IsLocalWorker(res.Worker) {
				output.Successf(cmd.OutOrStdout(), "cleaned logs at %s", res.RemoteLogPath)
			} else {
				output.Successf(cmd.OutOrStdout(), "cleaned logs at %s:%s", res.Worker, res.RemoteLogPath)
			}
			return nil
		},
	}
	stopFirst := true
	noStop := false
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Stop and remove the selected run directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			loaded, paths, err := cleanupPaths(cmd, opts)
			if err != nil {
				return err
			}
			if noStop {
				stopFirst = false
			}
			if stopFirst {
				if registry.IsLocalWorker(opts.worker) {
					if _, err := localDaemonCall(cmd.Context(), opts, paths, "stop", nil, controlExtra{All: true}); err != nil {
						if opts.verbose && !opts.json {
							output.Warningf(cmd.ErrOrStderr(), "stop before cleanup skipped: %s", err)
						}
					}
				} else {
					oldOut := cmd.OutOrStdout()
					cmd.SetOut(io.Discard)
					err := remoteControl(cmd, opts, loaded, "stop", paths.RunID, nil, controlExtra{All: true})
					cmd.SetOut(oldOut)
					if err != nil {
						return err
					}
				}
			}
			var res remote.CleanupResult
			if registry.IsLocalWorker(opts.worker) {
				res, err = cleanupLocalRun(paths)
			} else {
				res, err = remote.CleanupRun(cmd.Context(), opts.worker, paths)
			}
			if err != nil {
				return output.NewError("RUN_CLEANUP_FAILED", err.Error(), "")
			}
			store := registry.New(registry.DefaultPath(opts.stateDir))
			_, _ = store.Remove(opts.worker, paths.Project, paths.RunID)
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), res)
			}
			if registry.IsLocalWorker(res.Worker) {
				output.Successf(cmd.OutOrStdout(), "removed run at %s", res.RemoteRunPath)
			} else {
				output.Successf(cmd.OutOrStdout(), "removed run at %s:%s", res.Worker, res.RemoteRunPath)
			}
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
		return config.Loaded{}, remote.Paths{}, output.NewError("WORKER_REQUIRED", "--worker is required for cleanup", "Pass --worker localhost for this machine or --worker <name> for a registered worker")
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
	if registry.IsLocalWorker(opts.worker) {
		paths, err := buildLocalPaths(opts, loaded.Config.Name, run)
		if err != nil {
			return config.Loaded{}, remote.Paths{}, err
		}
		return loaded, paths, nil
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

// buildLocalPaths derives localhost run paths from the active state dir so
// sync, daemon validation, cleanup guards, and log paths all agree when
// --state-dir is set.
func buildLocalPaths(opts *options, projectName, run string) (remote.Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return remote.Paths{}, output.NewError("LOCAL_HOME_FAILED", err.Error(), "")
	}
	paths, err := remote.BuildLocalPaths(filepath.ToSlash(home), filepath.ToSlash(daemonStateDir(opts)), opts.remoteRoot, projectName, run)
	if err != nil {
		return remote.Paths{}, output.NewError("LOCAL_PATH_INVALID", err.Error(), "")
	}
	return paths, nil
}

func localRunExists(paths remote.Paths) (bool, error) {
	root, err := localManagedRunRoot(paths)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("refusing symlink run path: %s", root)
	}
	return info.IsDir(), nil
}

func cleanupLocalRun(paths remote.Paths) (remote.CleanupResult, error) {
	root, err := localManagedRunRoot(paths)
	if err != nil {
		return remote.CleanupResult{}, err
	}
	if err := guardLocalManagedPaths(paths); err != nil {
		return remote.CleanupResult{}, err
	}
	if err := os.RemoveAll(root); err != nil {
		return remote.CleanupResult{}, err
	}
	return remote.CleanupResult{OK: true, Worker: registry.LocalWorkerName, RemoteRunPath: paths.RunRoot, Action: "run"}, nil
}

func cleanupLocalLogs(paths remote.Paths) (remote.CleanupResult, error) {
	if _, err := localManagedRunRoot(paths); err != nil {
		return remote.CleanupResult{}, err
	}
	if err := guardLocalManagedPaths(paths); err != nil {
		return remote.CleanupResult{}, err
	}
	logs := filepath.FromSlash(paths.Logs)
	if err := os.MkdirAll(logs, 0o700); err != nil {
		return remote.CleanupResult{}, err
	}
	if err := filepath.WalkDir(logs, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink log file: %s", file)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if filepath.Ext(file) != ".log" && filepath.Ext(file) != ".jsonl" {
			return nil
		}
		return os.WriteFile(file, nil, 0o600)
	}); err != nil {
		return remote.CleanupResult{}, err
	}
	return remote.CleanupResult{OK: true, Worker: registry.LocalWorkerName, RemoteRunPath: paths.RunRoot, RemoteLogPath: paths.Logs, Action: "logs"}, nil
}

func localManagedRunRoot(paths remote.Paths) (string, error) {
	if strings.TrimSpace(paths.Home) == "" || strings.TrimSpace(paths.RunRoot) == "" {
		return "", fmt.Errorf("local paths are incomplete")
	}
	root := filepath.Clean(filepath.FromSlash(paths.RunRoot))
	runsRoot := filepath.Clean(filepath.FromSlash(path.Join(localStateBase(paths), "runs")))
	rel, err := filepath.Rel(runsRoot, root)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return "", fmt.Errorf("run path must stay under %s", runsRoot)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("run path must be exactly under %s/<project>/<run>", runsRoot)
	}
	if filepath.Clean(filepath.FromSlash(paths.Logs)) != filepath.Join(root, "logs") {
		return "", fmt.Errorf("logs path must be inside the run root")
	}
	return root, nil
}

func localStateBase(paths remote.Paths) string {
	if strings.TrimSpace(paths.StateDir) != "" {
		return paths.StateDir
	}
	return path.Join(paths.Home, ".workyard")
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
				StateDir:     opts.stateDir,
				Socket:       opts.socket,
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
		_, _ = fmt.Fprintln(w)
		output.OKf(w, "required checks passed")
		return
	}
	_, _ = fmt.Fprintln(w)
	output.Failedf(w, "one or more required checks failed")
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
		_, _ = fmt.Fprintln(w)
		output.OKf(w, "worker setup completed")
		return
	}
	_, _ = fmt.Fprintln(w)
	output.Failedf(w, "worker setup did not complete")
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

func uiCommand(opts *options) *cobra.Command {
	var listen string
	var refreshInterval time.Duration
	var open bool
	var autoStartDaemon bool
	cmd := &cobra.Command{
		Use:     "ui",
		Aliases: []string{"server"},
		Short:   "Run the local Workyard monitor UI (with --json, prints listener info first, then serves)",
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
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "workyard ui %s on http://%s\n", output.StatusWord(cmd.ErrOrStderr(), output.RoleInfo, "listening"), listen)
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
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:3099", "loopback address for the monitor UI")
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
			if err := requireWorker(opts, "watch"); err != nil {
				return err
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
			var localPaths remote.Paths
			if registry.IsLocalWorker(opts.worker) {
				localPaths, err = buildLocalPaths(opts, loaded.Config.Name, run)
				if err != nil {
					return err
				}
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
						_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "watch %s %s\n", output.Styled(cmd.ErrOrStderr(), output.RoleWarning, "warning:"), err)
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
				syncOpts := syncer.Options{
					Worker:     opts.worker,
					RunID:      run,
					RemoteRoot: opts.remoteRoot,
					StateDir:   opts.stateDir,
					Delete:     true,
				}
				var res syncer.Result
				if registry.IsLocalWorker(opts.worker) {
					res, err = syncer.RunLocal(cmd.Context(), loaded, syncOpts, Version)
				} else {
					res, err = syncer.Run(cmd.Context(), loaded, syncOpts, Version)
				}
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
					if registry.IsLocalWorker(opts.worker) {
						if _, err := localDaemonCall(cmd.Context(), opts, localPaths, "restart", []string{spec.Service}, controlExtra{}); err != nil {
							return err
						}
					} else {
						oldOut := cmd.OutOrStdout()
						cmd.SetOut(io.Discard)
						if err := remoteControl(cmd, opts, loaded, "restart", run, []string{spec.Service}, controlExtra{}); err != nil {
							cmd.SetOut(oldOut)
							return err
						}
						cmd.SetOut(oldOut)
					}
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
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "%s %s; %s %s\n", output.StatusWord(cmd.ErrOrStderr(), output.RoleSuccess, "synced"), res.RemoteSourcePath, output.StatusWord(cmd.ErrOrStderr(), output.RoleSuccess, "restarted"), strings.Join(restarted, ","))
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

func mirrorCommand(opts *options) *cobra.Command {
	var once bool
	var pollInterval time.Duration
	root := &cobra.Command{
		Use:   "mirror",
		Short: "Continuously mirror registered directories to workers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMirrorForeground(cmd, opts, once, pollInterval)
		},
	}
	root.Flags().BoolVar(&once, "once", false, "sync enabled mirrors once and exit")
	root.Flags().DurationVar(&pollInterval, "poll-interval", 500*time.Millisecond, "fallback polling interval")
	root.AddCommand(mirrorHelpCommand(), mirrorSetupCommand(opts), mirrorListCommand(opts), mirrorSyncCommand(opts), mirrorStartCommand(opts), mirrorStopCommand(opts), mirrorStatusCommand(opts), mirrorPauseCommand(opts), mirrorResumeCommand(opts), mirrorRenameCommand(opts), mirrorDoctorCommand(opts), mirrorShellCommand(opts), mirrorExecCommand(opts), mirrorServicesCommand(opts), mirrorTmuxCommand(opts), mirrorDeleteCommand(opts))
	return root
}

func mirrorHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "help",
		Short:  "Help about mirror",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Parent().Help()
		},
	}
}

func mirrorSetupCommand(opts *options) *cobra.Command {
	var localRoot string
	var remotePath string
	var name string
	var force bool
	var yes bool
	var includeGit bool
	var noDelete bool
	var presets []string
	includeGit = true
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure a directory mirror with an interactive wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			reader := bufio.NewReader(cmd.InOrStdin())
			remotePathFlag := cmd.Flags().Changed("remote-path")
			if localRoot == "" {
				cwd, err := os.Getwd()
				if err != nil {
					return err
				}
				localRoot = promptLine(cmd.OutOrStdout(), reader, "Local directory to mirror", cwd)
			}
			localRootAbs, err := filepath.Abs(localRoot)
			if err != nil {
				return output.NewError("MIRROR_LOCAL_INVALID", err.Error(), "")
			}
			if name == "" {
				name = mirror.DefaultName(localRootAbs)
			}
			resolvedPresets, err := mirror.ResolvePresetSelection(localRootAbs, presets)
			if err != nil {
				return output.NewError("MIRROR_PRESET_INVALID", err.Error(), "")
			}
			selectedWorker := opts.worker
			if selectedWorker == "" {
				selectedWorker, err = promptMirrorWorker(cmd.OutOrStdout(), reader, opts.stateDir)
				if err != nil {
					return err
				}
			}
			if remotePath == "" {
				remotePath = promptLine(cmd.OutOrStdout(), reader, "Remote destination", mirror.DefaultRemotePath(localRootAbs))
			}
			profile := mirror.Profile{
				Name:          name,
				Enabled:       true,
				LocalRoot:     localRootAbs,
				Worker:        selectedWorker,
				RemotePath:    remotePath,
				Delete:        !noDelete,
				IncludeGit:    includeGit,
				AllowNonEmpty: force,
				Presets:       resolvedPresets,
			}
			profile, err = mirror.Normalize(profile)
			if err != nil {
				return output.NewError("MIRROR_CONFIG_INVALID", err.Error(), "")
			}
			resolved, err := resolveMirrorProfile(opts.stateDir, profile)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			check, err := checkedMirrorDestination(cmd, opts, reader, &profile, &resolved, remotePathFlag, force)
			if err != nil {
				return err
			}
			if !yes {
				printMirrorSetupSummary(cmd.OutOrStdout(), profile, resolved.Worker, check)
				if force && check.State == "non-empty" {
					if got := promptLine(cmd.OutOrStdout(), reader, "Type mirror name to confirm non-empty destination", ""); got != profile.Name {
						return output.NewError("MIRROR_SETUP_CANCELLED", "mirror setup cancelled", "")
					}
				} else if !promptYes(cmd.OutOrStdout(), reader, "Create this mirror?", true) {
					return output.NewError("MIRROR_SETUP_CANCELLED", "mirror setup cancelled", "")
				}
			}
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			stored, err := store.Upsert(profile)
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_WRITE_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "path": store.Path(), "mirror": stored, "destination": check})
			}
			output.Successf(cmd.OutOrStdout(), "configured mirror %s (%s)", stored.Name, stored.ID)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "registry: %s\n", store.Path())
			return nil
		},
	}
	cmd.Flags().StringVar(&localRoot, "local", "", "local directory to mirror (defaults to the current directory)")
	cmd.Flags().StringVar(&remotePath, "remote-path", "", "remote destination path")
	cmd.Flags().StringVar(&name, "name", "", "mirror profile name")
	cmd.Flags().BoolVar(&force, "force", false, "allow setup when the remote destination is not empty")
	cmd.Flags().BoolVar(&yes, "yes", false, "accept setup confirmation prompts")
	cmd.Flags().BoolVar(&includeGit, "include-git", true, "include the .git directory in mirror syncs")
	cmd.Flags().BoolVar(&noDelete, "no-delete", false, "do not delete remote files removed locally")
	cmd.Flags().StringSliceVar(&presets, "preset", []string{"auto"}, "exclude preset: auto, none, or one of "+strings.Join(mirror.PresetNames(), ", "))
	return cmd
}

func mirrorListCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured mirrors",
		RunE: func(cmd *cobra.Command, args []string) error {
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profiles, err := store.List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "path": store.Path(), "mirrors": profiles})
			}
			rows := make([][]string, 0, len(profiles))
			for _, profile := range profiles {
				rows = append(rows, []string{profile.ID, profile.Name, fmt.Sprint(profile.Enabled), profile.Worker, strings.Join(profile.Presets, ","), profile.LocalRoot, profile.RemotePath, formatTime(profile.UpdatedAt)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"ID", "NAME", "ENABLED", "WORKER", "PRESETS", "LOCAL", "REMOTE", "UPDATED"}, rows)
		},
	}
}

func mirrorSyncCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "sync [name-or-id]",
		Short: "Sync configured mirrors once",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			var profiles []mirror.Profile
			if len(args) == 1 {
				profile, ok, err := store.Get(args[0])
				if err != nil {
					return mirrorRefCommandError(err)
				}
				if !ok {
					return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
				}
				profile.Enabled = true
				profiles = []mirror.Profile{profile}
			} else {
				var err error
				profiles, err = store.List()
				if err != nil {
					return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
				}
				if mirrorEnabledCount(profiles) == 0 {
					return mirrorNoEnabledConfiguredError(profiles)
				}
			}
			profiles, err := resolveMirrorProfiles(opts.stateDir, profiles)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			return runMirrorOnce(cmd, opts, profiles)
		},
	}
}

func mirrorStartCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start background mirroring",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath := mirror.DefaultPIDPath(opts.stateDir)
			if pid, ok := readRunningPID(pidPath); ok {
				if opts.json {
					return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "pid": pid, "pidPath": pidPath})
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror already %s pid=%d\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "running"), pid)
				return nil
			}
			profiles, err := mirror.NewStore(mirror.DefaultPath(opts.stateDir)).List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			if mirrorEnabledCount(profiles) == 0 {
				return mirrorNoEnabledConfiguredError(profiles)
			}
			pid, logPath, err := launchMirrorProcess(opts)
			if err != nil {
				return output.NewError("MIRROR_START_FAILED", err.Error(), mirrorStartFailureHint(logPath))
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "pid": pid, "pidPath": pidPath, "log": logPath})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror %s pid=%d log=%s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "started"), pid, logPath)
			return nil
		},
	}
}

func mirrorStopCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop background mirroring",
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath := mirror.DefaultPIDPath(opts.stateDir)
			pid, ok := readRunningPID(pidPath)
			if !ok {
				_ = os.Remove(pidPath)
				if opts.json {
					return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": false, "message": "mirror was not running"})
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror was %s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "not running"))
				return nil
			}
			if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
				if fallbackErr := syscall.Kill(pid, syscall.SIGTERM); fallbackErr != nil {
					return output.NewError("MIRROR_STOP_FAILED", fallbackErr.Error(), "")
				}
			}
			waitForPIDStopped(pid, 5*time.Second)
			_ = os.Remove(pidPath)
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": false, "pid": pid})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror %s pid=%d\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "stopped"), pid)
			return nil
		},
	}
}

func mirrorStatusCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "status [name-or-id]",
		Short: "Show background mirror status and last sync results",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pidPath := mirror.DefaultPIDPath(opts.stateDir)
			pid, running := readRunningPID(pidPath)
			stateStore := mirror.NewStateStore(mirror.DefaultStatePath(opts.stateDir))
			state, err := stateStore.Read()
			if err != nil {
				return output.NewError("MIRROR_STATE_READ_FAILED", err.Error(), "")
			}
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profiles, err := store.List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			if len(args) == 1 {
				profile, ok, err := mirror.Resolve(profiles, args[0])
				if err != nil {
					return mirrorRefCommandError(err)
				}
				if !ok {
					return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
				}
				profiles = []mirror.Profile{profile}
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": running, "pid": pid, "pidPath": pidPath, "statePath": mirror.DefaultStatePath(opts.stateDir), "state": state, "mirrors": profiles})
			}
			if running {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror %s pid=%d\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "running"), pid)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "mirror %s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "stopped"))
			}
			statusByID := map[string]mirror.RuntimeStatus{}
			for _, item := range state.Mirrors {
				statusByID[item.ID] = item
			}
			rows := make([][]string, 0, len(profiles))
			for _, profile := range profiles {
				status := statusByID[profile.ID]
				stateValue := status.State
				if stateValue == "" {
					stateValue = "-"
				}
				lastErr := status.LastError
				if lastErr == "" {
					lastErr = "-"
				}
				rows = append(rows, []string{profile.ID, profile.Name, profile.Worker, strings.Join(profile.Presets, ","), stateValue, formatTime(status.LastSync), lastErr})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"ID", "NAME", "WORKER", "PRESETS", "STATE", "LAST SYNC", "LAST ERROR"}, rows)
		},
	}
}

func mirrorPauseCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <name-or-id>",
		Short: "Disable a configured mirror",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mirrorSetEnabled(cmd, opts, args[0], false)
		},
	}
}

func mirrorResumeCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <name-or-id>",
		Short: "Enable a configured mirror",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return mirrorSetEnabled(cmd, opts, args[0], true)
		},
	}
}

func mirrorRenameCommand(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "rename <name-or-id> <new-name>",
		Short: "Rename a configured mirror",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profile, ok, err := store.Rename(args[0], args[1])
			if err != nil {
				var ambiguous mirror.AmbiguousRefError
				if errors.As(err, &ambiguous) {
					return mirrorRefCommandError(err)
				}
				return output.NewError("MIRROR_RENAME_INVALID", err.Error(), "")
			}
			if !ok {
				return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "mirror": profile})
			}
			output.Successf(cmd.OutOrStdout(), "renamed mirror %s (%s)", profile.Name, profile.ID)
			return nil
		},
	}
}

func mirrorDoctorCommand(opts *options) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:   "doctor [name-or-id]",
		Short: "Check mirror configuration, connectivity, and destination safety",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profiles, err := store.List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			if len(profiles) == 0 {
				return output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
			}
			if len(args) == 1 {
				profile, ok, err := mirror.Resolve(profiles, args[0])
				if err != nil {
					return mirrorRefCommandError(err)
				}
				if !ok {
					return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
				}
				profiles = []mirror.Profile{profile}
			}
			profiles, err = resolveMirrorProfiles(opts.stateDir, profiles)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			var fixes mirrorFixReport
			if fix {
				fixes = fixMirrorDoctor(cmd.Context(), opts.stateDir, profiles)
			}
			report := mirror.Doctor(cmd.Context(), profiles)
			if opts.json {
				if fix {
					if err := output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": report.OK && fixes.OK, "fixes": fixes, "doctor": report}); err != nil {
						return err
					}
				} else {
					if err := output.WriteJSON(cmd.OutOrStdout(), report); err != nil {
						return err
					}
				}
			} else {
				if fix {
					printMirrorFixReport(cmd.OutOrStdout(), fixes)
				}
				printMirrorDoctorReport(cmd.OutOrStdout(), report)
			}
			if !report.OK || (fix && !fixes.OK) {
				return printedError{err: errors.New("mirror doctor found required check failures"), exitCode: 1}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply safe mirror fixes before reporting")
	return cmd
}

func mirrorShellCommand(opts *options) *cobra.Command {
	var syncBefore bool
	var autoSync bool
	var remoteCommandText string
	var useTmux bool
	var sessionOverride string
	cmd := &cobra.Command{
		Use:   "shell [name-or-id]",
		Short: "Open a shell in a mirrored directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.json {
				return output.NewError("MIRROR_SHELL_JSON_UNSUPPORTED", "mirror shell streams SSH output and does not support --json", "")
			}
			if useTmux && remoteCommandText != "" {
				return output.NewError("MIRROR_SHELL_ARGS_INVALID", "--tmux cannot be used with --command", "Run an interactive tmux shell, or remove --tmux to run a command")
			}
			if sessionOverride != "" && !useTmux {
				return output.NewError("MIRROR_SHELL_ARGS_INVALID", "--session requires --tmux", "")
			}
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profiles, err := store.List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			ref := ""
			if len(args) == 1 {
				ref = args[0]
			}
			profile, ok, err := selectMirrorProfileForShell(profiles, ref)
			if err != nil {
				return mirrorRefCommandError(err)
			}
			if !ok {
				if len(profiles) == 0 {
					return output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
				}
				if ref != "" {
					return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", ref), "Run workyard mirror list")
				}
				return output.NewError("MIRROR_REF_REQUIRED", "multiple mirrors are configured and none uniquely match the current directory", "Pass a mirror id from workyard mirror list")
			}
			resolved, err := resolveMirrorProfile(opts.stateDir, profile)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			if syncBefore {
				autoSync = false
			}
			mode := mirrorPrepareRequireReady
			if syncBefore {
				mode = mirrorPrepareSyncAlways
			} else if autoSync {
				mode = mirrorPrepareSyncAuto
			}
			check, err := prepareMirrorForRemoteUse(cmd.Context(), resolved, mirrorPrepareOptions{
				Mode:     mode,
				Version:  Version,
				Verbose:  opts.verbose,
				Quiet:    opts.quiet,
				Progress: cmd.ErrOrStderr(),
			})
			if err != nil {
				return output.NewError("MIRROR_SHELL_NOT_READY", fmt.Sprintf("%s:%s is not ready: %s", resolved.Worker, resolved.RemotePath, err), "Run workyard mirror shell --auto "+profile.ID+" or workyard mirror --once")
			}
			if remoteCommandText != "" {
				return runMirrorShellRemoteCommand(cmd.Context(), resolved.Worker, check.ResolvedPath, remoteCommandText, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) {
				return output.NewError("MIRROR_SHELL_TTY_REQUIRED", "interactive mirror shell requires a terminal", "Use --command for non-interactive commands")
			}
			if useTmux {
				sessionName, err := mirrorShellSessionName(profile, sessionOverride)
				if err != nil {
					return output.NewError("MIRROR_SHELL_SESSION_INVALID", err.Error(), "")
				}
				if err := requireRemoteCommand(cmd.Context(), resolved.Worker, "tmux"); err != nil {
					return output.NewError("MIRROR_SHELL_TMUX_MISSING", "tmux is not available on the worker", "Install tmux on the worker or run without --tmux")
				}
				if !opts.quiet {
					output.Infof(cmd.ErrOrStderr(), "opening tmux shell %s for %s (%s) at %s:%s", sessionName, profile.Name, profile.ID, resolved.Worker, check.ResolvedPath)
				}
				return runMirrorInteractiveSSH(cmd.Context(), resolved.Worker, mirrorRemoteShellCommand(mirrorTmuxShellScript(check.ResolvedPath, sessionName)), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			if !opts.quiet {
				output.Infof(cmd.ErrOrStderr(), "opening shell for %s (%s) at %s:%s", profile.Name, profile.ID, resolved.Worker, check.ResolvedPath)
			}
			return runMirrorInteractiveSSH(cmd.Context(), resolved.Worker, mirrorRemoteShellCommand(mirrorPlainShellScript(check.ResolvedPath)), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&syncBefore, "sync", false, "sync the mirror once before opening the shell")
	cmd.Flags().BoolVar(&autoSync, "auto", false, "sync once only when the destination is missing or empty")
	cmd.Flags().StringVarP(&remoteCommandText, "command", "c", "", "run a command in the remote mirror and exit")
	cmd.Flags().BoolVar(&useTmux, "tmux", false, "open the shell inside a persistent tmux session")
	cmd.Flags().StringVar(&sessionOverride, "session", "", "tmux session name (defaults to workyard-<mirror-id>)")
	return cmd
}

func mirrorExecCommand(opts *options) *cobra.Command {
	var syncBefore bool
	var autoSync bool
	cmd := &cobra.Command{
		Use:   "exec [name-or-id] -- <command> [args...]",
		Short: "Run a command in a mirrored directory",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, commandArgs, err := parseMirrorExecArgs(cmd, args)
			if err != nil {
				return err
			}
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profiles, err := store.List()
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
			}
			profile, ok, err := selectMirrorProfileForShell(profiles, ref)
			if err != nil {
				return mirrorRefCommandError(err)
			}
			if !ok {
				if len(profiles) == 0 {
					return output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
				}
				if ref != "" {
					return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", ref), "Run workyard mirror list")
				}
				return output.NewError("MIRROR_REF_REQUIRED", "multiple mirrors are configured and none uniquely match the current directory", "Pass a mirror id from workyard mirror list")
			}
			resolved, err := resolveMirrorProfile(opts.stateDir, profile)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			mode := mirrorPrepareRequireReady
			if syncBefore {
				mode = mirrorPrepareSyncAlways
			} else if autoSync {
				mode = mirrorPrepareSyncAuto
			}
			check, err := prepareMirrorForRemoteUse(cmd.Context(), resolved, mirrorPrepareOptions{
				Mode:     mode,
				Version:  Version,
				Verbose:  opts.verbose,
				Quiet:    opts.quiet,
				Progress: cmd.ErrOrStderr(),
			})
			if err != nil {
				return output.NewError("MIRROR_EXEC_NOT_READY", fmt.Sprintf("%s:%s is not ready: %s", resolved.Worker, resolved.RemotePath, err), "Run workyard mirror exec --auto -- <command>")
			}
			return runMirrorShellRemoteCommand(cmd.Context(), resolved.Worker, check.ResolvedPath, mirrorCommandFromArgs(commandArgs), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolVar(&syncBefore, "sync", false, "sync the mirror once before running the command")
	cmd.Flags().BoolVar(&autoSync, "auto", false, "sync once only when the destination is missing or empty")
	return cmd
}

func mirrorServicesCommand(opts *options) *cobra.Command {
	root := &cobra.Command{
		Use:     "services",
		Aliases: []string{"svc"},
		Short:   "Manage services from a mirrored workspace",
	}
	root.AddCommand(mirrorServicesUpCommand(opts))
	for _, action := range []string{"setup", "build"} {
		root.AddCommand(mirrorServicesLifecycleCommand(opts, action))
	}
	for _, action := range []string{"start", "stop", "restart"} {
		root.AddCommand(mirrorServicesControlCommand(opts, action))
	}
	for _, action := range []string{"status", "inspect", "urls"} {
		root.AddCommand(mirrorServicesReadCommand(opts, action))
	}
	root.AddCommand(mirrorServicesLogsCommand(opts))
	root.AddCommand(mirrorServicesEventsCommand(opts))
	root.AddCommand(mirrorServicesWaitCommand(opts))
	root.AddCommand(mirrorServicesCleanupCommand(opts))
	return root
}

func mirrorServicesUpCommand(opts *options) *cobra.Command {
	var noSync bool
	var skipSetup bool
	var skipBuild bool
	var skipWait bool
	var timeout string
	var install bool
	var artifactDir string
	var localBinary string
	cmd := &cobra.Command{
		Use:   "up [name-or-id] [service...]",
		Short: "Sync, prepare, and start services from a mirror",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTimeoutFlag(timeout); err != nil {
				return err
			}
			selection, err := selectMirrorServiceSelection(opts.stateDir, args, true)
			if err != nil {
				return err
			}
			mode := mirrorPrepareSyncAlways
			if noSync {
				mode = mirrorPrepareRequireReady
			}
			ctx := cmd.Context()
			loaded, resolved, check, paths, err := prepareMirrorServiceRun(ctx, opts, selection.Profile, mirrorPrepareOptions{
				Mode:     mode,
				Version:  Version,
				Verbose:  opts.verbose,
				Quiet:    opts.quiet,
				Progress: cmd.ErrOrStderr(),
			})
			if err != nil {
				return output.NewError("MIRROR_SERVICES_NOT_READY", err.Error(), "Run workyard mirror services up --sync "+selection.Profile.ID)
			}
			services := selection.Services
			if err := validateServiceArgs(loaded.Config, "start", services); err != nil {
				return err
			}
			callOpts := *opts
			callOpts.worker = resolved.Worker
			if install {
				if _, err := deployInstall(cmd, &callOpts, deployOptions{artifactDir: artifactDir, localBinary: localBinary}, paths); err != nil {
					return err
				}
				if !opts.quiet && !opts.json {
					output.OKf(cmd.ErrOrStderr(), "install")
				}
			}
			if !skipSetup {
				res, err := remoteDaemonCall(ctx, &callOpts, paths, "setup", nil, controlExtra{})
				if err != nil {
					return output.NewError("MIRROR_SERVICES_SETUP_FAILED", err.Error(), "Run workyard mirror services logs "+selection.Profile.ID+" setup")
				}
				printMirrorServiceStep(cmd.ErrOrStderr(), opts, "setup", res.Message)
			}
			if !skipBuild {
				res, err := remoteDaemonCall(ctx, &callOpts, paths, "build", nil, controlExtra{})
				if err != nil {
					return output.NewError("MIRROR_SERVICES_BUILD_FAILED", err.Error(), "Run workyard mirror services logs "+selection.Profile.ID+" build")
				}
				printMirrorServiceStep(cmd.ErrOrStderr(), opts, "build", res.Message)
			}
			stopExtra := controlExtra{}
			if len(services) == 0 {
				stopExtra.All = true
			}
			if res, err := remoteDaemonCall(ctx, &callOpts, paths, "stop", services, stopExtra); err == nil {
				printMirrorServiceStep(cmd.ErrOrStderr(), opts, "stop", serviceStatusSummary(res.Services))
			} else if opts.verbose && !opts.json {
				output.Warningf(cmd.ErrOrStderr(), "stop before start skipped: %s", err)
			}
			startRes, err := remoteDaemonCall(ctx, &callOpts, paths, "start", services, controlExtra{Timeout: timeout})
			if err != nil {
				return output.NewError("MIRROR_SERVICES_START_FAILED", err.Error(), "Run workyard mirror services inspect "+selection.Profile.ID)
			}
			printMirrorServiceStep(cmd.ErrOrStderr(), opts, "start", serviceStatusSummary(startRes.Services))
			waitServices := services
			if len(waitServices) == 0 {
				waitServices = config.ServiceNames(loaded.Config.Services)
			}
			if !skipWait && len(waitServices) > 0 {
				res, err := remoteDaemonCall(ctx, &callOpts, paths, "wait", waitServices, controlExtra{Healthy: true, Timeout: timeout})
				if err != nil {
					return output.NewError("MIRROR_SERVICES_WAIT_FAILED", err.Error(), "Run workyard mirror services inspect "+selection.Profile.ID)
				}
				printMirrorServiceStep(cmd.ErrOrStderr(), opts, "wait", res.Message)
			}
			urlsRes, err := remoteDaemonCall(ctx, &callOpts, paths, "urls", services, controlExtra{})
			if err != nil {
				return output.NewError("MIRROR_SERVICES_URLS_FAILED", err.Error(), "Run workyard mirror services status "+selection.Profile.ID)
			}
			rememberMirrorServiceRun(cmd.ErrOrStderr(), opts, loaded, resolved, paths, check.ResolvedPath)
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{
					"ok":               true,
					"mirror":           selection.Profile,
					"worker":           resolved.Worker,
					"project":          paths.Project,
					"runId":            paths.RunID,
					"remoteRunPath":    paths.RunRoot,
					"remoteSourcePath": check.ResolvedPath,
					"services":         urlsRes.Services,
					"urls":             urlsRes.URLs,
				})
			}
			printDeployURLs(cmd.OutOrStdout(), urlsRes.URLs)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noSync, "no-sync", false, "do not sync the mirror before starting services")
	cmd.Flags().BoolVar(&skipSetup, "skip-setup", false, "skip project setup")
	cmd.Flags().BoolVar(&skipBuild, "skip-build", false, "skip project build")
	cmd.Flags().BoolVar(&skipWait, "skip-wait", false, "skip waiting for healthy services")
	cmd.Flags().StringVar(&timeout, "timeout", "60s", "health wait timeout")
	cmd.Flags().BoolVar(&install, "install", false, "install or upgrade the remote worker binary before starting")
	cmd.Flags().StringVar(&artifactDir, "artifact-dir", "dist", "directory containing workyard-<os>-<arch> artifacts")
	cmd.Flags().StringVar(&localBinary, "local-binary", "", "specific local binary to upload when --install is set")
	return cmd
}

func mirrorServicesLifecycleCommand(opts *options, action string) *cobra.Command {
	var syncBefore bool
	var autoSync bool
	cmd := &cobra.Command{
		Use:   action + " [name-or-id]",
		Short: "Run mirror service " + action,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode := mirrorServicePrepareMode(syncBefore, autoSync)
			return runMirrorServiceControl(cmd, opts, action, args, false, controlExtra{}, mode)
		},
	}
	cmd.Flags().BoolVar(&syncBefore, "sync", false, "sync the mirror once before running")
	cmd.Flags().BoolVar(&autoSync, "auto", false, "sync once only when the destination is missing or empty")
	return cmd
}

func mirrorServicesControlCommand(opts *options, action string) *cobra.Command {
	var syncBefore bool
	var autoSync bool
	var all bool
	var timeout string
	cmd := &cobra.Command{
		Use:   action + " [name-or-id] [service...]",
		Short: "Run mirror service " + action,
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout != "" {
				if err := validateTimeoutFlag(timeout); err != nil {
					return err
				}
			}
			mode := mirrorServicePrepareMode(syncBefore, autoSync)
			return runMirrorServiceControl(cmd, opts, action, args, true, controlExtra{All: all, Timeout: timeout}, mode)
		},
	}
	cmd.Flags().BoolVar(&syncBefore, "sync", false, "sync the mirror once before running")
	cmd.Flags().BoolVar(&autoSync, "auto", false, "sync once only when the destination is missing or empty")
	if action == "stop" {
		cmd.Flags().BoolVar(&all, "all", false, "stop all services (the default when no services are named)")
	}
	if action == "start" || action == "restart" {
		cmd.Flags().StringVar(&timeout, "timeout", "", "startup health timeout")
	}
	return cmd
}

func mirrorServicesReadCommand(opts *options, action string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " [name-or-id]",
		Short: "Show mirror service " + action,
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMirrorServiceControl(cmd, opts, action, args, false, controlExtra{}, mirrorPrepareRequireReady)
		},
	}
}

func mirrorServicesLogsCommand(opts *options) *cobra.Command {
	var tail int
	var maxBytes int64
	var stream string
	cmd := &cobra.Command{
		Use:   "logs [name-or-id] <service>",
		Short: "Read bounded logs for a mirrored service",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			selection, target, err := selectMirrorServiceLogSelection(opts.stateDir, args)
			if err != nil {
				return err
			}
			return runMirrorServiceSelection(cmd, opts, "logs", selection, []string{target}, controlExtra{Tail: tail, MaxBytes: maxBytes, Stream: stream}, mirrorPrepareRequireReady)
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 200, "lines to read")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 128*1024, "maximum bytes to read")
	cmd.Flags().StringVar(&stream, "stream", "both", "stdout, stderr, or both")
	return cmd
}

func mirrorServicesEventsCommand(opts *options) *cobra.Command {
	var tail int
	var maxBytes int64
	cmd := &cobra.Command{
		Use:   "events [name-or-id]",
		Short: "Read lifecycle events for mirrored services",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMirrorServiceControl(cmd, opts, "events", args, false, controlExtra{Tail: tail, MaxBytes: maxBytes}, mirrorPrepareRequireReady)
		},
	}
	cmd.Flags().IntVar(&tail, "tail", 100, "events to read")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 128*1024, "maximum bytes to read")
	return cmd
}

func mirrorServicesWaitCommand(opts *options) *cobra.Command {
	var healthy bool
	var status string
	var timeout string
	cmd := &cobra.Command{
		Use:   "wait [name-or-id] [service...]",
		Short: "Wait for mirrored services",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTimeoutFlag(timeout); err != nil {
				return err
			}
			return runMirrorServiceControl(cmd, opts, "wait", args, true, controlExtra{Healthy: healthy, Status: status, Timeout: timeout}, mirrorPrepareRequireReady)
		},
	}
	cmd.Flags().BoolVar(&healthy, "healthy", true, "wait for healthy services")
	cmd.Flags().StringVar(&status, "status", "", "wait for a specific status")
	cmd.Flags().StringVar(&timeout, "timeout", "60s", "wait timeout")
	return cmd
}

func mirrorServicesCleanupCommand(opts *options) *cobra.Command {
	var stop bool
	var noStop bool
	cmd := &cobra.Command{
		Use:   "cleanup [name-or-id]",
		Short: "Stop services and remove mirror service run state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selection, err := selectMirrorServiceSelection(opts.stateDir, args, false)
			if err != nil {
				return err
			}
			loaded, resolved, check, paths, err := prepareMirrorServiceRun(cmd.Context(), opts, selection.Profile, mirrorPrepareOptions{
				Mode:     mirrorPrepareRequireReady,
				Version:  Version,
				Verbose:  opts.verbose,
				Quiet:    true,
				Progress: cmd.ErrOrStderr(),
			})
			if err != nil {
				return output.NewError("MIRROR_SERVICES_NOT_READY", err.Error(), "")
			}
			callOpts := *opts
			callOpts.worker = resolved.Worker
			if noStop {
				stop = false
			}
			if stop {
				if _, err := remoteDaemonCall(cmd.Context(), &callOpts, paths, "stop", nil, controlExtra{All: true}); err != nil && opts.verbose {
					output.Warningf(cmd.ErrOrStderr(), "stop before cleanup failed: %s", err)
				}
			}
			if err := cleanupMirrorServiceRun(cmd.Context(), resolved, paths, check.ResolvedPath); err != nil {
				return output.NewError("MIRROR_SERVICES_CLEANUP_FAILED", err.Error(), "")
			}
			store := registry.New(registry.DefaultPath(opts.stateDir))
			_, _ = store.Remove(resolved.Worker, paths.Project, paths.RunID)
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "mirror": selection.Profile, "worker": resolved.Worker, "project": loaded.Config.Name, "runId": paths.RunID, "removed": paths.RunRoot})
			}
			output.Successf(cmd.OutOrStdout(), "removed mirror service run %s:%s", resolved.Worker, paths.RunRoot)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stop, "stop", true, "stop services before removing mirror service run state")
	cmd.Flags().BoolVar(&noStop, "no-stop", false, "skip stopping services before cleanup")
	_ = cmd.Flags().MarkHidden("no-stop")
	return cmd
}

type mirrorServiceSelection struct {
	Profile  mirror.Profile
	Services []string
}

func mirrorServicePrepareMode(syncBefore, autoSync bool) mirrorPrepareMode {
	if syncBefore {
		return mirrorPrepareSyncAlways
	}
	if autoSync {
		return mirrorPrepareSyncAuto
	}
	return mirrorPrepareRequireReady
}

func runMirrorServiceControl(cmd *cobra.Command, opts *options, action string, args []string, allowServices bool, extra controlExtra, mode mirrorPrepareMode) error {
	selection, err := selectMirrorServiceSelection(opts.stateDir, args, allowServices)
	if err != nil {
		return err
	}
	return runMirrorServiceSelection(cmd, opts, action, selection, selection.Services, extra, mode)
}

func runMirrorServiceSelection(cmd *cobra.Command, opts *options, action string, selection mirrorServiceSelection, services []string, extra controlExtra, mode mirrorPrepareMode) error {
	loaded, resolved, check, paths, err := prepareMirrorServiceRun(cmd.Context(), opts, selection.Profile, mirrorPrepareOptions{
		Mode:     mode,
		Version:  Version,
		Verbose:  opts.verbose,
		Quiet:    opts.quiet,
		Progress: cmd.ErrOrStderr(),
	})
	if err != nil {
		return output.NewError("MIRROR_SERVICES_NOT_READY", err.Error(), "Run workyard mirror services up "+selection.Profile.ID)
	}
	if err := validateServiceArgs(loaded.Config, action, services); err != nil {
		return err
	}
	callOpts := *opts
	callOpts.worker = resolved.Worker
	if action == "logs" || action == "events" {
		if err := remoteDaemonPassthrough(cmd.Context(), &callOpts, paths, action, services, extra, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
			var pe printedError
			if errors.As(err, &pe) {
				return err
			}
			if output.AsCommandError(err) != nil {
				return err
			}
			return output.NewError("MIRROR_SERVICES_FAILED", err.Error(), "Run workyard mirror services inspect "+selection.Profile.ID)
		}
		rememberMirrorServiceRun(cmd.ErrOrStderr(), opts, loaded, resolved, paths, check.ResolvedPath)
		return nil
	}
	res, err := remoteDaemonCall(cmd.Context(), &callOpts, paths, action, services, extra)
	if err != nil {
		return output.NewError("MIRROR_SERVICES_FAILED", err.Error(), "Run workyard mirror services inspect "+selection.Profile.ID)
	}
	rememberMirrorServiceRun(cmd.ErrOrStderr(), opts, loaded, resolved, paths, check.ResolvedPath)
	return printDaemonResponse(cmd.OutOrStdout(), res, opts.json, action)
}

func selectMirrorServiceSelection(stateDir string, args []string, allowServices bool) (mirrorServiceSelection, error) {
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profiles, err := store.List()
	if err != nil {
		return mirrorServiceSelection{}, output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
	}
	ref := ""
	services := args
	if len(args) > 0 {
		if _, ok, err := mirror.Resolve(profiles, args[0]); err != nil {
			return mirrorServiceSelection{}, mirrorRefCommandError(err)
		} else if ok {
			ref = args[0]
			services = args[1:]
		}
	}
	if !allowServices && len(services) > 0 {
		return mirrorServiceSelection{}, output.NewError("MIRROR_SERVICES_ARGS_INVALID", "this mirror services command does not accept service names", "")
	}
	profile, ok, err := selectMirrorProfileForShell(profiles, ref)
	if err != nil {
		return mirrorServiceSelection{}, mirrorRefCommandError(err)
	}
	if !ok {
		return mirrorServiceSelection{}, mirrorSelectionError(profiles, ref)
	}
	return mirrorServiceSelection{Profile: profile, Services: services}, nil
}

func selectMirrorServiceLogSelection(stateDir string, args []string) (mirrorServiceSelection, string, error) {
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profiles, err := store.List()
	if err != nil {
		return mirrorServiceSelection{}, "", output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
	}
	ref := ""
	target := ""
	if len(args) == 2 {
		ref = args[0]
		target = args[1]
	} else {
		if _, ok, err := mirror.Resolve(profiles, args[0]); err != nil {
			return mirrorServiceSelection{}, "", mirrorRefCommandError(err)
		} else if ok {
			return mirrorServiceSelection{}, "", output.NewError("MIRROR_SERVICES_ARGS_INVALID", "logs requires a service target after the mirror id or name", "Use workyard mirror services logs "+args[0]+" <service>")
		}
		target = args[0]
	}
	profile, ok, err := selectMirrorProfileForShell(profiles, ref)
	if err != nil {
		return mirrorServiceSelection{}, "", mirrorRefCommandError(err)
	}
	if !ok {
		return mirrorServiceSelection{}, "", mirrorSelectionError(profiles, ref)
	}
	return mirrorServiceSelection{Profile: profile}, target, nil
}

func mirrorSelectionError(profiles []mirror.Profile, ref string) error {
	if len(profiles) == 0 {
		return output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
	}
	if ref != "" {
		return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", ref), "Run workyard mirror list")
	}
	return output.NewError("MIRROR_REF_REQUIRED", "multiple mirrors are configured and none uniquely match the current directory", "Pass a mirror id from workyard mirror list")
}

func prepareMirrorServiceRun(ctx context.Context, opts *options, profile mirror.Profile, prep mirrorPrepareOptions) (config.Loaded, mirror.Profile, mirror.DestinationCheck, remote.Paths, error) {
	resolved, err := resolveMirrorProfile(opts.stateDir, profile)
	if err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
	}
	loaded, err := config.Load(resolved.LocalRoot)
	if err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, output.NewError("CONFIG_LOAD_FAILED", err.Error(), "Mirror service commands require a workyard.yaml in the mirrored directory")
	}
	check, err := prepareMirrorForRemoteUse(ctx, resolved, prep)
	if err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, err
	}
	home, err := remote.Home(ctx, resolved.Worker)
	if err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, err
	}
	paths, err := remote.BuildPaths(home, opts.remoteRoot, loaded.Config.Name, resolved.ID)
	if err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, err
	}
	if err := ensureMirrorServiceRun(ctx, resolved, paths, check.ResolvedPath); err != nil {
		return config.Loaded{}, mirror.Profile{}, mirror.DestinationCheck{}, remote.Paths{}, err
	}
	return loaded, resolved, check, paths, nil
}

func ensureMirrorServiceRun(ctx context.Context, profile mirror.Profile, paths remote.Paths, mirrorPath string) error {
	script := strings.Join([]string{
		"set -eu",
		"run=" + remote.ShellQuote(paths.RunRoot),
		"source=" + remote.ShellQuote(paths.Source),
		"logs=" + remote.ShellQuote(paths.Logs),
		"mirror=" + remote.ShellQuote(mirrorPath),
		"if [ -L \"$run\" ]; then printf 'refusing symlink run root\\n' >&2; exit 1; fi",
		"if [ -e \"$run\" ] && [ ! -d \"$run\" ]; then printf 'mirror service run root is not a directory\\n' >&2; exit 1; fi",
		"if [ -L \"$mirror\" ] || [ ! -d \"$mirror\" ]; then printf 'mirror destination is not a real directory\\n' >&2; exit 1; fi",
		"mkdir -p \"$run\" \"$logs\"",
		"chmod go-rwx \"$run\" \"$logs\"",
		"if [ -L \"$source\" ]; then target=$(readlink \"$source\"); if [ \"$target\" != \"$mirror\" ]; then printf 'refusing source symlink pointing at %s\\n' \"$target\" >&2; exit 1; fi",
		"elif [ -e \"$source\" ]; then printf 'refusing non-symlink source path\\n' >&2; exit 1",
		"else ln -s \"$mirror\" \"$source\"; fi",
	}, "\n")
	_, err := remote.Run(ctx, profile.Worker, []string{"sh", "-lc", script}, nil, 20*time.Second)
	return err
}

func cleanupMirrorServiceRun(ctx context.Context, profile mirror.Profile, paths remote.Paths, mirrorPath string) error {
	script := strings.Join([]string{
		"set -eu",
		"run=" + remote.ShellQuote(paths.RunRoot),
		"source=" + remote.ShellQuote(paths.Source),
		"mirror=" + remote.ShellQuote(mirrorPath),
		"if [ -L \"$run\" ]; then printf 'refusing symlink run root\\n' >&2; exit 1; fi",
		"if [ -e \"$source\" ]; then if [ ! -L \"$source\" ]; then printf 'refusing non-symlink source path\\n' >&2; exit 1; fi; target=$(readlink \"$source\"); if [ \"$target\" != \"$mirror\" ]; then printf 'refusing source symlink pointing at %s\\n' \"$target\" >&2; exit 1; fi; fi",
		"rm -rf -- \"$run\"",
	}, "\n")
	_, err := remote.Run(ctx, profile.Worker, []string{"sh", "-lc", script}, nil, 30*time.Second)
	return err
}

func rememberMirrorServiceRun(w io.Writer, opts *options, loaded config.Loaded, profile mirror.Profile, paths remote.Paths, mirrorPath string) {
	rememberRun(w, opts, loaded, registry.RunRef{
		Worker:           profile.Worker,
		Project:          paths.Project,
		RunID:            paths.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    paths.RunRoot,
		RemoteSourcePath: mirrorPath,
		RemoteBinary:     opts.remoteBinary,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
}

func printMirrorServiceStep(w io.Writer, opts *options, name, message string) {
	if opts.quiet || opts.json {
		return
	}
	if strings.TrimSpace(message) == "" {
		output.OKf(w, "%s", name)
		return
	}
	output.OKf(w, "%s - %s", name, message)
}

func mirrorTmuxCommand(opts *options) *cobra.Command {
	root := &cobra.Command{
		Use:   "tmux",
		Short: "List or kill mirror tmux sessions",
	}
	list := &cobra.Command{
		Use:   "list [name-or-id]",
		Short: "List default tmux sessions for configured mirrors",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := loadMirrorProfilesForOptionalRef(opts.stateDir, args)
			if err != nil {
				return err
			}
			rows := make([]mirrorTmuxSessionStatus, 0, len(profiles))
			for _, profile := range profiles {
				resolved, err := resolveMirrorProfile(opts.stateDir, profile)
				if err != nil {
					rows = append(rows, mirrorTmuxSessionStatus{
						ID:      profile.ID,
						Name:    profile.Name,
						Worker:  profile.Worker,
						Session: mustDefaultMirrorTmuxSessionName(profile),
						Status:  "error",
						Message: err.Error(),
					})
					continue
				}
				rows = append(rows, inspectMirrorTmuxSession(cmd.Context(), resolved, profile, ""))
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "sessions": rows})
			}
			tableRows := make([][]string, 0, len(rows))
			for _, row := range rows {
				tableRows = append(tableRows, []string{row.ID, row.Name, row.Worker, row.Session, row.Status, fmt.Sprint(row.Attached), fmt.Sprint(row.Windows), firstNonEmpty(row.Path, row.Message)})
			}
			return output.WriteTable(cmd.OutOrStdout(), []string{"ID", "NAME", "WORKER", "SESSION", "STATUS", "ATTACHED", "WINDOWS", "PATH/MESSAGE"}, tableRows)
		},
	}
	var sessionOverride string
	kill := &cobra.Command{
		Use:   "kill <name-or-id>",
		Short: "Kill a mirror tmux session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profile, ok, err := store.Get(args[0])
			if err != nil {
				return mirrorRefCommandError(err)
			}
			if !ok {
				return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
			}
			resolved, err := resolveMirrorProfile(opts.stateDir, profile)
			if err != nil {
				return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
			}
			sessionName, err := mirrorShellSessionName(profile, sessionOverride)
			if err != nil {
				return output.NewError("MIRROR_TMUX_SESSION_INVALID", err.Error(), "")
			}
			status, err := killMirrorTmuxSession(cmd.Context(), resolved.Worker, sessionName)
			if err != nil {
				return output.NewError("MIRROR_TMUX_KILL_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": status.Status == "killed" || status.Status == "missing", "session": status})
			}
			switch status.Status {
			case "killed":
				output.Successf(cmd.OutOrStdout(), "killed tmux session %s on %s", sessionName, resolved.Worker)
			case "missing":
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "tmux session %s was %s on %s\n", sessionName, output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "not running"), resolved.Worker)
			default:
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "tmux session %s status %s on %s: %s\n", sessionName, status.Status, resolved.Worker, status.Message)
			}
			return nil
		},
	}
	kill.Flags().StringVar(&sessionOverride, "session", "", "custom tmux session name to kill (defaults to workyard-<mirror-id>)")
	root.AddCommand(list, kill)
	return root
}

func mirrorSetEnabled(cmd *cobra.Command, opts *options, ref string, enabled bool) error {
	store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
	profile, ok, err := store.SetEnabled(ref, enabled)
	if err != nil {
		return mirrorRefCommandError(err)
	}
	if !ok {
		return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", ref), "Run workyard mirror list")
	}
	if opts.json {
		return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "mirror": profile})
	}
	action := "resumed"
	if !enabled {
		action = "paused"
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s mirror %s (%s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, action), profile.Name, profile.ID)
	return nil
}

func mirrorDeleteCommand(opts *options) *cobra.Command {
	var deleteRemote bool
	var keepRemote bool
	var yes bool
	cmd := &cobra.Command{
		Use:     "delete <name-or-id>",
		Aliases: []string{"remove"},
		Short:   "Delete a mirror registry record",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if deleteRemote && keepRemote {
				return output.NewError("MIRROR_DELETE_INVALID", "--delete-remote and --keep-remote cannot both be set", "")
			}
			store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
			profile, ok, err := store.Get(args[0])
			if err != nil {
				return mirrorRefCommandError(err)
			}
			if !ok {
				return output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
			}
			var deletedPath string
			if deleteRemote {
				if !yes {
					reader := bufio.NewReader(cmd.InOrStdin())
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "This will delete remote files at %s:%s\n", profile.Worker, profile.RemotePath)
					if got := promptLine(cmd.OutOrStdout(), reader, "Type mirror id to confirm remote deletion", ""); got != profile.ID {
						return output.NewError("MIRROR_DELETE_CANCELLED", "mirror delete cancelled", "")
					}
				}
				resolved, err := resolveMirrorProfile(opts.stateDir, profile)
				if err != nil {
					return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
				}
				deletedPath, err = mirror.DeleteRemote(cmd.Context(), resolved)
				if err != nil {
					return output.NewError("MIRROR_REMOTE_DELETE_FAILED", err.Error(), "Remote deletion requires a matching Workyard mirror marker")
				}
			}
			removed, ok, err := store.Delete(profile.ID)
			if err != nil {
				return output.NewError("MIRROR_REGISTRY_DELETE_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removed": ok, "mirror": removed, "remoteDeleted": deleteRemote, "remoteDeletedPath": deletedPath})
			}
			output.Successf(cmd.OutOrStdout(), "removed mirror %s (%s)", profile.Name, profile.ID)
			if deleteRemote {
				output.Successf(cmd.OutOrStdout(), "deleted remote files at %s:%s", profile.Worker, deletedPath)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "remote files left untouched at %s:%s\n", profile.Worker, profile.RemotePath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deleteRemote, "delete-remote", false, "delete remote mirror files after verifying the Workyard marker")
	cmd.Flags().BoolVar(&keepRemote, "keep-remote", false, "keep remote files (default)")
	cmd.Flags().BoolVar(&yes, "yes", false, "accept delete confirmation prompts")
	return cmd
}

func printMirrorSyncResult(w io.Writer, res mirror.SyncResult, verbose bool) {
	_, _ = fmt.Fprintf(w, "%s %s to %s:%s\n", output.StatusWord(w, output.RoleSuccess, "synced"), res.Name, res.Worker, res.ResolvedPath)
	if !verbose {
		return
	}
	_, _ = fmt.Fprintf(w, "  changed: %d item(s), %s\n", len(res.Changed), humanBytes(res.BytesTransferred))
	for _, change := range res.Changed {
		_, _ = fmt.Fprintf(w, "  %s %s\n", change.Code, change.Path)
	}
}

func mirrorEnabledCount(profiles []mirror.Profile) int {
	enabled := 0
	for _, profile := range profiles {
		if profile.Enabled {
			enabled++
		}
	}
	return enabled
}

func mirrorNoEnabledConfiguredError(profiles []mirror.Profile) error {
	if len(profiles) == 0 {
		return output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
	}
	return output.NewError("MIRROR_NONE_CONFIGURED", "no enabled mirrors are configured", "Run workyard mirror list, then workyard mirror resume <id>")
}

func runMirrorForeground(cmd *cobra.Command, opts *options, once bool, pollInterval time.Duration) error {
	store := mirror.NewStore(mirror.DefaultPath(opts.stateDir))
	profiles, err := store.List()
	if err != nil {
		return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
	}
	profiles, err = resolveMirrorProfiles(opts.stateDir, profiles)
	if err != nil {
		return output.NewError("MIRROR_WORKER_INVALID", err.Error(), "Run workyard workers list")
	}
	enabled := 0
	for _, profile := range profiles {
		if profile.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return mirrorNoEnabledConfiguredError(profiles)
	}
	if !opts.quiet {
		output.Infof(cmd.ErrOrStderr(), "mirroring %d profile(s)", enabled)
	}
	if err := runMirror(cmd, opts, profiles, mirror.RunOptions{
		StateDir:     opts.stateDir,
		Version:      Version,
		Once:         once,
		PollInterval: pollInterval,
	}); err != nil {
		return output.NewError("MIRROR_FAILED", err.Error(), "Run workyard mirror status")
	}
	return nil
}

func runMirrorOnce(cmd *cobra.Command, opts *options, profiles []mirror.Profile) error {
	if err := runMirror(cmd, opts, profiles, mirror.RunOptions{
		StateDir: opts.stateDir,
		Version:  Version,
		Once:     true,
	}); err != nil {
		return output.NewError("MIRROR_FAILED", err.Error(), "Run workyard mirror status")
	}
	return nil
}

func runMirror(cmd *cobra.Command, opts *options, profiles []mirror.Profile, runOpts mirror.RunOptions) error {
	runOpts.OnResult = func(res mirror.SyncResult) {
		if opts.json {
			_ = output.WriteJSONLine(cmd.OutOrStdout(), res)
			return
		}
		if !opts.quiet {
			printMirrorSyncResult(cmd.OutOrStdout(), res, opts.verbose)
		}
	}
	runOpts.OnError = func(profile mirror.Profile, err error) {
		if opts.json {
			_ = output.WriteJSONLine(cmd.OutOrStdout(), map[string]any{"ok": false, "name": profile.Name, "worker": profile.Worker, "error": err.Error()})
			return
		}
		_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "mirror %s %s %s\n", profile.Name, output.Styled(cmd.ErrOrStderr(), output.RoleError, "error:"), err)
	}
	return mirror.Run(cmd.Context(), profiles, runOpts)
}

type mirrorPrepareMode int

const (
	mirrorPrepareRequireReady mirrorPrepareMode = iota
	mirrorPrepareSyncAuto
	mirrorPrepareSyncAlways
)

type mirrorPrepareOptions struct {
	Mode     mirrorPrepareMode
	Version  string
	Verbose  bool
	Quiet    bool
	Progress io.Writer
}

func prepareMirrorForRemoteUse(ctx context.Context, profile mirror.Profile, opts mirrorPrepareOptions) (mirror.DestinationCheck, error) {
	if opts.Mode == mirrorPrepareSyncAlways {
		if err := syncMirrorForRemoteUse(ctx, profile, opts); err != nil {
			return mirror.DestinationCheck{}, err
		}
		return mirror.ReadyDestination(ctx, profile)
	}
	if opts.Mode == mirrorPrepareSyncAuto {
		check, err := mirror.CheckDestination(ctx, profile)
		if err != nil {
			return mirror.DestinationCheck{}, err
		}
		switch check.State {
		case "missing", "empty":
			if err := syncMirrorForRemoteUse(ctx, profile, opts); err != nil {
				return mirror.DestinationCheck{}, err
			}
			return mirror.ReadyDestination(ctx, profile)
		case "marker-only":
			if check.OK && check.Marker != nil {
				return check, nil
			}
			return mirror.ReadyDestination(ctx, profile)
		case "non-empty":
			marker, err := mirror.ReadMarker(ctx, profile.Worker, check.ResolvedPath)
			if err != nil {
				return check, fmt.Errorf("destination is non-empty and has no readable mirror marker: %w", err)
			}
			check.Marker = &marker
			check.OK = mirror.MarkerMatches(marker, profile)
			if check.OK {
				return check, nil
			}
			return check, fmt.Errorf("destination marker belongs to mirror %q from %s", marker.Name, marker.LocalRoot)
		default:
			return mirror.ReadyDestination(ctx, profile)
		}
	}
	return mirror.ReadyDestination(ctx, profile)
}

func syncMirrorForRemoteUse(ctx context.Context, profile mirror.Profile, opts mirrorPrepareOptions) error {
	res, err := mirror.Sync(ctx, profile, mirror.SyncOptions{Version: opts.Version})
	if err != nil {
		return err
	}
	if !opts.Quiet && opts.Progress != nil {
		printMirrorSyncResult(opts.Progress, res, opts.Verbose)
	}
	return nil
}

func parseMirrorExecArgs(cmd *cobra.Command, args []string) (string, []string, error) {
	dashAt := cmd.ArgsLenAtDash()
	if dashAt < 0 {
		return "", nil, output.NewError("MIRROR_EXEC_ARGS_INVALID", "mirror exec requires -- before the command", "Use workyard mirror exec [id] -- <command> [args...]")
	}
	if dashAt > 1 {
		return "", nil, output.NewError("MIRROR_EXEC_ARGS_INVALID", "mirror exec accepts at most one mirror id before --", "Use workyard mirror exec [id] -- <command> [args...]")
	}
	ref := ""
	if dashAt == 1 {
		ref = args[0]
	}
	commandArgs := args[dashAt:]
	if len(commandArgs) == 0 {
		return "", nil, output.NewError("MIRROR_EXEC_ARGS_INVALID", "mirror exec requires a command after --", "Use workyard mirror exec [id] -- <command> [args...]")
	}
	return ref, commandArgs, nil
}

func mirrorCommandFromArgs(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		parts[i] = remote.ShellQuote(arg)
	}
	return strings.Join(parts, " ")
}

type mirrorTmuxSessionStatus struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Worker   string `json:"worker"`
	Session  string `json:"session"`
	Status   string `json:"status"`
	Attached bool   `json:"attached"`
	Windows  int    `json:"windows"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message,omitempty"`
}

func loadMirrorProfilesForOptionalRef(stateDir string, args []string) ([]mirror.Profile, error) {
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profiles, err := store.List()
	if err != nil {
		return nil, output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
	}
	if len(profiles) == 0 {
		return nil, output.NewError("MIRROR_NONE_CONFIGURED", "no mirrors are configured", "Run workyard mirror setup")
	}
	if len(args) == 0 {
		return profiles, nil
	}
	profile, ok, err := mirror.Resolve(profiles, args[0])
	if err != nil {
		return nil, mirrorRefCommandError(err)
	}
	if !ok {
		return nil, output.NewError("MIRROR_NOT_FOUND", fmt.Sprintf("mirror %q was not found", args[0]), "Run workyard mirror list")
	}
	return []mirror.Profile{profile}, nil
}

func inspectMirrorTmuxSession(ctx context.Context, resolved mirror.Profile, stored mirror.Profile, override string) mirrorTmuxSessionStatus {
	sessionName, err := mirrorShellSessionName(stored, override)
	if err != nil {
		return mirrorTmuxSessionStatus{ID: stored.ID, Name: stored.Name, Worker: resolved.Worker, Status: "error", Message: err.Error()}
	}
	status := mirrorTmuxSessionStatus{
		ID:      stored.ID,
		Name:    stored.Name,
		Worker:  resolved.Worker,
		Session: sessionName,
		Status:  "missing",
	}
	out, err := remote.Run(ctx, resolved.Worker, []string{"sh", "-lc", mirrorTmuxInspectScript(sessionName)}, nil, 8*time.Second)
	if err != nil {
		status.Status = "error"
		status.Message = err.Error()
		return status
	}
	text := strings.TrimSpace(out.Stdout)
	switch text {
	case "tmux-missing":
		status.Status = "tmux-missing"
		status.Message = "tmux is not installed on the worker"
		return status
	case "missing", "":
		status.Status = "missing"
		return status
	}
	parts := strings.SplitN(text, "\t", 3)
	if len(parts) < 3 {
		status.Status = "error"
		status.Message = "unexpected tmux output: " + text
		return status
	}
	status.Status = "running"
	status.Attached = parts[0] != "0"
	status.Windows, _ = strconv.Atoi(parts[1])
	status.Path = parts[2]
	return status
}

func killMirrorTmuxSession(ctx context.Context, worker, sessionName string) (mirrorTmuxSessionStatus, error) {
	status := mirrorTmuxSessionStatus{Worker: worker, Session: sessionName}
	out, err := remote.Run(ctx, worker, []string{"sh", "-lc", mirrorTmuxKillScript(sessionName)}, nil, 8*time.Second)
	if err != nil {
		return status, err
	}
	switch strings.TrimSpace(out.Stdout) {
	case "killed":
		status.Status = "killed"
	case "missing":
		status.Status = "missing"
	case "tmux-missing":
		status.Status = "tmux-missing"
		status.Message = "tmux is not installed on the worker"
	default:
		status.Status = "error"
		status.Message = strings.TrimSpace(out.Stdout)
	}
	return status, nil
}

func mirrorTmuxInspectScript(sessionName string) string {
	target := "=" + sessionName
	return strings.Join([]string{
		"set -eu",
		"if ! command -v tmux >/dev/null 2>&1; then printf 'tmux-missing\\n'; exit 0; fi",
		"session=" + remote.ShellQuote(sessionName),
		"target=" + remote.ShellQuote(target),
		"if ! tmux has-session -t \"$target\" 2>/dev/null; then printf 'missing\\n'; exit 0; fi",
		"summary=$(tmux list-sessions -F '#{session_name}\t#{session_attached}\t#{session_windows}' | awk -v session=\"$session\" 'BEGIN { FS=\"\\t\"; OFS=\"\\t\" } $1 == session { print $2, $3; found=1; exit } END { if (!found) exit 1 }')",
		"path=$(tmux list-panes -t \"$target\" -F '#{pane_current_path}' | head -n 1)",
		"printf '%s\\t%s\\n' \"$summary\" \"$path\"",
	}, "\n")
}

func mirrorTmuxKillScript(sessionName string) string {
	target := "=" + sessionName
	return strings.Join([]string{
		"set -eu",
		"if ! command -v tmux >/dev/null 2>&1; then printf 'tmux-missing\\n'; exit 0; fi",
		"target=" + remote.ShellQuote(target),
		"if ! tmux has-session -t \"$target\" 2>/dev/null; then printf 'missing\\n'; exit 0; fi",
		"tmux kill-session -t \"$target\"",
		"printf 'killed\\n'",
	}, "\n")
}

func mustDefaultMirrorTmuxSessionName(profile mirror.Profile) string {
	sessionName, err := mirrorShellSessionName(profile, "")
	if err != nil {
		return "workyard-" + profile.ID
	}
	return sessionName
}

func selectMirrorProfileForShell(profiles []mirror.Profile, ref string) (mirror.Profile, bool, error) {
	if strings.TrimSpace(ref) != "" {
		return mirror.Resolve(profiles, ref)
	}
	if len(profiles) == 0 {
		return mirror.Profile{}, false, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return mirror.Profile{}, false, err
	}
	matches := mirrorProfilesContainingPath(profiles, cwd)
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	if len(matches) > 1 {
		return mirror.Profile{}, false, ambiguousMirrorProfiles("current directory", matches)
	}
	if len(profiles) == 1 {
		return profiles[0], true, nil
	}
	return mirror.Profile{}, false, nil
}

func mirrorProfilesContainingPath(profiles []mirror.Profile, cwd string) []mirror.Profile {
	cwd = mirrorComparablePath(cwd)
	var matches []mirror.Profile
	for _, profile := range profiles {
		if mirrorLocalRootContains(profile.LocalRoot, cwd) {
			matches = append(matches, profile)
		}
	}
	if len(matches) < 2 {
		return matches
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return len(filepath.Clean(matches[i].LocalRoot)) > len(filepath.Clean(matches[j].LocalRoot))
	})
	longest := len(filepath.Clean(matches[0].LocalRoot))
	var mostSpecific []mirror.Profile
	for _, profile := range matches {
		if len(filepath.Clean(profile.LocalRoot)) != longest {
			break
		}
		mostSpecific = append(mostSpecific, profile)
	}
	return mostSpecific
}

func mirrorLocalRootContains(localRoot, cwd string) bool {
	root := mirrorComparablePath(localRoot)
	rel, err := filepath.Rel(root, cwd)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func mirrorComparablePath(value string) string {
	abs, err := filepath.Abs(value)
	if err != nil {
		abs = filepath.Clean(value)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

func ambiguousMirrorProfiles(ref string, profiles []mirror.Profile) mirror.AmbiguousRefError {
	ids := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		ids = append(ids, profile.ID)
	}
	sort.Strings(ids)
	return mirror.AmbiguousRefError{Ref: ref, IDs: ids}
}

func runMirrorShellRemoteCommand(ctx context.Context, worker, resolvedPath, userCommand string, stdin io.Reader, stdout, stderr io.Writer) error {
	script := "cd " + remote.ShellQuote(resolvedPath) + " && " + userCommand
	return runMirrorSSH(ctx, worker, mirrorRemoteShellCommand(script), stdin, stdout, stderr, false, true)
}

func runMirrorInteractiveSSH(ctx context.Context, worker, remoteCommand string, stdin io.Reader, stdout, stderr io.Writer) error {
	return runMirrorSSH(ctx, worker, remoteCommand, stdin, stdout, stderr, true, false)
}

func runMirrorSSH(ctx context.Context, worker, remoteCommand string, stdin io.Reader, stdout, stderr io.Writer, tty bool, batch bool) error {
	if err := remote.ValidateWorker(worker); err != nil {
		return err
	}
	args := []string{}
	if batch {
		args = append(args, "-o", "BatchMode=yes")
	}
	if tty {
		args = append(args, "-t")
	}
	args = append(args, "--", worker, remoteCommand)
	ssh := exec.CommandContext(ctx, "ssh", args...)
	ssh.Stdin = stdin
	ssh.Stdout = stdout
	ssh.Stderr = stderr
	if err := ssh.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return printedError{err: fmt.Errorf("ssh %s exited with status %d", worker, exit.ExitCode()), exitCode: exit.ExitCode()}
		}
		return err
	}
	return nil
}

func requireRemoteCommand(ctx context.Context, worker, name string) error {
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", "command -v " + remote.ShellQuote(name) + " >/dev/null"}, nil, 8*time.Second)
	return err
}

func mirrorRemoteShellCommand(script string) string {
	return "sh -lc " + remote.ShellQuote(script)
}

func mirrorPlainShellScript(resolvedPath string) string {
	return "cd " + remote.ShellQuote(resolvedPath) + " && exec \"${SHELL:-sh}\" -l"
}

func mirrorTmuxShellScript(resolvedPath, sessionName string) string {
	return strings.Join([]string{
		"set -eu",
		"case \"${TERM:-}\" in ''|dumb|unknown) export TERM=xterm-256color;; esac",
		"cd " + remote.ShellQuote(resolvedPath),
		"session=" + remote.ShellQuote(sessionName),
		"target=" + remote.ShellQuote("="+sessionName),
		"if tmux has-session -t \"$target\" 2>/dev/null; then exec tmux attach-session -t \"$target\"; fi",
		"exec tmux new-session -s \"$session\"",
	}, "\n")
}

func mirrorShellSessionName(profile mirror.Profile, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		name := strings.TrimSpace(override)
		if err := validateMirrorShellSessionName(name); err != nil {
			return "", err
		}
		return name, nil
	}
	seed := profile.ID
	if seed == "" {
		seed = profile.Name
	}
	name := "workyard-" + sanitizeMirrorShellSessionPart(seed)
	if name == "workyard-" {
		name = "workyard-session"
	}
	return name, nil
}

func validateMirrorShellSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("tmux session name is required")
	}
	if len(name) > 80 {
		return fmt.Errorf("tmux session name is too long")
	}
	if strings.ContainsAny(name, ":\r\n\t /\\") {
		return fmt.Errorf("tmux session name may only contain letters, numbers, dots, dashes, and underscores")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("tmux session name may only contain letters, numbers, dots, dashes, and underscores")
	}
	return nil
}

func sanitizeMirrorShellSessionPart(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func mirrorRefCommandError(err error) error {
	var ambiguous mirror.AmbiguousRefError
	if errors.As(err, &ambiguous) {
		return output.NewError("MIRROR_AMBIGUOUS", ambiguous.Error(), "Use one of: "+strings.Join(ambiguous.IDs, ", "))
	}
	return output.NewError("MIRROR_REGISTRY_READ_FAILED", err.Error(), "")
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	size := float64(value)
	for _, unit := range units {
		size = size / 1024
		if size < 1024 {
			return fmt.Sprintf("%.1f %s", size, unit)
		}
	}
	return fmt.Sprintf("%.1f PB", size/1024)
}

func checkedMirrorDestination(cmd *cobra.Command, opts *options, reader *bufio.Reader, profile, resolved *mirror.Profile, fixedRemotePath, force bool) (mirror.DestinationCheck, error) {
	for {
		check, err := mirror.CheckDestination(cmd.Context(), *resolved)
		if err != nil {
			return mirror.DestinationCheck{}, output.NewError("MIRROR_DESTINATION_CHECK_FAILED", err.Error(), "Check Tailscale/SSH connectivity and the remote path")
		}
		if check.OK {
			return check, nil
		}
		if force && check.State == "non-empty" {
			return check, nil
		}
		if fixedRemotePath {
			reason := firstNonEmpty(check.NonEmptyReason, check.State)
			return mirror.DestinationCheck{}, output.NewError("MIRROR_DESTINATION_NOT_READY", fmt.Sprintf("%s:%s is not ready: %s", profile.Worker, profile.RemotePath, reason), "Choose a different --remote-path; --force only allows non-empty directories")
		}
		output.Warningf(cmd.OutOrStdout(), "destination is not ready: %s", firstNonEmpty(check.NonEmptyReason, check.State))
		next := promptLine(cmd.OutOrStdout(), reader, "Remote destination", mirror.DefaultRemotePath(profile.LocalRoot))
		profile.RemotePath = next
		resolved.RemotePath = next
		normalized, normErr := mirror.Normalize(*profile)
		if normErr != nil {
			return mirror.DestinationCheck{}, output.NewError("MIRROR_CONFIG_INVALID", normErr.Error(), "")
		}
		*profile = normalized
		nextResolved, resolveErr := resolveMirrorProfile(opts.stateDir, *profile)
		if resolveErr != nil {
			return mirror.DestinationCheck{}, output.NewError("MIRROR_WORKER_INVALID", resolveErr.Error(), "Run workyard workers list")
		}
		*resolved = nextResolved
	}
}

func printMirrorSetupSummary(w io.Writer, profile mirror.Profile, sshTarget string, check mirror.DestinationCheck) {
	_, _ = fmt.Fprintln(w, "Confirm mirror:")
	_, _ = fmt.Fprintf(w, "  name:       %s\n", profile.Name)
	_, _ = fmt.Fprintf(w, "  local:      %s\n", profile.LocalRoot)
	_, _ = fmt.Fprintf(w, "  worker:     %s (%s)\n", profile.Worker, sshTarget)
	_, _ = fmt.Fprintf(w, "  remote:     %s\n", profile.RemotePath)
	_, _ = fmt.Fprintf(w, "  resolved:   %s\n", check.ResolvedPath)
	_, _ = fmt.Fprintf(w, "  presets:    %s\n", firstNonEmpty(strings.Join(profile.Presets, ","), "none"))
	_, _ = fmt.Fprintf(w, "  includeGit: %t\n", profile.IncludeGit)
	_, _ = fmt.Fprintf(w, "  delete:     %t\n", profile.Delete)
	if check.State == "non-empty" {
		_, _ = fmt.Fprintf(w, "  %s    destination is not empty\n", output.Styled(w, output.RoleWarning, "warning:"))
	}
}

func printMirrorDoctorReport(w io.Writer, report mirror.DoctorReport) {
	_, _ = fmt.Fprintln(w, "workyard mirror doctor")
	rows := make([][]string, 0)
	for _, profile := range report.Profiles {
		for _, check := range profile.Checks {
			rows = append(rows, []string{profile.ID, profile.Name, check.Name, strings.ToUpper(check.Status), check.Message})
		}
	}
	_ = output.WriteTable(w, []string{"ID", "NAME", "CHECK", "STATUS", "MESSAGE"}, rows)
	for _, profile := range report.Profiles {
		for _, check := range profile.Checks {
			if check.Detail != "" && check.Status != mirror.DoctorPass {
				_, _ = fmt.Fprintf(w, "  %s %s detail: %s\n", profile.ID, check.Name, check.Detail)
			}
		}
	}
	if report.OK {
		_, _ = fmt.Fprintln(w)
		output.OKf(w, "mirror checks passed")
		return
	}
	_, _ = fmt.Fprintln(w)
	output.Failedf(w, "one or more mirror checks failed")
}

type mirrorFixReport struct {
	OK      bool              `json:"ok"`
	Actions []mirrorFixAction `json:"actions"`
}

type mirrorFixAction struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Target  string `json:"target"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

func fixMirrorDoctor(ctx context.Context, stateDir string, profiles []mirror.Profile) mirrorFixReport {
	report := mirrorFixReport{OK: true}
	add := func(action mirrorFixAction) {
		report.Actions = append(report.Actions, action)
		if action.Status == "failed" {
			report.OK = false
		}
	}
	if action, ok := fixStaleMirrorPID(mirror.DefaultPIDPath(stateDir)); ok {
		add(action)
	}
	for _, profile := range profiles {
		check, err := mirror.CheckDestination(ctx, profile)
		if err != nil {
			add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: profile.Worker + ":" + profile.RemotePath, Action: "destination", Status: "failed", Message: err.Error()})
			continue
		}
		target := check.Worker + ":" + check.ResolvedPath
		switch check.State {
		case "missing":
			if resolved, err := mirror.EnsureDestination(ctx, profile); err != nil {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "create-destination", Status: "failed", Message: err.Error()})
			} else {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: check.Worker + ":" + resolved, Action: "create-destination", Status: "fixed", Message: "created destination directory"})
			}
		case "empty":
			if resolved, err := mirror.EnsureDestination(ctx, profile); err != nil {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "secure-destination", Status: "failed", Message: err.Error()})
			} else {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: check.Worker + ":" + resolved, Action: "secure-destination", Status: "fixed", Message: "ensured destination permissions"})
			}
		case "marker-only":
			if !check.OK {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "secure-destination", Status: "skipped", Message: "marker belongs to a different mirror"})
				continue
			}
			if resolved, err := mirror.EnsureDestination(ctx, profile); err != nil {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "secure-destination", Status: "failed", Message: err.Error()})
			} else {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: check.Worker + ":" + resolved, Action: "secure-destination", Status: "fixed", Message: "ensured destination permissions"})
			}
		case "non-empty":
			marker, err := mirror.ReadMarker(ctx, profile.Worker, check.ResolvedPath)
			if err != nil || !mirror.MarkerMatches(marker, profile) {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "secure-destination", Status: "skipped", Message: "non-empty destination has no matching marker"})
				continue
			}
			if resolved, err := mirror.EnsureDestination(ctx, profile); err != nil {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "secure-destination", Status: "failed", Message: err.Error()})
			} else {
				add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: check.Worker + ":" + resolved, Action: "secure-destination", Status: "fixed", Message: "ensured destination permissions"})
			}
		case "symlink", "not-directory":
			add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "destination", Status: "skipped", Message: firstNonEmpty(check.NonEmptyReason, check.State)})
		default:
			add(mirrorFixAction{ID: profile.ID, Name: profile.Name, Target: target, Action: "destination", Status: "skipped", Message: "no safe fix for destination state " + check.State})
		}
	}
	return report
}

func fixStaleMirrorPID(pidPath string) (mirrorFixAction, bool) {
	data, err := os.ReadFile(pidPath)
	if err != nil {
		return mirrorFixAction{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err == nil && pid > 0 && pidRunning(pid) {
		return mirrorFixAction{Target: pidPath, Action: "pid-file", Status: "skipped", Message: fmt.Sprintf("mirror process is running pid=%d", pid)}, true
	}
	if err := os.Remove(pidPath); err != nil {
		return mirrorFixAction{Target: pidPath, Action: "pid-file", Status: "failed", Message: err.Error()}, true
	}
	return mirrorFixAction{Target: pidPath, Action: "pid-file", Status: "fixed", Message: "removed stale mirror pid file"}, true
}

func printMirrorFixReport(w io.Writer, report mirrorFixReport) {
	_, _ = fmt.Fprintln(w, "workyard mirror doctor --fix")
	if len(report.Actions) == 0 {
		_, _ = fmt.Fprintln(w, "no safe fixes applied")
		_, _ = fmt.Fprintln(w)
		return
	}
	rows := make([][]string, 0, len(report.Actions))
	for _, action := range report.Actions {
		rows = append(rows, []string{action.ID, action.Name, action.Action, strings.ToUpper(action.Status), action.Target, action.Message})
	}
	_ = output.WriteTable(w, []string{"ID", "NAME", "ACTION", "STATUS", "TARGET", "MESSAGE"}, rows)
	_, _ = fmt.Fprintln(w)
}

func promptMirrorWorker(w io.Writer, reader *bufio.Reader, stateDir string) (string, error) {
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	workers, err := store.List()
	if err != nil {
		return "", output.NewError("WORKER_CONFIG_READ_FAILED", err.Error(), "")
	}
	if len(workers) == 0 {
		return "", output.NewError("WORKER_REQUIRED", "no registered workers are available", "Run workyard workers add <tailscale-device-or-host>")
	}
	_, _ = fmt.Fprintln(w, "Worker:")
	for i, worker := range workers {
		_, _ = fmt.Fprintf(w, "  %d. %s (%s)\n", i+1, worker.Name, worker.EffectiveSSHTarget())
	}
	def := workers[0].Name
	for {
		answer := promptLine(w, reader, "Select worker", def)
		for i, worker := range workers {
			if answer == worker.Name || answer == worker.EffectiveSSHTarget() || answer == fmt.Sprint(i+1) {
				return worker.Name, nil
			}
		}
		_, _ = fmt.Fprintf(w, "Choose a worker by number or name.\n")
	}
}

func promptLine(w io.Writer, reader *bufio.Reader, label, def string) string {
	if def != "" {
		_, _ = fmt.Fprintf(w, "%s [%s]: ", label, def)
	} else {
		_, _ = fmt.Fprintf(w, "%s: ", label)
	}
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return def
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func promptYes(w io.Writer, reader *bufio.Reader, label string, def bool) bool {
	suffix := " [y/N]"
	if def {
		suffix = " [Y/n]"
	}
	_, _ = fmt.Fprintf(w, "%s%s: ", label, suffix)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

func resolveMirrorProfiles(stateDir string, profiles []mirror.Profile) ([]mirror.Profile, error) {
	out := make([]mirror.Profile, 0, len(profiles))
	for _, profile := range profiles {
		resolved, err := resolveMirrorProfile(stateDir, profile)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func resolveMirrorProfile(stateDir string, profile mirror.Profile) (mirror.Profile, error) {
	resolved, err := resolveWorkerTarget(stateDir, profile.Worker)
	if err != nil {
		return mirror.Profile{}, err
	}
	profile.Worker = resolved
	return mirror.Normalize(profile)
}

func launchMirrorProcess(opts *options) (int, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, "", err
	}
	logPath := mirror.DefaultLogPath(opts.stateDir)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return 0, "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return 0, "", err
	}
	defer logFile.Close()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return 0, "", err
	}
	defer devNull.Close()
	argv := []string{}
	if opts.stateDir != "" {
		argv = append(argv, "--state-dir", opts.stateDir)
	}
	argv = append(argv, "--quiet", "mirror")
	child := exec.Command(exe, argv...)
	child.Stdin = devNull
	child.Stdout = logFile
	child.Stderr = logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := child.Start(); err != nil {
		return 0, "", err
	}
	pidPath := mirror.DefaultPIDPath(opts.stateDir)
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		return 0, "", err
	}
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", child.Process.Pid)), 0o600); err != nil {
		return 0, "", err
	}
	time.Sleep(1200 * time.Millisecond)
	if !pidRunning(child.Process.Pid) {
		_ = os.Remove(pidPath)
		return 0, logPath, fmt.Errorf("mirror process exited during startup")
	}
	return child.Process.Pid, logPath, nil
}

func mirrorStartFailureHint(logPath string) string {
	if logPath == "" {
		return "Run workyard mirror to see foreground errors"
	}
	if tail := tailTextFile(logPath, 12); tail != "" {
		return "Inspect " + logPath + "\nLast log lines:\n" + tail
	}
	return "Inspect " + logPath + " or run workyard mirror to see foreground errors"
}

func tailTextFile(path string, maxLines int) string {
	if maxLines <= 0 {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

func readRunningPID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, pidRunning(pid)
}

func pidRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func waitForPIDStopped(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !pidRunning(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
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
			output.Successf(cmd.OutOrStdout(), "created %s", path)
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
			output.OKf(cmd.OutOrStdout(), "%s", loaded.Config.Path)
			for _, warning := range loaded.Warnings {
				output.Warningf(cmd.OutOrStdout(), "%s", warning)
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
		Short: "Run the full deploy flow for a project",
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
	if err := requireWorker(opts, "deploy"); err != nil {
		return err
	}
	if err := validateTimeoutFlag(d.timeout); err != nil {
		return err
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
				output.OKf(cmd.ErrOrStderr(), "%s", name)
			} else {
				output.OKf(cmd.ErrOrStderr(), "%s - %s", name, message)
			}
		}
	}
	if !opts.quiet && !opts.json {
		output.Infof(cmd.ErrOrStderr(), "deploying %s to %s run=%s", loaded.Config.Name, opts.worker, run)
	}
	if registry.IsLocalWorker(opts.worker) {
		return runLocalDeploy(cmd, opts, d, loaded, services, waitServices, run, &steps, step)
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
		output.Warningf(cmd.ErrOrStderr(), "stop before start skipped: %s", err)
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

func runLocalDeploy(cmd *cobra.Command, opts *options, d deployOptions, loaded config.Loaded, services, waitServices []string, run string, steps *[]deployStep, step func(string, string)) error {
	paths, err := buildLocalPaths(opts, loaded.Config.Name, run)
	if err != nil {
		return err
	}
	if d.install {
		step("install", "using local workyard "+Version)
	}
	if !d.skipDoctor {
		report := doctor.Run(cmd.Context(), doctor.Options{
			Project:      loaded.Config.Root,
			Worker:       registry.LocalWorkerName,
			RemoteRoot:   opts.remoteRoot,
			RemoteBinary: opts.remoteBinary,
			Version:      Version,
			CheckProject: true,
			Timeout:      8 * time.Second,
			StateDir:     opts.stateDir,
			Socket:       opts.socket,
		}, doctor.SystemRunner{})
		if !report.OK {
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": false, "failedStep": "doctor", "doctor": report, "steps": *steps})
			}
			printDoctorReport(cmd.OutOrStdout(), report)
			return printedError{err: errors.New("doctor found required check failures"), exitCode: 1}
		}
		step("doctor", "required checks passed")
	}
	if d.fresh {
		removed, err := deployLocalFresh(cmd, opts, paths, run)
		if err != nil {
			return err
		}
		if removed {
			step("fresh", "removed existing run")
		} else {
			step("fresh", "no existing run")
		}
	}
	syncRes, err := syncer.RunLocal(cmd.Context(), loaded, syncer.Options{
		Worker:     registry.LocalWorkerName,
		RunID:      run,
		RemoteRoot: opts.remoteRoot,
		StateDir:   opts.stateDir,
		Delete:     true,
		Verbose:    opts.verbose,
	}, Version)
	if err != nil {
		return output.NewError("LOCAL_SYNC_FAILED", err.Error(), "Check rsync and the local Workyard run directory")
	}
	rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
		Worker:           syncRes.Worker,
		Project:          syncRes.Project,
		RunID:            syncRes.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    syncRes.RemoteRunPath,
		RemoteSourcePath: syncRes.RemoteSourcePath,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
	step("sync", syncRes.RemoteSourcePath)
	if !d.skipSetup {
		res, err := localDaemonCall(cmd.Context(), opts, paths, "setup", nil, controlExtra{})
		if err != nil {
			return output.NewError("SETUP_FAILED", err.Error(), "Run workyard events --worker localhost --json or logs setup")
		}
		step("setup", res.Message)
	}
	if !d.skipBuild {
		res, err := localDaemonCall(cmd.Context(), opts, paths, "build", nil, controlExtra{})
		if err != nil {
			return output.NewError("BUILD_FAILED", err.Error(), "Run workyard events --worker localhost --json or logs build")
		}
		step("build", res.Message)
	}
	stopExtra := controlExtra{}
	if len(services) == 0 {
		stopExtra.All = true
	}
	if res, err := localDaemonCall(cmd.Context(), opts, paths, "stop", services, stopExtra); err == nil {
		step("stop", serviceStatusSummary(res.Services))
	} else if opts.verbose && !opts.json {
		output.Warningf(cmd.ErrOrStderr(), "stop before start skipped: %s", err)
	}
	startRes, err := localDaemonCall(cmd.Context(), opts, paths, "start", services, controlExtra{Timeout: d.timeout})
	if err != nil {
		return output.NewError("START_FAILED", err.Error(), "Run workyard inspect --worker localhost --json or logs <service>")
	}
	step("start", serviceStatusSummary(startRes.Services))
	if !d.skipWait && len(waitServices) > 0 {
		res, err := localDaemonCall(cmd.Context(), opts, paths, "wait", waitServices, controlExtra{Healthy: true, Timeout: d.timeout})
		if err != nil {
			return output.NewError("WAIT_FAILED", err.Error(), "Run workyard inspect --worker localhost --json")
		}
		step("wait", res.Message)
	}
	urlsRes, err := localDaemonCall(cmd.Context(), opts, paths, "urls", services, controlExtra{})
	if err != nil {
		return output.NewError("URLS_FAILED", err.Error(), "Run workyard status --worker localhost --json")
	}
	step("urls", fmt.Sprintf("%d url(s)", len(urlsRes.URLs)))
	if opts.json {
		return output.WriteJSON(cmd.OutOrStdout(), map[string]any{
			"ok":               true,
			"worker":           registry.LocalWorkerName,
			"project":          paths.Project,
			"runId":            paths.RunID,
			"remoteRunPath":    paths.RunRoot,
			"remoteSourcePath": paths.Source,
			"services":         urlsRes.Services,
			"urls":             urlsRes.URLs,
			"steps":            *steps,
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
	binary, err := remote.EnsureArtifact(cmd.Context(), platform, d.artifactDir, d.localBinary, Version)
	if err != nil {
		return remote.InstallResult{}, output.NewError("WORKER_ARTIFACT_MISSING", err.Error(), "Build it with GOOS="+platform.OS+" GOARCH="+platform.Arch+" go build -o dist/"+platform.ArtifactName()+" ./cmd/workyard or pass --local-binary")
	}
	res, err := remote.InstallBinary(cmd.Context(), opts.worker, platform, remote.InstallOptions{
		LocalBinary:     binary,
		RemoteBinary:    opts.remoteBinary,
		ExpectedVersion: Version,
	})
	if err != nil {
		return remote.InstallResult{}, output.NewError("WORKER_INSTALL_FAILED", err.Error(), "Check SSH access to the worker and rerun with --verbose")
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
		output.Infof(cmd.ErrOrStderr(), "fresh: removing existing run %s", paths.RunRoot)
	}
	if _, err := remoteDaemonCall(cmd.Context(), opts, paths, "stop", nil, controlExtra{All: true}); err != nil {
		if !opts.quiet {
			output.Warningf(cmd.ErrOrStderr(), "stop before fresh cleanup failed: %s", err)
		}
	}
	if _, err := remote.CleanupRun(cmd.Context(), opts.worker, paths); err != nil {
		return false, output.NewError("REMOTE_RUN_CLEANUP_FAILED", err.Error(), "")
	}
	store := registry.New(registry.DefaultPath(opts.stateDir))
	_, _ = store.Remove(opts.worker, paths.Project, run)
	return true, nil
}

func deployLocalFresh(cmd *cobra.Command, opts *options, paths remote.Paths, run string) (bool, error) {
	exists, err := localRunExists(paths)
	if err != nil {
		return false, output.NewError("LOCAL_RUN_CHECK_FAILED", err.Error(), "")
	}
	if !exists {
		return false, nil
	}
	if !opts.quiet && !opts.json {
		output.Infof(cmd.ErrOrStderr(), "fresh: removing existing run %s", paths.RunRoot)
	}
	if _, err := localDaemonCall(cmd.Context(), opts, paths, "stop", nil, controlExtra{All: true}); err != nil {
		if !opts.quiet {
			output.Warningf(cmd.ErrOrStderr(), "stop before fresh cleanup failed: %s", err)
		}
	}
	if _, err := cleanupLocalRun(paths); err != nil {
		return false, output.NewError("LOCAL_RUN_CLEANUP_FAILED", err.Error(), "")
	}
	store := registry.New(registry.DefaultPath(opts.stateDir))
	_, _ = store.Remove(registry.LocalWorkerName, paths.Project, run)
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
	argv := remoteDaemonArgv(opts, paths, action, services, extra, true)
	out, err := remote.Run(ctx, opts.worker, argv, nil, remoteTimeout(action, extra.Timeout))
	if err != nil {
		return worker.Response{}, err
	}
	var res worker.Response
	if err := json.Unmarshal([]byte(out.Stdout), &res); err != nil {
		return worker.Response{}, fmt.Errorf("decode %s response: %w", action, err)
	}
	warnDaemonVersion(os.Stderr, opts, res.Version)
	if !res.OK {
		if res.Error != nil {
			return res, fmt.Errorf("%s: %s", res.Error.Code, res.Error.Message)
		}
		return res, fmt.Errorf("%s failed", action)
	}
	return res, nil
}

func remoteDaemonPassthrough(ctx context.Context, opts *options, paths remote.Paths, action string, services []string, extra controlExtra, stdout, stderr io.Writer) error {
	if err := remote.EnsureDaemon(ctx, opts.worker, paths, opts.remoteBinary); err != nil {
		return err
	}
	argv := remoteDaemonArgv(opts, paths, action, services, extra, opts.json)
	res, err := remote.Run(ctx, opts.worker, argv, nil, remoteTimeout(action, extra.Timeout))
	if res.Stdout != "" {
		_, _ = io.WriteString(stdout, res.Stdout)
	}
	if res.Stderr != "" && opts.verbose {
		_, _ = io.WriteString(stderr, res.Stderr)
	}
	if err != nil {
		if strings.TrimSpace(res.Stdout) != "" {
			return printedError{err: err, exitCode: 1}
		}
		return fmt.Errorf("%s on %s: %s", action, opts.worker, truncateForDisplay(err.Error(), 2048))
	}
	return nil
}

func remoteDaemonArgv(opts *options, paths remote.Paths, action string, services []string, extra controlExtra, jsonOut bool) []string {
	binary := paths.Binary
	if opts.remoteBinary != "" {
		binary = opts.remoteBinary
	}
	argv := []string{binary, "daemonctl", action, "--socket", paths.Socket, "--run-root", paths.RunRoot, "--project-name", paths.Project, "--run-id", paths.RunID, "--worker-name", opts.worker}
	if jsonOut {
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
	return append(argv, services...)
}

// deployProjectAndServices splits deploy's positional args into a project
// path and service names. The first arg is a project path only when it is
// written like one (./x, ../x, ~/x, absolute, contains a separator, or ends
// in .yaml/.yml); a plain word is always a service name. The ambiguous case —
// a plain word that also names a directory — is rejected instead of guessed.
func deployProjectAndServices(defaultProject string, args []string) (string, []string, error) {
	project := defaultProject
	if project == "" {
		project = "."
	}
	if len(args) == 0 {
		return project, nil, nil
	}
	first := args[0]
	if looksLikeDeployProjectPath(first) || strings.HasSuffix(first, ".yaml") || strings.HasSuffix(first, ".yml") {
		if _, err := os.Stat(first); err != nil {
			return "", nil, fmt.Errorf("project path %q does not exist", first)
		}
		return first, args[1:], nil
	}
	if stat, err := os.Stat(first); err == nil && stat.IsDir() {
		return "", nil, fmt.Errorf("%q names both a directory and a possible service; use ./%s for the path or --project", first, first)
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
		Short: "Copy project files into a worker run directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireWorker(opts, "sync"); err != nil {
				return err
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
			syncOpts := syncer.Options{
				Worker:     opts.worker,
				RunID:      run,
				RemoteRoot: opts.remoteRoot,
				StateDir:   opts.stateDir,
				DryRun:     dryRun,
				Delete:     deleteRemote,
				Verbose:    opts.verbose,
			}
			var res syncer.Result
			if registry.IsLocalWorker(opts.worker) {
				res, err = syncer.RunLocal(cmd.Context(), loaded, syncOpts, Version)
			} else {
				res, err = syncer.Run(cmd.Context(), loaded, syncOpts, Version)
			}
			if err != nil {
				return output.NewError("SYNC_FAILED", err.Error(), "Check rsync, worker connectivity, and run workyard sync with --verbose")
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
			if registry.IsLocalWorker(res.Worker) {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s to %s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "synced"), res.Project, res.RemoteSourcePath)
			} else {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s %s to %s:%s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "synced"), res.Project, res.Worker, res.RemoteSourcePath)
			}
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
			if !cmd.Flags().Changed("foreground") || !foreground {
				return cmd.Help()
			}
			if opts.json {
				_ = output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "message": "daemon starting"})
			} else if !opts.quiet {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "workyard daemon %s\n", output.StatusWord(cmd.ErrOrStderr(), output.RoleInfo, "starting"))
			}
			return worker.Serve(cmd.Context(), worker.DaemonOptions{StateDir: opts.stateDir, Socket: opts.socket, AllowRoot: allowRoot, Version: Version})
		},
	}
	cmd.Flags().BoolVar(&foreground, "foreground", true, "serve the daemon in the foreground (use workyard daemon start for the background)")
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
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon %s (%s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "stopped"), socket)
				return nil
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "socket": socket, "version": res.Version, "message": res.Message})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon %s (%s) version=%s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "running"), socket, firstNonEmpty(res.Version, "unknown"))
			warnDaemonVersion(cmd.ErrOrStderr(), opts, res.Version)
			return nil
		},
	}
	return cmd
}

var daemonVersionWarned bool

// warnDaemonVersion prints a one-time warning when the daemon that answered is
// not the same build as this CLI. Empty versions (pre-stamped daemons) are skipped.
func warnDaemonVersion(w io.Writer, opts *options, daemonVersion string) {
	if daemonVersionWarned || opts.quiet || opts.json {
		return
	}
	if daemonVersion == "" || daemonVersion == Version {
		return
	}
	daemonVersionWarned = true
	output.Warningf(w, "daemon version %s does not match CLI version %s; restart it with workyard daemon stop && workyard daemon start", daemonVersion, Version)
}

func startLocalDaemon(cmd *cobra.Command, opts *options, allowRoot bool) error {
	socket := daemonSocket(opts)
	if res, err := worker.Call(socket, worker.Request{Action: "ping"}); err == nil {
		if opts.json {
			return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "running": true, "socket": socket, "message": res.Message})
		}
		if !opts.quiet {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon already %s (%s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "running"), socket)
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
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon %s pid=%d socket=%s log=%s\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "started"), result.PID, result.Socket, result.Log)
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
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon was %s (%s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "not running"), socket)
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
		if res.Message == "daemon stopped" {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon %s (%s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleSuccess, "stopped"), socket)
		} else {
			output.Successf(cmd.OutOrStdout(), "%s (%s)", res.Message, socket)
		}
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
	var positional cobra.PositionalArgs
	switch action {
	case "status", "inspect":
		use = action
		positional = cobra.NoArgs
	case "probe":
		use = "probe <service>"
		positional = cobra.ExactArgs(1)
	}
	var all bool
	cmd := &cobra.Command{
		Use:   use,
		Short: controlCommandShort(action),
		Args:  positional,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runControl(cmd, opts, action, args, controlExtra{All: all})
		},
	}
	if action == "stop" {
		cmd.Flags().BoolVar(&all, "all", false, "stop all services (the default when no services are named)")
	}
	return cmd
}

func controlCommandShort(action string) string {
	switch action {
	case "setup":
		return "Run the configured setup command for a project run"
	case "build":
		return "Run the configured build command for a project run"
	case "start":
		return "Start services on a worker (all services when none are named)"
	case "stop":
		return "Stop services on a worker (all services when none are named)"
	case "restart":
		return "Restart services on a worker (all services when none are named)"
	case "status":
		return "Show current service status for a run"
	case "inspect":
		return "Show detailed service state, hints, and recent errors"
	case "urls":
		return "Show service preview URLs for a run"
	case "probe":
		return "Probe a service health endpoint from the worker"
	default:
		return "Manage services on a worker"
	}
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
	if err := requireWorker(opts, "logs --follow"); err != nil {
		return err
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
	relPaths, err := followLogPaths(loaded.Config, target, extra.Stream)
	if err != nil {
		return output.NewError("LOG_TARGET_INVALID", err.Error(), "")
	}
	if registry.IsLocalWorker(opts.worker) {
		return followLocalLogs(cmd, opts, loaded, run, relPaths, extra.Tail)
	}
	return followRemoteLogs(cmd, opts, loaded, run, relPaths, extra.Tail)
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
		output.Infof(cmd.ErrOrStderr(), "following %d log file(s) on %s", len(files), opts.worker)
	}
	err = remote.Stream(cmd.Context(), opts.worker, []string{"sh", "-lc", script}, nil, cmd.OutOrStdout(), cmd.ErrOrStderr())
	if cmd.Context().Err() != nil {
		return nil
	}
	return err
}

func followLocalLogs(cmd *cobra.Command, opts *options, loaded config.Loaded, run string, relPaths []string, tailLines int) error {
	paths, err := buildLocalPaths(opts, loaded.Config.Name, run)
	if err != nil {
		return err
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
		Args:  cobra.NoArgs,
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
		Use:   "wait [service...]",
		Short: "Wait for service state or health (all services when none are named)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTimeoutFlag(timeout); err != nil {
				return err
			}
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
			return openURL(cmd.OutOrStdout(), cmd.ErrOrStderr(), res.URLs[0].URL)
		},
	}
}

// openURL launches the platform browser opener; when none exists (Linux
// servers, SSH sessions) it prints the URL instead of failing.
func openURL(out, errOut io.Writer, url string) error {
	var argv []string
	switch runtime.GOOS {
	case "darwin":
		argv = []string{"open", url}
	case "linux":
		argv = []string{"xdg-open", url}
	case "windows":
		argv = []string{"rundll32", "url.dll,FileProtocolHandler", url}
	}
	if len(argv) > 0 {
		if _, err := exec.LookPath(argv[0]); err == nil {
			if err := exec.Command(argv[0], argv[1:]...).Run(); err == nil {
				return nil
			}
		}
	}
	_, _ = fmt.Fprintf(errOut, "no browser opener available; URL:\n")
	_, _ = fmt.Fprintln(out, url)
	return nil
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
	if err := requireWorker(opts, action); err != nil {
		return err
	}
	run := opts.run
	loaded, err := config.Load(opts.project)
	if err != nil {
		ref, fallbackErr := registryRunFallback(opts, action, err)
		if fallbackErr != nil {
			return fallbackErr
		}
		loaded, err = config.Load(ref.LocalRoot)
		if err != nil {
			return output.NewError("CONFIG_LOAD_FAILED", err.Error(), "The registered run's project at "+ref.LocalRoot+" no longer has a loadable workyard.yaml; see workyard runs list")
		}
		if run == "" {
			run = ref.RunID
		}
		if !opts.quiet && !opts.json {
			output.Infof(cmd.ErrOrStderr(), "using registered run %s/%s (%s)", ref.Project, ref.RunID, ref.LocalRoot)
		}
	}
	if err := validateServiceArgs(loaded.Config, action, services); err != nil {
		return err
	}
	if action == "wait" && len(services) == 0 {
		services = config.ServiceNames(loaded.Config.Services)
	}
	if run == "" {
		run = runid.Default(loaded.Config.Root)
	}
	run, err = runid.Validate(run)
	if err != nil {
		return output.NewError("RUN_ID_INVALID", err.Error(), "")
	}
	if registry.IsLocalWorker(opts.worker) {
		return localControl(cmd, opts, loaded, action, run, services, extra)
	}
	return remoteControl(cmd, opts, loaded, action, run, services, extra)
}

// registryRunFallback lets read-only commands work outside a project
// directory: when no workyard.yaml is found and --project was not set, the
// run registered for this worker is used instead. Multiple registered runs
// are ambiguous and enumerated rather than guessed between.
func registryRunFallback(opts *options, action string, loadErr error) (registry.RunRef, error) {
	const notFoundHint = "Run from a project directory, pass --project, or see workyard runs list"
	readOnly := map[string]bool{"status": true, "inspect": true, "urls": true, "logs": true, "events": true, "wait": true, "probe": true, "stop": true}
	configMissing := errors.Is(loadErr, config.ErrNotFound)
	if !readOnly[action] || !configMissing || opts.project != "." {
		hint := ""
		if configMissing {
			hint = notFoundHint
		}
		return registry.RunRef{}, output.NewError("CONFIG_LOAD_FAILED", loadErr.Error(), hint)
	}
	store := registry.New(registry.DefaultPath(opts.stateDir))
	runs, err := store.List()
	if err != nil {
		return registry.RunRef{}, output.NewError("CONFIG_LOAD_FAILED", loadErr.Error(), notFoundHint)
	}
	var candidates []registry.RunRef
	for _, ref := range runs {
		if ref.Worker != opts.worker || strings.TrimSpace(ref.LocalRoot) == "" {
			continue
		}
		if opts.run != "" && ref.RunID != opts.run {
			continue
		}
		candidates = append(candidates, ref)
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return registry.RunRef{}, output.NewError("CONFIG_LOAD_FAILED", loadErr.Error(), notFoundHint)
	default:
		choices := make([]string, 0, len(candidates))
		for _, ref := range candidates {
			choices = append(choices, fmt.Sprintf("%s/%s (--project %s)", ref.Project, ref.RunID, ref.LocalRoot))
		}
		return registry.RunRef{}, output.NewError("RUN_AMBIGUOUS", "no workyard.yaml here and multiple runs are registered for "+opts.worker, "Choose one with --project or --run: "+strings.Join(choices, "; "))
	}
}

// validateServiceArgs rejects unknown service names before any daemon or SSH
// round trip, so typos fail fast with the same error locally and remotely.
// logs additionally accepts the setup/build and per-service lifecycle targets.
func validateServiceArgs(cfg config.Config, action string, services []string) error {
	if len(services) == 0 {
		return nil
	}
	names := config.ServiceNames(cfg.Services)
	valid := map[string]bool{}
	for _, name := range names {
		valid[name] = true
	}
	if action == "logs" {
		valid["setup"] = true
		valid["build"] = true
		for _, name := range names {
			valid[name+".beforeStart"] = true
			valid[name+".onClose"] = true
		}
	}
	for _, svc := range services {
		if !valid[svc] {
			return output.NewError("SERVICE_UNKNOWN", fmt.Sprintf("unknown service %q", svc), "Configured services: "+strings.Join(names, ", "))
		}
	}
	return nil
}

func requireWorker(opts *options, command string) error {
	if strings.TrimSpace(opts.worker) != "" {
		return nil
	}
	return output.NewError("WORKER_REQUIRED", "--worker is required for "+command, workerRequiredHint(opts.stateDir))
}

// workerRequiredHint names the actual workers the user can pass so the error
// itself answers the question. Falls back to generic phrasing when the
// registry is empty or unreadable.
func workerRequiredHint(stateDir string) string {
	names := registeredWorkerNames(stateDir)
	if len(names) == 0 {
		return "Pass --worker localhost for this machine or --worker <name> for a registered worker"
	}
	const maxListed = 6
	if len(names) > maxListed {
		names = append(names[:maxListed], "...")
	}
	return "Pass --worker localhost or one of: " + strings.Join(names, ", ")
}

// workerCompletions lists every value --worker accepts: the builtin
// localhost plus all registered worker names.
func workerCompletions(stateDir string) []string {
	return append([]string{registry.LocalWorkerName}, registeredWorkerNames(stateDir)...)
}

func registeredWorkerNames(stateDir string) []string {
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	registered, err := store.List()
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(registered))
	for _, worker := range registered {
		if strings.TrimSpace(worker.Name) != "" {
			names = append(names, worker.Name)
		}
	}
	sort.Strings(names)
	return names
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
	paths, err := buildLocalPaths(opts, loaded.Config.Name, run)
	if err != nil {
		return err
	}
	if actionNeedsLocalSource(action) {
		res, err := syncer.RunLocal(cmd.Context(), loaded, syncer.Options{
			Worker:     registry.LocalWorkerName,
			RunID:      run,
			RemoteRoot: opts.remoteRoot,
			StateDir:   opts.stateDir,
			Delete:     true,
			Verbose:    opts.verbose,
		}, Version)
		if err != nil {
			return output.NewError("LOCAL_SYNC_FAILED", err.Error(), "Check rsync and the local Workyard run directory")
		}
		rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
			Worker:           res.Worker,
			Project:          res.Project,
			RunID:            res.RunID,
			RemoteRoot:       opts.remoteRoot,
			RemoteRunPath:    res.RemoteRunPath,
			RemoteSourcePath: res.RemoteSourcePath,
			LocalRoot:        loaded.Config.Root,
			ConfigPath:       loaded.Config.Path,
		})
	}
	rememberRun(cmd.ErrOrStderr(), opts, loaded, registry.RunRef{
		Worker:           registry.LocalWorkerName,
		Project:          paths.Project,
		RunID:            paths.RunID,
		RemoteRoot:       opts.remoteRoot,
		RemoteRunPath:    paths.RunRoot,
		RemoteSourcePath: paths.Source,
		LocalRoot:        loaded.Config.Root,
		ConfigPath:       loaded.Config.Path,
	})
	res, err := localDaemonCall(cmd.Context(), opts, paths, action, services, extra)
	if err != nil && res.Error == nil {
		if action == "stop" && errors.Is(err, worker.ErrDaemonUnreachable) {
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), worker.Response{OK: true, Message: "daemon not running; nothing to stop"})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "nothing to stop (daemon %s)\n", output.StatusWord(cmd.OutOrStdout(), output.RoleWarning, "not running"))
			return nil
		}
		if output.AsCommandError(err) != nil {
			return err
		}
		if errors.Is(err, worker.ErrDaemonUnreachable) {
			return output.NewError("DAEMON_UNREACHABLE", err.Error(), "Run workyard daemon start")
		}
		return output.NewError("DAEMONCTL_FAILED", err.Error(), "")
	}
	return printDaemonResponse(cmd.OutOrStdout(), res, opts.json || action == "open", action)
}

func localDaemonCall(ctx context.Context, opts *options, paths remote.Paths, action string, services []string, extra controlExtra) (worker.Response, error) {
	socket := daemonSocket(opts)
	if action != "stop" {
		if err := ensureLocalDaemonRunning(ctx, opts); err != nil {
			return worker.Response{}, err
		}
	}
	res, err := worker.Call(socket, worker.Request{
		Action:   action,
		RunRoot:  paths.RunRoot,
		Project:  paths.Project,
		RunID:    paths.RunID,
		Worker:   registry.LocalWorkerName,
		Services: services,
		All:      extra.All,
		Tail:     extra.Tail,
		MaxBytes: extra.MaxBytes,
		Stream:   extra.Stream,
		Healthy:  extra.Healthy,
		Status:   extra.Status,
		Timeout:  extra.Timeout,
	})
	warnDaemonVersion(os.Stderr, opts, res.Version)
	return res, err
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

func guardLocalManagedPaths(paths remote.Paths) error {
	base := localStateBase(paths)
	for _, value := range []string{
		filepath.FromSlash(base),
		filepath.FromSlash(path.Join(base, "runs")),
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
		if strings.TrimSpace(res.Stdout) != "" {
			// The remote daemonctl already printed its error payload above.
			return printedError{err: err, exitCode: 1}
		}
		return output.NewError("REMOTE_COMMAND_FAILED", fmt.Sprintf("%s on %s: %s", action, opts.worker, truncateForDisplay(err.Error(), 2048)), "Check SSH connectivity to the worker or rerun with --verbose")
	}
	return nil
}

func validateTimeoutFlag(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := time.ParseDuration(value); err != nil {
		return output.NewError("TIMEOUT_INVALID", fmt.Sprintf("invalid --timeout %q", value), "Use a Go duration such as 30s, 2m, or 1h")
	}
	return nil
}

func truncateForDisplay(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + " ... (truncated)"
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
		output.Successf(w, "%s", res.Message)
	case "setup", "build":
		output.Successf(w, "%s", res.Message)
	case "inspect":
		printInspectResponse(w, res)
	default:
		rows := make([][]string, 0, len(res.Services))
		for _, svc := range res.Services {
			pid, port, url := "-", "-", "-"
			if isRunningServiceStatus(svc.Status) {
				pid = fmt.Sprint(svc.PID)
				port = fmt.Sprint(svc.AssignedPort)
				url = svc.URL
			}
			rows = append(rows, []string{svc.Name, svc.Status, fmt.Sprint(svc.Healthy), pid, port, url})
		}
		return output.WriteTable(w, []string{"SERVICE", "STATUS", "HEALTHY", "PID", "PORT", "URL"}, rows)
	}
	return nil
}

func isRunningServiceStatus(status string) bool {
	switch status {
	case "stopped", "exited":
		return false
	default:
		return true
	}
}

// printInspectResponse renders the detail the daemon already provides —
// lifecycle timestamps, ports, log paths, recent stderr, and hints — instead
// of the bare status table.
func printInspectResponse(w io.Writer, res worker.Response) {
	if len(res.Services) == 0 {
		_, _ = fmt.Fprintln(w, "no services")
		return
	}
	for i, svc := range res.Services {
		if i > 0 {
			_, _ = fmt.Fprintln(w)
		}
		health := "unhealthy"
		if svc.Healthy {
			health = "healthy"
		}
		_, _ = fmt.Fprintf(w, "%s  %s (%s)\n", svc.Name, output.ColorizeTableCell(w, "STATUS", svc.Status), output.ColorizeTableCell(w, "HEALTHY", health))
		field := func(label, value string) {
			if strings.TrimSpace(value) != "" {
				_, _ = fmt.Fprintf(w, "  %-15s %s\n", label+":", value)
			}
		}
		field("start command", svc.StartCommand)
		field("cwd", svc.Cwd)
		if svc.ConfiguredPort > 0 || svc.AssignedPort > 0 {
			port := fmt.Sprintf("%d -> %d", svc.ConfiguredPort, svc.AssignedPort)
			if svc.PortEnv != "" {
				port += " (env " + svc.PortEnv + ")"
			}
			field("port", port)
		}
		if svc.PID > 0 {
			field("pid", fmt.Sprint(svc.PID))
		}
		field("url", svc.URL)
		field("health url", svc.HealthURL)
		if !svc.StartedAt.IsZero() {
			field("started", svc.StartedAt.Format(time.RFC3339))
		}
		if !svc.StoppedAt.IsZero() {
			stopped := svc.StoppedAt.Format(time.RFC3339)
			if svc.ExitCode != nil {
				stopped += fmt.Sprintf(" (exit code %d)", *svc.ExitCode)
			}
			field("stopped", stopped)
		}
		var logPaths []string
		for _, p := range []string{svc.Logs.Stdout, svc.Logs.Stderr} {
			if p != "" {
				logPaths = append(logPaths, p)
			}
		}
		field("logs", strings.Join(logPaths, ", "))
		field("logs command", svc.LogsCommand)
		if len(svc.RecentErrors) > 0 {
			_, _ = fmt.Fprintf(w, "  %s\n", output.Styled(w, output.RoleError, "recent stderr:"))
			for _, line := range svc.RecentErrors {
				_, _ = fmt.Fprintf(w, "    %s\n", line)
			}
		}
	}
	if len(res.Hints) > 0 {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintf(w, "%s\n", output.Styled(w, output.RoleHint, "hints:"))
		for _, hint := range res.Hints {
			_, _ = fmt.Fprintf(w, "  [%s] %s: %s\n", output.ColorizeTableCell(w, "STATUS", hint.Severity), hint.Service, hint.Message)
			if hint.NextCommand != "" {
				_, _ = fmt.Fprintf(w, "    next: %s\n", hint.NextCommand)
			}
		}
	}
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
		output.Warningf(w, "failed to update local monitor registry: %s", err)
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
