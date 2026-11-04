package review

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

const branchSHA = "0123456789abcdef0123456789abcdef01234567"

// checkRunStub captures the one check run a branch review publishes.
func checkRunStub(t *testing.T, captured *map[string]any, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method+" "+r.URL.Path != "POST /repos/acme/widget/check-runs" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if captured != nil {
			*captured = body
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id": 1}`))
	}))
}

func findingsOutput(n int) string {
	var b strings.Builder
	b.WriteString("A branch review summary.\n\n```json\n{\"findings\":[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"path":"internal/a` + strconv.Itoa(i) + `.go","line":` + strconv.Itoa(i+1) +
			`,"severity":"high","title":"Finding ` + strconv.Itoa(i) + `","body":"Because."}`)
	}
	b.WriteString("]}\n```\n")
	return b.String()
}

// A branch-created review has no pull request to post to. Before this, its
// findings died with the pod behind a Complete Job status (#65). A check run
// is keyed by commit rather than by pull request, so it needs no store and is
// already attached when a pull request opens on that commit later.
func TestBranchReviewPublishesACheckRunOnTheCommit(t *testing.T) {
	var body map[string]any
	api := checkRunStub(t, &body, 201)
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postCheckRun(
		context.Background(), "acme/widget", "test-token", "feature", branchSHA, findingsOutput(2))
	if err != nil {
		t.Fatalf("postCheckRun: %v", err)
	}
	if body["head_sha"] != branchSHA {
		t.Fatalf("check run is not anchored to the reviewed commit: %v", body["head_sha"])
	}
	if body["status"] != "completed" {
		t.Fatalf("check run left incomplete: %v", body["status"])
	}
	// neutral, not failure: a review must not turn a commit red or block a
	// merge by surprise. Findings are reported, not enforced.
	if body["conclusion"] != "neutral" {
		t.Fatalf("findings concluded %v, want neutral", body["conclusion"])
	}
	output, _ := body["output"].(map[string]any)
	if output == nil {
		t.Fatal("check run carries no output")
	}
	annotations, _ := output["annotations"].([]any)
	if len(annotations) != 2 {
		t.Fatalf("got %d annotations, want one per finding", len(annotations))
	}
	first, _ := annotations[0].(map[string]any)
	if first["path"] != "internal/a0.go" {
		t.Fatalf("annotation not anchored to the finding's file: %v", first["path"])
	}
	if first["annotation_level"] != "failure" {
		t.Fatalf("a high-severity finding annotated as %v", first["annotation_level"])
	}
	if first["start_line"] != float64(1) {
		t.Fatalf("annotation not anchored to the finding's line: %v", first["start_line"])
	}
}

// A clean review is a result, not an absence. It must be distinguishable from
// no review having run at all.
func TestACleanBranchReviewConcludesSuccessfully(t *testing.T) {
	var body map[string]any
	api := checkRunStub(t, &body, 201)
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postCheckRun(
		context.Background(), "acme/widget", "test-token", "feature", branchSHA, "Nothing material found.\n\n```json\n{\"findings\":[]}\n```\n")
	if err != nil {
		t.Fatalf("postCheckRun: %v", err)
	}
	if body["conclusion"] != "success" {
		t.Fatalf("clean review concluded %v, want success", body["conclusion"])
	}
}

// docs/github-app.md calls the marker lines an interface: agents select
// Bosun's publications by them. A check run is a publication, so it carries
// the same marker and the same ref-and-commit metadata line.
func TestCheckRunCarriesTheSameMarkerAndMetadataAsAComment(t *testing.T) {
	var body map[string]any
	api := checkRunStub(t, &body, 201)
	defer api.Close()
	if err := (githubClient{api.URL, api.Client()}).postCheckRun(
		context.Background(), "acme/widget", "test-token", "feature", branchSHA, findingsOutput(1)); err != nil {
		t.Fatalf("postCheckRun: %v", err)
	}
	output, _ := body["output"].(map[string]any)
	text, _ := output["summary"].(string)
	if !strings.HasPrefix(text, reviewMarker+"\n") {
		t.Fatalf("check run omits the review marker: %q", text)
	}
	if !strings.Contains(text, "`feature` @ `"+branchSHA+"`") {
		t.Fatalf("check run omits ref and commit: %q", text)
	}
}

