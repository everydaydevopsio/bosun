package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// A review that arrives while every slot is taken is queued rather than
// refused. Refusing meant answering the webhook 503, and GitHub never
// redelivers a failed App webhook, so the review was simply lost (#82).
//
// The queue is the Job objects themselves: a queued review is a reviewer Job
// created suspended, which starts no pod and so costs the cluster nothing but
// an API object. That makes the queue durable across controller restarts and
// shared by every replica, with no store of its own to keep consistent. The
// Dispatcher releases queued Jobs, oldest first, as slots free up, under the
// same admission lease that decides whether a review fits at all.
const (
	// QueuedLabel marks a reviewer Job that is waiting for a slot. It is a
	// label rather than spec.suspend alone because a Job is also suspended,
	// briefly and under the lease, while its credential Secret is created.
	QueuedLabel = "bosun/queued"
	// queuedAtAnnotation orders the queue. creationTimestamp has one-second
	// resolution, which ties under a burst of deliveries.
	queuedAtAnnotation = "bosun/queued-at"
	repositoryJobLabel = "bosun/repository"
	pullRequestLabel   = "bosun/pr"
)

// DefaultQueueInterval paces the dispatcher. A review runs for minutes, so a
// freed slot standing idle for a few seconds costs nothing noticeable.
const DefaultQueueInterval = 10 * time.Second

// QueueFullError means the review could neither start nor wait. It satisfies
// the same Capacity() contract as CapacityError, so the webhook answers 503.
type QueueFullError struct{ Queued, Cap int }

func (e QueueFullError) Error() string {
	return fmt.Sprintf("review queue is full (%d waiting, cap %d)", e.Queued, e.Cap)
}
func (e QueueFullError) Capacity() {}

// starter releases one Job to run. It is the only part that differs between
// the shared-token path and the per-repository credential path.
type starter func(ctx context.Context, job *batchv1.Job) error

// isQueued reports whether a Job is waiting for a slot.
func isQueued(job *batchv1.Job) bool { return job.Labels[QueuedLabel] == "true" }

func finished(job *batchv1.Job) bool { return job.Status.Succeeded > 0 || job.Status.Failed > 0 }

// markQueued turns a built Job into a queue entry: suspended, labelled, and
// stamped so the dispatcher can tell how long it has waited and in what order.
func markQueued(job *batchv1.Job, now time.Time) {
	job.Spec.Suspend = ptr(true)
	job.Labels[QueuedLabel] = "true"
	job.Annotations[queuedAtAnnotation] = now.UTC().Format(time.RFC3339Nano)
}

func queuedAt(job *batchv1.Job) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, job.Annotations[queuedAtAnnotation]); err == nil {
		return t
	}
	return job.CreationTimestamp.Time
}

// unqueue is the patch that releases a queued Job: drop the label and let it
// run. Patch, not Update, for the same reason SubmitAuthenticated patches.
var unqueue = []byte(`{"metadata":{"labels":{"` + QueuedLabel + `":null}},"spec":{"suspend":false}}`)

