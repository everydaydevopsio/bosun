package localreview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/review"
)

// ResultSchemaVersion is the version of the document `review-result --json`
// emits. A consumer pins it; bump it only for an incompatible change.
const ResultSchemaVersion = 1

// revision compares what was reviewed with what the caller has now. It never
// says whether a finding was fixed: a moved line is not a resolved finding,
// and a passing test is not a review.
type revision struct {
	Ref             string `json:"ref"`
	ReviewedHeadSHA string `json:"reviewed_head_sha"`
	CurrentSHA      string `json:"current_sha,omitempty"`
	SameRevision    bool   `json:"same_revision"`
	Comparable      bool   `json:"comparable"`
	Note            string `json:"note,omitempty"`
}

type resultDocument struct {
	SchemaVersion int    `json:"schema_version"`
	Job           string `json:"job"`
	Outcome       string `json:"outcome"`
	Repo          string `json:"repo,omitempty"`
	Branch        string `json:"branch,omitempty"`
	Provider      string `json:"provider,omitempty"`
	// Revision provenance. Empty on records written before Bosun stored it;
	// reported as "unknown" rather than guessed from the report text.
	ReviewedHeadSHA string `json:"reviewed_head_sha,omitempty"`
	BaseSHA         string `json:"base_sha,omitempty"`
	MergeBaseSHA    string `json:"merge_base_sha,omitempty"`
	// CommittedOnly is false when the review also read uncommitted changes,
	// which no commit identifies.
	CommittedOnly  bool      `json:"committed_only"`
	Started        time.Time `json:"started,omitempty"`
	Finished       time.Time `json:"finished,omitempty"`
	ElapsedSeconds float64   `json:"elapsed_seconds,omitempty"`
	// FindingsReported separates "the reviewer found nothing" from "the
	// reviewer did not report findings in a form this can read". Both have an
	// empty findings list and they mean opposite things.
	FindingsReported bool                 `json:"findings_reported"`
	Findings         []review.Finding     `json:"findings"`
	Note             string               `json:"note,omitempty"`
	Failure          string               `json:"failure,omitempty"`
	Markdown         string               `json:"markdown,omitempty"`
	Revision         *revision            `json:"revision,omitempty"`
	Usage            *review.UsageSummary `json:"usage"`
	UsageNote        string               `json:"usage_note,omitempty"`
}

func document(r record) resultDocument {
	d := resultDocument{
		SchemaVersion:   ResultSchemaVersion,
		Job:             r.Job,
		Outcome:         r.Outcome,
		Repo:            r.Repo,
		Branch:          r.Branch,
		Provider:        r.Provider,
		ReviewedHeadSHA: r.ReviewedSHA,
		BaseSHA:         r.BaseSHA,
		MergeBaseSHA:    r.MergeBase,
		CommittedOnly:   r.Committed,
		Started:         r.Started,
		Finished:        r.Finished,
		Failure:         r.Failure,
		Markdown:        r.Result,
		Findings:        []review.Finding{},
	}
	if d.Outcome == "" {
		d.Outcome = "running"
	}
	if r.Usage != "" {
		var usage review.UsageSummary
		if err := json.Unmarshal([]byte(r.Usage), &usage); err == nil && usage.SchemaVersion == 1 {
			d.Usage = &usage
		} else {
			d.UsageNote = "stored usage is unreadable or uses an unsupported schema; cost remains unknown"
		}
	}
	if !r.Finished.IsZero() && !r.Started.IsZero() {
		d.ElapsedSeconds = r.Finished.Sub(r.Started).Seconds()
	}
	if r.Structured != "" {
		var parsed review.Result
		if json.Unmarshal([]byte(r.Structured), &parsed) == nil {
			d.FindingsReported = true
			d.Note = parsed.Note
			if parsed.Findings != nil {
				d.Findings = parsed.Findings
			}
		} else {
			d.Note = "the stored findings payload could not be read; the Markdown report is authoritative"
		}
	} else if r.Result != "" {
		d.Note = "this review reported no machine-readable findings; the Markdown report is authoritative"
	}
	return d
}

