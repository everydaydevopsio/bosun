// Package sentinel reports reviewer Jobs that died without reporting
// themselves.
//
// A reviewer publishes its own failures from a deferred function in
// internal/review. That covers every error it can return, and nothing it
// cannot survive. An OOM kill is SIGKILL: no deferred function runs, the Job
// goes to Failed, its credential Secret is garbage-collected with it, and once
// the Job TTL expires the logs are gone too. The pull request is told nothing,
// and the absence is indistinguishable from a review that never ran.
//
// The reviewer cannot report its own SIGKILL, so the controller does. This is
// that part of the controller.
package sentinel

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// reportedAnnotation records that a Job's termination has been accounted for,
// whether that meant publishing a comment or finding one already there. It is
// the durable half of the publish-once guard: in-memory state does not survive
// a controller restart, and a restart must not re-report everything that
// failed within the Job TTL.
const reportedAnnotation = "bosun/termination-reported"

// DefaultInterval paces the reconcile. Reviews run for minutes and a Job lives
// for its TTL -- an hour by default -- so half a minute is prompt without
// making the API server carry a poll it does not need.
const DefaultInterval = 30 * time.Second

// clockSkewMargin is subtracted from a Job's start time before it is used as
// the lower bound for "was this review already published?".
//
// The bound exists so an earlier attempt's publication cannot suppress this
// one. It is pushed slightly early because the two clocks being compared are
// the cluster's and GitHub's: if GitHub's is behind, this reviewer's own
// publication could be stamped just before the Job started, fall outside the
// bound, and be contradicted. Missing our own publication is the worse
// failure, so the margin errs that way.
const clockSkewMargin = 30 * time.Second

// Sentinel watches reviewer Jobs for deaths the reviewer could not report.
type Sentinel struct {
	Client kubernetes.Interface
	Config config.Config
	// Publish reports one termination and says whether it published. False
	// means the report was unnecessary because Bosun had already published for
	// that commit -- a resolved state, not a failure.
	Publish func(context.Context, review.Termination) (bool, error)
	// Lock serializes one report across controller replicas. Nil runs the
	// report directly, which is correct for a single replica.
	Lock func(context.Context, func(context.Context) error) error
	// Interval between reconciles. Zero means DefaultInterval.
	Interval time.Duration
}

// Run reconciles until the context is cancelled.
func (s Sentinel) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	slog.Info("termination sentinel started", "logger", "bosun.sentinel", "interval", interval.String())
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Reconcile(ctx); err != nil && ctx.Err() == nil {
				// A reconcile failure is transient by construction: the next
				// tick retries every Job it did not record.
				slog.Warn("termination sentinel pass incomplete", "logger", "bosun.sentinel", "error", err)
			}
		}
	}
}

// Reconcile makes one pass over the namespace's reviewer Jobs.
//
// It returns an error if any termination could not be reported, having still
// attempted the rest: one unreachable pull request must not stop the others
// from being told.
func (s Sentinel) Reconcile(ctx context.Context) error {
	list, err := s.Client.BatchV1().Jobs(s.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun"})
	if err != nil {
		return fmt.Errorf("list reviewer jobs: %w", err)
	}
	var failures []error
	for i := range list.Items {
		if err := s.inspect(ctx, &list.Items[i]); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 1 {
		return failures[0]
	}
	if len(failures) > 1 {
		return fmt.Errorf("%d terminations could not be reported, first: %w", len(failures), failures[0])
	}
	return nil
}

// inspect examines one Job, recovering from a panic rather than letting it
// reach the goroutine Run started. The sentinel shares a process with the
// webhook server that submits every review: a panic here -- client-go raises
// one outright for a label selector it cannot parse -- would stop reviews
// being accepted at all, trading the primary feature for a secondary one.
func (s Sentinel) inspect(ctx context.Context, job *batchv1.Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("inspecting %s panicked: %v", job.Name, r)
		}
	}()
	return s.inspectJob(ctx, job)
}

