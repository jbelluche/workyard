#!/usr/bin/env bash
set -euo pipefail

# Local development installer for macOS and Linux.

detect_shell_rc() {
  case "${SHELL:-}" in
    */zsh) printf '%s' "$HOME/.zshrc" ;;
    */bash) printf '%s' "$HOME/.bashrc" ;;
    *) printf '' ;;
  esac
}

INSTALL_DIR="${WORKYARD_INSTALL_DIR:-$HOME/.local/bin}"
SHELL_RC="${WORKYARD_SHELL_RC:-$(detect_shell_rc)}"
UPDATE_SHELL=1

usage() {
  cat <<'USAGE'
Usage: scripts/local/install.sh [--install-dir DIR] [--no-shell-update]

Builds Workyard from this checkout and installs it as DIR/workyard.

Options:
  --install-dir DIR     Install directory. Defaults to ~/.local/bin.
  --no-shell-update     Do not add the install directory to the shell profile.
  -h, --help            Show this help.

Environment:
  WORKYARD_INSTALL_DIR  Default install directory override.
  WORKYARD_SHELL_RC     Shell profile to update. Detected from $SHELL
                        (~/.zshrc for zsh, ~/.bashrc for bash).
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)
      [[ $# -ge 2 ]] || { printf 'missing value for --install-dir\n' >&2; exit 2; }
      INSTALL_DIR="$2"
      shift 2
      ;;
    --no-shell-update)
      UPDATE_SHELL=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$(uname -s)" in
  Darwin|Linux) ;;
  *)
    printf 'scripts/local/install.sh supports macOS and Linux only.\n' >&2
    exit 1
    ;;
esac

if [[ -z "${HOME:-}" || ! -d "$HOME" ]]; then
  printf 'HOME is not set to a valid directory\n' >&2
  exit 1
fi

if [[ "$INSTALL_DIR" != /* ]]; then
  printf 'install directory must be absolute: %s\n' "$INSTALL_DIR" >&2
  exit 1
fi

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd -- "$SCRIPT_DIR/../.." && pwd -P)"

if [[ ! -f "$REPO_ROOT/go.mod" || ! -f "$REPO_ROOT/cmd/workyard/main.go" ]]; then
  printf 'script must be run from a valid Workyard checkout: %s\n' "$REPO_ROOT" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  printf 'go is required but was not found on PATH\n' >&2
  exit 1
fi

mkdir -p "$INSTALL_DIR"
INSTALL_DIR_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
HOME_REAL="$(cd -- "$HOME" && pwd -P)"

case "$INSTALL_DIR_REAL" in
  "$HOME_REAL"/*) ;;
  *)
    printf 'refusing to install outside HOME: %s\n' "$INSTALL_DIR_REAL" >&2
    printf 'choose a directory under %s, such as %s/.local/bin\n' "$HOME_REAL" "$HOME_REAL" >&2
    exit 1
    ;;
esac

if [[ ! -w "$INSTALL_DIR_REAL" ]]; then
  printf 'install directory is not writable: %s\n' "$INSTALL_DIR_REAL" >&2
  exit 1
fi

DEST="$INSTALL_DIR_REAL/workyard"
if [[ -L "$DEST" ]]; then
  printf 'refusing to overwrite symlink: %s\n' "$DEST" >&2
  exit 1
fi
if [[ -e "$DEST" && ! -f "$DEST" ]]; then
  printf 'refusing to overwrite non-regular file: %s\n' "$DEST" >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

printf 'building workyard from %s\n' "$REPO_ROOT"
(
  cd "$REPO_ROOT"
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || printf '0.1.0')"
  go build -ldflags "-X github.com/jackbelluche/workyard/internal/cli.Version=${VERSION}" -o "$TMP_DIR/workyard" ./cmd/workyard
)

if [[ ! -x "$TMP_DIR/workyard" ]]; then
  printf 'built binary is not executable\n' >&2
  exit 1
fi

"$TMP_DIR/workyard" version >/dev/null
install -m 755 "$TMP_DIR/workyard" "$DEST"

if [[ "$UPDATE_SHELL" -eq 1 ]]; then
  if [[ -n "$SHELL_RC" ]]; then
    touch "$SHELL_RC"
    chmod go-rwx "$SHELL_RC" 2>/dev/null || true
    if ! grep -Fq '# >>> workyard local install >>>' "$SHELL_RC"; then
      {
        printf '\n# >>> workyard local install >>>\n'
        printf 'export PATH="%s:$PATH"\n' "$INSTALL_DIR_REAL"
        printf '# <<< workyard local install <<<\n'
      } >> "$SHELL_RC"
      printf 'added %s to PATH in %s\n' "$INSTALL_DIR_REAL" "$SHELL_RC"
      printf 'restart your shell (or source %s) to pick up the new PATH\n' "$SHELL_RC"
    fi
  else
    case "${SHELL:-}" in
      */fish)
        printf 'add the install directory to fish with:\n  fish_add_path %s\n' "$INSTALL_DIR_REAL"
        ;;
      *)
        printf 'add the install directory to your shell profile manually:\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR_REAL"
        ;;
    esac
  fi
fi

export PATH="$INSTALL_DIR_REAL:$PATH"
RESOLVED="$(command -v workyard || true)"
if [[ -z "$RESOLVED" ]]; then
  printf 'installed workyard to %s, but it is not currently on PATH\n' "$DEST" >&2
  exit 1
fi

RESOLVED_DIR="$(cd -- "$(dirname -- "$RESOLVED")" && pwd -P)"
RESOLVED_REAL="$RESOLVED_DIR/$(basename -- "$RESOLVED")"
if [[ "$RESOLVED_REAL" != "$DEST" ]]; then
  printf 'installed workyard to %s\n' "$DEST"
  printf 'warning: command -v workyard resolves to %s; restart your shell or move %s earlier in PATH\n' "$RESOLVED" "$INSTALL_DIR_REAL" >&2
else
  printf 'installed workyard to %s\n' "$DEST"
fi

workyard version
