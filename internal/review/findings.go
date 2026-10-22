package review

import (
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Bounds on what a provider can emit. Every field below reaches the GitHub API
// verbatim, and nothing upstream constrains a model's output, so the parser
// rather than the publisher is where a runaway review stops.
const (
	findingLimit = 50
	fieldLimit   = 4000
	titleLimit   = 250
	pathLimit    = 512
)

// severities is the ranking a consumer triages by, highest first.
var severities = map[string]int{"blocker": 0, "high": 1, "medium": 2, "low": 3}

// Finding is one anchorable claim about the change. It is the machine-readable
// half of the review; the Markdown summary carries the same findings as prose.
type Finding struct {
	// ID is stable within one review and is what a person or an agent quotes
	// when saying which finding they acted on. It is assigned here, after
	// ordering, rather than taken from the model: a model-chosen identifier
	// would not be stable and could collide.
	ID          string `json:"id,omitempty"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	EndLine     int    `json:"end_line,omitempty"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	// Confidence and Uncertainty carry the reviewer's own hedging. A finding
	// read out of code without running it is a claim, and dropping the hedge
	// would present every claim as a verdict.
	Confidence  string `json:"confidence,omitempty"`
	Uncertainty string `json:"uncertainty,omitempty"`
}

// parseFindings splits a review into its human summary and its findings.
//
// The absence of a block is deliberately not an error. A provider that ignores
// the output contract still produced a review, and failing here would trade a
// publishing improvement for a lost review. The caller falls back to
// publishing the whole output as a conversation comment.
func parseFindings(output string) (string, []Finding) {
	summary, findings, _, _ := parseReview(output)
	return summary, findings
}

// parseReview splits a review into its human summary and its findings, and
// reports whether the reviewer honoured the output contract at all.
//
// reported distinguishes the two zero-finding cases, which mean opposite
// things: a review that emitted the contract block and found nothing is a
// clean review, while one that emitted no block may have found plenty and
// said so only in prose. note carries a diagnostic for the second kind, so a
// reader is told the Markdown is authoritative rather than shown an empty
// findings list.
func parseReview(output string) (string, []Finding, bool, string) {
	start, end, inner := findingsPayload(output)
	if inner == "" {
		return output, nil, false, ""
	}
	var parsed struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		return output, nil, false, "the reviewer emitted a findings block that is not valid JSON; the Markdown report is authoritative"
	}
	if parsed.Findings == nil {
		// A JSON block that is not the contract: leave it in the summary,
		// where a human can still read whatever the model meant by it.
		return output, nil, false, ""
	}
	var findings []Finding
	dropped := 0
	for _, f := range parsed.Findings {
		if f, ok := normalize(f); ok {
			findings = append(findings, f)
		} else {
			dropped++
		}
		if len(findings) == findingLimit {
			break
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return severities[findings[i].Severity] < severities[findings[j].Severity]
	})
	for i := range findings {
		findings[i].ID = "F" + strconv.Itoa(i+1)
	}
	note := ""
	if dropped > 0 {
		note = "dropped " + strconv.Itoa(dropped) + " finding(s) that could not be anchored; the Markdown report still carries them"
	}
	summary := strings.TrimRight(output[:start], " \t\n")
	if rest := strings.TrimSpace(output[end:]); rest != "" {
		summary += "\n\n" + rest
	}
	return summary, findings, true, note
}

// Result is the structured half of a completed review, carried on the event
// stream so a caller does not have to parse prose, and so the findings survive
// in durable local state after the Job and its logs are gone.
type Result struct {
	SchemaVersion int       `json:"schema_version"`
	Findings      []Finding `json:"findings"`
	Note          string    `json:"note,omitempty"`
}

// ResultSchemaVersion is bumped when the shape above changes incompatibly.
const ResultSchemaVersion = 1

// StructuredResult renders a review's findings for the event stream. It
// returns "" when the reviewer emitted no contract block: there is nothing
// structured to report, and an empty findings list would claim a clean review.
func StructuredResult(output string) string {
	_, findings, reported, note := parseReview(output)
	if !reported {
		return ""
	}
	if findings == nil {
		findings = []Finding{}
	}
	payload, err := json.Marshal(Result{SchemaVersion: ResultSchemaVersion, Findings: findings, Note: note})
	if err != nil {
		return ""
	}
	return string(payload)
}

