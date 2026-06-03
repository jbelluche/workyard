package worker

import (
	"strings"
	"testing"
)

func TestRedactMasksCommonSecretShapes(t *testing.T) {
	cases := []string{
		"TOKEN=abc123",
		"api_key: abc123",
		"Authorization: Bearer abc.def.ghi",
		"postgres://user:pass@example.test/db",
	}
	for _, input := range cases {
		got := redact(input)
		if got == input || containsAny(got, []string{"abc123", "abc.def.ghi", "user:pass"}) {
			t.Fatalf("redact(%q) = %q", input, got)
		}
	}
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
