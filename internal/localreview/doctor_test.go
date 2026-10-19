package localreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDoctorImageDistinguishesCacheAndVersion(t *testing.T) {
	for _, tc := range []struct {
		name, ref, status string
		found             bool
		labels            map[string]string
	}{
		{name: "published image not cached", ref: "ghcr.io/everydaydevopsio/bosun:0.1.5", status: "not cached"},
		{name: "dev image missing", ref: "bosun:dev", status: "missing"},
		{name: "matching release", ref: "ghcr.io/everydaydevopsio/bosun:0.1.5", found: true, labels: map[string]string{titleLabel: "bosun", versionLabel: "0.1.5"}, status: "ok"},
		{name: "stale release", ref: "ghcr.io/everydaydevopsio/bosun:0.1.5", found: true, labels: map[string]string{titleLabel: "bosun", versionLabel: "0.1.4"}, status: "mismatch"},
		{name: "wrong image", ref: "ghcr.io/everydaydevopsio/bosun:0.1.5", found: true, labels: map[string]string{titleLabel: "bridgectl", versionLabel: "0.1.5"}, status: "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check := inspectDoctorImage(context.Background(), tc.ref, "0.1.5", func(context.Context, string) ([]byte, bool, error) {
				return inspectJSON(t, tc.labels), tc.found, nil
			})
			if check.Status != tc.status {
				t.Fatalf("status=%q want=%q: %+v", check.Status, tc.status, check)
			}
			if tc.status == "not cached" && check.Required {
				t.Fatal("pullable image should not fail doctor before first review")
			}
		})
	}
}

func TestFindReadyNodeRequiresSelectedControlPlaneReady(t *testing.T) {
	nodes := []corev1.Node{
		{ObjectMeta: metav1.ObjectMeta{Name: "other-control-plane"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "bosun-control-plane"}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionFalse}}}},
	}
	if findReadyNode(nodes, "bosun-control-plane") != nil {
		t.Fatal("unready selected node reported ready")
	}
	nodes[1].Status.Conditions[0].Status = corev1.ConditionTrue
	if findReadyNode(nodes, "bosun-control-plane") == nil {
		t.Fatal("ready selected node not found")
	}
}

func TestDoctorMissingClusterIsReadOnlyAndMachineReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	logPath := filepath.Join(dir, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + logPath + "'\nif [ \"$1\" = version ]; then echo 'kind v0.31.0'; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "kind"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var out, errout bytes.Buffer
	code := runDoctor(context.Background(), options{cluster: "bosun", namespace: "bosun", provider: "claude-bosun", json: true}, &out, &errout)
	if code != 1 {
		t.Fatalf("code=%d output=%s error=%s", code, out.String(), errout.String())
	}
	var report doctorReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Healthy || report.Cluster != "bosun" {
		t.Fatalf("unexpected report: %+v", report)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(calls); !strings.Contains(got, "get clusters") || strings.Contains(got, "create") || strings.Contains(got, "delete") {
		t.Fatalf("doctor changed cluster state: %q", got)
	}
}

func TestDoctorReportsControllerImageVersionAndReadiness(t *testing.T) {
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "bosun", Namespace: "bosun", Labels: map[string]string{"app.kubernetes.io/name": "bosun"}},
		Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "bosun", Image: "ghcr.io/everydaydevopsio/bosun:0.1.4"}}}}},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
	r := doctorReport{Healthy: true}
	doctorController(context.Background(), "bosun", fake.NewSimpleClientset(deployment), &r)
	if len(r.Checks) != 1 || r.Checks[0].Version != "0.1.4" || r.Checks[0].Detail != deployment.Spec.Template.Spec.Containers[0].Image {
		t.Fatalf("controller version not reported: %+v", r.Checks)
	}
	deployment.Spec.Template.Spec.Containers[0].Image = "bosun:dev"
	deployment.Status.ReadyReplicas = 0
	r = doctorReport{Healthy: true}
	doctorController(context.Background(), "bosun", fake.NewSimpleClientset(deployment), &r)
	if r.Checks[0].Status != "unready" || r.Healthy {
		t.Fatalf("unready controller reported healthy: %+v", r)
	}
}

func TestDoctorDistinguishesMissingCredentialFromAPIFailure(t *testing.T) {
	notFound := fmt.Errorf("read secret: %w", apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "bosun-ai"))
	if check := credentialDoctorCheck(notFound, "bosun up"); check.Status != "missing" || check.Remedy != "bosun up" {
		t.Fatalf("missing secret: %+v", check)
	}
	if check := credentialDoctorCheck(errors.New("API timeout"), "bosun up"); check.Status != "error" || !strings.Contains(check.Detail, "timeout") {
		t.Fatalf("API error misreported: %+v", check)
	}
}
