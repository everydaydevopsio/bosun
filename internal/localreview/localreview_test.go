package localreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/progress"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %v: %s", args, e, b)
	}
	return strings.TrimSpace(string(b))
}
func write(t *testing.T, path, value string) {
	t.Helper()
	if e := os.WriteFile(path, []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestSnapshotBranchIsolationAndDirtyCheckout(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init", "-b", "main")
	write(t, filepath.Join(repo, "file"), "base")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "base")
	gitTest(t, repo, "checkout", "-b", "feature")
	write(t, filepath.Join(repo, "file"), "feature")
	gitTest(t, repo, "commit", "-am", "feature")
	gitTest(t, repo, "checkout", "main")
	write(t, filepath.Join(repo, "file"), "staged")
	gitTest(t, repo, "add", "file")
	write(t, filepath.Join(repo, "file"), "dirty")
	write(t, filepath.Join(repo, "new"), "untracked")
	write(t, filepath.Join(repo, ".env"), "private")
	before := gitTest(t, repo, "status", "--porcelain")
	s, e := snapshot(context.Background(), repo, "feature", "main")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(s.Path)
	b, _ := os.ReadFile(filepath.Join(s.Path, "file"))
	if string(b) != "feature" {
		t.Fatalf("wrong branch content %q", b)
	}
	if _, e := os.Stat(filepath.Join(s.Path, "new")); !os.IsNotExist(e) {
		t.Fatal("dirty files leaked into explicit branch")
	}
	s, e = snapshot(context.Background(), repo, "", "main")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(s.Path)
	b, _ = os.ReadFile(filepath.Join(s.Path, "file"))
	if string(b) != "dirty" {
		t.Fatalf("dirty content missing %q", b)
	}
	if got := gitTest(t, s.Path, "show", ":file"); got != "staged" {
		t.Fatalf("index lost: %s", got)
	}
	if _, e := os.Stat(filepath.Join(s.Path, ".env")); !os.IsNotExist(e) {
		t.Fatal(".env copied")
	}
	if got := gitTest(t, repo, "status", "--porcelain"); got != before {
		t.Fatal("source checkout modified")
	}
}
func TestSnapshotLinkedWorktree(t *testing.T) {
	repo := t.TempDir()
	gitTest(t, repo, "init", "-b", "main")
	write(t, filepath.Join(repo, "file"), "base")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "base")
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, repo, "worktree", "add", "-b", "linked", linked)
	write(t, filepath.Join(linked, "file"), "linked dirty")
	s, e := snapshot(context.Background(), linked, "", "main")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(s.Path)
	b, _ := os.ReadFile(filepath.Join(s.Path, "file"))
	if string(b) != "linked dirty" {
		t.Fatal("worktree missing")
	}
	if _, e := git(context.Background(), s.Path, "status", "--porcelain"); e != nil {
		t.Fatal(e)
	}
}

func TestSnapshotUsesVerifiedUpstreamBaseInForkLayout(t *testing.T) {
	upstream := t.TempDir()
	gitTest(t, upstream, "init", "-b", "main")
	write(t, filepath.Join(upstream, "file"), "common")
	gitTest(t, upstream, "add", ".")
	gitTest(t, upstream, "commit", "-m", "common")
	fork := t.TempDir()
	gitTest(t, upstream, "clone", upstream, fork)
	gitTest(t, fork, "checkout", "-b", "feature")
	write(t, filepath.Join(fork, "feature"), "feature")
	gitTest(t, fork, "add", ".")
	gitTest(t, fork, "commit", "-m", "feature")
	gitTest(t, fork, "checkout", "main")
	write(t, filepath.Join(fork, "fork-only"), "fork")
	gitTest(t, fork, "add", ".")
	gitTest(t, fork, "commit", "-m", "fork main diverged")
	write(t, filepath.Join(upstream, "upstream-only"), "upstream")
	gitTest(t, upstream, "add", ".")
	gitTest(t, upstream, "commit", "-m", "upstream main advanced")
	upstreamBase := gitTest(t, upstream, "rev-parse", "HEAD")
	gitTest(t, fork, "fetch", upstream, "main")
	if got := gitTest(t, fork, "rev-parse", "FETCH_HEAD"); got != upstreamBase {
		t.Fatalf("fetched base = %s, want %s", got, upstreamBase)
	}
	s, err := snapshot(context.Background(), fork, "feature", upstreamBase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(s.Path) })
	if s.Base != upstreamBase {
		t.Fatalf("snapshot base = %s, want %s", s.Base, upstreamBase)
	}
	if s.MergeBase != gitTest(t, upstream, "rev-parse", "HEAD~") {
		t.Fatalf("merge base = %s, want shared commit", s.MergeBase)
	}
}

