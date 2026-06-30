package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/output"
	"github.com/jackbelluche/workyard/internal/registry"
)

func TestMergeWorkerDiscoveryMarksTrackedAndUntrackedDevices(t *testing.T) {
	devices := []tailscaleDevice{
		{Name: "linux-builder", Host: "linux-builder", DNSName: "linux-builder.tailnet.ts.net", Online: true, TailscaleIPs: []string{"100.64.0.10"}},
		{Name: "other", Host: "other", DNSName: "other.tailnet.ts.net", Online: false},
	}
	registered := []registry.WorkerConfig{
		{Name: "linux-builder", Host: "linux-builder", User: "dev", Source: "tailscale"},
		{Name: "old-worker", Host: "old-worker", User: "dev", Source: "manual"},
	}
	rows := mergeWorkerDiscovery(devices, registered)
	if len(rows) != 3 {
		t.Fatalf("rows=%#v", rows)
	}
	byName := map[string]workerDiscoveryRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if row := byName["linux-builder"]; !row.Tracked || row.SSHTarget != "dev@linux-builder" || !row.Online {
		t.Fatalf("unexpected tracked row: %#v", row)
	}
	if row := byName["other"]; row.Tracked || row.Online {
		t.Fatalf("unexpected untracked row: %#v", row)
	}
	if row := byName["old-worker"]; !row.Tracked || row.Source != "manual" {
		t.Fatalf("unexpected config-only row: %#v", row)
	}
}

func TestWorkerKeysMatchShortDNSAndSSHTargetHost(t *testing.T) {
	keys := workerKeys("dev@linux-builder", "linux-builder.tailnet.ts.net.")
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	for _, want := range []string{"linux-builder", "linux-builder.tailnet.ts.net"} {
		if !seen[want] {
			t.Fatalf("missing key %q from %#v", want, keys)
		}
	}
}

func TestMergeWorkerConfigsDropsReplacedKeys(t *testing.T) {
	registered := []registry.WorkerConfig{
		{Name: "devbox", Host: "old.example.com", User: "dev", Source: "manual"},
	}
	configured := []registry.WorkerConfig{
		{Name: "devbox", Host: "new.example.com", User: "dev", Source: "config"},
		{Name: "oldbox", Host: "old.example.com", User: "dev", Source: "config"},
	}
	merged := mergeWorkerConfigs(registered, configured)
	if len(merged) != 2 {
		t.Fatalf("merged=%#v, want two workers", merged)
	}
	byName := map[string]string{}
	for _, worker := range merged {
		byName[worker.Name] = worker.EffectiveSSHTarget()
	}
	if byName["devbox"] != "dev@new.example.com" || byName["oldbox"] != "dev@old.example.com" {
		t.Fatalf("unexpected merge result: %#v", merged)
	}
}

func TestTailscaleDeviceFromPeerUsesHostName(t *testing.T) {
	device := tailscaleDeviceFromPeer(tailscalePeerStatus{
		DNSName:      "linux-builder.tailnet.ts.net.",
		HostName:     "linux-builder",
		Online:       true,
		TailscaleIPs: []string{"100.64.0.10"},
	}, false)
	if device.Name != "linux-builder" || device.Host != "linux-builder" || device.DNSName != "linux-builder.tailnet.ts.net" {
		t.Fatalf("unexpected device: %#v", device)
	}
}

func TestResolveWorkerTargetPreservesLocalhost(t *testing.T) {
	got, err := resolveWorkerTarget(t.TempDir(), "localhost")
	if err != nil {
		t.Fatal(err)
	}
	if got != registry.LocalWorkerName {
		t.Fatalf("worker=%q, want %q", got, registry.LocalWorkerName)
	}
}

