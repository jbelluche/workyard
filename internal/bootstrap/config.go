package bootstrap

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultConfigName = "workyard.bootstrap.yaml"

type Config struct {
	Version int                   `yaml:"version" json:"version"`
	Workers map[string]WorkerSpec `yaml:"workers" json:"workers"`
}

type WorkerSpec struct {
	SSH       SSHSpec       `yaml:"ssh" json:"ssh"`
	Register  *bool         `yaml:"register" json:"register,omitempty"`
	Workyard  WorkyardSpec  `yaml:"workyard" json:"workyard"`
	Tailscale TailscaleSpec `yaml:"tailscale" json:"tailscale"`
	Packages  PackageSpec   `yaml:"packages" json:"packages"`
	Docker    DockerSpec    `yaml:"docker" json:"docker"`
	Checks    ChecksSpec    `yaml:"checks" json:"checks"`
}

type SSHSpec struct {
	User   string `yaml:"user" json:"user"`
	Host   string `yaml:"host" json:"host"`
	Target string `yaml:"target" json:"target,omitempty"`
}

type WorkyardSpec struct {
	Install *bool `yaml:"install" json:"install,omitempty"`
	Daemon  *bool `yaml:"daemon" json:"daemon,omitempty"`
}

type TailscaleSpec struct {
	RequireConnected *bool `yaml:"requireConnected" json:"requireConnected,omitempty"`
}

type PackageSpec struct {
	Install *bool        `yaml:"install" json:"install,omitempty"`
	Apt     []AptPackage `yaml:"apt" json:"apt,omitempty"`
}

type AptPackage struct {
	Name    string `yaml:"name" json:"name"`
	Version string `yaml:"version,omitempty" json:"version,omitempty"`
}

type DockerSpec struct {
	Required       *bool  `yaml:"required" json:"required,omitempty"`
	Install        *bool  `yaml:"install" json:"install,omitempty"`
	ComposePlugin  *bool  `yaml:"composePlugin" json:"composePlugin,omitempty"`
	AddUserToGroup *bool  `yaml:"addUserToGroup" json:"addUserToGroup,omitempty"`
	Version        string `yaml:"version,omitempty" json:"version,omitempty"`
	ComposeVersion string `yaml:"composeVersion,omitempty" json:"composeVersion,omitempty"`
}

type ChecksSpec struct {
	Doctor *bool `yaml:"doctor" json:"doctor,omitempty"`
}

func LoadConfig(path string, required bool) (Config, bool, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultConfigName
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) && !required {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, err
	}
	if cfg.Version != 0 && cfg.Version != 1 {
		return Config{}, false, fmt.Errorf("unsupported bootstrap config version %d", cfg.Version)
	}
	for name, spec := range cfg.Workers {
		if strings.TrimSpace(name) == "" {
			return Config{}, false, errors.New("worker names must not be empty")
		}
		if strings.TrimSpace(spec.SSH.Target) == "" && strings.TrimSpace(spec.SSH.Host) == "" {
			return Config{}, false, fmt.Errorf("worker %q must set ssh.host or ssh.target", name)
		}
		for i, pkg := range spec.Packages.Apt {
			if err := validateAptPackage(pkg); err != nil {
				return Config{}, false, fmt.Errorf("worker %q packages.apt[%d]: %w", name, i, err)
			}
		}
		if err := validatePackageVersion(spec.Docker.Version); err != nil {
			return Config{}, false, fmt.Errorf("worker %q docker.version: %w", name, err)
		}
		if err := validatePackageVersion(spec.Docker.ComposeVersion); err != nil {
			return Config{}, false, fmt.Errorf("worker %q docker.composeVersion: %w", name, err)
		}
	}
	return cfg, true, nil
}

func (p *AptPackage) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		p.Name = strings.TrimSpace(value.Value)
		p.Version = ""
		return nil
	case yaml.MappingNode:
		type raw AptPackage
		var out raw
		if err := value.Decode(&out); err != nil {
			return err
		}
		p.Name = strings.TrimSpace(out.Name)
		p.Version = strings.TrimSpace(out.Version)
		return nil
	default:
		return fmt.Errorf("expected package name or mapping")
	}
}

func boolDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func validateAptPackage(pkg AptPackage) error {
	if strings.TrimSpace(pkg.Name) == "" {
		return errors.New("name is required")
	}
	if strings.ContainsAny(pkg.Name, "\x00\r\n\t =") {
		return fmt.Errorf("name %q contains unsupported characters", pkg.Name)
	}
	return validatePackageVersion(pkg.Version)
}

func validatePackageVersion(version string) error {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil
	}
	if strings.ContainsAny(version, "\x00\r\n\t ") {
		return fmt.Errorf("version %q contains unsupported whitespace or control characters", version)
	}
	return nil
}
