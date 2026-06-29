package worker

import (
	"testing"

	"github.com/jackbelluche/workyard/internal/config"
)

func TestParsePortRange(t *testing.T) {
	rng, err := parsePortRange("3100-3102")
	if err != nil {
		t.Fatal(err)
	}
	if rng.Start != 3100 || rng.End != 3102 {
		t.Fatalf("unexpected range %#v", rng)
	}
}

func TestParsePortRangeRejectsInvalid(t *testing.T) {
	if _, err := parsePortRange("3999-3100"); err == nil {
		t.Fatal("expected invalid range")
	}
}

func TestApplyRuntimeArgsSubstitutesPortWithoutShell(t *testing.T) {
	got := applyRuntimeArgs([]string{"python3", "-m", "http.server", "${WORKYARD_PORT}", "${PORT}"}, 3107)
	want := []string{"python3", "-m", "http.server", "3107", "3107"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}

func TestMinimalEnvDropsSSHAuthSock(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	env := minimalEnv()
	for _, item := range env {
		if item == "SSH_AUTH_SOCK=/tmp/agent.sock" {
			t.Fatalf("minimal env leaked SSH_AUTH_SOCK: %#v", env)
		}
	}
}

func TestProcessIdentityRequiresStartTimeOnLinux(t *testing.T) {
	id := currentProcessID(1)
	if id.StartTime != "" {
		id.StartTime = "definitely-not-the-real-start-time"
		if processIdentityMatches(id) {
			t.Fatal("expected mismatched process start time to be rejected")
		}
	}
}

func TestServiceLifecycleEnvIncludesRuntimePort(t *testing.T) {
	env := serviceLifecycleEnv(config.Service{Port: config.PortConfig{Default: 3000, Env: "PORT"}}, 3123)
	if !envContains(env, "PORT") || !envContains(env, "WORKYARD_PORT") || !envContains(env, "WORKYARD") {
		t.Fatalf("missing runtime env values: %#v", env)
	}
}

func TestServiceEnvForRunIncludesPeerServiceURLs(t *testing.T) {
	env := serviceEnvForRun(
		config.Service{Port: config.PortConfig{Default: 3000, Env: "PORT"}},
		3201,
		[]ServiceState{
			{Name: "api", AssignedPort: 3201},
			{Name: "analytics-worker", AssignedPort: 3202},
		},
	)
	want := map[string]string{
		"WORKYARD_SERVICE_API_PORT":              "3201",
		"WORKYARD_SERVICE_API_URL":               "http://127.0.0.1:3201",
		"WORKYARD_SERVICE_ANALYTICS_WORKER_PORT": "3202",
		"WORKYARD_SERVICE_ANALYTICS_WORKER_URL":  "http://127.0.0.1:3202",
	}
	for key, value := range want {
		if !envContainsValue(env, key, value) {
			t.Fatalf("missing %s=%s in %#v", key, value, env)
		}
	}
}

func TestAllocatePortTreatsPreparingAsReserved(t *testing.T) {
	st := RunState{Services: map[string]ServiceState{
		"api": {Status: "preparing", AssignedPort: 3210},
	}}
	got, err := allocatePort(st, "events", 3210, portRange{Start: 3210, End: 3211})
	if err != nil {
		t.Fatal(err)
	}
	if got == 3210 {
		t.Fatalf("allocated reserved preparing port %d", got)
	}
}

func envContainsValue(env []string, key, value string) bool {
	want := key + "=" + value
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}
