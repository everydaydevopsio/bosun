package jobs

import (
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	corev1 "k8s.io/api/core/v1"
)

func TestWorkerReceivesItsImageReferenceForSetupDiagnostics(t *testing.T) {
	image := "ghcr.io/everydaydevopsio/bosun@sha256:example"
	job := build(config.Config{ReviewImage: image, ReviewTimeoutSeconds: 60}, review.Request{Repo: "acme/widget"}, "review-test")
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
	got := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job").
		Spec.Template.Spec.Containers[0].Resources

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
	got := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job").
		Spec.Template.Spec.Containers[0].Resources
	if len(got.Requests) != 2 {
		t.Errorf("requests = %v, want cpu and memory", got.Requests)
	}
	if len(got.Limits) != 0 {
		t.Errorf("limits = %v, want none", got.Limits)
	}

	none := build(config.Config{Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800},
		review.Request{Repo: "owner/repo", Ref: "main"}, "job").
		Spec.Template.Spec.Containers[0].Resources
	if len(none.Requests) != 0 || len(none.Limits) != 0 {
		t.Errorf("unset config produced %v / %v, want an empty block", none.Requests, none.Limits)
	}
}

// A misspelt quantity drops that one entry rather than failing the review.
// Refusing to review because a limit has a typo trades a degraded review for
// no review at all.
func TestReviewerJobResourcesSkipUnparseableQuantities(t *testing.T) {
	cfg := config.Config{
		Namespace: "bosun", ReviewImage: "img", RunAsUser: 1001, ReviewTimeoutSeconds: 1800,
		ReviewResources: config.ReviewResources{CPURequest: "250m", MemoryRequest: "512 megabytes"},
	}
	got := build(cfg, review.Request{Repo: "owner/repo", Ref: "main"}, "job").
		Spec.Template.Spec.Containers[0].Resources
	if _, ok := got.Requests[corev1.ResourceCPU]; !ok {
		t.Error("a bad memory value discarded the valid cpu request")
	}
	if _, ok := got.Requests[corev1.ResourceMemory]; ok {
		t.Error("an unparseable memory value was accepted")
	}
}
