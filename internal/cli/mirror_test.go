package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/mirror"
	"github.com/jackbelluche/workyard/internal/output"
)

func TestMirrorPauseRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "pause", "project"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected ambiguous mirror name to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
	if !strings.Contains(ce.Hint, firstID) {
		t.Fatalf("hint %q missing id %q", ce.Hint, firstID)
	}
}

func TestMirrorPauseResumeByID(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "pause", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("pause by id: %v", err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profile, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get paused profile ok=%t err=%v", ok, err)
	}
	if profile.Enabled {
		t.Fatalf("expected profile to be paused: %#v", profile)
	}

	root = newRoot(&options{})
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "resume", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("resume by id: %v", err)
	}
	profile, ok, err = store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get resumed profile ok=%t err=%v", ok, err)
	}
	if !profile.Enabled {
		t.Fatalf("expected profile to be resumed: %#v", profile)
	}
}

func TestMirrorListShowsIDColumn(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "ID") || !strings.Contains(got, firstID) {
		t.Fatalf("list output missing id column/id:\n%s", got)
	}
}

func TestMirrorDeleteByIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "delete", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("delete by id: %v", err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	if _, ok, err := store.Get(firstID); err != nil || ok {
		t.Fatalf("deleted id still resolved ok=%t err=%v", ok, err)
	}
}

func writeMirrorConflictRegistry(t *testing.T) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	local := filepath.Join(stateDir, "project")
	if err := ensureTestDir(local); err != nil {
		t.Fatal(err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	first, err := store.Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "jack@jack-r5-16gb",
		RemotePath: "~/workspace/project-b",
	}); err != nil {
		t.Fatal(err)
	}
	return stateDir, first.ID
}

func ensureTestDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
