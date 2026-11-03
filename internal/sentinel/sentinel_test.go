package sentinel

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	namespace = "bosun"
	headSHA   = "0123456789abcdef0123456789abcdef01234567"
)

func testConfig() config.Config {
	return config.Config{
		Namespace: namespace, ReviewImage: "ghcr.io/everydaydevopsio/bosun:test",
		ReviewProvider: "codex-bosun", GitHubTokenSecret: "bosun-github", AISecret: "bosun-ai",
		JobTTLSeconds: 3600, ReviewTimeoutSeconds: 1800, MaxConcurrentReviews: 3, RunAsUser: 1001,
		ReviewResources: config.ReviewResources{MemoryLimit: "1Gi"},
	}
}

// recorder captures what the sentinel decided to publish.
type recorder struct {
	seen      []review.Termination
	published bool
	err       error
}

func (r *recorder) publish(_ context.Context, t review.Termination) (bool, error) {
	r.seen = append(r.seen, t)
	return r.published, r.err
}

func sentinelFor(client kubernetes.Interface, rec *recorder) Sentinel {
	return Sentinel{Client: client, Config: testConfig(), Publish: rec.publish}
}

// oomPod is the pod a kubelet leaves behind when it kills a container for
// exceeding its memory limit.
func oomPod(jobName string, reason string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: jobName + "-abcde", Namespace: namespace,
			Labels: map[string]string{"batch.kubernetes.io/job-name": jobName},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodFailed,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "reviewer",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: 137}},
			}},
		},
	}
}

func failedJob(name string, prNumber string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: namespace,
			Labels:      map[string]string{"app.kubernetes.io/managed-by": "bosun"},
			Annotations: map[string]string{"bosun/repository": "acme/widget"},
		},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "reviewer", Env: []corev1.EnvVar{
				{Name: "BOSUN_REPO", Value: "acme/widget"},
				{Name: "BOSUN_REF", Value: "feature"},
				{Name: "BOSUN_SHA", Value: headSHA},
				{Name: "BOSUN_PR_NUMBER", Value: prNumber},
			}}},
		}}},
		Status: batchv1.JobStatus{Failed: 1},
	}
}

func annotation(t *testing.T, client kubernetes.Interface, name, key string) string {
	t.Helper()
	job, err := client.BatchV1().Jobs(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return job.Annotations[key]
}

// The headline case from #62: the reviewer is SIGKILLed, so its own deferred
// publication never runs and the pull request is told nothing at all.
func TestOOMKilledReviewerIsReportedOnce(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-widget-feature-0123456789", "7"), oomPod("review-widget-feature-0123456789", "OOMKilled"))
	rec := &recorder{published: true}
	s := sentinelFor(client, rec)

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("want one termination reported, got %d", len(rec.seen))
	}
	got := rec.seen[0]
	if got.Repo != "acme/widget" || got.PRNumber != 7 || got.Ref != "feature" || got.SHA != headSHA {
		t.Errorf("termination does not identify the review: %+v", got)
	}
	if got.Cause != review.OutOfMemoryCause("1Gi") {
		t.Errorf("cause does not name the configured memory limit: %q", got.Cause)
	}
	if annotation(t, client, "review-widget-feature-0123456789", reportedAnnotation) == "" {
		t.Error("a reported termination was not recorded on the Job, so the next pass will report it again")
	}

	// A second pass must be silent: the annotation is what stops a reconcile
	// loop from posting the same comment every interval.
	rec.seen = nil
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(rec.seen) != 0 {
		t.Errorf("reported the same termination twice: %+v", rec.seen)
	}
}

