package review

import (
	"fmt"
	"strings"
)

// The trailer the prompt requires. A reviewer that cannot review -- no diff, an
// unusable checkout, changed files it could not read -- says so here.
const (
	markerPrefix     = "bosun-review:"
	markerComplete   = "complete"
	markerIncomplete = "incomplete"
)

// checkReviewed reports whether output is a review at all.
//
// Without this, a reviewer that reviewed nothing succeeds: the Job exits 0, the
// CLI prints "Review completed", the run counts toward duration estimates, and
// on the hosted path the refusal is posted to the pull request as though it
// were a review. Silence that looks like success is worse than a failure,
// because nobody goes looking for it.
//
// Detection is deliberately narrow. The marker is matched only as a whole line,
// never as prose, because real reviews discuss incompleteness constantly -- a
// finding about partial error handling must not fail the run. Output with no
// marker at all is accepted: images built before the marker existed still
// produce real reviews, and failing them would trade a silent non-review for a
// silent non-review plus an outage.
func checkReviewed(output string) error {
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("the reviewer produced no output; nothing was reviewed")
	}
	for _, line := range strings.Split(output, "\n") {
		rest, ok := markerValue(line)
		if !ok {
			continue
		}
		switch rest {
		case markerIncomplete:
			return fmt.Errorf("the reviewer reported that it could not complete the review: %s", reason(output))
		case markerComplete:
			return nil
		}
	}
	return nil
}

// markerValue reports the status a line carries, if the line is the trailer.
func markerValue(line string) (string, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(line))
	if !strings.HasPrefix(trimmed, markerPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, markerPrefix)), true
}

// reason carries the reviewer's own explanation into the error, so an operator
// reading a failed Job sees why rather than only that it failed.
func reason(output string) string {
	var kept []string
	for _, line := range strings.Split(output, "\n") {
		if _, isMarker := markerValue(line); isMarker {
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	text := strings.Join(kept, " ")
	const limit = 500
	if len(text) > limit {
		return text[:limit] + "..."
	}
	return text
}

// withoutMarker removes the status trailer, which is a protocol artifact
// between the prompt and this package rather than part of the review.
func withoutMarker(output string) string {
	lines := strings.Split(output, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if _, isMarker := markerValue(line); isMarker {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimRight(strings.Join(kept, "\n"), " \t\n")
}