// admit is the transaction every hosted submission runs under the admission
// lease: create the Job, then either start it now or leave it queued. start
// releases a Job created suspended; nil means it can be created runnable.
func admit(ctx context.Context, client kubernetes.Interface, cfg config.Config, job *batchv1.Job, start starter) (string, error) {
	name := job.Name
	if _, err := client.BatchV1().Jobs(cfg.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return "", ExistsError{name}
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}
	list, err := client.BatchV1().Jobs(cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun"})
	if err != nil {
		return "", err
	}
	running, queued := tally(list.Items)
	if running < cfg.MaxConcurrentReviews {
		// A slot is free. Anything already queued was here first, but the
		// dispatcher will start it on its next pass if a slot remains; taking
		// this slot now keeps the common, uncontended case immediate.
		// A nil starter means the Job needs nothing before it runs, so it is
		// created runnable rather than suspended and then released.
		if start == nil {
			if _, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					return "", ExistsError{name}
				}
				return "", err
			}
			return name, nil
		}
		// Created as a queue entry and released below. If the release fails
		// and the cleanup fails too, what is left is an ordinary queued
		// review the dispatcher retries or expires -- never a suspended Job
		// counted as running that nothing will ever start or remove.
		markQueued(job, time.Now())
		created, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return "", ExistsError{name}
		}
		if err != nil {
			return "", err
		}
		if err := start(ctx, created); err != nil {
			// The transaction's context may be what failed; clean up on one
			// of its own.
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), admissionOperationTimeout)
			defer cancel()
			if derr := client.BatchV1().Jobs(cfg.Namespace).Delete(cleanup, name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)}); derr != nil && !apierrors.IsNotFound(derr) {
				slog.Warn("could not remove a review that failed to start; it stays queued for the dispatcher", "logger", "bosun.queue", "job", name, "error", derr)
			}
			return "", err
		}
		return name, nil
	}

	// Superseded entries do not count against the queue: they are about to go.
	stale := superseded(list.Items, job)
	if queued-len(stale) >= cfg.MaxQueuedReviews {
		if cfg.MaxQueuedReviews <= 0 {
			return "", CapacityError{running, cfg.MaxConcurrentReviews}
		}
		return "", QueueFullError{queued - len(stale), cfg.MaxQueuedReviews}
	}
	markQueued(job, time.Now())
	if _, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return "", ExistsError{name}
		}
		return "", err
	}
	for _, old := range stale {
		// A newer request for the same pull request replaces a queued one
		// that never started: reviewing the older head would only be
		// overtaken. A running review is never touched.
		if err := client.BatchV1().Jobs(cfg.Namespace).Delete(ctx, old, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)}); err != nil && !apierrors.IsNotFound(err) {
			slog.Warn("could not remove a superseded queued review", "logger", "bosun.queue", "job", old, "error", err)
			continue
		}
		slog.Info("queued review superseded", "logger", "bosun.queue", "job", old, "by", name)
	}
	slog.Info("review queued: all slots are busy", "logger", "bosun.queue",
		"job", name, "repo", job.Annotations["bosun/repository"], "pr", job.Labels[pullRequestLabel],
		"running", running, "max_concurrent", cfg.MaxConcurrentReviews, "position", queued-len(stale)+1)
	return name, nil
}

// tally counts Jobs holding a slot and Jobs waiting for one.
func tally(items []batchv1.Job) (running, queued int) {
	for i := range items {
		switch {
		case finished(&items[i]):
		case isQueued(&items[i]):
			queued++
		default:
			running++
		}
	}
	return running, queued
}

// superseded names the queued Jobs a new submission replaces: same repository
// and same pull request. Requests with no pull request never supersede, since
// two branch reviews are not known to be about the same thing.
func superseded(items []batchv1.Job, next *batchv1.Job) []string {
	pr, repo := next.Labels[pullRequestLabel], next.Labels[repositoryJobLabel]
	if pr == "" || repo == "" {
		return nil
	}
	var out []string
	for i := range items {
		j := &items[i]
		if isQueued(j) && !finished(j) && j.Name != next.Name && j.Labels[pullRequestLabel] == pr && j.Labels[repositoryJobLabel] == repo {
			out = append(out, j.Name)
		}
	}
	return out
}

func labelPullRequest(job *batchv1.Job, repo string, pr int) {
	job.Labels[repositoryJobLabel] = repositoryLabel(repo)
	if pr > 0 {
		job.Labels[pullRequestLabel] = strconv.Itoa(pr)
	}
}

// startShared releases a Job that reads the shared GitHub secret.
func startShared(client kubernetes.Interface, namespace string) starter {
	return func(ctx context.Context, job *batchv1.Job) error {
		if _, err := client.BatchV1().Jobs(namespace).Patch(ctx, job.Name, types.StrategicMergePatchType, unqueue, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("start review: %w", err)
		}
		return nil
	}
}