// Everything the reviewer can report, it already reports from its own deferred
// publication. Acting on those would post a second comment contradicting the
// first.
func TestTerminationsTheReviewerReportsItselfAreLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pod     *corev1.Pod
		mutate  func(*batchv1.Job)
		jobName string
	}{
		{name: "a graceful non-zero exit", jobName: "review-a", pod: oomPod("review-a", "Error")},
		{name: "a completed container", jobName: "review-b", pod: oomPod("review-b", "Completed")},
		{
			name: "a job that succeeded", jobName: "review-c", pod: oomPod("review-c", "OOMKilled"),
			mutate: func(j *batchv1.Job) { j.Status = batchv1.JobStatus{Succeeded: 1} },
		},
		{
			name: "a job still running", jobName: "review-d", pod: oomPod("review-d", "OOMKilled"),
			mutate: func(j *batchv1.Job) { j.Status = batchv1.JobStatus{Active: 1} },
		},
		{
			name: "a local review with no pull request", jobName: "review-e", pod: oomPod("review-e", "OOMKilled"),
			mutate: func(j *batchv1.Job) { j.Labels["bosun/local"] = "true" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := failedJob(tc.jobName, "7")
			if tc.mutate != nil {
				tc.mutate(job)
			}
			client := fake.NewSimpleClientset(job, tc.pod)
			rec := &recorder{published: true}
			if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if len(rec.seen) != 0 {
				t.Errorf("reported a termination that needed no report: %+v", rec.seen)
			}
		})
	}
}

// A branch-created review has no pull request to post to. That review being
// discarded is #65; the sentinel must not invent a destination for it.
func TestOOMWithoutAPullRequestIsRecordedButNotPublished(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-nopr", "0"), oomPod("review-nopr", "OOMKilled"))
	rec := &recorder{published: true}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 0 {
		t.Errorf("published a termination with no pull request: %+v", rec.seen)
	}
}

// A failed publish is the one case that must stay retryable: recording it
// would turn a transient GitHub error into permanent silence.
func TestAFailedPublishIsRetriedOnTheNextPass(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-retry", "7"), oomPod("review-retry", "OOMKilled"))
	rec := &recorder{err: errors.New("GitHub POST returned HTTP 502")}
	s := sentinelFor(client, rec)

	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("a failed publish was reported as a clean reconcile")
	}
	if annotation(t, client, "review-retry", reportedAnnotation) != "" {
		t.Fatal("a failed publish was recorded as reported, so it will never be retried")
	}

	rec.err, rec.published = nil, true
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(rec.seen) != 2 {
		t.Errorf("want the termination retried, got %d attempts", len(rec.seen))
	}
	if annotation(t, client, "review-retry", reportedAnnotation) == "" {
		t.Error("the successful retry was not recorded")
	}
}

// Bosun having already published for this commit is a resolved state, not a
// failure: record it so the sentinel stops asking GitHub about it.
func TestAnAlreadyPublishedCommitIsRecordedWithoutPosting(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-dup", "7"), oomPod("review-dup", "OOMKilled"))
	rec := &recorder{published: false}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if annotation(t, client, "review-dup", reportedAnnotation) == "" {
		t.Error("a termination already covered by an existing publication was not recorded")
	}
}

// The sentinel reads the review's identity out of the Job the submitter built.
// Building that Job through the real submission path is what keeps the two
// from drifting apart when the environment contract changes.
func TestTheSentinelReadsAJobBuiltBySubmission(t *testing.T) {
	client := fake.NewSimpleClientset()
	cfg := testConfig()
	req := review.Request{Repo: "acme/widget", Ref: "feature", SHA: headSHA, PRNumber: 7, Trigger: "pull-request-synchronize"}
	name, err := jobs.Submit(context.Background(), client, cfg, req, "delivery-1")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	job, err := client.BatchV1().Jobs(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	job.Status.Failed = 1
	if _, err := client.BatchV1().Jobs(namespace).Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update job: %v", err)
	}
	if _, err := client.CoreV1().Pods(namespace).Create(context.Background(), oomPod(name, "OOMKilled"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	rec := &recorder{published: true}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("want one termination from a real submitted Job, got %d", len(rec.seen))
	}
	got := rec.seen[0]
	if got.Repo != req.Repo || got.PRNumber != req.PRNumber || got.Ref != req.Ref || got.SHA != req.SHA {
		t.Errorf("sentinel misread a Job built by Submit: %+v", got)
	}
}

// Kubernetes renamed the Job's pod label; clusters below 1.27 only carry the
// old one, and a sentinel that cannot find the pod cannot read the reason.
func TestPodsAreFoundByTheLegacyJobLabel(t *testing.T) {
	pod := oomPod("review-legacy", "OOMKilled")
	pod.Labels = map[string]string{"job-name": "review-legacy"}
	client := fake.NewSimpleClientset(failedJob("review-legacy", "7"), pod)
	rec := &recorder{published: true}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("a pod labelled only with the legacy job-name was not found: %d", len(rec.seen))
	}
}

