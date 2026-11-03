package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
)

const resolvedSHA = "4ce5baee84d7cfb5fe5d2cb8c6726e83871c47f1"

func pullRequestStub(t *testing.T, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widget/pulls/73" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		*calls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"head": map[string]string{"ref": "feature", "sha": resolvedSHA},
		})
	}))
}

// A comment-triggered request carries Ref="pr-73" and no commit: the reviewer
// resolves the real head itself, in memory, and the Job it runs in never
// learns the answer. Anything outside the process -- the sentinel that reports
// a reviewer killed before it could publish -- then has no commit to name and
// no way to recognise the reviewer's own publication.
//
// Resolving before the Job is built is what makes the Job self-describing.
func TestACommentTriggeredRequestIsResolvedBeforeSubmission(t *testing.T) {
	var calls int
	api := pullRequestStub(t, &calls)
	defer api.Close()
	t.Setenv("BOSUN_GITHUB_API_URL", api.URL)
	t.Setenv("BOSUN_GITHUB_APP_ID", "")
	t.Setenv("BOSUN_GITHUB_PRIVATE_KEY", "")
	t.Setenv("GITHUB_TOKEN", "test-token")

	req := review.Request{Repo: "acme/widget", Ref: "pr-73", PRNumber: 73, Trigger: "comment"}
	got := resolveIdentity(context.Background(), req)

	if got.Ref != "feature" || got.SHA != resolvedSHA {
		t.Fatalf("request was not resolved: Ref=%q SHA=%q", got.Ref, got.SHA)
	}
	if calls != 1 {
		t.Errorf("want one lookup, got %d", calls)
	}
	// The Job's name is this delivery's deduplication key and derives from the
	// ref. Resolution must not move it, or a redelivery after a failed
	// resolution creates a second Job.
	if got.NameRef() != "pr-73" {
		t.Errorf("NameRef() = %q; resolution moved the deduplication key", got.NameRef())
	}
}

// A request that already names its commit -- every pull_request trigger does --
// must not spend a GitHub call re-asking.
func TestAnAlreadyIdentifiedRequestIsNotResolvedAgain(t *testing.T) {
	var calls int
	api := pullRequestStub(t, &calls)
	defer api.Close()
	t.Setenv("BOSUN_GITHUB_API_URL", api.URL)
	t.Setenv("GITHUB_TOKEN", "test-token")

	req := review.Request{Repo: "acme/widget", Ref: "feature", SHA: resolvedSHA, PRNumber: 73, Trigger: "pull-request-synchronize"}
	if got := resolveIdentity(context.Background(), req); got != req {
		t.Fatalf("an identified request was altered: %+v", got)
	}
	if calls != 0 {
		t.Errorf("spent %d GitHub calls on a request that needed none", calls)
	}
}

// Resolution is an improvement, not a precondition. The reviewer still
// resolves for itself, so a GitHub hiccup here must cost the review nothing --
// it costs only the sentinel's ability to report that review's termination.
func TestAFailedResolutionStillSubmitsTheReview(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()
	t.Setenv("BOSUN_GITHUB_API_URL", api.URL)
	t.Setenv("GITHUB_TOKEN", "test-token")

	req := review.Request{Repo: "acme/widget", Ref: "pr-73", PRNumber: 73, Trigger: "comment"}
	if got := resolveIdentity(context.Background(), req); got != req {
		t.Fatalf("a failed resolution changed the request instead of leaving it alone: %+v", got)
	}
}

// A branch-created request has no pull request to resolve against.
func TestARequestWithoutAPullRequestIsLeftAlone(t *testing.T) {
	var calls int
	api := pullRequestStub(t, &calls)
	defer api.Close()
	t.Setenv("BOSUN_GITHUB_API_URL", api.URL)
	t.Setenv("GITHUB_TOKEN", "test-token")

	req := review.Request{Repo: "acme/widget", Ref: "feature", Trigger: "branch-created"}
	if got := resolveIdentity(context.Background(), req); got != req {
		t.Fatalf("a branch request was altered: %+v", got)
	}
	if calls != 0 {
		t.Errorf("spent %d GitHub calls resolving a branch", calls)
	}
}

// Resolution runs inside the webhook response, before admission. Each GitHub
// call it makes carries its own 30-second client timeout -- a token mint is
// two of them, then the lookup is a third -- so an unbounded resolution can
// spend 90 seconds before admission has even started, and the delivery loses
// its acknowledgement while its Job is created anyway.
func TestResolutionIsBoundedSoTheDeliveryStillGetsAnAnswer(t *testing.T) {
	blocked := make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blocked
	}))
	// Defers run last-in-first-out, so the handler is released before Close
	// waits on it. The other order deadlocks the test.
	defer api.Close()
	defer close(blocked)
	t.Setenv("BOSUN_GITHUB_API_URL", api.URL)
	t.Setenv("BOSUN_GITHUB_APP_ID", "")
	t.Setenv("BOSUN_GITHUB_PRIVATE_KEY", "")
	t.Setenv("GITHUB_TOKEN", "test-token")

	req := review.Request{Repo: "acme/widget", Ref: "pr-73", PRNumber: 73, Trigger: "comment"}
	start := time.Now()
	got := resolveIdentity(context.Background(), req)
	elapsed := time.Since(start)

	if got != req {
		t.Errorf("a timed-out resolution altered the request: %+v", got)
	}
	if elapsed > identityResolveBudget+5*time.Second {
		t.Errorf("resolution took %v; it must give up within its %v budget", elapsed, identityResolveBudget)
	}
}

// The budget only helps if the response still has room for admission and for
// writing the reply.
func TestTheResponseBudgetCoversResolutionAndAdmission(t *testing.T) {
	srv := newWebhookServer(nil)
	needed := identityResolveBudget + jobs.AdmissionBudget
	if srv.WriteTimeout <= needed {
		t.Errorf("WriteTimeout %v must exceed resolution plus admission (%v)", srv.WriteTimeout, needed)
	}
}
