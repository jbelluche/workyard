# Workyard UX & Functionality Review

*Date: 2026-06-10 · Reviewed against `main` @ f5ed574 · No code was modified.*

## Disposition (2026-06-10, branch `ux-review-fixes`)

Every finding was triaged; fixes were applied and verified locally and
against the `jack-rasp-five` worker. Status per finding:

**Fixed** — §1.1 (`--worker` completion + enumerated `WORKER_REQUIRED` hints),
§1.2 (registry fallback for read-only commands outside the project), §1.3
(README quick start), §1.4 (daemon hint passthrough), §1.5 (empty selection =
all for start/stop/restart/wait; `--all` kept as documented alias), §1.6
(argument validation with valid-name listings), §1.7 (`stop` no-daemon
no-op), §1.8 (real `inspect` renderer with hints/recent stderr), §1.9
(cross-platform `open` with URL fallback), §1.10 (strict deploy arg parsing
with ambiguity error), §1.11 (remote failures surfaced via
`REMOTE_COMMAND_FAILED`), §1.12 signal-named exit events, blanked URL/PORT
for stopped services, bare `daemon` shows help, friendly config errors with
did-you-mean; §2 (`--state-dir` split-brain — verified end-to-end); §3 silent
state/event write failures (logged, deduped), version drift (ldflags +
version in every daemon response + mismatch warning), macOS process identity
(`ps lstart`), silent timeout coercions (CLI validation + cap event), doctor
python3 dependency (new hidden `portcheck` with python3 fallback); §4.3
installer (Linux + shell detection); §5 error-code reference
(`docs/errors.md`), exit-code classes (2 usage / 3 connectivity / 4 daemon /
5 wait), `runs remove` accepts registered names; doctor `ssh-copy-id` hint.

**Partial** — §4.1 worker provisioning: `install`/`workers setup` now
auto-build the matching artifact when `dist/` lacks it and Go is available;
release-artifact download stays open until releases are published. §4.5
README: quick start fixed, install sections de-duplicated, design stance
added; full restructure not done. §1.12 `ui --json` behavior documented in
help rather than changed.

**Deferred** (functionality projects, intentionally out of this pass) —
service restart policies and reboot persistence (systemd unit), log
rotation, background health polling, monitor SSH batching/backoff,
`RestartDaemon` transactionality, JSON envelope unification (breaking),
configurable SSH timeouts, multi-service `logs` tail, interactive setup
wizard, bootstrap package-manager detection/armv7 support.

## What Workyard is (as reviewed)

Workyard is an agent-first remote development runner: it syncs a local project to a worker
(remote over SSH/Tailscale, or `localhost`) into `~/.workyard/runs/<project>/<run>/`, runs
setup/build/services through a per-worker daemon on a private Unix socket, tracks health and
lifecycle events, and exposes a loopback monitor UI. The core loop is
`deploy → status/logs/events → watch → cleanup`.

**What's already good:** the command grouping in `--help` is clear; `deploy` is a genuinely nice
one-shot flow with stepwise `ok:` output; config parsing is strict (`KnownFields`) so typos fail
loudly; doctor is table-driven with hints; the security posture (loopback-only UI, Unix socket,
symlink guards, `.env` exclusion, bounded log reads) is consistently thought through; `--json`
exists almost everywhere, which matters for the agent-first goal.

The findings below are ordered by how much they hurt day-to-day use.

---

## 1. Ergonomics: the cost of every command

### 1.1 `--worker` is explicit by design — make explicitness cheap

**Design stance (owner-confirmed):** there is intentionally no default worker. Every command
must name its target (`requireWorker`, cli.go:2903) so a user or agent can never act on the
wrong machine by accident. The recommendations below keep that property and instead reduce the
*cost* of being explicit:

1. **Shell completion for worker names.** Register a cobra flag-completion function for
   `--worker` that offers `localhost` plus the names in `workers.yaml` (and optionally online
   Tailscale devices). `workyard --worker <TAB>` should finish the job; the `completion`
   command already exists but completes nothing useful for this flag today.
2. **Enumerate real options in the `WORKER_REQUIRED` error.** The current hint is generic
   ("Pass --worker localhost for this machine or --worker <name> for a registered worker").
   When workers are registered, list them:
   `registered workers: jack-r5-16gb, jack-rasp-five (or localhost)` — turning the error into
   the answer, which matters doubly for agents that read stderr and retry.
