package worker

import (
	"fmt"

	"github.com/jackbelluche/workyard/internal/config"
)

func (d *Daemon) setup(req Request) Response {
	return d.projectLifecycle(req, "setup")
}

func (d *Daemon) build(req Request) Response {
	return d.projectLifecycle(req, "build")
}

func (d *Daemon) projectLifecycle(req Request, name string) Response {
	loaded, err := config.Load(sourceRoot(req.RunRoot))
	if err != nil {
		return errorResponse("CONFIG_LOAD_FAILED", err.Error(), "Run workyard sync again and confirm workyard.yaml exists in the synced source")
	}
	var cmd *config.LifecycleCommand
	switch name {
	case "setup":
		cmd = loaded.Config.Setup
	case "build":
		cmd = loaded.Config.Build
	default:
		return errorResponse("UNKNOWN_LIFECYCLE", fmt.Sprintf("unknown lifecycle command %q", name), "")
	}
	if cmd == nil || cmd.Command == "" {
		return Response{OK: true, Project: loaded.Config.Name, RunID: req.RunID, Worker: req.Worker, Message: name + " not configured"}
	}
	if err := runLifecycleCommand(req.RunRoot, lifecycleRun{
		Name:        name,
		Command:     *cmd,
		Cwd:         sourceRoot(req.RunRoot),
		Env:         projectLifecycleEnv(),
		EventPrefix: "project." + name,
	}); err != nil {
		return errorResponse("LIFECYCLE_FAILED", err.Error(), "Run workyard events --json or inspect the lifecycle logs")
	}
	return Response{OK: true, Project: loaded.Config.Name, RunID: req.RunID, Worker: req.Worker, Message: name + " completed"}
}
