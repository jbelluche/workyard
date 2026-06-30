package remote_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const remoteIntegrationEnv = "WORKYARD_REMOTE_INTEGRATION"

func TestRemoteHealthFixtureDeploySmoke(t *testing.T) {
	worker := remoteWorker(t)
	requireCommand(t, "go")
	requireCommand(t, "rsync")
	requireCommand(t, "ssh")

	env := newRemoteCLIEnv(t)
	runID := "remote-smoke-" + time.Now().UTC().Format("150405")
	fixture := filepath.Join(env.repoRoot, "fixtures", "health-server")
	artifactDir := filepath.Join(env.root, "artifacts")

	t.Cleanup(func() {
		_ = env.workyardWithTimeout(90*time.Second, "--json", "--worker", worker, "--project", fixture, "--run", runID, "cleanup", "run").err
		home := remoteHome(t, worker)
		cleanupRemotePath(t, worker, home+"/.workyard/runs/workyard-health-fixture/"+runID)
	})

	res := env.workyardWithTimeout(4*time.Minute, "--json", "--worker", worker, "--run", runID, "deploy", "--install", "--fresh", "--skip-doctor", "--timeout", "45s", "--artifact-dir", artifactDir, fixture)
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONString(t, payload, "project", "workyard-health-fixture")
	assertJSONString(t, payload, "runId", runID)
	assertDeployStep(t, payload, "install")
	assertDeployStep(t, payload, "sync")
	assertDeployStep(t, payload, "start")
	assertDeployStep(t, payload, "wait")
	assertURLHealthy(t, payload, "web")

	res = env.workyardWithTimeout(90*time.Second, "--json", "--worker", worker, "--project", fixture, "--run", runID, "status")
	res.assertExit(t, 0)
	assertServiceStatusIn(t, res.jsonObject(t), "web", []string{"running", "healthy"}, true)
}

