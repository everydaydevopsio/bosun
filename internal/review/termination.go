package review

import (
	"context"
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

// terminationPages bounds the search for an existing publication. A hundred
// per page over ten pages covers any pull request where finding Bosun's own
// output is still meaningful, and the bound means a repository with a pathological
// comment count cannot turn one reconcile into an unbounded walk of the API.
const terminationPages = 10

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
		for page := 1; page <= terminationPages; page++ {
			var items []struct {
				Body string `json:"body"`
			}
			if err := g.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), token, nil, &items); err != nil {
				return false, err
			}
			for _, item := range items {
				if !strings.Contains(item.Body, want) {
					continue
				}
				if strings.Contains(item.Body, reviewMarker) || strings.Contains(item.Body, failureMarker) {
					return true, nil
				}
			}
			if len(items) < 100 {
				break
			}
		}
	}
	return false, nil
}
