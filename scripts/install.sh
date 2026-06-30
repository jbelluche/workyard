#!/usr/bin/env sh
set -eu

REPO="${WORKYARD_REPO:-jbelluche/workyard}"
VERSION="${WORKYARD_VERSION:-latest}"
REF="${WORKYARD_REF:-}"
METHOD="${WORKYARD_INSTALL_METHOD:-auto}"
INSTALL_DIR="${WORKYARD_INSTALL_DIR:-$HOME/.local/bin}"
SHELL_RC="${WORKYARD_SHELL_RC:-}"
UPDATE_SHELL=1

usage() {
  cat <<'USAGE'
Usage: install.sh [--install-dir DIR] [--version VERSION] [--repo OWNER/REPO]
                  [--method auto|release|source] [--ref REF] [--no-shell-update]

Installs Workyard without requiring a local git checkout.

The default method downloads a release artifact when one exists and falls back
to building from a public source archive when Go is available.

Options:
  --install-dir DIR     Install directory. Defaults to ~/.local/bin.
  --version VERSION     Release version/tag to install. Defaults to latest.
  --repo OWNER/REPO     GitHub repository. Defaults to jbelluche/workyard.
  --method METHOD       auto, release, or source. Defaults to auto.
  --ref REF             Source ref for --method source. Defaults to main for
                        latest, otherwise the requested version/tag.
  --no-shell-update     Do not add the install directory to the shell profile.
  -h, --help            Show this help.

Environment:
  WORKYARD_REPO
  WORKYARD_VERSION
  WORKYARD_REF
  WORKYARD_INSTALL_METHOD
  WORKYARD_INSTALL_DIR
  WORKYARD_SHELL_RC
USAGE
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir)
      [ "$#" -ge 2 ] || { printf 'missing value for --install-dir\n' >&2; exit 2; }
      INSTALL_DIR="$2"
      shift 2
      ;;
    --version)
      [ "$#" -ge 2 ] || { printf 'missing value for --version\n' >&2; exit 2; }
      VERSION="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || { printf 'missing value for --repo\n' >&2; exit 2; }
      REPO="$2"
      shift 2
      ;;
    --method)
      [ "$#" -ge 2 ] || { printf 'missing value for --method\n' >&2; exit 2; }
      METHOD="$2"
      shift 2
      ;;
    --ref)
      [ "$#" -ge 2 ] || { printf 'missing value for --ref\n' >&2; exit 2; }
      REF="$2"
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

case "$METHOD" in
  auto|release|source) ;;
  *) printf 'install method must be auto, release, or source\n' >&2; exit 2 ;;
esac

if [ -z "${HOME:-}" ] || [ ! -d "$HOME" ]; then
  printf 'HOME is not set to a valid directory\n' >&2
  exit 1
fi

case "$INSTALL_DIR" in
  /*) ;;
  *) printf 'install directory must be absolute: %s\n' "$INSTALL_DIR" >&2; exit 1 ;;
esac

os=$(uname -s | tr '[:upper:]' '[:lower:]')
machine=$(uname -m | tr '[:upper:]' '[:lower:]')

case "$os" in
  darwin|linux) ;;
  *) printf 'unsupported OS: %s\n' "$os" >&2; exit 1 ;;
esac

case "$machine" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) printf 'unsupported architecture: %s\n' "$machine" >&2; exit 1 ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  printf 'curl is required to install Workyard\n' >&2
  exit 1
fi
if ! command -v tar >/dev/null 2>&1; then
  printf 'tar is required to install Workyard\n' >&2
  exit 1
fi

tmp_dir=$(mktemp -d)
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

detect_shell_rc() {
  if [ -n "$SHELL_RC" ]; then
    printf '%s' "$SHELL_RC"
    return
  fi
  case "${SHELL:-}" in
    */zsh) printf '%s' "$HOME/.zshrc" ;;
    */bash) printf '%s' "$HOME/.bashrc" ;;
    *) printf '' ;;
  esac
}

