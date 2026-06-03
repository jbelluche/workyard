package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
