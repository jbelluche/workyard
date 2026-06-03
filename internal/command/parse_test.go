package command

import "testing"

func TestParseSplitsQuotes(t *testing.T) {
	got, err := Parse(`python3 server.py --name "hello world"`, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"python3", "server.py", "--name", "hello world"}
	if len(got) != len(want) {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v want %#v", got, want)
		}
	}
}

func TestParseRejectsShellOperators(t *testing.T) {
	for _, input := range []string{"npm run dev && rm -rf x", "cat x | grep y", "echo $(whoami)", "echo hi > out"} {
		if _, err := Parse(input, false); err == nil {
			t.Fatalf("expected %q to be rejected", input)
		}
	}
}
