package globalconfig

import (
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackbelluche/workyard/internal/registry"
	"github.com/jackbelluche/workyard/internal/remote"
	"github.com/pelletier/go-toml/v2"
)

const FileName = "config.toml"

type Config struct {
	Defaults    Defaults       `toml:"defaults" json:"defaults,omitempty"`
	Workers     []WorkerConfig `toml:"workers" json:"workers,omitempty"`
	KnownHosts  []WorkerConfig `toml:"known_hosts" json:"knownHosts,omitempty"`
	StaticHosts []WorkerConfig `toml:"static_hosts" json:"staticHosts,omitempty"`
}

type Defaults struct {
	SSHUser         string `toml:"ssh_user" json:"sshUser,omitempty"`
	RemoteWorkspace string `toml:"remote_workspace" json:"remoteWorkspace,omitempty"`
}

type WorkerConfig struct {
	ID              string `toml:"id" json:"id,omitempty"`
	Name            string `toml:"name" json:"name,omitempty"`
	Host            string `toml:"host" json:"host,omitempty"`
	User            string `toml:"user" json:"user,omitempty"`
	SSH             string `toml:"ssh" json:"ssh,omitempty"`
	SSHTarget       string `toml:"ssh_target" json:"sshTarget,omitempty"`
	RemoteWorkspace string `toml:"remote_workspace" json:"remoteWorkspace,omitempty"`
	Enabled         *bool  `toml:"enabled" json:"enabled,omitempty"`
}

type Loaded struct {
	Path   string `json:"path"`
	Found  bool   `json:"found"`
	Config Config `json:"config"`
}

func DefaultPath(stateDir string) string {
	if stateDir == "" {
		home, _ := os.UserHomeDir()
		stateDir = filepath.Join(home, ".workyard")
	}
	return filepath.Join(stateDir, FileName)
}

func LoadDefault(stateDir string) (Loaded, error) {
	return Load(DefaultPath(stateDir))
}

func Load(configPath string) (Loaded, error) {
	if strings.TrimSpace(configPath) == "" {
		return Loaded{}, errors.New("global config path is required")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Loaded{Path: configPath}, nil
		}
		return Loaded{}, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Loaded{}, err
	}
	loaded := Loaded{Path: configPath, Found: true, Config: cfg}
	if _, err := loaded.Workers(); err != nil {
		return Loaded{}, err
	}
	return loaded, nil
}

func (l Loaded) Workers() ([]registry.WorkerConfig, error) {
	return l.Config.WorkersFromConfig()
}

func (c Config) WorkersFromConfig() ([]registry.WorkerConfig, error) {
	var out []registry.WorkerConfig
	entries := []struct {
		section string
		workers []WorkerConfig
	}{
		{section: "workers", workers: c.Workers},
		{section: "known_hosts", workers: c.KnownHosts},
		{section: "static_hosts", workers: c.StaticHosts},
	}
	for _, entry := range entries {
		for i, worker := range entry.workers {
			if !worker.enabled() {
				continue
			}
			converted, err := worker.toRegistry(c.Defaults)
			if err != nil {
				return nil, fmt.Errorf("%s[%d]: %w", entry.section, i, err)
			}
			out = append(out, converted)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (w WorkerConfig) enabled() bool {
	return w.Enabled == nil || *w.Enabled
}

func (w WorkerConfig) toRegistry(defaults Defaults) (registry.WorkerConfig, error) {
	ssh := firstNonEmpty(w.SSH, w.SSHTarget)
	inputUser, inputHost, hasInputUser := splitSSHTarget(ssh)
	user := firstNonEmpty(w.User, inputUser, defaults.SSHUser, currentUsername())
	host := firstNonEmpty(w.Host, inputHost, ssh)
	name := firstNonEmpty(w.ID, w.Name)
	if name == "" {
		name = sanitizeName(firstNonEmpty(displayName(host), displayName(ssh)))
	}
	if name == "" {
		return registry.WorkerConfig{}, errors.New("name or host is required")
	}
	if hasInputUser {
		user = inputUser
	}
	if user == "" {
		return registry.WorkerConfig{}, errors.New("user or defaults.ssh_user is required")
	}
	if host == "" {
		return registry.WorkerConfig{}, errors.New("host or ssh is required")
	}
	if err := validateSSHTarget(ssh); err != nil {
		return registry.WorkerConfig{}, err
	}
	worker := registry.WorkerConfig{
		Name:            name,
		Host:            host,
		User:            user,
		Source:          "config",
		RemoteWorkspace: firstNonEmpty(w.RemoteWorkspace, defaults.RemoteWorkspace),
	}
	if ssh != "" {
		worker.SSHTarget = ssh
	}
	if err := registry.ValidateWorkerConfig(worker); err != nil {
		return registry.WorkerConfig{}, err
	}
	if err := remote.ValidateWorker(worker.EffectiveSSHTarget()); err != nil {
		return registry.WorkerConfig{}, err
	}
	return worker, nil
}

func validateSSHTarget(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("ssh target %q must not start with '-'", value)
	}
	if strings.ContainsAny(value, "\x00\r\n\t /\\:;|&<>`'\"") {
		return fmt.Errorf("ssh target %q contains unsupported characters", value)
	}
	return nil
}

func splitSSHTarget(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", value, false
	}
	return parts[0], parts[1], true
}

func displayName(value string) string {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "@") {
		_, host, ok := splitSSHTarget(value)
		if ok {
			value = host
		}
	}
	value = strings.TrimSuffix(value, ".")
	if strings.Contains(value, ".") {
		return strings.Split(value, ".")[0]
	}
	return value
}

func sanitizeName(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '.' {
			break
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func JoinRemoteWorkspace(workspace, localRoot string) string {
	base := filepath.Base(filepath.Clean(localRoot))
	if strings.TrimSpace(base) == "" || base == "." || base == string(filepath.Separator) {
		base = "mirror"
	}
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = "~/workspace"
	}
	return path.Join(workspace, base)
}

func currentUsername() string {
	for _, key := range []string{"USER", "LOGNAME"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if current, err := osuser.Current(); err == nil && current != nil {
		if value := strings.TrimSpace(current.Username); value != "" {
			if strings.Contains(value, "\\") {
				parts := strings.Split(value, "\\")
				return parts[len(parts)-1]
			}
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
