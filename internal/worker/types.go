package worker

import "time"

type Request struct {
	Action   string   `json:"action"`
	RunRoot  string   `json:"runRoot"`
	Project  string   `json:"project"`
	RunID    string   `json:"runId"`
	Worker   string   `json:"worker"`
	Services []string `json:"services,omitempty"`
	All      bool     `json:"all,omitempty"`
	Tail     int      `json:"tail,omitempty"`
	MaxBytes int64    `json:"maxBytes,omitempty"`
	Stream   string   `json:"stream,omitempty"`
	Healthy  bool     `json:"healthy,omitempty"`
	Status   string   `json:"status,omitempty"`
	Timeout  string   `json:"timeout,omitempty"`
}

type Response struct {
	OK       bool           `json:"ok"`
	Project  string         `json:"project,omitempty"`
	RunID    string         `json:"runId,omitempty"`
	Worker   string         `json:"worker,omitempty"`
	Services []ServiceState `json:"services,omitempty"`
	Entries  []LogEntry     `json:"entries,omitempty"`
	Events   []Event        `json:"events,omitempty"`
	URLs     []PreviewURL   `json:"urls,omitempty"`
	Hints    []Hint         `json:"hints,omitempty"`
	Message  string         `json:"message,omitempty"`
	Error    *Error         `json:"error,omitempty"`
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type RunState struct {
	Project   string                  `json:"project"`
	RunID     string                  `json:"runId"`
	Worker    string                  `json:"worker,omitempty"`
	UpdatedAt time.Time               `json:"updatedAt"`
	Services  map[string]ServiceState `json:"services"`
}

type ServiceState struct {
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	Healthy        bool      `json:"healthy"`
	PID            int       `json:"pid,omitempty"`
	Process        ProcessID `json:"process,omitempty,omitzero"`
	StartedAt      time.Time `json:"startedAt,omitempty,omitzero"`
	StoppedAt      time.Time `json:"stoppedAt,omitempty,omitzero"`
	ExitCode       *int      `json:"exitCode,omitempty"`
	StartCommand   string    `json:"startCommand,omitempty"`
	Cwd            string    `json:"cwd,omitempty"`
	ConfiguredPort int       `json:"configuredPort,omitempty"`
	AssignedPort   int       `json:"assignedPort,omitempty"`
	PortEnv        string    `json:"portEnv,omitempty"`
	URL            string    `json:"url,omitempty"`
	HealthURL      string    `json:"healthUrl,omitempty"`
	Logs           LogPaths  `json:"logs,omitempty"`
	RecentErrors   []string  `json:"recentErrors,omitempty"`
	LogsCommand    string    `json:"logsCommand,omitempty"`
}

type ProcessID struct {
	PID       int    `json:"pid,omitempty"`
	PGID      int    `json:"pgid,omitempty"`
	StartTime string `json:"startTime,omitempty"`
}

type LogPaths struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Events string `json:"events"`
}

type LogEntry struct {
	Time    time.Time `json:"time"`
	RunID   string    `json:"runId"`
	Service string    `json:"service"`
	Stream  string    `json:"stream"`
	Line    string    `json:"line"`
}

type Event struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Service  string    `json:"service,omitempty"`
	Message  string    `json:"message"`
	PID      int       `json:"pid,omitempty"`
	ExitCode *int      `json:"exitCode,omitempty"`
}

type PreviewURL struct {
	Service string `json:"service"`
	URL     string `json:"url"`
	Private bool   `json:"private"`
	Public  bool   `json:"public"`
	Healthy bool   `json:"healthy"`
}

type Hint struct {
	Code        string `json:"code"`
	Service     string `json:"service,omitempty"`
	Message     string `json:"message"`
	Severity    string `json:"severity"`
	NextCommand string `json:"nextCommand,omitempty"`
}
