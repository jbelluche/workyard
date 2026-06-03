package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

func eventPath(runRoot, service string) string {
	if service == "" {
		return filepath.Join(logsDir(runRoot), "lifecycle.events.jsonl")
	}
	return filepath.Join(logsDir(runRoot), service+".events.jsonl")
}

func appendEvent(runRoot string, ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	_ = os.MkdirAll(logsDir(runRoot), 0o700)
	f, err := os.OpenFile(eventPath(runRoot, ev.Service), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(ev)
}
