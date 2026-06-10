package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
)

func TestResolveWorkerFromConfig(t *testing.T) {
	cfg := Config{Workers: map[string]WorkerSpec{
		"pi": {
			SSH: SSHSpec{User: "jack", Host: "jack-r5-16gb"},
		},
	}}
	resolved, err := ResolveWorker("pi", cfg, filepath.Join(t.TempDir(), "workers.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "pi" || resolved.Target != "jack@jack-r5-16gb" || resolved.Source != "config" {
		t.Fatalf("unexpected resolved worker: %#v", resolved)
	}
}

func TestResolveWorkerFromRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workers.yaml")
	store := registry.NewWorkerStore(path)
	if err := store.Upsert(registry.WorkerConfig{Name: "pi", Host: "jack-r5-16gb", User: "jack"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveWorker("pi", Config{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "pi" || resolved.Target != "jack@jack-r5-16gb" || resolved.Source != "registry" {
		t.Fatalf("unexpected resolved worker: %#v", resolved)
	}
}

func TestDryRunPlansBootstrapSteps(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "workyard.bootstrap.yaml")
	cfg := []byte(`
version: 1
workers:
  pi:
    ssh:
      user: jack
      host: jack-r5-16gb
    packages:
      install: true
      apt:
        - curl
        - name: rsync
          version: 3.2.7-1
    docker:
      install: true
      composePlugin: true
      version: 20.10.24+dfsg1-1+deb12u1
      composeVersion: 2.26.1-1
`)
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Options{
		Worker:         "pi",
		ConfigPath:     cfgPath,
		ConfigRequired: true,
		DryRun:         true,
		Version:        "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || !report.DryRun || !report.ConfigFound {
		t.Fatalf("unexpected report: %#v", report)
	}
	steps := map[string]Step{}
	for _, step := range report.Steps {
		steps[step.Name] = step
		if step.Status != StatusPass && step.Status != StatusPlan {
			t.Fatalf("unexpected non-plan/pass step: %#v", step)
		}
	}
	for _, name := range []string{"ssh", "directories", "packages", "docker", "workyard.install", "workyard.daemon", "registry", "doctor"} {
		if _, ok := steps[name]; !ok {
			t.Fatalf("missing planned step %q from %#v", name, report.Steps)
		}
	}
}

func TestPackageListIncludesRsyncOnce(t *testing.T) {
	got := packageInstallArgs(packageList(WorkerSpec{Packages: PackageSpec{Apt: []AptPackage{
		{Name: "curl"},
		{Name: "rsync", Version: "3.2.7-1"},
		{Name: "curl"},
	}}}))
	want := []string{"curl", "rsync=3.2.7-1"}
	if len(got) != len(want) {
		t.Fatalf("packages=%#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("packages=%#v want %#v", got, want)
		}
	}
}

func TestDockerPackagesSupportPinnedVersions(t *testing.T) {
	got := packageInstallArgs(dockerPackages(WorkerSpec{Docker: DockerSpec{
		Install:        boolPtr(true),
		ComposePlugin:  boolPtr(true),
		Version:        "20.10.24+dfsg1-1+deb12u1",
		ComposeVersion: "2.26.1-1",
	}}))
	want := []string{"docker-compose-plugin=2.26.1-1", "docker.io=20.10.24+dfsg1-1+deb12u1"}
	if len(got) != len(want) {
		t.Fatalf("packages=%#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("packages=%#v want %#v", got, want)
		}
	}
}

func TestSudoAuthDefaultsToPasswordlessSudo(t *testing.T) {
	auth := sudoAuth{}
	prelude := strings.Join(auth.prelude("package install"), "\n")
	if !strings.Contains(prelude, "sudo -n") {
		t.Fatalf("expected passwordless sudo prelude, got %q", prelude)
	}
	if got := auth.stdin(); got != nil {
		t.Fatalf("expected no stdin for passwordless sudo, got %#v", got)
	}
	if got := auth.command("apt-get", "update"); got != "sudo_cmd apt-get update" {
		t.Fatalf("unexpected sudo command %q", got)
	}
}

func TestSudoAuthWithPasswordUsesStdinOnly(t *testing.T) {
	auth := sudoAuth{Password: "secret value"}
	prelude := strings.Join(auth.prelude("package install"), "\n")
	if !strings.Contains(prelude, "sudo -S -p ''") {
		t.Fatalf("expected sudo -S prelude, got %q", prelude)
	}
	if strings.Contains(prelude, auth.Password) {
		t.Fatalf("password leaked into prelude: %q", prelude)
	}
	if got, want := string(auth.stdin()), "secret value\n"; got != want {
		t.Fatalf("stdin=%q want %q", got, want)
	}
}

func TestFindRepoRootWorksFromSubdirectory(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
	if err := os.Chdir(filepath.Join(original, "..", "..", "fixtures", "health-server")); err != nil {
		t.Fatal(err)
	}
	root, err := remote.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(root) != "workyard" {
		t.Fatalf("unexpected repo root %q", root)
	}
}

func TestRegisterWorkerPreservesExistingTailscaleMetadata(t *testing.T) {
	stateDir := t.TempDir()
	store := registry.NewWorkerStore(registry.DefaultWorkersPath(stateDir))
	if err := store.Upsert(registry.WorkerConfig{
		Name:         "pi",
		Host:         "pi",
		User:         "jack",
		Source:       "tailscale",
		DNSName:      "pi.tailnet.ts.net",
		TailscaleIPs: []string{"100.64.0.10"},
	}); err != nil {
		t.Fatal(err)
	}
	err := registerWorker(stateDir, resolvedWorker{Name: "pi", Host: "pi", User: "jack", Target: "jack@pi"})
	if err != nil {
		t.Fatal(err)
	}
	worker, ok, err := store.Resolve("pi")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected worker to resolve")
	}
	if worker.Source != "tailscale" || worker.DNSName != "pi.tailnet.ts.net" || len(worker.TailscaleIPs) != 1 {
		t.Fatalf("metadata was not preserved: %#v", worker)
	}
}

func boolPtr(value bool) *bool {
	return &value
}
