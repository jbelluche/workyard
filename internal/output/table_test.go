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
