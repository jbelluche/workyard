package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jackbelluche/workyard/internal/config"
	watcher "github.com/jackbelluche/workyard/internal/watch"
)

const (
	PIDFileName   = "mirror.pid"
	StateFileName = "mirror-state.json"
	LogFileName   = "mirror.log"
)

type RunOptions struct {
	StateDir     string
	Version      string
	Once         bool
	PollInterval time.Duration
	OnResult     func(SyncResult)
	OnError      func(Profile, error)
}

type State struct {
	UpdatedAt time.Time       `json:"updatedAt"`
	Mirrors   []RuntimeStatus `json:"mirrors"`
}

type RuntimeStatus struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Worker     string    `json:"worker"`
	LocalRoot  string    `json:"localRoot"`
	RemotePath string    `json:"remotePath"`
	State      string    `json:"state"`
	LastSync   time.Time `json:"lastSync,omitempty,omitzero"`
	LastError  string    `json:"lastError,omitempty"`
}

type StateStore struct {
	path string
	mu   sync.Mutex
}

func DefaultPIDPath(stateDir string) string {
	return filepath.Join(localStateDir(stateDir), PIDFileName)
}

func DefaultStatePath(stateDir string) string {
	return filepath.Join(localStateDir(stateDir), StateFileName)
}

func DefaultLogPath(stateDir string) string {
	return filepath.Join(localStateDir(stateDir), LogFileName)
}

func localStateDir(stateDir string) string {
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".workyard")
	}
	return filepath.Join(stateDir, "local")
}

func NewStateStore(path string) *StateStore {
	return &StateStore{path: path}
}

func (s *StateStore) Read() (State, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s *StateStore) Set(profile Profile, status string, synced time.Time, syncErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.Read()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state.UpdatedAt = now
	found := false
	for i := range state.Mirrors {
		if state.Mirrors[i].ID != profile.ID {
			continue
		}
		state.Mirrors[i] = runtimeStatus(profile, status, synced, syncErr)
		found = true
		break
	}
	if !found {
		state.Mirrors = append(state.Mirrors, runtimeStatus(profile, status, synced, syncErr))
	}
	return s.write(state)
}

func (s *StateStore) write(state State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mirror-state-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	keep = true
	return os.Rename(tmpName, s.path)
}

func runtimeStatus(profile Profile, status string, synced time.Time, syncErr error) RuntimeStatus {
	item := RuntimeStatus{
		ID:         profile.ID,
		Name:       profile.Name,
		Worker:     profile.Worker,
		LocalRoot:  profile.LocalRoot,
		RemotePath: profile.RemotePath,
		State:      status,
		LastSync:   synced,
	}
	if syncErr != nil {
		item.LastError = syncErr.Error()
	}
	return item
}

func Run(ctx context.Context, profiles []Profile, opts RunOptions) error {
	enabled := make([]Profile, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Enabled {
			enabled = append(enabled, profile)
		}
	}
	if len(enabled) == 0 {
		return fmt.Errorf("no enabled mirrors configured")
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 500 * time.Millisecond
	}
	state := NewStateStore(DefaultStatePath(opts.StateDir))
	for _, profile := range enabled {
		_ = state.Set(profile, "syncing", time.Time{}, nil)
		res, err := Sync(ctx, profile, SyncOptions{Version: opts.Version})
		if err != nil {
			_ = state.Set(profile, "error", time.Time{}, err)
			if opts.OnError != nil {
				opts.OnError(profile, err)
			}
			if opts.Once {
				return err
			}
			continue
		}
		_ = state.Set(profile, "synced", res.FinishedAt, nil)
		if opts.OnResult != nil {
			opts.OnResult(res)
		}
	}
	if opts.Once {
		return nil
	}
	errs := make(chan error, len(enabled))
	var wg sync.WaitGroup
	for _, profile := range enabled {
		profile := profile
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := runProfile(ctx, profile, opts, state); err != nil {
				errs <- err
			}
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return nil
	case <-done:
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	}
}

func runProfile(ctx context.Context, profile Profile, opts RunOptions, state *StateStore) error {
	spec := watcher.Spec{
		Service:    profile.Name,
		IncludeGit: profile.IncludeGit,
		Watch: config.WatchConfig{
			Paths:    []string{"."},
			Exclude:  profile.Exclude,
			Debounce: 750 * time.Millisecond,
		},
	}
	changes, watchErrs, err := watcher.Changes(ctx, profile.LocalRoot, []watcher.Spec{spec}, opts.PollInterval)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, ok := <-watchErrs:
			if !ok {
				return nil
			}
			if err != nil && opts.OnError != nil {
				opts.OnError(profile, err)
			}
		case _, ok := <-changes:
			if !ok {
				return nil
			}
			time.Sleep(750 * time.Millisecond)
			_ = state.Set(profile, "syncing", time.Time{}, nil)
			res, err := Sync(ctx, profile, SyncOptions{Version: opts.Version})
			if err != nil {
				_ = state.Set(profile, "error", time.Time{}, err)
				if opts.OnError != nil {
					opts.OnError(profile, err)
				}
				continue
			}
			_ = state.Set(profile, "synced", res.FinishedAt, nil)
			if opts.OnResult != nil {
				opts.OnResult(res)
			}
		}
	}
}
