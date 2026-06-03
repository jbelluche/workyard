package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateRunRootRequiresManagedRunsDirectory(t *testing.T) {
	stateDir := t.TempDir()
	d := &Daemon{opts: DaemonOptions{StateDir: stateDir}}
	outside := filepath.Join(t.TempDir(), "project", "run")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := d.validateRunRoot(outside); err == nil {
		t.Fatal("expected outside run root to be rejected")
	}
}

func TestValidateRunRootAcceptsProjectRunUnderStateDir(t *testing.T) {
	stateDir := t.TempDir()
	runRoot := filepath.Join(stateDir, "runs", "fixture", "run-1")
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{opts: DaemonOptions{StateDir: stateDir}}
	clean, project, run, err := d.validateRunRoot(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	wantClean, err := filepath.EvalSymlinks(runRoot)
	if err != nil {
		t.Fatal(err)
	}
	if clean != wantClean || project != "fixture" || run != "run-1" {
		t.Fatalf("clean=%s project=%s run=%s", clean, project, run)
	}
}

func TestValidateRequestRejectsMismatchedProject(t *testing.T) {
	stateDir := t.TempDir()
	runRoot := filepath.Join(stateDir, "runs", "fixture", "run-1")
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{opts: DaemonOptions{StateDir: stateDir}}
	if _, err := d.validateRequest(Request{Action: "status", RunRoot: runRoot, Project: "other", RunID: "run-1"}); err == nil {
		t.Fatal("expected mismatched project to be rejected")
	}
}
