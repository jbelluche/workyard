package watch

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/jackbelluche/workyard/internal/config"
)

var defaultExcludes = []string{".git", "node_modules", ".workyard", ".workyard-fixture", "__pycache__", ".next", ".turbo", "dist", "build", "coverage"}

type Spec struct {
	Service string
	Watch   config.WatchConfig
}

type FileState struct {
	ModTime time.Time
	Size    int64
}

func Snapshot(root string, specs []Spec) (map[string]FileState, error) {
	out := map[string]FileState{}
	for _, spec := range specs {
		for _, watchPath := range spec.Watch.Paths {
			base := filepath.Join(root, watchPath)
			if err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				rel, err := filepath.Rel(root, path)
				if err != nil {
					return nil
				}
				rel = filepath.ToSlash(rel)
				if entry.IsDir() {
					if rel != "." && excluded(rel, spec.Watch.Exclude) {
						return filepath.SkipDir
					}
					return nil
				}
				if !included(rel, spec.Watch.Include) || excluded(rel, spec.Watch.Exclude) {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return nil
				}
				out[rel] = FileState{ModTime: info.ModTime(), Size: info.Size()}
				return nil
			}); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
	}
	return out, nil
}

func Changed(before, after map[string]FileState) bool {
	if len(before) != len(after) {
		return true
	}
	for path, old := range before {
		next, ok := after[path]
		if !ok || !next.ModTime.Equal(old.ModTime) || next.Size != old.Size {
			return true
		}
	}
	return false
}

func Changes(ctx context.Context, root string, specs []Spec, pollInterval time.Duration) (<-chan struct{}, <-chan error, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return pollingChanges(ctx, root, specs, pollInterval)
	}
	changes := make(chan struct{}, 1)
	errs := make(chan error, 1)
	if err := addWatchTrees(w, root, specs); err != nil {
		_ = w.Close()
		return pollingChanges(ctx, root, specs, pollInterval)
	}
	go func() {
		defer close(changes)
		defer close(errs)
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				sendError(errs, err)
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
					continue
				}
				if ev.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
						_ = addWatchTree(w, root, ev.Name, specs)
					}
				}
				if eventMatches(root, ev.Name, specs) {
					sendChange(changes)
				}
			}
		}
	}()
	return changes, errs, nil
}

func pollingChanges(ctx context.Context, root string, specs []Spec, pollInterval time.Duration) (<-chan struct{}, <-chan error, error) {
	changes := make(chan struct{}, 1)
	errs := make(chan error, 1)
	snapshot, err := Snapshot(root, specs)
	if err != nil {
		return nil, nil, err
	}
	go func() {
		defer close(changes)
		defer close(errs)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				next, err := Snapshot(root, specs)
				if err != nil {
					sendError(errs, err)
					continue
				}
				if Changed(snapshot, next) {
					snapshot = next
					sendChange(changes)
				}
			}
		}
	}()
	return changes, errs, nil
}

func addWatchTrees(w *fsnotify.Watcher, root string, specs []Spec) error {
	for _, spec := range specs {
		for _, watchPath := range spec.Watch.Paths {
			base := filepath.Join(root, watchPath)
			if err := addWatchTree(w, root, base, []Spec{spec}); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func addWatchTree(w *fsnotify.Watcher, root, base string, specs []Spec) error {
	return filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, spec := range specs {
			if rel != "." && excluded(rel, spec.Watch.Exclude) {
				return filepath.SkipDir
			}
		}
		_ = w.Add(path)
		return nil
	})
}

func eventMatches(root, path string, specs []Spec) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, spec := range specs {
		if included(rel, spec.Watch.Include) && !excluded(rel, spec.Watch.Exclude) {
			return true
		}
	}
	return false
}

func sendChange(changes chan struct{}) {
	select {
	case changes <- struct{}{}:
	default:
	}
}

func sendError(errs chan error, err error) {
	select {
	case errs <- err:
	default:
	}
}

func included(rel string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matchPattern(pattern, rel) {
			return true
		}
	}
	return false
}

func excluded(rel string, patterns []string) bool {
	for _, pattern := range append(defaultExcludes, patterns...) {
		if matchPattern(pattern, rel) {
			return true
		}
	}
	return false
}

func matchPattern(pattern, rel string) bool {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	rel = filepath.ToSlash(rel)
	if pattern == "" {
		return false
	}
	if pattern == rel || strings.HasPrefix(rel, strings.TrimSuffix(pattern, "/")+"/") {
		return true
	}
	base := filepath.Base(rel)
	if ok, _ := filepath.Match(pattern, rel); ok {
		return true
	}
	if ok, _ := filepath.Match(pattern, base); ok {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		if ok, _ := filepath.Match(strings.TrimPrefix(pattern, "**/"), base); ok {
			return true
		}
	}
	return false
}
