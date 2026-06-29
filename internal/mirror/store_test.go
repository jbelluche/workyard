package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreUpsertListDelete(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "project")
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "mirrors.yaml"))
	stored, err := store.Upsert(Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project",
		Delete:     true,
		IncludeGit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || stored.RegisteredAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("unexpected stored profile: %#v", stored)
	}
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "project" {
		t.Fatalf("profiles=%#v", profiles)
	}
	removed, ok, err := store.Delete("project")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || removed.Name != "project" {
		t.Fatalf("removed=%#v ok=%t", removed, ok)
	}
	profiles, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 0 {
		t.Fatalf("expected empty registry, got %#v", profiles)
	}
}

func TestDefaultRemotePathUsesWorkspaceBasename(t *testing.T) {
	got := DefaultRemotePath("/Users/jack/workspace/workyard")
	if got != "~/workspace/workyard" {
		t.Fatalf("remote path=%q", got)
	}
}

func TestValidateResolvedRemotePathRejectsBroadPaths(t *testing.T) {
	for _, path := range []string{"/", "/home/jack", "/tmp/x"} {
		if err := ValidateResolvedRemotePath("/home/jack", path); err == nil {
			t.Fatalf("expected %q to be rejected", path)
		}
	}
	if err := ValidateResolvedRemotePath("/home/jack", "/home/jack/workspace/workyard"); err != nil {
		t.Fatalf("expected workspace path to be accepted: %v", err)
	}
}

func TestWriteExcludeFileHonorsIncludeGit(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "project")
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := writeExcludeFile(Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project",
		IncludeGit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), ".git\n") {
		t.Fatalf("did not expect .git to be excluded when IncludeGit=true:\n%s", string(data))
	}
	if !strings.Contains(string(data), MarkerFileName) {
		t.Fatalf("expected marker file to be excluded:\n%s", string(data))
	}

	path, err = writeExcludeFile(Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project",
		IncludeGit: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".git\n") {
		t.Fatalf("expected .git to be excluded when IncludeGit=false:\n%s", string(data))
	}
}
