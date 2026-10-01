package cluster

import (
	"context"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureNamespace(t *testing.T) {
	client := fake.NewSimpleClientset()
	ctx := context.Background()
	if e := EnsureNamespace(ctx, client, "bosun"); e != nil {
		t.Fatal(e)
	}
	if _, e := client.CoreV1().Namespaces().Get(ctx, "bosun", metav1.GetOptions{}); e != nil {
		t.Fatalf("namespace was not created: %v", e)
	}
	// Bootstrap runs on every review, so it must be idempotent rather than
	// failing the second time with AlreadyExists.
	if e := EnsureNamespace(ctx, client, "bosun"); e != nil {
		t.Fatalf("second call failed: %v", e)
	}
}

func TestEnsureSecret(t *testing.T) {
	ctx := context.Background()
	t.Run("creates the secret when absent", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		written, e := EnsureSecret(ctx, client, "bosun", "bosun-ai", []credentials.Credential{{Key: "codex-auth", Value: "{}"}})
		if e != nil {
			t.Fatal(e)
		}
		if len(written) != 1 || written[0] != "codex-auth" {
			t.Fatalf("wrote %v, want [codex-auth]", written)
		}
		s, e := client.CoreV1().Secrets("bosun").Get(ctx, "bosun-ai", metav1.GetOptions{})
		if e != nil {
			t.Fatal(e)
		}
		if string(s.Data["codex-auth"]) != "{}" {
			t.Errorf("codex-auth = %q", s.Data["codex-auth"])
		}
	})
	t.Run("updates one key and leaves the others", func(t *testing.T) {
		client := fake.NewSimpleClientset(&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "bosun-ai", Namespace: "bosun"},
			Data: map[string][]byte{
				"codex-auth": []byte("old"),
				// A key this run did not discover must survive: the user may
				// have loaded it from somewhere else.
				"gemini-api-key": []byte("keep"),
			},
		})
		written, e := EnsureSecret(ctx, client, "bosun", "bosun-ai", []credentials.Credential{{Key: "codex-auth", Value: "new"}})
		if e != nil {
			t.Fatal(e)
		}
		if len(written) != 1 {
			t.Fatalf("wrote %v, want one key", written)
		}
		s, _ := client.CoreV1().Secrets("bosun").Get(ctx, "bosun-ai", metav1.GetOptions{})
		if string(s.Data["codex-auth"]) != "new" {
			t.Errorf("codex-auth = %q, want it replaced", s.Data["codex-auth"])
		}
		if string(s.Data["gemini-api-key"]) != "keep" {
			t.Errorf("gemini-api-key = %q, want it preserved", s.Data["gemini-api-key"])
		}
	})
	t.Run("nothing to write is not an error", func(t *testing.T) {
		client := fake.NewSimpleClientset()
		written, e := EnsureSecret(ctx, client, "bosun", "bosun-ai", nil)
		if e != nil || len(written) != 0 {
			t.Fatalf("written=%v err=%v", written, e)
		}
		if _, e := client.CoreV1().Secrets("bosun").Get(ctx, "bosun-ai", metav1.GetOptions{}); e == nil {
			t.Error("an empty credential set should not create a Secret")
		}
	})
}
