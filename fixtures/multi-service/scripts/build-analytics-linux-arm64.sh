#!/usr/bin/env sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
OUT="$ROOT/services/analytics/bin/analytics-linux-arm64"

mkdir -p "$(dirname -- "$OUT")"
cd "$ROOT/services/analytics"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$OUT" .
chmod 755 "$OUT"
