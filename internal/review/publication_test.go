package review

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

// A branch moves. Output that does not name the revision it reviewed leaves a
// consumer matching reviews to pushes by timestamp, which is ambiguous for two
// pushes in quick succession.
func TestPublishedReviewNamesTheReviewedCommit(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	var posted string
	api := commentStub(t, &posted, nil)
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "test-token", "feature", sha, "Finding.")
	if err != nil {
		t.Fatalf("postReview: %v", err)
	}
	if !strings.HasPrefix(posted, "## Bosun code review\n") {
		t.Fatalf("marker line changed, which breaks consumers: %q", posted)
	}
	if !strings.Contains(posted, "`feature` @ `"+sha+"`") {
		t.Fatalf("published review omits ref and commit: %q", posted)
	}
	if !strings.Contains(posted, "Finding.") {
		t.Fatalf("published review omits the findings: %q", posted)
	}
}

// The failure comment is the only signal a pull-request author gets when a
// hosted review dies, so it must be distinguishable from a published review
// and must carry the commit it was trying to review.
func TestFailureCommentIsDistinguishableAndCorrelated(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	var posted string
	api := commentStub(t, &posted, nil)
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postFailure(context.Background(), "acme/widget", 7, "test-token", "feature", sha, "provider: deadline\nexceeded")
	if err != nil {
		t.Fatalf("postFailure: %v", err)
	}
	if !strings.HasPrefix(posted, "## Bosun code review failed\n") {
		t.Fatalf("failure marker changed, which breaks consumers: %q", posted)
	}
	if strings.HasPrefix(posted, "## Bosun code review\n") {
		t.Fatalf("failure comment is indistinguishable from a review: %q", posted)
	}
	if !strings.Contains(posted, "`feature` @ `"+sha+"`") {
		t.Fatalf("failure comment omits ref and commit: %q", posted)
	}
	// A multi-line reason inside a code span would end the span and let the
	// remainder escape into markup.
	if !strings.Contains(posted, "`provider: deadline exceeded`") {
		t.Fatalf("failure reason not rendered as a single code span: %q", posted)
	}
}

// Backticks in caller-supplied text would close the code span the metadata
// line and failure reason are rendered in.
func TestPublicationEscapesCodeSpanDelimiters(t *testing.T) {
	var posted string
	api := commentStub(t, &posted, nil)
	defer api.Close()
	if err := (githubClient{api.URL, api.Client()}).postFailure(context.Background(), "acme/widget", 7, "test-token", "fea`ture", "sha", "rea`son"); err != nil {
		t.Fatalf("postFailure: %v", err)
	}
	for _, line := range strings.Split(posted, "\n") {
		if strings.Count(line, "`")%2 != 0 {
			t.Fatalf("unbalanced code span: %q", line)
		}
	}
}

// A hosted review is fire-and-forget: without this comment a failed review and
// a review that was never requested look identical from the pull request.
func TestHostedFailurePublishesExactlyOneFailureComment(t *testing.T) {
	const token = "ghs-hosted-test-token"
	t.Setenv("GITHUB_TOKEN", token)
	t.Setenv("BOSUN_REPO", "acme/repo")
	t.Setenv("BOSUN_REF", "feature")
	t.Setenv("BOSUN_SHA", "0123456789abcdef0123456789abcdef01234567")
	t.Setenv("BOSUN_PR_NUMBER", "7")

	var posted string
	count := 0
	api := commentStub(t, &posted, &count)
	defer api.Close()

	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	w := Worker{
		APIBaseURL: api.URL,
		// No repository exists under this root, so the clone fails after the
		// token is in hand — the first stage that can reach the pull request.
		CloneBaseURL: t.TempDir(),
		PromptPath:   prompt,
		Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
			t.Error("provider ran despite a failed clone")
			return "", nil
		},
	}
	err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60})
	if err == nil {
		t.Fatal("expected the review to fail")
	}
	if count != 1 {
		t.Fatalf("published %d comments, want exactly 1", count)
	}
	if !strings.HasPrefix(posted, "## Bosun code review failed") {
		t.Fatalf("unexpected comment: %q", posted)
	}
	if strings.Contains(posted, token) {
		t.Fatalf("failure comment leaked the installation token: %q", posted)
	}
	// The webhook supplied the exact commit. A consumer waiting on that commit
	// cannot match a failure that reports an empty one.
	if !strings.Contains(posted, "`0123456789abcdef0123456789abcdef01234567`") {
		t.Fatalf("failure comment lost the commit the webhook supplied: %q", posted)
	}
}

// Exactly one terminal publication per review: a review, or a failure, never
// both. A published review followed by a failing stdout write would otherwise
// contradict itself on the pull request.
func TestPublishedReviewIsNotFollowedByAFailureComment(t *testing.T) {
	const token = "ghs-hosted-test-token"
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	remote := filepath.Join(root, "acme", "repo.git")
	for _, dir := range []string{repo, filepath.Dir(remote)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, repo, "init", "-b", "main")
	writeTestFile(t, filepath.Join(repo, "file"), "base")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "base")
	writeTestFile(t, filepath.Join(repo, "file"), "reviewed")
	testGit(t, repo, "commit", "-am", "review this")
	head := testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, root, "clone", "--bare", repo, remote)
	testGit(t, root, "--git-dir", remote, "update-ref", "refs/pull/7/head", head)

	t.Setenv("GITHUB_TOKEN", token)
	t.Setenv("BOSUN_REPO", "acme/repo")
	t.Setenv("BOSUN_REF", "main")
	t.Setenv("BOSUN_SHA", head)
	t.Setenv("BOSUN_PR_NUMBER", "7")

	var posted string
	count := 0
	api := commentStub(t, &posted, &count)
	defer api.Close()

	prompt := filepath.Join(root, "prompt.md")
	writeTestFile(t, prompt, "review this")
	w := Worker{
		APIBaseURL:   api.URL,
		CloneBaseURL: root,
		PromptPath:   prompt,
		Output:       failingWriter{},
		Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
			return "Finding.", nil
		},
	}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err == nil {
		t.Fatal("expected the output write to fail")
	}
	if count != 1 {
		t.Fatalf("published %d comments, want exactly 1", count)
	}
	if !strings.HasPrefix(posted, "## Bosun code review\n") {
		t.Fatalf("the published review was contradicted by a failure comment: %q", posted)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("output stream closed") }

func commentStub(t *testing.T, posted *string, count *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /repos/acme/repo/issues/7/comments" &&
			r.Method+" "+r.URL.Path != "POST /repos/acme/widget/issues/7/comments" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		*posted = body.Body
		if count != nil {
			*count++
		}
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{}`))
	}))
}
