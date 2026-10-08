package localreview

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// A reconnect to a finished run used to say "Stored review result" whether the
// review succeeded or Bosun fell over, so the reason a run failed survived only
// in the terminal scrollback of whoever started it.
func TestStoredFailureReportsWhyItFailed(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute)
	r := record{
		Job: "review-x", Outcome: "failed", Started: started, Finished: time.Now(),
		Failure: "Bosun could not obtain the repository: authentication failed",
	}
	var out, errout bytes.Buffer
	code := monitor(context.Background(), nil, &r, options{}, &out, &errout)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	combined := out.String() + errout.String()
	if !strings.Contains(combined, "could not obtain the repository") {
		t.Errorf("stored failure does not report the reason: %q", combined)
	}
	if strings.Contains(combined, "Stored review result") {
		t.Errorf("a failed run should not report a stored result: %q", combined)
	}
}

// A completed run keeps the old wording; only failures gained a reason.
func TestStoredSuccessIsUnchanged(t *testing.T) {
	r := record{Job: "review-y", Outcome: "completed", Started: time.Now().Add(-time.Minute), Finished: time.Now(), Result: "No material findings."}
	var out, errout bytes.Buffer
	if code := monitor(context.Background(), nil, &r, options{}, &out, &errout); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	combined := out.String() + errout.String()
	for _, want := range []string{"No material findings.", "Stored review result"} {
		if !strings.Contains(combined, want) {
			t.Errorf("output %q is missing %q", combined, want)
		}
	}
}
