package review

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

// reviewURL is what GitHub returns for a published review.
const reviewURL = "https://github.com/acme/widget/pull/7#pullrequestreview-99"

type reviewPayload struct {
	Event    string `json:"event"`
	CommitID string `json:"commit_id"`
	Body     string `json:"body"`
	Comments []struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		StartLine int    `json:"start_line"`
		Side      string `json:"side"`
		Body      string `json:"body"`
	} `json:"comments"`
}

// reviewStub serves the pull-request files and reviews endpoints, recording
// every review payload. reject makes the reviews endpoint answer 422 on the
// calls whose index it returns true for, which is how GitHub refuses a review
// carrying one unanchorable comment.
func reviewStub(t *testing.T, files []string, reject func(int) bool, comments *[]string) (*httptest.Server, *[]reviewPayload) {
	t.Helper()
	var payloads []reviewPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/7/files"):
			if files == nil {
				http.Error(w, "boom", 500)
				return
			}
			var entries []string
			for _, f := range files {
				entries = append(entries, fmt.Sprintf(`{"filename":%q}`, f))
			}
			fmt.Fprint(w, "["+strings.Join(entries, ",")+"]")
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls/7/reviews"):
			var payload reviewPayload
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			payloads = append(payloads, payload)
			if reject != nil && reject(len(payloads)-1) {
				http.Error(w, "unprocessable", 422)
				return
			}
			w.WriteHeader(200)
			fmt.Fprint(w, `{"html_url":"`+reviewURL+`"}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
			if comments != nil {
				var posted struct {
					Body string `json:"body"`
				}
				if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
					t.Error(err)
				}
				*comments = append(*comments, posted.Body)
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	return server, &payloads
}

func withFindings(findings string) string {
	return "Summary of the change.\n\n```json\n{\"findings\":[" + findings + "]}\n```"
}

// Findings become review threads, which is the whole point: a conversation
// comment has nothing to reply to and nothing to resolve.
func TestFindingsPublishAsAReviewWithInlineComments(t *testing.T) {
	api, payloads := reviewStub(t, []string{"a.go"}, nil, nil)
	defer api.Close()
	output := withFindings(`{"path":"a.go","line":3,"end_line":5,"severity":"high","title":"Leaks the token","body":"Impact.","remediation":"Redact it."}`)
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
		t.Fatalf("postReview: %v", err)
	}
	if len(*payloads) != 1 {
		t.Fatalf("posted %d reviews, want 1", len(*payloads))
	}
	review := (*payloads)[0]
	if review.Event != "COMMENT" {
		t.Fatalf("event = %q, want COMMENT: an approval or a request for changes is not Bosun's to make", review.Event)
	}
	if !strings.HasPrefix(review.Body, reviewMarker+"\n") || !strings.Contains(review.Body, "`feature` @ `"+testSHA+"`") {
		t.Fatalf("review body lost the marker or the commit: %q", review.Body)
	}
	if !strings.Contains(review.Body, "Summary of the change.") || strings.Contains(review.Body, "```json") {
		t.Fatalf("review body should carry the summary without the contract block: %q", review.Body)
	}
	if len(review.Comments) != 1 {
		t.Fatalf("got %d inline comments, want 1", len(review.Comments))
	}
	// Without commit_id GitHub attaches the review to the latest commit, so a
	// push landing mid-review anchors findings to lines they never described.
	if review.CommitID != testSHA {
		t.Fatalf("commit_id = %q, want the reviewed commit %q", review.CommitID, testSHA)
	}
	c := review.Comments[0]
	if c.Path != "a.go" || c.Line != 5 || c.StartLine != 3 || c.Side != "RIGHT" {
		t.Fatalf("comment anchored wrong: %+v", c)
	}
	if !strings.Contains(c.Body, "**high**") || !strings.Contains(c.Body, "Leaks the token") || !strings.Contains(c.Body, "Redact it.") {
		t.Fatalf("comment body = %q", c.Body)
	}
}

// A finding on a file the pull request does not touch cannot be a thread, but
// losing it would be worse than publishing it as text.
func TestFindingsOutsideTheDiffAreFoldedIntoTheBody(t *testing.T) {
	api, payloads := reviewStub(t, []string{"a.go"}, nil, nil)
	defer api.Close()
	output := withFindings(`{"path":"b.go","line":9,"severity":"medium","title":"Stale caller","body":"Impact."}`)
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
		t.Fatalf("postReview: %v", err)
	}
	review := (*payloads)[0]
	if len(review.Comments) != 0 {
		t.Fatalf("anchored a finding to a file outside the diff: %+v", review.Comments)
	}
	if !strings.Contains(review.Body, "Findings outside the diff") || !strings.Contains(review.Body, "b.go:9") {
		t.Fatalf("finding lost: %q", review.Body)
	}
}

// GitHub rejects the entire review when any one position is invalid, so the
// fallback is the expected production path, not a defensive extra.
func TestInvalidPositionRepublishesEverythingInTheBody(t *testing.T) {
	api, payloads := reviewStub(t, []string{"a.go"}, func(i int) bool { return i == 0 }, nil)
	defer api.Close()
	output := withFindings(`{"path":"a.go","line":9999,"severity":"blocker","title":"Unanchorable","body":"Impact."}`)
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
		t.Fatalf("postReview: %v", err)
	}
	if len(*payloads) != 2 {
		t.Fatalf("posted %d reviews, want 2 (one rejected, one folded)", len(*payloads))
	}
	retry := (*payloads)[1]
	if len(retry.Comments) != 0 {
		t.Fatalf("retry carried comments again: %+v", retry.Comments)
	}
	if retry.CommitID != testSHA {
		t.Fatalf("fallback lost the reviewed commit: %q", retry.CommitID)
	}
	if !strings.Contains(retry.Body, "Unanchorable") || !strings.Contains(retry.Body, "a.go:9999") {
		t.Fatalf("retry lost the finding: %q", retry.Body)
	}
}

