#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
GO_BIN=$("$ROOT/scripts/ensure-go.sh")
OUT="$ROOT/services/analytics/bin/analytics-linux-arm64"

mkdir -p "$(dirname -- "$OUT")"
cd "$ROOT/services/analytics"
CGO_ENABLED=0 "$GO_BIN" build -o "$OUT" .
chmod 755 "$OUT"

"$ROOT/scripts/verify-runtime.sh"
