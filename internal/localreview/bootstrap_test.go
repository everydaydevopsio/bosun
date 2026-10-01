package localreview

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

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
			found:      []credentials.Credential{{Key: "codex-auth", Value: "{}", From: "/home/u/.codex/auth.json"}},
			wantKeys:   []string{"codex-auth"},
			wantNotice: "/home/u/.codex/auth.json",
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
			client := fake.NewSimpleClientset()
			var notices []string
			e := loadCredentials(context.Background(), client, "bosun", "bosun-ai", tc.provider, tc.found, func(m string) { notices = append(notices, m) })
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
