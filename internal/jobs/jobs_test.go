package jobs

import "testing"

func TestNameIsStableSafeAndUnique(t *testing.T) {
	a := Name("Acme/Widget", "Feature/Some_Branch", "delivery")
	b := Name("Acme/Widget", "Feature/Some_Branch", "delivery")
	c := Name("Acme/Widget", "Feature/Some_Branch", "other")
	if a != b || a == c || len(a) > MaxName {
		t.Fatalf("bad names %q %q %q", a, b, c)
	}
}