3. **State the stance in the docs.** One sentence in the README ("Workyard never infers a
   target; every command names its worker") converts what reads as an omission into a visible
   safety guarantee, and heads off the inevitable "add a default worker" issue reports.
4. **Keep registered names short.** Already supported via `workers add --name`; worth showing
   in the Quick Start so users type `--worker pi` instead of `--worker jack@jack-rasp-five`.

### 1.2 You must be inside the project to inspect anything

`workyard --worker localhost status` from outside the project dir fails with
`error: workyard.yaml not found from .` — empty hint. Yet the runs registry
(`runs.json`) already stores `LocalRoot` and `ConfigPath` for every run, and `runs list` shows
them. Inspection commands shouldn't require re-reading the project config at all — the daemon
only needs project name + run id.

**Fix:** for read-only actions (`status`, `logs`, `events`, `urls`, `inspect`, `stop`), when no
`workyard.yaml` is found, fall back to the registry: match by cwd prefix, or accept
`workyard status <project>[/<run>]`. At minimum, add a hint:
*"Run from a project directory, pass --project, or see `workyard runs list`."*

### 1.3 README Quick Start is broken

The README shows (lines 149–156):

```sh
workyard daemon start
workyard build
```

`workyard build` exits 1 with `--worker is required for build`. Given the explicit-worker
design stance (§1.1), the docs are simply stale.

**Fix:** update the README to `workyard --worker localhost build` (and audit the other
Quick Start lines for the same omission).

### 1.4 Misleading blanket hint on every daemon error

`localControl` wraps **all** daemon errors as
`DAEMONCTL_FAILED … hint: Run workyard daemon start` (cli.go:2986–2989). Observed:

```
$ workyard --worker localhost start nosuchsvc
error: SERVICE_SELECTION_FAILED: unknown service "nosuchsvc"
hint: Run workyard daemon start        ← daemon is running fine
```

**Fix:** pass through the daemon's own `Error.Hint`; only suggest `daemon start` when the error
is a socket dial failure.

### 1.5 `stop --all` is cosmetic; bare `stop` already stops everything

`selectServices` (lifecycle.go:350–351) treats empty service list as "all", so
`workyard stop` ≡ `workyard stop --all`. The README always shows `stop --all`, implying bare
`stop` does something narrower. Meanwhile `wait` *requires* ≥1 service (can't wait for all) —
three different "empty means…" conventions across sibling commands.

**Fix:** standardize: empty selection = all services for `start`/`stop`/`restart`/`wait`, drop
`--all`, or keep `--all` as a required confirmation only for destructive ops. Document whichever
rule wins.

### 1.6 Unknown/extra arguments are silently ignored

`workyard status notaservice` prints the normal table; `events foo` likewise. `status`,
`inspect`, `urls`, `events` have no cobra `Args` validator and pass args through to the daemon,
which ignores unknown names on read paths. Contrast with `start`, which errors properly.

**Fix:** add `cobra.NoArgs` to `status`/`inspect`/`urls`/`events` (or validate service names for
the filterable ones).

### 1.7 `stop` with no daemon leaks a raw socket error

```
$ workyard --worker localhost stop
error: dial unix /tmp/.../daemon/workyard.sock: connect: no such file or directory
hint: Run workyard daemon start
```

Starting a daemon just to stop nothing is the wrong hint, and the dial string is noise.

**Fix:** treat dial-failure on `stop` as a clean no-op: `nothing to stop (daemon not running)`,
exit 0.

### 1.8 `inspect` doesn't deliver what it promises

Help says *"Show detailed service state, hints, and recent events"*, but the human output is the
identical 6-column table `status` prints (`printDaemonResponse` default case, cli.go:3176). The
JSON *does* have rich fields (`startedAt`, `healthUrl`, `logs`, `logsCommand`…) — but no hints
and no events at all.

**Fix:** give `inspect` a real human renderer (per-service block with start command, cwd, ports,
health URL, log paths, last N events), and either add events/hints to the response or fix the
description.

### 1.9 `open` is macOS-only

`exec.Command("open", res.URLs[0].URL)` (cli.go:2852). On Linux this fails with a confusing
"executable not found".

**Fix:** switch on `runtime.GOOS` (`open` / `xdg-open` / `start`), and print the URL as a
fallback when no opener exists (also the right behavior over SSH sessions).

### 1.10 `deploy`'s positional-argument guessing is fragile

`deploy [project-path|workyard.yaml] [service...]` disambiguates the first arg by `os.Stat`
(cli.go:2193–2227): if a file/dir with that name exists it's a project path, otherwise it's a
service. So `workyard deploy web` deploys service `web` — unless someone creates a `web/`
directory, at which point the same command silently deploys a different project. It also
duplicates the global `--project` flag.

**Fix:** make `deploy` take only `[service...]` positionally and use `--project` for the path
(consistent with every other command), or require path-looking syntax (`./web`, absolute) for
the path form and error on ambiguity.

### 1.11 Remote command failures can be near-silent

`remoteControl` (cli.go:3113–3122) prints remote stdout, but **stderr only with `--verbose`**,
then returns a `printedError` that `Execute()` deliberately doesn't print. If the remote
`daemonctl` invocation fails before producing stdout (bad binary, SSH hiccup, JSON garbage), the
user gets exit 1 and nothing else.

**Fix:** always surface remote stderr (trimmed/bounded) on failure, and make the fallback error
message name the action and worker.

### 1.12 Smaller polish items

- `service.exit web exited with code -1` after a normal `stop` — render signal terminations as
  `terminated by SIGTERM`, not `-1`.
- Stopped services still show `URL`/`PORT` in `status` — misleading; blank them or add a note.
- `workyard daemon` (bare) runs the daemon **in the foreground** — surprising for a command
  group; the `--foreground` flag is declared then ignored (`_ = foreground`, cli.go:2325). Make
  bare `daemon` print help/status and move serving to a hidden `daemon run`.
- `workyard ui --json` prints a JSON blob *and then blocks serving* — a `--json` flag that
  changes nothing about the long-running behavior. Drop or document.
- `logs` accepts exactly one service; tailing all services of a run (`logs --all`) is a common
  want.
- Config typo errors leak Go internals (`field startCmd not found in type config.Service`) —
  rewrite as `unknown field "startCmd" in services.web` and suggest the nearest valid field
  (`startCommand`).

---

## 2. The `--state-dir` split-brain (bug)

Observed directly:

```
$ workyard --state-dir /tmp/state deploy ./fixture --worker localhost
ok: sync - /Users/jack/.workyard/runs/.../source     ← synced to REAL home
error: RUN_ROOT_INVALID: run root must be exactly under /tmp/state/runs/<project>/<run>
```

The local syncer and `BuildPaths` derive the runs root from `$HOME/.workyard` regardless of
`--state-dir`, while the daemon (correctly started with the custom state dir) validates run
roots against `<state-dir>/runs`. The escape hatch doesn't work either:
`--remote-root /tmp/state/runs` is rejected with *"remote root must stay under
~/.workyard/runs"*. Net effect: `--state-dir` + any local run command is unusable, and a failed
deploy leaves orphan files in the real `~/.workyard/runs`.

**Fix:** for the `localhost` worker, derive the runs root from the state dir in one place and
use it for sync, path building, and daemon validation alike. Relax `--remote-root` validation to
"under the active state dir" for local workers. Add an integration test that runs a full local
deploy under a temp `--state-dir`.

---

## 3. Lifecycle & robustness gaps (worker daemon)

These are functionality gaps rather than polish; the first two undermine the "remote dev runner"
promise the most.

| Gap | Evidence | Recommendation |
|---|---|---|
| **No restart policy / crash recovery.** A crashed service stays `exited` forever; after a worker reboot nothing comes back. | lifecycle.go:214–245; daemon recovery only re-attaches still-running PIDs (daemon.go:106–149) | Add per-service `restartPolicy: never\|on-failure\|always` with backoff; emit `service.crashed` events |
| **Daemon doesn't survive reboots.** Remote daemon is a `nohup` started over SSH (remote.go:204–228). | remote.go:204 | Have `workers setup` install a systemd user unit (`workyard daemon install-service`); fall back to nohup |
| **No log rotation anywhere.** Service logs, events JSONL, and `daemon.log` grow unbounded. | lifecycle.go:173; remote.go:213 | Size-capped rotation (e.g. keep N×10MB), plus `cleanup logs` already existing as manual relief |
| **Health is only re-checked when someone asks.** A service that goes unhealthy stays unhealthy until the next `status` call; no background probing. | lifecycle.go:336–344 | Optional daemon-side periodic health polling; record transitions as events |
| **State/event writes fail silently.** Event append errors discarded (events.go:17–28); `saveState` failures ignored in several paths (lifecycle.go:41,94,146,242) | as cited | Log to daemon.log at minimum; surface in `inspect` |
| **Version drift is invisible.** `Version` is a hardcoded `0.1.0` const (cli.go:36); CLI↔daemon mismatch is only checked during `install`, never at daemon contact | install.go:97–99 | Embed real version via `-ldflags`; include version in daemon `ping` response and warn on mismatch |
| **macOS process identity is weak.** Without `/proc` start-time, PID reuse can defeat stale-process detection | process.go:33–34 | Use `ps -o lstart=` or kinfo_proc on darwin |
| **`RestartDaemon` is stop-then-start with no rollback.** Failure after stop leaves services orphaned with no daemon | remote.go:231–236 | Start-new-then-confirm, or at least re-attempt + clear error |
| **Silent timeout coercions.** Invalid `--timeout` values fall back silently (lifecycle.go:490–498); setup/build silently capped at 30min (lifecycle_command.go:40) | as cited | Validate `--timeout` at the CLI (cobra duration flag); warn when capping |
| **Doctor's remote port-range check requires python3 on the worker** | doctor.go:537 | Use a pure-shell or daemon-side check; minimal images often lack python3 |

---

## 4. Setup & installation experience

You called this out specifically — the machine-setup story is workable for *you* (the developer
with a checkout and Go installed) but breaks for anyone else, and has avoidable friction even
for you.

### 4.1 Worker provisioning requires a source checkout *(structural)*

`install` and `workers setup` expect `dist/workyard-<os>-<arch>` to exist locally, or build it
with the local Go toolchain (install hint literally says run `GOOS=… go build …`). Someone who
installed Workyard via the release script or Homebrew has no `dist/`, no repo, possibly no Go —
they **cannot provision a worker at all**.

**Fix (recommended):** teach `install`/`setup` to fetch the matching release artifact:
try `dist/` → try local Go build → download `workyard-<os>-<arch>.tar.gz` for the running
version from GitHub releases, verify against `checksums.txt`, upload. The release packaging
pipeline (`build-release.sh`, manifest, checksums) already exists — this closes the loop.

### 4.2 First-run flow is a scavenger hunt

Today a new user must: read README → clone → `scripts/local/install.sh` → `workyard doctor` →
set up SSH keys themselves → hand-write `workyard.bootstrap.yaml` → `workers setup` →
`workyard init` → edit config → `deploy`. Each step is individually fine; together they're a
long unguided path.

**Fix:** an interactive `workyard setup` (or `workers setup --interactive`) wizard:
1. run local doctor checks inline
2. list Tailscale devices (`workers discover` already exists) and let the user pick
3. test SSH; on failure print the exact `ssh-copy-id user@host` to run
4. generate a minimal bootstrap config from answers (write it out for reuse)
5. install binary + daemon (+ offer the systemd unit from §3)
6. register the worker under a short name
7. finish with `Try: workyard deploy . --worker <name>`

Also add an SSH-key/agent check to doctor with the `ssh-copy-id` hint — it currently checks that
`ssh` exists but not that auth will succeed non-interactively, which is *the* classic first-run
failure.

### 4.3 Local installer is macOS + zsh only

`scripts/local/install.sh` refuses Linux and only patches `~/.zshrc`. Bash/fish users on a Mac
get a binary that isn't on PATH with no message about it.

**Fix:** support Linux (it's just `go build` + copy), detect `$SHELL` and patch the right rc
file (or print the export line instead of editing), and verify `workyard` resolves on PATH at
the end, printing "restart your shell" when needed.

### 4.4 Bootstrap rough edges

- apt-only package install with no package-manager detection — fails opaquely on non-Debian
  workers; detect `apt-get`/`dnf`/`apk` or fail early with a clear "unsupported distro" message.
- `docker` step treats `systemctl enable --now docker || true` as success — verify
  `docker info` afterward; warn that the docker group needs re-login.
- Architecture support is exactly {linux,darwin}×{amd64,arm64}; `armv7` (older Pis) hard-fails.
  Either add `GOARM` targets or emit a specific "32-bit ARM is unsupported, use a 64-bit OS"
  message.
- Pinned apt versions must exist in the worker's repos; on mismatch surface the available
  version (`apt-cache madison`) in the hint.

### 4.5 README structure

The Install / Build / Quick Start sections repeat the local-installer instructions twice and mix
maintainer concerns (release packaging, cross-compiling) with user setup. Restructure as:
**Install (user) → First worker (wizard) → Daily commands → Configuration → Maintainer docs.**
And fix the broken Quick Start (§1.3).

---

## 5. Consistency & machine-output details

- **JSON envelope inconsistency:** some commands emit `{"ok": true, …}` maps, `sync` emits its
  raw result struct, `logs`/`events` emit JSONL lines, dashboard APIs use their own shapes.
  Define one envelope (`ok`, `data`, `error{code,message,hint}`) and a documented JSONL rule for
  streaming commands.
- **Error codes are ad-hoc** (`WORKER_CONFIG_INVALID`, `DAEMONCTL_FAILED`, `SSH_FAILED`…) with
  no reference doc. Enumerate them in one Go file and generate a docs table; agents (the target
  audience!) need stable codes to branch on.
- **Exit codes are all 1.** Differentiate: 1 generic, 2 usage/config, 3 connectivity, 4 daemon
  error, 5 health timeout — and document it.
- **Hardcoded timeouts** (8s doctor SSH, 30/90s remote actions, 5s daemon grace, 3s UI poll) —
  fine as defaults, but expose `--ssh-timeout`/config knobs for slow links; Tailscale relay
  (DERP) paths regularly exceed 8s on first contact.
- **Monitor polls each run with separate SSH connections** every 3s (fetcher) — N runs on one
  worker = N connections per tick. Batch per-worker (one SSH call returning all runs) and/or use
  SSH `ControlMaster`. Stale registry entries poll forever until manually pruned; auto-mark
  `offline` entries dormant after several failures and stop polling them at full rate.
- `runs remove <worker> <project> <run>` requires the worker as the **resolved ssh target
  string** (`jack@host`), while everywhere else accepts registered names. Accept names here too.

---

## Top 10 recommended fixes, prioritized

| # | Fix | Why first | Effort |
|---|---|---|---|
| 1 | `--worker` completion + worker names enumerated in `WORKER_REQUIRED` errors | Keeps the explicit-worker design while removing most of its typing cost | S |
| 2 | Fix `--state-dir` local-run split-brain | It's a real bug that strands files in `~/.workyard` | S |
| 3 | Pass through daemon error hints; fix `stop`-with-no-daemon no-op | Wrong hints actively mislead | S |
| 4 | Status/logs/events without `workyard.yaml` via runs registry | Makes inspection location-independent | M |
| 5 | Release-artifact download in `install`/`workers setup` | Unblocks worker provisioning for non-developers | M |
| 6 | Interactive `workyard setup` wizard + doctor SSH-auth check | Collapses the painful first-run path | M |
| 7 | Restart policy + systemd unit for the worker daemon | Services surviving crashes/reboots is table stakes for a runner | M–L |
| 8 | Unify empty-selection semantics (`stop`/`--all`/`wait`); add `Args` validators | Removes silent surprises | S |
| 9 | Real `inspect` human output; signal-aware exit reporting | The debugging command should debug | S |
| 10 | Version via ldflags + mismatch warning on daemon contact | Prevents a class of mystery failures as the tool evolves | S |

*(S ≈ hours, M ≈ a day or two, L ≈ multi-day.)*

---

## Verification notes

Findings marked "observed" were reproduced against a fresh build (`go build ./cmd/workyard`) on
this machine using the bundled `fixtures/health-server`, an isolated `--state-dir`, and a full
local deploy/stop/cleanup cycle (test run was removed afterward via `workyard cleanup run`; the
pre-existing `main` runs in the registry were untouched). File:line references are against the
current working tree. One earlier-draft claim was dropped after verification: sudo passwords
containing newlines are already rejected at the prompt (cli.go:1406), so the bootstrap stdin
path is not exposed to them.