func TestRemoteMirrorSetupSyncDoctorDeleteSmoke(t *testing.T) {
	worker := remoteWorker(t)
	requireCommand(t, "rsync")
	requireCommand(t, "ssh")

	env := newRemoteCLIEnv(t)
	runID := "mirror-smoke-" + time.Now().UTC().Format("150405")
	fixture := filepath.Join(env.repoRoot, "fixtures", "health-server")
	home := remoteHome(t, worker)
	mirrorRoot := home + "/.workyard/runs/workyard-mirror-integration/" + runID
	remotePath := mirrorRoot + "/source"
	cleanupRemotePath(t, worker, mirrorRoot)
	t.Cleanup(func() { cleanupRemotePath(t, worker, mirrorRoot) })

	res := env.workyardWithTimeout(60*time.Second, "--json", "--worker", worker, "mirror", "setup", "--local", fixture, "--remote-path", remotePath, "--name", runID, "--yes")
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	id := nestedString(t, payload, []string{"mirror", "id"})
	if id == "" {
		t.Fatalf("mirror setup did not return an id: %#v", payload)
	}
	assertNestedString(t, payload, []string{"destination", "state"}, "missing")
	assertNestedStringSliceContains(t, payload, []string{"mirror", "presets"}, "python")

	res = env.workyardWithTimeout(90*time.Second, "--json", "mirror", "sync", id)
	res.assertExit(t, 0)
	syncPayload := res.firstJSONObjectLine(t)
	assertJSONBool(t, syncPayload, "ok", true)
	assertJSONString(t, syncPayload, "id", id)
	assertJSONString(t, syncPayload, "resolvedPath", remotePath)
	assertRemoteMirrorMarker(t, worker, remotePath, id, runID)

	res = env.workyardWithTimeout(30*time.Second, "mirror", "exec", id, "--", "pwd")
	res.assertExit(t, 0)
	if !strings.Contains(res.stdout, remotePath) {
		t.Fatalf("mirror exec did not run in remote path %s\nstdout:\n%s\nstderr:\n%s", remotePath, res.stdout, res.stderr)
	}

	res = env.workyardWithTimeout(30*time.Second, "mirror", "shell", "--command", "pwd", id)
	res.assertExit(t, 0)
	if !strings.Contains(res.stdout, remotePath) {
		t.Fatalf("mirror shell --command did not run in remote path %s\nstdout:\n%s\nstderr:\n%s", remotePath, res.stdout, res.stderr)
	}

	res = env.workyardWithTimeout(60*time.Second, "--json", "mirror", "doctor", id)
	res.assertExit(t, 0)
	assertJSONBool(t, res.jsonObject(t), "ok", true)

	if remoteHasCommand(t, worker, "tmux") {
		createRemoteTmuxSession(t, worker, "workyard-"+id, remotePath)
		t.Cleanup(func() { killRemoteTmuxSession(t, worker, "workyard-"+id) })
		res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "tmux", "list", id)
		res.assertExit(t, 0)
		assertTmuxListStatus(t, res.jsonObject(t), id, "running")

		res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "tmux", "kill", id)
		res.assertExit(t, 0)
		payload := res.jsonObject(t)
		assertJSONBool(t, payload, "ok", true)
		assertNestedString(t, payload, []string{"session", "status"}, "killed")
	} else {
		res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "tmux", "list", id)
		res.assertExit(t, 0)
		assertTmuxListStatus(t, res.jsonObject(t), id, "tmux-missing")

		res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "tmux", "kill", id)
		res.assertExit(t, 0)
		payload := res.jsonObject(t)
		assertJSONBool(t, payload, "ok", false)
		assertNestedString(t, payload, []string{"session", "status"}, "tmux-missing")
	}

	res = env.workyardWithTimeout(45*time.Second, "--json", "mirror", "start")
	res.assertExit(t, 0)
	assertJSONBool(t, res.jsonObject(t), "running", true)
	t.Cleanup(func() { _ = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "stop").err })

	res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "status", id)
	res.assertExit(t, 0)
	assertJSONBool(t, res.jsonObject(t), "running", true)

	res = env.workyardWithTimeout(30*time.Second, "--json", "mirror", "stop")
	res.assertExit(t, 0)
	assertJSONBool(t, res.jsonObject(t), "running", false)

	res = env.workyardWithTimeout(60*time.Second, "--json", "mirror", "delete", id, "--delete-remote", "--yes")
	res.assertExit(t, 0)
	payload = res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	assertJSONBool(t, payload, "remoteDeleted", true)
}

