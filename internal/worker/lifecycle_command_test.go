package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
)

func TestRunLifecycleCommandWritesLogsAndEvents(t *testing.T) {
	runRoot := t.TempDir()
	if err := os.MkdirAll(sourceRoot(runRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	err := runLifecycleCommand(runRoot, lifecycleRun{
		Name: "setup",
		Command: config.LifecycleCommand{
			Command: "echo ok",
			Shell:   true,
			Timeout: 2 * time.Second,
		},
		Cwd: sourceRoot(runRoot),
		Env: projectLifecycleEnv(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(runRoot, "logs", "setup.stdout.log")); err != nil || string(data) != "ok\n" {
		t.Fatalf("stdout data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(eventPath(runRoot, "")); err != nil || !strings.Contains(string(data), "lifecycle.setup.ok") {
		t.Fatalf("event data=%q err=%v", string(data), err)
	}
}
