package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpgradeAliasResolvesToLocalUpdate(t *testing.T) {
	root := newRoot(&options{})
	cmd, _, err := root.Find([]string{"upgrade", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Name() != "update" {
		t.Fatalf("upgrade resolved to %v, want update", cmd)
	}
}

func TestUpdateDryRunPrintsInstallerPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installDir := filepath.Join(home, "bin")
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"update", "--dry-run", "--version", "v1.2.3", "--install-dir", installDir, "--repo", "owner/repo", "--method", "release"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		"would update workyard",
		"https://raw.githubusercontent.com/owner/repo/main/scripts/install.sh",
		"--install-dir " + installDir,
		"--version v1.2.3",
		"--method release",
		"--no-shell-update",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, got)
		}
	}
}

func TestUpdateRunsInstallerAndVerifiesInstalledVersion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installDir := filepath.Join(home, "bin")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript")
		_, _ = w.Write([]byte(fakeUpdateInstallerScript()))
	}))
	defer server.Close()

	root := newRoot(&options{})
	var out bytes.Buffer
	var errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"update", "--json", "--installer-url", server.URL, "--install-dir", installDir, "--version", "v9.9.9", "--repo", "owner/repo", "--method", "release"})
	if err := root.Execute(); err != nil {
		t.Fatalf("update failed: %v\nstderr:\n%s", err, errOut.String())
	}
	var result localUpdateResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v\n%s", err, out.String())
	}
	if !result.OK || result.InstalledVersion != "v9.9.9" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(result.Stdout, "fake installer installed v9.9.9") {
		t.Fatalf("installer stdout not captured: %#v", result)
	}
}

func TestResolveUpdateInstallDirUsesCurrentBinaryWhenSafe(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "dev")
	dir, source, err := resolveUpdateInstallDir("", filepath.Join(home, ".local", "bin", "workyard"), home)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".local", "bin") || source != "current-binary" {
		t.Fatalf("dir=%q source=%q", dir, source)
	}
	dir, source, err = resolveUpdateInstallDir("", filepath.Join(string(filepath.Separator), "tmp", "go-build", "workyard"), home)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".local", "bin") || source != "default" {
		t.Fatalf("dir=%q source=%q", dir, source)
	}
	dir, source, err = resolveUpdateInstallDir("", filepath.Join(home, "Library", "Caches", "go-build", "ab", "workyard"), home)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(home, ".local", "bin") || source != "default" {
		t.Fatalf("dir=%q source=%q", dir, source)
	}
}

func TestBuildLocalUpdatePlanRejectsUnsafeValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []localUpdateOptions{
		{Version: "main", Repo: defaultUpdateRepo, Method: defaultUpdateMethod, InstallDir: filepath.Join(home, "bin")},
		{Version: defaultUpdateVersion, Repo: "owner/repo/extra", Method: defaultUpdateMethod, InstallDir: filepath.Join(home, "bin")},
		{Version: defaultUpdateVersion, Repo: defaultUpdateRepo, Method: "curl", InstallDir: filepath.Join(home, "bin")},
		{Version: defaultUpdateVersion, Repo: defaultUpdateRepo, Method: defaultUpdateMethod, Ref: "../main", InstallDir: filepath.Join(home, "bin")},
	} {
		if _, err := buildLocalUpdatePlan(tc); err == nil {
			t.Fatalf("expected %#v to fail", tc)
		}
	}
}

func fakeUpdateInstallerScript() string {
	return `#!/bin/sh
set -eu
install_dir=
version=
repo=
method=
ref=
shell_update=1
while [ "$#" -gt 0 ]; do
  case "$1" in
    --install-dir) install_dir="$2"; shift 2 ;;
    --version) version="$2"; shift 2 ;;
    --repo) repo="$2"; shift 2 ;;
    --method) method="$2"; shift 2 ;;
    --ref) ref="$2"; shift 2 ;;
    --no-shell-update) shell_update=0; shift ;;
    *) echo "unexpected arg: $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$install_dir"
printf '%s' "$version" > "$install_dir/.workyard-version"
cat > "$install_dir/workyard" <<'BIN'
#!/bin/sh
if [ "${1:-}" = "version" ]; then
  version=$(cat "$(dirname "$0")/.workyard-version")
  if [ "${2:-}" = "--json" ]; then
    printf '{"ok":true,"version":"%s"}\n' "$version"
  else
    printf '%s\n' "$version"
  fi
  exit 0
fi
exit 0
BIN
chmod +x "$install_dir/workyard"
printf 'fake installer installed %s from %s with %s ref=%s shell=%s\n' "$version" "$repo" "$method" "$ref" "$shell_update"
`
}
