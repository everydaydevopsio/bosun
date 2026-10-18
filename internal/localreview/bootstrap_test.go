package localreview

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestLoadCredentialsChecksProviderBeforeWritingSecret(t *testing.T) {
	client := fake.NewSimpleClientset()
	called := false
	err := loadCredentials(context.Background(), client, "bosun", "bosun-ai", "claude-bosun",
		[]credentials.Credential{{Key: "claude-credentials", Value: `{"claudeAiOauth":{"accessToken":"fake"}}`}},
		func(string) {}, func(context.Context, string) error { called = true; return fmt.Errorf("AUTH_REJECTED") })
	if err == nil || !strings.Contains(err.Error(), "AUTH_REJECTED") || !called {
		t.Fatalf("expected failed live check, got %v called=%v", err, called)
	}
	if _, err := client.CoreV1().Secrets("bosun").Get(context.Background(), "bosun-ai", metav1.GetOptions{}); err == nil {
		t.Fatal("credential uploaded despite failed provider check")
	}
}

func TestLoadCredentialsUploadsTokenRefreshedByProbe(t *testing.T) {
	client := fake.NewSimpleClientset()
	path := filepath.Join(t.TempDir(), ".credentials.json")
	old := `{"claudeAiOauth":{"accessToken":"old"}}`
	fresh := `{"claudeAiOauth":{"accessToken":"fresh"}}`
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	err := loadCredentials(context.Background(), client, "bosun", "bosun-ai", "claude-bosun",
		[]credentials.Credential{{Key: "claude-credentials", Value: old, From: path}},
		func(string) {}, func(context.Context, string) error {
			return os.WriteFile(path, []byte(fresh), 0600)
		})
	if err != nil {
		t.Fatal(err)
	}
	secret, err := client.CoreV1().Secrets("bosun").Get(context.Background(), "bosun-ai", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["claude-credentials"]) != fresh {
		t.Fatal("uploaded stale credential captured before probe")
	}
}

func TestLoadCredentialsDoesNotVerifyDifferentLocalLogin(t *testing.T) {
	client := fake.NewSimpleClientset()
	err := loadCredentials(context.Background(), client, "bosun", "bosun-ai", "claude-bosun",
		[]credentials.Credential{{Key: "claude-credentials", Value: `{"claudeAiOauth":{"accessToken":"fake"}}`, From: "CLAUDE_CREDENTIALS"}},
		func(string) {}, func(context.Context, string) error { t.Fatal("probe must not use a different login"); return nil })
	if err == nil || !strings.Contains(err.Error(), "cannot live-check CLAUDE_CREDENTIALS") {
		t.Fatalf("unexpected status: %v", err)
	}
}

func TestProviderProbeDoesNotEchoProviderOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "claude")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' 'Failed to authenticate. API Error: 401 OAuth access token has been revoked. secret-value' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	err := probeLocalProvider(context.Background(), "claude-bosun")
	if err == nil || !strings.Contains(err.Error(), "AUTH_REVOKED") || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unsafe or missing diagnosis: %v", err)
	}
}

func TestProviderProbeAcceptsHeadlessSuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nprintf OK\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := probeLocalProvider(context.Background(), "claude-bosun"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderProbeRejectsEmptySuccess(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := probeLocalProvider(context.Background(), "claude-bosun"); err == nil || !strings.Contains(err.Error(), "no response") {
		t.Fatalf("empty provider response accepted: %v", err)
	}
}

func TestClusterCredentialStatusChecksSelectedProvider(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "bosun-ai", Namespace: "bosun"}, Data: map[string][]byte{"codex-auth": []byte(`{}`)}})
	if err := checkClusterCredentials(context.Background(), client, "bosun", "bosun-ai", "claude-bosun"); err == nil {
		t.Fatal("missing selected provider credential reported ready")
	}
	if err := checkClusterCredentials(context.Background(), client, "bosun", "bosun-ai", "codex-bosun"); err != nil {
		t.Fatalf("stored credential rejected: %v", err)
	}
}

