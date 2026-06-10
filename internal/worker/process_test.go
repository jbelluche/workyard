package worker

import (
	"os"
	"runtime"
	"testing"
)

func TestCurrentProcessIDCapturesStartTime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("start time identity not implemented for " + runtime.GOOS)
	}
	id := currentProcessID(os.Getpid())
	if id.StartTime == "" {
		t.Fatalf("expected a start time for pid %d", os.Getpid())
	}
	if !processIdentityMatches(id) {
		t.Fatal("expected own process identity to match")
	}
}

func TestProcessIdentityRejectsWrongStartTime(t *testing.T) {
	id := currentProcessID(os.Getpid())
	if id.StartTime == "" {
		t.Skip("no start time on this platform")
	}
	id.StartTime = "definitely-not-the-real-start-time"
	if processIdentityMatches(id) {
		t.Fatal("expected mismatched start time to be rejected")
	}
}
