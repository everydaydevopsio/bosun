package version

import "testing"

// An unstamped build must still report something, so a locally built binary is
// distinguishable from a release one rather than printing an empty string.
func TestVersionDefaultsToDev(t *testing.T) {
	if Version != "dev" {
		t.Fatalf("default Version = %q, want %q", Version, "dev")
	}
}
