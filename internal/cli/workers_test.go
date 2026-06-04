package cli

import (
	"testing"

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
