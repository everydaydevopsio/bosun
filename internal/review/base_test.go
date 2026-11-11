package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

// hostedReleaseRepo publishes a remote whose default branch is main, with a
// pull request (refs/pull/7/head) into a release branch that main has moved
// past. It returns the remote root, the release tip, and the PR head.
func hostedReleaseRepo(t *testing.T) (root, release, head string) {
	t.Helper()
	root = t.TempDir()
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
	testGit(t, repo, "branch", "release")
	writeTestFile(t, filepath.Join(repo, "main-only"), "main moved on")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "main moves")
	testGit(t, repo, "checkout", "-q", "release")
	writeTestFile(t, filepath.Join(repo, "release-only"), "release fix")
	testGit(t, repo, "add", ".")
	testGit(t, repo, "commit", "-m", "release fix")
	release = testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, repo, "checkout", "-q", "-b", "feature")
	writeTestFile(t, filepath.Join(repo, "file"), "reviewed")
	testGit(t, repo, "commit", "-qam", "the pull request")
	head = testGit(t, repo, "rev-parse", "HEAD")
	testGit(t, root, "clone", "-q", "--bare", repo, remote)
	testGit(t, root, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	testGit(t, root, "--git-dir", remote, "update-ref", "refs/pull/7/head", head)
	// GitHub serves any reachable commit by SHA; the clone fetches the head
	// and base that way.
	testGit(t, root, "--git-dir", remote, "config", "uploadpack.allowAnySHA1InWant", "true")
	return root, release, head
}

func runHosted(t *testing.T, root, head, base string) (prompt string, coverage Coverage) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "ghs-hosted-test-token")
	t.Setenv("BOSUN_REPO", "acme/repo")
	t.Setenv("BOSUN_REF", "feature")
	t.Setenv("BOSUN_SHA", head)
	t.Setenv("BOSUN_PR_NUMBER", "7")
	t.Setenv("BOSUN_BASE_SHA", base)
	var posted string
	count := 0
	api := commentStub(t, &posted, &count)
	defer api.Close()
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, promptPath, "review this")
	w := Worker{
		APIBaseURL: api.URL, CloneBaseURL: root, PromptPath: promptPath, Output: &strings.Builder{},
		Agent: func(_ context.Context, workspace, _ string, p string, _ time.Duration) (string, error) {
			prompt = p
			stats, err := diffStats(context.Background(), workspace, os.Getenv("BOSUN_BASE_SHA"), false)
			coverage = buildCoverage(stats, err, nil)
			return "Clean.\nBOSUN-FINDINGS {\"findings\":[],\"residual_risks\":[]}\n", nil
		},
	}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if count != 1 || !strings.Contains(posted, "Review coverage:") {
		t.Fatalf("published %d: %q", count, posted)
	}
	return prompt, coverage
}

// #96 AC1: a pull request into a non-default branch is reviewed, and sized,
// against that branch -- not against main, whose newer commits are not part
// of the change.
func TestHostedReviewComparesAgainstThePullRequestBase(t *testing.T) {
	root, release, head := hostedReleaseRepo(t)
	prompt, coverage := runHosted(t, root, head, release)
	if !strings.Contains(prompt, "Base commit: "+release) {
		t.Fatalf("prompt does not name the PR base:\n%s", prompt)
	}
	// Only `file` changed relative to the release branch. Against main, the
	// release-only commit would be counted too.
	if coverage.Diff == nil || coverage.Diff.MergeBase != release || coverage.Diff.Reviewable.Files != 1 {
		t.Fatalf("coverage = %+v (%s)", coverage.Diff, coverage.DiffNote)
	}
}

// A base the clone cannot fetch must not fail the review: it falls back to
// the default branch, as before #96, rather than losing the review.
func TestHostedReviewSurvivesAnUnfetchableBase(t *testing.T) {
	root, _, head := hostedReleaseRepo(t)
	missing := strings.Repeat("e", 40)
	prompt, _ := runHosted(t, root, head, missing)
	if strings.Contains(prompt, missing) {
		t.Fatalf("prompt names a base the clone does not have:\n%s", prompt)
	}
}
