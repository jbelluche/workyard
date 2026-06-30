package syncer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
)

func TestWriteExcludeFileHonorsIncludeEnvFiles(t *testing.T) {
	path, err := writeExcludeFile(config.Config{
		Sync: config.SyncConfig{
			IncludeEnvFiles: true,
			Exclude:         []string{".env.local", "node_modules", "custom"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, ".env") {
		t.Fatalf("expected env excludes to be omitted, got:\n%s", got)
	}
	if !strings.Contains(got, "custom") || !strings.Contains(got, "node_modules") {
		t.Fatalf("expected non-env excludes to remain, got:\n%s", got)
	}
}

func TestWriteExcludeFileKeepsEnvExcludesByDefault(t *testing.T) {
	path, err := writeExcludeFile(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".env") {
		t.Fatalf("expected env excludes by default, got:\n%s", string(data))
	}
}

func TestRemotePrepareScriptGuardsManagedSymlinks(t *testing.T) {
	paths, err := remote.BuildPaths("/home/dev", "", "fixture", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	script := remotePrepareScript(paths, "/home/dev/.workyard/bin")
	for _, want := range []string{
		"if [ -L \"$p\" ]",
		"mkdir -p",
		"/home/dev/.workyard/runs/fixture/run-1/source",
		"/home/dev/.workyard/runs/fixture",
		"/home/dev/.workyard/bin",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("prepare script missing %q:\n%s", want, script)
		}
	}
}

func TestRunLocalCopiesSourceIntoManagedRun(t *testing.T) {
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	root := filepath.Join(t.TempDir(), "fixture")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig("fixture")
	cfg.Root = root
	cfg.Path = filepath.Join(root, config.FileName)
	loaded := config.Loaded{Config: cfg}

	res, err := RunLocal(context.Background(), loaded, Options{RunID: "main", Delete: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if res.Worker != registry.LocalWorkerName {
		t.Fatalf("worker=%q, want %q", res.Worker, registry.LocalWorkerName)
	}
	copied := filepath.Join(home, ".workyard", "runs", "fixture", "main", "source", "hello.txt")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("copied file=%q", string(data))
	}
	if _, err := os.Stat(filepath.Join(home, ".workyard", "runs", "fixture", "main", "sync.json")); err != nil {
		t.Fatal(err)
	}
}
