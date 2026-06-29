package worker

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

type portRange struct {
	Start int
	End   int
}

func parsePortRange(raw string) (portRange, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "3100-3999"
	}
	parts := strings.Split(raw, "-")
	if len(parts) != 2 {
		return portRange{}, fmt.Errorf("port range must look like 3100-3999")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return portRange{}, err
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return portRange{}, err
	}
	if start < 1 || end > 65535 || start > end {
		return portRange{}, fmt.Errorf("invalid port range %q", raw)
	}
	return portRange{Start: start, End: end}, nil
}

func allocatePort(st RunState, service string, configured int, rng portRange) (int, error) {
	if existing, ok := st.Services[service]; ok && existing.AssignedPort > 0 && portAvailable(existing.AssignedPort) {
		return existing.AssignedPort, nil
	}
	used := map[int]bool{}
	for name, svc := range st.Services {
		if name != service && reservesPort(svc.Status) && svc.AssignedPort > 0 {
			used[svc.AssignedPort] = true
		}
	}
	if configured >= rng.Start && configured <= rng.End && !used[configured] && portAvailable(configured) {
		return configured, nil
	}
	for port := rng.Start; port <= rng.End; port++ {
		if used[port] || !portAvailable(port) {
			continue
		}
		return port, nil
	}
	return 0, fmt.Errorf("no free port in range %d-%d", rng.Start, rng.End)
}

func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func isRunningStatus(status string) bool {
	return status == "starting" || status == "running" || status == "healthy" || status == "unhealthy"
}

func reservesPort(status string) bool {
	return status == "preparing" || isRunningStatus(status)
}