// A second 422 means the review cannot be published at all; the runner turns
// that into a publish-stage failure and a failure comment.
func TestRepeatedRejectionFailsRatherThanSilentlyDroppingTheReview(t *testing.T) {
	api, _ := reviewStub(t, []string{"a.go"}, func(int) bool { return true }, nil)
	defer api.Close()
	output := withFindings(`{"path":"a.go","line":1,"severity":"low","title":"T"}`)
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err == nil {
		t.Fatal("expected the publish to fail")
	}
}

// A clean review publishes on the same surface as one with findings. Splitting
// them by outcome meant a check that read only one surface got the answer
// inverted: issue comments showed clean reviews and hid the ones that found
// something. One surface for every outcome makes "did Bosun review this" a
// single query, and one review is one publication, with no pointer beside it.
func TestReviewWithoutFindingsPublishesAsAReview(t *testing.T) {
	var comments []string
	api, payloads := reviewStub(t, []string{"a.go"}, nil, &comments)
	defer api.Close()
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, "No findings."); err != nil {
		t.Fatalf("postReview: %v", err)
	}
	if len(comments) != 0 || len(*payloads) != 1 {
		t.Fatalf("comments=%d reviews=%d, want 0 and 1", len(comments), len(*payloads))
	}
	review := (*payloads)[0]
	if review.Event != "COMMENT" {
		t.Fatalf("event = %q, want COMMENT: a clean review is not an approval, and an approval can satisfy branch protection", review.Event)
	}
	if review.CommitID != testSHA {
		t.Fatalf("commit_id = %q, want the reviewed commit %q", review.CommitID, testSHA)
	}
	if len(review.Comments) != 0 {
		t.Fatalf("a clean review carried threads: %+v", review.Comments)
	}
	if !strings.HasPrefix(review.Body, reviewMarker+"\n") || !strings.Contains(review.Body, "`feature` @ `"+testSHA+"`") || !strings.Contains(review.Body, "No findings.") {
		t.Fatalf("review body lost the marker, the commit, or the summary: %q", review.Body)
	}
}

// Losing the file list is a reason to publish unfiltered, not to lose the
// review: an unanchorable comment is already covered by the 422 fallback.
func TestUnavailableFileListStillPublishes(t *testing.T) {
	api, payloads := reviewStub(t, nil, nil, nil)
	defer api.Close()
	output := withFindings(`{"path":"a.go","line":2,"severity":"low","title":"T"}`)
	if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
		t.Fatalf("postReview: %v", err)
	}
	if len(*payloads) != 1 || len((*payloads)[0].Comments) != 1 {
		t.Fatalf("review not published unfiltered: %+v", *payloads)
	}
}

// Every review is one publication. The summary comment that used to sit beside
// a findings review existed only because clean reviews published elsewhere;
// with one surface for both it is a duplicate message on the pull request.
func TestFindingsReviewPostsNoComment(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reject func(int) bool
	}{
		{"direct", nil},
		{"422 fallback", func(i int) bool { return i == 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var comments []string
			api, _ := reviewStub(t, []string{"a.go"}, tc.reject, &comments)
			defer api.Close()
			output := withFindings(`{"path":"a.go","line":3,"severity":"high","title":"T"}`)
			if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
				t.Fatalf("postReview: %v", err)
			}
			if len(comments) != 0 {
				t.Fatalf("posted %d comments beside the review, want 0: %q", len(comments), comments)
			}
		})
	}
}

// Found by Bosun review. Asking request to decode the response made a
// successful publication depend on reading its body: a truncated body or a
// read timeout after a 2xx turned a review that GitHub had created into a
// returned error. Worker.Run would then leave published false and post a
// failure comment contradicting the review on the pull request -- both
// publications, which GO-4 forbids. Nothing in the reply is needed, so it is
// not read.
func TestUnreadableResponseDoesNotFailAPublishedReview(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reject bool
	}{
		{"direct", false},
		{"422 fallback", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var comments []string
			reviews := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/7/files"):
					fmt.Fprint(w, `[{"filename":"a.go"}]`)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pulls/7/reviews"):
					reviews++
					if tc.reject && reviews == 1 {
						http.Error(w, "unprocessable", 422)
						return
					}
					// Status says the review exists; the body is cut short.
					w.WriteHeader(200)
					fmt.Fprint(w, `{"html_url": "https://github.`)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
					var posted struct {
						Body string `json:"body"`
					}
					if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
						t.Error(err)
					}
					comments = append(comments, posted.Body)
					w.WriteHeader(201)
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			}))
			defer api.Close()
			output := withFindings(`{"path":"a.go","line":3,"severity":"high","title":"T"}`)
			if err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "t", "feature", testSHA, output); err != nil {
				t.Fatalf("an unreadable response must not fail a created review: %v", err)
			}
			if len(comments) != 0 {
				t.Fatalf("posted %d comments beside the review, want 0", len(comments))
			}
		})
	}
}
