# UX-Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply the fixes from UX-REVIEW.md so the workyard CLI is friendlier for users and agents, preserving the explicit-`--worker` design.

**Architecture:** All CLI-surface changes land in `internal/cli/cli.go` (the established single-file pattern); daemon-side changes in `internal/worker`; path unification in `internal/remote` + `internal/syncer`; doctor additions in `internal/doctor`. Each task is independently testable with `go test ./...` plus a manual check against `--worker localhost` and the Raspberry Pi worker `jack-rasp-five` (fixtures only, per AGENTS.md).

**Tech Stack:** Go 1.25, cobra, existing table/JSON output helpers. No new dependencies.

**Verification workers:** `localhost` (builtin) and `jack-rasp-five` (linux-arm64 Pi over Tailscale; cross-compile with `GOOS=linux GOARCH=arm64`). Use `fixtures/health-server` only. Existing remote run `workyard-health-fixture/main` on the Pi must be left intact; use run id `uxfix` for tests and `cleanup run` afterwards.

**Scope note / deferred items** (functionality projects, not CLI polish — listed in UX-REVIEW.md §3/§4/§5; each gets a disposition note in the final report): service restart policies, systemd unit installation, log rotation, background health polling, monitor SSH batching/backoff, release-artifact download (no published releases yet — mitigated by Task 14 build-fallback), interactive setup wizard (agent-unfriendly; mitigated by doctor SSH hints in Task 14), JSON envelope overhaul (breaking).

---

### Task 1: Real version via ldflags (foundation for version checks)

**Files:** Modify `internal/cli/cli.go:36`, `scripts/build-release.sh`, `scripts/local/install.sh`, `internal/worker/daemon.go` (ping response).

- [ ] Change `const Version = "0.1.0"` to `var Version = "0.1.0"` (package var, overridable via `-ldflags "-X github.com/jackbelluche/workyard/internal/cli.Version=..."`).
- [ ] Thread a `Version` into the daemon (`worker.DaemonOptions`) and return it in the `ping` response (`Response.Message` already used; add `Version string` field to `worker.Response`).
- [ ] In `localDaemonCall`/`remoteDaemonCall` paths that ping, warn on stderr when daemon version ≠ CLI version (non-fatal).
- [ ] Update build scripts to pass `-ldflags` with `git describe --tags --always --dirty` falling back to 0.1.0.
- [ ] Test: unit test that ping response includes version; manual `workyard version`, `workyard daemon status --json`.

### Task 2: `--worker` completion + enumerated `WORKER_REQUIRED` error

**Files:** Modify `internal/cli/cli.go` (`newRoot`, `requireWorker`); Test `internal/cli/workers_test.go`.

- [ ] `requireWorker`: load the worker store; when registered workers exist, hint becomes
  `Pass --worker localhost or one of: jack-r5-16gb, jack-rasp-five` (names sorted, capped ~6). Needs `opts.stateDir` → change signature to `requireWorker(opts *options, command string)` (already has opts).
- [ ] Register cobra flag completion for `--worker`: `root.RegisterFlagCompletionFunc("worker", ...)` returning `localhost` + registered names.
- [ ] Unit test: error hint contains registered names; completion func returns expected set.
- [ ] Manual: `workyard status` (no worker) shows names; `workyard __complete status --worker ""` lists them.

### Task 3: Daemon error hint passthrough + `stop` no-daemon no-op

**Files:** Modify `internal/cli/cli.go` (`localControl` ~2986, `localDaemonCall` ~2993); Test new cases in `internal/cli/`.

