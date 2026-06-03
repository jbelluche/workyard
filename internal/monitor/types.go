package monitor

import (
	"time"

	"github.com/jackbelluche/workyard/internal/worker"
)

type State struct {
	OK              bool              `json:"ok"`
	Version         string            `json:"version"`
	GeneratedAt     time.Time         `json:"generatedAt"`
	RefreshInterval string            `json:"refreshInterval"`
	RegistryError   string            `json:"registryError,omitempty"`
	Workers         []WorkerSnapshot  `json:"workers"`
	Runs            []RunSnapshot     `json:"runs"`
	Services        []ServiceSnapshot `json:"services"`
	Events          []EventSnapshot   `json:"events"`
}

type WorkerSnapshot struct {
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	Connected     bool      `json:"connected"`
	RunCount      int       `json:"runCount"`
	ServiceCount  int       `json:"serviceCount"`
	HealthyCount  int       `json:"healthyCount"`
	RunningCount  int       `json:"runningCount"`
	LastHeartbeat time.Time `json:"lastHeartbeat,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
}

type RunSnapshot struct {
	Key               string                `json:"key"`
	Worker            string                `json:"worker"`
	Project           string                `json:"project"`
	RunID             string                `json:"runId"`
	Status            string                `json:"status"`
	Connected         bool                  `json:"connected"`
	LastHeartbeat     time.Time             `json:"lastHeartbeat,omitempty"`
	LastError         string                `json:"lastError,omitempty"`
	RemoteRunPath     string                `json:"remoteRunPath,omitempty"`
	RemoteSourcePath  string                `json:"remoteSourcePath,omitempty"`
	LocalRoot         string                `json:"localRoot,omitempty"`
	RegisteredAt      time.Time             `json:"registeredAt,omitempty"`
	UpdatedAt         time.Time             `json:"updatedAt,omitempty"`
	Services          []worker.ServiceState `json:"services"`
	Hints             []worker.Hint         `json:"hints,omitempty"`
	RecentEvents      []worker.Event        `json:"recentEvents,omitempty"`
	ServiceCount      int                   `json:"serviceCount"`
	HealthyCount      int                   `json:"healthyCount"`
	RunningCount      int                   `json:"runningCount"`
	LastServiceUpdate time.Time             `json:"lastServiceUpdate,omitempty"`
}

type ServiceSnapshot struct {
	Worker       string    `json:"worker"`
	Project      string    `json:"project"`
	RunID        string    `json:"runId"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Healthy      bool      `json:"healthy"`
	PID          int       `json:"pid,omitempty"`
	Port         int       `json:"port,omitempty"`
	URL          string    `json:"url,omitempty"`
	StartCommand string    `json:"startCommand,omitempty"`
	UpdatedAt    time.Time `json:"updatedAt,omitempty"`
}

type EventSnapshot struct {
	Worker  string    `json:"worker"`
	Project string    `json:"project"`
	RunID   string    `json:"runId"`
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	Service string    `json:"service,omitempty"`
	Message string    `json:"message"`
}

type URLSnapshot struct {
	Worker  string `json:"worker"`
	Project string `json:"project"`
	RunID   string `json:"runId"`
	Service string `json:"service"`
	URL     string `json:"url"`
	Healthy bool   `json:"healthy"`
	Private bool   `json:"private"`
	Public  bool   `json:"public"`
}

type runData struct {
	Response worker.Response
	Events   []worker.Event
}
