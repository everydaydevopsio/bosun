package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A user who sees a failed review needs to know whose failure it was: their
// repository or configuration, Bosun's plumbing, the AI provider, or the
// reviewer declining to review. All four used to render identically.
func TestDescribe(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		wantMentions string
		wantCode     int
	}{
		{
			name: "configuration", err: fail(StageSetup, errors.New("BOSUN_REPO and BOSUN_REF are required")),
			wantMentions: "could not prepare", wantCode: 10,
		},
		{
			name: "clone", err: fail(StageClone, errors.New("authentication failed")),
			wantMentions: "could not obtain the repository", wantCode: 11,
		},
		{
			name: "provider", err: fail(StageProvider, errors.New("session exited 1")),
			wantMentions: "provider failed", wantCode: 12,
		},
		{
			name: "reviewer declined", err: fail(StageReview, errors.New("no source files reviewed")),
			wantMentions: "did not review", wantCode: 13,
		},
		{
			name: "publish", err: fail(StagePublish, errors.New("403 from GitHub")),
			wantMentions: "could not be published", wantCode: 14,
		},
		{
			// Anything not yet classified must still reach the user unchanged
			// rather than being swallowed by a default category.
			name: "unclassified", err: errors.New("something else went wrong"),
			wantMentions: "something else went wrong", wantCode: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Describe(tc.err)
			if !strings.Contains(got, tc.wantMentions) {
				t.Errorf("Describe() = %q, want it to mention %q", got, tc.wantMentions)
			}
			// The underlying reason must survive: the category alone is not
			// actionable.
			if !strings.Contains(got, tc.err.Error()) && !strings.Contains(got, errors.Unwrap(tc.err).Error()) {
				t.Errorf("Describe() = %q, loses the underlying reason", got)
			}
			if code := ExitCode(tc.err); code != tc.wantCode {
				t.Errorf("ExitCode() = %d, want %d", code, tc.wantCode)
			}
		})
	}
	if code := ExitCode(nil); code != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", code)
	}
}

// The worker reports a timeout and a cancellation as their own event types.
// Classifying an error must not hide the sentinel underneath, or a timed-out
// review would be reported as a plain failure.
func TestFailurePreservesSentinels(t *testing.T) {
	for _, sentinel := range []error{context.DeadlineExceeded, context.Canceled} {
		wrapped := fail(StageProvider, fmt.Errorf("agent: %w", sentinel))
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is lost %v through a Failure", sentinel)
		}
	}
	var f *Failure
	if !errors.As(fail(StageClone, errors.New("x")), &f) || f.Stage != StageClone {
		t.Error("errors.As did not recover the stage")
	}
}
