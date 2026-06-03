package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const FileName = "runs.json"

type RunRef struct {
	Worker           string    `json:"worker"`
	Project          string    `json:"project"`
	RunID            string    `json:"runId"`
	RemoteRoot       string    `json:"remoteRoot,omitempty"`
	RemoteRunPath    string    `json:"remoteRunPath,omitempty"`
	RemoteSourcePath string    `json:"remoteSourcePath,omitempty"`
	RemoteBinary     string    `json:"remoteBinary,omitempty"`
	LocalRoot        string    `json:"localRoot,omitempty"`
	ConfigPath       string    `json:"configPath,omitempty"`
	RegisteredAt     time.Time `json:"registeredAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type File struct {
	Runs []RunRef `json:"runs"`
}

type WorkerRef struct {
	Worker    string    `json:"worker"`
	RunCount  int       `json:"runCount"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Store struct {
	path string
}

func DefaultPath(stateDir string) string {
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".workyard")
	}
	return filepath.Join(stateDir, "local", FileName)
}

func New(path string) Store {
	return Store{path: path}
}

func (s Store) Path() string {
	return s.path
}

func (s Store) List() ([]RunRef, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]RunRef(nil), file.Runs...)
	sortRuns(out)
	return out, nil
}

func (s Store) Upsert(ref RunRef) error {
	if err := validate(ref); err != nil {
		return err
	}
	file, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	ref.UpdatedAt = now
	found := false
	for i, existing := range file.Runs {
		if sameRun(existing, ref) {
			if existing.RegisteredAt.IsZero() {
				ref.RegisteredAt = now
			} else {
				ref.RegisteredAt = existing.RegisteredAt
			}
			file.Runs[i] = ref
			found = true
			break
		}
	}
	if !found {
		ref.RegisteredAt = now
		file.Runs = append(file.Runs, ref)
	}
	sortRuns(file.Runs)
	return s.save(file)
}

func (s Store) Remove(worker, project, runID string) (bool, error) {
	target := RunRef{Worker: worker, Project: project, RunID: runID}
	if err := validate(target); err != nil {
		return false, err
	}
	file, err := s.load()
	if err != nil {
		return false, err
	}
	next := file.Runs[:0]
	removed := false
	for _, ref := range file.Runs {
		if sameRun(ref, target) {
			removed = true
			continue
		}
		next = append(next, ref)
	}
	file.Runs = next
	if !removed {
		return false, nil
	}
	return true, s.save(file)
}

func (s Store) RemoveWorker(worker string) (int, error) {
	worker = strings.TrimSpace(worker)
	if worker == "" || strings.Contains(worker, "\x00") || strings.ContainsAny(worker, "\r\n") {
		return 0, fmt.Errorf("worker is required")
	}
	file, err := s.load()
	if err != nil {
		return 0, err
	}
	next := file.Runs[:0]
	removed := 0
	for _, ref := range file.Runs {
		if ref.Worker == worker {
			removed++
			continue
		}
		next = append(next, ref)
	}
	file.Runs = next
	if removed == 0 {
		return 0, nil
	}
	return removed, s.save(file)
}

func (s Store) Prune(cutoff time.Time) ([]RunRef, error) {
	if cutoff.IsZero() {
		return nil, fmt.Errorf("cutoff is required")
	}
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	next := file.Runs[:0]
	var removed []RunRef
	for _, ref := range file.Runs {
		if !ref.UpdatedAt.IsZero() && ref.UpdatedAt.Before(cutoff) {
			removed = append(removed, ref)
			continue
		}
		next = append(next, ref)
	}
	file.Runs = next
	if len(removed) == 0 {
		return nil, nil
	}
	sortRuns(removed)
	return removed, s.save(file)
}

func (s Store) Workers() ([]WorkerRef, error) {
	runs, err := s.List()
	if err != nil {
		return nil, err
	}
	byWorker := map[string]*WorkerRef{}
	for _, run := range runs {
		item, ok := byWorker[run.Worker]
		if !ok {
			item = &WorkerRef{Worker: run.Worker}
			byWorker[run.Worker] = item
		}
		item.RunCount++
		if run.UpdatedAt.After(item.UpdatedAt) {
			item.UpdatedAt = run.UpdatedAt
		}
	}
	out := make([]WorkerRef, 0, len(byWorker))
	for _, item := range byWorker {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Worker < out[j].Worker })
	return out, nil
}

func (s Store) load() (File, error) {
	if s.path == "" {
		return File{}, errors.New("registry path is required")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, err
	}
	for i := range file.Runs {
		if err := validate(file.Runs[i]); err != nil {
			return File{}, fmt.Errorf("invalid registry entry %d: %w", i, err)
		}
	}
	return file, nil
}

func (s Store) save(file File) error {
	if s.path == "" {
		return errors.New("registry path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".runs-*.json")
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
	if err := enc.Encode(file); err != nil {
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

func validate(ref RunRef) error {
	for label, value := range map[string]string{
		"worker":  ref.Worker,
		"project": ref.Project,
		"runId":   ref.RunID,
	} {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s is required", label)
		}
		if strings.Contains(value, "\x00") || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s contains invalid characters", label)
		}
	}
	return nil
}

func sameRun(a, b RunRef) bool {
	return a.Worker == b.Worker && a.Project == b.Project && a.RunID == b.RunID
}

func sortRuns(runs []RunRef) {
	sort.Slice(runs, func(i, j int) bool {
		a := runs[i]
		b := runs[j]
		if a.Worker != b.Worker {
			return a.Worker < b.Worker
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		return a.RunID < b.RunID
	})
}