func TestRemoteMultiServiceMirrorPortConflictSmoke(t *testing.T) {
	worker := remoteWorker(t)
	requireCommand(t, "go")
	requireCommand(t, "rsync")
	requireCommand(t, "ssh")

	env := newRemoteCLIEnv(t)
	fixture := filepath.Join(env.repoRoot, "fixtures", "multi-service")
	artifactDir := filepath.Join(env.root, "artifacts")
	home := remoteHome(t, worker)
	suffix := time.Now().UTC().Format("150405")
	nameA := "multi-a-" + suffix
	nameB := "multi-b-" + suffix
	rootA := home + "/.workyard/runs/workyard-mirror-integration/" + nameA
	rootB := home + "/.workyard/runs/workyard-mirror-integration/" + nameB
	cleanupRemotePath(t, worker, rootA)
	cleanupRemotePath(t, worker, rootB)
	t.Cleanup(func() {
		cleanupRemotePath(t, worker, rootA)
		cleanupRemotePath(t, worker, rootB)
	})

	idA := setupRemoteMirror(t, env, worker, fixture, rootA+"/source", nameA)
	idB := setupRemoteMirror(t, env, worker, fixture, rootB+"/source", nameB)
	t.Cleanup(func() {
		cleanupMirrorServices(t, env, idB)
		deleteRemoteMirror(t, env, idB)
		cleanupMirrorServices(t, env, idA)
		deleteRemoteMirror(t, env, idA)
	})

	services := []string{"analytics", "api", "customer-ui", "events", "operator-ui"}
	resA := env.workyardWithTimeout(8*time.Minute, append([]string{
		"--json", "mirror", "services", "up",
		"--install",
		"--timeout", "180s",
		"--artifact-dir", artifactDir,
		idA,
	}, services...)...)
	resA.assertExit(t, 0)
	payloadA := resA.jsonObject(t)
	assertJSONBool(t, payloadA, "ok", true)
	assertJSONString(t, payloadA, "runId", idA)
	assertServicesHealthy(t, payloadA, services)
	portsA := assignedPorts(t, payloadA, services)

	resB := env.workyardWithTimeout(8*time.Minute, append([]string{
		"--json", "mirror", "services", "up",
		"--timeout", "180s",
		idB,
	}, services...)...)
	resB.assertExit(t, 0)
	payloadB := resB.jsonObject(t)
	assertJSONBool(t, payloadB, "ok", true)
	assertJSONString(t, payloadB, "runId", idB)
	assertServicesHealthy(t, payloadB, services)
	portsB := assignedPorts(t, payloadB, services)

	for _, service := range services {
		if portsA[service] == portsB[service] {
			t.Fatalf("service %s reused assigned port %d across simultaneous runs: first=%#v second=%#v", service, portsA[service], portsA, portsB)
		}
	}

	res := env.workyardWithTimeout(60*time.Second, "--json", "mirror", "services", "status", idA)
	res.assertExit(t, 0)
	assertServicesHealthy(t, res.jsonObject(t), services)

	res = env.workyardWithTimeout(60*time.Second, "--json", "mirror", "services", "status", idB)
	res.assertExit(t, 0)
	assertServicesHealthy(t, res.jsonObject(t), services)
}

type remoteCLIEnv struct {
	root     string
	home     string
	stateDir string
	repoRoot string
	bin      string
}

func setupRemoteMirror(t *testing.T, env remoteCLIEnv, worker, fixture, remotePath, name string) string {
	t.Helper()
	res := env.workyardWithTimeout(90*time.Second, "--json", "--worker", worker, "mirror", "setup", "--local", fixture, "--remote-path", remotePath, "--name", name, "--yes")
	res.assertExit(t, 0)
	payload := res.jsonObject(t)
	assertJSONBool(t, payload, "ok", true)
	id := nestedString(t, payload, []string{"mirror", "id"})
	if id == "" {
		t.Fatalf("mirror setup for %s returned no id: %#v", name, payload)
	}
	return id
}

func cleanupMirrorServices(t *testing.T, env remoteCLIEnv, id string) {
	t.Helper()
	if id == "" {
		return
	}
	_ = env.workyardWithTimeout(90*time.Second, "--json", "mirror", "services", "cleanup", id).err
}

func deleteRemoteMirror(t *testing.T, env remoteCLIEnv, id string) {
	t.Helper()
	if id == "" {
		return
	}
	_ = env.workyardWithTimeout(90*time.Second, "--json", "mirror", "delete", id, "--delete-remote", "--yes").err
}