func TestReviewHeaderReportsComparisonCommits(t *testing.T) {
	s := Snapshot{Branch: "feature", Head: "aaaaaaaaaaaa", Base: "bbbbbbbbbbbb", MergeBase: "cccccccccccc", Committed: true}
	got := reviewHeader("review-job", s, "codex-bosun", time.Minute)
	for _, want := range []string{"base bbbbbbbbbbbb", "merge base cccccccccccc", "feature at aaaaaaaaaaaa"} {
		if !strings.Contains(got, want) {
			t.Errorf("review header %q is missing %q", got, want)
		}
	}
}
func TestFlagsAfterPath(t *testing.T) {
	o, args, e := parse("review", []string{"/repo with spaces", "--branch", "feature", "--provider", "claude-bosun", "--timeout", "2m"}, io.Discard)
	if e != nil || len(args) != 1 || o.branch != "feature" || o.provider != "claude-bosun" || o.timeout != 2*time.Minute {
		t.Fatalf("%+v %v %v", o, args, e)
	}
	if _, _, e = parse("review", []string{"--timeout", "0"}, io.Discard); e == nil {
		t.Fatal("invalid timeout accepted")
	}
}
func TestEstimatesNeedHistory(t *testing.T) {
	t.Setenv("BOSUN_STATE_DIR", t.TempDir())
	r := record{Repo: "repo", Provider: "claude-bosun", Image: "digest", Started: time.Now(), Changes: 4}
	if !strings.Contains(estimate(&r), "unavailable") {
		t.Fatal("fabricated estimate")
	}
	for i := 0; i < 5; i++ {
		x := r
		x.Job = fmt.Sprint(i)
		x.Outcome = "completed"
		x.Finished = x.Started.Add(time.Duration(i+1) * time.Minute)
		if e := save(&x); e != nil {
			t.Fatal(e)
		}
	}
	if !strings.Contains(estimate(&r), "5 comparable") {
		t.Fatal(estimate(&r))
	}
}
func TestMonitorSeparatesResultAndHandlesFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			t.Setenv("BOSUN_STATE_DIR", t.TempDir())
			var logs bytes.Buffer
			reporter := progress.New(&logs, "job")
			reporter.Emit("stage", "reviewing")
			if failed {
				reporter.Emit("failed", "authentication failed")
			} else {
				reporter.Emit("result", "Final findings")
				reporter.Emit("completed", "done")
			}
			condition := batchv1.JobComplete
			phase := corev1.PodSucceeded
			if failed {
				condition = batchv1.JobFailed
				phase = corev1.PodFailed
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/apis/batch/v1/namespaces/bosun/jobs/job":
					json.NewEncoder(w).Encode(&batchv1.Job{Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{{Type: condition, Status: corev1.ConditionTrue}}}})
				case "/api/v1/namespaces/bosun/pods":
					json.NewEncoder(w).Encode(&corev1.PodList{Items: []corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "pod"}, Status: corev1.PodStatus{Phase: phase}}}})
				case "/api/v1/namespaces/bosun/pods/pod/log":
					fmt.Fprint(w, logs.String())
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client, e := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if e != nil {
				t.Fatal(e)
			}
			record := record{Job: "job", Namespace: "bosun", Started: time.Now(), Snapshot: "/invalid"}
			var out, diag bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			code := monitor(ctx, client, &record, options{follow: true}, &out, &diag)
			if failed {
				if code != 1 || out.Len() != 0 || !strings.Contains(diag.String(), "authentication failed") {
					t.Fatalf("code %d out %s diag %s", code, &out, &diag)
				}
			} else {
				if code != 0 || out.String() != "Final findings\n" {
					t.Fatalf("code %d out %s diag %s", code, &out, &diag)
				}
			}
		})
	}
}

func TestSavedTerminalResultCannotRegress(t *testing.T) {
	t.Setenv("BOSUN_STATE_DIR", t.TempDir())
	r := record{Job: "job", Outcome: "completed", Result: "done", Finished: time.Now(), LastSeq: 10}
	if err := save(&r); err != nil {
		t.Fatal(err)
	}
	stale := record{Job: "job", LastSeq: 2}
	if err := save(&stale); err != nil {
		t.Fatal(err)
	}
	got, err := load("job")
	if err != nil || got.Outcome != "completed" || got.Result != "done" {
		t.Fatalf("%+v %v", got, err)
	}
}
