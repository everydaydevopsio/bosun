package jobs

import (
	"context"
	"errors"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAuthenticatedJobHasOnlyRepositoryCredential(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := config.Config{Namespace: "bosun", MaxConcurrentReviews: 3, ReviewTimeoutSeconds: 1800, JobTTLSeconds: 3600, RunAsUser: 1001, GitHubTokenSecret: "app-signing-key", AISecret: "bosun-ai"}
	name, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "feature", PRNumber: 1}, "delivery", func(context.Context, string) (string, error) { return "test-repository-token", nil })
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.BatchV1().Jobs("bosun").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Suspend == nil || *job.Spec.Suspend {
		t.Fatal("job was not resumed")
	}
	c := job.Spec.Template.Spec.Containers[0]
	for _, e := range c.Env {
		if e.Name == "BOSUN_GITHUB_PRIVATE_KEY" || e.Name == "BOSUN_GITHUB_APP_ID" {
			t.Fatal("App credentials entered agent pod")
		}
		if e.Value == "test-repository-token" {
			t.Fatal("token stored in job spec")
		}
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == "app-signing-key" {
			t.Fatal("App secret referenced by pod")
		}
	}
	if c.Resources.Limits.Cpu().IsZero() || c.Resources.Limits.Memory().IsZero() {
		t.Fatal("missing resource limits")
	}
	secret, err := client.CoreV1().Secrets("bosun").Get(ctx, name+"-github", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["token"]) != "test-repository-token" || len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != name {
		t.Fatal("credential ownership incorrect")
	}
	_, err = SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "feature", PRNumber: 1}, "delivery", func(context.Context, string) (string, error) { t.Fatal("duplicate minted a token"); return "", nil })
	if _, ok := err.(ExistsError); !ok {
		t.Fatalf("expected duplicate, got %v", err)
	}
}

func TestTokenFailureCreatesNoJob(t *testing.T) {
	client := fake.NewSimpleClientset()
	_, err := SubmitAuthenticated(context.Background(), client, config.Config{Namespace: "bosun", MaxConcurrentReviews: 1}, review.Request{Repo: "owner/repo", Ref: "feature"}, "d", func(context.Context, string) (string, error) { return "", errors.New("denied") })
	if err == nil {
		t.Fatal("expected failure")
	}
	list, _ := client.BatchV1().Jobs("bosun").List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != 0 {
		t.Fatal("created a job without credentials")
	}
}
