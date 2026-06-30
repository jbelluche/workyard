package local_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

var (
	repoRoot    string
	workyardBin string
)

func TestMain(m *testing.M) {
	var err error
	repoRoot, err = findRepoRoot()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "find repo root: %v\n", err)
		os.Exit(1)
	}
	tmp, err := os.MkdirTemp("", "workyard-local-integration-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	workyardBin = filepath.Join(tmp, "workyard")
	if runtime.GOOS == "windows" {
		workyardBin += ".exe"
	}
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/jackbelluche/workyard/internal/cli.Version=integration-test", "-o", workyardBin, "./cmd/workyard")
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(tmp)
		_, _ = fmt.Fprintf(os.Stderr, "build workyard binary: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

func TestBinaryCommandWeirdnessAndDryRunUpdate(t *testing.T) {
	env := newCLIEnv(t)

	res := env.workyard("version")
	res.assertExit(t, 0)
	res.assertStdoutContains(t, "integration-test")

	res = env.workyard("mirror", "help")
	res.assertExit(t, 0)
	res.assertStdoutContains(t, "Continuously mirror registered directories to workers")
	res.assertStdoutContains(t, "Usage:")
	res.assertStdoutContains(t, "setup")
	assertNotExists(t, filepath.Join(env.stateDir, "local", "mirror.pid"))

	res = env.workyard("mirror", "definitely-not-a-command")
	res.assertNonZero(t)
	res.assertCombinedContains(t, `unknown command "definitely-not-a-command"`)
	if strings.Contains(res.combined(), "no mirrors are configured") {
		t.Fatalf("unknown mirror command started the foreground sync path:\n%s", res.combined())
	}
	assertNotExists(t, filepath.Join(env.stateDir, "local", "mirror.pid"))

	res = env.workyard("--json", "mirror", "sync")
	res.assertNonZero(t)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", false)
	assertJSONErrorCode(t, payload, "MIRROR_NONE_CONFIGURED")

	res = env.workyard("--json", "mirror", "exec", "echo", "hi")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "MIRROR_EXEC_ARGS_INVALID")

	res = env.workyard("--json", "upgrade", "--dry-run", "--method", "source", "--ref", "main", "--install-dir", filepath.Join(env.home, ".local", "bin"))
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONString(t, payload, "method", "source")
	assertJSONString(t, payload, "ref", "main")
	assertJSONString(t, payload, "installDir", filepath.Join(env.home, ".local", "bin"))
	if _, ok := payload["installerArgs"].([]any); !ok {
		t.Fatalf("installerArgs missing or wrong type in update dry-run payload: %#v", payload)
	}

	res = env.workyard("--json", "status")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "WORKER_REQUIRED")

	env.writeGlobalConfig(t, "[defaults\nssh_user =")
	res = env.workyard("--json", "config", "show")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "GLOBAL_CONFIG_INVALID")
}

