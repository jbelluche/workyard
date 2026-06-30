package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigParsesBootstrapWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workyard.bootstrap.yaml")
	data := []byte(`
version: 1
workers:
  linux-builder:
    ssh:
      user: dev
      host: linux-builder
    register: true
    workyard:
      install: true
      daemon: true
    packages:
      install: true
      apt:
        - rsync
        - name: curl
          version: 7.88.1-10
    docker:
      install: true
      composePlugin: true
      addUserToGroup: true
      version: 20.10.24+dfsg1-1+deb12u1
      composeVersion: 2.26.1-1
    checks:
      doctor: true
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, found, err := LoadConfig(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected config to be found")
	}
	spec := cfg.Workers["linux-builder"]
	if spec.SSH.User != "dev" || spec.SSH.Host != "linux-builder" {
		t.Fatalf("unexpected ssh spec: %#v", spec.SSH)
	}
	if !boolDefault(spec.Workyard.Install, false) || !boolDefault(spec.Docker.Install, false) || !boolDefault(spec.Checks.Doctor, false) {
		t.Fatalf("expected true options to parse: %#v", spec)
	}
	if len(spec.Packages.Apt) != 2 || spec.Packages.Apt[0].Name != "rsync" || spec.Packages.Apt[1].Version != "7.88.1-10" {
		t.Fatalf("unexpected apt packages: %#v", spec.Packages.Apt)
	}
	if spec.Docker.Version != "20.10.24+dfsg1-1+deb12u1" || spec.Docker.ComposeVersion != "2.26.1-1" {
		t.Fatalf("unexpected docker versions: %#v", spec.Docker)
	}
}

func TestLoadConfigOptionalMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	cfg, found, err := LoadConfig(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if found || cfg.Workers != nil {
		t.Fatalf("unexpected config: found=%t cfg=%#v", found, cfg)
	}
}

func TestLoadConfigRequiredMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	if _, _, err := LoadConfig(path, true); err == nil {
		t.Fatal("expected missing required config to fail")
	}
}
