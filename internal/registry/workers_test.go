package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerStoreUpsertResolveAndRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.yaml")
	store := NewWorkerStore(path)
	worker := WorkerConfig{
		Name:    "linux-builder",
		Host:    "linux-builder",
		User:    "dev",
		Source:  "tailscale",
		DNSName: "linux-builder.example.ts.net.",
	}
	if err := store.Upsert(worker); err != nil {
		t.Fatal(err)
	}
	first, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("expected one worker, got %d", len(first))
	}
	if first[0].RegisteredAt.IsZero() || first[0].UpdatedAt.IsZero() {
		t.Fatalf("timestamps were not populated: %#v", first[0])
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "workers:\n") || strings.Contains(string(data), "{") {
		t.Fatalf("expected YAML worker config, got:\n%s", string(data))
	}

	time.Sleep(time.Millisecond)
	worker.User = "pi"
	if err := store.Upsert(worker); err != nil {
		t.Fatal(err)
	}
	resolved, ok, err := store.Resolve("linux-builder")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || resolved.EffectiveSSHTarget() != "pi@linux-builder" {
		t.Fatalf("unexpected resolved worker: ok=%t worker=%#v", ok, resolved)
	}
	if !resolved.RegisteredAt.Equal(first[0].RegisteredAt) || !resolved.UpdatedAt.After(first[0].UpdatedAt) {
		t.Fatalf("timestamps were not preserved/advanced: %#v", resolved)
	}

	removed, err := store.Remove("pi@linux-builder")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected worker to be removed")
	}
	workers, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 0 {
		t.Fatalf("unexpected remaining workers: %#v", workers)
	}
}

func TestWorkerConfigEffectiveSSHTargetUsesEditableUserAndHost(t *testing.T) {
	worker := WorkerConfig{Name: "pi", Host: "linux-builder", User: "dev"}
	if got := worker.EffectiveSSHTarget(); got != "dev@linux-builder" {
		t.Fatalf("target=%q", got)
	}
	worker.User = "debian"
	if got := worker.EffectiveSSHTarget(); got != "debian@linux-builder" {
		t.Fatalf("target after user edit=%q", got)
	}
	worker.SSHTarget = "custom-worker"
	if got := worker.EffectiveSSHTarget(); got != "custom-worker" {
		t.Fatalf("override target=%q", got)
	}
}

func TestWorkerStoreRejectsInvalidWorker(t *testing.T) {
	store := NewWorkerStore(filepath.Join(t.TempDir(), "workers.yaml"))
	if err := store.Upsert(WorkerConfig{Name: "bad name", Host: "host", User: "dev"}); err == nil {
		t.Fatal("expected invalid worker name to be rejected")
	}
}

func TestWorkerStoreRejectsLocalhostOverride(t *testing.T) {
	store := NewWorkerStore(filepath.Join(t.TempDir(), "workers.yaml"))
	if err := store.Upsert(WorkerConfig{Name: LocalWorkerName, Host: LocalWorkerName, User: "dev"}); err == nil {
		t.Fatal("expected localhost worker override to be rejected")
	}
	if !IsLocalWorker("LOCALHOST") {
		t.Fatal("expected local worker helper to be case-insensitive")
	}
}

func TestWorkerStoreMigratesLegacyJSONWhenYAMLMissing(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "workers.json")
	yamlPath := filepath.Join(dir, "workers.yaml")
	data, err := json.Marshal(WorkersFile{Workers: []WorkerConfig{{
		Name:      "workyard-pi",
		Host:      "workyard-pi",
		User:      "dev",
		Source:    "tailscale",
		DNSName:   "workyard-pi.tailnet-example.ts.net",
		UpdatedAt: time.Now().UTC(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewWorkerStore(yamlPath)
	workers, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].Name != "workyard-pi" {
		t.Fatalf("unexpected migrated workers: %#v", workers)
	}
	migrated, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(migrated), "workyard-pi") || strings.Contains(string(migrated), "{") {
		t.Fatalf("expected migrated YAML, got:\n%s", string(migrated))
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("expected legacy JSON to remain: %v", err)
	}
}