func TestBinaryGlobalConfigWorkersAndMirrorRegistryFlow(t *testing.T) {
	env := newCLIEnv(t)
	env.writeGlobalConfig(t, `
[defaults]
ssh_user = "dev"
remote_workspace = "/srv/workspaces"

[[known_hosts]]
name = "devbox"
host = "devbox.example.test"
`)

	res := env.workyard("--json", "config", "show")
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONString(t, payload, "path", filepath.Join(env.stateDir, "config.toml"))
	assertWorkerPresent(t, payload, "devbox", "dev@devbox.example.test", "/srv/workspaces")

	res = env.workyard("--json", "workers", "list")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertWorkerListRow(t, payload, "localhost", "builtin", "local")
	assertWorkerListRow(t, payload, "devbox", "config", "dev@devbox.example.test")

	res = env.workyard("--json", "workers", "remove", "devbox")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "WORKER_CONFIG_READONLY")

	projectA := env.mkdir(t, "work/project-a/project")
	projectB := env.mkdir(t, "work/project-b/project")
	env.writeMirrorRegistry(t, projectA, projectB)

	res = env.workyard("--json", "mirror", "list")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertMirrorEnabled(t, payload, "abc123", true)
	assertMirrorEnabled(t, payload, "def456", true)

	res = env.workyard("--json", "mirror", "pause", "project")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "MIRROR_AMBIGUOUS")

	res = env.workyard("--json", "mirror", "pause", "abc123")
	res.assertExit(t, 0)
	assertNestedBool(t, res.jsonObject(t), []string{"mirror", "enabled"}, false)

	res = env.workyard("--json", "mirror", "resume", "abc123")
	res.assertExit(t, 0)
	assertNestedBool(t, res.jsonObject(t), []string{"mirror", "enabled"}, true)

	res = env.workyard("--json", "mirror", "rename", "abc123", "renamed-project")
	res.assertExit(t, 0)
	assertNestedString(t, res.jsonObject(t), []string{"mirror", "name"}, "renamed-project")

	res = env.workyard("--json", "mirror", "status")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONBool(t, payload, "running", false)
	assertMirrorPresent(t, payload, "abc123")
	assertMirrorPresent(t, payload, "def456")

	res = env.workyard("--json", "mirror", "delete", "def456")
	res.assertExit(t, 0)
	assertNestedString(t, res.jsonObject(t), []string{"mirror", "id"}, "def456")

	res = env.workyard("--json", "mirror", "list")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertMirrorPresent(t, payload, "abc123")
	assertMirrorMissing(t, payload, "def456")

	res = env.workyard("mirror", "list")
	res.assertExit(t, 0)
	res.assertStdoutContains(t, "ID")
	res.assertStdoutContains(t, "NAME")
	res.assertStdoutContains(t, "renamed-project")
}

