package remote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildPathsRejectsRemoteRootOutsideWorkyardRuns(t *testing.T) {
	if _, err := BuildPaths("/home/jack", "/tmp/workyard", "project", "run"); err == nil {
		t.Fatal("expected outside remote root to be rejected")
	}
}

func TestBuildPathsDefault(t *testing.T) {
	paths, err := BuildPaths("/home/jack", "", "my project", "feature-1")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Source != "/home/jack/.workyard/runs/my-project/feature-1/source" {
		t.Fatalf("unexpected source path %s", paths.Source)
	}
}

func TestValidateWorkerAcceptsNormalTargets(t *testing.T) {
	for _, worker := range []string{"jack@jack-rasp-five", "jack-rasp-five", "pi_01"} {
		if err := ValidateWorker(worker); err != nil {
			t.Fatalf("expected %q to be accepted: %v", worker, err)
		}
	}
}

func TestValidateWorkerRejectsOptionAndShellLikeTargets(t *testing.T) {
	for _, worker := range []string{
		"-oProxyCommand=sh",
		"jack@host:/tmp",
		"jack@host name",
		"jack@host;touch /tmp/pwned",
		"jack@host\nwhoami",
	} {
		if err := ValidateWorker(worker); err == nil {
			t.Fatalf("expected %q to be rejected", worker)
		}
	}
}

func TestNormalizePlatformMapsLinuxArm64(t *testing.T) {
	platform, err := NormalizePlatform("Linux", "aarch64")
	if err != nil {
		t.Fatal(err)
	}
	if platform.OS != "linux" || platform.Arch != "arm64" || platform.ArtifactName() != "workyard-linux-arm64" {
		t.Fatalf("unexpected platform %#v", platform)
	}
}

func TestArtifactFreshTracksWorkyardSourceFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module github.com/jackbelluche/workyard\n")
	mustWrite(t, filepath.Join(root, "go.sum"), "")
	mustWrite(t, filepath.Join(root, "cmd", "workyard", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(root, "internal", "worker", "worker.go"), "package worker\n")
	artifact := filepath.Join(root, "dist", "workyard-linux-arm64")
	mustWrite(t, artifact, "binary")

	old := time.Now().Add(-2 * time.Hour)
	newer := time.Now().Add(-1 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "go.mod"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "go.sum"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "cmd", "workyard", "main.go"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(root, "internal", "worker", "worker.go"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(artifact, newer, newer); err != nil {
		t.Fatal(err)
	}
	if !artifactFresh(root, artifact) {
		t.Fatal("expected artifact to be fresh when it is newer than sources")
	}

	latest := time.Now()
	if err := os.Chtimes(filepath.Join(root, "internal", "worker", "worker.go"), latest, latest); err != nil {
		t.Fatal(err)
	}
	if artifactFresh(root, artifact) {
		t.Fatal("expected artifact to be stale when a source file is newer")
	}
}

func TestInstallDestinationStaysUnderWorkyardBin(t *testing.T) {
	dest, err := installDestination("/home/jack", "")
	if err != nil {
		t.Fatal(err)
	}
	if dest != "/home/jack/.workyard/bin/workyard" {
		t.Fatalf("dest=%s", dest)
	}
	if _, err := installDestination("/home/jack", "/tmp/workyard"); err == nil {
		t.Fatal("expected outside install destination to be rejected")
	}
}

func TestDaemonPathsUseWorkyardDaemonAndInstalledBinary(t *testing.T) {
	paths, err := DaemonPaths("/home/jack", "")
	if err != nil {
		t.Fatal(err)
	}
	if paths.Socket != "/home/jack/.workyard/daemon/workyard.sock" {
		t.Fatalf("socket=%s", paths.Socket)
	}
	if paths.Binary != "/home/jack/.workyard/bin/workyard" {
		t.Fatalf("binary=%s", paths.Binary)
	}
}

func TestDaemonPathsRejectBinaryOutsideWorkyardBin(t *testing.T) {
	if _, err := DaemonPaths("/home/jack", "/tmp/workyard"); err == nil {
		t.Fatal("expected outside daemon binary to be rejected")
	}
}

func TestForceStopDaemonScriptUsesLockAndSocketGuards(t *testing.T) {
	paths := Paths{
		DaemonDir: "/home/jack/.workyard/daemon",
		Socket:    "/home/jack/.workyard/daemon/workyard.sock",
	}
	script := forceStopDaemonScript(paths)
	for _, want := range []string{
		"daemon.lock",
		"refusing symlink daemon lock or socket",
		"invalid daemon pid",
		"workyard daemon",
		"$socket",
		"kill -TERM",
		"kill -KILL",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("force stop script missing %q:\n%s", want, script)
		}
	}
}

func TestValidateManagedRunRejectsOutsidePaths(t *testing.T) {
	paths, err := BuildPaths("/home/jack", "", "fixture", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedRun(paths); err != nil {
		t.Fatal(err)
	}
	paths.RunRoot = "/tmp/workyard"
	if err := validateManagedRun(paths); err == nil {
		t.Fatal("expected outside run path to be rejected")
	}
}

func TestBuildLocalPathsUsesStateDir(t *testing.T) {
	paths, err := BuildLocalPaths("/home/jack", "/tmp/custom-state", "", "fixture", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if paths.RunRoot != "/tmp/custom-state/runs/fixture/run-1" {
		t.Fatalf("runRoot=%q", paths.RunRoot)
	}
	if paths.Socket != "/tmp/custom-state/daemon/workyard.sock" {
		t.Fatalf("socket=%q", paths.Socket)
	}
	if paths.StateDir != "/tmp/custom-state" {
		t.Fatalf("stateDir=%q", paths.StateDir)
	}
}

func TestBuildLocalPathsDefaultsToHomeWorkyard(t *testing.T) {
	paths, err := BuildLocalPaths("/home/jack", "", "", "fixture", "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if paths.RunRoot != "/home/jack/.workyard/runs/fixture/run-1" {
		t.Fatalf("runRoot=%q", paths.RunRoot)
	}
	if paths.StateDir != "/home/jack/.workyard" {
		t.Fatalf("stateDir=%q", paths.StateDir)
	}
}

func TestBuildLocalPathsRejectsRemoteRootOutsideStateDir(t *testing.T) {
	if _, err := BuildLocalPaths("/home/jack", "/tmp/custom-state", "/home/jack/.workyard/runs", "fixture", "run-1"); err == nil {
		t.Fatal("expected remote root outside the state dir runs root to be rejected")
	}
	if _, err := BuildLocalPaths("/home/jack", "/tmp/custom-state", "/tmp/custom-state/runs", "fixture", "run-1"); err != nil {
		t.Fatalf("expected remote root under the state dir to be accepted: %v", err)
	}
}

func mustWrite(t *testing.T, path string, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
