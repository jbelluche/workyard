# Workyard

Workyard is an agent-first remote workspace tool. Its primary workflow is `workyard mirror`: register a local directory, keep it mirrored to a private worker over SSH/Tailscale, then shell or run commands in that remote workspace as if it were a normal clone.

Workyard also includes lower-level service orchestration commands for projects that need a worker daemon, health checks, preview URLs, logs, and lifecycle events. Those commands remain useful, but the mirror workflow is the simplest daily path for "put this repo on a bigger Linux box and work there."

Workyard does not guess where to run. Mirror records store the worker selected during setup, and service/run commands name their worker with `--worker`. Use `--worker localhost` for this machine or a registered worker name (shell completion offers both).

> **Warning**
> Workyard should only be installed and run on machines you trust. It syncs project files, starts configured commands, manages local/remote processes, and reads bounded logs over SSH.

## Install

Install from the public repository without cloning:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | sh
```

The installer supports macOS and Linux. It downloads the latest release artifact when one exists and falls back to building from a public source archive when Go is available. It installs Workyard to `~/.local/bin/workyard` by default and adds a marked PATH block to your shell profile (`~/.zshrc` or `~/.bashrc`) when needed.

Upgrade an existing local install:

```sh
workyard update
workyard upgrade
workyard update --version v0.1.0
```

Install a specific release, custom repo, or custom directory:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | WORKYARD_VERSION=v0.1.0 sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | WORKYARD_INSTALL_DIR="$HOME/bin" sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | sh -s -- --repo owner/workyard --method source --ref main
```

For local development from a checkout, use:

```sh
git clone https://github.com/jbelluche/workyard.git
cd workyard
scripts/local/install.sh
```

Uninstall the local binary:

```sh
rm -f ~/.local/bin/workyard
```

The default model is private and local:

- Project files sync with `rsync` over SSH.
- Remote control happens through a private Unix socket on the worker.
- Preview URLs stay on your private network, such as Tailscale.
- The monitor dashboard listens on loopback only.
- Logs are read through bounded CLI commands, not exposed publicly.

## Requirements

- Go 1.25 or newer when building from source
- `rsync`
- `ssh`
- Tailscale installed, running, and connected
- A worker machine reachable over SSH, usually through Tailscale
- Optional: `tmux` on the worker for persistent remote shells

Check your local setup with:

```sh
workyard doctor
```

With a worker:

```sh
workyard --worker user@worker-host doctor
```

Once passwordless SSH works, bootstrap a new worker:

```sh
workyard workers setup worker-name --config workyard.bootstrap.yaml
```

## Build

From the repository root:

```sh
go build -o dist/workyard-darwin-arm64 ./cmd/workyard
```

For a Linux ARM64 worker, such as a Raspberry Pi:

```sh
GOOS=linux GOARCH=arm64 go build -o dist/workyard-linux-arm64 ./cmd/workyard
```

