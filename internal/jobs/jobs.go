package jobs

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/credentials"
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

func repositoryLabel(repo string) string {
	return Name(repo, "repository", repo)
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
	return withAdmission(ctx, client, cfg.Namespace, func(ctx context.Context) (string, error) {
		return submitLocked(ctx, client, cfg, req, delivery)
	})
}

func submitLocked(ctx context.Context, client kubernetes.Interface, cfg config.Config, req review.Request, delivery string) (string, error) {
	name := Name(req.Repo, req.Ref, delivery)
	if _, err := client.BatchV1().Jobs(cfg.Namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return "", ExistsError{name}
	} else if !errors.IsNotFound(err) {
		return "", err
	}
	n, err := Active(ctx, client, cfg.Namespace)
	if err != nil {
		return "", err
	}
	if n >= cfg.MaxConcurrentReviews {
		return "", CapacityError{n, cfg.MaxConcurrentReviews}
	}
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

	// GitHub credentials are how the reviewer clones and posts, so they are not
	// provider-scoped.
	refs := []struct{ n, s, k string }{
		{"GITHUB_TOKEN", cfg.GitHubTokenSecret, "token"},
		{"BOSUN_GITHUB_APP_ID", cfg.GitHubTokenSecret, "app-id"},
		{"BOSUN_GITHUB_PRIVATE_KEY", cfg.GitHubTokenSecret, "private-key"},
	}
	for _, c := range credentials.ForProvider(cfg.ReviewProvider) {
		refs = append(refs, struct{ n, s, k string }{c.Variable, cfg.AISecret, c.Key})
	}

	for _, x := range refs {
		base = append(base, corev1.EnvVar{Name: x.n, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: x.s}, Key: x.k, Optional: ptr(true)}}})
	}
	return base
}
func build(cfg config.Config, req review.Request, name string) *batchv1.Job {
	deadline := cfg.ReviewTimeoutSeconds + 120
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app.kubernetes.io/managed-by": "bosun"}, Annotations: map[string]string{"bosun/repository": req.Repo}},
		Spec: batchv1.JobSpec{
			BackoffLimit: ptr(int32(0)), TTLSecondsAfterFinished: &cfg.JobTTLSeconds, ActiveDeadlineSeconds: &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/name": "bosun-review", "bosun/repository": repositoryLabel(req.Repo)}, Annotations: map[string]string{"bosun/repository": req.Repo}},
				Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr(false), Containers: []corev1.Container{{
					Name: "reviewer", Image: cfg.ReviewImage, Command: []string{"/usr/local/bin/bosun", "reviewer"}, Env: env(cfg, req),
					SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), RunAsNonRoot: ptr(true), RunAsUser: &cfg.RunAsUser, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				}}},
			},
		},
	}
}

// SubmitLocal shares admission and credential construction with hosted reviews.
func SubmitLocal(ctx context.Context, client kubernetes.Interface, cfg config.Config, req review.Request, snapshot, base string, committed bool, group int64) (string, error) {
	return withAdmission(ctx, client, cfg.Namespace, func(ctx context.Context) (string, error) {
		n, err := Active(ctx, client, cfg.Namespace)
		if err != nil {
			return "", err
		}
		if n >= cfg.MaxConcurrentReviews {
			return "", CapacityError{n, cfg.MaxConcurrentReviews}
		}
		name := Name(req.Repo, req.Ref, snapshot)
		job := build(cfg, req, name)
		job.Labels["bosun/local"] = "true"
		job.Annotations["bosun/snapshot"] = snapshot
		pod := &job.Spec.Template.Spec
		pod.SecurityContext = &corev1.PodSecurityContext{RunAsGroup: &group, RunAsUser: &cfg.RunAsUser, RunAsNonRoot: ptr(true)}
		pod.Volumes = []corev1.Volume{{Name: "repos", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/repos", Type: ptr(corev1.HostPathDirectory)}}}}
		c := &pod.Containers[0]
		// Local jobs do not need GitHub credentials.
		filtered := c.Env[:0]
		for _, v := range c.Env {
			if v.Name != "GITHUB_TOKEN" && v.Name != "BOSUN_GITHUB_APP_ID" && v.Name != "BOSUN_GITHUB_PRIVATE_KEY" {
				filtered = append(filtered, v)
			}
		}
		c.Env = filtered
		c.VolumeMounts = []corev1.VolumeMount{{Name: "repos", MountPath: "/repos"}}
		path := "/repos/" + snapshot
		c.Env = append(c.Env, corev1.EnvVar{Name: "BOSUN_LOCAL_PATH", Value: path}, corev1.EnvVar{Name: "BOSUN_EVENTS", Value: "1"}, corev1.EnvVar{Name: "BOSUN_RUN_ID", Value: name}, corev1.EnvVar{Name: "BOSUN_EVENT_FILE", Value: path + ".events"}, corev1.EnvVar{Name: "BOSUN_BASE_SHA", Value: base}, corev1.EnvVar{Name: "GIT_CONFIG_COUNT", Value: "1"}, corev1.EnvVar{Name: "GIT_CONFIG_KEY_0", Value: "safe.directory"}, corev1.EnvVar{Name: "GIT_CONFIG_VALUE_0", Value: path})
		if committed {
			c.Env = append(c.Env, corev1.EnvVar{Name: "BOSUN_COMMITTED_ONLY", Value: "1"})
		}
		_, err = client.BatchV1().Jobs(cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
		return name, err
	})
}