// parseFindings caps a review at findingLimit findings and GitHub accepts
// checkRunAnnotationLimit annotations per check run. While the first is at or
// below the second, every finding becomes an annotation. Raising findingLimit
// past it would silently drop the excess, making a 60-finding review look like
// a 50-finding one, so pin the relationship rather than the symptom.
func TestEveryFindingCanBecomeAnAnnotation(t *testing.T) {
	if findingLimit > checkRunAnnotationLimit {
		t.Fatalf("findingLimit %d exceeds the %d annotations GitHub accepts; postCheckRun would drop %d finding(s)",
			findingLimit, checkRunAnnotationLimit, findingLimit-checkRunAnnotationLimit)
	}
	var body map[string]any
	api := checkRunStub(t, &body, 201)
	defer api.Close()
	if err := (githubClient{api.URL, api.Client()}).postCheckRun(
		context.Background(), "acme/widget", "test-token", "feature", branchSHA, findingsOutput(findingLimit)); err != nil {
		t.Fatalf("postCheckRun: %v", err)
	}
	output, _ := body["output"].(map[string]any)
	annotations, _ := output["annotations"].([]any)
	if len(annotations) != findingLimit {
		t.Fatalf("got %d annotations for %d findings", len(annotations), findingLimit)
	}
}

// An install that has not granted the optional Checks permission must keep
// working. The review is already complete; failing the Job over a publication
// surface the operator did not opt into would turn a usable review into a
// failure.
func TestACheckRunRejectedForPermissionIsNotAFailure(t *testing.T) {
	api := checkRunStub(t, nil, http.StatusForbidden)
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postCheckRun(
		context.Background(), "acme/widget", "test-token", "feature", branchSHA, findingsOutput(1))
	if !isChecksUnavailable(err) {
		t.Fatalf("a 403 must be reported as Checks being unavailable, got %v", err)
	}
}

// Requesting a permission the installation does not hold makes GitHub reject
// the whole token mint with 422, which would break every existing install that
// has not granted Checks.
func TestChecksIsRequestedOnlyWhenTheInstallationGrantsIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		granted map[string]string
		want    bool
	}{
		{"granted", map[string]string{"contents": "read", "pull_requests": "write", "checks": "write"}, true},
		{"not granted", map[string]string{"contents": "read", "pull_requests": "write"}, false},
		{"read only", map[string]string{"contents": "read", "pull_requests": "write", "checks": "read"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tokenPermissions(tc.granted)
			if _, ok := got["checks"]; ok != tc.want {
				t.Fatalf("checks requested=%v, want %v (granted %v)", ok, tc.want, tc.granted)
			}
			if got["contents"] != "read" || got["pull_requests"] != "write" {
				t.Fatalf("the base permissions changed: %v", got)
			}
		})
	}
}

// A pull request opened while the review was running is the case the old
// Job-creation-time PR number could never see. Resolving late means that
// review publishes as a normal inline review instead of a check run.
func TestAPullRequestOpenedDuringTheReviewIsFoundLate(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widget/commits/"+branchSHA+"/pulls" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"number": 41, "state": "open"}]`))
	}))
	defer api.Close()
	number, err := (githubClient{api.URL, api.Client()}).associatedPR(context.Background(), "acme/widget", branchSHA, "test-token")
	if err != nil {
		t.Fatalf("associatedPR: %v", err)
	}
	if number != 41 {
		t.Fatalf("got pull request %d, want 41", number)
	}
}

