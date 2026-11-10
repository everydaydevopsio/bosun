package sweep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

var now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)

const (
	sha1 = "1111111111111111111111111111111111111111"
	sha2 = "2222222222222222222222222222222222222222"
)

type fakeGitHub struct {
	prs      map[string][]review.OpenPullRequest
	reviewed map[string]bool // repo@sha
	err      error
	errFor   map[int]error // per pull request
}

func (f *fakeGitHub) Installations(context.Context) ([]review.Installation, error) {
	return []review.Installation{{ID: 1}}, nil
}

func (f *fakeGitHub) Token(context.Context, review.Installation) (string, error) {
	return "ghs_read", nil
}

func (f *fakeGitHub) Repositories(context.Context, string) ([]string, error) {
	var out []string
	for r := range f.prs {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeGitHub) OpenPullRequests(_ context.Context, _, repo string, _ time.Time) ([]review.OpenPullRequest, error) {
	return f.prs[repo], nil
}

func (f *fakeGitHub) Reviewed(_ context.Context, _, repo string, number int, sha string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if err := f.errFor[number]; err != nil {
		return false, err
	}
	return f.reviewed[repo+"@"+sha], nil
}

func cfg() config.Config {
	return config.Config{
		Namespace: "bosun", MaxConcurrentReviews: 3, MaxQueuedReviews: 10, ReviewTimeoutSeconds: 1800,
		ReviewImage: "img", SweepLookbackSeconds: 86400, SweepGraceSeconds: 300, SweepMaxPerPass: 10,
	}
}

func sweeper(client kubernetes.Interface, gh GitHub, c config.Config) *Sweeper {
	return &Sweeper{
		Client: client, Config: c, GitHub: gh, Now: func() time.Time { return now },
		Submit: func(ctx context.Context, r review.Request, d string) (string, error) {
			return jobs.Submit(ctx, client, c, r, d)
		},
	}
}

func pr(n int, sha string, age time.Duration) review.OpenPullRequest {
	return review.OpenPullRequest{Repo: "acme/ballast", Number: n, Ref: "branch", SHA: sha, UpdatedAt: now.Add(-age)}
}

func jobCount(t *testing.T, client kubernetes.Interface) int {
	t.Helper()
	list, err := client.BatchV1().Jobs("bosun").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return len(list.Items)
}

// ballast#425: opened, webhook refused, no review. The sweep must request it.
func TestAnUnreviewedHeadGetsAReview(t *testing.T) {
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(425, sha1, time.Hour)}}}
	if err := sweeper(client, gh, cfg()).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	list, _ := client.BatchV1().Jobs("bosun").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("jobs = %d, want 1", len(list.Items))
	}
	j := &list.Items[0]
	if jobs.ReviewedSHA(j) != sha1 {
		t.Errorf("job reviews %q, want the head commit", jobs.ReviewedSHA(j))
	}
	found := false
	for _, v := range j.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "BOSUN_TRIGGER" && v.Value == Trigger {
			found = true
		}
	}
	if !found {
		t.Error("the job does not say the sweep requested it")
	}
}

func TestTheSweepLeavesAlone(t *testing.T) {
	draft := pr(3, sha1, time.Hour)
	draft.Draft = true
	cases := map[string]struct {
		pr       review.OpenPullRequest
		reviewed bool
	}{
		"a reviewed head":               {pr(1, sha1, time.Hour), true},
		"a draft":                       {draft, false},
		"a change its webhook may own":  {pr(4, sha1, time.Minute), false},
		"a pull request outside window": {pr(5, sha1, 48*time.Hour), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			gh := &fakeGitHub{
				prs:      map[string][]review.OpenPullRequest{"acme/ballast": {c.pr}},
				reviewed: map[string]bool{"acme/ballast@" + sha1: c.reviewed},
			}
			if err := sweeper(client, gh, cfg()).Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := jobCount(t, client); n != 0 {
				t.Errorf("jobs = %d, want 0", n)
			}
		})
	}
}

// A Job for this head already exists -- running, queued, or finished inside
// its TTL. It will publish, or already did; a second review is waste.
func TestAHeadWithAJobIsNotReviewedAgain(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	c := cfg()
	if _, err := jobs.Submit(ctx, client, c, review.Request{Repo: "acme/ballast", Ref: "branch", SHA: sha1, PRNumber: 9, Trigger: "pull-request-opened"}, "webhook"); err != nil {
		t.Fatal(err)
	}
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(9, sha1, time.Hour)}}}
	if err := sweeper(client, gh, c).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, client); n != 1 {
		t.Errorf("jobs = %d, want only the webhook's", n)
	}
}

