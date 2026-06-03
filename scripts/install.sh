#!/usr/bin/env sh
set -eu

REPO="${WORKYARD_REPO:-jackbelluche/workyard}"
VERSION="${WORKYARD_VERSION:-latest}"
INSTALL_DIR="${WORKYARD_INSTALL_DIR:-$HOME/.local/bin}"

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

artifact="workyard-${os}-${arch}.tar.gz"
base_url="https://github.com/${REPO}/releases"
if [ "$VERSION" = "latest" ]; then
  url="${base_url}/latest/download/${artifact}"
  checksums_url="${base_url}/latest/download/checksums.txt"
else
  url="${base_url}/download/${VERSION}/${artifact}"
  checksums_url="${base_url}/download/${VERSION}/checksums.txt"
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

curl -fsSL "$url" -o "$tmp_dir/$artifact"
if curl -fsSL "$checksums_url" -o "$tmp_dir/checksums.txt"; then
  expected=$(awk -v f="$artifact" '$2 == f { print $1 }' "$tmp_dir/checksums.txt")
  if [ -z "$expected" ]; then
    printf 'checksum for %s not found\n' "$artifact" >&2
    exit 1
  fi
  if command -v shasum >/dev/null 2>&1; then
    actual=$(shasum -a 256 "$tmp_dir/$artifact" | awk '{ print $1 }')
  else
    actual=$(sha256sum "$tmp_dir/$artifact" | awk '{ print $1 }')
  fi
  if [ "$actual" != "$expected" ]; then
    printf 'checksum mismatch for %s\n' "$artifact" >&2
    exit 1
  fi
fi

tar -C "$tmp_dir" -xzf "$tmp_dir/$artifact"
mkdir -p "$INSTALL_DIR"
install -m 755 "$tmp_dir/workyard-${os}-${arch}" "$INSTALL_DIR/workyard"
printf 'installed workyard to %s/workyard\n' "$INSTALL_DIR"