func (s Sentinel) inspectJob(ctx context.Context, job *batchv1.Job) error {
	if job.Annotations[reportedAnnotation] != "" {
		return nil
	}
	// A local review has no pull request, and its CLI watches the pod itself.
	if job.Labels["bosun/local"] == "true" {
		return nil
	}
	if !failed(job) {
		return nil
	}
	cause, err := s.cause(ctx, job)
	if err != nil {
		return err
	}
	if cause == "" {
		// Either the reviewer exited on its own -- in which case it already
		// published -- or the pods are gone and there is nothing left to read.
		return nil
	}
	term := review.Termination{
		Since:    startedAt(job),
		Repo:     env(job, "BOSUN_REPO"),
		Ref:      env(job, "BOSUN_REF"),
		SHA:      env(job, "BOSUN_SHA"),
		PRNumber: number(env(job, "BOSUN_PR_NUMBER")),
		Cause:    cause,
	}
	// A review whose Job never recorded a commit cannot be correlated. The
	// comment would name no commit, and the guard that keeps the
	// one-publication invariant works by matching the metadata line, so it
	// would also fail to recognise the reviewer's own publication and
	// contradict a review that succeeded. Submission resolves this identity
	// before the Job is created; if that did not happen, silence is the
	// smaller failure.
	if term.SHA == "" {
		slog.Warn("reviewer terminated before its review identity was resolved; cannot correlate a report", "logger", "bosun.sentinel",
			"job", job.Name, "repo", term.Repo, "ref", term.Ref, "pr", term.PRNumber, "cause", cause)
		return s.record(ctx, job, "unresolved-identity")
	}
	if term.PRNumber <= 0 {
		// A branch-created review has nowhere to publish. Record it so the
		// sentinel stops looking, and say so once: a silent drop behind a
		// Failed Job is the part that makes this hard to diagnose. The
		// discarded review itself is #65.
		slog.Warn("reviewer terminated with no pull request to report to", "logger", "bosun.sentinel",
			"job", job.Name, "repo", term.Repo, "ref", term.Ref, "cause", cause)
		return s.record(ctx, job, "no-pull-request")
	}
	var published bool
	report := func(ctx context.Context) error {
		var err error
		published, err = s.Publish(ctx, term)
		return err
	}
	// Under the lock the whole report is one critical section: another replica
	// must not slip between "has Bosun already published?" and the comment
	// that answers it.
	if s.Lock != nil {
		err = s.Lock(ctx, report)
	} else {
		err = report(ctx)
	}
	if err != nil {
		// Leave the Job unrecorded: a transient GitHub failure, or a lease
		// held by another replica, must stay retryable or it becomes the
		// permanent silence this exists to fix.
		return fmt.Errorf("report termination of %s: %w", job.Name, err)
	}
	outcome := "already-published"
	if published {
		outcome = "published"
		slog.Info("reported a reviewer termination the reviewer could not report", "logger", "bosun.sentinel",
			"job", job.Name, "repo", term.Repo, "pr", term.PRNumber, "sha", term.SHA)
	}
	return s.record(ctx, job, outcome)
}

// record marks the Job so no later pass, and no restarted controller, reports
// it again. Patch rather than Update: the object in hand came from a List and
// the job controller writes status while this runs, so an Update would carry a
// stale resourceVersion and conflict.
func (s Sentinel) record(ctx context.Context, job *batchv1.Job, outcome string) error {
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, reportedAnnotation, outcome)
	_, err := s.Client.BatchV1().Jobs(s.Config.Namespace).Patch(ctx, job.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("record termination of %s: %w", job.Name, err)
	}
	return nil
}

// cause classifies a failed Job as a termination the reviewer could not have
// reported, or as nothing to do.
//
// Only reasons that are provably unsurvivable belong here. A reviewer that
// fails gracefully also exits non-zero, having already published its own
// failure comment; reporting that would contradict it with a second comment
// and break the one-terminal-publication invariant. OOMKilled is the case
// #60's memory limit made reachable by configuration rather than by luck.
func (s Sentinel) cause(ctx context.Context, job *batchv1.Job) (string, error) {
	pods, err := s.pods(ctx, job.Name)
	if err != nil {
		return "", err
	}
	for _, pod := range pods {
		statuses := append(append([]corev1.ContainerStatus{}, pod.Status.ContainerStatuses...), pod.Status.InitContainerStatuses...)
		for _, c := range statuses {
			// LastTerminationState as well as State: a pod the kubelet has
			// begun recycling reports the kill it is replacing there.
			for _, state := range []*corev1.ContainerStateTerminated{c.State.Terminated, c.LastTerminationState.Terminated} {
				if state != nil && state.Reason == "OOMKilled" {
					// Checked before the deadline below: both can be true of
					// one Job, and memory is the cause an operator can act on.
					return review.OutOfMemoryCause(s.Config.ReviewResources.MemoryLimit), nil
				}
			}
		}
	}
	// A deadline kill is a SIGTERM, so the container reason is "Error" --
	// indistinguishable from a reviewer that exited non-zero on its own, which
	// has already published. Only the Job condition separates them.
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && c.Reason == "DeadlineExceeded" {
			return review.DeadlineExceededCause(s.Config.ReviewTimeoutSeconds), nil
		}
	}
	return "", nil
}

// pods finds a Job's pods under either spelling of the label. Kubernetes 1.27
// introduced batch.kubernetes.io/job-name and kept the bare job-name
// alongside it; a cluster below that only carries the old one, and a sentinel
// that cannot find the pod cannot read why it died.
func (s Sentinel) pods(ctx context.Context, name string) ([]corev1.Pod, error) {
	for _, selector := range []string{"batch.kubernetes.io/job-name=" + name, "job-name=" + name} {
		list, err := s.Client.CoreV1().Pods(s.Config.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return nil, fmt.Errorf("list pods for %s: %w", name, err)
		}
		if len(list.Items) > 0 {
			return list.Items, nil
		}
	}
	return nil, nil
}

func failed(job *batchv1.Job) bool {
	if job.Status.Failed > 0 {
		return true
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// startedAt is the lower bound for publications belonging to this review. A
// Job with no recorded start time yields the zero time, which disables the
// bound and leaves the commit correlation the guard had before.
func startedAt(job *batchv1.Job) time.Time {
	if job.Status.StartTime == nil {
		return time.Time{}
	}
	return job.Status.StartTime.Time.Add(-clockSkewMargin)
}

func env(job *batchv1.Job, name string) string {
	containers := job.Spec.Template.Spec.Containers
	if len(containers) == 0 {
		return ""
	}
	for _, v := range containers[0].Env {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}

func number(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return n
}