// mint issues the repository token a review will run with. It talks to
// GitHub, so it runs outside the admission lease and on a budget of its own:
// inside the lease it would spend the transaction's few seconds, and a slow
// mint would leave that transaction -- and its cleanup -- with nothing.
func mint(ctx context.Context, cfg config.Config, issue TokenIssuer, repo string) (review.RepositoryToken, error) {
	ctx, cancel := context.WithTimeout(ctx, mintBudget)
	defer cancel()
	token, err := issue(ctx, repo)
	if err != nil {
		return review.RepositoryToken{}, fmt.Errorf("issue repository token: %w", err)
	}
	if token.Value == "" {
		return review.RepositoryToken{}, fmt.Errorf("issue repository token: GitHub returned an empty token")
	}
	// A token that dies before the review does means paying for the whole
	// review and then failing to publish it. Refuse now, where it costs
	// nothing, rather than at the end.
	deadline := time.Duration(cfg.ReviewTimeoutSeconds+startupAllowanceSeconds) * time.Second
	if !token.ExpiresAt.IsZero() && time.Until(token.ExpiresAt) < deadline {
		return review.RepositoryToken{}, fmt.Errorf("the repository token expires in %s but this review may run for %s; lower review.timeoutSeconds", time.Until(token.ExpiresAt).Round(time.Minute), deadline)
	}
	return token, nil
}

// startWithToken stores an already-minted token in a Secret the Job owns, and
// only then releases the Job. Both are local API calls, safe under the lease.
func startWithToken(client kubernetes.Interface, namespace string, token review.RepositoryToken) starter {
	return func(ctx context.Context, job *batchv1.Job) error {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: job.Name + "-github", Namespace: namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "bosun"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
					Controller: ptr(true), BlockOwnerDeletion: ptr(true),
				}},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte(token.Value)},
		}
		// A retry after a failed release finds its own Secret already there,
		// holding the earlier token. Replace it so the review gets this one.
		if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
			if err := client.CoreV1().Secrets(namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("store repository token: %w", err)
			}
			if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("store repository token: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("store repository token: %w", err)
		}
		if _, err := client.BatchV1().Jobs(namespace).Patch(ctx, job.Name, types.StrategicMergePatchType, unqueue, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("start review: %w", err)
		}
		return nil
	}
}

// Dispatcher starts queued reviews as slots free up.
type Dispatcher struct {
	Client kubernetes.Interface
	Config config.Config
	// Issue mints per-repository tokens. Nil selects the shared-secret path.
	Issue TokenIssuer
	// Interval between passes. Zero means DefaultQueueInterval.
	Interval time.Duration
	// Now is the clock, replaceable in tests. Nil means time.Now.
	Now func() time.Time
}

// Run dispatches until the context is cancelled.
func (d Dispatcher) Run(ctx context.Context) {
	interval := d.Interval
	if interval <= 0 {
		interval = DefaultQueueInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slog.Info("review queue dispatcher started", "logger", "bosun.queue", "interval", interval.String(),
		"max_queued", d.Config.MaxQueuedReviews, "queue_timeout_seconds", d.Config.QueueTimeoutSeconds)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.Dispatch(ctx); err != nil && ctx.Err() == nil {
				slog.Error("review queue dispatch failed", "logger", "bosun.queue", "error", err)
			}
		}
	}
}

// Dispatch makes one pass in two halves. Outside the lease it picks the
// oldest queued reviews that look like they fit and mints each one's token on
// a budget of its own, so a repository whose mint is slow costs one budget
// and the next entry is tried with a fresh one. Under the lease it expires
// entries that waited too long and starts the prepared ones that still fit,
// using only local API calls.
func (d Dispatcher) Dispatch(ctx context.Context) error {
	ns := d.Config.Namespace
	list, err := d.Client.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun," + QueuedLabel + "=true"})
	if err != nil {
		return fmt.Errorf("list queued reviews: %w", err)
	}
	if len(list.Items) == 0 {
		return nil
	}
	running, err := Active(ctx, d.Client, ns)
	if err != nil {
		return err
	}
	queue := oldestFirst(list.Items)
	var ready []prepared
	for i := range queue {
		job := &queue[i]
		if running+len(ready) >= d.Config.MaxConcurrentReviews {
			break
		}
		if finished(job) || d.expired(job) {
			continue
		}
		start := startShared(d.Client, ns)
		if d.Issue != nil {
			token, err := mint(ctx, d.Config, d.Issue, job.Annotations["bosun/repository"])
			if err != nil {
				// Leave it queued and try the next: one repository must not
				// hold up every other review. The queue timeout bounds how
				// long a persistent failure retries.
				slog.Error("could not prepare a queued review; will retry", "logger", "bosun.queue",
					"job", job.Name, "repo", job.Annotations["bosun/repository"], "error", err)
				continue
			}
			start = startWithToken(d.Client, ns, token)
		}
		ready = append(ready, prepared{name: job.Name, start: start})
	}
	_, err = withAdmission(ctx, d.Client, ns, func(ctx context.Context) (string, error) {
		return "", d.startLocked(ctx, ready)
	})
	return err
}

