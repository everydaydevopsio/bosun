package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// Issue #82: a delivery that arrives while every slot is busy used to be
// refused with 503. GitHub never redelivers a failed App webhook, so the
// review was lost. These tests cover the queue that replaces the refusal.

func queueConfig() config.Config {
	cfg := authConfig()
	cfg.MaxConcurrentReviews = 1
	cfg.MaxQueuedReviews = 5
	cfg.QueueTimeoutSeconds = 3600
	return cfg
}

func getJob(t *testing.T, client kubernetes.Interface, name string) *batchv1.Job {
	t.Helper()
	job, err := client.BatchV1().Jobs("bosun").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	return job
}

func complete(t *testing.T, client kubernetes.Interface, name string) {
	t.Helper()
	job := getJob(t, client, name)
	job.Status.Succeeded = 1
	if _, err := client.BatchV1().Jobs("bosun").UpdateStatus(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func suspended(job *batchv1.Job) bool { return job.Spec.Suspend != nil && *job.Spec.Suspend }

// The ballast#425 case: a review for another repository holds the only slot
// and a pull request opens seconds later.
func TestADeliveryAtCapacityIsQueuedNotRefused(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()

	first, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/bilge-pump", Ref: "feat", PRNumber: 3}, "d1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/ballast", Ref: "chore", PRNumber: 425}, "d2")
	if err != nil {
		t.Fatalf("a delivery at capacity was refused: %v", err)
	}
	if suspended(getJob(t, client, first)) {
		t.Error("the review that had a free slot did not start")
	}
	queued := getJob(t, client, second)
	if !suspended(queued) || !isQueued(queued) {
		t.Errorf("the second review should wait suspended and labelled; suspend=%v labels=%v", queued.Spec.Suspend, queued.Labels)
	}
	if n, _ := Active(ctx, client, "bosun"); n != 1 {
		t.Errorf("active = %d, a queued review must not hold a slot", n)
	}
}

func TestAFullQueueRefusesWithACapacityError(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	cfg.MaxQueuedReviews = 1

	for i, d := range []string{"a", "b"} {
		if _, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: d, PRNumber: i + 1}, d); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "c", PRNumber: 3}, "c")
	var full QueueFullError
	if !errors.As(err, &full) {
		t.Fatalf("err = %v, want QueueFullError", err)
	}
	if _, ok := err.(interface{ Capacity() }); !ok {
		t.Error("QueueFullError must satisfy Capacity() so the webhook answers 503")
	}
	list, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 2 {
		t.Errorf("jobs = %d, a refused delivery must not leave a Job", len(list.Items))
	}
}

func TestDispatchStartsTheOldestQueuedReviewWhenASlotFrees(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()

	running, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x", PRNumber: 1}, "1")
	older, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/b", Ref: "y", PRNumber: 2}, "2")
	time.Sleep(2 * time.Millisecond)
	newer, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/c", Ref: "z", PRNumber: 3}, "3")

	d := Dispatcher{Client: client, Config: cfg}
	if err := d.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if !suspended(getJob(t, client, older)) {
		t.Fatal("a queued review started while the only slot was busy")
	}

	complete(t, client, running)
	if err := d.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	o, n := getJob(t, client, older), getJob(t, client, newer)
	if suspended(o) || isQueued(o) {
		t.Error("the oldest queued review was not started")
	}
	if !suspended(n) || !isQueued(n) {
		t.Error("the newer review started beyond the concurrency cap")
	}
}

// A push to a pull request whose previous review is still waiting replaces
// it: the older head would only be reviewed to be overtaken.
func TestANewerRequestForTheSamePullRequestSupersedesItsQueuedReview(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()

	running, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "head", PRNumber: 7, SHA: "0"}, "r")
	stale, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "head", PRNumber: 7, SHA: "1"}, "s1")
	other, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "other", PRNumber: 8, SHA: "1"}, "o")
	fresh, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/repo", Ref: "head", PRNumber: 7, SHA: "2"}, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.BatchV1().Jobs("bosun").Get(ctx, stale, metav1.GetOptions{}); err == nil {
		t.Error("the superseded queued review is still there")
	}
	for _, keep := range []string{running, other, fresh} {
		getJob(t, client, keep)
	}
}