- [ ] In `localControl`, distinguish dial errors (socket connect failures) from daemon-returned errors. Only dial errors get hint `Run workyard daemon start`; daemon errors pass through their own code/message/hint via `output.NewError(res.Error.Code, res.Error.Message, res.Error.Hint)`.
- [ ] `worker.Call` returns `res` even on error when the daemon replied; expose an `errors.Is`-able dial sentinel (e.g. wrap with `ErrDaemonUnreachable`).
- [ ] `stop` (and `cleanup run`'s stop phase) with unreachable daemon → success no-op: print `nothing to stop (daemon not running)`, exit 0, `--json` → `{"ok":true,"services":[],"message":"daemon not running"}`.
- [ ] Manual: `workyard --worker localhost start nosuchsvc` → SERVICE_SELECTION_FAILED with the daemon's own hint; `workyard --worker localhost stop` with stopped daemon → exit 0.

### Task 4: Argument validation + empty-selection unification

**Files:** Modify `internal/cli/cli.go` (`controlCommand`, `waitCommand`, `eventsCommand`, `runControl`); `internal/worker/lifecycle.go` only if needed.

- [ ] `events`: `cobra.NoArgs`.
- [ ] `runControl`: client-side validation — every requested service must exist in `loaded.Config.Services` (logs also allows `setup`, `build`, `svc.beforeStart`, `svc.onClose`). Unknown → `SERVICE_UNKNOWN` error listing valid names.
- [ ] `wait` with zero args = all configured services (drop `MinimumNArgs(1)`); update Use string `wait [service...]`.
- [ ] `stop --all` kept as a no-op alias for compat; help text for stop/start/restart states "with no services, applies to all".
- [ ] Manual: `workyard --worker localhost status web` ok, `status bogus` errors, `wait` with no args waits on all.

### Task 5: Strict `deploy` positional parsing

**Files:** Modify `internal/cli/cli.go` (`deployProjectAndServices` ~2193); Test `internal/cli/deploy_test.go`.

- [ ] New rule: first arg is a **path** iff it looks like one (`.`/`..` prefix, `~`, absolute, contains a separator, or ends in `.yaml`/`.yml`); otherwise it is a **service**. If a non-path-looking first arg names an existing directory, error `DEPLOY_ARGS_AMBIGUOUS` telling the user to write `./name` or pass `--project`.
- [ ] Path-looking args that don't exist → existing "does not exist" error.
- [ ] Update existing deploy tests + add ambiguity case.

### Task 6: Surface remote stderr on failure

**Files:** Modify `internal/cli/cli.go` (`remoteControl` ~3113).

- [ ] On error: always print trimmed remote stderr (bounded ~4KB) to stderr regardless of `--verbose`; replace bare `printedError` with `output.NewError("REMOTE_COMMAND_FAILED", "<action> on <worker>: <err>", "Run with --verbose for full output")` when stdout was empty.
- [ ] Manual: break the remote binary path with `--remote-binary /nonexistent` against the Pi and confirm a readable error.

### Task 7: Real `inspect` output, stopped-service rendering, signal-aware exits

**Files:** Modify `internal/cli/cli.go` (`printDaemonResponse`), `internal/worker/lifecycle.go` (exit event ~214-245), `internal/worker/types.go` if a field is needed.

- [ ] `inspect` human renderer: per-service block — status/health, pid, started/stopped timestamps, startCommand, cwd, configured/assigned port + portEnv, URL, healthUrl, log paths, logsCommand.
- [ ] Status table: blank URL and PORT columns when service status is `stopped`/`exited` (keep in JSON).
- [ ] Exit events: when the process died from a signal, emit `web terminated by signal SIGTERM` instead of `exited with code -1` (use `ProcessState.Sys().(syscall.WaitStatus)`).
- [ ] Manual: deploy fixture locally; `inspect` shows block; `stop`; `status` shows blank URL; `events` shows signal message.

### Task 8: Cross-platform `open`

**Files:** Modify `internal/cli/cli.go` (`openCommand` ~2852).

- [ ] Choose opener by `runtime.GOOS`: darwin `open`, linux `xdg-open`, windows `rundll32 url.dll,FileProtocolHandler`. If opener missing or errors, print the URL to stdout with a note instead of failing.

### Task 9: `daemon` command shape + timeout validation

**Files:** Modify `internal/cli/cli.go` (`daemonCommand` ~2318, `waitCommand`, `deployCommand`, `logsCommand` flag types).

- [ ] Bare `workyard daemon` prints help (RunE → `cmd.Help()`) **unless** `--foreground` was explicitly set (`cmd.Flags().Changed("foreground")`), preserving `remote.EnsureDaemon`'s `daemon --foreground` invocation. Keep `--quiet` path working.
- [ ] `--timeout` flags on `deploy`/`wait`: validate with `time.ParseDuration` up front; error `TIMEOUT_INVALID` with example values.
- [ ] Daemon-side: warn in response message when lifecycle timeout is capped at 30m (`lifecycle_command.go:39`).
- [ ] `ui --json` help text documents that it prints listener info as JSON and then serves.

### Task 10: Fix `--state-dir` local-run split-brain (bug)

**Files:** Modify `internal/cli/cli.go` (every local `remote.BuildPaths(home,...)` call site), `internal/remote/remote.go` (`BuildPaths` validation), `internal/syncer/syncer.go` (`RunLocal`); Tests in `internal/remote/remote_test.go`, `internal/syncer/syncer_test.go`.

- [ ] Introduce `localPathsRoot(opts) string`: stateDir if set else `$HOME/.workyard`; local `BuildPaths` calls use it so run roots live under `<state-dir>/runs/...`.
- [ ] `BuildPaths` validation: "remote root must stay under <base>/.workyard/runs" becomes relative to the chosen base for local workers.
- [ ] `syncer.RunLocal` accepts the runs root (via `Options.RemoteRoot` already? verify) — ensure it derives destination from the same base.
- [ ] `localManagedRunRoot`/`guardLocalManagedPaths` validate against the same base.
- [ ] Integration check: `workyard --state-dir /tmp/wy-sd --worker localhost deploy fixtures/health-server` completes fully under `/tmp/wy-sd/runs/...`, then `cleanup run` removes it.

### Task 11: Inspection without `workyard.yaml` (registry fallback)

**Files:** Modify `internal/cli/cli.go` (`runControl`), `internal/registry/registry.go` (lookup helper); Test `internal/registry/registry_test.go`.

- [ ] For read/stop actions (`status,inspect,urls,logs,events,stop,wait,probe`): when `config.Load` fails with not-found AND `--project` was not explicitly set, look up `runs.json` for entries whose `LocalRoot` contains the cwd (or cwd is under it); if exactly one match, use its `ConfigPath`/project name (load config from `ConfigPath`); if several, error listing candidates; if zero, keep the original error but hint `Run from a project directory, pass --project, or see workyard runs list`.
- [ ] Manual: from `/tmp`, `workyard --worker localhost status` errors with helpful hint; from a subdirectory of the fixture run's source project, it works.

### Task 12: Daemon write-failure logging + version in ping

**Files:** Modify `internal/worker/events.go:17-28`, `internal/worker/lifecycle.go` saveState sites, `internal/worker/daemon.go`.

- [ ] `appendEvent`: on open/encode error, log once per path to the daemon's stderr (`log.Printf`) — daemon stdout/stderr already goes to `daemon.log`.
- [ ] `saveState` failures: same logging treatment at call sites that currently discard.
- [ ] (Version-in-ping handled in Task 1.)

### Task 13: macOS process identity

**Files:** Modify `internal/worker/process.go`; Test `internal/worker/daemon_test.go` additions.

- [ ] On darwin, capture start time via `ps -o lstart= -p <pid>` at spawn and compare on recovery (string equality), mirroring the linux `/proc` stat path. Fall back to PGID check when `ps` fails.

### Task 14: Install/build fallback + doctor SSH-auth & port-check fixes

**Files:** Modify `internal/cli/cli.go` (`installCommand`), `internal/remote/install.go`, `internal/doctor/doctor.go` (ssh check, port check ~537).

- [ ] `install`: when the artifact is missing and `go` is on PATH and we're in a module checkout, build it (reuse `bootstrap`'s build logic; extract to a shared helper). Error message otherwise lists both options (build command + `--local-binary`).
- [ ] Doctor remote: before the existing checks, test `ssh -o BatchMode=yes <target> true`; on failure, hint `ssh-copy-id <target>` explicitly.
- [ ] Doctor port check: try the installed worker binary first (`workyard daemonctl portcheck` — add tiny hidden daemonctl action or run a Go-side listener check via the binary); keep python3 as fallback; if both unavailable, mark check `skip` with message instead of fail.
- [ ] Manual on Pi: `workyard --worker jack-rasp-five doctor` passes; temporarily `--worker jack@nonexistent-host` shows the ssh-copy-id hint path.

