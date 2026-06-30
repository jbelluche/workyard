# Workyard Testing

Workyard uses layered tests so fast package checks and real CLI behavior both stay covered.

## Local Suite

Run the full local suite with:

```sh
go test ./...
```

This includes:

- Package/unit tests under `internal/...`.
- Binary-level CLI integration tests under `integration/local`.
- A localhost service lifecycle smoke test using `fixtures/health-server`.
- Fake-SSH mirror failure tests for dirty destinations, stale PID repair, bad config, missing workers, and missing tmux.

The local integration tests build `cmd/workyard`, run the binary in an isolated temporary `HOME` and `--state-dir`, and verify real command output, JSON contracts, registry files, daemon behavior, and cleanup. Tests that need external local tools such as `rsync` or `python3` skip when those tools are unavailable.

To rerun only the heavier local CLI checks:

```sh
go test ./integration/local -count=1
```

## Remote Suite

Remote tests are opt-in and must only use Workyard-owned fixtures. By default they target the Raspberry Pi test worker:

```text
jack@jack-rasp-five
```

Run them with:

```sh
WORKYARD_REMOTE_INTEGRATION=1 go test ./integration/remote -count=1
```

Override the worker with:

```sh
WORKYARD_REMOTE_INTEGRATION=1 WORKYARD_REMOTE_WORKER=jack@jack-r5-16gb go test ./integration/remote -count=1
```

The remote suite currently covers:

- Deploying `fixtures/health-server` to the worker with `--install`.
- Starting the service through the worker daemon and checking status/URLs.
- Setting up a mirror for `fixtures/health-server`.
- Running mirror sync, marker verification, mirror shell/exec, mirror doctor, tmux list/kill behavior, background start/status/stop, and mirror delete with remote cleanup.
- Starting two simultaneous five-service `fixtures/multi-service` mirrored stacks on one worker and verifying port conflicts are resolved with different assigned ports.

Remote test paths are constrained to the worker's `~/.workyard/runs/` tree. Do not point these tests at private repositories or non-fixture workspaces.