// compare resolves the caller's ref in the current checkout and reports how it
// relates to the reviewed commit, declining to compare rather than guessing
// when either commit is unavailable here.
func compare(ctx context.Context, dir, ref, reviewed string, committedOnly bool) *revision {
	c := &revision{Ref: ref, ReviewedHeadSHA: reviewed}
	if reviewed == "" {
		c.Note = "this review predates revision provenance; the reviewed commit is unknown"
		return c
	}
	current, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || current == "" {
		c.Note = "could not resolve " + ref + " in this checkout; comparison was not made"
		return c
	}
	c.CurrentSHA = current
	if _, err := git(ctx, dir, "cat-file", "-e", reviewed+"^{commit}"); err != nil {
		c.Note = "the reviewed commit is not available in this checkout; comparison was not made"
		return c
	}
	c.Comparable = true
	c.SameRevision = current == reviewed
	switch {
	case !c.SameRevision:
		c.Note = fmt.Sprintf("This review covers %.7s; %s is %.7s. Changes after %.7s were not reviewed.", reviewed, ref, current, reviewed)
	case !committedOnly:
		c.Note = "This review read uncommitted changes, which no commit identifies; a matching revision does not mean matching content."
	}
	return c
}

func renderResult(d resultDocument, summaryOnly bool, out io.Writer) {
	status := d.Outcome
	if d.ElapsedSeconds > 0 {
		status += fmt.Sprintf(" in %s", (time.Duration(d.ElapsedSeconds) * time.Second).String())
	}
	fmt.Fprintf(out, "Review %s | %s\n", d.Job, status)
	scope := "including working-tree changes"
	if d.CommittedOnly {
		scope = "committed content only"
	}
	fmt.Fprintf(out, "Reviewed %s at %s | base %s | merge base %s | %s\n",
		value(d.Branch), short(d.ReviewedHeadSHA), short(d.BaseSHA), short(d.MergeBaseSHA), scope)
	if d.Revision != nil && d.Revision.Note != "" {
		fmt.Fprintln(out, d.Revision.Note)
	}
	switch {
	case !d.FindingsReported:
		fmt.Fprintln(out, "Findings: not reported in machine-readable form")
	case len(d.Findings) == 0:
		fmt.Fprintln(out, "Findings: none")
	default:
		fmt.Fprintf(out, "Findings: %s\n", tally(d.Findings))
	}
	if d.Note != "" {
		fmt.Fprintln(out, d.Note)
	}
	if d.Failure != "" {
		fmt.Fprintln(out, "Failure:", d.Failure)
	}
	for _, f := range d.Findings {
		fmt.Fprintf(out, "\n%s  %-8s %s  %s\n", f.ID, f.Severity, location(f), f.Title)
		if summaryOnly {
			continue
		}
		for _, line := range []string{f.Body, remediation(f), confidence(f)} {
			if line != "" {
				fmt.Fprintln(out, indent(line))
			}
		}
	}
	if !summaryOnly && d.Markdown != "" {
		fmt.Fprintf(out, "\n%s\n", d.Markdown)
	}
}

func tally(findings []review.Finding) string {
	byName := map[string]int{}
	for _, f := range findings {
		byName[f.Severity]++
	}
	var parts []string
	for _, severity := range []string{"blocker", "high", "medium", "low"} {
		if n := byName[severity]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, severity))
		}
	}
	return fmt.Sprintf("%s (%d total)", strings.Join(parts, ", "), len(findings))
}

func location(f review.Finding) string {
	at := fmt.Sprintf("%s:%d", f.Path, f.Line)
	if f.EndLine > f.Line {
		at = fmt.Sprintf("%s-%d", at, f.EndLine)
	}
	return at
}

func remediation(f review.Finding) string {
	if f.Remediation == "" {
		return ""
	}
	return "Remediation: " + f.Remediation
}

func confidence(f review.Finding) string {
	switch {
	case f.Confidence == "" && f.Uncertainty == "":
		return ""
	case f.Uncertainty == "":
		return "Confidence: " + f.Confidence
	case f.Confidence == "":
		return "Unconfirmed: " + f.Uncertainty
	}
	return "Confidence: " + f.Confidence + " — unconfirmed: " + f.Uncertainty
}

func indent(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = "    " + line
	}
	return strings.Join(lines, "\n")
}

func short(sha string) string {
	if sha == "" {
		return "unknown"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func value(text string) string {
	if text == "" {
		return "unknown"
	}
	return text
}

func runResult(ctx context.Context, job string, o options, out, errout io.Writer) int {
	r, e := load(job)
	if e != nil {
		fmt.Fprintln(errout, "Load review metadata:", e)
		return 1
	}
	d := document(r)
	if o.againstRef != "" {
		d.Revision = compare(ctx, ".", o.againstRef, r.ReviewedSHA, r.Committed)
	}
	if o.json {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(d); err != nil {
			fmt.Fprintln(errout, err)
			return 1
		}
		return 0
	}
	renderResult(d, o.summary, out)
	return 0
}
