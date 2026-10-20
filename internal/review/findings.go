package review

import (
	"encoding/json"
	"path"
	"sort"
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
	Path        string `json:"path"`
	Line        int    `json:"line"`
	EndLine     int    `json:"end_line,omitempty"`
	Severity    string `json:"severity"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

// parseFindings splits a review into its human summary and its findings.
//
// The absence of a block is deliberately not an error. A provider that ignores
// the output contract still produced a review, and failing here would trade a
// publishing improvement for a lost review. The caller falls back to
// publishing the whole output as a conversation comment.
func parseFindings(output string) (string, []Finding) {
	start, end, inner := lastJSONBlock(output)
	if inner == "" {
		return output, nil
	}
	var parsed struct {
		Findings []Finding `json:"findings"`
	}
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil || parsed.Findings == nil {
		// Unparsable, or a JSON block that is not the contract: leave it in the
		// summary, where a human can still read whatever the model meant by it.
		return output, nil
	}
	var findings []Finding
	for _, f := range parsed.Findings {
		if f, ok := normalize(f); ok {
			findings = append(findings, f)
		}
		if len(findings) == findingLimit {
			break
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return severities[findings[i].Severity] < severities[findings[j].Severity]
	})
	summary := strings.TrimRight(output[:start], " \t\n")
	if rest := strings.TrimSpace(output[end:]); rest != "" {
		summary += "\n\n" + rest
	}
	return summary, findings
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