// Two controller replicas both reconciling can both pass the "has Bosun
// already published?" check in the same instant. The lease is what stops that
// race from producing two comments.
func TestPublishingRunsUnderTheLock(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-locked", "7"), oomPod("review-locked", "OOMKilled"))
	rec := &recorder{published: true}
	held := 0
	s := sentinelFor(client, rec)
	s.Lock = func(ctx context.Context, op func(context.Context) error) error {
		held++
		return op(ctx)
	}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if held != 1 {
		t.Errorf("want the publish serialized once, got %d", held)
	}
	if len(rec.seen) != 1 {
		t.Errorf("the lock swallowed the publish: %+v", rec.seen)
	}
}

// Losing the lease means another replica may be publishing right now. Treat it
// as a retryable failure, not as a termination that has been handled.
func TestAnUnavailableLockLeavesTheTerminationUnrecorded(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-nolock", "7"), oomPod("review-nolock", "OOMKilled"))
	rec := &recorder{published: true}
	s := sentinelFor(client, rec)
	s.Lock = func(context.Context, func(context.Context) error) error {
		return errors.New("could not acquire the bosun-termination-sentinel lease")
	}
	if err := s.Reconcile(context.Background()); err == nil {
		t.Fatal("a lost lease was reported as a clean reconcile")
	}
	if len(rec.seen) != 0 {
		t.Errorf("published without holding the lease: %+v", rec.seen)
	}
	if annotation(t, client, "review-nolock", reportedAnnotation) != "" {
		t.Error("recorded a termination that was never reported")
	}
}