func TestBinaryMirrorSetupRejectsDirtyDestination(t *testing.T) {
	env := newCLIEnv(t)
	env.writeFakeSSH(t, fakeSSHScript(map[string]string{
		`printf %s "$HOME"`: "/home/dev",
		"find \"$dest\"":    "non-empty\n",
	}))
	project := env.mkdir(t, "dirty-destination-project")
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte(`{"private":true}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := env.workyard("--json", "--worker", "dev@fake-worker", "mirror", "setup", "--local", project, "--remote-path", "~/workspace/project", "--name", "project", "--yes")
	res.assertNonZero(t)
	payload := res.jsonObject(t)
	assertJSONErrorCode(t, payload, "MIRROR_DESTINATION_NOT_READY")
	if !strings.Contains(fmt.Sprint(nestedValue(payload, []string{"error", "message"})), "destination contains existing files") {
		t.Fatalf("expected dirty destination message, got %#v", payload)
	}

	res = env.workyard("--json", "--worker", "dev@fake-worker", "mirror", "setup", "--local", project, "--remote-path", "/", "--name", "bad-path", "--yes")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "MIRROR_CONFIG_INVALID")
}

func TestBinaryMirrorSetupReportsUnreachableSSH(t *testing.T) {
	env := newCLIEnv(t)
	env.writeFakeSSH(t, "#!/bin/sh\nexit 255\n")
	project := env.mkdir(t, "unreachable-project")

	res := env.workyard("--json", "--worker", "dev@offline-worker", "mirror", "setup", "--local", project, "--remote-path", "~/workspace/unreachable-project", "--name", "unreachable-project", "--yes")
	res.assertNonZero(t)
	assertJSONErrorCode(t, res.jsonObject(t), "MIRROR_DESTINATION_CHECK_FAILED")
}

func TestBinaryMirrorDoctorFixesStalePIDAndReportsDestination(t *testing.T) {
	requireCommand(t, "rsync")
	env := newCLIEnv(t)
	env.writeFakeSSH(t, fakeSSHScript(map[string]string{
		`printf %s "$HOME"`:  "/home/dev",
		"command -v rsync":   "",
		"find \"$dest\"":     "missing\n",
		"mkdir -p \"$dest\"": "",
	}))
	project := env.mkdir(t, "doctor-project")
	env.writeSingleMirrorRegistry(t, "abc123", "doctor-project", project, "dev@fake-worker", "~/workspace/doctor-project", []string{"node"})
	pidPath := filepath.Join(env.stateDir, "local", "mirror.pid")
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, []byte("999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	res := env.workyard("--json", "mirror", "doctor", "--fix", "abc123")
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertFixAction(t, payload, "pid-file", "fixed")
	assertFixAction(t, payload, "create-destination", "fixed")
	assertNotExists(t, pidPath)
}

func TestBinaryMirrorTmuxReportsMissingTmux(t *testing.T) {
	env := newCLIEnv(t)
	env.writeFakeSSH(t, fakeSSHScript(map[string]string{
		"command -v tmux": "tmux-missing\n",
	}))
	project := env.mkdir(t, "tmux-project")
	env.writeSingleMirrorRegistry(t, "abc123", "tmux-project", project, "dev@fake-worker", "~/workspace/tmux-project", nil)

	res := env.workyard("--json", "mirror", "tmux", "list", "abc123")
	res.assertExit(t, 0)
	assertTmuxListStatus(t, res.jsonObject(t), "abc123", "tmux-missing")

	res = env.workyard("--json", "mirror", "tmux", "kill", "abc123")
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", false)
	assertNestedString(t, payload, []string{"session", "status"}, "tmux-missing")
}

func TestBinaryLocalDeployLifecycleSmoke(t *testing.T) {
	requireCommand(t, "python3")
	requireCommand(t, "rsync")

	env := newCLIEnv(t)
	runID := "local-smoke"
	fixture := filepath.Join(repoRoot, "fixtures", "health-server")

	t.Cleanup(func() {
		_ = env.workyardWithTimeout(30*time.Second, "--json", "--worker", "localhost", "--project", fixture, "--run", runID, "cleanup", "run").err
		_ = env.workyardWithTimeout(15*time.Second, "--json", "daemon", "stop").err
	})

	res := env.workyardWithTimeout(90*time.Second, "--json", "--worker", "localhost", "--run", runID, "deploy", "--skip-doctor", "--timeout", "30s", fixture)
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONString(t, payload, "worker", "localhost")
	assertJSONString(t, payload, "project", "workyard-health-fixture")
	assertJSONString(t, payload, "runId", runID)
	assertDeployStep(t, payload, "sync")
	assertDeployStep(t, payload, "setup")
	assertDeployStep(t, payload, "build")
	assertDeployStep(t, payload, "start")
	assertDeployStep(t, payload, "wait")
	assertURLHealthy(t, payload, "web")

	res = env.workyard("--json", "--worker", "localhost", "--project", fixture, "--run", runID, "status")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertServiceStatusIn(t, payload, "web", []string{"running", "healthy"}, true)

	res = env.workyard("--json", "--worker", "localhost", "--project", fixture, "--run", runID, "urls")
	res.assertExit(t, 0)
	assertURLHealthy(t, res.jsonObject(t), "web")

	res = env.workyard("--json", "--worker", "localhost", "--project", fixture, "--run", runID, "logs", "--tail", "20", "web")
	res.assertExit(t, 0)
	if !strings.Contains(res.stdout, `"service":"web"`) && !strings.Contains(res.stdout, "fixture listening") {
		t.Fatalf("expected web logs to include service output, stdout:\n%s\nstderr:\n%s", res.stdout, res.stderr)
	}

	res = env.workyard("--json", "--worker", "localhost", "--project", fixture, "--run", runID, "stop", "--all")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertServiceStatus(t, payload, "web", "stopped", false)
}

type cliEnv struct {
	home     string
	stateDir string
	workDir  string
	binDir   string
}

type commandResult struct {
	args     []string
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func newCLIEnv(t *testing.T) cliEnv {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "wy-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	stateDir := filepath.Join(home, ".workyard")
	workDir := filepath.Join(root, "workspace")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{home, stateDir, workDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return cliEnv{home: home, stateDir: stateDir, workDir: workDir, binDir: binDir}
}

func (e cliEnv) workyard(args ...string) commandResult {
	return e.workyardWithTimeout(45*time.Second, args...)
}

func (e cliEnv) workyardWithTimeout(timeout time.Duration, args ...string) commandResult {
	all := append([]string{"--state-dir", e.stateDir}, args...)
	return e.runWithTimeout(timeout, all...)
}

func (e cliEnv) runWithTimeout(timeout time.Duration, args ...string) commandResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, workyardBin, args...)
	cmd.Dir = e.workDir
	cmd.Env = append(os.Environ(),
		"HOME="+e.home,
		"PATH="+e.binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"WORKYARD_COLOR=never",
		"NO_COLOR=1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return commandResult{args: args, stdout: stdout.String(), stderr: stderr.String(), exitCode: -1, err: ctx.Err()}
	}
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if ok := asExitError(err, &exitErr); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return commandResult{args: args, stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode, err: err}
}

func (e cliEnv) mkdir(t *testing.T, rel string) string {
	t.Helper()
	dir := filepath.Join(e.workDir, rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func (e cliEnv) writeGlobalConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.stateDir, "config.toml"), []byte(strings.TrimSpace(body)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e cliEnv) writeMirrorRegistry(t *testing.T, projectA, projectB string) {
	t.Helper()
	e.writeMirrorRegistryBody(t, fmt.Sprintf(`mirrors:
  - id: abc123
    name: project
    enabled: true
    localRoot: %q
    worker: dev@linux-builder
    remotePath: ~/workspace/project-a
    delete: true
    includeGit: true
    presets:
      - node
      - go
    registeredAt: 2026-01-01T00:00:00Z
    updatedAt: 2026-01-01T00:00:00Z
  - id: def456
    name: project
    enabled: true
    localRoot: %q
    worker: dev@linux-builder
    remotePath: ~/workspace/project-b
    delete: true
    includeGit: true
    presets:
      - python
    registeredAt: 2026-01-01T00:00:00Z
    updatedAt: 2026-01-01T00:00:00Z
`, filepath.ToSlash(projectA), filepath.ToSlash(projectB)))
}

func (e cliEnv) writeSingleMirrorRegistry(t *testing.T, id, name, localRoot, worker, remotePath string, presets []string) {
	t.Helper()
	var presetLines strings.Builder
	if len(presets) > 0 {
		presetLines.WriteString("    presets:\n")
		for _, preset := range presets {
			presetLines.WriteString("      - " + preset + "\n")
		}
	}
	e.writeMirrorRegistryBody(t, fmt.Sprintf(`mirrors:
  - id: %s
    name: %s
    enabled: true
    localRoot: %q
    worker: %s
    remotePath: %s
    delete: true
    includeGit: true
%s    registeredAt: 2026-01-01T00:00:00Z
    updatedAt: 2026-01-01T00:00:00Z
`, id, name, filepath.ToSlash(localRoot), worker, remotePath, presetLines.String()))
}

func (e cliEnv) writeMirrorRegistryBody(t *testing.T, body string) {
	t.Helper()
	path := filepath.Join(e.stateDir, "local", "mirrors.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e cliEnv) writeFakeSSH(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(e.binDir, "ssh")
	if runtime.GOOS == "windows" {
		t.Skip("fake ssh integration helpers are shell-based")
	}
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func fakeSSHScript(matches map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\nset -eu\nlast=''\nfor arg do last=\"$arg\"; done\n")
	keys := make([]string, 0, len(matches))
	for needle := range matches {
		keys = append(keys, needle)
	}
	sort.Strings(keys)
	for _, needle := range keys {
		output := matches[needle]
		b.WriteString("if printf '%s' \"$last\" | grep -F -- " + shellQuote(needle) + " >/dev/null 2>&1; then printf '%s' " + shellQuote(output) + "; exit 0; fi\n")
	}
	b.WriteString("printf 'unexpected fake ssh command: %s\\n' \"$last\" >&2\nexit 42\n")
	return b.String()
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (r commandResult) assertExit(t *testing.T, want int) {
	t.Helper()
	if r.exitCode != want {
		t.Fatalf("workyard %s exit=%d want=%d err=%v\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), r.exitCode, want, r.err, r.stdout, r.stderr)
	}
}

func (r commandResult) assertNonZero(t *testing.T) {
	t.Helper()
	if r.exitCode == 0 {
		t.Fatalf("workyard %s succeeded unexpectedly\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), r.stdout, r.stderr)
	}
}

func (r commandResult) assertStdoutContains(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(r.stdout, want) {
		t.Fatalf("stdout missing %q for workyard %s\nstdout:\n%s\nstderr:\n%s", want, strings.Join(r.args, " "), r.stdout, r.stderr)
	}
}

func (r commandResult) assertCombinedContains(t *testing.T, want string) {
	t.Helper()
	if !strings.Contains(r.combined(), want) {
		t.Fatalf("output missing %q for workyard %s\nstdout:\n%s\nstderr:\n%s", want, strings.Join(r.args, " "), r.stdout, r.stderr)
	}
}

func (r commandResult) combined() string {
	return r.stdout + "\n" + r.stderr
}

func (r commandResult) jsonObject(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("decode JSON for workyard %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), err, r.stdout, r.stderr)
	}
	return out
}

func findRepoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return "", err
	}
	return root, nil
}

func requireCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is required for this integration test: %v", name, err)
	}
}

func assertNotExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist, stat err=%v", path, err)
	}
}

func assertJSONBool(t *testing.T, payload map[string]any, key string, want bool) {
	t.Helper()
	got, ok := payload[key].(bool)
	if !ok || got != want {
		t.Fatalf("%s=%#v, want %t in %#v", key, payload[key], want, payload)
	}
}

func assertJSONString(t *testing.T, payload map[string]any, key, want string) {
	t.Helper()
	got, ok := payload[key].(string)
	if !ok || got != want {
		t.Fatalf("%s=%#v, want %q in %#v", key, payload[key], want, payload)
	}
}

func assertJSONErrorCode(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	errObj, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error object missing in %#v", payload)
	}
	got, _ := errObj["code"].(string)
	if got != want {
		t.Fatalf("error.code=%q, want %q in %#v", got, want, payload)
	}
}

func assertFixAction(t *testing.T, payload map[string]any, action, status string) {
	t.Helper()
	fixes, ok := payload["fixes"].(map[string]any)
	if !ok {
		t.Fatalf("fixes missing in %#v", payload)
	}
	actions, ok := fixes["actions"].([]any)
	if !ok {
		t.Fatalf("fixes.actions missing in %#v", payload)
	}
	for _, item := range actions {
		row, ok := item.(map[string]any)
		if ok && row["action"] == action && row["status"] == status {
			return
		}
	}
	t.Fatalf("fix action %q status %q missing in %#v", action, status, actions)
}

func assertTmuxListStatus(t *testing.T, payload map[string]any, id, status string) {
	t.Helper()
	sessions, ok := payload["sessions"].([]any)
	if !ok {
		t.Fatalf("sessions missing in %#v", payload)
	}
	for _, item := range sessions {
		session, ok := item.(map[string]any)
		if ok && session["id"] == id {
			if session["status"] != status {
				t.Fatalf("session %s status=%#v, want %q in %#v", id, session["status"], status, session)
			}
			return
		}
	}
	t.Fatalf("session id %q missing in %#v", id, sessions)
}

func assertNestedString(t *testing.T, payload map[string]any, path []string, want string) {
	t.Helper()
	got, ok := nestedValue(payload, path).(string)
	if !ok || got != want {
		t.Fatalf("%s=%#v, want %q in %#v", strings.Join(path, "."), nestedValue(payload, path), want, payload)
	}
}

func assertNestedBool(t *testing.T, payload map[string]any, path []string, want bool) {
	t.Helper()
	got, ok := nestedValue(payload, path).(bool)
	if !ok || got != want {
		t.Fatalf("%s=%#v, want %t in %#v", strings.Join(path, "."), nestedValue(payload, path), want, payload)
	}
}

func nestedValue(payload map[string]any, path []string) any {
	var cur any = payload
	for _, part := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[part]
	}
	return cur
}

func assertWorkerPresent(t *testing.T, payload map[string]any, name, target, workspace string) {
	t.Helper()
	workers, ok := payload["workers"].([]any)
	if !ok {
		t.Fatalf("workers missing or wrong type: %#v", payload)
	}
	for _, item := range workers {
		worker, ok := item.(map[string]any)
		if !ok {
			continue
		}
		workerTarget, _ := worker["sshTarget"].(string)
		if workerTarget == "" {
			user, _ := worker["user"].(string)
			host, _ := worker["host"].(string)
			if user != "" && host != "" {
				workerTarget = user + "@" + host
			}
		}
		if worker["name"] == name && workerTarget == target && worker["remoteWorkspace"] == workspace {
			return
		}
	}
	t.Fatalf("worker %q target=%q workspace=%q missing in %#v", name, target, workspace, payload)
}

func assertWorkerListRow(t *testing.T, payload map[string]any, name, source, target string) {
	t.Helper()
	workers, ok := payload["workers"].([]any)
	if !ok {
		t.Fatalf("workers missing or wrong type: %#v", payload)
	}
	for _, item := range workers {
		worker, ok := item.(map[string]any)
		if !ok || worker["name"] != name {
			continue
		}
		if worker["source"] != source || worker["sshTarget"] != target {
			t.Fatalf("worker row for %s=%#v, want source=%q target=%q", name, worker, source, target)
		}
		return
	}
	t.Fatalf("worker row %q missing in %#v", name, payload)
}

func assertMirrorPresent(t *testing.T, payload map[string]any, id string) {
	t.Helper()
	if mirrorByID(payload, id) == nil {
		t.Fatalf("mirror %q missing in %#v", id, payload)
	}
}

func assertMirrorMissing(t *testing.T, payload map[string]any, id string) {
	t.Helper()
	if mirrorByID(payload, id) != nil {
		t.Fatalf("mirror %q unexpectedly present in %#v", id, payload)
	}
}

func assertMirrorEnabled(t *testing.T, payload map[string]any, id string, want bool) {
	t.Helper()
	mirror := mirrorByID(payload, id)
	if mirror == nil {
		t.Fatalf("mirror %q missing in %#v", id, payload)
	}
	got, ok := mirror["enabled"].(bool)
	if !ok || got != want {
		t.Fatalf("mirror %s enabled=%#v, want %t in %#v", id, mirror["enabled"], want, mirror)
	}
}

func mirrorByID(payload map[string]any, id string) map[string]any {
	mirrors, _ := payload["mirrors"].([]any)
	for _, item := range mirrors {
		mirror, ok := item.(map[string]any)
		if ok && mirror["id"] == id {
			return mirror
		}
	}
	return nil
}

func assertDeployStep(t *testing.T, payload map[string]any, name string) {
	t.Helper()
	steps, ok := payload["steps"].([]any)
	if !ok {
		t.Fatalf("steps missing or wrong type: %#v", payload)
	}
	for _, item := range steps {
		step, ok := item.(map[string]any)
		if ok && step["name"] == name && step["ok"] == true {
			return
		}
	}
	t.Fatalf("step %q missing or not ok in %#v", name, steps)
}

func assertURLHealthy(t *testing.T, payload map[string]any, service string) {
	t.Helper()
	urls, ok := payload["urls"].([]any)
	if !ok {
		t.Fatalf("urls missing or wrong type: %#v", payload)
	}
	for _, item := range urls {
		url, ok := item.(map[string]any)
		value := fmt.Sprint(url["url"])
		if ok && url["service"] == service && url["healthy"] == true && (strings.HasPrefix(value, "http://127.0.0.1:") || strings.HasPrefix(value, "http://localhost:")) {
			return
		}
	}
	t.Fatalf("healthy URL for %q missing in %#v", service, urls)
}

func assertServiceStatus(t *testing.T, payload map[string]any, service, status string, healthy bool) {
	t.Helper()
	assertServiceStatusIn(t, payload, service, []string{status}, healthy)
}

func assertServiceStatusIn(t *testing.T, payload map[string]any, service string, statuses []string, healthy bool) {
	t.Helper()
	services, ok := payload["services"].([]any)
	if !ok {
		t.Fatalf("services missing or wrong type: %#v", payload)
	}
	valid := map[string]bool{}
	for _, status := range statuses {
		valid[status] = true
	}
	for _, item := range services {
		svc, ok := item.(map[string]any)
		if ok && svc["name"] == service {
			gotStatus, _ := svc["status"].(string)
			if !valid[gotStatus] || svc["healthy"] != healthy {
				t.Fatalf("service %s=%#v, want status in %v healthy=%t", service, svc, statuses, healthy)
			}
			return
		}
	}
	t.Fatalf("service %q missing in %#v", service, services)
}

func asExitError(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = exitErr
	return true
}
