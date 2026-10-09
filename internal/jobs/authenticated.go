package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// TokenIssuer mints a credential for one repository.
type TokenIssuer func(ctx context.Context, repo string) (review.RepositoryToken, error)

// SubmitAuthenticated creates a reviewer Job that holds a credential for the
// single repository it is reviewing, and nothing more.
//
// The alternative, which this replaces, is mounting the App's private key into
// every Job. That key mints tokens for every repository the App is installed
// on, and the Job it would sit in runs an AI agent over the contents of a pull
// request -- attacker-supplied input that the agent reads by design. The same
// argument already decided model credentials: a Job gets the narrowest
// credential that still lets it work.
//
// The ordering is the delicate part. The Job is created suspended so the Secret
// can name it as owner; Kubernetes then garbage-collects the credential with
// the Job instead of leaving it live after the Job's TTL expires. Only once the
// Secret exists is the Job released to run, so it can never start looking for a
// credential that is not there yet.
func SubmitAuthenticated(ctx context.Context, client kubernetes.Interface, cfg config.Config, req review.Request, delivery string, issue TokenIssuer) (string, error) {
	return withAdmission(ctx, client, cfg.Namespace, func(ctx context.Context) (string, error) {
		name := Name(req.Repo, req.Ref, delivery)
		if _, err := client.BatchV1().Jobs(cfg.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return "", ExistsError{name}
		} else if !apierrors.IsNotFound(err) {
			return "", err
		}
		active, err := Active(ctx, client, cfg.Namespace)
		if err != nil {
			return "", err
		}
		if active >= cfg.MaxConcurrentReviews {
			return "", CapacityError{active, cfg.MaxConcurrentReviews}
		}

		token, err := issue(ctx, req.Repo)
		if err != nil {
			return "", fmt.Errorf("issue repository token: %w", err)
		}
		if token.Value == "" {
			return "", fmt.Errorf("issue repository token: GitHub returned an empty token")
		}
		// A token that dies before the review does means paying for the whole
		// review and then failing to publish it. Refuse now, where it costs
		// nothing, rather than at the end.
		deadline := time.Duration(cfg.ReviewTimeoutSeconds+startupAllowanceSeconds) * time.Second
		if !token.ExpiresAt.IsZero() && time.Until(token.ExpiresAt) < deadline {
			return "", fmt.Errorf("the repository token expires in %s but this review may run for %s; lower review.timeoutSeconds", time.Until(token.ExpiresAt).Round(time.Minute), deadline)
		}

		secretName := name + "-github"
		job := build(cfg, req, name)
		useJobToken(job, secretName)
		job.Spec.Suspend = ptr(true)
		created, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return "", ExistsError{name}
		}
		if err != nil {
			return "", err
		}

		// From here the Job exists but cannot run. Anything that goes wrong
		// must remove it, or it holds a concurrency slot forever waiting for a
		// credential that will never arrive.
		release := func(cause error) (string, error) {
			_ = client.BatchV1().Jobs(cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: ptr(metav1.DeletePropagationBackground)})
			return "", cause
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name: secretName, Namespace: cfg.Namespace,
				Labels: map[string]string{"app.kubernetes.io/managed-by": "bosun"},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "batch/v1", Kind: "Job", Name: created.Name, UID: created.UID,
					Controller: ptr(true), BlockOwnerDeletion: ptr(true),
				}},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{"token": []byte(token.Value)},
		}
		if _, err := client.CoreV1().Secrets(cfg.Namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return release(fmt.Errorf("store repository token: %w", err))
		}

		created.Spec.Suspend = ptr(false)
		if _, err := client.BatchV1().Jobs(cfg.Namespace).Update(ctx, created, metav1.UpdateOptions{}); err != nil {
			return release(fmt.Errorf("start review: %w", err))
		}
		return name, nil
	})
}

// useJobToken points the Job at its own credential and removes every route to a
// broader one. The App key is deliberately absent rather than merely unused:
// the reviewer falls back to it if it is present.
func useJobToken(job *batchv1.Job, secretName string) {
	container := &job.Spec.Template.Spec.Containers[0]
	kept := container.Env[:0]
	for _, v := range container.Env {
		switch v.Name {
		case "BOSUN_GITHUB_APP_ID", "BOSUN_GITHUB_PRIVATE_KEY", "BOSUN_GITHUB_PRIVATE_KEY_FILE":
			continue
		case "GITHUB_TOKEN":
			v.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "token",
			}}
		}
		kept = append(kept, v)
	}
	container.Env = kept
}
