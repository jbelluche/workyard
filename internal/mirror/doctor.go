package mirror

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jackbelluche/workyard/internal/remote"
)

const (
	DoctorPass = "pass"
	DoctorWarn = "warn"
	DoctorFail = "fail"
)

type DoctorReport struct {
	OK       bool                  `json:"ok"`
	Checked  int                   `json:"checked"`
	Profiles []DoctorProfileReport `json:"profiles"`
}

type DoctorProfileReport struct {
	ID     string        `json:"id"`
	Name   string        `json:"name"`
	Worker string        `json:"worker"`
	Checks []DoctorCheck `json:"checks"`
}

type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
}

func Doctor(ctx context.Context, profiles []Profile) DoctorReport {
	report := DoctorReport{OK: true, Checked: len(profiles)}
	localRsync := commandAvailable("rsync")
	for _, profile := range profiles {
		pr := DoctorProfileReport{ID: profile.ID, Name: profile.Name, Worker: profile.Worker}
		add := func(name, status, message, detail string) {
			pr.Checks = append(pr.Checks, DoctorCheck{Name: name, Status: status, Message: message, Detail: detail})
			if status == DoctorFail {
				report.OK = false
			}
		}
		if info, err := os.Stat(profile.LocalRoot); err != nil {
			add("local-root", DoctorFail, "local directory is not readable", err.Error())
		} else if !info.IsDir() {
			add("local-root", DoctorFail, "local root is not a directory", profile.LocalRoot)
		} else {
			add("local-root", DoctorPass, "local directory exists", profile.LocalRoot)
		}
		if localRsync {
			add("local-rsync", DoctorPass, "local rsync is available", "")
		} else {
			add("local-rsync", DoctorFail, "local rsync is not available", "Install rsync on this machine")
		}
		if err := remote.ValidateWorker(profile.Worker); err != nil {
			add("worker", DoctorFail, "worker target is invalid", err.Error())
			report.Profiles = append(report.Profiles, pr)
			continue
		}
		home, err := remote.Home(ctx, profile.Worker)
		if err != nil {
			add("ssh", DoctorFail, "could not reach worker over SSH", err.Error())
			report.Profiles = append(report.Profiles, pr)
			continue
		}
		add("ssh", DoctorPass, "worker is reachable", home)
		if remoteCommandAvailable(ctx, profile.Worker, "rsync") {
			add("remote-rsync", DoctorPass, "remote rsync is available", "")
		} else {
			add("remote-rsync", DoctorFail, "remote rsync is not available", "Install rsync on the worker")
		}
		check, err := CheckDestination(ctx, profile)
		if err != nil {
			add("destination", DoctorFail, "could not inspect destination", err.Error())
		} else {
			status, message, detail := doctorDestinationStatus(ctx, profile, check)
			add("destination", status, message, detail)
		}
		detected := DetectPresets(profile.LocalRoot)
		switch {
		case len(profile.Presets) == 0 && len(detected) > 0:
			add("presets", DoctorWarn, "repository type was detected but no presets are stored", "detected: "+strings.Join(detected, ", "))
		case len(profile.Presets) > 0:
			add("presets", DoctorPass, "presets are configured", strings.Join(profile.Presets, ", "))
		default:
			add("presets", DoctorPass, "no repository preset detected", "")
		}
		report.Profiles = append(report.Profiles, pr)
	}
	return report
}

func doctorDestinationStatus(ctx context.Context, profile Profile, check DestinationCheck) (string, string, string) {
	switch check.State {
	case "missing":
		return DoctorPass, "destination does not exist yet", check.ResolvedPath
	case "empty":
		return DoctorPass, "destination exists and is empty", check.ResolvedPath
	case "marker-only":
		if check.OK {
			return DoctorPass, "destination has a matching mirror marker", check.ResolvedPath
		}
		return DoctorFail, "destination marker belongs to a different mirror", check.ResolvedPath
	case "non-empty":
		marker, err := ReadMarker(ctx, profile.Worker, check.ResolvedPath)
		if err == nil && MarkerMatches(marker, profile) {
			return DoctorPass, "destination contains a matching mirror marker", check.ResolvedPath
		}
		if err != nil {
			return DoctorFail, "destination is non-empty and has no readable mirror marker", err.Error()
		}
		return DoctorFail, "destination marker belongs to a different mirror", fmt.Sprintf("%s from %s", marker.Name, marker.LocalRoot)
	case "symlink":
		return DoctorFail, "destination is a symlink", check.ResolvedPath
	case "not-directory":
		return DoctorFail, "destination exists but is not a directory", check.ResolvedPath
	default:
		return DoctorFail, "destination state is unknown", check.ResolvedPath
	}
}

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func remoteCommandAvailable(ctx context.Context, worker, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	_, err := remote.Run(ctx, worker, []string{"sh", "-lc", "command -v " + remote.ShellQuote(name) + " >/dev/null"}, nil, 8*time.Second)
	return err == nil
}