// --local-credentials is the flag that turns "I am signed in to codex on this
// laptop" into a Secret the reviewer Job can read.
func TestLoadCredentials(t *testing.T) {
	tests := []struct {
		name, provider string
		found          []credentials.Credential
		wantErr        string
		wantKeys       []string
		wantNotice     string
	}{
		{
			name: "writes what it found", provider: "codex-bosun",
			found:    []credentials.Credential{{Key: "codex-auth", Value: "{}"}},
			wantKeys: []string{"codex-auth"},
		},
		{
			name: "fails when the provider's credential is the one missing", provider: "claude-bosun",
			found:   []credentials.Credential{{Key: "codex-auth", Value: "{}", From: "x"}},
			wantErr: "claude-bosun",
		},
		{
			name: "fails when nothing was discovered", provider: "codex-bosun",
			wantErr: "codex-bosun",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "writes what it found" {
				path := filepath.Join(t.TempDir(), "auth.json")
				if err := os.WriteFile(path, []byte(tc.found[0].Value), 0600); err != nil {
					t.Fatal(err)
				}
				tc.found[0].From = path
				tc.wantNotice = path
			}
			client := fake.NewSimpleClientset()
			var notices []string
			e := loadCredentials(context.Background(), client, "bosun", "bosun-ai", tc.provider, tc.found, func(m string) { notices = append(notices, m) }, func(context.Context, string) error { return nil })
			if tc.wantErr != "" {
				if e == nil || !strings.Contains(e.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one mentioning %q", e, tc.wantErr)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			s, err := client.CoreV1().Secrets("bosun").Get(context.Background(), "bosun-ai", metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range tc.wantKeys {
				if _, ok := s.Data[k]; !ok {
					t.Errorf("secret is missing %s", k)
				}
			}
			joined := strings.Join(notices, "\n")
			if !strings.Contains(joined, tc.wantNotice) {
				t.Errorf("notices %q do not report the source %q", joined, tc.wantNotice)
			}
			// The value itself must never be printed.
			for _, c := range tc.found {
				if strings.Contains(joined, c.Value) && c.Value != "" {
					t.Errorf("notice leaked a credential value: %q", joined)
				}
			}
		})
	}
}

// up and down exist so a user never has to find scripts/kind-up.sh.
func TestParseLifecycleCommands(t *testing.T) {
	t.Setenv("BOSUN_KIND_CLUSTER", "")
	o, _, e := parse("up", []string{"--local-credentials"}, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if !o.localCredentials {
		t.Error("--local-credentials was not parsed for up")
	}
	if o.cluster != "bosun" {
		t.Errorf("default cluster = %q, want bosun", o.cluster)
	}
	if o, _, e = parse("down", []string{"--keep-snapshots", "--cluster", "other"}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !o.keepSnapshots || o.cluster != "other" {
		t.Errorf("down parsed keepSnapshots=%v cluster=%q", o.keepSnapshots, o.cluster)
	}
	// review takes the same credential flag, plus an escape hatch from the
	// implicit bootstrap.
	if o, _, e = parse("review", []string{"--local-credentials", "--no-bootstrap"}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !o.localCredentials || !o.noBootstrap {
		t.Errorf("review parsed localCredentials=%v noBootstrap=%v", o.localCredentials, o.noBootstrap)
	}
	// A cluster name reaches review through --context, so the two must agree.
	if o, _, e = parse("review", []string{"--context", "kind-other"}, io.Discard); e != nil {
		t.Fatal(e)
	}
	if o.context != "kind-other" {
		t.Errorf("context = %q", o.context)
	}
}

// BOSUN_KIND_CLUSTER names the cluster for every command, not just review.
func TestParseHonoursClusterEnvironment(t *testing.T) {
	t.Setenv("BOSUN_KIND_CLUSTER", "custom")
	o, _, e := parse("up", nil, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if o.cluster != "custom" {
		t.Errorf("cluster = %q, want custom", o.cluster)
	}
	if o, _, e = parse("review", nil, io.Discard); e != nil {
		t.Fatal(e)
	}
	if o.context != "kind-custom" {
		t.Errorf("context = %q, want kind-custom", o.context)
	}
}
