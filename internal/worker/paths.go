package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackbelluche/workyard/internal/runid"
)

func (d *Daemon) validateRequest(req Request) (Request, error) {
	if req.Action == "ping" {
		return req, nil
	}
	if req.RunRoot == "" {
		return req, fmt.Errorf("run root is required")
	}
	cleanRoot, project, run, err := d.validateRunRoot(req.RunRoot)
	if err != nil {
		return req, err
	}
	if req.Project != "" && req.Project != project {
		return req, fmt.Errorf("project %q does not match run root project %q", req.Project, project)
	}
	if req.RunID != "" && req.RunID != run {
		return req, fmt.Errorf("run id %q does not match run root run %q", req.RunID, run)
	}
	req.RunRoot = cleanRoot
	req.Project = project
	req.RunID = run
	return req, nil
}

func (d *Daemon) validateRunRoot(runRoot string) (string, string, string, error) {
	stateDir := d.opts.StateDir
	if stateDir == "" {
		stateDir = defaultStateDir()
	}
	runsRoot, err := filepath.Abs(filepath.Join(stateDir, "runs"))
	if err != nil {
		return "", "", "", err
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(runRoot))
	if err != nil {
		return "", "", "", err
	}
	if real, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		cleanRoot = real
	}
	if real, err := filepath.EvalSymlinks(runsRoot); err == nil {
		runsRoot = real
	}
	rel, err := filepath.Rel(runsRoot, cleanRoot)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) || len(parts) != 2 {
		return "", "", "", fmt.Errorf("run root must be exactly under %s/<project>/<run>", runsRoot)
	}
	project, err := runid.ProjectName(parts[0])
	if err != nil || project != parts[0] {
		return "", "", "", fmt.Errorf("invalid run root project %q", parts[0])
	}
	run, err := runid.Validate(parts[1])
	if err != nil {
		return "", "", "", fmt.Errorf("invalid run root run id %q: %w", parts[1], err)
	}
	if _, err := os.Stat(cleanRoot); err != nil && !os.IsNotExist(err) {
		return "", "", "", err
	}
	return cleanRoot, project, run, nil
}
