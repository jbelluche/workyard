package mirror

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/jackbelluche/workyard/internal/runid"
	"gopkg.in/yaml.v3"
)

const FileName = "mirrors.yaml"

type Profile struct {
	Name          string    `yaml:"name" json:"name"`
	Enabled       bool      `yaml:"enabled" json:"enabled"`
	LocalRoot     string    `yaml:"localRoot" json:"localRoot"`
	Worker        string    `yaml:"worker" json:"worker"`
	RemotePath    string    `yaml:"remotePath" json:"remotePath"`
	Delete        bool      `yaml:"delete" json:"delete"`
	IncludeGit    bool      `yaml:"includeGit" json:"includeGit"`
	AllowNonEmpty bool      `yaml:"allowNonEmpty,omitempty" json:"allowNonEmpty,omitempty"`
	Exclude       []string  `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	RegisteredAt  time.Time `yaml:"registeredAt" json:"registeredAt"`
	UpdatedAt     time.Time `yaml:"updatedAt" json:"updatedAt"`
}

type File struct {
	Mirrors []Profile `yaml:"mirrors" json:"mirrors"`
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

func NewStore(path string) Store {
	return Store{path: path}
}

func (s Store) Path() string {
	return s.path
}

func (s Store) List() ([]Profile, error) {
	file, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]Profile(nil), file.Mirrors...)
	sortProfiles(out)
	return out, nil
}

func (s Store) Get(name string) (Profile, bool, error) {
	name = strings.TrimSpace(name)
	profiles, err := s.List()
	if err != nil {
		return Profile{}, false, err
	}
	for _, profile := range profiles {
		if profile.Name == name {
			return profile, true, nil
		}
	}
	return Profile{}, false, nil
}

func (s Store) Upsert(profile Profile) (Profile, error) {
	profile, err := Normalize(profile)
	if err != nil {
		return Profile{}, err
	}
	file, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	now := time.Now().UTC()
	profile.UpdatedAt = now
	found := false
	for i, existing := range file.Mirrors {
		if existing.Name != profile.Name {
			continue
		}
		if existing.RegisteredAt.IsZero() {
			profile.RegisteredAt = now
		} else {
			profile.RegisteredAt = existing.RegisteredAt
		}
		file.Mirrors[i] = profile
		found = true
		break
	}
	if !found {
		profile.RegisteredAt = now
		file.Mirrors = append(file.Mirrors, profile)
	}
	sortProfiles(file.Mirrors)
	if err := s.save(file); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (s Store) Delete(name string) (Profile, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "\x00\r\n/\\") {
		return Profile{}, false, fmt.Errorf("mirror name is required")
	}
	file, err := s.load()
	if err != nil {
		return Profile{}, false, err
	}
	next := file.Mirrors[:0]
	var removed Profile
	found := false
	for _, profile := range file.Mirrors {
		if profile.Name == name {
			removed = profile
			found = true
			continue
		}
		next = append(next, profile)
	}
	file.Mirrors = next
	if !found {
		return Profile{}, false, nil
	}
	return removed, true, s.save(file)
}

func (s Store) load() (File, error) {
	if s.path == "" {
		return File{}, errors.New("mirror registry path is required")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{}, nil
		}
		return File{}, err
	}
	var file File
	if err := yaml.Unmarshal(data, &file); err != nil {
		return File{}, err
	}
	for i := range file.Mirrors {
		profile, err := Normalize(file.Mirrors[i])
		if err != nil {
			return File{}, fmt.Errorf("invalid mirror entry %d: %w", i, err)
		}
		file.Mirrors[i] = profile
	}
	return file, nil
}

func (s Store) save(file File) error {
	if s.path == "" {
		return errors.New("mirror registry path is required")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mirrors-*.yaml")
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

func Normalize(profile Profile) (Profile, error) {
	name, err := ValidateName(profile.Name)
	if err != nil {
		return Profile{}, err
	}
	localRoot, err := normalizeLocalRoot(profile.LocalRoot)
	if err != nil {
		return Profile{}, err
	}
	if err := remote.ValidateWorker(profile.Worker); err != nil {
		return Profile{}, err
	}
	remotePath := strings.TrimSpace(profile.RemotePath)
	if err := ValidateRemotePath(remotePath); err != nil {
		return Profile{}, err
	}
	profile.Name = name
	profile.LocalRoot = localRoot
	profile.Worker = strings.TrimSpace(profile.Worker)
	profile.RemotePath = remotePath
	if !profile.Enabled {
		// Existing zero-valued profiles from manual edits should remain disabled,
		// but new profiles are created enabled by the CLI before Normalize.
		profile.Enabled = false
	}
	for _, exclude := range profile.Exclude {
		exclude = strings.TrimSpace(exclude)
		if exclude == "" {
			continue
		}
		if filepath.IsAbs(exclude) {
			return Profile{}, fmt.Errorf("exclude %q must be relative", exclude)
		}
		clean := filepath.Clean(exclude)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Profile{}, fmt.Errorf("exclude %q escapes the mirror root", exclude)
		}
	}
	return profile, nil
}

func ValidateName(name string) (string, error) {
	safe, err := runid.ProjectName(name)
	if err != nil {
		return "", err
	}
	if safe != strings.TrimSpace(name) {
		return "", fmt.Errorf("mirror name %q may only contain letters, numbers, dots, underscores, and dashes", name)
	}
	return safe, nil
}

func DefaultName(localRoot string) string {
	base := filepath.Base(filepath.Clean(localRoot))
	name, err := runid.ProjectName(base)
	if err != nil || name == "" {
		return "mirror"
	}
	return name
}

func DefaultRemotePath(localRoot string) string {
	base := filepath.Base(filepath.Clean(localRoot))
	if strings.TrimSpace(base) == "" || base == "." || base == string(filepath.Separator) {
		base = "mirror"
	}
	return "~/workspace/" + base
}

func normalizeLocalRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("local root is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("local root must be a directory: %s", abs)
	}
	return abs, nil
}

func ValidateRemotePath(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("remote path is required")
	}
	if strings.Contains(value, "\x00") || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("remote path contains invalid characters")
	}
	if value == "/" || value == "~" || value == "." || value == ".." {
		return fmt.Errorf("remote path %q is too broad", value)
	}
	return nil
}

func sortProfiles(profiles []Profile) {
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
}

func MarshalForDisplay(profile Profile) map[string]any {
	data, _ := json.Marshal(profile)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}
