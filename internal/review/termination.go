package review

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Termination is a reviewer death the reviewer could not report itself.
//
// The reviewer publishes its own failures from a deferred function, which
// covers every error it can return. It cannot cover a SIGKILL: an OOM kill
// runs no deferred function, so the Job fails, its credential Secret is
// garbage-collected with it, and after the Job TTL the logs are gone too. From
// the pull request's side the review simply never happened. Something outside
// the process has to speak for it, and this is what that something publishes.
type Termination struct {
	Repo     string
	PRNumber int
	Ref, SHA string
	// Cause is the operator-facing explanation, already safe to render.
	Cause string
}

// OutOfMemoryCause explains a reviewer killed for exceeding its memory limit.
//
// It names memory and the chart value deliberately. "The review provider
// failed" sends an operator looking at the agent, its credentials, and the
// model, when the fix is one quantity in values.yaml.
func OutOfMemoryCause(limit string) string {
	if limit == "" {
		return "The reviewer was killed for exceeding its memory limit, so it could not report the failure itself. " +
			"Raise `review.resources.limits.memory` and review this commit again."
	}
	return "The reviewer was killed for exceeding its memory limit of `" + codeSpan(limit) + "`, so it could not report the failure itself. " +
		"Raise `review.resources.limits.memory` and review this commit again."
}

// terminationPages bounds the search for an existing publication, so that a
// repository with a pathological comment count cannot turn one reconcile into
// an unbounded walk of the API.
//
// Exhausting it is not an answer. "Not found in the first thousand" is not
// "was never published", and treating it as one publishes a failure comment
// contradicting a review sitting just past the boundary. The search says so
// instead, and the caller retries.
const terminationPages = 10

// errSearchIncomplete means the search hit its bound without establishing
// whether Bosun has already published. It is retryable: the sentinel leaves
// the Job unrecorded and tries again.
var errSearchIncomplete = errors.New("could not establish whether Bosun already published: too many comments to search")

// PublishTermination reports a reviewer that was killed before it could speak.
//
// It returns false, and posts nothing, when Bosun has already published for
// this commit. That guard is what keeps the invariant in docs/github-app.md
// true -- exactly one terminal publication, never both a review and a failure
// comment -- in the case the caller cannot see: a reviewer that published its
// review and was then killed while finishing up. It also makes the call
// idempotent, so a controller restart that loses its own record of what it has
// reported does not produce a second comment.
func PublishTermination(ctx context.Context, apiBaseURL string, client *http.Client, token string, t Termination) (bool, error) {
	if t.PRNumber <= 0 {
		return false, nil
	}
	if !validRepository(t.Repo) {
		return false, fmt.Errorf("invalid repository name")
	}
	if token == "" {
		return false, fmt.Errorf("no GitHub token to publish with")
	}
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	g := githubClient{base: apiBaseURL, http: client}
	published, err := g.publishedFor(ctx, t.Repo, t.PRNumber, token, t.Ref, t.SHA)
	if err != nil {
		return false, err
	}
	if published {
		return false, nil
	}
	body := failureMarker + "\n\n" + metadata(t.Ref, t.SHA) + "\n\n" + truncateOutput(t.Cause)
	if err := g.postComment(ctx, t.Repo, t.PRNumber, token, body); err != nil {
		return false, err
	}
	return true, nil
}

// publishedFor reports whether a Bosun publication already names this commit.
//
// Both surfaces have to be read. Findings are published as a pull-request
// review, a no-findings review and every failure as an issue comment, and
// listing comments does not return reviews.
func (g githubClient) publishedFor(ctx context.Context, repo string, number int, token, ref, sha string) (bool, error) {
	want := metadata(ref, sha)
	for _, path := range []string{
		fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number),
		fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number),
	} {
		done := false
		for page := 1; page <= terminationPages; page++ {
			var items []struct {
				Body string `json:"body"`
			}
			if err := g.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), token, nil, &items); err != nil {
				return false, err
			}
			for _, item := range items {
				if isPublication(item.Body, want) {
					return true, nil
				}
			}
			if len(items) < 100 {
				done = true
				break
			}
		}
		if !done {
			return false, errSearchIncomplete
		}
	}
	return false, nil
}

// isPublication decides whether a body is one of Bosun's own publications for
// this commit, rather than something that merely contains its text.
//
// The marker has to open the body. GitHub's "Quote reply" reproduces a whole
// comment behind "> ", and a person discussing a review quotes both the marker
// and the metadata line; matching either anywhere would let an ordinary reply
// pass as Bosun's output and suppress a report the pull request is owed.
// Anchoring to the start is install-independent, which checking the author is
// not: an App install publishes as a bot and a personal-access-token install
// publishes as the user who owns the token.
func isPublication(body, metadataLine string) bool {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, reviewMarker) && !strings.HasPrefix(trimmed, failureMarker) {
		return false
	}
	return strings.Contains(trimmed, metadataLine)
}
