package output

import (
	"strings"
	"testing"
)

func TestWriteTableAlignsColumns(t *testing.T) {
	var b strings.Builder
	err := WriteTable(&b,
		[]string{"NAME", "HOST", "ONLINE", "TRACKED", "SSH TARGET", "IP"},
		[][]string{
			{"Developer Laptop", "Developer Laptop", "false", "false", "", "100.64.0.10"},
			{"linux-builder", "linux-builder", "true", "false", "", "100.64.0.11"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"NAME              HOST              ONLINE  TRACKED  SSH TARGET  IP\n" +
		"Developer Laptop  Developer Laptop  false   false                100.64.0.10\n" +
		"linux-builder     linux-builder     true    false                100.64.0.11\n"
	if got := b.String(); got != want {
		t.Fatalf("table mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestWriteTableCleansMultilineCells(t *testing.T) {
	var b strings.Builder
	if err := WriteTable(&b, []string{"A", "B"}, [][]string{{"one\ntwo", "x"}}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(b.String(), "\t\r") || strings.Contains(b.String(), "one\ntwo") {
		t.Fatalf("cell was not cleaned: %q", b.String())
	}
}

func TestColorHelpersUseSemanticPrefixes(t *testing.T) {
	t.Setenv("WORKYARD_COLOR", "always")
	var b strings.Builder
	OKf(&b, "setup - setup completed")
	Warningf(&b, "daemon version mismatch")
	Failedf(&b, "one or more checks failed")
	got := b.String()
	for _, want := range []string{
		"\x1b[32mok:\x1b[0m setup - setup completed\n",
		"\x1b[33mwarning:\x1b[0m daemon version mismatch\n",
		"\x1b[31mfailed:\x1b[0m one or more checks failed\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing colored line %q in %q", want, got)
		}
	}
}

func TestWriteTableColorsStatusCellsWhenEnabled(t *testing.T) {
	t.Setenv("WORKYARD_COLOR", "always")
	var b strings.Builder
	if err := WriteTable(&b, []string{"SERVICE", "STATUS", "HEALTHY"}, [][]string{{"api", "running", "true"}, {"worker", "failed", "false"}}); err != nil {
		t.Fatal(err)
	}
	got := b.String()
	for _, want := range []string{
		"\x1b[32mrunning\x1b[0m",
		"\x1b[32mtrue\x1b[0m",
		"\x1b[31mfailed\x1b[0m",
		"\x1b[31mfalse\x1b[0m",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