type prepared struct {
	name  string
	start starter
}

func oldestFirst(items []batchv1.Job) []batchv1.Job {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := queuedAt(&items[i]), queuedAt(&items[j])
		if a.Equal(b) {
			return items[i].Name < items[j].Name
		}
		return a.Before(b)
	})
	return items
}

func (d Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d Dispatcher) expired(job *batchv1.Job) bool {
	timeout := time.Duration(d.Config.QueueTimeoutSeconds) * time.Second
	return timeout > 0 && d.now().Sub(queuedAt(job)) > timeout
}

func (d Dispatcher) startLocked(ctx context.Context, ready []prepared) error {
	ns := d.Config.Namespace
	list, err := d.Client.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun"})
	if err != nil {
		return fmt.Errorf("list reviews: %w", err)
	}
	queued := map[string]*batchv1.Job{}
	for i := range list.Items {
		job := &list.Items[i]
		if !isQueued(job) || finished(job) {
			continue
		}
		if d.expired(job) {
			if err := d.Client.BatchV1().Jobs(ns).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)}); err != nil && !apierrors.IsNotFound(err) {
				slog.Warn("could not remove an expired queued review", "logger", "bosun.queue", "job", job.Name, "error", err)
				continue
			}
			slog.Error("queued review expired before a slot freed; it will not run", "logger", "bosun.queue",
				"job", job.Name, "repo", job.Annotations["bosun/repository"], "pr", job.Labels[pullRequestLabel],
				"waited", d.now().Sub(queuedAt(job)).Round(time.Second).String(), "queue_timeout_seconds", d.Config.QueueTimeoutSeconds)
			continue
		}
		queued[job.Name] = job
	}
	running, _ := tally(list.Items)
	for _, p := range ready {
		if running >= d.Config.MaxConcurrentReviews {
			break
		}
		// Re-read under the lease: another replica may have started or
		// superseded it since it was prepared.
		job, ok := queued[p.name]
		if !ok {
			continue
		}
		if err := p.start(ctx, job); err != nil {
			slog.Error("could not start a queued review; will retry", "logger", "bosun.queue",
				"job", job.Name, "repo", job.Annotations["bosun/repository"], "error", err)
			continue
		}
		running++
		slog.Info("queued review started", "logger", "bosun.queue", "job", job.Name,
			"repo", job.Annotations["bosun/repository"], "pr", job.Labels[pullRequestLabel],
			"waited", d.now().Sub(queuedAt(job)).Round(time.Second).String())
	}
	return nil
}

// ForPullRequest lists the reviewer Jobs, in any state, for one pull request.
func ForPullRequest(ctx context.Context, client kubernetes.Interface, namespace, repo string, pr int) ([]batchv1.Job, error) {
	selector := "app.kubernetes.io/managed-by=bosun," + repositoryJobLabel + "=" + repositoryLabel(repo) + "," + pullRequestLabel + "=" + strconv.Itoa(pr)
	list, err := client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// ReviewedSHA is the commit a reviewer Job was submitted for, or "" when the
// trigger carried none and it was not resolved before submission.
func ReviewedSHA(job *batchv1.Job) string {
	for _, v := range job.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "BOSUN_SHA" {
			return v.Value
		}
	}
	return ""
}

// MintBudget bounds one repository-token mint. Minting talks to GitHub, so it
// runs outside the admission lease with a budget of its own.
const MintBudget = 10 * time.Second

// mintBudget is MintBudget, variable so tests can shorten it.
var mintBudget = MintBudget
