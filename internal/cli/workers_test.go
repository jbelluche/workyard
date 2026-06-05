package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/jackbelluche/workyard/internal/output"
	"github.com/jackbelluche/workyard/internal/registry"
)

func TestMergeWorkerDiscoveryMarksTrackedAndUntrackedDevices(t *testing.T) {
	devices := []tailscaleDevice{
		{Name: "jack-r5-16gb", Host: "jack-r5-16gb", DNSName: "jack-r5-16gb.tailnet.ts.net", Online: true, TailscaleIPs: []string{"100.64.0.10"}},
		{Name: "other", Host: "other", DNSName: "other.tailnet.ts.net", Online: false},
	}
	registered := []registry.WorkerConfig{
		{Name: "jack-r5-16gb", Host: "jack-r5-16gb", User: "jack", Source: "tailscale"},
		{Name: "old-worker", Host: "old-worker", User: "jack", Source: "manual"},
	}
	rows := mergeWorkerDiscovery(devices, registered)
	if len(rows) != 3 {
		t.Fatalf("rows=%#v", rows)
	}
	byName := map[string]workerDiscoveryRow{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	if row := byName["jack-r5-16gb"]; !row.Tracked || row.SSHTarget != "jack@jack-r5-16gb" || !row.Online {
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
	keys := workerKeys("jack@jack-r5-16gb", "jack-r5-16gb.tailnet.ts.net.")
	seen := map[string]bool{}
	for _, key := range keys {
		seen[key] = true
	}
	for _, want := range []string{"jack-r5-16gb", "jack-r5-16gb.tailnet.ts.net"} {
		if !seen[want] {
			t.Fatalf("missing key %q from %#v", want, keys)
		}
	}
}

func TestTailscaleDeviceFromPeerUsesHostName(t *testing.T) {
	device := tailscaleDeviceFromPeer(tailscalePeerStatus{
		DNSName:      "jack-r5-16gb.tailnet.ts.net.",
		HostName:     "jack-r5-16gb",
		Online:       true,
		TailscaleIPs: []string{"100.64.0.10"},
	}, false)
	if device.Name != "jack-r5-16gb" || device.Host != "jack-r5-16gb" || device.DNSName != "jack-r5-16gb.tailnet.ts.net" {
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
