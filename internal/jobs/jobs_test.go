package jobs

import (
	"context"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestWorkerReceivesItsImageReferenceForSetupDiagnostics(t *testing.T) {
	image := "ghcr.io/everydaydevopsio/bosun@sha256:example"
	job, err := build(config.Config{ReviewImage: image, ReviewTimeoutSeconds: 60}, review.Request{Repo: "acme/widget"}, "review-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, variable := range job.Spec.Template.Spec.Containers[0].Env {
		if variable.Name == "BOSUN_WORKER_IMAGE" {
			if variable.Value != image {
				t.Fatalf("worker image = %q, want %q", variable.Value, image)
			}
			return
		}
	}
	t.Fatal("worker image reference absent from reviewer environment")
}

func TestNameIsStableSafeAndUnique(t *testing.T) {
	a := Name("Acme/Widget", "Feature/Some_Branch", "delivery")
	b := Name("Acme/Widget", "Feature/Some_Branch", "delivery")
	c := Name("Acme/Widget", "Feature/Some_Branch", "other")
	if a != b || a == c || len(a) > MaxName {
		t.Fatalf("bad names %q %q %q", a, b, c)
	}
}

// Without requests the reviewer is BestEffort: the scheduler cannot tell
// whether another concurrent review fits, and the kubelet has no basis to
// evict the reviewer ahead of the controller that launched it. Issue #29.
func TestReviewerJobCarriesResources(t *testing.T) {
	cfg := config.Config{
		Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800,
		ReviewResources: config.ReviewResources{
			CPURequest: "250m", MemoryRequest: "512Mi", CPULimit: "1", MemoryLimit: "1Gi",
		},
	}
	job, err := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job")
	if err != nil {
		t.Fatal(err)
	}
	got := job.Spec.Template.Spec.Containers[0].Resources

	for _, c := range []struct {
		list corev1.ResourceList
		name corev1.ResourceName
		want string
	}{
		{got.Requests, corev1.ResourceCPU, "250m"},
		{got.Requests, corev1.ResourceMemory, "512Mi"},
		{got.Limits, corev1.ResourceCPU, "1"},
		{got.Limits, corev1.ResourceMemory, "1Gi"},
	} {
		q, ok := c.list[c.name]
		if !ok {
			t.Errorf("%s missing", c.name)
			continue
		}
		if q.String() != c.want {
			t.Errorf("%s = %s, want %s", c.name, q.String(), c.want)
		}
	}
}

// An operator dropping a limit must not lose the request with it, and a Job
// with nothing configured must still be submittable.
func TestReviewerJobResourcesOmitEmptyEntries(t *testing.T) {
	cfg := config.Config{
		Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800,
		ReviewResources: config.ReviewResources{CPURequest: "100m", MemoryRequest: "256Mi"},
	}
	job, err := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job")
	if err != nil {
		t.Fatal(err)
	}
	got := job.Spec.Template.Spec.Containers[0].Resources
	if len(got.Requests) != 2 {
		t.Errorf("requests = %v, want cpu and memory", got.Requests)
	}
	if len(got.Limits) != 0 {
		t.Errorf("limits = %v, want none", got.Limits)
	}

	unset, err := build(config.Config{Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800},
		review.Request{Repo: "owner/repo", Ref: "main"}, "job")
	if err != nil {
		t.Fatal(err)
	}
	none := unset.Spec.Template.Spec.Containers[0].Resources
	if len(none.Requests) != 0 || len(none.Limits) != 0 {
		t.Errorf("unset config produced %v / %v, want an empty block", none.Requests, none.Limits)
	}
}

// A misspelt quantity is refused rather than dropped. Dropping it would leave
// a Job with no limit where the operator configured one -- the unbounded
// reviewer this feature exists to prevent -- and do it silently, so the
// protection reads as configured while being absent.
func TestReviewerJobResourcesRejectUnparseableQuantities(t *testing.T) {
	for _, tc := range []struct {
		name  string
		res   config.ReviewResources
		field string
	}{
		{"memory limit", config.ReviewResources{CPURequest: "250m", MemoryLimit: "1GiB"}, "review.resources.limits.memory"},
		{"cpu request", config.ReviewResources{CPURequest: "half a core"}, "review.resources.requests.cpu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800, ReviewResources: tc.res}
			job, err := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job")
			if err == nil {
				t.Fatal("an unparseable quantity was accepted")
			}
			if job != nil {
				t.Error("a Job was built despite the error")
			}
			// The operator has to know which value to fix.
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name the field", err)
			}
		})
	}
}

// A submission must not reach Kubernetes when the configuration is invalid.
func TestSubmitRefusesInvalidResourceConfiguration(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := config.Config{
		Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800,
		MaxConcurrentReviews: 3, ReviewResources: config.ReviewResources{MemoryRequest: "512 megabytes"},
	}
	if _, err := Submit(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "d"); err == nil {
		t.Fatal("expected a refusal")
	}
	jobs, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("a Job was created despite invalid resource configuration: %d", len(jobs.Items))
	}
}
