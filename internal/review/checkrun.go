package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// A branch-created review runs with no pull request to post to. Before this,
// its findings died with the pod behind a Complete Job status (#65).
//
// A check run is keyed by commit rather than by pull request, which is what
// makes it the right home: nothing has to be stored and replayed. When a pull
// request opens on that commit later, GitHub surfaces the existing check run
// on it, so the findings arrive without a second provider call.
//
// Checks is an optional permission. An install that has not granted it keeps
// today's behaviour and gets a WARN instead of silence.
const (
	// checkRunName is what the check appears as on the commit. It is an
	// interface: a consumer selects Bosun's check run by it.
	checkRunName = "Bosun review"
	// checkRunAnnotationLimit is GitHub's cap for one check-run request.
	// Findings beyond it are reported in the output text rather than dropped
	// silently, which would make a 60-finding review look like a 50-finding
	// one.
	checkRunAnnotationLimit = 50
	// GitHub's documented ceilings for the annotation fields. The summary is
	// bounded by truncateOutput, which reviewBody already applies.
	checkRunTitleLimit   = 255
	checkRunMessageLimit = 64000
)

// checksUnavailableError means the token cannot write check runs -- almost
// always because the App installation has not granted the optional Checks
// permission. The review itself succeeded, so this is a publication surface
// being absent, not a failure of the review.
type checksUnavailableError struct{ cause error }

func (e checksUnavailableError) Error() string {
	return "the GitHub App installation has not granted the optional Checks permission, so a branch-only review has nowhere to publish: " + e.cause.Error()
}
func (e checksUnavailableError) Unwrap() error { return e.cause }

func isChecksUnavailable(err error) bool {
	var unavailable checksUnavailableError
	return errors.As(err, &unavailable)
}

// tokenPermissions is the permission set an installation token is minted with.
//
// Requesting a permission the installation does not hold makes GitHub reject
// the whole mint with 422, which would break every install that has not opted
// into Checks. So the grant is read from the installation first and `checks`
// is asked for only when it is actually held at write level.
func tokenPermissions(granted map[string]string) map[string]string {
	permissions := map[string]string{"contents": "read", "pull_requests": "write"}
	if granted["checks"] == "write" {
		permissions["checks"] = "write"
	}
	return permissions
}

// annotationLevel maps a finding's severity onto the three levels GitHub
// renders. Nothing maps to a conclusion that fails the check: see postCheckRun.
func annotationLevel(severity string) string {
	switch severity {
	case "blocker", "high":
		return "failure"
	case "medium":
		return "warning"
	default:
		return "notice"
	}
}

// postCheckRun publishes a branch-only review as a check run on the commit.
func (g githubClient) postCheckRun(ctx context.Context, repo, token, ref, sha, output string) error {
	summary, findings := parseFindings(output)
	annotations := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		if len(annotations) == checkRunAnnotationLimit {
			break
		}
		end := f.EndLine
		if end < f.Line {
			end = f.Line
		}
		annotations = append(annotations, map[string]any{
			"path":             f.Path,
			"start_line":       f.Line,
			"end_line":         end,
			"annotation_level": annotationLevel(f.Severity),
			"title":            truncate(f.Severity+": "+f.Title, checkRunTitleLimit),
			"message":          truncate(findingBody(f), checkRunMessageLimit),
		})
	}
	// Unreachable while findingLimit <= checkRunAnnotationLimit, which
	// TestEveryFindingCanBecomeAnAnnotation pins. Kept so that raising the
	// parse limit degrades into a visible note rather than a silent drop.
	var text string
	if dropped := len(findings) - len(annotations); dropped > 0 {
		text = fmt.Sprintf("GitHub accepts %d annotations per check run; %d more finding(s) are listed in the summary above.",
			checkRunAnnotationLimit, dropped)
	}
	// neutral, never failure: a review reports, it does not gate. Concluding
	// failure would turn the commit red and, on a repository that requires
	// checks, block a merge on an opinion no one opted into.
	conclusion := "neutral"
	title := strconv.Itoa(len(findings)) + " finding(s)"
	if len(findings) == 0 {
		conclusion, title = "success", "No material findings"
	}
	body := map[string]any{
		"name":       checkRunName,
		"head_sha":   sha,
		"status":     "completed",
		"conclusion": conclusion,
		"output": map[string]any{
			"title": title,
			// The same marker and metadata line a comment carries, so a
			// consumer selects a check run exactly as it selects a comment.
			"summary":     reviewBody(ref, sha, summary, "Findings beyond the annotation limit", findings[min(len(findings), checkRunAnnotationLimit):]),
			"text":        text,
			"annotations": annotations,
		},
	}
	err := g.request(ctx, "POST", "/repos/"+repo+"/check-runs", token, body, nil)
	var status httpError
	if errors.As(err, &status) && (status.status == http.StatusForbidden || status.status == http.StatusNotFound) {
		return checksUnavailableError{cause: err}
	}
	return err
}

// associatedPR reports an open pull request for a commit, or 0.
//
// The pull-request number is fixed when the Job is built, which is before the
// review runs. A branch pushed and then opened as a pull request minutes later
// -- the ordinary `git push -u` then `gh pr create` flow -- left the review
// with no destination even though one existed by the time it finished. Asking
// again at publish time closes that race.
func (g githubClient) associatedPR(ctx context.Context, repo, sha, token string) (int, error) {
	if !commitPattern.MatchString(sha) {
		return 0, fmt.Errorf("invalid commit SHA")
	}
	var pulls []struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	if err := g.request(ctx, "GET", "/repos/"+repo+"/commits/"+sha+"/pulls", token, nil, &pulls); err != nil {
		return 0, err
	}
	for _, pr := range pulls {
		// A closed pull request is not a home for a review. The check run
		// stays attached to the commit, which is better than a comment on
		// something nobody is going to merge.
		if pr.State == "open" && pr.Number > 0 {
			return pr.Number, nil
		}
	}
	return 0, nil
}