// Two replicas, or two passes, reach the same head: one Job.
func TestRepeatedPassesRequestOneReview(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(7, sha2, time.Hour)}}}
	a, b := sweeper(client, gh, cfg()), sweeper(client, gh, cfg())
	for _, s := range []*Sweeper{a, b, a} {
		if err := s.Sweep(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := jobCount(t, client); n != 1 {
		t.Errorf("jobs = %d, want 1", n)
	}
}

// A review whose publication never lands must not be bought again every pass.
func TestACommitIsRequestedOncePerController(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(7, sha2, time.Hour)}}}
	s := sweeper(client, gh, cfg())
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// The Job finishes and its TTL removes it, with nothing published.
	list, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	for _, j := range list.Items {
		if err := client.BatchV1().Jobs("bosun").Delete(ctx, j.Name, metav1.DeleteOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, client); n != 0 {
		t.Errorf("jobs = %d, the same commit was requested twice", n)
	}
}

func TestOnePassIsBounded(t *testing.T) {
	client := fake.NewSimpleClientset()
	var prs []review.OpenPullRequest
	for i := 1; i <= 5; i++ {
		prs = append(prs, pr(i, strings.Repeat(string(rune('0'+i)), 40), time.Hour))
	}
	c := cfg()
	c.SweepMaxPerPass = 2
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": prs}}
	if err := sweeper(client, gh, c).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, client); n != 2 {
		t.Errorf("jobs = %d, want the per-pass limit of 2", n)
	}
}

// An unanswerable "was it reviewed?" is not "no": starting a review on that
// guess could duplicate one.
func TestAnUnknownPublicationStateRequestsNothing(t *testing.T) {
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(1, sha1, time.Hour)}}, err: errors.New("HTTP 502")}
	if err := sweeper(client, gh, cfg()).Sweep(context.Background()); err == nil {
		t.Error("the failure was not reported")
	}
	if n := jobCount(t, client); n != 0 {
		t.Errorf("jobs = %d, want 0", n)
	}
}

// Bosun review of #88, finding 3: two pull requests can share a head commit
// (the same branch proposed against two bases). Each is owed its own review.
func TestPullRequestsSharingAHeadEachGetAReview(t *testing.T) {
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(10, sha1, time.Hour), pr(11, sha1, time.Hour)}}}
	if err := sweeper(client, gh, cfg()).Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := jobCount(t, client); n != 2 {
		t.Errorf("jobs = %d, want one per pull request", n)
	}
}

// Bosun review of 8f6b75a, Medium: one pull request whose publications cannot
// be searched to the end ended the whole repository's scan, on every pass.
// The uncertain one is skipped; the rest are still considered.
func TestOneUnsearchablePullRequestDoesNotHideTheRest(t *testing.T) {
	client := fake.NewSimpleClientset()
	gh := &fakeGitHub{
		prs:    map[string][]review.OpenPullRequest{"acme/ballast": {pr(1, sha1, time.Hour), pr(2, sha2, 2*time.Hour)}},
		errFor: map[int]error{1: errors.New("could not establish whether Bosun already published")},
	}
	err := sweeper(client, gh, cfg()).Sweep(context.Background())
	if err == nil {
		t.Error("the unsearchable pull request was not reported")
	}
	list, _ := client.BatchV1().Jobs("bosun").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 1 || jobs.ReviewedSHA(&list.Items[0]) != sha2 {
		t.Errorf("jobs = %d, want one review for the searchable pull request", len(list.Items))
	}
}

// Bosun review of #89: the sweep's request must be dated when it read the
// head, not when it submitted. Otherwise a webhook for a newer head received
// in between looks older than the sweep's obsolete head and is dropped.
func TestASweepRequestIsDatedWhenItReadTheHead(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	c := cfg()
	c.MaxConcurrentReviews = 1
	t0 := now
	// Fill the only slot so both requests below queue.
	if _, err := jobs.Submit(ctx, client, c, review.Request{Repo: "acme/other", Ref: "x", PRNumber: 1}, "busy"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s := sweeper(client, &fakeGitHub{prs: map[string][]review.OpenPullRequest{"acme/ballast": {pr(7, sha1, time.Hour)}}}, c)
	s.Now = func() time.Time { calls++; return t0.Add(time.Duration(calls-1) * 10 * time.Second) }
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// The webhook for the newer head arrived while the sweep was checking
	// publications, after the sweep read the old head.
	_, err := jobs.Submit(ctx, client, c, review.Request{Repo: "acme/ballast", Ref: "branch", SHA: sha2, PRNumber: 7, ReceivedAt: t0.Add(5 * time.Second)}, "webhook")
	if err != nil {
		t.Fatalf("the newer head's webhook was dropped: %v", err)
	}
	list, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	for _, j := range list.Items {
		if jobs.ReviewedSHA(&j) == sha1 {
			t.Error("the sweep's obsolete head is still queued beside the newer one")
		}
	}
}
