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
			{"Jack’s MacBook Pro", "Jack’s MacBook Pro", "false", "false", "", "100.82.63.64"},
			{"jack-r5-16gb", "jack-r5-16gb", "true", "false", "", "100.84.97.112"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "" +
		"NAME                HOST                ONLINE  TRACKED  SSH TARGET  IP\n" +
		"Jack’s MacBook Pro  Jack’s MacBook Pro  false   false                100.82.63.64\n" +
		"jack-r5-16gb        jack-r5-16gb        true    false                100.84.97.112\n"
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