// Minting at start, not at submission, so a review that waited still gets a
// token with its full lifetime -- an installation token lasts an hour.
func TestAQueuedAuthenticatedReviewGetsItsTokenWhenItStarts(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	minted := 0
	issue := func(context.Context, string) (review.RepositoryToken, error) {
		minted++
		return review.RepositoryToken{Value: "ghs_scoped", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}

	running, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x", PRNumber: 1}, "1", issue)
	if err != nil {
		t.Fatal(err)
	}
	queued, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/b", Ref: "y", PRNumber: 2}, "2", issue)
	if err != nil {
		t.Fatal(err)
	}
	// Submission mints before admission and discards the token when the
	// review is queued; what matters is that nothing durable holds it.
	minted = 0
	if _, err := client.CoreV1().Secrets("bosun").Get(ctx, queued+"-github", metav1.GetOptions{}); err == nil {
		t.Error("a queued review already has a credential Secret")
	}

	complete(t, client, running)
	if err := (Dispatcher{Client: client, Config: cfg, Issue: issue}).Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets("bosun").Get(ctx, queued+"-github", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the started review has no credential: %v", err)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != queued {
		t.Errorf("Secret owner = %+v", secret.OwnerReferences)
	}
	if suspended(getJob(t, client, queued)) {
		t.Error("the queued review was not released")
	}
	if minted != 1 {
		t.Errorf("minted %d tokens at start, want a fresh one", minted)
	}
}

// One repository whose token cannot be minted must not block the queue.
func TestAStartFailureLeavesTheReviewQueuedAndTriesTheNext(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	issue := func(_ context.Context, repo string) (review.RepositoryToken, error) {
		if repo == "acme/broken" {
			return review.RepositoryToken{}, errors.New("installation not found")
		}
		return review.RepositoryToken{Value: "ghs", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	running, _ := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x"}, "1", issue)
	// Queued while its installation was healthy, broken by the time it starts.
	broken := queueDirectly(t, client, cfg, review.Request{Repo: "acme/broken", Ref: "y"}, "2", time.Now().Add(-time.Minute))
	healthy := queueDirectly(t, client, cfg, review.Request{Repo: "acme/c", Ref: "z"}, "3", time.Now())

	complete(t, client, running)
	if err := (Dispatcher{Client: client, Config: cfg, Issue: issue}).Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if b := getJob(t, client, broken); !isQueued(b) || !suspended(b) {
		t.Error("a review that could not start should stay queued for a retry")
	}
	if h := getJob(t, client, healthy); isQueued(h) || suspended(h) {
		t.Error("the next queued review did not get the free slot")
	}
}

func TestAQueuedReviewThatWaitsTooLongIsDropped(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()

	if _, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x"}, "1"); err != nil {
		t.Fatal(err)
	}
	waiting, _ := Submit(ctx, client, cfg, review.Request{Repo: "acme/b", Ref: "y"}, "2")

	later := func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := (Dispatcher{Client: client, Config: cfg, Now: later}).Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BatchV1().Jobs("bosun").Get(ctx, waiting, metav1.GetOptions{}); err == nil {
		t.Error("an expired queued review was kept")
	}
}

// Zero disables the queue and restores the old refusal, for an operator who
// would rather drop than wait.
func TestAZeroQueueRefusesAtCapacity(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	cfg.MaxQueuedReviews = 0

	if _, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x"}, "1"); err != nil {
		t.Fatal(err)
	}
	_, err := Submit(ctx, client, cfg, review.Request{Repo: "acme/b", Ref: "y"}, "2")
	if _, ok := err.(CapacityError); !ok {
		t.Fatalf("err = %v, want CapacityError", err)
	}
}

// Bosun review of #88, finding 1: the token used to be minted inside the
// admission transaction's five-second budget. A slow mint expired that budget,
// the cleanup Delete ran on the same dead context, and an unlabelled suspended
// Job was left holding a slot forever. Minting now happens before the Job
// exists, so a slow or failing issuer leaves nothing behind.
func TestASlowTokenMintLeavesNoJobBehind(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	slow := func(ctx context.Context, _ string) (review.RepositoryToken, error) {
		<-ctx.Done()
		return review.RepositoryToken{}, ctx.Err()
	}
	withMintBudget(t, 50*time.Millisecond)

	started := time.Now()
	if _, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x", PRNumber: 1}, "1", slow); err == nil {
		t.Fatal("a mint that never returned was reported as success")
	}
	// Bounded by the mint budget, not the admission transaction's: a mint
	// that eats the transaction's budget is what left Jobs behind. (The fake
	// client ignores contexts, so the leftover itself cannot be reproduced
	// here; the budget it came from can.)
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("a slow mint held the submission for %v; it must give up within its own budget", elapsed)
	}
	list, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatalf("jobs = %d: a failed mint must not leave a Job holding a slot", len(list.Items))
	}
	if n, _ := Active(ctx, client, "bosun"); n != 0 {
		t.Errorf("active = %d", n)
	}
}

