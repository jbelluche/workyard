package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const FileName = "workyard.yaml"

var serviceNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)
var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Name     string             `yaml:"name" json:"name"`
	Sync     SyncConfig         `yaml:"sync" json:"sync"`
	Worker   WorkerConfig       `yaml:"worker" json:"worker,omitempty"`
	Setup    *LifecycleCommand  `yaml:"setup,omitempty" json:"setup,omitempty"`
	Build    *LifecycleCommand  `yaml:"build,omitempty" json:"build,omitempty"`
	Services map[string]Service `yaml:"services" json:"services"`
	Path     string             `yaml:"-" json:"-"`
	Root     string             `yaml:"-" json:"-"`
}

type SyncConfig struct {
	Exclude         []string `yaml:"exclude" json:"exclude"`
	IncludeEnvFiles bool     `yaml:"includeEnvFiles" json:"includeEnvFiles"`
}

type WorkerConfig struct {
	PortRange string `yaml:"portRange" json:"portRange,omitempty"`
}

type LifecycleCommand struct {
	Command string        `yaml:"command" json:"command"`
	Timeout time.Duration `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Shell   bool          `yaml:"shell,omitempty" json:"shell,omitempty"`
}

func (c *LifecycleCommand) UnmarshalYAML(value *yaml.Node) error {
	type raw struct {
		Command string `yaml:"command"`
		Timeout string `yaml:"timeout"`
		Shell   bool   `yaml:"shell"`
	}
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	c.Command = r.Command
	c.Shell = r.Shell
	if r.Timeout != "" {
		d, err := time.ParseDuration(r.Timeout)
		if err != nil {
			return fmt.Errorf("invalid lifecycle timeout: %w", err)
		}
		c.Timeout = d
	}
	return nil
}

func (c LifecycleCommand) MarshalJSON() ([]byte, error) {
	type out struct {
		Command string `json:"command"`
		Timeout string `json:"timeout,omitempty"`
		Shell   bool   `json:"shell,omitempty"`
	}
	o := out{Command: c.Command, Shell: c.Shell}
	if c.Timeout > 0 {
		o.Timeout = c.Timeout.String()
	}
	return json.Marshal(o)
}

type Service struct {
	Path         string            `yaml:"path" json:"path"`
	StartCommand string            `yaml:"startCommand" json:"startCommand"`
	Shell        bool              `yaml:"shell" json:"shell"`
	Port         PortConfig        `yaml:"port" json:"port"`
	Env          map[string]string `yaml:"env" json:"env,omitempty"`
	Health       HealthConfig      `yaml:"health" json:"health,omitempty"`
	BeforeStart  *LifecycleCommand `yaml:"beforeStart,omitempty" json:"beforeStart,omitempty"`
	OnClose      *LifecycleCommand `yaml:"onClose,omitempty" json:"onClose,omitempty"`
	Watch        *WatchConfig      `yaml:"watch,omitempty" json:"watch,omitempty"`
}

type WatchConfig struct {
	Paths    []string      `yaml:"paths" json:"paths"`
	Include  []string      `yaml:"include,omitempty" json:"include,omitempty"`
	Exclude  []string      `yaml:"exclude,omitempty" json:"exclude,omitempty"`
	Action   string        `yaml:"action,omitempty" json:"action,omitempty"`
	Debounce time.Duration `yaml:"debounce,omitempty" json:"debounce,omitempty"`
}

func (w *WatchConfig) UnmarshalYAML(value *yaml.Node) error {
	type raw struct {
		Paths    []string `yaml:"paths"`
		Include  []string `yaml:"include"`
		Exclude  []string `yaml:"exclude"`
		Action   string   `yaml:"action"`
		Debounce string   `yaml:"debounce"`
	}
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	w.Paths = r.Paths
	w.Include = r.Include
	w.Exclude = r.Exclude
	w.Action = r.Action
	if r.Debounce != "" {
		d, err := time.ParseDuration(r.Debounce)
		if err != nil {
			return fmt.Errorf("invalid watch debounce: %w", err)
		}
		w.Debounce = d
	}
	return nil
}

func (w WatchConfig) MarshalJSON() ([]byte, error) {
	type out struct {
		Paths    []string `json:"paths"`
		Include  []string `json:"include,omitempty"`
		Exclude  []string `json:"exclude,omitempty"`
		Action   string   `json:"action,omitempty"`
		Debounce string   `json:"debounce,omitempty"`
	}
	o := out{Paths: w.Paths, Include: w.Include, Exclude: w.Exclude, Action: w.Action}
	if w.Debounce > 0 {
		o.Debounce = w.Debounce.String()
	}
	return json.Marshal(o)
}

type PortConfig struct {
	Default int    `yaml:"default" json:"default"`
	Env     string `yaml:"env" json:"env"`
}

func (p *PortConfig) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		if value.Value == "" {
			return nil
		}
		port, err := strconv.Atoi(value.Value)
		if err != nil {
			return fmt.Errorf("port must be a number or mapping: %w", err)
		}
		p.Default = port
		p.Env = "PORT"
		return nil
	case yaml.MappingNode:
		type raw PortConfig
		var out raw
		if err := value.Decode(&out); err != nil {
			return err
		}
		if out.Env == "" {
			out.Env = "PORT"
		}
		*p = PortConfig(out)
		return nil
	default:
		return errors.New("port must be a number or mapping")
	}
}

type HealthConfig struct {
	URL     string        `yaml:"url" json:"url,omitempty"`
	Timeout time.Duration `yaml:"timeout" json:"timeout,omitempty"`
}

func (h *HealthConfig) UnmarshalYAML(value *yaml.Node) error {
	type raw struct {
		URL     string `yaml:"url"`
		Timeout string `yaml:"timeout"`
	}
	var r raw
	if err := value.Decode(&r); err != nil {
		return err
	}
	h.URL = r.URL
	if r.Timeout != "" {
		d, err := time.ParseDuration(r.Timeout)
		if err != nil {
			return fmt.Errorf("invalid health timeout: %w", err)
		}
		h.Timeout = d
	}
	return nil
}

func (h HealthConfig) MarshalJSON() ([]byte, error) {
	type out struct {
		URL     string `json:"url,omitempty"`
		Timeout string `json:"timeout,omitempty"`
	}
	o := out{URL: h.URL}
	if h.Timeout > 0 {
		o.Timeout = h.Timeout.String()
	}
	return json.Marshal(o)
}

type Loaded struct {
	Config   Config
	Warnings []string
}

func DefaultConfig(projectName string) Config {
	return Config{
		Name: projectName,
		Sync: SyncConfig{Exclude: []string{
			"node_modules",
			".next",
			".nuxt",
			".turbo",
			".vite",
			"dist",
			"build",
			"coverage",
			".cache",
		}},
		Worker: WorkerConfig{PortRange: "3100-3999"},
		Services: map[string]Service{
			"web": {
				Path:         ".",
				StartCommand: "python3 -m http.server ${WORKYARD_PORT}",
				Port:         PortConfig{Default: 3000, Env: "PORT"},
				Health:       HealthConfig{URL: "http://127.0.0.1:3000"},
			},
		},
	}
}

func Find(start string) (string, string, error) {
	root, err := filepath.Abs(start)
	if err != nil {
		return "", "", err
	}
	stat, err := os.Stat(root)
	if err == nil && !stat.IsDir() {
		root = filepath.Dir(root)
	}
	for {
		candidate := filepath.Join(root, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, root, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", "", fmt.Errorf("%s not found from %s", FileName, start)
		}
		root = parent
	}
}

func Load(projectPath string) (Loaded, error) {
	if projectPath == "" {
		projectPath = "."
	}
	path, root, err := Find(projectPath)
	if err != nil {
		return Loaded{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Loaded{}, err
	}
	if err := rejectUnsupportedCommandField(data); err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", path, err)
	}
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Loaded{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Path = path
	cfg.Root = root
	warnings, err := Validate(&cfg)
	return Loaded{Config: cfg, Warnings: warnings}, err
}

func Write(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func rejectUnsupportedCommandField(data []byte) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	root := mappingNode(&doc)
	if root == nil {
		return nil
	}
	services := mappingValue(root, "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(services.Content); i += 2 {
		serviceName := services.Content[i].Value
		service := services.Content[i+1]
		if service.Kind != yaml.MappingNode {
			continue
		}
		if mappingValue(service, "command") != nil {
			return fmt.Errorf("service %s uses unsupported field \"command\"; use \"startCommand\"", serviceName)
		}
	}
	return nil
}

func Validate(cfg *Config) ([]string, error) {
	var warnings []string
	var errs []string

	if strings.TrimSpace(cfg.Name) == "" {
		errs = append(errs, "name is required")
	}
	if len(cfg.Services) == 0 {
		errs = append(errs, "at least one service is required")
	}
	if cfg.Worker.PortRange == "" {
		cfg.Worker.PortRange = "3100-3999"
	}
	if err := validateLifecycle("setup", cfg.Setup); err != nil {
		errs = append(errs, err.Error())
	}
	if err := validateLifecycle("build", cfg.Build); err != nil {
		errs = append(errs, err.Error())
	}

	for _, exclude := range cfg.Sync.Exclude {
		if filepath.IsAbs(exclude) {
			errs = append(errs, fmt.Sprintf("sync exclude %q must not be absolute", exclude))
		}
		clean := filepath.Clean(exclude)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			errs = append(errs, fmt.Sprintf("sync exclude %q must not escape the project", exclude))
		}
	}
	if !cfg.Sync.IncludeEnvFiles && fileExists(filepath.Join(cfg.Root, ".env")) {
		warnings = append(warnings, ".env exists locally and will not be synced by default")
	}

	names := make([]string, 0, len(cfg.Services))
	for name := range cfg.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		svc := cfg.Services[name]
		if !serviceNameRE.MatchString(name) {
			errs = append(errs, fmt.Sprintf("service %q must match %s", name, serviceNameRE.String()))
		}
		if strings.TrimSpace(svc.StartCommand) == "" {
			errs = append(errs, fmt.Sprintf("service %q startCommand is required", name))
		}
		if svc.Path == "" {
			svc.Path = "."
		}
		if filepath.IsAbs(svc.Path) {
			errs = append(errs, fmt.Sprintf("service %q path must be relative", name))
		} else if err := ensureInside(cfg.Root, filepath.Join(cfg.Root, svc.Path)); err != nil {
			errs = append(errs, fmt.Sprintf("service %q path escapes project root", name))
		}
		servicePath := filepath.Join(cfg.Root, svc.Path)
		if !dirExists(servicePath) {
			warnings = append(warnings, fmt.Sprintf("service %q path does not exist: %s", name, svc.Path))
		}
		if svc.Port.Default != 0 && (svc.Port.Default < 1 || svc.Port.Default > 65535) {
			errs = append(errs, fmt.Sprintf("service %q port must be between 1 and 65535", name))
		}
		if svc.Port.Default != 0 && svc.Port.Env == "" {
			svc.Port.Env = "PORT"
		}
		if svc.Port.Env != "" && !envNameRE.MatchString(svc.Port.Env) {
			errs = append(errs, fmt.Sprintf("service %q port env %q is not a valid environment variable name", name, svc.Port.Env))
		}
		for key := range svc.Env {
			if !envNameRE.MatchString(key) {
				errs = append(errs, fmt.Sprintf("service %q env key %q is not a valid environment variable name", name, key))
			}
		}
		if svc.Health.URL != "" {
			if warn, err := ValidateHealthURL(svc.Health.URL); err != nil {
				errs = append(errs, fmt.Sprintf("service %q health url: %s", name, err))
			} else if warn != "" {
				warnings = append(warnings, fmt.Sprintf("service %q health url: %s", name, warn))
			}
		}
		if err := validateLifecycle(fmt.Sprintf("service %q beforeStart", name), svc.BeforeStart); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateLifecycle(fmt.Sprintf("service %q onClose", name), svc.OnClose); err != nil {
			errs = append(errs, err.Error())
		}
		if err := validateWatch(cfg.Root, name, svc.Watch); err != nil {
			errs = append(errs, err.Error())
		}
		cfg.Services[name] = svc
	}

	if len(errs) > 0 {
		return warnings, errors.New(strings.Join(errs, "; "))
	}
	return warnings, nil
}

func validateLifecycle(name string, cmd *LifecycleCommand) error {
	if cmd == nil {
		return nil
	}
	if strings.TrimSpace(cmd.Command) == "" {
		return fmt.Errorf("%s command is required when configured", name)
	}
	if cmd.Timeout < 0 {
		return fmt.Errorf("%s timeout must not be negative", name)
	}
	if cmd.Timeout > 30*time.Minute {
		return fmt.Errorf("%s timeout must be 30m or less", name)
	}
	return nil
}

func validateWatch(root, service string, watch *WatchConfig) error {
	if watch == nil {
		return nil
	}
	if len(watch.Paths) == 0 {
		return fmt.Errorf("service %q watch paths are required when watch is configured", service)
	}
	if watch.Action == "" {
		watch.Action = "sync-restart"
	}
	if watch.Action != "sync-restart" && watch.Action != "sync-only" {
		return fmt.Errorf("service %q watch action must be sync-restart or sync-only", service)
	}
	if watch.Debounce == 0 {
		watch.Debounce = 750 * time.Millisecond
	}
	if watch.Debounce < 100*time.Millisecond || watch.Debounce > 30*time.Second {
		return fmt.Errorf("service %q watch debounce must be between 100ms and 30s", service)
	}
	for _, p := range watch.Paths {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("service %q watch path must not be empty", service)
		}
		if filepath.IsAbs(p) {
			return fmt.Errorf("service %q watch path %q must be relative", service, p)
		}
		if err := ensureInside(root, filepath.Join(root, p)); err != nil {
			return fmt.Errorf("service %q watch path %q escapes project root", service, p)
		}
	}
	return nil
}

func mappingNode(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func ServiceNames(services map[string]Service) []string {
	names := make([]string, 0, len(services))
	for name := range services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ServicePath(sourceRoot string, svc Service) (string, error) {
	if svc.Path == "" {
		svc.Path = "."
	}
	path := filepath.Join(sourceRoot, svc.Path)
	if err := ensureInside(sourceRoot, path); err != nil {
		return "", err
	}
	return path, nil
}

func ensureInside(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = real
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if real, err := filepath.EvalSymlinks(pathAbs); err == nil {
		pathAbs = real
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("%s is outside %s", pathAbs, rootAbs)
	}
	return nil
}

func ValidateHealthURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return "", errors.New("host is required")
	}
	if host == "localhost" {
		return "", nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", errors.New("host must be localhost, loopback, or private IP by default")
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return "", nil
	}
	return "", errors.New("host must be localhost, loopback, or private IP by default")
}

func fileExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && !stat.IsDir()
}

func dirExists(path string) bool {
	stat, err := os.Stat(path)
	return err == nil && stat.IsDir()
}
