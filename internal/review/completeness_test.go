package review

import (
	"strings"
	"testing"
)

// A reviewer that reviewed nothing used to exit 0: the Job succeeded, the CLI
// printed "Review completed", the run counted toward duration estimates, and on
// the hosted path the refusal would have been posted to the pull request as
// though it were a review. This is the observed output that motivated the check.
const refusal = `Commit identifiers alone do not establish what the change does or support code findings.
Please provide the complete merge-base-to-HEAD diff, applicable repository-local rule files, and surrounding source for changed functions.
Review status: incomplete. No source files reviewed; no conclusions about material findings.
BOSUN-REVIEW: incomplete`

func TestCheckReviewed(t *testing.T) {
	tests := []struct {
		name, output string
		wantErr      bool
		wantMentions string
	}{
		{
			name: "the refusal that motivated this check", output: refusal,
			wantErr: true, wantMentions: "No source files reviewed",
		},
		{
			name:   "a complete review passes",
			output: "Reviewed commit `abc` against merge base `def`.\n- **High** — `main.go:10`: boom.\nBOSUN-REVIEW: complete",
		},
		{
			name:   "a clean review with no findings passes",
			output: "Reviewed commit `abc` against merge base `def`.\nNo material findings.\nBOSUN-REVIEW: complete",
		},
		{
			name: "empty output is not a review", output: "",
			wantErr: true, wantMentions: "no output",
		},
		{
			name: "whitespace is not a review", output: "   \n\t\n",
			wantErr: true, wantMentions: "no output",
		},
		{
			// The word appears constantly in real reviews -- "the test coverage
			// is incomplete" must not fail the run.
			name:   "prose about incompleteness is not a refusal",
			output: "Reviewed commit `abc` against merge base `def`.\n- **Low** — `a.go:3`: error handling is incomplete; the review status of this path is unclear.\nBOSUN-REVIEW: complete",
		},
		{
			// Older images predate the marker, and a missing one must not fail
			// an otherwise real review.
			name:   "substantive output without a marker is accepted",
			output: "Reviewed commit `abc`.\n- **Medium** — `b.go:7`: leaks a file handle.",
		},
		{
			name:    "the marker is matched on its own line, not inside prose",
			output:  "The agent may print BOSUN-REVIEW: incomplete inside a sentence about itself.\nReviewed commit `abc`.\nBOSUN-REVIEW: complete",
			wantErr: false,
		},
		{
			name:    "case and spacing around the marker are tolerated",
			output:  "could not read the diff\n  bosun-review:  INCOMPLETE  ",
			wantErr: true, wantMentions: "could not read the diff",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := checkReviewed(tc.output)
			if !tc.wantErr {
				if e != nil {
					t.Fatalf("unexpected error: %v", e)
				}
				return
			}
			if e == nil {
				t.Fatal("expected an error")
			}
			if tc.wantMentions != "" && !strings.Contains(e.Error(), tc.wantMentions) {
				t.Errorf("error %q does not carry the reviewer's reason %q", e, tc.wantMentions)
			}
		})
	}
}

// The trailer is protocol, not review content: a reader of a pull request
// comment should never see it.
func TestWithoutMarker(t *testing.T) {
	tests := []struct {
		name, output, want string
	}{
		{
			name:   "trailing marker is removed",
			output: "Reviewed commit `abc`.\n- **Low** — `a.go:1`: nit.\nBOSUN-REVIEW: complete",
			want:   "Reviewed commit `abc`.\n- **Low** — `a.go:1`: nit.",
		},
		{
			name:   "marker with trailing blank lines leaves no gap",
			output: "Findings here.\n\nBOSUN-REVIEW: complete\n\n",
			want:   "Findings here.",
		},
		{
			name:   "prose mentioning the marker is untouched",
			output: "The agent should print BOSUN-REVIEW: complete at the end.",
			want:   "The agent should print BOSUN-REVIEW: complete at the end.",
		},
		{
			name:   "output without a marker is unchanged",
			output: "Reviewed commit `abc`.\nNo material findings.",
			want:   "Reviewed commit `abc`.\nNo material findings.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := withoutMarker(tc.output); got != tc.want {
				t.Errorf("withoutMarker() = %q, want %q", got, tc.want)
			}
		})
	}
}
