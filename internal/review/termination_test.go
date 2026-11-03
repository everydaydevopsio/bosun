package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const terminatedSHA = "0123456789abcdef0123456789abcdef01234567"

// terminationStub serves the three endpoints the publisher touches: the two it
// reads to decide whether Bosun has already spoken, and the one it posts to.
// Existing comments and reviews are supplied as bodies.
type terminationStub struct {
	comments, reviews []string
	posted            []string
	postStatus        int
	// createdAt timestamps every existing item. Zero means "long ago", which
	// keeps the cases that predate the since filter unaffected by it.
	createdAt time.Time
}

func (s *terminationStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	page := func(w http.ResponseWriter, r *http.Request, bodies []string) {
		// Page 1 carries everything; later pages are empty, which is how the
		// publisher learns to stop.
		if r.URL.Query().Get("page") != "" && r.URL.Query().Get("page") != "1" {
			bodies = nil
		}
		stamp := s.createdAt
		if stamp.IsZero() {
			stamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		out := make([]map[string]string, 0, len(bodies))
		for _, b := range bodies {
			out = append(out, map[string]string{"body": b, "created_at": stamp.Format(time.RFC3339), "submitted_at": stamp.Format(time.RFC3339)})
		}
		_ = json.NewEncoder(w).Encode(out)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/repos/acme/widget/issues/7/comments":
			page(w, r, s.comments)
		case r.Method == "GET" && r.URL.Path == "/repos/acme/widget/pulls/7/reviews":
			page(w, r, s.reviews)
		case r.Method == "POST" && r.URL.Path == "/repos/acme/widget/issues/7/comments":
			var body struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			s.posted = append(s.posted, body.Body)
			status := s.postStatus
			if status == 0 {
				status = 201
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func memoryTermination() Termination {
	return Termination{Repo: "acme/widget", PRNumber: 7, Ref: "feature", SHA: terminatedSHA, Cause: OutOfMemoryCause("1Gi")}
}

// An operator reading "the review provider failed" goes looking at the agent.
// The fix is a chart value, so the comment has to name it.
func TestTerminationCommentNamesMemoryAndTheChartValue(t *testing.T) {
	stub := &terminationStub{}
	api := stub.server(t)
	defer api.Close()

	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if !published {
		t.Fatal("nothing was published for an unreported termination")
	}
	if len(stub.posted) != 1 {
		t.Fatalf("want exactly one comment, got %d", len(stub.posted))
	}
	body := stub.posted[0]
	for _, want := range []string{
		"## Bosun code review failed",
		"`feature` @ `" + terminatedSHA + "`",
		"memory",
		"review.resources.limits.memory",
		"1Gi",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("termination comment omits %q:\n%s", want, body)
		}
	}
}

// The invariant in docs/github-app.md: exactly one terminal publication, never
// both a review and a failure comment. A reviewer that published its review and
// was then killed must not be contradicted.
func TestTerminationIsSilentWhenBosunAlreadyPublished(t *testing.T) {
	for _, tc := range []struct {
		name     string
		comments []string
		reviews  []string
	}{
		{
			name:    "a review landed for this commit",
			reviews: []string{reviewMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\nFindings."},
		},
		{
			name:     "a summary comment landed for this commit",
			comments: []string{reviewMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\nFindings."},
		},
		{
			name:     "a failure comment already reported this commit",
			comments: []string{failureMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\n`boom`"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &terminationStub{comments: tc.comments, reviews: tc.reviews}
			api := stub.server(t)
			defer api.Close()

			published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
			if err != nil {
				t.Fatalf("PublishTermination: %v", err)
			}
			if published {
				t.Error("published a second terminal publication for the same commit")
			}
			if len(stub.posted) != 0 {
				t.Errorf("posted despite an existing publication: %q", stub.posted)
			}
		})
	}
}

// Publications are correlated to a commit, not to a pull request. An earlier
// push having been reviewed says nothing about this one.
func TestTerminationPublishesWhenOnlyAnEarlierCommitWasReviewed(t *testing.T) {
	other := strings.Repeat("a", 40)
	stub := &terminationStub{
		reviews:  []string{reviewMarker + "\n\n" + metadata("feature", other) + "\n\nFindings."},
		comments: []string{"a human comment", failureMarker + "\n\n" + metadata("feature", other) + "\n\n`boom`"},
	}
	api := stub.server(t)
	defer api.Close()

	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if !published || len(stub.posted) != 1 {
		t.Fatalf("a termination on an unreviewed commit was not reported: published=%v posts=%d", published, len(stub.posted))
	}
}

// Reporting the failure of the report is the caller's decision, so the error
// has to reach it rather than being swallowed into a silent false.
func TestTerminationReportsAFailedPost(t *testing.T) {
	stub := &terminationStub{postStatus: 500}
	api := stub.server(t)
	defer api.Close()

	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
	if err == nil {
		t.Fatal("a rejected comment was reported as success")
	}
	if published {
		t.Error("reported a publication that GitHub rejected")
	}
}

// A termination with no pull request has nowhere to go, and a caller that
// passes one should not be made to special-case it.
func TestTerminationWithoutAPullRequestIsNotPublished(t *testing.T) {
	stub := &terminationStub{}
	api := stub.server(t)
	defer api.Close()

	term := memoryTermination()
	term.PRNumber = 0
	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", term)
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if published || len(stub.posted) != 0 {
		t.Errorf("published without a pull request: published=%v posts=%q", published, stub.posted)
	}
}

// An unset limit still produces a usable message: the operator needs to be
// pointed at the value whether or not the chart currently sets one.
func TestOutOfMemoryCauseWithoutAConfiguredLimit(t *testing.T) {
	cause := OutOfMemoryCause("")
	if !strings.Contains(cause, "memory") || !strings.Contains(cause, "review.resources.limits.memory") {
		t.Fatalf("cause does not point at the memory limit: %q", cause)
	}
}

// GitHub's "Quote reply" reproduces a whole comment body behind "> ". Matching
// the marker anywhere in the body therefore lets an ordinary human reply look
// like Bosun's own publication, and silently suppress the termination report
// the pull request is owed.
func TestAQuotedPublicationDoesNotSuppressTheReport(t *testing.T) {
	quoted := "> " + reviewMarker + "\n> \n> " + metadata("feature", terminatedSHA) + "\n> \n> Findings.\n\nWhat did it mean by this?"
	for _, tc := range []struct {
		name     string
		comments []string
		reviews  []string
	}{
		{name: "a quote-reply to a review", comments: []string{quoted}},
		{name: "a quote-reply to a comment", reviews: []string{quoted}},
		{
			name:     "prose that happens to mention both strings",
			comments: []string{"I think " + reviewMarker + " output for " + metadata("feature", terminatedSHA) + " was wrong."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &terminationStub{comments: tc.comments, reviews: tc.reviews}
			api := stub.server(t)
			defer api.Close()

			published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
			if err != nil {
				t.Fatalf("PublishTermination: %v", err)
			}
			if !published || len(stub.posted) != 1 {
				t.Fatalf("a quoted or mentioned marker suppressed the report: published=%v posts=%d", published, len(stub.posted))
			}
		})
	}
}

// Running out of pages means "not found here", not "does not exist". Treating
// the bound as proof of absence publishes a failure comment contradicting a
// review that is sitting just past the boundary.
func TestAnExhaustedSearchIsAnErrorNotAnAbsence(t *testing.T) {
	var posted int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			posted++
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		// Every page is full, so the search can never conclude.
		full := make([]map[string]string, 100)
		for i := range full {
			full[i] = map[string]string{"body": "an ordinary comment"}
		}
		_ = json.NewEncoder(w).Encode(full)
	}))
	defer api.Close()

	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", memoryTermination())
	if err == nil {
		t.Fatal("an unfinished search was reported as proof that nothing was published")
	}
	if published || posted != 0 {
		t.Errorf("posted without establishing that nothing was there: published=%v posts=%d", published, posted)
	}
}

// The Job records the commit the controller resolved, and the reviewer
// publishes under the commit it reviewed. If the reviewer re-resolves at
// startup it can pick up a newer head after a push, publish under that, and
// leave the sentinel correlating against the commit the Job recorded -- the
// same mismatch resolving at submission exists to remove.
//
// Re-resolution is only needed when nothing resolved it first, which is what
// an empty commit means.
func TestTheReviewerKeepsTheCommitItsJobRecorded(t *testing.T) {
	source, err := os.ReadFile("runner.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), `req.Trigger == "comment"`) {
		t.Error("the reviewer still re-resolves every comment-triggered review, so it can drift from the commit its Job recorded")
	}
	if !strings.Contains(string(source), `req.PRNumber > 0 && req.SHA == ""`) {
		t.Error("the reviewer no longer resolves a request that arrived without a commit")
	}
}

// Correlating only by commit makes an earlier attempt's failure comment
// suppress a later attempt's. A review of commit X fails and says so; the user
// retries the same commit; the retry is OOM-killed and the sentinel sees the
// first failure, stays silent, and records the retry as already reported. The
// pull request keeps a stale reason for a different failure.
//
// Only a publication made after this review started can be this review's.
func TestAnEarlierAttemptDoesNotSuppressThisOne(t *testing.T) {
	started := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	earlier := failureMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\n`provider: context deadline exceeded`"

	stub := &terminationStub{
		comments:  []string{earlier},
		createdAt: started.Add(-10 * time.Minute),
	}
	api := stub.server(t)
	defer api.Close()

	term := memoryTermination()
	term.Since = started
	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", term)
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if !published || len(stub.posted) != 1 {
		t.Fatalf("an earlier attempt's failure suppressed this one: published=%v posts=%d", published, len(stub.posted))
	}
}

// The guard that matters still has to hold: this review's own publication is
// made while the review runs, so it is always newer than the start.
func TestThisReviewsOwnPublicationStillSuppresses(t *testing.T) {
	started := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	stub := &terminationStub{
		reviews:   []string{reviewMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\nFindings."},
		createdAt: started.Add(40 * time.Second),
	}
	api := stub.server(t)
	defer api.Close()

	term := memoryTermination()
	term.Since = started
	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", term)
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if published || len(stub.posted) != 0 {
		t.Fatalf("contradicted a review this reviewer had already published: published=%v posts=%d", published, len(stub.posted))
	}
}

// The deadline message has to point at the knob that fixes it, the way the
// memory message points at review.resources.limits.memory.
func TestDeadlineCauseNamesTheReviewTimeout(t *testing.T) {
	cause := DeadlineExceededCause(1800)
	for _, want := range []string{"review.timeoutSeconds", "1800"} {
		if !strings.Contains(cause, want) {
			t.Errorf("deadline cause omits %q: %q", want, cause)
		}
	}
	if DeadlineExceededCause(0) == "" {
		t.Error("an unconfigured timeout produces no message at all")
	}
}

// A reviewer can publish its own timeout and then hang past the Job deadline.
// The sentinel will try to report that kill; the guard has to recognise the
// reviewer's own comment and stay silent, or the pull request carries both.
func TestADeadlineAfterTheReviewerPublishedIsNotReportedAgain(t *testing.T) {
	started := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	stub := &terminationStub{
		comments:  []string{failureMarker + "\n\n" + metadata("feature", terminatedSHA) + "\n\n`provider: context deadline exceeded`"},
		createdAt: started.Add(30 * time.Minute),
	}
	api := stub.server(t)
	defer api.Close()

	term := memoryTermination()
	term.Since = started
	term.Cause = DeadlineExceededCause(1800)
	published, err := PublishTermination(context.Background(), api.URL, api.Client(), "test-token", term)
	if err != nil {
		t.Fatalf("PublishTermination: %v", err)
	}
	if published || len(stub.posted) != 0 {
		t.Fatalf("published a second terminal comment beside the reviewer's own: published=%v posts=%d", published, len(stub.posted))
	}
}
