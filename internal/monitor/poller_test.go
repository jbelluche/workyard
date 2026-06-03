package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/worker"
)

type fakeRegistry struct {
	refs []registry.RunRef
	err  error
}

func (f fakeRegistry) List() ([]registry.RunRef, error) {
	return f.refs, f.err
}

type fakeFetcher struct {
	data runData
	err  error
}

func (f fakeFetcher) Fetch(ctx context.Context, ref registry.RunRef) (runData, error) {
	return f.data, f.err
}

func TestPollerRefreshAggregatesWorkerRunAndService(t *testing.T) {
	now := time.Now().UTC()
	reg := fakeRegistry{refs: []registry.RunRef{{
		Worker:        "jack@jack-rasp-five",
		Project:       "fixture",
		RunID:         "run-1",
		RemoteRunPath: "/home/jack/.workyard/runs/fixture/run-1",
	}}}
	fetch := fakeFetcher{data: runData{
		Response: worker.Response{
			OK:      true,
			Worker:  "jack@jack-rasp-five",
			Project: "fixture",
			RunID:   "run-1",
			Services: []worker.ServiceState{{
				Name:         "web",
				Status:       "healthy",
				Healthy:      true,
				PID:          1234,
				AssignedPort: 3100,
				URL:          "http://jack-rasp-five:3100",
				StartedAt:    now,
			}},
		},
		Events: []worker.Event{{Time: now, Type: "health.ok", Service: "web", Message: "health check passed"}},
	}}
	poller := NewPoller(reg, fetch, "test", time.Second, time.Second)
	poller.Refresh(context.Background())
	state := poller.State()
	if !state.OK || len(state.Workers) != 1 || len(state.Runs) != 1 || len(state.Services) != 1 || len(state.Events) != 1 {
		t.Fatalf("unexpected state: %#v", state)
	}
	if state.Workers[0].Status != "online" || state.Workers[0].HealthyCount != 1 {
		t.Fatalf("unexpected worker snapshot: %#v", state.Workers[0])
	}
	if state.Services[0].Name != "web" || !state.Services[0].Healthy {
		t.Fatalf("unexpected service snapshot: %#v", state.Services[0])
	}
}

func TestPollerStripsInspectOnlyErrorFields(t *testing.T) {
	reg := fakeRegistry{refs: []registry.RunRef{{Worker: "jack@jack-rasp-five", Project: "fixture", RunID: "run-1"}}}
	fetch := fakeFetcher{data: runData{
		Response: worker.Response{
			OK:      true,
			Worker:  "jack@jack-rasp-five",
			Project: "fixture",
			RunID:   "run-1",
			Services: []worker.ServiceState{{
				Name:         "web",
				Status:       "unhealthy",
				RecentErrors: []string{"SECRET=should-not-leak"},
				LogsCommand:  "workyard logs web --json",
			}},
		},
	}}
	poller := NewPoller(reg, fetch, "test", time.Second, time.Second)
	poller.Refresh(context.Background())
	state := poller.State()
	if got := state.Runs[0].Services[0].RecentErrors; len(got) != 0 {
		t.Fatalf("expected recent errors to be stripped, got %#v", got)
	}
	if state.Runs[0].Services[0].LogsCommand != "" {
		t.Fatalf("expected logs command to be stripped, got %q", state.Runs[0].Services[0].LogsCommand)
	}
}

func TestValidateListenRequiresLoopback(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:3099", "localhost:3099", "[::1]:3099"} {
		if err := validateListen(addr); err != nil {
			t.Fatalf("expected %s to be accepted: %v", addr, err)
		}
	}
	if err := validateListen("0.0.0.0:3099"); err == nil {
		t.Fatal("expected non-loopback listen address to be rejected")
	}
}

func TestHandlerStateEndpoint(t *testing.T) {
	provider := staticProvider{state: State{OK: true, Version: "test"}}
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	Handler(provider).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", rec.Code)
	}
	var state State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.OK || state.Version != "test" {
		t.Fatalf("unexpected response %#v", state)
	}
}

func TestHandlerRejectsExternalHostHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.test/api/state", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	rec := httptest.NewRecorder()
	Handler(staticProvider{state: State{OK: true}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %d", rec.Code)
	}
}

func TestHandlerSetsSecurityHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.RemoteAddr = "127.0.0.1:4444"
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	Handler(staticProvider{state: State{OK: true}}).ServeHTTP(rec, req)
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("expected Content-Security-Policy header")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("expected nosniff header")
	}
}

func TestHandlerRejectsNonLoopbackClients(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.RemoteAddr = "192.0.2.10:4444"
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	Handler(staticProvider{state: State{OK: true}}).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden, got %d", rec.Code)
	}
}

type staticProvider struct {
	state State
}

func (s staticProvider) State() State {
	return s.state
}