// Finding 2: one dispatch pass shared one five-second budget, so a slow oldest
// entry starved every healthy entry behind it. Each mint now has its own
// budget, outside the lease.
func TestASlowOldestEntryDoesNotStarveTheQueue(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := queueConfig()
	cfg.MaxConcurrentReviews = 2
	withMintBudget(t, 50*time.Millisecond)
	issue := func(ctx context.Context, repo string) (review.RepositoryToken, error) {
		if repo == "acme/slow" {
			<-ctx.Done()
			return review.RepositoryToken{}, ctx.Err()
		}
		// A real HTTP call fails on a context the slow entry already spent.
		if err := ctx.Err(); err != nil {
			return review.RepositoryToken{}, err
		}
		return review.RepositoryToken{Value: "ghs", ExpiresAt: time.Now().Add(time.Hour)}, nil
	}
	a, _ := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/a", Ref: "x"}, "1", issue)
	b, _ := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "acme/b", Ref: "x"}, "2", issue)
	// Queue the slow entry first, then a healthy one behind it.
	slow := queueDirectly(t, client, cfg, review.Request{Repo: "acme/slow", Ref: "y"}, "3", time.Now().Add(-time.Minute))
	healthy := queueDirectly(t, client, cfg, review.Request{Repo: "acme/c", Ref: "z"}, "4", time.Now())
	complete(t, client, a)
	complete(t, client, b)

	if err := (Dispatcher{Client: client, Config: cfg, Issue: issue}).Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if s := getJob(t, client, slow); !isQueued(s) {
		t.Error("the slow entry should stay queued for a retry")
	}
	if h := getJob(t, client, healthy); isQueued(h) || suspended(h) {
		t.Error("a healthy entry behind a slow one did not start")
	}
}

func withMintBudget(t *testing.T, d time.Duration) {
	t.Helper()
	old := mintBudget
	mintBudget = d
	t.Cleanup(func() { mintBudget = old })
}

// queueDirectly places an authenticated review in the queue as admission would
// at capacity, with a chosen queue time.
func queueDirectly(t *testing.T, client kubernetes.Interface, cfg config.Config, req review.Request, delivery string, at time.Time) string {
	t.Helper()
	name := Name(req.Repo, req.NameRef(), delivery)
	job, err := build(cfg, req, name)
	if err != nil {
		t.Fatal(err)
	}
	useJobToken(job, name+"-github")
	labelPullRequest(job, req.Repo, req.PRNumber)
	markQueued(job, at)
	if _, err := client.BatchV1().Jobs(cfg.Namespace).Create(context.Background(), job, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	return name
}
