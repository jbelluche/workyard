package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
	"github.com/jackbelluche/workyard/internal/logs"
)

func (d *Daemon) logs(req Request) Response {
	d.mu.Lock()
	st, err := loadState(req.RunRoot, req.Project, req.RunID, workerName(req.Worker))
	if err != nil {
		d.mu.Unlock()
		return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
	}
	d.mu.Unlock()
	if len(req.Services) != 1 {
		return errorResponse("SERVICE_REQUIRED", "logs requires exactly one service", "")
	}
	service := req.Services[0]
	logPaths, ok := logPathsForTarget(st, service)
	if !ok {
		return errorResponse("SERVICE_NOT_FOUND", fmt.Sprintf("service or lifecycle log target %q has no state", service), "")
	}
	stream := req.Stream
	if stream == "" {
		stream = "both"
	}
	var entries []LogEntry
	now := time.Now().UTC()
	if stream == "stdout" || stream == "both" {
		lines, _, err := logs.TailFile(filepath.Join(req.RunRoot, logPaths.Stdout), req.Tail, req.MaxBytes)
		if err != nil {
			return errorResponse("LOG_READ_FAILED", err.Error(), "")
		}
		for _, line := range lines {
			entries = append(entries, LogEntry{Time: now, RunID: st.RunID, Service: service, Stream: "stdout", Line: redact(line)})
		}
	}
	if stream == "stderr" || stream == "both" {
		lines, _, err := logs.TailFile(filepath.Join(req.RunRoot, logPaths.Stderr), req.Tail, req.MaxBytes)
		if err != nil {
			return errorResponse("LOG_READ_FAILED", err.Error(), "")
		}
		for _, line := range lines {
			entries = append(entries, LogEntry{Time: now, RunID: st.RunID, Service: service, Stream: "stderr", Line: redact(line)})
		}
	}
	if stream != "stdout" && stream != "stderr" && stream != "both" {
		return errorResponse("INVALID_STREAM", "stream must be stdout, stderr, or both", "")
	}
	return Response{OK: true, Project: st.Project, RunID: st.RunID, Worker: st.Worker, Entries: entries}
}

func (d *Daemon) events(req Request) Response {
	d.mu.Lock()
	st, err := loadState(req.RunRoot, req.Project, req.RunID, workerName(req.Worker))
	if err != nil {
		d.mu.Unlock()
		return errorResponse("STATE_LOAD_FAILED", err.Error(), "")
	}
	d.mu.Unlock()
	tail := req.Tail
	if tail <= 0 {
		tail = 200
	}
	var events []Event
	lines, _, err := logs.TailFile(eventPath(req.RunRoot, ""), tail, req.MaxBytes)
	if err != nil {
		return errorResponse("EVENT_READ_FAILED", err.Error(), "")
	}
	for _, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			events = append(events, ev)
		}
	}
	for _, svc := range sortedStates(st) {
		lines, _, err := logs.TailFile(filepath.Join(req.RunRoot, svc.Logs.Events), tail, req.MaxBytes)
		if err != nil {
			return errorResponse("EVENT_READ_FAILED", err.Error(), "")
		}
		for _, line := range lines {
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err == nil {
				events = append(events, ev)
			}
		}
	}
	return Response{OK: true, Project: st.Project, RunID: st.RunID, Worker: st.Worker, Events: events}
}

func (d *Daemon) inspect(req Request) Response {
	status := d.status(req)
	if !status.OK {
		return status
	}
	var hints []Hint
	for i := range status.Services {
		svc := &status.Services[i]
		if svc.Logs.Stderr != "" {
			lines, _, _ := logs.TailFile(filepath.Join(req.RunRoot, svc.Logs.Stderr), 20, 32*1024)
			svc.RecentErrors = recentErrors(lines)
		}
		svc.LogsCommand = fmt.Sprintf("workyard logs %s --worker %s --run %s --tail 200 --json", svc.Name, status.Worker, status.RunID)
		if svc.Status == "exited" {
			msg := fmt.Sprintf("%s exited", svc.Name)
			if svc.ExitCode != nil {
				msg = fmt.Sprintf("%s exited with code %d", svc.Name, *svc.ExitCode)
			}
			hints = append(hints, Hint{Code: "SERVICE_EXITED", Service: svc.Name, Message: msg, Severity: "error", NextCommand: svc.LogsCommand})
		}
		if svc.Status == "unhealthy" {
			hints = append(hints, Hint{Code: "HEALTH_CHECK_FAILED", Service: svc.Name, Message: svc.Name + " did not pass its health check", Severity: "error", NextCommand: svc.LogsCommand})
		}
		for _, line := range svc.RecentErrors {
			if strings.Contains(strings.ToLower(line), "address already in use") {
				hints = append(hints, Hint{Code: "PORT_IN_USE", Service: svc.Name, Message: "stderr mentions address already in use", Severity: "error", NextCommand: svc.LogsCommand})
				break
			}
		}
	}
	for _, ev := range recentLifecycleEvents(req.RunRoot, status.Services) {
		if strings.Contains(ev.Type, ".failed") || strings.Contains(ev.Type, ".timeout") {
			hints = append(hints, Hint{
				Code:        "LIFECYCLE_FAILED",
				Service:     ev.Service,
				Message:     ev.Message,
				Severity:    "error",
				NextCommand: "workyard events --worker " + status.Worker + " --run " + status.RunID + " --json",
			})
		}
	}
	status.Hints = hints
	return status
}

