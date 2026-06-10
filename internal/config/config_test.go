package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFixtureConfig(t *testing.T) {
	loaded, err := Load("../../fixtures/health-server")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Name != "workyard-health-fixture" {
		t.Fatalf("unexpected project name %q", loaded.Config.Name)
	}
	web := loaded.Config.Services["web"]
	if web.Port.Default != 3000 || web.Port.Env != "PORT" {
		t.Fatalf("unexpected port config %#v", web.Port)
	}
}

func TestValidateRejectsServiceTraversal(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Name: "bad",
		Root: dir,
		Services: map[string]Service{
			"web": {Path: "../outside", StartCommand: "python3 server.py", Port: PortConfig{Default: 3000, Env: "PORT"}},
		},
	}
	if _, err := Validate(&cfg); err == nil {
		t.Fatal("expected traversal service path to be rejected")
	}
}

func TestValidateRejectsServiceSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "linked-service")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	cfg := Config{
		Name: "bad-link",
		Root: root,
		Services: map[string]Service{
			"web": {Path: "linked-service", StartCommand: "python3 server.py", Port: PortConfig{Default: 3000, Env: "PORT"}},
		},
	}
	if _, err := Validate(&cfg); err == nil {
		t.Fatal("expected symlinked service path escape to be rejected")
	}
}

func TestValidateRejectsPublicHealthURL(t *testing.T) {
	cfg := Config{
		Name: "bad-health",
		Root: t.TempDir(),
		Services: map[string]Service{
			"web": {Path: ".", StartCommand: "python3 server.py", Port: PortConfig{Default: 3000, Env: "PORT"}, Health: HealthConfig{URL: "https://example.com/health"}},
		},
	}
	if _, err := Validate(&cfg); err == nil {
		t.Fatal("expected public health URL to be rejected")
	}
}

func TestValidateRejectsLinkLocalHealthURL(t *testing.T) {
	cfg := Config{
		Name: "bad-health",
		Root: t.TempDir(),
		Services: map[string]Service{
			"web": {Path: ".", StartCommand: "python3 server.py", Port: PortConfig{Default: 3000, Env: "PORT"}, Health: HealthConfig{URL: "http://169.254.169.254/latest/meta-data"}},
		},
	}
	if _, err := Validate(&cfg); err == nil {
		t.Fatal("expected link-local health URL to be rejected")
	}
}

func TestValidateRejectsInvalidEnvNames(t *testing.T) {
	cfg := Config{
		Name: "bad-env",
		Root: t.TempDir(),
		Services: map[string]Service{
			"web": {Path: ".", StartCommand: "python3 server.py", Port: PortConfig{Default: 3000, Env: "BAD-NAME"}, Env: map[string]string{"ALSO-BAD": "x"}},
		},
	}
	if _, err := Validate(&cfg); err == nil {
		t.Fatal("expected invalid env names to be rejected")
	}
}

func TestFindFromChildDirectory(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name: x\nservices:\n  web:\n    path: .\n    startCommand: python3 -m http.server\n    port: 3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, root, err := Find(child)
	if err != nil {
		t.Fatal(err)
	}
	if root != dir || path != filepath.Join(dir, FileName) {
		t.Fatalf("path=%s root=%s", path, root)
	}
}

func TestLoadRejectsOldCommandField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("name: x\nservices:\n  web:\n    path: .\n    command: python3 -m http.server\n    port: 3000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("expected old command field to be rejected")
	}
}

func TestWriteUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, DefaultConfig("fixture")); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := stat.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode=%#o, want 0600", got)
	}
}

func TestLoadRewritesUnknownFieldErrors(t *testing.T) {
	dir := t.TempDir()
	content := "name: bad\nservices:\n  web:\n    startCmd: npm run dev\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	if err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown field "startCmd"`) {
		t.Fatalf("missing friendly message: %s", msg)
	}
	if !strings.Contains(msg, `did you mean "startCommand"`) {
		t.Fatalf("missing suggestion: %s", msg)
	}
	if strings.Contains(msg, "config.Service") {
		t.Fatalf("leaked Go type name: %s", msg)
	}
}
