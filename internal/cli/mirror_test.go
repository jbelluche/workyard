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

func TestMirrorShellRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "shell", "project", "--command", "pwd"})
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

func TestMirrorShellRejectsTmuxWithCommand(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "shell", "--tmux", "--command", "pwd"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected invalid shell args to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_SHELL_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_SHELL_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorShellRejectsSessionWithoutTmux(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "shell", "--session", "work"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected invalid shell args to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_SHELL_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_SHELL_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorShellReportsNoMirrorsConfigured(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", t.TempDir(), "mirror", "shell", "--command", "pwd"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected shell without mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
}

func TestMirrorShellInfersMostSpecificCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "project")
	child := filepath.Join(parent, "service")
	leaf := filepath.Join(child, "cmd")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(leaf); err != nil {
		t.Fatal(err)
	}
	got, ok, err := selectMirrorProfileForShell([]mirror.Profile{
		{ID: "aaaaaa", Name: "project", LocalRoot: parent},
		{ID: "bbbbbb", Name: "service", LocalRoot: child},
	}, "")
	if err != nil || !ok {
		t.Fatalf("select ok=%t err=%v", ok, err)
	}
	if got.ID != "bbbbbb" {
		t.Fatalf("selected id=%q, want bbbbbb", got.ID)
	}
}

func TestMirrorShellRequiresIDForCurrentDirectoryCollision(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "project")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(local); err != nil {
		t.Fatal(err)
	}
	_, ok, err := selectMirrorProfileForShell([]mirror.Profile{
		{ID: "aaaaaa", Name: "project", LocalRoot: local, RemotePath: "~/workspace/a"},
		{ID: "bbbbbb", Name: "project", LocalRoot: local, RemotePath: "~/workspace/b"},
	}, "")
	if err == nil || ok {
		t.Fatalf("expected current-directory collision, ok=%t err=%v", ok, err)
	}
	ambiguous, ok := err.(mirror.AmbiguousRefError)
	if !ok {
		t.Fatalf("expected AmbiguousRefError, got %T: %v", err, err)
	}
	if !strings.Contains(strings.Join(ambiguous.IDs, ","), "aaaaaa") || !strings.Contains(strings.Join(ambiguous.IDs, ","), "bbbbbb") {
		t.Fatalf("ambiguous ids=%#v", ambiguous.IDs)
	}
}

func TestMirrorShellSessionName(t *testing.T) {
	got, err := mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "workyard-abc123" {
		t.Fatalf("session=%q, want workyard-abc123", got)
	}
	got, err = mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "wy-test_1.2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wy-test_1.2" {
		t.Fatalf("session=%q, want wy-test_1.2", got)
	}
	if _, err := mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "bad:name"); err == nil {
		t.Fatal("expected invalid tmux session name to fail")
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
