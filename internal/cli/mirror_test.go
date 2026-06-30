package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackbelluche/workyard/internal/mirror"
	"github.com/jackbelluche/workyard/internal/output"
	"github.com/jackbelluche/workyard/internal/remote"
)

func TestMirrorHelpShowsHelpInsteadOfStartingSync(t *testing.T) {
	stateDir := t.TempDir()
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Continuously mirror registered directories to workers", "Usage:", "workyard mirror", "setup"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "no mirrors are configured") {
		t.Fatalf("mirror help started sync path:\n%s", got)
	}
}

func TestMirrorUnknownCommandFailsInsteadOfStartingSync(t *testing.T) {
	stateDir := t.TempDir()
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "definitely-not-a-command"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown mirror command to fail")
	}
	if !strings.Contains(err.Error(), `unknown command "definitely-not-a-command"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out.String(), "no mirrors are configured") {
		t.Fatalf("unknown command started sync path:\n%s", out.String())
	}
	if _, statErr := os.Stat(mirror.DefaultPIDPath(stateDir)); !os.IsNotExist(statErr) {
		t.Fatalf("pid file should not exist, stat err=%v", statErr)
	}
}

func TestMirrorPauseRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "pause", "project"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected ambiguous mirror name to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
	if !strings.Contains(ce.Hint, firstID) {
		t.Fatalf("hint %q missing id %q", ce.Hint, firstID)
	}
}

func TestMirrorPauseResumeByID(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "pause", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("pause by id: %v", err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profile, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get paused profile ok=%t err=%v", ok, err)
	}
	if profile.Enabled {
		t.Fatalf("expected profile to be paused: %#v", profile)
	}

	root = newRoot(&options{})
	out.Reset()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "resume", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("resume by id: %v", err)
	}
	profile, ok, err = store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get resumed profile ok=%t err=%v", ok, err)
	}
	if !profile.Enabled {
		t.Fatalf("expected profile to be resumed: %#v", profile)
	}
}

func TestMirrorListShowsIDColumn(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "list"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "ID") || !strings.Contains(got, firstID) {
		t.Fatalf("list output missing id column/id:\n%s", got)
	}
}

func TestMirrorSyncFailsWithoutConfiguredMirrors(t *testing.T) {
	stateDir := t.TempDir()
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "sync"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected sync without mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
}

func TestMirrorSyncUnknownRefFailsBeforeSync(t *testing.T) {
	stateDir, _ := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "sync", "missing"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected unknown sync ref to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NOT_FOUND" {
		t.Fatalf("code=%q, want MIRROR_NOT_FOUND", ce.Code)
	}
}

func TestMirrorDeleteByIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "delete", firstID})
	if err := root.Execute(); err != nil {
		t.Fatalf("delete by id: %v", err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	if _, ok, err := store.Get(firstID); err != nil || ok {
		t.Fatalf("deleted id still resolved ok=%t err=%v", ok, err)
	}
}

func TestMirrorRenameByID(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "rename", firstID, "renamed-project"})
	if err := root.Execute(); err != nil {
		t.Fatalf("rename by id: %v", err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profile, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get renamed profile ok=%t err=%v", ok, err)
	}
	if profile.Name != "renamed-project" {
		t.Fatalf("name=%q, want renamed-project", profile.Name)
	}
	if !strings.Contains(out.String(), firstID) {
		t.Fatalf("output missing id %q:\n%s", firstID, out.String())
	}
}

func TestMirrorRenameRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "rename", "project", "renamed-project"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected ambiguous mirror name to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
	if !strings.Contains(ce.Hint, firstID) {
		t.Fatalf("hint %q missing id %q", ce.Hint, firstID)
	}
}

func TestMirrorRenameKeepsDefaultTmuxSessionName(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	before, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get profile ok=%t err=%v", ok, err)
	}
	beforeSession, err := mirrorShellSessionName(before, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Rename(firstID, "renamed-project"); err != nil || !ok {
		t.Fatalf("rename ok=%t err=%v", ok, err)
	}
	after, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get renamed profile ok=%t err=%v", ok, err)
	}
	afterSession, err := mirrorShellSessionName(after, "")
	if err != nil {
		t.Fatal(err)
	}
	if beforeSession != afterSession {
		t.Fatalf("session changed after rename: before=%q after=%q", beforeSession, afterSession)
	}
	if afterSession != "workyard-"+firstID {
		t.Fatalf("session=%q, want workyard-%s", afterSession, firstID)
	}
}

func TestMirrorTmuxKillRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "tmux", "kill", "project"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected ambiguous mirror name to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
	if !strings.Contains(ce.Hint, firstID) {
		t.Fatalf("hint %q missing id %q", ce.Hint, firstID)
	}
}

func TestMirrorTmuxListReportsNoMirrorsConfigured(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", t.TempDir(), "mirror", "tmux", "list"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected tmux list without mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
}

func TestMirrorTmuxScriptsUseExactSessionTargets(t *testing.T) {
	inspect := mirrorTmuxInspectScript("workyard-abc123")
	kill := mirrorTmuxKillScript("workyard-abc123")
	shell := mirrorTmuxShellScript("/home/dev/workspace/project", "workyard-abc123")
	for name, script := range map[string]string{"inspect": inspect, "kill": kill, "shell": shell} {
		if !strings.Contains(script, "target==workyard-abc123") && !strings.Contains(script, "target='=workyard-abc123'") {
			t.Fatalf("%s script does not use exact target:\n%s", name, script)
		}
	}
}

func TestMirrorStartFailsWithoutConfiguredMirrors(t *testing.T) {
	stateDir := t.TempDir()
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "start"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected start without mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
	if !strings.Contains(ce.Message, "no mirrors are configured") {
		t.Fatalf("message=%q, want no mirrors configured", ce.Message)
	}
	if strings.Contains(out.String(), "mirror started") {
		t.Fatalf("start printed success unexpectedly:\n%s", out.String())
	}
	if _, err := os.Stat(mirror.DefaultPIDPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("pid file should not exist, stat err=%v", err)
	}
}

func TestMirrorStartFailsWhenAllMirrorsPaused(t *testing.T) {
	stateDir := t.TempDir()
	local := filepath.Join(stateDir, "project")
	if err := ensureTestDir(local); err != nil {
		t.Fatal(err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	if _, err := store.Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    false,
		LocalRoot:  local,
		Worker:     "dev@linux-builder",
		RemotePath: "~/workspace/project",
	}); err != nil {
		t.Fatal(err)
	}
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "start"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected start with paused mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
	if !strings.Contains(ce.Message, "no enabled mirrors are configured") {
		t.Fatalf("message=%q, want no enabled mirrors configured", ce.Message)
	}
	if !strings.Contains(ce.Hint, "mirror resume <id>") {
		t.Fatalf("hint=%q, want resume guidance", ce.Hint)
	}
	if strings.Contains(out.String(), "mirror started") {
		t.Fatalf("start printed success unexpectedly:\n%s", out.String())
	}
}

func TestMirrorExecRequiresDashBeforeCommand(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "exec", "echo", "hi"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected exec without -- to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_EXEC_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_EXEC_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorExecRequiresCommandAfterDash(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "exec", "--"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected exec without command to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_EXEC_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_EXEC_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorCommandFromArgsQuotesShellWords(t *testing.T) {
	got := mirrorCommandFromArgs([]string{"printf", "%s", "hello world", "it's ok"})
	want := "printf %s 'hello world' 'it'\\''s ok'"
	if got != want {
		t.Fatalf("command=%q, want %q", got, want)
	}
}

func TestFixStaleMirrorPIDRemovesInvalidPIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "mirror.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	action, ok := fixStaleMirrorPID(pidPath)
	if !ok {
		t.Fatal("expected stale pid action")
	}
	if action.Status != "fixed" {
		t.Fatalf("status=%q, want fixed", action.Status)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, stat err=%v", err)
	}
}

func TestMirrorShellRequiresIDWhenNameIsAmbiguous(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", stateDir, "mirror", "shell", "project", "--command", "pwd"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected ambiguous mirror name to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
	if !strings.Contains(ce.Hint, firstID) {
		t.Fatalf("hint %q missing id %q", ce.Hint, firstID)
	}
}

func TestMirrorShellRejectsTmuxWithCommand(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "shell", "--tmux", "--command", "pwd"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected invalid shell args to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_SHELL_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_SHELL_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorShellRejectsSessionWithoutTmux(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"mirror", "shell", "--session", "work"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected invalid shell args to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_SHELL_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_SHELL_ARGS_INVALID", ce.Code)
	}
}

func TestMirrorShellReportsNoMirrorsConfigured(t *testing.T) {
	root := newRoot(&options{})
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"--state-dir", t.TempDir(), "mirror", "shell", "--command", "pwd"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected shell without mirrors to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_NONE_CONFIGURED" {
		t.Fatalf("code=%q, want MIRROR_NONE_CONFIGURED", ce.Code)
	}
}

func TestMirrorShellInfersMostSpecificCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "project")
	child := filepath.Join(parent, "service")
	leaf := filepath.Join(child, "cmd")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(leaf); err != nil {
		t.Fatal(err)
	}
	got, ok, err := selectMirrorProfileForShell([]mirror.Profile{
		{ID: "aaaaaa", Name: "project", LocalRoot: parent},
		{ID: "bbbbbb", Name: "service", LocalRoot: child},
	}, "")
	if err != nil || !ok {
		t.Fatalf("select ok=%t err=%v", ok, err)
	}
	if got.ID != "bbbbbb" {
		t.Fatalf("selected id=%q, want bbbbbb", got.ID)
	}
}

func TestMirrorShellRequiresIDForCurrentDirectoryCollision(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "project")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(local); err != nil {
		t.Fatal(err)
	}
	_, ok, err := selectMirrorProfileForShell([]mirror.Profile{
		{ID: "aaaaaa", Name: "project", LocalRoot: local, RemotePath: "~/workspace/a"},
		{ID: "bbbbbb", Name: "project", LocalRoot: local, RemotePath: "~/workspace/b"},
	}, "")
	if err == nil || ok {
		t.Fatalf("expected current-directory collision, ok=%t err=%v", ok, err)
	}
	ambiguous, ok := err.(mirror.AmbiguousRefError)
	if !ok {
		t.Fatalf("expected AmbiguousRefError, got %T: %v", err, err)
	}
	if !strings.Contains(strings.Join(ambiguous.IDs, ","), "aaaaaa") || !strings.Contains(strings.Join(ambiguous.IDs, ","), "bbbbbb") {
		t.Fatalf("ambiguous ids=%#v", ambiguous.IDs)
	}
}

func TestMirrorServicesSelectionUsesIDWhenPresent(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	selection, err := selectMirrorServiceSelection(stateDir, []string{firstID, "api", "events"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile.ID != firstID {
		t.Fatalf("profile id=%q, want %q", selection.Profile.ID, firstID)
	}
	if strings.Join(selection.Services, ",") != "api,events" {
		t.Fatalf("services=%#v, want api/events", selection.Services)
	}
}

func TestMirrorServicesSelectionRequiresIDForCurrentDirectoryCollision(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	profile, ok, err := store.Get(firstID)
	if err != nil || !ok {
		t.Fatalf("get profile ok=%t err=%v", ok, err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	if err := os.Chdir(profile.LocalRoot); err != nil {
		t.Fatal(err)
	}
	_, err = selectMirrorServiceSelection(stateDir, []string{"api"}, true)
	if err == nil {
		t.Fatal("expected current-directory mirror collision to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_AMBIGUOUS" {
		t.Fatalf("code=%q, want MIRROR_AMBIGUOUS", ce.Code)
	}
}

func TestMirrorServicesLogSelectionTreatsSingleArgAsServiceWithOneMirror(t *testing.T) {
	stateDir := t.TempDir()
	local := filepath.Join(stateDir, "project")
	if err := ensureTestDir(local); err != nil {
		t.Fatal(err)
	}
	stored, err := mirror.NewStore(mirror.DefaultPath(stateDir)).Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "dev@linux-builder",
		RemotePath: "~/workspace/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, target, err := selectMirrorServiceLogSelection(stateDir, []string{"api"})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile.ID != stored.ID {
		t.Fatalf("profile id=%q, want %q", selection.Profile.ID, stored.ID)
	}
	if target != "api" {
		t.Fatalf("target=%q, want api", target)
	}
}

func TestMirrorServicesLogSelectionRequiresTargetAfterMirrorID(t *testing.T) {
	stateDir, firstID := writeMirrorConflictRegistry(t)
	_, _, err := selectMirrorServiceLogSelection(stateDir, []string{firstID})
	if err == nil {
		t.Fatal("expected missing log target to fail")
	}
	ce := output.AsCommandError(err)
	if ce.Code != "MIRROR_SERVICES_ARGS_INVALID" {
		t.Fatalf("code=%q, want MIRROR_SERVICES_ARGS_INVALID", ce.Code)
	}
}

func TestRemoteDaemonArgvControlsJSONMode(t *testing.T) {
	opts := &options{worker: "dev@worker"}
	paths := remote.Paths{
		Binary:  "/home/dev/.workyard/bin/workyard",
		Socket:  "/home/dev/.workyard/daemon/workyard.sock",
		RunRoot: "/home/dev/.workyard/runs/project/run",
		Project: "project",
		RunID:   "run",
	}
	plain := strings.Join(remoteDaemonArgv(opts, paths, "logs", []string{"api"}, controlExtra{Tail: 10}, false), " ")
	if strings.Contains(plain, "--json") {
		t.Fatalf("plain argv unexpectedly included --json: %s", plain)
	}
	jsonArgv := strings.Join(remoteDaemonArgv(opts, paths, "logs", []string{"api"}, controlExtra{Tail: 10}, true), " ")
	if !strings.Contains(jsonArgv, "--json") {
		t.Fatalf("json argv missing --json: %s", jsonArgv)
	}
}

func TestMirrorShellSessionName(t *testing.T) {
	got, err := mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "workyard-abc123" {
		t.Fatalf("session=%q, want workyard-abc123", got)
	}
	got, err = mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "wy-test_1.2")
	if err != nil {
		t.Fatal(err)
	}
	if got != "wy-test_1.2" {
		t.Fatalf("session=%q, want wy-test_1.2", got)
	}
	if _, err := mirrorShellSessionName(mirror.Profile{ID: "abc123"}, "bad:name"); err == nil {
		t.Fatal("expected invalid tmux session name to fail")
	}
}

func writeMirrorConflictRegistry(t *testing.T) (string, string) {
	t.Helper()
	stateDir := t.TempDir()
	local := filepath.Join(stateDir, "project")
	if err := ensureTestDir(local); err != nil {
		t.Fatal(err)
	}
	store := mirror.NewStore(mirror.DefaultPath(stateDir))
	first, err := store.Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "dev@linux-builder",
		RemotePath: "~/workspace/project-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(mirror.Profile{
		Name:       "project",
		Enabled:    true,
		LocalRoot:  local,
		Worker:     "dev@linux-builder",
		RemotePath: "~/workspace/project-b",
	}); err != nil {
		t.Fatal(err)
	}
	return stateDir, first.ID
}

func ensureTestDir(path string) error {
	return os.MkdirAll(path, 0o700)
}