func (d *Daemon) wait(req Request) Response {
	timeout := 60 * time.Second
	if req.Timeout != "" {
		parsed, err := time.ParseDuration(req.Timeout)
		if err != nil {
			return errorResponse("INVALID_TIMEOUT", err.Error(), "")
		}
		timeout = parsed
	}
	deadline := time.Now().Add(timeout)
	for {
		status := d.status(req)
		if !status.OK {
			return status
		}
		matched := true
		for _, name := range req.Services {
			found := false
			for _, svc := range status.Services {
				if svc.Name != name {
					continue
				}
				found = true
				if req.Healthy && !svc.Healthy {
					matched = false
				}
				if req.Status != "" && svc.Status != req.Status {
					matched = false
				}
			}
			if !found {
				matched = false
			}
		}
		if matched {
			status.Message = "condition met"
			return status
		}
		if time.Now().After(deadline) {
			return errorResponse("WAIT_TIMEOUT", "wait timed out", "Run workyard inspect --json for current service state")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (d *Daemon) urls(req Request) Response {
	status := d.status(req)
	if !status.OK {
		return status
	}
	var urls []PreviewURL
	for _, svc := range status.Services {
		if len(req.Services) > 0 && !contains(req.Services, svc.Name) {
			continue
		}
		if svc.URL == "" {
			continue
		}
		urls = append(urls, PreviewURL{Service: svc.Name, URL: svc.URL, Private: true, Public: false, Healthy: svc.Healthy})
	}
	status.URLs = urls
	return status
}

func (d *Daemon) probe(req Request) Response {
	status := d.status(req)
	if !status.OK {
		return status
	}
	if len(req.Services) != 1 {
		return errorResponse("SERVICE_REQUIRED", "probe requires exactly one service", "")
	}
	for _, svc := range status.Services {
		if svc.Name != req.Services[0] {
			continue
		}
		url := svc.HealthURL
		if url == "" && svc.AssignedPort > 0 {
			url = fmt.Sprintf("http://127.0.0.1:%d", svc.AssignedPort)
		}
		if url == "" {
			return errorResponse("PROBE_URL_MISSING", "service has no health URL or assigned port", "")
		}
		code, err := probeURL(url, 5*time.Second)
		if err != nil {
			return errorResponse("PROBE_FAILED", err.Error(), "")
		}
		status.Message = fmt.Sprintf("probe %s returned HTTP %d", url, code)
		return status
	}
	return errorResponse("SERVICE_NOT_FOUND", fmt.Sprintf("service %q has no state", req.Services[0]), "")
}

func recentErrors(lines []string) []string {
	var out []string
	for _, line := range lines {
		line = redact(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	if len(out) > 10 {
		out = out[len(out)-10:]
	}
	return out
}

func redact(line string) string {
	line = bearerTokenRE.ReplaceAllString(line, `Bearer [REDACTED]`)
	line = secretAssignmentRE.ReplaceAllString(line, `${1}[REDACTED]`)
	line = urlCredentialRE.ReplaceAllString(line, `://[REDACTED]@`)
	return line
}

var (
	secretAssignmentRE = regexp.MustCompile(`(?i)([A-Z0-9_.-]*(?:TOKEN|SECRET|PASSWORD|PASS|API[_-]?KEY|ACCESS[_-]?KEY|PRIVATE[_-]?KEY|AUTH|COOKIE|SESSION|JWT)[A-Z0-9_.-]*\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
	bearerTokenRE      = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`)
	urlCredentialRE    = regexp.MustCompile(`://[^/@\s]+@`)
)

func probeURL(raw string, timeout time.Duration) (int, error) {
	if _, err := config.ValidateHealthURL(raw); err != nil {
		return 0, fmt.Errorf("health URL rejected: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "workyard-health/1")
	res, err := privateHealthClient(timeout).Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	return res.StatusCode, nil
}

func privateHealthClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many health check redirects")
			}
			if _, err := config.ValidateHealthURL(req.URL.String()); err != nil {
				return fmt.Errorf("health check redirect rejected: %w", err)
			}
			return nil
		},
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func logPathsForTarget(st RunState, target string) (LogPaths, bool) {
	if svc, ok := st.Services[target]; ok {
		return svc.Logs, true
	}
	switch target {
	case "setup", "build":
		return lifecycleLogPaths(target), true
	}
	parts := strings.Split(target, ".")
	if len(parts) == 2 {
		if _, ok := st.Services[parts[0]]; ok {
			return lifecycleLogPaths(target), true
		}
	}
	return LogPaths{}, false
}

func lifecycleLogPaths(name string) LogPaths {
	return LogPaths{
		Stdout: filepath.ToSlash(filepath.Join("logs", name+".stdout.log")),
		Stderr: filepath.ToSlash(filepath.Join("logs", name+".stderr.log")),
		Events: filepath.ToSlash(filepath.Join("logs", "lifecycle.events.jsonl")),
	}
}

func readEventsFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err == nil {
			events = append(events, ev)
		}
	}
	return events, scanner.Err()
}

func recentLifecycleEvents(runRoot string, services []ServiceState) []Event {
	var out []Event
	read := func(path string) {
		lines, _, err := logs.TailFile(path, 100, 64*1024)
		if err != nil {
			return
		}
		for _, line := range lines {
			var ev Event
			if err := json.Unmarshal([]byte(line), &ev); err == nil {
				out = append(out, ev)
			}
		}
	}
	read(eventPath(runRoot, ""))
	for _, svc := range services {
		read(filepath.Join(runRoot, svc.Logs.Events))
	}
	return out
}
