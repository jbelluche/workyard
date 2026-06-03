package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "WORKER  PROJECT  RUN  UPDATED")
			for _, ref := range runs {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %s\n", ref.Worker, ref.Project, ref.RunID, ref.UpdatedAt.Format(time.RFC3339))
			}
			return nil
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
	root := &cobra.Command{Use: "workers", Short: "Manage locally registered Workyard workers"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List registered workers",
		RunE: func(cmd *cobra.Command, args []string) error {
			store := registry.New(registry.DefaultPath(opts.stateDir))
			workers, err := store.Workers()
			if err != nil {
				return output.NewError("REGISTRY_READ_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "path": store.Path(), "workers": workers})
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "WORKER  RUNS  UPDATED")
			for _, ref := range workers {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %d  %s\n", ref.Worker, ref.RunCount, ref.UpdatedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
	remove := &cobra.Command{
		Use:   "remove <worker>",
		Short: "Remove a worker and its runs from the local monitor registry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store := registry.New(registry.DefaultPath(opts.stateDir))
			count, err := store.RemoveWorker(args[0])
			if err != nil {
				return output.NewError("REGISTRY_REMOVE_FAILED", err.Error(), "")
			}
			if opts.json {
				return output.WriteJSON(cmd.OutOrStdout(), map[string]any{"ok": true, "removedCount": count})
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "removed %d run(s)\n", count)
			return nil
		},
	}
	root.AddCommand(list, remove)
	return root
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
	_, _ = fmt.Fprintln(w, "CHECK                         STATUS  MESSAGE")
	for _, check := range report.Checks {
		status := strings.ToUpper(check.Status)
		_, _ = fmt.Fprintf(w, "%-29s %-7s %s\n", check.Name, status, check.Message)
		if check.Detail != "" {
			_, _ = fmt.Fprintf(w, "  detail: %s\n", check.Detail)
		}
		if check.Hint != "" && check.Status != doctor.StatusPass {
			_, _ = fmt.Fprintf(w, "  hint: %s\n", check.Hint)
		}
	}
	if report.OK {
		_, _ = fmt.Fprintln(w, "\nok: required checks passed")
		return
	}
	_, _ = fmt.Fprintln(w, "\nfailed: one or more required checks failed")
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
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "SERVICE  PATH  START COMMAND  PORT")
			for _, name := range names {
				svc := loaded.Config.Services[name]
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s  %s  %s  %d\n", name, svc.Path, svc.StartCommand, svc.Port.Default)
			}
			return nil
		},
	}
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
		Short: "Run the private worker daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !foreground && !opts.quiet {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "daemon currently runs in the foreground; use shell/backgrounding if needed")
			}
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
	return cmd
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
		return output.NewError("DAEMONCTL_FAILED", err.Error(), "Start workyard daemon --foreground")
	}
	return printDaemonResponse(cmd.OutOrStdout(), res, opts.json || action == "open", action)
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
		_, _ = fmt.Fprintln(w, "SERVICE  STATUS  HEALTHY  PID  PORT  URL")
		for _, svc := range res.Services {
			_, _ = fmt.Fprintf(w, "%s  %s  %t  %d  %d  %s\n", svc.Name, svc.Status, svc.Healthy, svc.PID, svc.AssignedPort, svc.URL)
		}
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
	if action == "wait" && timeout != "" {
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
