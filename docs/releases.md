# Release Process

Workyard releases are GitHub Releases created from `vX.Y.Z` tags. A release packages installable binaries for macOS and Linux, publishes checksums, and makes the no-clone installer fast for users who do not have Go installed.

## What A Release Is

- A git tag, such as `v0.1.0`, names an exact source revision.
- The release workflow builds that tag into platform archives.
- A GitHub Release publishes those archives as downloadable assets.
- `scripts/install.sh` downloads those assets by default and falls back to a source build only when release assets are unavailable.

## Release Artifacts

Each release publishes:

- `workyard-darwin-amd64.tar.gz`
- `workyard-darwin-arm64.tar.gz`
- `workyard-linux-amd64.tar.gz`
- `workyard-linux-arm64.tar.gz`
- `checksums.txt`
- `manifest.json`

## Create A Release

Start from a clean `main`:

```sh
git checkout main
git pull --rebase origin main
go test ./...
go vet ./...
```

Choose the next semantic version. Stable releases use `vX.Y.Z`; prereleases use a suffix such as `v0.2.0-rc.1`.

Create and push an annotated tag:

```sh
git tag -a v0.1.0 -m "Workyard v0.1.0"
git push origin v0.1.0
```

Pushing the tag starts `.github/workflows/release.yml`. The workflow:

- validates the tag format
- checks shell scripts
- runs `go test ./...`
- runs `go vet ./...`
- validates fixture configs
- builds all release archives with `scripts/build-release.sh`
- verifies archive contents and checksums
- creates a GitHub Release for tag pushes

## Dry Run A Release Build

Use the workflow dispatch input in GitHub Actions to build artifacts without publishing a GitHub Release, or run locally:

```sh
VERSION=v0.1.0 scripts/build-release.sh
```

Local artifacts are written to `dist/release/`.

## Verify Installation

After the GitHub Release is published:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | WORKYARD_VERSION=v0.1.0 sh
workyard version
```

Existing users can upgrade through the CLI:

```sh
workyard update --version v0.1.0
```

For a prerelease, pass its exact tag:

```sh
curl -fsSL https://raw.githubusercontent.com/jbelluche/workyard/main/scripts/install.sh | WORKYARD_VERSION=v0.2.0-rc.1 sh
workyard update --version v0.2.0-rc.1
```

## Versioning Notes

- Use stable tags for normal public releases: `v0.1.0`, `v0.2.0`.
- Use prerelease tags for testing installable candidates: `v0.2.0-rc.1`.
- The GitHub Release is marked as a prerelease whenever the tag contains `-`.
- Stable releases become the latest release; prereleases do not.
