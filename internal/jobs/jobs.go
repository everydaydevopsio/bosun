package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const MaxName = 63

var unsafe = regexp.MustCompile(`[^a-z0-9-]+`)

type ExistsError struct{ Name string }

func (e ExistsError) Error() string { return "review job already exists: " + e.Name }
func (e ExistsError) Duplicate()    {}

type CapacityError struct{ Running, Cap int }

func (e CapacityError) Error() string {
	return fmt.Sprintf("%d reviews already running (cap %d)", e.Running, e.Cap)
}
func (e CapacityError) Capacity() {}

func Name(repo, ref, delivery string) string {
	sum := sha256.Sum256([]byte(repo + "\n" + ref + "\n" + delivery))
	suffix := fmt.Sprintf("%x", sum[:])[:10]
	stem := unsafe.ReplaceAllString(strings.ToLower("review-"+last(repo)+"-"+ref), "-")
	stem = strings.Trim(stem, "-")
	limit := MaxName - len(suffix) - 1
	if len(stem) > limit {
		stem = strings.Trim(stem[:limit], "-")
	}
	if stem == "" {
		return "review-" + suffix
	}
	return stem + "-" + suffix
}
func last(s string) string {
	if i := strings.LastIndex(s, "/"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func Active(ctx context.Context, client kubernetes.Interface, namespace string) (int, error) {
	list, err := client.BatchV1().Jobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=bosun"})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, j := range list.Items {
		if j.Status.Succeeded == 0 && j.Status.Failed == 0 {
			n++
		}
	}
	return n, nil
}
func Submit(ctx context.Context, client kubernetes.Interface, cfg config.Config, req review.Request, delivery string) (string, error) {
	n, err := Active(ctx, client, cfg.Namespace)
	if err != nil {
		return "", err
	}
	if n >= cfg.MaxConcurrentReviews {
		return "", CapacityError{n, cfg.MaxConcurrentReviews}
	}
	name := Name(req.Repo, req.Ref, delivery)
	job := build(cfg, req, name)
	_, err = client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if errors.IsAlreadyExists(err) {
		return "", ExistsError{name}
	}
	if err != nil {
		return "", err
	}
	return name, nil
}
func ptr[T any](v T) *T { return &v }
func env(cfg config.Config, req review.Request) []corev1.EnvVar {
	base := []corev1.EnvVar{{Name: "BOSUN_REPO", Value: req.Repo}, {Name: "BOSUN_REF", Value: req.Ref}, {Name: "BOSUN_SHA", Value: req.SHA}, {Name: "BOSUN_TRIGGER", Value: req.Trigger}, {Name: "BOSUN_REVIEW_PROVIDER", Value: cfg.ReviewProvider}, {Name: "BOSUN_REVIEW_TIMEOUT_SECONDS", Value: fmt.Sprint(cfg.ReviewTimeoutSeconds)}, {Name: "BOSUN_PR_NUMBER", Value: fmt.Sprint(req.PRNumber)}}
	for _, x := range []struct{ n, s, k string }{{"GITHUB_TOKEN", cfg.GitHubTokenSecret, "token"}, {"OPENAI_API_KEY", cfg.AISecret, "openai-api-key"}, {"CLAUDE_CODE_OAUTH_TOKEN", cfg.AISecret, "claude-code-oauth-token"}, {"ANTHROPIC_API_KEY", cfg.AISecret, "anthropic-api-key"}, {"GEMINI_API_KEY", cfg.AISecret, "gemini-api-key"}, {"CODEX_AUTH", cfg.AISecret, "codex-auth"}, {"CLAUDE_CREDENTIALS", cfg.AISecret, "claude-credentials"}, {"BOSUN_GITHUB_APP_ID", cfg.GitHubTokenSecret, "app-id"}, {"BOSUN_GITHUB_PRIVATE_KEY", cfg.GitHubTokenSecret, "private-key"}} {
		base = append(base, corev1.EnvVar{Name: x.n, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: x.s}, Key: x.k, Optional: ptr(true)}}})
	}
	return base
}
func build(cfg config.Config, req review.Request, name string) *batchv1.Job {
	deadline := cfg.ReviewTimeoutSeconds + 120
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "bosun"}},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)), TTLSecondsAfterFinished: &cfg.JobTTLSeconds, ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "bosun-review", "bosun/repository": strings.ReplaceAll(req.Repo, "/", "-")}},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr(false), Containers: []corev1.Container{{
					Name: "reviewer", Image: cfg.ReviewImage, Command: []string{"/usr/local/bin/bosun", "reviewer"}, Env: env(cfg, req),
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), RunAsNonRoot: ptr(true), RunAsUser: &cfg.RunAsUser, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				}}},
			},
		},
	}
}
