#!/usr/bin/env sh
set -eu

VERSION="${VERSION:-v0.1.0}"
OUT_DIR="${OUT_DIR:-dist/release}"
LDFLAGS="-s -w -X github.com/jackbelluche/workyard/internal/cli.Version=${VERSION}"

if ! printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  printf 'VERSION must look like v0.1.0 or v0.1.0-rc.1, got: %s\n' "$VERSION" >&2
  exit 2
fi

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

targets='darwin amd64
darwin arm64
linux amd64
linux arm64'

printf '%s\n' "$targets" | while read -r os arch; do
  [ -n "$os" ] || continue
  name="workyard-${os}-${arch}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags="$LDFLAGS" -o "$OUT_DIR/$name" ./cmd/workyard
  tar -C "$OUT_DIR" -czf "$OUT_DIR/${name}.tar.gz" "$name"
done

(
  cd "$OUT_DIR"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 *.tar.gz > checksums.txt
  else
    sha256sum *.tar.gz > checksums.txt
  fi
  {
    printf '{\n'
    printf '  "version": "%s",\n' "$VERSION"
    printf '  "artifacts": [\n'
    first=1
    for archive in *.tar.gz; do
      checksum=$(awk -v f="$archive" '$2 == f { print $1 }' checksums.txt)
      if [ "$first" -eq 0 ]; then
        printf ',\n'
      fi
      first=0
      printf '    {"name": "%s", "sha256": "%s"}' "$archive" "$checksum"
    done
    printf '\n  ]\n'
    printf '}\n'
  } > manifest.json
)

printf 'release artifacts written to %s\n' "$OUT_DIR"
