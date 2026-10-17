package jobs

import (
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
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