### Task 15: `runs remove` accepts registered names

**Files:** Modify `internal/cli/cli.go` (`runsCommand` remove/list).

- [ ] Resolve arg through the worker store (same as `resolveWorkerTarget`) before `store.Remove`; try both raw and resolved values (pattern already exists in `workers remove`).

### Task 16: Friendly config errors

**Files:** Modify `internal/config/config.go` (Load error wrapping); Test `internal/config/config_test.go`.

- [ ] Post-process yaml strict-mode errors: rewrite `field X not found in type config.Service` → `unknown field "X" in services.<name>` where derivable, else `unknown field "X"`; suggest nearest known field by edit distance ≤2 (`did you mean "startCommand"?`). Static field lists per struct.

### Task 17: Exit code differentiation

**Files:** Modify `internal/output/output.go`, `internal/cli/cli.go` (`ExitCode`), `cmd/workyard/main.go` if needed; doc in README.

- [ ] Map error classes: usage/config (`WORKER_REQUIRED`, `CONFIG_*`, `*_INVALID`, `DEPLOY_ARGS_*`) → 2; connectivity (`SSH_FAILED`, `TAILSCALE_*`) → 3; daemon errors → 4; wait/health timeouts → 5; default 1. Implement as a code→class table in `output`.
- [ ] Unit test the mapping; document in README command reference.

