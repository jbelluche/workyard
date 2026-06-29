# Workyard

Workyard is an agent-first remote development runner. It syncs a local project to a private worker over SSH, starts configured services through a worker daemon, tracks health and lifecycle events, and exposes a local dashboard for monitoring what is running.

Workyard never infers a target machine: every command names its worker with `--worker`, so a user or agent cannot act on the wrong machine by accident. Use `--worker localhost` for this machine or a registered worker name (shell completion offers both).

> **Warning**
> Workyard should only be installed and run on machines you trust. It syncs project files, starts configured commands, manages local/remote processes, and reads bounded logs over SSH.

## Install

For the current private repository, clone over SSH and run the local macOS installer:

```sh
git clone git@github.com:jbelluche/workyard.git
cd workyard
scripts/local/install.sh
```

The local installer supports macOS and Linux. It builds Workyard from source, installs it to `~/.local/bin/workyard`, and adds a marked PATH block to your shell profile (`~/.zshrc` or `~/.bashrc`) when needed.

Uninstall the local binary:

```sh
scripts/local/uninstall.sh
```

The default model is private and local:

- Project files sync with `rsync` over SSH.
- Remote control happens through a private Unix socket on the worker.
- Preview URLs stay on your private network, such as Tailscale.
- The monitor dashboard listens on loopback only.
- Logs are read through bounded CLI commands, not exposed publicly.

## Requirements

- Go 1.25 or newer
- `rsync`
- `ssh`
- Tailscale installed, running, and connected
- A worker machine reachable over SSH, usually through Tailscale

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

Create a config:

```sh
workyard init
```

Validate the config:

```sh
workyard config check
```

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

You can still run individual lower-level commands:

```sh
workyard --worker user@worker-host sync
workyard --worker user@worker-host setup
workyard --worker user@worker-host build
workyard --worker user@worker-host start web
```

Inspect status and URLs:

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
workyard workers add jack-r5-16gb --user jack
workyard workers config show
workyard workers list
```

Registered workers are stored in `~/.workyard/local/workers.yaml`. The normal editable fields are `name`, `host`, and `user`; Workyard connects with `user@host` unless `sshTarget` is set as an explicit override.

```yaml
workers:
  - name: jack-r5-16gb
    host: jack-r5-16gb
    user: jack
    source: tailscale
    dnsName: jack-r5-16gb.tailnet.ts.net
```

Registered names can be used anywhere `--worker` is accepted:

```sh
workyard deploy . --worker jack-r5-16gb --fresh
```

Bootstrap a reachable SSH machine as a Workyard-ready worker:

```sh
workyard workers setup jack-r5-16gb --config workyard.bootstrap.yaml
```

The setup command can create private Workyard directories, repair `~/.workyard/runs` permissions, build and upload the correct worker binary, start or restart the worker daemon, optionally install apt packages or Docker, register the worker locally, and run a final worker doctor check.

Example `workyard.bootstrap.yaml`:

```yaml
version: 1

workers:
  jack-r5-16gb:
    ssh:
      user: jack
      host: jack-r5-16gb

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
workyard workers setup jack-r5-16gb --config workyard.bootstrap.yaml --dry-run
```

By default, setup uses non-interactive SSH and non-interactive `sudo -n`; if a privileged install needs a password, Workyard stops and prints the manual command to run on the worker. For trusted workers, you can opt into a local hidden sudo prompt:

```sh
workyard workers setup jack-r5-16gb --config workyard.bootstrap.yaml --ask-sudo-password
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
workyard workers remove jack-r5-16gb
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

## Secrets Model

Workyard treats `.env` files as sensitive inputs. By default, sync excludes `.env`, `.env.local`, and `.env.*.local`; set `sync.includeEnvFiles: true` only for fixtures or worker environments where those files are intentionally copied. Workyard never edits `.env` files to resolve port conflicts. Assigned ports are injected into process environments through `WORKYARD_PORT` and the configured `port.env` name.

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

## Mirror Mode

Mirror mode keeps registered local directories reflected on workers without requiring a `workyard.yaml` service config. It is useful when you want to SSH into a Tailscale-connected device and find a normal workspace directory with your latest local edits.

Configure a mirror with the wizard:

```sh
workyard mirror setup
```

The wizard asks for:

- Local directory, defaulting to the current directory.
- Registered worker.
- Remote destination, defaulting to `~/workspace/<directory-name>`.
- Confirmation before writing the local mirror registry.

Run all enabled mirrors in the current terminal:

```sh
workyard mirror
```

Press `Ctrl-C` to stop foreground mirroring. Start and stop background mirroring with:

```sh
workyard mirror start
workyard mirror status
workyard mirror stop
```

List and delete configured mirrors:

```sh
workyard mirror list
workyard mirror delete <name>
```

`mirror setup` requires the remote destination to be missing, empty, or already marked as the same Workyard mirror. Use `--force` only when you intentionally want to mirror into a non-empty directory. Mirror syncs use default excludes such as `node_modules`, build outputs, logs, and `.env` files; `.git` is included by default so the remote workspace behaves like a clone.

Deleting a mirror only removes the local registry record by default. To remove the remote files too, pass `--delete-remote`; Workyard will refuse unless the destination contains a matching `.workyard-mirror.json` marker written by mirror sync:

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

Build GitHub/Homebrew-ready release artifacts:

```sh
VERSION=0.1.0 scripts/build-release.sh
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

End-user install script, once releases are published from a public or otherwise accessible repository:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | sh
```

Set `WORKYARD_REPO`, `WORKYARD_VERSION`, or `WORKYARD_INSTALL_DIR` before running the script when installing from a non-default release location. Homebrew formulas can reference the tarball URL and matching SHA-256 from `checksums.txt`.

## Command Reference

Project commands:

```sh
workyard init
workyard config check
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

Dependency, install, and daemon commands:

```sh
workyard doctor
workyard --worker user@worker-host doctor
workyard --worker user@worker-host install
workyard daemon start
workyard daemon status
workyard daemon stop
```

Worker and monitor registry commands:

```sh
workyard workers discover
workyard workers add jack-r5-16gb --user jack
workyard workers setup jack-r5-16gb --config workyard.bootstrap.yaml
workyard workers config show
workyard workers list
workyard workers remove jack-r5-16gb
workyard runs list
workyard runs remove user@worker-host my-project feature-branch
workyard runs prune --older-than 168h
```

Cleanup and utility commands:

```sh
workyard --worker user@worker-host cleanup logs
workyard --worker user@worker-host cleanup run
workyard ui --open
workyard version
workyard completion zsh
```

Most commands support:

- `--worker user@worker-host`
- `--run <run-id>`
- `--project <path>`
- `--json`
- `--verbose`

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

Workyard is early and intentionally local-first. It is useful today for private remote development workflows, synthetic service fixtures, and agent-friendly inspection of running services.

## License

License TBD.
