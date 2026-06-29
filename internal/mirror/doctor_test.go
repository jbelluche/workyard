package mirror

import (
	"context"
	"path/filepath"
	"testing"
)

func TestDoctorFailsInvalidLocalRootBeforeRemoteChecks(t *testing.T) {
	report := Doctor(context.Background(), []Profile{{
		ID:         "abc1234",
		Name:       "project",
		Enabled:    true,
		LocalRoot:  filepath.Join(t.TempDir(), "missing"),
		Worker:     "bad worker",
		RemotePath: "~/workspace/project",
	}})
	if report.OK {
		t.Fatalf("expected doctor report to fail: %#v", report)
	}
	if len(report.Profiles) != 1 {
		t.Fatalf("profiles=%#v", report.Profiles)
	}
	foundLocalFailure := false
	for _, check := range report.Profiles[0].Checks {
		if check.Name == "local-root" && check.Status == DoctorFail {
			foundLocalFailure = true
		}
	}
	if !foundLocalFailure {
		t.Fatalf("missing local-root failure: %#v", report.Profiles[0].Checks)
	}
}