type commandResult struct {
	args     []string
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func remoteWorker(t *testing.T) string {
	t.Helper()
	if os.Getenv(remoteIntegrationEnv) != "1" {
		t.Skipf("set %s=1 to run remote Workyard integration tests", remoteIntegrationEnv)
	}
	worker := strings.TrimSpace(os.Getenv("WORKYARD_REMOTE_WORKER"))
	if worker == "" {
		worker = "jack@jack-rasp-five"
	}
	return worker
}

func newRemoteCLIEnv(t *testing.T) remoteCLIEnv {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	base := os.TempDir()
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	root, err := os.MkdirTemp(base, "wy-remote-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home := filepath.Join(root, "home")
	stateDir := filepath.Join(home, ".workyard")
	for _, dir := range []string{home, stateDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "workyard")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-ldflags", "-X github.com/jackbelluche/workyard/internal/cli.Version=remote-integration-test", "-o", bin, "./cmd/workyard")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("build workyard binary: %v\nstderr:\n%s", err, stderr.String())
	}
	return remoteCLIEnv{root: root, home: home, stateDir: stateDir, repoRoot: repoRoot, bin: bin}
}

func (e remoteCLIEnv) workyardWithTimeout(timeout time.Duration, args ...string) commandResult {
	all := append([]string{"--state-dir", e.stateDir}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, all...)
	cmd.Dir = e.repoRoot
	cmd.Env = append(os.Environ(),
		"HOME="+e.home,
		"WORKYARD_COLOR=never",
		"NO_COLOR=1",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return commandResult{args: all, stdout: stdout.String(), stderr: stderr.String(), exitCode: -1, err: ctx.Err()}
	}
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return commandResult{args: all, stdout: stdout.String(), stderr: stderr.String(), exitCode: exitCode, err: err}
}

func (r commandResult) assertExit(t *testing.T, want int) {
	t.Helper()
	if r.exitCode != want {
		t.Fatalf("workyard %s exit=%d want=%d err=%v\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), r.exitCode, want, r.err, r.stdout, r.stderr)
	}
}

func (r commandResult) jsonObject(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("decode JSON for workyard %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), err, r.stdout, r.stderr)
	}
	return out
}

func (r commandResult) firstJSONObjectLine(t *testing.T) map[string]any {
	t.Helper()
	for _, line := range strings.Split(r.stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			t.Fatalf("decode JSON line for workyard %s: %v\nline:\n%s\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), err, line, r.stdout, r.stderr)
		}
		return out
	}
	t.Fatalf("no JSON lines for workyard %s\nstdout:\n%s\nstderr:\n%s", strings.Join(r.args, " "), r.stdout, r.stderr)
	return nil
}

func remoteHome(t *testing.T, worker string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", worker, `printf %s "$HOME"`)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("read remote home from %s: %v\nstdout:\n%s\nstderr:\n%s", worker, err, stdout.String(), stderr.String())
	}
	home := strings.TrimSpace(stdout.String())
	if home == "" || strings.ContainsAny(home, "\x00\r\n") {
		t.Fatalf("invalid remote home from %s: %q", worker, home)
	}
	return home
}

func cleanupRemotePath(t *testing.T, worker, target string) {
	t.Helper()
	if !strings.Contains(target, "/.workyard/runs/") {
		t.Fatalf("refusing to clean non-Workyard run path: %s", target)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := strings.Join([]string{
		"set -eu",
		"target=" + shellQuote(target),
		"case \"$target\" in */.workyard/runs/*) rm -rf -- \"$target\" ;; *) printf 'refusing path %s\\n' \"$target\" >&2; exit 2 ;; esac",
	}, "\n")
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", worker, script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Logf("remote cleanup failed for %s:%s: %v: %s", worker, target, err, stderr.String())
	}
}

func assertRemoteMirrorMarker(t *testing.T, worker, remotePath, id, name string) {
	t.Helper()
	markerDir := path.Join(path.Dir(remotePath), ".workyard-mirrors")
	pattern := path.Base(remotePath) + "-*.json"
	stdout, err := runRemoteScriptOutput(t, worker, strings.Join([]string{
		"set -eu",
		"marker=$(find " + shellQuote(markerDir) + " -maxdepth 1 -type f -name " + shellQuote(pattern) + " -print -quit)",
		"test -n \"$marker\"",
		"cat \"$marker\"",
	}, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	var marker map[string]any
	if err := json.Unmarshal([]byte(stdout), &marker); err != nil {
		t.Fatalf("decode remote mirror marker: %v\nmarker:\n%s", err, stdout)
	}
	if marker["id"] != id || marker["name"] != name || marker["remotePath"] != remotePath {
		t.Fatalf("unexpected remote marker: %#v", marker)
	}
}

func createRemoteTmuxSession(t *testing.T, worker, sessionName, dir string) {
	t.Helper()
	runRemoteScript(t, worker, strings.Join([]string{
		"set -eu",
		"session=" + shellQuote(sessionName),
		"dir=" + shellQuote(dir),
		"command -v tmux >/dev/null",
		"tmux kill-session -t \"=$session\" 2>/dev/null || true",
		"cd \"$dir\"",
		"tmux new-session -d -s \"$session\" 'sleep 300'",
	}, "\n"))
}

func remoteHasCommand(t *testing.T, worker, name string) bool {
	t.Helper()
	return runRemoteScriptAllowFailure(t, worker, "command -v "+shellQuote(name)+" >/dev/null") == nil
}

func killRemoteTmuxSession(t *testing.T, worker, sessionName string) {
	t.Helper()
	runRemoteScriptAllowFailure(t, worker, "tmux kill-session -t "+shellQuote("="+sessionName)+" 2>/dev/null || true")
}

func runRemoteScript(t *testing.T, worker, script string) {
	t.Helper()
	if err := runRemoteScriptAllowFailure(t, worker, script); err != nil {
		t.Fatal(err)
	}
}

func runRemoteScriptAllowFailure(t *testing.T, worker, script string) error {
	t.Helper()
	_, err := runRemoteScriptOutput(t, worker, script)
	return err
}

func runRemoteScriptOutput(t *testing.T, worker, script string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "--", worker, script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("remote script failed on %s: %w\nstdout:\n%s\nstderr:\n%s\nscript:\n%s", worker, err, stdout.String(), stderr.String(), script)
	}
	return stdout.String(), nil
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

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
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

func assertNestedString(t *testing.T, payload map[string]any, path []string, want string) {
	t.Helper()
	got := nestedString(t, payload, path)
	if got != want {
		t.Fatalf("%s=%#v, want %q in %#v", strings.Join(path, "."), got, want, payload)
	}
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
	t.Fatalf("session %q missing in %#v", id, sessions)
}

func nestedString(t *testing.T, payload map[string]any, path []string) string {
	t.Helper()
	var cur any = payload
	for _, part := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[part]
	}
	got, _ := cur.(string)
	return got
}

func assertNestedStringSliceContains(t *testing.T, payload map[string]any, path []string, want string) {
	t.Helper()
	var cur any = payload
	for _, part := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s missing in %#v", strings.Join(path, "."), payload)
		}
		cur = obj[part]
	}
	items, ok := cur.([]any)
	if !ok {
		t.Fatalf("%s=%#v, want string slice in %#v", strings.Join(path, "."), cur, payload)
	}
	for _, item := range items {
		if item == want {
			return
		}
	}
	t.Fatalf("%s missing %q in %#v", strings.Join(path, "."), want, items)
}

func assertServicesHealthy(t *testing.T, payload map[string]any, names []string) {
	t.Helper()
	for _, name := range names {
		assertServiceStatusIn(t, payload, name, []string{"running", "healthy"}, true)
	}
}

func assignedPorts(t *testing.T, payload map[string]any, names []string) map[string]int {
	t.Helper()
	services, ok := payload["services"].([]any)
	if !ok {
		t.Fatalf("services missing or wrong type: %#v", payload)
	}
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	ports := map[string]int{}
	for _, item := range services {
		svc, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := svc["name"].(string)
		if !want[name] {
			continue
		}
		portFloat, ok := svc["assignedPort"].(float64)
		if !ok || portFloat <= 0 {
			t.Fatalf("service %s has invalid assigned port in %#v", name, svc)
		}
		ports[name] = int(portFloat)
	}
	for _, name := range names {
		if ports[name] == 0 {
			t.Fatalf("service %s missing assigned port in %#v", name, services)
		}
	}
	return ports
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
		if ok && url["service"] == service && url["healthy"] == true {
			return
		}
	}
	t.Fatalf("healthy URL for %q missing in %#v", service, urls)
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
