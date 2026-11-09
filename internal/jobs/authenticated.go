package jobs

import (
	"context"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
// credential that is not there yet. When every slot is busy the Job stays
// suspended in the queue, and the token is minted when the Dispatcher starts
// it, not here.
func SubmitAuthenticated(ctx context.Context, client kubernetes.Interface, cfg config.Config, req review.Request, delivery string, issue TokenIssuer) (string, error) {
	return withAdmission(ctx, client, cfg.Namespace, func(ctx context.Context) (string, error) {
		name := Name(req.Repo, req.NameRef(), delivery)
		job, err := build(cfg, req, name)
		if err != nil {
			return "", err
		}
		useJobToken(job, name+"-github")
		labelPullRequest(job, req.Repo, req.PRNumber)
		return admit(ctx, client, cfg, job, startAuthenticated(client, cfg, issue))
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
