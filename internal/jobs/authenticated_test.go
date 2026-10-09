package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func authConfig() config.Config {
	return config.Config{
		Namespace: "bosun", MaxConcurrentReviews: 3, ReviewTimeoutSeconds: 1800,
		ReviewProvider: "codex-bosun", GitHubTokenSecret: "bosun-github", AISecret: "bosun-ai",
		ReviewImage: "ghcr.io/everydaydevopsio/bosun:1", RunAsUser: 1001,
	}
}

func issuer(value string, lifetime time.Duration) TokenIssuer {
	return func(context.Context, string) (review.RepositoryToken, error) {
		return review.RepositoryToken{Value: value, ExpiresAt: time.Now().Add(lifetime)}, nil
	}
}

// The App private key can mint a token for every repository the App is
// installed on. A reviewer Job reads attacker-supplied pull request content, so
// it must hold neither the key nor a token broader than the one repository it
// is reviewing.
func TestSubmitAuthenticatedKeepsTheAppKeyOutOfTheJob(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := authConfig()
	req := review.Request{Repo: "owner/repo", Ref: "refs/heads/main", SHA: "a", PRNumber: 7}

	name, err := SubmitAuthenticated(ctx, client, cfg, req, "delivery-1", issuer("ghs_scoped", time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	job, err := client.BatchV1().Jobs("bosun").Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		t.Error("the Job was left suspended; it would never run")
	}
	for _, v := range job.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "BOSUN_GITHUB_APP_ID" || v.Name == "BOSUN_GITHUB_PRIVATE_KEY" {
			t.Errorf("%s reached the reviewer Job", v.Name)
		}
		if v.Name == "GITHUB_TOKEN" {
			ref := v.ValueFrom.SecretKeyRef
			if ref.Name == cfg.GitHubTokenSecret {
				t.Error("GITHUB_TOKEN still points at the shared App secret")
			}
			if ref.Name != name+"-github" {
				t.Errorf("GITHUB_TOKEN reads %q, want the Job's own Secret", ref.Name)
			}
		}
	}

	secret, err := client.CoreV1().Secrets("bosun").Get(ctx, name+"-github", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("the per-Job Secret was not created: %v", err)
	}
	if string(secret.Data["token"]) != "ghs_scoped" {
		t.Errorf("token = %q", secret.Data["token"])
	}
	// Owned by its Job, so Kubernetes deletes it with the Job rather than
	// leaving a live credential behind after the TTL collects the Job.
	owners := secret.OwnerReferences
	if len(owners) != 1 || owners[0].Kind != "Job" || owners[0].Name != name {
		t.Errorf("Secret owner = %+v, want the Job", owners)
	}
	if owners[0].Controller == nil || !*owners[0].Controller {
		t.Error("Secret owner reference is not a controller reference")
	}
}

// A token that expires before the review's deadline means spending the model
// budget and then failing to publish. Refuse at submission, where it is cheap.
func TestSubmitAuthenticatedRefusesATokenShorterThanTheReview(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := authConfig() // 1800s review + 120s startup allowance

	_, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "refs/heads/main"}, "d", issuer("ghs", 10*time.Minute))
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"expires", "timeoutSeconds"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	jobs, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Error("a Job was created despite the refusal")
	}
}

// If the Secret cannot be created the Job must not be left suspended forever,
// holding a concurrency slot and waiting for a credential that never arrives.
func TestSubmitAuthenticatedCleansUpWhenTheTokenCannotBeStored(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := authConfig()
	failing := func(context.Context, string) (review.RepositoryToken, error) {
		return review.RepositoryToken{}, errors.New("installation not found")
	}

	_, err := SubmitAuthenticated(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "refs/heads/main"}, "d", failing)
	if err == nil || !strings.Contains(err.Error(), "installation not found") {
		t.Fatalf("error = %v, want the issuer's reason", err)
	}
	jobs, _ := client.BatchV1().Jobs("bosun").List(ctx, metav1.ListOptions{})
	if len(jobs.Items) != 0 {
		t.Errorf("a Job survived a failed token mint: %d", len(jobs.Items))
	}
}

// The existing shared-token path stays available for installs using a PAT.
func TestSubmitStillUsesTheSharedSecret(t *testing.T) {
	ctx := context.Background()
	client := fake.NewSimpleClientset()
	cfg := authConfig()
	name, err := Submit(ctx, client, cfg, review.Request{Repo: "owner/repo", Ref: "refs/heads/main"}, "d")
	if err != nil {
		t.Fatal(err)
	}
	job, _ := client.BatchV1().Jobs("bosun").Get(ctx, name, metav1.GetOptions{})
	found := false
	for _, v := range job.Spec.Template.Spec.Containers[0].Env {
		if v.Name == "GITHUB_TOKEN" && v.ValueFrom.SecretKeyRef.Name == cfg.GitHubTokenSecret {
			found = true
		}
	}
	if !found {
		t.Error("the shared-secret path changed")
	}
}