// A closed pull request is not a home for a review: publishing there is worse
// than the check run, which stays attached to the commit.
func TestAClosedPullRequestIsNotTreatedAsAHome(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"number": 41, "state": "closed"}]`))
	}))
	defer api.Close()
	number, err := (githubClient{api.URL, api.Client()}).associatedPR(context.Background(), "acme/widget", branchSHA, "test-token")
	if err != nil {
		t.Fatalf("associatedPR: %v", err)
	}
	if number != 0 {
		t.Fatalf("got pull request %d, want none", number)
	}
}

// branchRepo builds a bare remote holding one reviewable commit on `feature`,
// the shape a branch-created review clones.
func branchRepo(t *testing.T) (root, head string) {
	t.Helper()
	root = t.TempDir()
	repo := filepath.Join(root, "work")
	remote := filepath.Join(root, "acme", "repo.git")
	for _, dir := range []string{repo, filepath.Dir(remote)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	testGit(t, repo, "init", "-b", "feature")
	writeTestFile(t, filepath.Join(repo, "file"), "base")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "base")
	writeTestFile(t, filepath.Join(repo, "file"), "reviewed")
	testGit(t, repo, "commit", "-am", "review this")
	head = testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, root, "clone", "--bare", repo, remote)
	return root, head
}

// hostedStub records every GitHub call a hosted review makes and lets a test
// decide what the commit's pull-request lookup and the check-run write return.
type hostedStub struct {
	seen       map[string]int
	pullsBody  string
	checkRunOK bool
}

func (h *hostedStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	h.seen = map[string]int{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		h.seen[key]++
		switch {
		case strings.HasSuffix(r.URL.Path, "/pulls") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(h.pullsBody))
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			if !h.checkRunOK {
				// What an installation without the optional Checks permission
				// returns.
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":1}`))
		default:
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
}

func runBranchReview(t *testing.T, api *httptest.Server, root, head string) error {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "ghs-hosted-test-token")
	t.Setenv("BOSUN_REPO", "acme/repo")
	t.Setenv("BOSUN_REF", "feature")
	t.Setenv("BOSUN_SHA", head)
	t.Setenv("BOSUN_PR_NUMBER", "0")
	t.Setenv("BOSUN_TRIGGER", "branch-created")
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	w := Worker{
		APIBaseURL:   api.URL,
		CloneBaseURL: root,
		PromptPath:   prompt,
		Output:       io.Discard,
		Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
			return findingsOutput(1), nil
		},
	}
	return w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60})
}

// The regression #65 asks for: before this, a completed branch-created review
// was discarded behind a Complete Job status, and no test asserted what
// happened to the result.
func TestABranchReviewWithNoPullRequestPublishesACheckRun(t *testing.T) {
	root, head := branchRepo(t)
	stub := &hostedStub{pullsBody: `[]`, checkRunOK: true}
	api := stub.server(t)
	defer api.Close()
	if err := runBranchReview(t, api, root, head); err != nil {
		t.Fatalf("branch review failed: %v", err)
	}
	if stub.seen["POST /repos/acme/repo/check-runs"] != 1 {
		t.Fatalf("the review was not published as a check run: %v", stub.seen)
	}
}

// Resolving the pull request late is what closes the push-then-open-a-PR race.
// The review publishes in the normal inline shape, not as a check run.
func TestABranchReviewPublishesToAPullRequestOpenedWhileItRan(t *testing.T) {
	root, head := branchRepo(t)
	stub := &hostedStub{pullsBody: `[{"number":41,"state":"open"}]`, checkRunOK: true}
	api := stub.server(t)
	defer api.Close()
	if err := runBranchReview(t, api, root, head); err != nil {
		t.Fatalf("branch review failed: %v", err)
	}
	if stub.seen["POST /repos/acme/repo/check-runs"] != 0 {
		t.Fatalf("published a check run despite a pull request existing: %v", stub.seen)
	}
	if stub.seen["POST /repos/acme/repo/pulls/41/reviews"] != 1 {
		t.Fatalf("the review did not reach the pull request opened mid-review: %v", stub.seen)
	}
}

// An install that never granted Checks keeps working. The review is complete,
// so failing the Job over a missing publication surface would turn a usable
// review into a failure -- but silence is what made #65 invisible, so it warns
// and names what a reader needs to find the result.
func TestABranchReviewWithoutChecksWarnsInsteadOfDiscardingSilently(t *testing.T) {
	root, head := branchRepo(t)
	stub := &hostedStub{pullsBody: `[]`, checkRunOK: false}
	api := stub.server(t)
	defer api.Close()

	var logged bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	if err := runBranchReview(t, api, root, head); err != nil {
		t.Fatalf("a missing Checks grant must not fail a completed review: %v", err)
	}
	text := logged.String()
	if !strings.Contains(text, "level=WARN") {
		t.Fatalf("the discarded review was not reported at WARN: %q", text)
	}
	for _, want := range []string{"acme/repo", "feature", head, "branch-created"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the warning omits %q, which a reader needs to find the review: %q", want, text)
		}
	}
}
