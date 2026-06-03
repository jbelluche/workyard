#!/usr/bin/env bash
set -euo pipefail

# macOS-only local development uninstaller.
# Linux support can be added later by expanding the platform checks and shell
# profile handling.

INSTALL_DIR="${WORKYARD_INSTALL_DIR:-$HOME/.local/bin}"
SHELL_RC="${WORKYARD_SHELL_RC:-$HOME/.zshrc}"
REMOVE_SHELL=1
FORCE=0

usage() {
  cat <<'USAGE'
Usage: scripts/local/uninstall.sh [--install-dir DIR] [--keep-shell-profile] [--force]

Removes DIR/workyard when it was installed by the local Workyard installer.

Options:
  --install-dir DIR        Install directory. Defaults to ~/.local/bin.
  --keep-shell-profile     Keep the Workyard PATH block in ~/.zshrc.
  --force                  Remove DIR/workyard even if version verification fails.
  -h, --help               Show this help.

Environment:
  WORKYARD_INSTALL_DIR     Default install directory override.
  WORKYARD_SHELL_RC        Shell profile to update. Defaults to ~/.zshrc.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install-dir)
      [[ $# -ge 2 ]] || { printf 'missing value for --install-dir\n' >&2; exit 2; }
      INSTALL_DIR="$2"
      shift 2
      ;;
    --keep-shell-profile)
      REMOVE_SHELL=0
      shift
      ;;
    --force)
      FORCE=1
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

if [[ "$(uname -s)" != "Darwin" ]]; then
  printf 'scripts/local/uninstall.sh currently supports macOS only. Linux support can be added later.\n' >&2
  exit 1
fi

if [[ -z "${HOME:-}" || ! -d "$HOME" ]]; then
  printf 'HOME is not set to a valid directory\n' >&2
  exit 1
fi

if [[ "$INSTALL_DIR" != /* ]]; then
  printf 'install directory must be absolute: %s\n' "$INSTALL_DIR" >&2
  exit 1
fi

if [[ ! -d "$INSTALL_DIR" ]]; then
  printf 'install directory does not exist: %s\n' "$INSTALL_DIR"
  exit 0
fi

INSTALL_DIR_REAL="$(cd -- "$INSTALL_DIR" && pwd -P)"
HOME_REAL="$(cd -- "$HOME" && pwd -P)"

case "$INSTALL_DIR_REAL" in
  "$HOME_REAL"/*) ;;
  *)
    printf 'refusing to uninstall outside HOME: %s\n' "$INSTALL_DIR_REAL" >&2
    exit 1
    ;;
esac

DEST="$INSTALL_DIR_REAL/workyard"
if [[ ! -e "$DEST" ]]; then
  printf 'workyard is not installed at %s\n' "$DEST"
else
  if [[ -L "$DEST" ]]; then
    printf 'refusing to remove symlink: %s\n' "$DEST" >&2
    printf 'pass --force if you really want to remove it\n' >&2
    [[ "$FORCE" -eq 1 ]] || exit 1
  elif [[ ! -f "$DEST" ]]; then
    printf 'refusing to remove non-regular file: %s\n' "$DEST" >&2
    exit 1
  fi

  if [[ "$FORCE" -ne 1 ]]; then
    if [[ ! -x "$DEST" ]] || ! "$DEST" version >/dev/null 2>&1; then
      printf 'refusing to remove %s because it does not look like a runnable workyard binary\n' "$DEST" >&2
      printf 'pass --force if you really want to remove it\n' >&2
      exit 1
    fi
  fi

  rm -f "$DEST"
  printf 'removed %s\n' "$DEST"
fi

if [[ "$REMOVE_SHELL" -eq 1 && -f "$SHELL_RC" ]]; then
  if grep -Fq '# >>> workyard local install >>>' "$SHELL_RC"; then
    TMP_FILE="$(mktemp)"
    awk '
      /^# >>> workyard local install >>>$/ { skip = 1; next }
      /^# <<< workyard local install <<<$/{ skip = 0; next }
      !skip { print }
    ' "$SHELL_RC" > "$TMP_FILE"
    cat "$TMP_FILE" > "$SHELL_RC"
    rm -f "$TMP_FILE"
    printf 'removed Workyard PATH block from %s\n' "$SHELL_RC"
  fi
fi

if command -v workyard >/dev/null 2>&1; then
  printf 'note: workyard still resolves to %s\n' "$(command -v workyard)"
fi
