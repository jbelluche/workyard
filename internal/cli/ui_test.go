package cli

import "testing"

func TestUICommandIsPrimaryAndServerAliasStillResolves(t *testing.T) {
	root := newRoot(&options{})
	cmd, _, err := root.Find([]string{"ui"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || cmd.Use != "ui" {
		t.Fatalf("ui command=%v", cmd)
	}
	alias, _, err := root.Find([]string{"server"})
	if err != nil {
		t.Fatal(err)
	}
	if alias != cmd {
		t.Fatalf("server alias resolved to %v, want ui command", alias)
	}
}
