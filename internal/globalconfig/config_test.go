package globalconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingConfigIsOptional(t *testing.T) {
	loaded, err := Load(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Found {
		t.Fatalf("missing config should not be marked found: %#v", loaded)
	}
	workers, err := loaded.Workers()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 0 {
		t.Fatalf("workers=%#v, want none", workers)
	}
}

func TestLoadWorkersAndKnownHosts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	data := []byte(`
[defaults]
ssh_user = "dev"
remote_workspace = "/srv/workspaces"

[[workers]]
name = "devbox"
ssh = "jack@devbox.example.com"

[[known_hosts]]
id = "aliasbox"
ssh = "ssh-alias"
remote_workspace = "~/src"

[[static_hosts]]
name = "disabled"
host = "disabled.example.com"
enabled = false
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	workers, err := loaded.Workers()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("workers=%#v, want two enabled workers", workers)
	}
	byName := map[string]string{}
	workspaces := map[string]string{}
	for _, worker := range workers {
		byName[worker.Name] = worker.EffectiveSSHTarget()
		workspaces[worker.Name] = worker.RemoteWorkspace
	}
	if byName["devbox"] != "jack@devbox.example.com" {
		t.Fatalf("devbox target=%q", byName["devbox"])
	}
	if workspaces["devbox"] != "/srv/workspaces" {
		t.Fatalf("devbox workspace=%q", workspaces["devbox"])
	}
	if byName["aliasbox"] != "ssh-alias" {
		t.Fatalf("aliasbox target=%q", byName["aliasbox"])
	}
	if workspaces["aliasbox"] != "~/src" {
		t.Fatalf("aliasbox workspace=%q", workspaces["aliasbox"])
	}
}

func TestLoadRejectsInvalidWorker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`
[[workers]]
name = "bad worker"
ssh = "dev@example.com"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected invalid worker to fail")
	}
}

func TestJoinRemoteWorkspace(t *testing.T) {
	got := JoinRemoteWorkspace("/srv/workspaces", "/Users/dev/workyard")
	if got != "/srv/workspaces/workyard" {
		t.Fatalf("path=%q", got)
	}
}
