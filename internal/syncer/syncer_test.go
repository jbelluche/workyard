package syncer

import (
	"os"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/config"
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
	paths, err := remote.BuildPaths("/home/jack", "", "fixture", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	script := remotePrepareScript(paths, "/home/jack/.workyard/bin")
	for _, want := range []string{
		"if [ -L \"$p\" ]",
		"mkdir -p",
		"/home/jack/.workyard/runs/fixture/run-1/source",
		"/home/jack/.workyard/runs/fixture",
		"/home/jack/.workyard/bin",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("prepare script missing %q:\n%s", want, script)
		}
	}
}