// The acceptance criterion from #62, end to end: an OOM-killed reviewer Job
// results in exactly one publication on the pull request, and that publication
// names memory as the cause. The two halves are unit-tested separately; this
// drives the real publisher so a mismatch between them cannot pass.
func TestAnOOMKilledReviewProducesExactlyOnePublication(t *testing.T) {
	var posted []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
			// Everything published so far, so a second pass sees the first.
			out := make([]map[string]string, 0, len(posted))
			if r.URL.Query().Get("page") == "1" {
				for _, b := range posted {
					out = append(out, map[string]string{"body": b})
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/7/reviews"):
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/issues/7/comments"):
			var body struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			posted = append(posted, body.Body)
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	client := fake.NewSimpleClientset(failedJob("review-e2e", "7"), oomPod("review-e2e", "OOMKilled"))
	s := Sentinel{Client: client, Config: testConfig(), Publish: func(ctx context.Context, term review.Termination) (bool, error) {
		return review.PublishTermination(ctx, api.URL, api.Client(), "test-token", term)
	}}

	// Two passes, because the guard that matters is the one that holds on the
	// second: a reconcile loop runs every interval for the Job's whole TTL.
	for pass := 1; pass <= 2; pass++ {
		if err := s.Reconcile(context.Background()); err != nil {
			t.Fatalf("Reconcile pass %d: %v", pass, err)
		}
	}
	if len(posted) != 1 {
		t.Fatalf("want exactly one publication, got %d: %q", len(posted), posted)
	}

	// The two guards have to hold independently. Strip the Job's record and
	// reconcile again: with the local guard gone, only the check against what
	// is already on the pull request can prevent a second comment. That is the
	// guard that covers what Kubernetes cannot show -- a reviewer that
	// published its review and was killed afterwards.
	job, err := client.BatchV1().Jobs(namespace).Get(context.Background(), "review-e2e", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	delete(job.Annotations, reportedAnnotation)
	if _, err := client.BatchV1().Jobs(namespace).Update(context.Background(), job, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update job: %v", err)
	}
	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile without the local record: %v", err)
	}
	if len(posted) != 1 {
		t.Fatalf("the pull request was told twice once the Job record was lost: %q", posted)
	}

	for _, want := range []string{"## Bosun code review failed", "`feature` @ `" + headSHA + "`", "memory", "review.resources.limits.memory", "1Gi"} {
		if !strings.Contains(posted[0], want) {
			t.Errorf("the published comment omits %q:\n%s", want, posted[0])
		}
	}
}

// Run is what the controller actually starts. It must reconcile on its
// interval and stop when the process is shutting down, or a drain waits on a
// goroutine that never returns.
func TestRunReconcilesOnItsIntervalAndStopsOnCancel(t *testing.T) {
	client := fake.NewSimpleClientset(failedJob("review-run", "7"), oomPod("review-run", "OOMKilled"))
	rec := &recorder{published: true}
	s := sentinelFor(client, rec)
	s.Interval = time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); s.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for annotation(t, client, "review-run", reportedAnnotation) == "" {
		select {
		case <-deadline:
			t.Fatal("Run never reconciled")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
	if len(rec.seen) != 1 {
		t.Errorf("a repeating loop reported the same termination %d times", len(rec.seen))
	}
}

// A zero interval is the documented opt-out, and it must not become a busy
// loop or a ticker panic.
func TestRunWithoutAnIntervalUsesTheDefault(t *testing.T) {
	client := fake.NewSimpleClientset()
	rec := &recorder{published: true}
	s := sentinelFor(client, rec)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { defer close(stopped); s.Run(ctx) }()
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return when its context was cancelled")
	}
}

// A comment-triggered Job is created with BOSUN_REF="pr-N" and no commit: the
// reviewer resolves the real branch and head itself, in memory. The sentinel
// reads the Job, so it never sees that resolution.
//
// Reporting from the unresolved identity is worse than not reporting. The
// comment would name no commit, and -- because the guard correlates by
// metadata line -- it would fail to recognise the reviewer's own publication
// and post a failure comment contradicting a review that succeeded.
func TestAnUnresolvedReviewIdentityIsNotReported(t *testing.T) {
	job := failedJob("review-bosun-pr-73", "73")
	for i, v := range job.Spec.Template.Spec.Containers[0].Env {
		switch v.Name {
		case "BOSUN_REF":
			job.Spec.Template.Spec.Containers[0].Env[i].Value = "pr-73"
		case "BOSUN_SHA":
			job.Spec.Template.Spec.Containers[0].Env[i].Value = ""
		}
	}
	client := fake.NewSimpleClientset(job, oomPod("review-bosun-pr-73", "OOMKilled"))
	rec := &recorder{published: true}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 0 {
		t.Fatalf("reported a termination that could not name the commit it reviewed: %+v", rec.seen)
	}
	if annotation(t, client, "review-bosun-pr-73", reportedAnnotation) == "" {
		t.Error("an unreportable termination was left to be retried every interval forever")
	}
}

// The other half of the same fix: once submission has resolved a
// comment-triggered review, its Job records a real branch and commit, and the
// termination becomes reportable under the identity the reviewer would have
// published under.
func TestAResolvedCommentTriggeredReviewIsReportable(t *testing.T) {
	job := failedJob("review-bosun-pr-73-resolved", "73")
	for i, v := range job.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "BOSUN_TRIGGER" {
			job.Spec.Template.Spec.Containers[0].Env[i].Value = "comment"
		}
	}
	// Ref and SHA are already the resolved head, as resolveIdentity leaves them.
	client := fake.NewSimpleClientset(job, oomPod("review-bosun-pr-73-resolved", "OOMKilled"))
	rec := &recorder{published: true}
	if err := sentinelFor(client, rec).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rec.seen) != 1 {
		t.Fatalf("a resolved comment-triggered termination was not reported: %d", len(rec.seen))
	}
	if got := rec.seen[0]; got.Ref != "feature" || got.SHA != headSHA {
		t.Errorf("reported under the wrong identity: %+v", got)
	}
}
