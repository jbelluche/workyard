package runid

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const MaxLength = 80

var allowed = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func Validate(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", err("run id is required")
	}
	if len(id) > MaxLength {
		return "", err("run id is too long")
	}
	if id == "." || id == ".." {
		return "", err("run id cannot be . or ..")
	}
	if strings.ContainsAny(id, `/\`+"\x00") {
		return "", err("run id cannot contain slashes or null bytes")
	}
	if !allowed.MatchString(id) {
		return "", err("run id may only contain letters, numbers, dots, underscores, and dashes")
	}
	return id, nil
}

func ProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", err("project name is required")
	}
	safe := sanitize(name)
	if safe == "" || safe == "." || safe == ".." {
		return "", err("project name does not contain any safe path characters")
	}
	if len(safe) > MaxLength {
		safe = safe[:MaxLength]
		safe = strings.Trim(safe, ".-_")
	}
	return safe, nil
}

func Default(projectRoot string) string {
	branch := gitBranch(projectRoot)
	if branch == "" || branch == "HEAD" {
		branch = filepath.Base(projectRoot)
	}
	safe := sanitize(branch)
	if safe == "" {
		safe = "run"
	}
	if len(safe) > 56 {
		safe = safe[:56]
		safe = strings.Trim(safe, ".-_")
	}
	return safe
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if ok {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), ".-_")
}

func gitBranch(root string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

type validationError string

func (e validationError) Error() string { return string(e) }

func err(message string) error { return validationError(message) }
