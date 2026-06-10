package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDeployProjectAndServicesUsesExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	project, services, err := deployProjectAndServices(".", []string{dir, "web"})
	if err != nil {
		t.Fatal(err)
	}
	if project != dir {
		t.Fatalf("project=%q, want %q", project, dir)
	}
	if !reflect.DeepEqual(services, []string{"web"}) {
		t.Fatalf("services=%#v", services)
	}
}

func TestDeployProjectAndServicesUsesYamlPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workyard.yaml")
	if err := os.WriteFile(path, []byte("name: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project, services, err := deployProjectAndServices(".", []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if project != path {
		t.Fatalf("project=%q, want %q", project, path)
	}
	if len(services) != 0 {
		t.Fatalf("services=%#v", services)
	}
}

func TestDeployProjectAndServicesKeepsServiceArgs(t *testing.T) {
	project, services, err := deployProjectAndServices("fixture", []string{"web", "api"})
	if err != nil {
		t.Fatal(err)
	}
	if project != "fixture" {
		t.Fatalf("project=%q", project)
	}
	if !reflect.DeepEqual(services, []string{"web", "api"}) {
		t.Fatalf("services=%#v", services)
	}
}

func TestDeployProjectAndServicesRejectsMissingYamlPath(t *testing.T) {
	if _, _, err := deployProjectAndServices(".", []string{"missing-workyard.yaml"}); err == nil {
		t.Fatal("expected missing yaml path to be rejected")
	}
}

func TestDeployProjectAndServicesRejectsMissingPathLikeArg(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, _, err := deployProjectAndServices(".", []string{missing}); err == nil {
		t.Fatal("expected missing path-like arg to be rejected")
	}
}

func TestDeployProjectAndServicesRejectsAmbiguousServiceDirectory(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Mkdir("web", 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deployProjectAndServices(".", []string{"web"}); err == nil {
		t.Fatal("expected ambiguous service/directory name to be rejected")
	}
}

func TestDeployProjectAndServicesTreatsPlainWordAsService(t *testing.T) {
	project, services, err := deployProjectAndServices(".", []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	if project != "." || !reflect.DeepEqual(services, []string{"web"}) {
		t.Fatalf("project=%q services=%#v", project, services)
	}
}

func TestRemoteTimeoutUsesRequestedStartTimeout(t *testing.T) {
	got := remoteTimeout("start", "10m")
	want := 10*time.Minute + 10*time.Second
	if got != want {
		t.Fatalf("start timeout=%s, want %s", got, want)
	}
}