prepare_install_dir() {
  mkdir -p "$INSTALL_DIR"
  install_dir_real=$(cd "$INSTALL_DIR" && pwd -P)
  home_real=$(cd "$HOME" && pwd -P)
  case "$install_dir_real" in
    "$home_real"/*) ;;
    *)
      printf 'refusing to install outside HOME: %s\n' "$install_dir_real" >&2
      printf 'choose a directory under %s, such as %s/.local/bin\n' "$home_real" "$home_real" >&2
      exit 1
      ;;
  esac
  if [ ! -w "$install_dir_real" ]; then
    printf 'install directory is not writable: %s\n' "$install_dir_real" >&2
    exit 1
  fi
  dest="$install_dir_real/workyard"
  if [ -L "$dest" ]; then
    printf 'refusing to overwrite symlink: %s\n' "$dest" >&2
    exit 1
  fi
  if [ -e "$dest" ] && [ ! -f "$dest" ]; then
    printf 'refusing to overwrite non-regular file: %s\n' "$dest" >&2
    exit 1
  fi
}

download_release() {
  artifact="workyard-${os}-${arch}.tar.gz"
  base_url="https://github.com/${REPO}/releases"
  if [ "$VERSION" = "latest" ]; then
    url="${base_url}/latest/download/${artifact}"
    checksums_url="${base_url}/latest/download/checksums.txt"
  else
    url="${base_url}/download/${VERSION}/${artifact}"
    checksums_url="${base_url}/download/${VERSION}/checksums.txt"
  fi

  printf 'downloading %s\n' "$url" >&2
  if ! curl -fsSL "$url" -o "$tmp_dir/$artifact"; then
    return 1
  fi

  if curl -fsSL "$checksums_url" -o "$tmp_dir/checksums.txt"; then
    expected=$(awk -v f="$artifact" '$2 == f { print $1 }' "$tmp_dir/checksums.txt")
    if [ -z "$expected" ]; then
      printf 'checksum for %s not found\n' "$artifact" >&2
      return 1
    fi
    if command -v shasum >/dev/null 2>&1; then
      actual=$(shasum -a 256 "$tmp_dir/$artifact" | awk '{ print $1 }')
    elif command -v sha256sum >/dev/null 2>&1; then
      actual=$(sha256sum "$tmp_dir/$artifact" | awk '{ print $1 }')
    else
      printf 'shasum or sha256sum is required to verify release checksums\n' >&2
      return 1
    fi
    if [ "$actual" != "$expected" ]; then
      printf 'checksum mismatch for %s\n' "$artifact" >&2
      return 1
    fi
  else
    printf 'warning: checksums.txt was unavailable; installing unverified release artifact\n' >&2
  fi

  tar -C "$tmp_dir" -xzf "$tmp_dir/$artifact"
  if [ ! -x "$tmp_dir/workyard-${os}-${arch}" ]; then
    printf 'release archive did not contain executable %s\n' "workyard-${os}-${arch}" >&2
    return 1
  fi
  cp "$tmp_dir/workyard-${os}-${arch}" "$tmp_dir/workyard"
}

build_from_source() {
  if ! command -v go >/dev/null 2>&1; then
    printf 'go is required to build Workyard from source\n' >&2
    return 1
  fi
  source_ref="$REF"
  if [ -z "$source_ref" ]; then
    if [ "$VERSION" = "latest" ]; then
      source_ref="main"
    else
      source_ref="$VERSION"
    fi
  fi
  source_url="https://codeload.github.com/${REPO}/tar.gz/${source_ref}"
  printf 'building Workyard from %s\n' "$source_url" >&2
  if ! curl -fsSL "$source_url" -o "$tmp_dir/source.tar.gz"; then
    return 1
  fi
  tar -C "$tmp_dir" -xzf "$tmp_dir/source.tar.gz"
  source_dir=$(find "$tmp_dir" -mindepth 1 -maxdepth 1 -type d | head -n 1)
  if [ -z "$source_dir" ] || [ ! -f "$source_dir/go.mod" ]; then
    printf 'source archive did not contain a Go module\n' >&2
    return 1
  fi
  version_label="$VERSION"
  if [ "$version_label" = "latest" ]; then
    version_label="$source_ref"
  fi
  (
    cd "$source_dir"
    go build -trimpath -ldflags "-X github.com/jackbelluche/workyard/internal/cli.Version=${version_label}" -o "$tmp_dir/workyard" ./cmd/workyard
  )
  if [ ! -x "$tmp_dir/workyard" ]; then
    printf 'built binary is not executable\n' >&2
    return 1
  fi
}

install_binary() {
  prepare_install_dir
  tmp_dest="$install_dir_real/.workyard-install-$$"
  rm -f "$tmp_dest"
  if ! install -m 755 "$tmp_dir/workyard" "$tmp_dest"; then
    rm -f "$tmp_dest"
    exit 1
  fi
  if ! "$tmp_dest" version >/dev/null; then
    rm -f "$tmp_dest"
    exit 1
  fi
  if ! mv -f "$tmp_dest" "$dest"; then
    rm -f "$tmp_dest"
    exit 1
  fi
  "$dest" version >/dev/null
}

update_shell_path() {
  [ "$UPDATE_SHELL" -eq 1 ] || return 0
  case ":${PATH:-}:" in
    *":$install_dir_real:"*) return 0 ;;
  esac
  rc=$(detect_shell_rc)
  if [ -n "$rc" ]; then
    touch "$rc"
    chmod go-rwx "$rc" 2>/dev/null || true
    if ! grep -Fq '# >>> workyard install >>>' "$rc"; then
      {
        printf '\n# >>> workyard install >>>\n'
        printf 'export PATH="%s:$PATH"\n' "$install_dir_real"
        printf '# <<< workyard install <<<\n'
      } >> "$rc"
      printf 'added %s to PATH in %s\n' "$install_dir_real" "$rc"
      printf 'restart your shell (or source %s) to pick up the new PATH\n' "$rc"
    fi
  else
    printf 'add Workyard to PATH with:\n  export PATH="%s:$PATH"\n' "$install_dir_real"
  fi
}

installed=0
case "$METHOD" in
  release)
    download_release
    installed=1
    ;;
  source)
    build_from_source
    installed=1
    ;;
  auto)
    if download_release; then
      installed=1
    else
      printf 'release artifact unavailable; falling back to source build\n' >&2
      build_from_source
      installed=1
    fi
    ;;
esac

[ "$installed" -eq 1 ] || exit 1
install_binary
update_shell_path

printf 'installed workyard to %s\n' "$dest"
"$dest" version
