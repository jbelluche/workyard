#!/usr/bin/env sh
set -eu

if [ -x "$HOME/.bun/bin/bun" ]; then
  exec "$HOME/.bun/bin/bun" "$@"
fi

if command -v bun >/dev/null 2>&1; then
  exec bun "$@"
fi

printf '%s\n' "bun is not installed. Run workyard setup for this fixture first." >&2
exit 127