Install the local binary somewhere on your `PATH` with `scripts/local/install.sh` (see [Install](#install)).

Install or upgrade the worker binary on the remote machine:

```sh
workyard --worker user@worker-host install
```

Workyard expects the remote binary at `~/.workyard/bin/workyard` unless you pass `--remote-binary`.
The install command detects the worker OS/architecture, uploads `dist/workyard-<os>-<arch>`, and verifies the installed version.

## Quick Start

Register a reachable Tailscale/SSH worker:

```sh
workyard workers discover
workyard workers add linux-builder --user dev
```

Create a mirror for the current directory:

```sh
workyard mirror setup
```

The wizard asks for the local directory, worker, remote destination, exclude presets, and a final confirmation. The default remote destination is `~/workspace/<directory-name>`.

Sync once and open a remote shell:

```sh
workyard mirror sync
workyard mirror shell --auto
```

Use a persistent tmux shell so the remote session survives disconnects:

```sh
workyard mirror shell --auto --tmux
```

Run one command in the remote mirror:

```sh
workyard mirror exec --auto -- git status
workyard mirror exec --auto -- npm test
```

If the mirrored project has a `workyard.yaml`, start its services from the mirror:

```sh
workyard mirror services up --timeout 90s
workyard mirror services status
workyard mirror services logs api --tail 200
workyard mirror services restart api
```

Run continuous mirroring in the current terminal:

```sh
workyard mirror
```

Or run mirroring in the background:

```sh
workyard mirror start
workyard mirror status
workyard mirror stop
```

Useful mirror maintenance commands:

```sh
workyard mirror list
workyard mirror doctor --fix
workyard mirror rename <id> <new-name>
workyard mirror tmux list
workyard mirror tmux kill <id>
workyard mirror delete <id>
```

Each mirror has a stable short ID. Names are labels and may collide; when a name is ambiguous, Workyard asks for an ID.

## Service Orchestration

Use service orchestration when you want Workyard to manage processes, health checks, preview URLs, logs, and lifecycle events through the worker daemon.

Create a service config:

```sh
workyard init
```

Validate the config:

```sh
workyard config check
```

When the project is mirrored, prefer the mirror service bridge:

```sh
workyard mirror services up <id> --timeout 90s
workyard mirror services status <id>
workyard mirror services logs <id> web --tail 200
workyard mirror services restart <id> web
workyard mirror services cleanup <id>
```

`mirror services up` syncs the mirror, runs `setup` and `build`, starts services through the worker daemon, waits for health checks, and prints preview URLs. The service run ID is the mirror ID, so two mirrors of the same repo get isolated run state and independent port allocation. `cleanup` removes only the daemon service run wrapper; it leaves the mirrored files intact.

Deploy everything to a worker:

```sh
workyard deploy . --worker user@worker-host
```

Deploy from any project path or config path:

```sh
workyard deploy /path/to/project --worker user@worker-host
workyard deploy /path/to/project/workyard.yaml --worker user@worker-host
```

Deploy from a clean remote run directory:

```sh
workyard deploy /path/to/project --worker user@worker-host --fresh
```

`deploy` runs `doctor`, `sync`, `setup`, `build`, relaunches services, waits for healthy services, and prints the active URLs. Add `--install` when you want it to install or upgrade the worker binary first; deploy restarts the worker daemon after installing so the new binary is active.

You can still run individual lower-level service commands:

```sh
workyard --worker user@worker-host sync
workyard --worker user@worker-host setup
workyard --worker user@worker-host build
workyard --worker user@worker-host start web
```

Inspect service status and URLs:

```sh
workyard --worker user@worker-host status
workyard --worker user@worker-host urls
workyard --worker user@worker-host inspect
```

For local fixture/dev runs, pass `--worker localhost`. Workyard uses a private background daemon on your machine (auto-started when needed) and prepares a local managed run under `~/.workyard/runs`:

```sh
workyard daemon start
workyard --worker localhost build
workyard daemon status
workyard daemon stop
```

Read logs and events:

```sh
workyard --worker user@worker-host logs web --tail 200
workyard --worker user@worker-host logs web --follow
workyard --worker user@worker-host events
```

Stop services (all services when none are named):

```sh
workyard --worker user@worker-host stop
```

Clean logs or remove a run:

```sh
workyard --worker user@worker-host cleanup logs
workyard --worker user@worker-host cleanup run
```

`cleanup logs` truncates Workyard log files in place. `cleanup run` stops services first, removes only the validated `~/.workyard/runs/<project>/<run>` directory, and removes that run from the local monitor registry.

## Dashboard

Workyard can run a local monitor UI with an embedded dashboard:

```sh
workyard ui --open
```

The UI listens on `127.0.0.1:3099` by default. Use another loopback port if needed:

```sh
workyard ui --listen 127.0.0.1:32200 --open
```

The dashboard is backed by local JSON APIs:

- `GET /api/state`
- `GET /api/workers`
- `GET /api/runs`
- `GET /api/services`
- `GET /api/events`
- `GET /api/urls`

Workers can be discovered from Tailscale and registered locally before they have any Workyard runs:

```sh
workyard workers discover
workyard workers add linux-builder --user dev
workyard workers config show
workyard workers list
```

Registered workers are stored in `~/.workyard/local/workers.yaml`. The normal editable fields are `name`, `host`, and `user`; Workyard connects with `user@host` unless `sshTarget` is set as an explicit override.

```yaml
workers:
  - name: linux-builder
    host: linux-builder
    user: dev
    source: tailscale
    dnsName: linux-builder.tailnet.ts.net
```

Static SSH workers and cross-tool defaults can also be configured in `~/.workyard/config.toml`. These workers are read-only from `workyard workers add/remove`, show up with `source=config`, and can be used anywhere `--worker` accepts a registered worker name.

```toml
[defaults]
ssh_user = "dev"
remote_workspace = "~/workspace"

[[workers]]
name = "devbox"
ssh = "dev@devbox.example.com"
remote_workspace = "/srv/workspaces"

[[known_hosts]]
name = "ssh-config-alias"
ssh = "my-devbox"
```

`[[workers]]`, `[[known_hosts]]`, and `[[static_hosts]]` are accepted aliases for configured static workers. `ssh` can be a plain SSH config alias, a host, or `user@host`; if `remote_workspace` is set, `workyard mirror setup` uses it when suggesting the default remote destination.

Inspect and validate the global config:

```sh
workyard config show
workyard config check
```

Registered and configured names can be used anywhere `--worker` is accepted:

```sh
workyard deploy . --worker linux-builder --fresh
```

Bootstrap a reachable SSH machine as a Workyard-ready worker:

```sh
workyard workers setup linux-builder --config workyard.bootstrap.yaml
```

The setup command can create private Workyard directories, repair `~/.workyard/runs` permissions, build and upload the correct worker binary, start or restart the worker daemon, optionally install apt packages or Docker, register the worker locally, and run a final worker doctor check.

Example `workyard.bootstrap.yaml`:

```yaml
version: 1

workers:
  linux-builder:
    ssh:
      user: dev
      host: linux-builder

    register: true

    workyard:
      install: true
      daemon: true

    tailscale:
      requireConnected: true

    packages:
      install: true
      apt:
        - rsync
        - name: curl
          version: 7.88.1-10+deb12u12
        - ca-certificates

    docker:
      install: true
      composePlugin: true
      addUserToGroup: true
      version: 20.10.24+dfsg1-1+deb12u1
      composeVersion: 2.26.1-1

    checks:
      doctor: true
```

Use `--dry-run` to review what setup would do without changing the worker:

```sh
workyard workers setup linux-builder --config workyard.bootstrap.yaml --dry-run
```

By default, setup uses non-interactive SSH and non-interactive `sudo -n`; if a privileged install needs a password, Workyard stops and prints the manual command to run on the worker. For trusted workers, you can opt into a local hidden sudo prompt:

```sh
workyard workers setup linux-builder --config workyard.bootstrap.yaml --ask-sudo-password
```

The password is sent over SSH stdin for the privileged setup commands and is not stored in the bootstrap config. Do not store passwords, Tailscale auth keys, or other secrets in `workyard.bootstrap.yaml`.

Package entries can be plain names or pinned apt package versions:

```yaml
packages:
  apt:
    - rsync
    - name: curl
      version: 7.88.1-10+deb12u12
```

Pinned versions use apt's `package=version` install syntax and must match a version available from the worker's configured apt repositories.

Commands such as `sync`, `start`, `status`, and `watch` register active runs in `~/.workyard/local/runs.json`, which the monitor polls through SSH.

Manage that local monitor registry:

```sh
workyard runs list
workyard runs remove user@worker-host my-project feature-branch
workyard runs prune --older-than 168h
workyard workers list
workyard workers remove linux-builder
```

Registry pruning only removes stale monitor entries. It does not delete remote run directories.

## Configuration

Workyard uses `workyard.yaml` at the project root.

Example:

```yaml
name: my-project

sync:
  exclude:
    - node_modules
    - dist

worker:
  portRange: "3100-3999"

setup:
  command: npm install
  timeout: 10m

build:
  command: npm run build
  timeout: 10m

services:
  web:
    path: .
    startCommand: npm run dev
    port:
      default: 3000
      env: PORT
    env:
      HOST: 0.0.0.0
    health:
      url: http://127.0.0.1:3000/health
      timeout: 30s
    beforeStart:
      command: npm run prepare
      timeout: 2m
    onClose:
      command: npm run cleanup
      timeout: 2m
    watch:
      paths:
        - src
        - package.json
      include:
        - "*.js"
        - "*.ts"
        - "*.tsx"
        - "package.json"
      exclude:
        - node_modules
      action: sync-restart
      debounce: 750ms
```

### Project Fields

- `name`: Project name. Used in the remote run path.
- `sync.exclude`: Extra paths or patterns to exclude from sync.
- `sync.includeEnvFiles`: Include `.env` files when syncing. Defaults to false.
- `worker.portRange`: Port range Workyard may allocate on the worker.
- `setup`: Optional project setup command.
- `build`: Optional project build command.
- `services`: Service definitions.

### Service Fields

- `path`: Relative working directory inside the project.
- `startCommand`: Command used to start the service.
- `shell`: Run the command through a shell when true.
- `port.default`: Local/configured port.
- `port.env`: Environment variable that receives the assigned worker port.
- `env`: Extra environment variables for the service.
- `health.url`: Local worker health URL.
- `health.timeout`: Startup health timeout.
- `beforeStart`: Optional command run before the service starts.
- `onClose`: Optional command run after the service stops.
- `watch`: Optional file watch configuration.

The old `command` service field is not supported. Use `startCommand`.

When services start, Workyard injects the selected service's assigned port through `WORKYARD_PORT` and the configured `port.env` name. It also injects peer service connection values for the whole run: `WORKYARD_SERVICE_<SERVICE>_PORT` and `WORKYARD_SERVICE_<SERVICE>_URL`, where `<SERVICE>` is the uppercased service name with non-alphanumeric characters replaced by `_`. For example, an `api` service can call `analytics` through `WORKYARD_SERVICE_ANALYTICS_URL`, and both values stay correct when two runs of the same project receive different worker ports.

## Secrets Model

Workyard treats `.env` files as sensitive inputs. By default, sync excludes `.env`, `.env.local`, and `.env.*.local`; set `sync.includeEnvFiles: true` only for fixtures or worker environments where those files are intentionally copied. Workyard never edits `.env` files to resolve port conflicts. Assigned ports are injected into process environments instead.

Daemon control stays on a private Unix socket under `~/.workyard/daemon`, remote commands run over SSH, and local commands can start the local daemon in the background with `workyard daemon start`. Stop it with `workyard daemon stop`, and check it with `workyard daemon status`. Logs are read through bounded commands unless `logs --follow` is explicitly requested. Log and inspect output redacts common secret shapes such as `TOKEN=value`, `api_key: value`, bearer tokens, and URL credentials, but redaction is a safety net rather than a reason to print secrets.

## Watch Mode

Watch mode polls local files, syncs changes, and optionally restarts services:

```sh
workyard --worker user@worker-host watch
```

Watch a specific service:

```sh
workyard --worker user@worker-host watch web
```

Run one change cycle and exit:

```sh
workyard --worker user@worker-host watch web --once
```

Supported watch actions:

- `sync-restart`: Sync changes and restart matching services.
- `sync-only`: Sync changes without restarting.

Watch mode uses filesystem events when available and falls back to polling with `--poll-interval`.

## Mirror Workflow

Mirror keeps registered local directories reflected on workers without requiring a `workyard.yaml` service config. It is useful when you want to SSH into a Tailscale-connected device or a static SSH dev box and find a normal workspace directory with your latest local edits.

Configure a mirror with the wizard:

```sh
workyard mirror setup
```

The wizard asks for:

- Local directory, defaulting to the current directory.
- Registered or globally configured worker.
- Remote destination, defaulting to `<remote_workspace>/<directory-name>` when configured, otherwise `~/workspace/<directory-name>`.
- Exclude presets, auto-detected by default from repository files.
- Confirmation before writing the local mirror registry.

Run all enabled mirrors in the current terminal:

```sh
workyard mirror
```

Sync once and exit:

```sh
workyard mirror sync
workyard mirror sync <name-or-id>
```

Foreground mirroring prints each sync:

```text
synced workyard to linux-builder:/home/dev/workspace/workyard
```

Pass `--verbose` to include the itemized changed paths and transferred size. Press `Ctrl-C` to stop foreground mirroring. Start and stop background mirroring with:

```sh
workyard mirror start
workyard mirror status
workyard mirror stop
```

Open a remote shell in a mirror:

```sh
workyard mirror shell <name-or-id>
```

If the destination is missing or empty, let Workyard sync once before opening the shell:

```sh
workyard mirror shell <name-or-id> --auto
```

Use tmux for persistent shells:

```sh
workyard mirror shell <name-or-id> --auto --tmux
```

Default tmux sessions are named from the immutable mirror ID:

```text
workyard-<id>
```

Renaming a mirror changes only its human label; it does not change the ID, the marker owner, or the default tmux session. List and kill mirror tmux sessions with:

```sh
workyard mirror tmux list
workyard mirror tmux list <name-or-id>
workyard mirror tmux kill <name-or-id>
workyard mirror tmux kill <name-or-id> --session <custom-session>
```

Run commands directly in the remote mirror directory:

```sh
workyard mirror exec <name-or-id> -- git status
workyard mirror exec <name-or-id> --auto -- npm test
workyard mirror exec --auto -- go test ./...
```

Run configured services from the mirrored workspace:

```sh
workyard mirror services up <name-or-id> --timeout 90s
workyard mirror services status <name-or-id>
workyard mirror services inspect <name-or-id>
workyard mirror services urls <name-or-id>
workyard mirror services logs <name-or-id> <service> --tail 200
workyard mirror services events <name-or-id>
workyard mirror services restart <name-or-id> <service>
workyard mirror services cleanup <name-or-id>
```

These commands load `workyard.yaml` from the local mirror source, sync when appropriate, then manage services against the remote mirror destination. Workyard creates a small managed run wrapper under `~/.workyard/runs/<project>/<mirror-id>` whose `source` points at the mirror. That keeps service logs, status, health checks, peer service environment variables, and port allocation in the daemon while preserving the remote workspace as the place you edit, shell into, and inspect.

Mirror excludes stop local generated directories from being uploaded, and they also protect worker-generated directories from later sync deletion. After `mirror services up`, the remote mirror may contain worker-local `node_modules`, `.next`, `bin`, or similar build outputs created by setup/build/start commands; those files are intentionally remote-local.

List, pause, resume, rename, doctor, and delete configured mirrors:

```sh
workyard mirror list
workyard mirror pause <name-or-id>
workyard mirror resume <name-or-id>
workyard mirror rename <name-or-id> <new-name>
workyard mirror doctor <name-or-id>
workyard mirror doctor <name-or-id> --fix
workyard mirror delete <name-or-id>
```

Each mirror has a stable short ID shown in `mirror list`. Commands accept a name when exactly one mirror has that name; if several mirrors share a name, Workyard asks you to use one of the IDs.

`mirror setup` requires the remote destination to be missing, empty, or already marked as the same Workyard mirror. Use `--force` only when you intentionally want to mirror into a non-empty directory. Mirror syncs use default excludes such as `node_modules`, build outputs, logs, and `.env` files; `.git` is included by default so the remote workspace behaves like a clone. Continuous mirror mode watches `.git` for mirrors that include git, so local commits update the worker's git state without restarting the mirror.

Mirror setup uses `--preset auto` by default. Auto detection looks for common repository files such as `package.json`, `pyproject.toml`, `go.mod`, `Cargo.toml`, `pom.xml`, `*.csproj`, and `Gemfile`, then adds generated/cache excludes for the detected ecosystems. Override it with explicit presets or disable it:

```sh
workyard mirror setup --preset node --preset python
workyard mirror setup --preset none
```

`workyard mirror doctor` checks local source readability, local and remote `rsync`, SSH connectivity, destination safety and marker ownership, and stored presets. `--fix` applies only safe repairs: removing stale local mirror pid files, creating missing destinations, and securing destinations that are empty or already marked as the same mirror. It skips non-empty unmarked paths, symlinks, files, and mismatched markers.

Deleting a mirror only removes the local registry record by default. To remove the remote files too, pass `--delete-remote`; Workyard will refuse unless the worker has a matching Workyard mirror marker for that destination. Markers are stored outside the mirrored directory so they do not show up in remote `git status`:

```sh
workyard mirror delete <name> --delete-remote
```

## Run IDs

By default, Workyard derives a run id from the current project. You can provide one explicitly:

```sh
workyard --worker user@worker-host --run feature-branch sync
workyard --worker user@worker-host --run feature-branch start
```

Remote runs are stored under:

```text
~/.workyard/runs/<project>/<run>/
```

Each run contains:

- `source/`: Synced project files.
- `logs/`: Service logs and lifecycle events.
- `state.json`: Worker daemon state.
- `sync.json`: Sync metadata.

## Release Packaging

Workyard releases are GitHub Releases created from `vX.Y.Z` tags. To cut a release:

```sh
git tag -a v0.1.0 -m "Workyard v0.1.0"
git push origin v0.1.0
```

The release workflow runs tests, builds release artifacts, verifies them, and publishes the GitHub Release. For a local dry run:

```sh
VERSION=v0.1.0 scripts/build-release.sh
```

The release builder writes tarballs, `checksums.txt`, and `manifest.json` under `dist/release/`:

```text
workyard-darwin-amd64.tar.gz
workyard-darwin-arm64.tar.gz
workyard-linux-amd64.tar.gz
workyard-linux-arm64.tar.gz
checksums.txt
manifest.json
```

See [docs/releases.md](docs/releases.md) for the full release process.

End-user install script:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | sh
```

Set `WORKYARD_REPO`, `WORKYARD_VERSION`, `WORKYARD_INSTALL_DIR`, `WORKYARD_INSTALL_METHOD`, or `WORKYARD_REF` when installing from a non-default location. Homebrew formulas can reference the tarball URL and matching SHA-256 from `checksums.txt`.

## Command Reference

Mirror commands:

```sh
workyard mirror setup
workyard mirror
workyard mirror sync
workyard mirror sync <id>
workyard mirror --once
workyard mirror --verbose
workyard mirror start
workyard mirror status
workyard mirror stop
workyard mirror list
workyard mirror shell <id>
workyard mirror shell <id> --auto
workyard mirror shell <id> --auto --tmux
workyard mirror exec <id> -- <command> [args...]
workyard mirror exec <id> --auto -- <command> [args...]
workyard mirror doctor <id>
workyard mirror doctor <id> --fix
workyard mirror rename <id> <new-name>
workyard mirror pause <id>
workyard mirror resume <id>
workyard mirror tmux list
workyard mirror tmux list <id>
workyard mirror tmux kill <id>
workyard mirror tmux kill <id> --session <custom-session>
workyard mirror delete <id>
workyard mirror delete <id> --delete-remote
```

Project commands:

```sh
workyard init
workyard config check
workyard config show
workyard services
workyard --worker user@worker-host sync
workyard --worker user@worker-host setup
workyard --worker user@worker-host build
workyard --worker user@worker-host start
workyard --worker user@worker-host start web
workyard --worker user@worker-host status
workyard --worker user@worker-host inspect
workyard --worker user@worker-host urls
workyard --worker user@worker-host open web
workyard --worker user@worker-host logs web --tail 200
workyard --worker user@worker-host logs web --follow
workyard --worker user@worker-host events
workyard --worker user@worker-host wait web --healthy
workyard --worker user@worker-host probe web
workyard --worker user@worker-host restart web
workyard --worker user@worker-host stop
workyard --worker user@worker-host watch
```

End-to-end deployment:

```sh
workyard deploy . --worker user@worker-host
workyard deploy /path/to/project/workyard.yaml --worker user@worker-host
workyard deploy . --worker user@worker-host --fresh
workyard deploy . --worker user@worker-host --install
```

Dependency, update, install, and daemon commands:

```sh
workyard doctor
workyard update
workyard update --dry-run
workyard --worker user@worker-host doctor
workyard --worker user@worker-host install
workyard daemon start
workyard daemon status
workyard daemon stop
```

Worker and monitor registry commands:

```sh
workyard workers discover
workyard workers add linux-builder --user dev
workyard workers setup linux-builder --config workyard.bootstrap.yaml
workyard workers config show
workyard workers list
workyard workers remove linux-builder
workyard runs list
workyard runs remove user@worker-host my-project feature-branch
workyard runs prune --older-than 168h
```

Cleanup and utility commands:

```sh
workyard --worker user@worker-host cleanup logs
workyard --worker user@worker-host cleanup run
workyard ui --open
workyard upgrade
workyard version
workyard completion zsh
```

Service/run commands commonly support:

- `--worker user@worker-host`
- `--run <run-id>`
- `--project <path>`
- `--json`
- `--verbose`

Mirror commands use the worker stored in each mirror record after `workyard mirror setup`; use `workyard mirror list` to see the mapping.

## Security Notes

Workyard is designed to stay private by default:

- It uses SSH over a private network such as Tailscale.
- The worker daemon listens on a Unix socket, not public TCP.
- The dashboard is loopback-only.
- Public and link-local health URLs are rejected.
- Service logs are not published by the dashboard.
- `.env` files are excluded from sync by default.
- Runtime ports are injected into process environments instead of mutating `.env` files.
- Worker paths are validated before running, syncing, reading logs, or deleting files.

## License

Workyard is released under the [MIT License](LICENSE).

## Development

Run tests:

```sh
go test ./...
```

Run vet:

```sh
go vet ./...
```

Build both local and Linux ARM64 binaries:

```sh
go build -o dist/workyard-darwin-arm64 ./cmd/workyard
GOOS=linux GOARCH=arm64 go build -o dist/workyard-linux-arm64 ./cmd/workyard
```

`workyard install` and `workyard workers setup` build the matching artifact automatically when `dist/` is missing it and the Go toolchain is available.

## Exit Codes

Errors exit with a class so scripts and agents can branch on the kind of failure: `1` generic, `2` usage or configuration, `3` SSH/Tailscale connectivity, `4` worker daemon, `5` health/wait timeout. See [docs/errors.md](docs/errors.md) for the code-by-code mapping.

Try the bundled fixture:

```sh
workyard --project fixtures/health-server config check
workyard --project fixtures/health-server services
```

With a worker:

```sh
workyard deploy fixtures/health-server --worker user@worker-host --fresh
workyard --project fixtures/health-server --worker user@worker-host status
workyard --project fixtures/health-server --worker user@worker-host stop
```

## Status

Workyard is early and intentionally local-first. It is useful today for private mirrored remote workspaces, synthetic service fixtures, and agent-friendly inspection of running services.

## License

License TBD.
