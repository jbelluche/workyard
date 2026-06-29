package mirror

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/jackbelluche/workyard/internal/runid"
	"gopkg.in/yaml.v3"
)

const FileName = "mirrors.yaml"

var idRE = regexp.MustCompile(`^[a-z0-9]{6,8}$`)

type Profile struct {
	ID            string    `yaml:"id" json:"id"`
	Name          string    `yaml:"name" json:"name"`
	Enabled       bool      `yaml:"enabled" json:"enabled"`
	LocalRoot     string    `yaml:"localRoot" json:"localRoot"`
	Worker        string    `yaml:"worker" json:"worker"`
	RemotePath    string    `yaml:"remotePath" json:"remotePath"`
	Delete        bool      `yaml:"delete" json:"delete"`
	IncludeGit    bool      `yaml:"includeGit" json:"includeGit"`
	AllowNonEmpty bool      `yaml:"allowNonEmpty,omitempty" json:"allowNonEmpty,omitempty"`
	Presets       []string  `yaml:"presets,omitempty" json:"presets,omitempty"`
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

type AmbiguousRefError struct {
	Ref string
	IDs []string
}

func (e AmbiguousRefError) Error() string {
	return fmt.Sprintf("mirror name %q is ambiguous", e.Ref)
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

func (s Store) Get(ref string) (Profile, bool, error) {
	ref = strings.TrimSpace(ref)
	profiles, err := s.List()
	if err != nil {
		return Profile{}, false, err
	}
	return Resolve(profiles, ref)
}

func Resolve(profiles []Profile, ref string) (Profile, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Profile{}, false, fmt.Errorf("mirror reference is required")
	}
	for _, profile := range profiles {
		if profile.ID == ref {
			return profile, true, nil
		}
	}
	var matches []Profile
	for _, profile := range profiles {
		if profile.Name == ref {
			matches = append(matches, profile)
		}
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, profile := range matches {
			ids = append(ids, profile.ID)
		}
		sort.Strings(ids)
		return Profile{}, false, AmbiguousRefError{Ref: ref, IDs: ids}
	}
	return Profile{}, false, nil
}

func (s Store) Upsert(profile Profile) (Profile, error) {
	file, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	if strings.TrimSpace(profile.ID) == "" {
		id, err := newUniqueID(file.Mirrors)
		if err != nil {
			return Profile{}, err
		}
		profile.ID = id
	}
	profile, err = Normalize(profile)
	if err != nil {
		return Profile{}, err
	}
	now := time.Now().UTC()
	profile.UpdatedAt = now
	found := false
	for i, existing := range file.Mirrors {
		if existing.ID != profile.ID {
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

func (s Store) SetEnabled(ref string, enabled bool) (Profile, bool, error) {
	file, err := s.load()
	if err != nil {
		return Profile{}, false, err
	}
	profile, ok, err := Resolve(file.Mirrors, ref)
	if err != nil || !ok {
		return Profile{}, ok, err
	}
	now := time.Now().UTC()
	for i := range file.Mirrors {
		if file.Mirrors[i].ID == profile.ID {
			file.Mirrors[i].Enabled = enabled
			file.Mirrors[i].UpdatedAt = now
			profile = file.Mirrors[i]
			break
		}
	}
	return profile, true, s.save(file)
}

func (s Store) Delete(ref string) (Profile, bool, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.ContainsAny(ref, "\x00\r\n/\\") {
		return Profile{}, false, fmt.Errorf("mirror reference is required")
	}
	file, err := s.load()
	if err != nil {
		return Profile{}, false, err
	}
	target, ok, err := Resolve(file.Mirrors, ref)
	if err != nil || !ok {
		return Profile{}, ok, err
	}
	next := file.Mirrors[:0]
	var removed Profile
	found := false
	for _, profile := range file.Mirrors {
		if profile.ID == target.ID {
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
	changed := false
	for i := range file.Mirrors {
		if strings.TrimSpace(file.Mirrors[i].ID) == "" {
			id, err := newUniqueID(file.Mirrors)
			if err != nil {
				return File{}, err
			}
			file.Mirrors[i].ID = id
			changed = true
		}
		profile, err := Normalize(file.Mirrors[i])
		if err != nil {
			return File{}, fmt.Errorf("invalid mirror entry %d: %w", i, err)
		}
		file.Mirrors[i] = profile
	}
	if changed {
		if err := s.save(file); err != nil {
			return File{}, err
		}
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
	id := strings.TrimSpace(profile.ID)
	if id != "" {
		if err := ValidateID(id); err != nil {
			return Profile{}, err
		}
	}
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
	presets, err := NormalizePresets(profile.Presets)
	if err != nil {
		return Profile{}, err
	}
	profile.ID = id
	profile.Name = name
	profile.LocalRoot = localRoot
	profile.Worker = strings.TrimSpace(profile.Worker)
	profile.RemotePath = remotePath
	profile.Presets = presets
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

func NormalizePresets(names []string) ([]string, error) {
	if err := ValidatePresets(names); err != nil {
		return nil, err
	}
	return uniqueSorted(names), nil
}

func ValidateID(id string) error {
	if id == "" {
		return fmt.Errorf("mirror id is required")
	}
	if !idRE.MatchString(id) {
		return fmt.Errorf("mirror id %q must be 6-8 lowercase letters or digits", id)
	}
	return nil
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
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].Name != profiles[j].Name {
			return profiles[i].Name < profiles[j].Name
		}
		return profiles[i].ID < profiles[j].ID
	})
}

func MarshalForDisplay(profile Profile) map[string]any {
	data, _ := json.Marshal(profile)
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

const idAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

func newUniqueID(existing []Profile) (string, error) {
	seen := map[string]bool{}
	for _, profile := range existing {
		if profile.ID != "" {
			seen[profile.ID] = true
		}
	}
	for attempts := 0; attempts < 100; attempts++ {
		id, err := randomID(7)
		if err != nil {
			return "", err
		}
		if !seen[id] {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique mirror id")
}

func randomID(length int) (string, error) {
	var b strings.Builder
	b.Grow(length)
	max := big.NewInt(int64(len(idAlphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(idAlphabet[n.Int64()])
	}
	return b.String(), nil
}
