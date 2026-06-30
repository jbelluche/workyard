package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

type Envelope struct {
	OK    bool   `json:"ok"`
	Error *Error `json:"error,omitempty"`
}

type CommandError struct {
	Code     string
	Message  string
	Hint     string
	ExitCode int
}

func (e *CommandError) Error() string {
	return e.Message
}

// Exit code classes, applied by error code. Documented in docs/errors.md.
const (
	ExitGeneric      = 1 // unclassified failures
	ExitUsage        = 2 // bad arguments, invalid or missing configuration
	ExitConnectivity = 3 // SSH or Tailscale failures
	ExitDaemon       = 4 // worker daemon transport or daemon-reported failures
	ExitWait         = 5 // health or status wait timeouts
)

// ExitCodeFor classifies an error code into an exit code class so scripts
// and agents can branch on the kind of failure.
func ExitCodeFor(code string) int {
	switch code {
	case "WORKER_REQUIRED", "SERVICE_UNKNOWN", "SERVICE_SELECTION_FAILED", "CONFIG_EXISTS", "PRUNE_AGE_REQUIRED", "RUN_AMBIGUOUS":
		return ExitUsage
	case "SSH_FAILED", "REMOTE_COMMAND_FAILED", "WORKER_PLATFORM_FAILED", "TAILSCALE_DISCOVER_FAILED":
		return ExitConnectivity
	case "DAEMON_UNREACHABLE", "DAEMON_START_FAILED", "DAEMON_STOP_FAILED", "DAEMONCTL_FAILED", "DAEMON_FAILED":
		return ExitDaemon
	case "WAIT_TIMEOUT", "WAIT_FAILED":
		return ExitWait
	}
	if strings.HasPrefix(code, "CONFIG_") || strings.HasPrefix(code, "DEPLOY_ARGS") || strings.HasSuffix(code, "_INVALID") {
		return ExitUsage
	}
	return ExitGeneric
}

func NewError(code, message, hint string) *CommandError {
	return &CommandError{Code: code, Message: message, Hint: hint, ExitCode: ExitCodeFor(code)}
}

func NewExitError(code, message, hint string, exitCode int) *CommandError {
	return &CommandError{Code: code, Message: message, Hint: hint, ExitCode: exitCode}
}

func WriteJSON(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func WriteJSONLine(w io.Writer, value any) error {
	return json.NewEncoder(w).Encode(value)
}

func WriteErrorJSON(w io.Writer, err error) error {
	ce := AsCommandError(err)
	return WriteJSON(w, Envelope{
		OK: false,
		Error: &Error{
			Code:    ce.Code,
			Message: ce.Message,
			Hint:    ce.Hint,
		},
	})
}

func AsCommandError(err error) *CommandError {
	if err == nil {
		return nil
	}
	if ce, ok := err.(*CommandError); ok {
		return ce
	}
	return &CommandError{Code: "WORKYARD_ERROR", Message: err.Error(), ExitCode: 1}
}

func HumanError(w io.Writer, err error) {
	ce := AsCommandError(err)
	if ce.Hint != "" {
		_, _ = fmt.Fprintf(w, "%s %s\n%s %s\n", Styled(w, RoleError, "error:"), ce.Message, Styled(w, RoleHint, "hint:"), ce.Hint)
		return
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", Styled(w, RoleError, "error:"), ce.Message)
}
