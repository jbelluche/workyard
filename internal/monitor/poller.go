package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/worker"
)

type Registry interface {
	List() ([]registry.RunRef, error)
}

type Fetcher interface {
	Fetch(ctx context.Context, ref registry.RunRef) (runData, error)
}

type Poller struct {
	registry Registry
	fetcher  Fetcher
	version  string
	interval time.Duration
	timeout  time.Duration

	mu    sync.RWMutex
	state State
}

func NewPoller(reg Registry, fetcher Fetcher, version string, interval, timeout time.Duration) *Poller {
	if interval <= 0 {
		interval = 3 * time.Second
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &Poller{
		registry: reg,
		fetcher:  fetcher,
		version:  version,
		interval: interval,
		timeout:  timeout,
		state: State{
			OK:              true,
			Version:         version,
			GeneratedAt:     time.Now().UTC(),
			RefreshInterval: interval.String(),
			Workers:         []WorkerSnapshot{},
			Runs:            []RunSnapshot{},
			Services:        []ServiceSnapshot{},
			Events:          []EventSnapshot{},
		},
	}
}

func (p *Poller) Start(ctx context.Context) {
	p.Refresh(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.Refresh(ctx)
		}
	}
}

func (p *Poller) Refresh(ctx context.Context) {
	next := State{
		OK:              true,
		Version:         p.version,
		GeneratedAt:     time.Now().UTC(),
		RefreshInterval: p.interval.String(),
		Workers:         []WorkerSnapshot{},
		Runs:            []RunSnapshot{},
		Services:        []ServiceSnapshot{},
		Events:          []EventSnapshot{},
	}
	refs, err := p.registry.List()
	if err != nil {
		next.RegistryError = err.Error()
		next.OK = false
		p.setState(next)
		return
	}
	for _, ref := range refs {
		run := p.fetchRun(ctx, ref)
		next.Runs = append(next.Runs, run)
		for _, svc := range run.Services {
			next.Services = append(next.Services, serviceSnapshot(run, svc))
		}
		for _, ev := range run.RecentEvents {
			next.Events = append(next.Events, EventSnapshot{
				Worker:  run.Worker,
				Project: run.Project,
				RunID:   run.RunID,
				Time:    ev.Time,
				Type:    ev.Type,
				Service: ev.Service,
				Message: ev.Message,
			})
		}
	}
	next.Workers = workerSnapshots(next.Runs)
	sortState(&next)
	p.setState(next)
}

func (p *Poller) State() State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.state
}

func (p *Poller) setState(next State) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = next
}

func (p *Poller) fetchRun(ctx context.Context, ref registry.RunRef) RunSnapshot {
	run := RunSnapshot{
		Key:              runKey(ref.Worker, ref.Project, ref.RunID),
		Worker:           ref.Worker,
		Project:          ref.Project,
		RunID:            ref.RunID,
		Status:           "offline",
		RemoteRunPath:    ref.RemoteRunPath,
		RemoteSourcePath: ref.RemoteSourcePath,
		LocalRoot:        ref.LocalRoot,
		RegisteredAt:     ref.RegisteredAt,
		UpdatedAt:        ref.UpdatedAt,
	}
	fetchCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	data, err := p.fetcher.Fetch(fetchCtx, ref)
	if err != nil {
		run.LastError = err.Error()
		return run
	}
	res := data.Response
	if res.Project != "" {
		run.Project = res.Project
	}
	if res.RunID != "" {
		run.RunID = res.RunID
	}
	if res.Worker != "" {
		run.Worker = res.Worker
	}
	run.Key = runKey(run.Worker, run.Project, run.RunID)
	run.Status = "online"
	run.Connected = true
	run.LastHeartbeat = time.Now().UTC()
	run.Services = sanitizeServices(res.Services)
	run.Hints = append([]worker.Hint(nil), res.Hints...)
	run.RecentEvents = append([]worker.Event(nil), data.Events...)
	run.ServiceCount = len(run.Services)
	for _, svc := range run.Services {
		if svc.Healthy {
			run.HealthyCount++
		}
		if isRunningStatus(svc.Status) {
			run.RunningCount++
		}
		latest := svc.StartedAt
		if svc.StoppedAt.After(latest) {
			latest = svc.StoppedAt
		}
		if latest.After(run.LastServiceUpdate) {
			run.LastServiceUpdate = latest
		}
	}
	return run
}

func sanitizeServices(services []worker.ServiceState) []worker.ServiceState {
	out := make([]worker.ServiceState, 0, len(services))
	for _, svc := range services {
		svc.RecentErrors = nil
		svc.LogsCommand = ""
		out = append(out, svc)
	}
	return out
}

func serviceSnapshot(run RunSnapshot, svc worker.ServiceState) ServiceSnapshot {
	updated := svc.StartedAt
	if svc.StoppedAt.After(updated) {
		updated = svc.StoppedAt
	}
	return ServiceSnapshot{
		Worker:       run.Worker,
		Project:      run.Project,
		RunID:        run.RunID,
		Name:         svc.Name,
		Status:       svc.Status,
		Healthy:      svc.Healthy,
		PID:          svc.PID,
		Port:         svc.AssignedPort,
		URL:          svc.URL,
		StartCommand: svc.StartCommand,
		UpdatedAt:    updated,
	}
}

func workerSnapshots(runs []RunSnapshot) []WorkerSnapshot {
	byName := map[string]*WorkerSnapshot{}
	for _, run := range runs {
		name := run.Worker
		if name == "" {
			name = "localhost"
		}
		item, ok := byName[name]
		if !ok {
			item = &WorkerSnapshot{Name: name, Status: "offline"}
			byName[name] = item
		}
		item.RunCount++
		item.ServiceCount += run.ServiceCount
		item.HealthyCount += run.HealthyCount
		item.RunningCount += run.RunningCount
		if run.Connected {
			item.Connected = true
			item.Status = "online"
		}
		if run.LastHeartbeat.After(item.LastHeartbeat) {
			item.LastHeartbeat = run.LastHeartbeat
		}
		if item.LastError == "" && run.LastError != "" {
			item.LastError = run.LastError
		}
	}
	out := make([]WorkerSnapshot, 0, len(byName))
	for _, item := range byName {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func sortState(st *State) {
	sort.Slice(st.Runs, func(i, j int) bool {
		if st.Runs[i].Worker != st.Runs[j].Worker {
			return st.Runs[i].Worker < st.Runs[j].Worker
		}
		if st.Runs[i].Project != st.Runs[j].Project {
			return st.Runs[i].Project < st.Runs[j].Project
		}
		return st.Runs[i].RunID < st.Runs[j].RunID
	})
	sort.Slice(st.Services, func(i, j int) bool {
		a := st.Services[i]
		b := st.Services[j]
		if a.Worker != b.Worker {
			return a.Worker < b.Worker
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.RunID != b.RunID {
			return a.RunID < b.RunID
		}
		return a.Name < b.Name
	})
	sort.Slice(st.Events, func(i, j int) bool {
		return st.Events[i].Time.After(st.Events[j].Time)
	})
	if len(st.Events) > 200 {
		st.Events = st.Events[:200]
	}
}

func runKey(workerName, project, runID string) string {
	return fmt.Sprintf("%s/%s/%s", workerName, project, runID)
}

func isRunningStatus(status string) bool {
	switch strings.ToLower(status) {
	case "preparing", "starting", "running", "healthy", "unhealthy":
		return true
	default:
		return false
	}
}
