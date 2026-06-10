# Workyard Error Codes and Exit Classes

Every CLI error carries a stable code (shown in `--json` output as
`error.code`) and exits with a class so scripts and agents can branch on the
kind of failure without parsing messages.

## Exit classes

| Exit code | Class | Meaning |
|---|---|---|
| 0 | success | Command completed |
| 1 | generic | Unclassified failure |
| 2 | usage | Bad arguments, invalid or missing configuration |
| 3 | connectivity | SSH or Tailscale failure |
| 4 | daemon | Worker daemon unreachable or daemon-reported failure |
| 5 | wait | Health or status wait timed out |

The mapping lives in `internal/output/output.go` (`ExitCodeFor`). Codes not
listed below exit 1 unless they match a class pattern (`CONFIG_*`,
`DEPLOY_ARGS*`, and `*_INVALID` exit 2).

## Common codes

| Code | Class | Emitted when |
|---|---|---|
| `WORKER_REQUIRED` | usage | A command needs `--worker`; the hint lists registered workers |
| `SERVICE_UNKNOWN` | usage | A named service is not in `workyard.yaml`; the hint lists valid names |
| `SERVICE_SELECTION_FAILED` | usage | The daemon rejected a service selection |
| `CONFIG_LOAD_FAILED` / `CONFIG_INVALID` | usage | `workyard.yaml` is missing or fails validation |
| `CONFIG_EXISTS` | usage | `workyard init` would overwrite an existing config |
| `RUN_ID_INVALID` / `TIMEOUT_INVALID` / `REMOTE_PATH_INVALID` | usage | A flag value failed validation |
| `RUN_AMBIGUOUS` | usage | No local config and multiple registered runs match; the hint enumerates them |
| `DEPLOY_ARGS_INVALID` | usage | Deploy positional arguments are ambiguous or point at a missing path |
| `SSH_FAILED` | connectivity | An SSH connection to the worker failed |
| `REMOTE_COMMAND_FAILED` | connectivity | A remote command failed without printing its own error |
| `WORKER_PLATFORM_FAILED` | connectivity | The worker OS/architecture could not be detected |
| `TAILSCALE_DISCOVER_FAILED` | connectivity | `tailscale status` failed or is unavailable |
| `DAEMON_UNREACHABLE` | daemon | Nothing is listening on the daemon socket |
| `DAEMON_START_FAILED` / `DAEMON_STOP_FAILED` | daemon | The daemon could not be started or stopped |
| `WAIT_TIMEOUT` | wait | `workyard wait` (or deploy's wait step) hit its timeout |
| `WORKER_ARTIFACT_MISSING` | generic | No matching binary artifact and no way to build one |
| `WORKER_INSTALL_FAILED` | generic | Uploading or verifying the worker binary failed |

Daemon-originated errors pass through with their own code, message, and hint;
only transport failures are rewritten by the CLI.
