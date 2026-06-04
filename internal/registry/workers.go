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

	"gopkg.in/yaml.v3"
)

const WorkersFileName = "workers.yaml"

type WorkerConfig struct {
	Name         string    `yaml:"name" json:"name"`
	Host         string    `yaml:"host" json:"host"`
	User         string    `yaml:"user" json:"user"`
	SSHTarget    string    `yaml:"sshTarget,omitempty" json:"sshTarget,omitempty"`
	Source       string    `yaml:"source,omitempty" json:"source,omitempty"`
	DNSName      string    `yaml:"dnsName,omitempty" json:"dnsName,omitempty"`
	TailscaleIPs []string  `yaml:"tailscaleIPs,omitempty" json:"tailscaleIPs,omitempty"`
	RegisteredAt time.Time `yaml:"registeredAt" json:"registeredAt"`
	UpdatedAt    time.Time `yaml:"updatedAt" json:"updatedAt"`
}

type WorkersFile struct {
	Workers []WorkerConfig `yaml:"workers" json:"workers"`
}

type WorkerStore struct {
	path string
}

func DefaultWorkersPath(stateDir string) string {
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".workyard")
	}
	return filepath.Join(stateDir, "local", WorkersFileName)
}

func NewWorkerStore(path string) WorkerStore {
	return WorkerStore{path: path}
}

func (s WorkerStore) Path() string {
	return s.path
}

func (s WorkerStore) List() ([]WorkerConfig, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]WorkerConfig(nil), file.Workers...)
	sortWorkers(out)
	return out, nil
}

func (s WorkerStore) Upsert(worker WorkerConfig) error {
	if err := validateWorkerConfig(worker); err != nil {
		return err
	}
	file, err := s.load()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	worker.UpdatedAt = now
	found := false
	for i, existing := range file.Workers {
		if sameWorker(existing, worker) {
			if existing.RegisteredAt.IsZero() {
				worker.RegisteredAt = now
			} else {
				worker.RegisteredAt = existing.RegisteredAt
			}
			file.Workers[i] = worker
			found = true
			break
		}
	}
	if !found {
		worker.RegisteredAt = now
		file.Workers = append(file.Workers, worker)
	}
	sortWorkers(file.Workers)
	return s.save(file)
}

func (s WorkerStore) Remove(name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || containsControl(name) {
		return false, fmt.Errorf("worker name is required")
	}
	file, err := s.load()
	if err != nil {
		return false, err
	}
	next := file.Workers[:0]
	removed := false
	for _, worker := range file.Workers {
		if worker.Name == name || worker.Host == name || worker.EffectiveSSHTarget() == name {
			removed = true
			continue
		}
		next = append(next, worker)
	}
	file.Workers = next
	if !removed {
		return false, nil
	}
	return true, s.save(file)
}

func (s WorkerStore) Resolve(value string) (WorkerConfig, bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return WorkerConfig{}, false, nil
	}
	workers, err := s.List()
	if err != nil {
		return WorkerConfig{}, false, err
	}
	for _, worker := range workers {
		if worker.Name == value || worker.Host == value || worker.EffectiveSSHTarget() == value || trimDNSName(worker.DNSName) == value {
			return worker, true, nil
		}
	}
	return WorkerConfig{}, false, nil
}

func (s WorkerStore) load() (WorkersFile, error) {
	if s.path == "" {
		return WorkersFile{}, errors.New("worker config path is required")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			if legacy, legacyErr := s.loadLegacyJSON(); legacyErr == nil {
				_ = s.save(legacy)
				return legacy, nil
			}
			return WorkersFile{}, nil
		}
		return WorkersFile{}, err
	}
	var file WorkersFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return WorkersFile{}, err
	}
	for i := range file.Workers {
		if err := validateWorkerConfig(file.Workers[i]); err != nil {
			return WorkersFile{}, fmt.Errorf("invalid worker config entry %d: %w", i, err)
		}
	}
	return file, nil
}

func (s WorkerStore) loadLegacyJSON() (WorkersFile, error) {
	legacyPath := legacyWorkersJSONPath(s.path)
	if legacyPath == "" {
		return WorkersFile{}, os.ErrNotExist
	}
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return WorkersFile{}, err
	}
	var file WorkersFile
	if err := json.Unmarshal(data, &file); err != nil {
		return WorkersFile{}, err
	}
	for i := range file.Workers {
		if err := validateWorkerConfig(file.Workers[i]); err != nil {
			return WorkersFile{}, fmt.Errorf("invalid legacy worker config entry %d: %w", i, err)
		}
	}
	return file, nil
}

func (s WorkerStore) save(file WorkersFile) error {
	if s.path == "" {
		return errors.New("worker config path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".workers-*.yaml")
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
	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)
	if err := enc.Encode(file); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := enc.Close(); err != nil {
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

func validateWorkerConfig(worker WorkerConfig) error {
	for label, value := range map[string]string{
		"name": worker.Name,
		"host": worker.Host,
		"user": worker.User,
	} {
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("%s is required", label)
		}
		if containsControl(value) {
			return fmt.Errorf("%s contains invalid characters", label)
		}
	}
	if strings.ContainsAny(worker.Name, " \t/\\@:") {
		return fmt.Errorf("name %q contains unsupported characters", worker.Name)
	}
	if strings.ContainsAny(worker.User, " \t/\\@:") {
		return fmt.Errorf("user %q contains unsupported characters", worker.User)
	}
	if worker.SSHTarget != "" && containsControl(worker.SSHTarget) {
		return fmt.Errorf("sshTarget contains invalid characters")
	}
	return nil
}

func (w WorkerConfig) EffectiveSSHTarget() string {
	if strings.TrimSpace(w.SSHTarget) != "" {
		return strings.TrimSpace(w.SSHTarget)
	}
	return w.User + "@" + w.Host
}

func sameWorker(a, b WorkerConfig) bool {
	return a.Name == b.Name || a.Host == b.Host || a.EffectiveSSHTarget() == b.EffectiveSSHTarget()
}

func sortWorkers(workers []WorkerConfig) {
	sort.Slice(workers, func(i, j int) bool { return workers[i].Name < workers[j].Name })
}

func trimDNSName(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), ".")
}

func legacyWorkersJSONPath(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return ""
	}
	return strings.TrimSuffix(path, ext) + ".json"
}

func containsControl(value string) bool {
	return strings.Contains(value, "\x00") || strings.ContainsAny(value, "\r\n")
}
