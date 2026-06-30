package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectPresetsFromRepoFiles(t *testing.T) {
	root := t.TempDir()
	for _, file := range []string{"package.json", "pyproject.toml", "go.mod", "Cargo.toml"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := strings.Join(DetectPresets(root), ",")
	for _, want := range []string{"go", "node", "python", "rust"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detected presets %q missing %q", got, want)
		}
	}
}

func TestResolvePresetSelectionAutoAndNone(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolvePresetSelection(root, []string{"auto"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "node" {
		t.Fatalf("auto presets=%#v, want node", got)
	}
	got, err = ResolvePresetSelection(root, []string{"none"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("none presets=%#v, want empty", got)
	}
}

func TestDetectPresetsFindsNestedMonorepoProjects(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "services", "analytics"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "services", "analytics", "go.mod"), []byte("module fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(DetectPresets(root), ",")
	for _, want := range []string{"go", "node"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detected presets %q missing %q", got, want)
		}
	}
}

func TestPresetExcludesPreservesCase(t *testing.T) {
	got := strings.Join(PresetExcludes([]string{"dotnet"}), "\n")
	if !strings.Contains(got, "TestResults") {
		t.Fatalf("expected TestResults case to be preserved, got:\n%s", got)
	}
}

func TestPresetExcludesCoverNodeAndGoGeneratedOutputs(t *testing.T) {
	got := strings.Join(PresetExcludes([]string{"node", "go"}), "\n")
	for _, want := range []string{"*.tsbuildinfo", "bin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected generated output exclude %q, got:\n%s", want, got)
		}
	}
}

func TestWriteExcludeFileIncludesPresetExcludes(t *testing.T) {
	root := t.TempDir()
	path, err := writeExcludeFile(Profile{
		ID:         "abc1234",
		Name:       "project",
		Enabled:    true,
		LocalRoot:  root,
		Worker:     "dev@linux-builder",
		RemotePath: "~/workspace/project",
		Presets:    []string{"python"},
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
	for _, want := range []string{".venv", ".pytest_cache", "__pycache__"} {
		if !strings.Contains(got, want) {
			t.Fatalf("exclude file missing %q:\n%s", want, got)
		}
	}
}
