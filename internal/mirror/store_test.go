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
	if stored.ID == "" || !stored.Enabled || stored.RegisteredAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Fatalf("unexpected stored profile: %#v", stored)
	}
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 1 || profiles[0].Name != "project" {
		t.Fatalf("profiles=%#v", profiles)
	}
	removed, ok, err := store.Delete(stored.ID)
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

func TestResolveAllowsUniqueNameAndRequiresIDForNameCollision(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "project")
	if err := os.Mkdir(local, 0o700); err != nil {
		t.Fatal(err)
	}
	store := NewStore(filepath.Join(root, "mirrors.yaml"))
	first, err := store.Upsert(Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Upsert(Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Upsert(Profile{
		Name:       "other",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == "" || second.ID == "" {
		t.Fatalf("ids were not unique: first=%q second=%q", first.ID, second.ID)
	}
	profiles, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := Resolve(profiles, other.Name)
	if err != nil || !ok || got.ID != other.ID {
		t.Fatalf("unique name resolve got=%#v ok=%t err=%v", got, ok, err)
	}
	got, ok, err = Resolve(profiles, first.ID)
	if err != nil || !ok || got.RemotePath != first.RemotePath {
		t.Fatalf("id resolve got=%#v ok=%t err=%v", got, ok, err)
	}
	_, ok, err = Resolve(profiles, "project")
	if ok || err == nil {
		t.Fatalf("expected ambiguous project name, ok=%t err=%v", ok, err)
	}
	ambiguous, ok := err.(AmbiguousRefError)
	if !ok {
		t.Fatalf("expected AmbiguousRefError, got %T: %v", err, err)
	}
	if len(ambiguous.IDs) != 2 {
		t.Fatalf("ambiguous ids=%#v", ambiguous.IDs)
	}
}

func TestSetEnabledUsesNameOnlyWhenUnambiguous(t *testing.T) {
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
	})
	if err != nil {
		t.Fatal(err)
	}
	paused, ok, err := store.SetEnabled("project", false)
	if err != nil || !ok {
		t.Fatalf("pause by unique name ok=%t err=%v", ok, err)
	}
	if paused.Enabled {
		t.Fatalf("expected paused profile: %#v", paused)
	}
	resumed, ok, err := store.SetEnabled(stored.ID, true)
	if err != nil || !ok {
		t.Fatalf("resume by id ok=%t err=%v", ok, err)
	}
	if !resumed.Enabled {
		t.Fatalf("expected resumed profile: %#v", resumed)
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

func TestParseChangesReadsRsyncItemizedOutput(t *testing.T) {
	got := parseChanges(strings.Join([]string{
		">f+++++++++ server.py",
		".d..t...... app",
		"Number of regular files transferred: 1",
		"Total transferred file size: 42 bytes",
	}, "\n"))
	if len(got) != 2 {
		t.Fatalf("changes=%#v", got)
	}
	if got[0].Code != ">f+++++++++" || got[0].Path != "server.py" {
		t.Fatalf("unexpected first change: %#v", got[0])
	}
}