func TestResolveWorkerTargetUsesGlobalConfigWorker(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"
remote_workspace = "/srv/workspaces"
`)
	got, err := resolveWorkerTarget(stateDir, "devbox")
	if err != nil {
		t.Fatal(err)
	}
	if got != "jack@devbox.example.com" {
		t.Fatalf("target=%q, want static host target", got)
	}
}

func TestBuildWorkerConfigRejectsLocalhost(t *testing.T) {
	_, err := buildWorkerConfig(context.Background(), &options{}, "localhost", "", "", "")
	if err == nil {
		t.Fatal("expected localhost registration to be rejected")
	}
	if ce := output.AsCommandError(err); ce.Code != "WORKER_RESERVED" {
		t.Fatalf("code=%q, want WORKER_RESERVED", ce.Code)
	}
}

func TestWorkerListRowsIncludesBuiltinLocalhost(t *testing.T) {
	stateDir := t.TempDir()
	runStore := registry.New(registry.DefaultPath(stateDir))
	if err := runStore.Upsert(registry.RunRef{Worker: registry.LocalWorkerName, Project: "fixture", RunID: "main"}); err != nil {
		t.Fatal(err)
	}
	rows, err := workerListRows(&options{stateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == registry.LocalWorkerName {
			if row.Source != "builtin" || row.SSHTarget != "local" || row.RunCount != 1 {
				t.Fatalf("unexpected localhost row: %#v", row)
			}
			return
		}
	}
	t.Fatalf("localhost row missing from %#v", rows)
}

func TestWorkerListRowsIncludesGlobalConfigWorker(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[defaults]
ssh_user = "dev"
remote_workspace = "/srv/workspaces"

[[known_hosts]]
name = "devbox"
host = "devbox.example.com"
`)
	rows, err := workerListRows(&options{stateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name == "devbox" {
			if row.Source != "config" || row.SSHTarget != "dev@devbox.example.com" || row.RemoteWorkspace != "/srv/workspaces" {
				t.Fatalf("unexpected global worker row: %#v", row)
			}
			return
		}
	}
	t.Fatalf("devbox row missing from %#v", rows)
}

func TestWorkerRequiredHintListsRegisteredWorkers(t *testing.T) {
	stateDir := t.TempDir()
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	for _, name := range []string{"pi", "r5"} {
		if err := store.Upsert(registry.WorkerConfig{Name: name, Host: name, User: "dev"}); err != nil {
			t.Fatal(err)
		}
	}
	err := requireWorker(&options{stateDir: stateDir}, "status")
	if err == nil {
		t.Fatal("expected missing worker to fail")
	}
	ce := output.AsCommandError(err)
	if ce == nil || ce.Code != "WORKER_REQUIRED" {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"localhost", "pi", "r5"} {
		if !strings.Contains(ce.Hint, want) {
			t.Fatalf("hint %q missing %q", ce.Hint, want)
		}
	}
}

func TestWorkerRequiredHintListsGlobalConfigWorkers(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"
`)
	err := requireWorker(&options{stateDir: stateDir}, "status")
	if err == nil {
		t.Fatal("expected missing worker to fail")
	}
	ce := output.AsCommandError(err)
	if ce == nil || !strings.Contains(ce.Hint, "devbox") {
		t.Fatalf("hint %q missing devbox", ce.Hint)
	}
}

func TestWorkerCompletionsIncludeLocalhostAndRegisteredNames(t *testing.T) {
	stateDir := t.TempDir()
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	if err := store.Upsert(registry.WorkerConfig{Name: "pi", Host: "pi", User: "dev"}); err != nil {
		t.Fatal(err)
	}
	got := workerCompletions(stateDir)
	want := []string{registry.LocalWorkerName, "pi"}
	if len(got) != len(want) {
		t.Fatalf("completions=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("completions=%#v, want %#v", got, want)
		}
	}
}

func TestWorkerCompletionsIncludeGlobalConfigNames(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"
`)
	got := workerCompletions(stateDir)
	want := []string{registry.LocalWorkerName, "devbox"}
	if len(got) != len(want) {
		t.Fatalf("completions=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("completions=%#v, want %#v", got, want)
		}
	}
}

func TestDefaultMirrorRemotePathUsesGlobalWorkerWorkspace(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[defaults]
remote_workspace = "~/workspace"

[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"
remote_workspace = "/srv/workspaces"
`)
	got := defaultMirrorRemotePath(stateDir, "devbox", "/Users/dev/workyard")
	if got != "/srv/workspaces/workyard" {
		t.Fatalf("remote path=%q, want configured workspace", got)
	}
}

func TestWorkersRemoveRefusesGlobalConfigWorker(t *testing.T) {
	stateDir := t.TempDir()
	writeGlobalConfig(t, stateDir, `
[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"
`)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "workers", "remove", "devbox"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected remove to fail for global config worker")
	}
	ce := output.AsCommandError(err)
	if ce == nil || ce.Code != "WORKER_CONFIG_READONLY" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLifecycleCommandRequiresExplicitWorker(t *testing.T) {
	opts := &options{}
	root := newRoot(opts)
	root.SetArgs([]string{"status"})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected missing worker to fail")
	}
	if ce := output.AsCommandError(err); ce.Code != "WORKER_REQUIRED" {
		t.Fatalf("code=%q, want WORKER_REQUIRED", ce.Code)
	}
}

func writeGlobalConfig(t *testing.T, stateDir, body string) {
	t.Helper()
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
