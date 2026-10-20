package localreview

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const reviewedSHA = "0ac3791a8ca79fdf46d245e4e7fdd7312a9d19b8"

func storeRecord(t *testing.T, r record) {
	t.Helper()
	t.Setenv("BOSUN_STATE_DIR", t.TempDir())
	if err := save(&r); err != nil {
		t.Fatal(err)
	}
}

func completed(structured string) record {
	started := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	return record{
		Job: "review-bosun-feature-abc", Repo: "/src/bosun", Provider: "codex-bosun",
		Branch: "feature", ReviewedSHA: reviewedSHA, BaseSHA: "74a168b36604a6d9a3f6e03210f9892b5bd875f0",
		MergeBase: "74a168b36604a6d9a3f6e03210f9892b5bd875f0", Committed: true,
		Started: started, Finished: started.Add(67 * time.Second),
		Outcome: "completed", Result: "The full Markdown report.", Structured: structured,
	}
}

func runResultFor(t *testing.T, job string, o options) (string, string, int) {
	t.Helper()
	var out, errout bytes.Buffer
	code := runResult(context.Background(), job, o, &out, &errout)
	return out.String(), errout.String(), code
}

const oneFinding = `{"schema_version":1,"findings":[{"id":"F1","path":"src/cli.ts","line":264,"end_line":275,"severity":"low","title":"Validation errors become misleading operational failures","body":"Impact.","remediation":"Separate the paths.","confidence":"high","uncertainty":"Runtime behavior not confirmed."}]}`

// An agent triaging findings should not have to parse prose to do it.
func TestResultJSONCarriesProvenanceAndFindings(t *testing.T) {
	storeRecord(t, completed(oneFinding))
	out, _, code := runResultFor(t, "review-bosun-feature-abc", options{json: true})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var d resultDocument
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if d.SchemaVersion != ResultSchemaVersion || d.Outcome != "completed" {
		t.Fatalf("header wrong: %+v", d)
	}
	if d.ReviewedHeadSHA != reviewedSHA || d.MergeBaseSHA == "" {
		t.Fatalf("provenance missing: %+v", d)
	}
	if d.ElapsedSeconds != 67 {
		t.Fatalf("elapsed = %v", d.ElapsedSeconds)
	}
	if !d.FindingsReported || len(d.Findings) != 1 {
		t.Fatalf("findings = %+v", d.Findings)
	}
	f := d.Findings[0]
	if f.ID != "F1" || f.Path != "src/cli.ts" || f.Line != 264 || f.EndLine != 275 || f.Confidence != "high" || f.Uncertainty == "" {
		t.Fatalf("finding incomplete: %+v", f)
	}
	if d.Markdown != "The full Markdown report." {
		t.Fatalf("original report not retained: %q", d.Markdown)
	}
}

// "Found nothing" and "did not report findings in a form I can read" have the
// same empty list and mean opposite things.
func TestNoFindingsIsDistinctFromNoStructuredOutput(t *testing.T) {
	t.Run("clean review", func(t *testing.T) {
		storeRecord(t, completed(`{"schema_version":1,"findings":[]}`))
		out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{})
		if !strings.Contains(out, "Findings: none") {
			t.Fatalf("clean review not stated: %q", out)
		}
	})
	t.Run("unstructured review", func(t *testing.T) {
		storeRecord(t, completed(""))
		out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{})
		if !strings.Contains(out, "not reported in machine-readable form") {
			t.Fatalf("unstructured review not stated: %q", out)
		}
		if !strings.Contains(out, "Markdown report is authoritative") {
			t.Fatalf("no fallback diagnostic: %q", out)
		}
		if !strings.Contains(out, "The full Markdown report.") {
			t.Fatalf("report lost: %q", out)
		}
	})
	t.Run("unreadable payload", func(t *testing.T) {
		storeRecord(t, completed("{not json"))
		out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{json: true})
		var d resultDocument
		if err := json.Unmarshal([]byte(out), &d); err != nil {
			t.Fatal(err)
		}
		if d.FindingsReported || !strings.Contains(d.Note, "could not be read") {
			t.Fatalf("parse failure not recorded: %+v", d)
		}
		if d.Markdown == "" {
			t.Fatal("fell back to nothing instead of the Markdown report")
		}
	})
}

