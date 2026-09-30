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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

type TokenIssuer func(context.Context, string) (string, error)

// SubmitAuthenticated keeps the App signing key in the controller. A suspended
// Job is created first so its per-repository token Secret can have an owner.
// The Job starts only after that Secret exists. No secret value enters the Job.
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
		if token == "" {
			return "", fmt.Errorf("empty repository token")
		}
		secretName := name + "-github"
		job := build(cfg, req, name)
		hardenRemoteJob(job, secretName)
		job.Spec.Suspend = ptr(true)
		created, err := client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return "", ExistsError{name}
		}
		if err != nil {
			return "", err
		}
		success := false
		defer func() {
			if !success {
				cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				// Garbage collection removes only this Job's owned token Secret.
				uid := created.UID
				_ = client.BatchV1().Jobs(cfg.Namespace).Delete(cleanup, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}, PropagationPolicy: ptr(metav1.DeletePropagationBackground)})
			}
		}()
		_, err = client.CoreV1().Secrets(cfg.Namespace).Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: name, UID: created.UID, Controller: ptr(true)}}},
			Type: corev1.SecretTypeOpaque, Immutable: ptr(true), Data: map[string][]byte{"token": []byte(token)},
		}, metav1.CreateOptions{})
		if err != nil {
			return "", fmt.Errorf("create review credential: %w", err)
		}
		_, err = client.BatchV1().Jobs(cfg.Namespace).Patch(ctx, name, types.MergePatchType, []byte(`{"spec":{"suspend":false}}`), metav1.PatchOptions{})
		if err != nil {
			return "", fmt.Errorf("start review job: %w", err)
		}
		success = true
		return name, nil
	})
}

func hardenRemoteJob(job *batchv1.Job, secretName string) {
	pod := &job.Spec.Template.Spec
	pod.SecurityContext = &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
	c := &pod.Containers[0]
	filtered := c.Env[:0]
	for _, e := range c.Env {
		switch e.Name {
		case "GITHUB_TOKEN", "BOSUN_GITHUB_APP_ID", "BOSUN_GITHUB_PRIVATE_KEY", "BOSUN_GITHUB_PRIVATE_KEY_FILE":
		default:
			filtered = append(filtered, e)
		}
	}
	c.Env = append(filtered, corev1.EnvVar{Name: "GITHUB_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "token"}}})
	c.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("1Gi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("2Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("4Gi")},
	}
}
