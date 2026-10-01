package jobs

import (
	"context"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLocalJobSecurityAndCredentialScope(t *testing.T) {
	c := fake.NewSimpleClientset()
	cfg := config.Load()
	cfg.Namespace = "bosun"
	name, e := SubmitLocal(context.Background(), c, cfg, review.Request{Repo: "repo", Ref: "main", SHA: "abc"}, "review.test", "base", true, 1000)
	if e != nil {
		t.Fatal(e)
	}
	job, e := c.BatchV1().Jobs("bosun").Get(context.Background(), name, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	security := job.Spec.Template.Spec.SecurityContext
	if security.RunAsUser == nil || *security.RunAsUser != 1001 || security.RunAsGroup == nil || *security.RunAsGroup != 1000 {
		t.Fatalf("invalid pod security: %+v", security)
	}
	var journal bool
	for _, env := range job.Spec.Template.Spec.Containers[0].Env {
		if env.Name == "GITHUB_TOKEN" || env.Name == "BOSUN_GITHUB_PRIVATE_KEY" {
			t.Fatal("GitHub credential in local job")
		}
		if env.Name == "BOSUN_EVENT_FILE" && env.Value == "/repos/review.test.events" {
			journal = true
		}
	}
	if !journal {
		t.Fatal("missing durable event journal")
	}
}
