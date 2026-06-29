#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)

"$ROOT/scripts/run-bun.sh" --version >/dev/null

if [ ! -x "$ROOT/services/analytics/bin/analytics-linux-arm64" ]; then
  printf '%s\n' "missing services/analytics/bin/analytics-linux-arm64; run bun run build:analytics locally before deploy" >&2
  exit 1
fi
