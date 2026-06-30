package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
)

func TestSnapshotHonorsIncludeExclude(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app/server.py", "ok")
	write("app/server.log", "skip")
	write("app/__pycache__/server.pyc", "skip")

	got, err := Snapshot(root, []Spec{{
		Service: "web",
		Watch: config.WatchConfig{
			Paths:   []string{"app"},
			Include: []string{"*.py"},
			Exclude: []string{"*.log"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["app/server.py"]; !ok {
		t.Fatalf("expected app/server.py in snapshot: %#v", got)
	}
	if _, ok := got["app/server.log"]; ok {
		t.Fatalf("did not expect app/server.log in snapshot: %#v", got)
	}
	if _, ok := got["app/__pycache__/server.pyc"]; ok {
		t.Fatalf("did not expect __pycache__ file in snapshot: %#v", got)
	}
}

func TestSnapshotCanIncludeGitForMirrors(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".git/HEAD", "ref: refs/heads/main")
	write("app/server.go", "package main")

	got, err := Snapshot(root, []Spec{{
		Service:    "mirror",
		IncludeGit: true,
		Watch: config.WatchConfig{
			Paths: []string{"."},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[".git/HEAD"]; !ok {
		t.Fatalf("expected .git/HEAD in snapshot: %#v", got)
	}

	got, err = Snapshot(root, []Spec{{
		Service: "service",
		Watch: config.WatchConfig{
			Paths: []string{"."},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[".git/HEAD"]; ok {
		t.Fatalf("did not expect .git/HEAD in default snapshot: %#v", got)
	}
}

func TestChangedDetectsContentChange(t *testing.T) {
	before := map[string]FileState{"x": {Size: 1}}
	after := map[string]FileState{"x": {Size: 2}}
	if !Changed(before, after) {
		t.Fatal("expected size change to be detected")
	}
}

func TestChangesReportsFilesystemEvent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	changes, errs, err := Changes(ctx, root, []Spec{{
		Service: "web",
		Watch: config.WatchConfig{
			Paths:   []string{"app"},
			Include: []string{"*.py"},
		},
	}}, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "server.py"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case err := <-errs:
		t.Fatalf("watch error: %v", err)
	case <-ctx.Done():
		t.Fatal("timed out waiting for change")
	}
}
