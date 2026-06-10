package worker

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

func eventPath(runRoot, service string) string {
	if service == "" {
		return filepath.Join(logsDir(runRoot), "lifecycle.events.jsonl")
	}
	return filepath.Join(logsDir(runRoot), service+".events.jsonl")
}

// writeFailures dedupes write-failure log lines per path so a persistently
// broken event file does not flood the daemon log.
var writeFailures sync.Map

func reportWriteFailure(kind, path string, err error) {
	if _, seen := writeFailures.LoadOrStore(kind+":"+path, true); seen {
		return
	}
	log.Printf("workyard daemon: failed to write %s at %s: %v", kind, path, err)
}

func appendEvent(runRoot string, ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	path := eventPath(runRoot, ev.Service)
	if err := os.MkdirAll(logsDir(runRoot), 0o700); err != nil {
		reportWriteFailure("event log dir", logsDir(runRoot), err)
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		reportWriteFailure("event log", path, err)
		return
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(ev); err != nil {
		reportWriteFailure("event log", path, err)
	}
}
