package cli

import (
	"strings"
	"testing"
)

func TestControlCommandShortDescriptionsAreDescriptive(t *testing.T) {
	root := newRoot(&options{})
	want := map[string]string{
		"setup":   "Run the configured setup command for a project run",
		"build":   "Run the configured build command for a project run",
		"start":   "Start services on a worker (all services when none are named)",
		"stop":    "Stop services on a worker (all services when none are named)",
		"restart": "Restart services on a worker (all services when none are named)",
		"status":  "Show current service status for a run",
		"inspect": "Show detailed service state, hints, and recent errors",
		"urls":    "Show service preview URLs for a run",
		"probe":   "Probe a service health endpoint from the worker",
	}
	for name, short := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cmd == nil {
			t.Fatalf("%s command was not found", name)
		}
		if cmd.Short != short {
			t.Fatalf("%s short=%q, want %q", name, cmd.Short, short)
		}
	}
}

func TestRootHelpGroupsCommandsByWorkflow(t *testing.T) {
	root := newRoot(&options{})
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	assertInOrder(t, help,
		"Primary Workflows",
		"  deploy      Run the full deploy flow for a project",
		"  watch       Watch local files, sync changes, and optionally restart services",
		"  mirror      Continuously mirror registered directories to workers",
		"Project Configuration",
		"  init        Create a starter workyard.yaml",
		"  config      Inspect Workyard config",
		"  services    List configured services",
		"Lifecycle Steps",
		"  sync        Copy project files into a worker run directory",
		"  setup       Run the configured setup command for a project run",
		"  build       Run the configured build command for a project run",
		"  start       Start services on a worker (all services when none are named)",
		"  stop        Stop services on a worker (all services when none are named)",
		"  restart     Restart services on a worker (all services when none are named)",
		"  wait        Wait for service state or health",
		"  probe       Probe a service health endpoint from the worker",
		"Runtime Inspection",
		"  status      Show current service status for a run",
		"  inspect     Show detailed service state, hints, and recent errors",
		"  logs        Read bounded service logs",
		"  events      Read lifecycle events",
		"  urls        Show service preview URLs for a run",
		"  open        Open a service preview URL",
		"  ui          Run the local Workyard monitor UI",
		"Worker Management",
		"  workers     Discover and manage registered Workyard workers",
		"  install     Install or upgrade the Workyard binary on a worker",
		"  doctor      Check local Workyard dependencies and connectivity",
		"  daemon      Manage the private worker daemon",
		"Run Maintenance",
		"  runs        Manage locally registered Workyard runs",
		"  cleanup     Safely clean Workyard runs and logs",
		"Utility",
		"  version     Print Workyard version",
		"  help        Help about any command",
		"  completion  Generate the autocompletion script for the specified shell",
	)
	if strings.Contains(help, "Available Commands:") {
		t.Fatalf("expected grouped help, got:\n%s", help)
	}
}

func assertInOrder(t *testing.T, value string, parts ...string) {
	t.Helper()
	offset := 0
	for _, part := range parts {
		next := strings.Index(value[offset:], part)
		if next < 0 {
			t.Fatalf("missing %q after offset %d in:\n%s", part, offset, value)
		}
		offset += next + len(part)
	}
}
