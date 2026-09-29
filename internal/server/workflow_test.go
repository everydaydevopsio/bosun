package server_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	"github.com/everydaydevopsio/bosun/internal/server"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

type jobSubmitter struct {
	client *fake.Clientset
	cfg    config.Config
}

func (s jobSubmitter) Submit(ctx context.Context, r review.Request, d string) (string, error) {
	return jobs.Submit(ctx, s.client, s.cfg, r, d)
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}
func TestWebhookToPinnedCheckoutAndPublishedReview(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	remote := filepath.Join(root, "acme", "repo.git")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(remote), 0700); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	git(t, repo, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("reviewed"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-am", "review this")
	pinned := git(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "file"), []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "commit", "-am", "newer head")
	latest := git(t, repo, "rev-parse", "HEAD")
	git(t, repo, "checkout", "main")
	git(t, root, "clone", "--bare", repo, remote)
	git(t, root, "--git-dir", remote, "update-ref", "refs/pull/7/head", latest)
	// The fork's feature branch need not exist in the base repository.
	git(t, root, "--git-dir", remote, "update-ref", "-d", "refs/heads/feature")
	prompt := filepath.Join(root, "prompt.md")
	if err := os.WriteFile(prompt, []byte("Review all changes"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, event, payload, wantSHA string
		resolve                       bool
	}{
		{"pinned PR", "pull_request", fmt.Sprintf(`{"action":"synchronize","repository":{"full_name":"acme/repo"},"pull_request":{"number":7,"head":{"ref":"feature","sha":%q}}}`, pinned), pinned, false},
		{"comment resolves current head", "issue_comment", `{"action":"created","repository":{"full_name":"acme/repo"},"issue":{"number":7,"pull_request":{}},"comment":{"body":"@bridgectl review","author_association":"OWNER"},"sender":{"type":"User"}}`, latest, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "test-github-token-sensitive"
			for _, key := range []string{"BOSUN_LOCAL_PATH", "BOSUN_GITHUB_APP_ID", "BOSUN_GITHUB_PRIVATE_KEY", "BOSUN_GITHUB_PRIVATE_KEY_FILE", "CODEX_AUTH", "CLAUDE_CREDENTIALS"} {
				t.Setenv(key, "")
			}
			t.Setenv("GITHUB_TOKEN", token)
			var posted string
			resolved := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					t.Error("missing GitHub authentication")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /repos/acme/repo/pulls/7":
					resolved = true
					fmt.Fprintf(w, `{"head":{"ref":"feature","sha":%q}}`, latest)
				case "POST /repos/acme/repo/issues/7/comments":
					var body struct {
						Body string `json:"body"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					posted = body.Body
					w.WriteHeader(201)
					fmt.Fprint(w, `{}`)
				default:
					t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			cfg := config.Config{Namespace: "test", MaxConcurrentReviews: 3, ReviewTimeoutSeconds: 30, ReviewProvider: "codex-exec", ReviewCommand: "@bridgectl review", AllowedAssociations: map[string]bool{"OWNER": true}, WebhookSecret: "webhook-secret"}
			client := fake.NewSimpleClientset()
			h := server.Handler{Config: cfg, Submitter: jobSubmitter{client, cfg}}
			req := httptest.NewRequest("POST", "/webhooks/github", strings.NewReader(tc.payload))
			req.Header.Set("X-GitHub-Event", tc.event)
			req.Header.Set("X-GitHub-Delivery", "delivery")
			mac := hmac.New(sha256.New, []byte(cfg.WebhookSecret))
			mac.Write([]byte(tc.payload))
			req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			response := httptest.NewRecorder()
			h.ServeHTTP(response, req)
			if response.Code != 200 {
				t.Fatalf("webhook: %d %s", response.Code, response.Body.String())
			}
			list, err := client.BatchV1().Jobs("test").List(context.Background(), metav1.ListOptions{})
			if err != nil || len(list.Items) != 1 {
				t.Fatalf("jobs: %v %v", list, err)
			}
			for _, env := range list.Items[0].Spec.Template.Spec.Containers[0].Env {
				if env.ValueFrom == nil {
					t.Setenv(env.Name, env.Value)
				}
			}
			var workspace string
			worker := review.Worker{APIBaseURL: api.URL, CloneBaseURL: root, PromptPath: prompt, Output: io.Discard, Agent: func(ctx context.Context, path, provider, instructions string, max time.Duration) (string, error) {
				workspace = path
				if got := git(t, path, "rev-parse", "HEAD"); got != tc.wantSHA {
					t.Fatalf("reviewed %s, want %s", got, tc.wantSHA)
				}
				config, err := os.ReadFile(filepath.Join(path, ".git", "config"))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(config, []byte(token)) {
					t.Fatal("Git token persisted in repository")
				}
				if !strings.Contains(instructions, tc.wantSHA) {
					t.Fatal("prompt omits selected commit")
				}
				return "Finding. " + token, nil
			}}
			if err := worker.Run(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if resolved != tc.resolve {
				t.Fatalf("resolve PR = %v", resolved)
			}
			if !strings.HasPrefix(posted, "## Bosun code review") || strings.Contains(posted, token) || !strings.Contains(posted, "[REDACTED]") {
				t.Fatalf("unexpected published review: %q", posted)
			}
			if _, err := os.Stat(workspace); !os.IsNotExist(err) {
				t.Fatal("remote workspace not cleaned")
			}
		})
	}
}