// normalize reports whether a finding can be published, and trims it to the
// bounds above. An entry that cannot be anchored is dropped rather than
// repaired: a guessed path or line anchors a comment to the wrong code.
func normalize(f Finding) (Finding, bool) {
	f.Path = strings.TrimSpace(f.Path)
	f.Severity = strings.ToLower(strings.TrimSpace(f.Severity))
	f.Title = truncate(strings.TrimSpace(f.Title), titleLimit)
	f.Body = truncate(strings.TrimSpace(f.Body), fieldLimit)
	f.Remediation = truncate(strings.TrimSpace(f.Remediation), fieldLimit)
	f.Confidence = truncate(strings.TrimSpace(f.Confidence), titleLimit)
	f.Uncertainty = truncate(strings.TrimSpace(f.Uncertainty), fieldLimit)
	// An ID from the model is not trusted: it is assigned after ordering.
	f.ID = ""
	if f.Path == "" || len(f.Path) > pathLimit || f.Title == "" {
		return f, false
	}
	// A path that escapes the repository is either a confused model or an
	// attempt to point a comment somewhere it does not belong.
	if strings.HasPrefix(f.Path, "/") || f.Path != path.Clean(f.Path) || strings.HasPrefix(f.Path, "../") {
		return f, false
	}
	if _, ok := severities[f.Severity]; !ok {
		return f, false
	}
	if f.Line < 1 || (f.EndLine != 0 && f.EndLine < f.Line) {
		return f, false
	}
	return f, true
}

// truncate keeps the result within limit, ellipsis included: the limit exists
// to bound what reaches GitHub, so overshooting it by the marker defeats it.
func truncate(text string, limit int) string {
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-3]) + "..."
	}
	return text
}

// lastJSONBlock locates the final fenced json block, which is where the output
// contract puts the findings. Taking the last one lets a review quote JSON in
// its prose without the quote being mistaken for the contract.
// FindingsMarker introduces the contract payload. It exists because a fenced
// block alone does not survive the trip.
//
// bridgectl providers with stream_json: true are read a line at a time, and
// any line that parses as JSON is consumed as a stream event rather than
// forwarded as output. The fences reach us; the one line between them does
// not. Measured against the shipped image: a provider printing
//
//	BEFORE / {"findings":[]} / AFTER
//
// delivers "BEFOREAFTER". Indenting does not help -- the line is trimmed
// before the check -- so the payload has to begin with something that is not
// JSON. A reviewer that found nothing therefore looked identical to one that
// ignored the contract, and every review published as a conversation comment
// because no finding could be anchored.
const FindingsMarker = "BOSUN-FINDINGS"

// findingsPayload locates the contract payload and the span to cut from the
// summary. The marker form is what providers emit today; the fenced form is
// still accepted because an adapter that does not go through the line filter
// has no reason to have changed, and an old transcript should still parse.
func findingsPayload(output string) (int, int, string) {
	if start, end, inner := markerLine(output); inner != "" {
		return start, end, inner
	}
	return lastJSONBlock(output)
}

func markerLine(output string) (int, int, string) {
	start := strings.LastIndex(output, FindingsMarker)
	if start < 0 {
		return 0, 0, ""
	}
	// Only at the start of a line, so the word appearing in prose -- or in
	// this file's own documentation, quoted back by a reviewer -- is not
	// mistaken for the payload.
	if start > 0 && output[start-1] != '\n' {
		return 0, 0, ""
	}
	rest := output[start+len(FindingsMarker):]
	end := strings.IndexByte(rest, '\n')
	if end < 0 {
		end = len(rest)
	}
	inner := strings.TrimSpace(rest[:end])
	if inner == "" {
		return 0, 0, ""
	}
	return start, start + len(FindingsMarker) + end, inner
}

func lastJSONBlock(output string) (int, int, string) {
	const fence = "```json"
	start := strings.LastIndex(output, fence)
	if start < 0 {
		return 0, 0, ""
	}
	rest := output[start+len(fence):]
	closing := strings.Index(rest, "```")
	if closing < 0 {
		return 0, 0, ""
	}
	inner := strings.TrimSpace(rest[:closing])
	if inner == "" {
		return 0, 0, ""
	}
	return start, start + len(fence) + closing + len("```"), inner
}

// humanReview renders a review for a terminal or a progress event. The
// findings block is a publishing contract; showing raw JSON to the person who
// typed `bosun review` would be a worse read than the prose it duplicates.
func humanReview(output string) string {
	summary, findings := parseFindings(output)
	if len(findings) == 0 {
		return output
	}
	text := strings.TrimRight(summary, " \t\n")
	for _, f := range findings {
		text += "\n\n" + location(f) + " — " + findingBody(f)
	}
	return text
}
