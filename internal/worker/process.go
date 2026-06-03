package worker

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func currentProcessID(pid int) ProcessID {
	if pid <= 0 {
		return ProcessID{}
	}
	pgid, _ := syscall.Getpgid(pid)
	return ProcessID{PID: pid, PGID: pgid, StartTime: linuxProcessStartTime(pid)}
}

func processIdentityMatches(expected ProcessID) bool {
	if expected.PID <= 0 {
		return false
	}
	actual := currentProcessID(expected.PID)
	if actual.PID != expected.PID {
		return false
	}
	if expected.PGID > 0 && actual.PGID != expected.PGID {
		return false
	}
	if expected.StartTime != "" && actual.StartTime != expected.StartTime {
		return false
	}
	if expected.StartTime == "" && runtime.GOOS == "linux" {
		return false
	}
	return processAlive(expected.PID)
}

func linuxProcessStartTime(pid int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	raw := string(data)
	end := strings.LastIndex(raw, ")")
	if end < 0 || end+2 >= len(raw) {
		return ""
	}
	fields := strings.Fields(raw[end+2:])
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
