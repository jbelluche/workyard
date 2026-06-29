#!/usr/bin/env sh
set -eu

GO_VERSION=${WORKYARD_FIXTURE_GO_VERSION:-1.25.11}
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
MACHINE=$(uname -m)

case "$OS" in
  linux) ;;
  *)
    if command -v go >/dev/null 2>&1; then
      command -v go
      exit 0
    fi
    printf '%s\n' "unsupported Go bootstrap OS: $OS" >&2
    exit 1
    ;;
esac

case "$MACHINE" in
  aarch64|arm64) ARCH=arm64 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *)
    printf '%s\n' "unsupported Go bootstrap architecture: $MACHINE" >&2
    exit 1
    ;;
esac

if command -v go >/dev/null 2>&1; then
  command -v go
  exit 0
fi

ROOT="$HOME/.workyard/toolchains/go${GO_VERSION}"
GO_BIN="$ROOT/bin/go"
if [ -x "$GO_BIN" ]; then
  printf '%s\n' "$GO_BIN"
  exit 0
fi

if ! command -v curl >/dev/null 2>&1; then
  printf '%s\n' "curl is required to install Go on this worker" >&2
  exit 1
fi
if ! command -v tar >/dev/null 2>&1; then
  printf '%s\n' "tar is required to install Go on this worker" >&2
  exit 1
fi

mkdir -p "$HOME/.workyard/toolchains"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

URL="https://go.dev/dl/go${GO_VERSION}.${OS}-${ARCH}.tar.gz"
curl -fsSL "$URL" -o "$TMP/go.tgz"
tar -C "$TMP" -xzf "$TMP/go.tgz"
rm -rf "$ROOT.tmp"
mv "$TMP/go" "$ROOT.tmp"
rm -rf "$ROOT"
mv "$ROOT.tmp" "$ROOT"

printf '%s\n' "$GO_BIN"