### Task 18: Installer script portability

**Files:** Modify `scripts/local/install.sh`, `scripts/local/uninstall.sh`.

- [ ] Support Linux (drop the darwin-only guard; build for host GOOS/GOARCH).
- [ ] Detect login shell → patch `.zshrc`/`.bashrc`/`config.fish` (or print export line for unknown shells); after install, verify `command -v workyard` and print restart-shell note when absent.
- [ ] Manual: run installer on macOS, confirm idempotent PATH block; shellcheck both scripts.

### Task 19: Docs

**Files:** Modify `README.md`; Create `docs/errors.md`.

- [ ] Fix Quick Start local block (`--worker localhost`), audit other examples.
- [ ] Add explicit-worker design sentence ("Workyard never infers a target; every command names its worker").
- [ ] De-duplicate the repeated local-installer instructions (Install vs Build sections).
- [ ] `docs/errors.md`: table of error codes → meaning → exit class (from Task 17).

### Task 20: Full verification matrix + report disposition

- [ ] `go test ./...`, `go vet ./...`.
- [ ] Local: full deploy/status/inspect/logs/events/stop/cleanup of `fixtures/health-server` with default state dir AND with `--state-dir /tmp/wy-sd`.
- [ ] Remote (Pi, run id `uxfix`): cross-compile arm64, `workyard --worker jack-rasp-five deploy fixtures/health-server --install --run uxfix --fresh`, then status/inspect/logs/events/urls/stop, `cleanup run`; confirm pre-existing `main` run untouched.
- [ ] Annotate UX-REVIEW.md findings with disposition (fixed/partial/deferred + reason).
- [ ] Commit checkpoints after each task (`feat:`/`fix:` per repo convention).