// A failed review has no findings, and must not read as a clean one.
func TestFailedReviewIsNotAnEmptyFindingsList(t *testing.T) {
	r := completed("")
	r.Outcome, r.Result, r.Failure = "failed", "", "provider: context deadline exceeded"
	storeRecord(t, r)
	out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{})
	if strings.Contains(out, "Findings: none") {
		t.Fatalf("failure presented as a clean review: %q", out)
	}
	if !strings.Contains(out, "provider: context deadline exceeded") {
		t.Fatalf("failure reason missing: %q", out)
	}
}

// A review of a commit that is no longer HEAD is still useful, but saying so
// is the caller's protection against treating it as current.
func TestAgainstRefWarnsWhenTheBranchHasMoved(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	write(t, dir+"/file", "one")
	gitTest(t, dir, "add", ".")
	gitTest(t, dir, "commit", "-m", "one")
	first := gitTest(t, dir, "rev-parse", "HEAD")
	write(t, dir+"/file", "two")
	gitTest(t, dir, "commit", "-am", "two")
	second := gitTest(t, dir, "rev-parse", "HEAD")

	t.Run("moved", func(t *testing.T) {
		c := compare(context.Background(), dir, "HEAD", first, true)
		if !c.Comparable || c.SameRevision {
			t.Fatalf("comparison = %+v", c)
		}
		if !strings.Contains(c.Note, first[:7]) || !strings.Contains(c.Note, second[:7]) {
			t.Fatalf("note names neither commit: %q", c.Note)
		}
		if strings.Contains(strings.ToLower(c.Note), "fixed") || strings.Contains(strings.ToLower(c.Note), "resolved") {
			t.Fatalf("note claims findings were addressed: %q", c.Note)
		}
	})
	t.Run("same", func(t *testing.T) {
		c := compare(context.Background(), dir, "HEAD", second, true)
		if !c.Comparable || !c.SameRevision || c.Note != "" {
			t.Fatalf("comparison = %+v", c)
		}
	})
	t.Run("same revision but the review read uncommitted changes", func(t *testing.T) {
		c := compare(context.Background(), dir, "HEAD", second, false)
		if !c.SameRevision || !strings.Contains(c.Note, "uncommitted") {
			t.Fatalf("dirty review not flagged: %+v", c)
		}
	})
	t.Run("reviewed commit unavailable here", func(t *testing.T) {
		c := compare(context.Background(), dir, "HEAD", reviewedSHA, true)
		if c.Comparable || !strings.Contains(c.Note, "not available") {
			t.Fatalf("guessed instead of declining: %+v", c)
		}
	})
	t.Run("ref unresolvable", func(t *testing.T) {
		c := compare(context.Background(), dir, "no-such-ref", second, true)
		if c.Comparable || !strings.Contains(c.Note, "could not resolve") {
			t.Fatalf("guessed instead of declining: %+v", c)
		}
	})
	t.Run("record predates provenance", func(t *testing.T) {
		c := compare(context.Background(), dir, "HEAD", "", true)
		if c.Comparable || !strings.Contains(c.Note, "unknown") {
			t.Fatalf("invented provenance: %+v", c)
		}
	})
}

// The point of the header is a fast read; the evidence must survive it.
func TestHumanOutputLeadsWithASummaryAndKeepsTheReport(t *testing.T) {
	storeRecord(t, completed(oneFinding))
	out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{})
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.Contains(first, "review-bosun-feature-abc") || !strings.Contains(first, "completed") {
		t.Fatalf("header line = %q", first)
	}
	for _, want := range []string{"1 low (1 total)", "F1", "src/cli.ts:264-275", "Remediation: Separate the paths.", "Confidence: high", "The full Markdown report."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lost %q:\n%s", want, out)
		}
	}
	summary, _, _ := runResultFor(t, "review-bosun-feature-abc", options{summary: true})
	if !strings.Contains(summary, "F1") || strings.Contains(summary, "The full Markdown report.") {
		t.Fatalf("--summary should keep titles and drop the report: %q", summary)
	}
}

// Older records carry no provenance. Report that, rather than parsing a SHA
// out of the report text and presenting it as verified.
func TestRecordsWithoutProvenanceReportUnknown(t *testing.T) {
	r := completed(oneFinding)
	r.Branch, r.ReviewedSHA, r.BaseSHA, r.MergeBase = "", "", "", ""
	storeRecord(t, r)
	out, _, _ := runResultFor(t, "review-bosun-feature-abc", options{})
	if strings.Count(out, "unknown") < 2 {
		t.Fatalf("missing provenance not reported: %q", out)
	}
	if strings.Contains(out, reviewedSHA) {
		t.Fatalf("invented a reviewed commit: %q", out)
	}
}
