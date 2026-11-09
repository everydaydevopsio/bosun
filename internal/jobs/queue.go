package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
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
		job.Spec.Suspend = ptr(true)
		created, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return "", ExistsError{name}
		}
		if err != nil {
			return "", err
		}
		if err := start(ctx, created); err != nil {
			_ = client.BatchV1().Jobs(cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)})
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

// startAuthenticated mints the Job's repository token, stores it in a Secret
// the Job owns, and only then releases the Job. Minting at start rather than
// at submission means a review that waited in the queue still gets a token
// with its full lifetime ahead of it.
func startAuthenticated(client kubernetes.Interface, cfg config.Config, issue TokenIssuer) starter {
	return func(ctx context.Context, job *batchv1.Job) error {
		repo := job.Annotations["bosun/repository"]
		token, err := issue(ctx, repo)
		if err != nil {
			return fmt.Errorf("issue repository token: %w", err)
		}
		if token.Value == "" {
			return fmt.Errorf("issue repository token: GitHub returned an empty token")
		}
		// A token that dies before the review does means paying for the whole
		// review and then failing to publish it. Refuse now, where it costs
		// nothing, rather than at the end.
		deadline := time.Duration(cfg.ReviewTimeoutSeconds+startupAllowanceSeconds) * time.Second
		if !token.ExpiresAt.IsZero() && time.Until(token.ExpiresAt) < deadline {
			return fmt.Errorf("the repository token expires in %s but this review may run for %s; lower review.timeoutSeconds", time.Until(token.ExpiresAt).Round(time.Minute), deadline)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: job.Name + "-github", Namespace: cfg.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "bosun"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID,
					Controller: ptr(true), BlockOwnerDeletion: ptr(true),
				}},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte(token.Value)},
		}
		// A retry after a failed patch finds its own Secret already there.
		// The token in it is the earlier one; replace it so the review gets
		// the full lifetime.
		if _, err := client.CoreV1().Secrets(cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{}); apierrors.IsAlreadyExists(err) {
			if err := client.CoreV1().Secrets(cfg.Namespace).Delete(ctx, secret.Name, metav1.DeleteOptions{}); err != nil {
				return fmt.Errorf("store repository token: %w", err)
			}
			if _, err := client.CoreV1().Secrets(cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
				return fmt.Errorf("store repository token: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("store repository token: %w", err)
		}
		if _, err := client.BatchV1().Jobs(cfg.Namespace).Patch(ctx, job.Name, types.StrategicMergePatchType, unqueue, metav1.PatchOptions{}); err != nil {
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

// Dispatch makes one pass: expire entries that waited too long, then start the
// oldest queued Jobs while slots remain.
func (d Dispatcher) Dispatch(ctx context.Context) error {
	_, err := withAdmission(ctx, d.Client, d.Config.Namespace, func(ctx context.Context) (string, error) {
		return "", d.dispatchLocked(ctx)
	})
	return err
}

func (d Dispatcher) dispatchLocked(ctx context.Context) error {
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
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
	queue := list.Items
	sort.SliceStable(queue, func(i, j int) bool {
		a, b := queuedAt(&queue[i]), queuedAt(&queue[j])
		if a.Equal(b) {
			return queue[i].Name < queue[j].Name
		}
		return a.Before(b)
	})
	start := startShared(d.Client, ns)
	if d.Issue != nil {
		start = startAuthenticated(d.Client, d.Config, d.Issue)
	}
	timeout := time.Duration(d.Config.QueueTimeoutSeconds) * time.Second
	for i := range queue {
		job := &queue[i]
		if finished(job) {
			continue
		}
		waited := now().Sub(queuedAt(job))
		if timeout > 0 && waited > timeout {
			if err := d.Client.BatchV1().Jobs(ns).Delete(ctx, job.Name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)}); err != nil && !apierrors.IsNotFound(err) {
				slog.Warn("could not remove an expired queued review", "logger", "bosun.queue", "job", job.Name, "error", err)
				continue
			}
			slog.Error("queued review expired before a slot freed; it will not run", "logger", "bosun.queue",
				"job", job.Name, "repo", job.Annotations["bosun/repository"], "pr", job.Labels[pullRequestLabel],
				"waited", waited.Round(time.Second).String(), "queue_timeout_seconds", d.Config.QueueTimeoutSeconds)
			continue
		}
		if running >= d.Config.MaxConcurrentReviews {
			continue
		}
		if err := start(ctx, job); err != nil {
			// Leave it queued and try the next: a failure minting one
			// repository's token must not hold up every other review. The
			// queue timeout bounds how long a persistent failure retries.
			slog.Error("could not start a queued review; will retry", "logger", "bosun.queue",
				"job", job.Name, "repo", job.Annotations["bosun/repository"], "error", err)
			continue
		}
		running++
		slog.Info("queued review started", "logger", "bosun.queue", "job", job.Name,
			"repo", job.Annotations["bosun/repository"], "pr", job.Labels[pullRequestLabel], "waited", waited.Round(time.Second).String())
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
