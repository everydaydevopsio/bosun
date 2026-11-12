package localreview

import (
	"bytes"
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// #95, Bosun review F2: an unknown --effort is refused before anything is
// snapshotted or recorded, not after.
func TestUnknownEffortIsRefusedBeforeAnySnapshot(t *testing.T) {
	state := t.TempDir()
	t.Setenv("BOSUN_STATE_DIR", state)
	var out, errout bytes.Buffer
	code := Run(context.Background(), "review", []string{"--effort", "max", t.TempDir()}, &out, &errout)
	if code != 2 || !strings.Contains(errout.String(), "invalid review effort") {
		t.Fatalf("exit %d, stderr %q", code, errout.String())
	}
	saved := 0
	_ = filepath.WalkDir(state, func(_ string, d fs.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			saved++
		}
		return nil
	})
	if saved != 0 {
		t.Fatalf("%d state file(s) written for a refused review", saved)
	}
}
