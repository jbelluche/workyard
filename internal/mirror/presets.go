package mirror

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Preset struct {
	Name     string
	Detect   []string
	Excludes []string
}

var presetDefinitions = map[string]Preset{
	"node": {
		Name:   "node",
		Detect: []string{"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb", "deno.json", "deno.jsonc"},
		Excludes: []string{
			"node_modules",
			"*.tsbuildinfo",
			".parcel-cache",
			".svelte-kit",
			".angular",
			"out",
		},
	},
	"python": {
		Name:   "python",
		Detect: []string{"pyproject.toml", "requirements.txt", "setup.py", "setup.cfg", "Pipfile", "poetry.lock", "uv.lock", "*.py"},
		Excludes: []string{
			".venv",
			"venv",
			"env",
			".tox",
			".nox",
			".pytest_cache",
			".mypy_cache",
			".ruff_cache",
			".hypothesis",
			"htmlcov",
			"__pycache__",
			"*.pyc",
		},
	},
	"go": {
		Name:   "go",
		Detect: []string{"go.mod", "go.work"},
		Excludes: []string{
			"bin",
			".gocache",
			"coverage.out",
			"*.test",
		},
	},
	"rust": {
		Name:     "rust",
		Detect:   []string{"Cargo.toml", "Cargo.lock"},
		Excludes: []string{"target"},
	},
	"java": {
		Name:   "java",
		Detect: []string{"pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts", "gradlew"},
		Excludes: []string{
			"target",
			".gradle",
			"build",
			"out",
		},
	},
	"dotnet": {
		Name:   "dotnet",
		Detect: []string{"*.csproj", "*.sln", "global.json", "Directory.Build.props"},
		Excludes: []string{
			"bin",
			"obj",
			"TestResults",
		},
	},
	"ruby": {
		Name:   "ruby",
		Detect: []string{"Gemfile", "Gemfile.lock", "*.gemspec"},
		Excludes: []string{
			".bundle",
			"vendor/bundle",
			"tmp",
		},
	},
}

func PresetNames() []string {
	names := make([]string, 0, len(presetDefinitions))
	for name := range presetDefinitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func ResolvePresetSelection(root string, values []string) ([]string, error) {
	if len(values) == 0 {
		values = []string{"auto"}
	}
	var out []string
	for _, raw := range values {
		for _, value := range strings.Split(raw, ",") {
			value = strings.TrimSpace(strings.ToLower(value))
			if value == "" {
				continue
			}
			switch value {
			case "auto":
				out = append(out, DetectPresets(root)...)
			case "none":
				return nil, nil
			default:
				if _, ok := presetDefinitions[value]; !ok {
					return nil, fmt.Errorf("unknown preset %q (available: auto, none, %s)", value, strings.Join(PresetNames(), ", "))
				}
				out = append(out, value)
			}
		}
	}
	return uniqueSorted(out), nil
}

func DetectPresets(root string) []string {
	var detected []string
	for name, preset := range presetDefinitions {
		if presetMatches(root, preset) {
			detected = append(detected, name)
		}
	}
	sort.Strings(detected)
	return detected
}

func PresetExcludes(names []string) []string {
	var out []string
	for _, name := range names {
		preset, ok := presetDefinitions[strings.TrimSpace(strings.ToLower(name))]
		if !ok {
			continue
		}
		out = append(out, preset.Excludes...)
	}
	return uniqueSortedPreserveCase(out)
}

func ValidatePresets(names []string) error {
	for _, name := range names {
		name = strings.TrimSpace(strings.ToLower(name))
		if name == "" {
			continue
		}
		if _, ok := presetDefinitions[name]; !ok {
			return fmt.Errorf("unknown preset %q (available: %s)", name, strings.Join(PresetNames(), ", "))
		}
	}
	return nil
}

func presetMatches(root string, preset Preset) bool {
	for _, pattern := range preset.Detect {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err == nil && len(matches) > 0 {
			for _, match := range matches {
				if info, err := os.Stat(match); err == nil && !info.IsDir() {
					return true
				}
			}
		}
	}
	return presetMatchesNested(root, preset, 4)
}

func presetMatchesNested(root string, preset Preset, maxDepth int) bool {
	root = filepath.Clean(root)
	found := false
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if entry.IsDir() {
			if shouldSkipPresetDir(entry.Name()) {
				return filepath.SkipDir
			}
			if depth > maxDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > maxDepth+1 {
			return nil
		}
		base := entry.Name()
		for _, pattern := range preset.Detect {
			if ok, _ := filepath.Match(pattern, base); ok {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

func shouldSkipPresetDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".next", ".turbo", "dist", "build", "target", ".venv", "venv":
		return true
	default:
		return false
	}
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueSortedPreserveCase(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
