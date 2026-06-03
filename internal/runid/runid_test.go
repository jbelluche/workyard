package runid

import "testing"

func TestValidateRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../x", `x\y`, "bad id", "semi;colon"} {
		if _, err := Validate(id); err == nil {
			t.Fatalf("expected %q to be rejected", id)
		}
	}
}

func TestValidateAllowsPathSafeIDs(t *testing.T) {
	got, err := Validate("feature.search_1-a")
	if err != nil {
		t.Fatal(err)
	}
	if got != "feature.search_1-a" {
		t.Fatalf("got %q", got)
	}
}
