#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

if ! "$ROOT/scripts/run-bun.sh" --version >/dev/null 2>&1; then
  if ! command -v curl >/dev/null 2>&1; then
    printf '%s\n' "curl is required to install Bun on this worker" >&2
    exit 1
  fi
  curl -fsSL https://bun.sh/install | bash
fi

cd "$ROOT"
"$ROOT/scripts/run-bun.sh" install --frozen-lockfile
