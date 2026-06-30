package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type Role int

const (
	RoleNeutral Role = iota
	RoleSuccess
	RoleWarning
	RoleError
	RoleHint
	RoleInfo
)

const ansiReset = "\x1b[0m"

func ColorEnabled(w io.Writer) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WORKYARD_COLOR"))) {
	case "always", "1", "true", "on", "yes":
		return true
	case "never", "0", "false", "off", "no":
		return false
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if force := firstNonEmptyEnv("FORCE_COLOR", "CLICOLOR_FORCE"); force != "" && force != "0" {
		return true
	}
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func Styled(w io.Writer, role Role, text string) string {
	if role == RoleNeutral || text == "" || !ColorEnabled(w) {
		return text
	}
	code := ansiCode(role)
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + ansiReset
}

func StatusWord(w io.Writer, role Role, word string) string {
	return Styled(w, role, word)
}

func OKf(w io.Writer, format string, args ...any) {
	writePrefixedLine(w, RoleSuccess, "ok:", format, args...)
}

func Warningf(w io.Writer, format string, args ...any) {
	writePrefixedLine(w, RoleWarning, "warning:", format, args...)
}

func Failedf(w io.Writer, format string, args ...any) {
	writePrefixedLine(w, RoleError, "failed:", format, args...)
}

func Successf(w io.Writer, format string, args ...any) {
	writeStyledLine(w, RoleSuccess, format, args...)
}

func Infof(w io.Writer, format string, args ...any) {
	writeStyledLine(w, RoleInfo, format, args...)
}

func ColorizeTableCell(w io.Writer, header, value string) string {
	role := tableCellRole(header, value)
	if role == RoleNeutral {
		return value
	}
	return Styled(w, role, value)
}

func writePrefixedLine(w io.Writer, role Role, prefix, format string, args ...any) {
	message := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	if strings.TrimSpace(message) == "" {
		_, _ = fmt.Fprintf(w, "%s\n", Styled(w, role, prefix))
		return
	}
	_, _ = fmt.Fprintf(w, "%s %s\n", Styled(w, role, prefix), message)
}

func writeStyledLine(w io.Writer, role Role, format string, args ...any) {
	message := strings.TrimRight(fmt.Sprintf(format, args...), "\n")
	if message == "" {
		_, _ = fmt.Fprintln(w)
		return
	}
	_, _ = fmt.Fprintf(w, "%s\n", styleFirstToken(w, role, message))
}

func styleFirstToken(w io.Writer, role Role, message string) string {
	if !ColorEnabled(w) {
		return message
	}
	idx := strings.IndexFunc(message, func(r rune) bool { return r == ' ' || r == '\t' })
	if idx < 0 {
		return Styled(w, role, message)
	}
	return Styled(w, role, message[:idx]) + message[idx:]
}

func tableCellRole(header, value string) Role {
	header = strings.ToUpper(strings.TrimSpace(header))
	value = strings.TrimSpace(value)
	normalized := strings.ToLower(strings.Trim(value, " []():,"))
	switch header {
	case "STATUS", "STATE":
		return statusRole(normalized)
	case "HEALTHY", "ONLINE", "OK":
		return boolRole(normalized, RoleError)
	case "ENABLED", "ATTACHED", "TRACKED":
		return boolRole(normalized, RoleWarning)
	case "LAST ERROR":
		if normalized != "" && normalized != "-" {
			return RoleError
		}
	}
	return RoleNeutral
}

func statusRole(value string) Role {
	switch value {
	case "pass", "ok", "true", "yes", "healthy", "running", "ready", "reachable", "online", "installed", "configured", "synced", "started", "killed":
		return RoleSuccess
	case "warn", "warning", "skip", "skipped", "plan", "pending", "starting", "stopping", "stopped", "disabled", "missing":
		return RoleWarning
	case "fail", "failed", "error", "false", "no", "unhealthy", "invalid", "timeout", "timed out", "unreachable", "refused", "crashed", "exited", "dead":
		return RoleError
	default:
		return RoleNeutral
	}
}

func boolRole(value string, falseRole Role) Role {
	switch value {
	case "true", "yes", "online":
		return RoleSuccess
	case "false", "no", "offline":
		return falseRole
	default:
		return statusRole(value)
	}
}

func ansiCode(role Role) string {
	switch role {
	case RoleSuccess:
		return "32"
	case RoleWarning:
		return "33"
	case RoleError:
		return "31"
	case RoleHint:
		return "36"
	case RoleInfo:
		return "36"
	default:
		return ""
	}
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}
