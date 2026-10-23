package review

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestGitHubAppTokenIsSignedAndRepositoryScoped(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOSUN_GITHUB_APP_ID", "123")
	t.Setenv("BOSUN_GITHUB_PRIVATE_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	t.Setenv("GITHUB_TOKEN", "")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Error("invalid JWT")
			w.WriteHeader(401)
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Error(err)
		}
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
			t.Error(err)
		}
		payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var claims struct {
			Iss      string
			Iat, Exp int64
		}
		if err := json.Unmarshal(payload, &claims); err != nil {
			t.Error(err)
		}
		if claims.Iss != "123" || claims.Exp <= time.Now().Unix() || claims.Exp-claims.Iat > 600 {
			t.Error("invalid JWT claims")
		}
		switch r.URL.Path {
		case "/repos/acme/widget/installation":
			fmt.Fprint(w, `{"id":42}`)
		case "/app/installations/42/access_tokens":
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Repositories) != 1 || body.Repositories[0] != "widget" || len(body.Permissions) != 2 || body.Permissions["contents"] != "read" || body.Permissions["pull_requests"] != "write" {
				t.Errorf("token not minimally scoped: %+v", body)
			}
			fmt.Fprint(w, `{"token":"installation-test-token"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	token, err := (githubClient{api.URL, api.Client()}).token(context.Background(), "acme/widget")
	if err != nil || token != "installation-test-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}
func TestAgentEnvironmentExcludesGitHubCredentials(t *testing.T) {
	for key := range withheld {
		t.Setenv(key, "sensitive-value")
	}
	t.Setenv("OPENAI_API_KEY", "provider-value")
	env := strings.Join(agentEnvironment(), "\n")
	if strings.Contains(env, "sensitive-value") {
		t.Fatal("GitHub credentials reached agent environment")
	}
	if !strings.Contains(env, "OPENAI_API_KEY=provider-value") {
		t.Fatal("provider credential stripped")
	}
}
func TestGitHubFailureDoesNotEchoResponseSecrets(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "sensitive token", 403) }))
	defer api.Close()
	err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "test-token", "feature", "0123456789abcdef0123456789abcdef01234567", "review")
	if err == nil || strings.Contains(err.Error(), "sensitive token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// An App that authenticates but is not installed on the repository is the
// most common way a correctly configured deployment still fails. Returning
// the transport error unchanged made it indistinguishable from any other 404
// -- a wrong owner, a renamed repository, a revoked key -- and
// docs/github-app.md promised a message the code never produced.
func TestInstallationLookupNamesAnUninstalledApp(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/widget/installation" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer api.Close()

	_, err := IssueRepositoryToken(context.Background(), api.URL, api.Client(), "acme/widget", "1", testPrivateKey(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	// The exact wording is a documented interface: docs/github-app.md quotes it.
	if got, want := err.Error(), "the GitHub App is not installed on acme/widget"; !strings.Contains(got, want) {
		t.Errorf("error = %q, want it to contain %q", got, want)
	}
}

// Only a 404 means "not installed". Other failures must keep their own
// diagnosis rather than being relabelled as an installation problem.
func TestInstallationLookupLeavesOtherFailuresAlone(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer api.Close()

	_, err := IssueRepositoryToken(context.Background(), api.URL, api.Client(), "acme/widget", "1", testPrivateKey(t))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "not installed") {
		t.Errorf("a 500 was reported as an installation problem: %v", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q loses the status code", err)
	}
}

// testPrivateKey returns a throwaway RSA key in the PEM form GitHub hands out.
func testPrivateKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// docs/github-app.md quotes these strings as what an operator should expect
// to see. Nothing but this test connects the two, and the page is read while
// standing up an App -- the moment with the least context and the most to get
// wrong. Four of its claims were wrong at once before #63: an invented
// logger= attribute, expires instead of expires_at, a fallback message with
// different text and no attributes, and an error that did not exist.
func TestDocumentedStringsExist(t *testing.T) {
	doc, err := os.ReadFile("../../docs/github-app.md")
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("github.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, quoted := range []string{
		"minted installation token",
		"GitHub App authentication failed; falling back to GITHUB_TOKEN",
		"the GitHub App is not installed on",
	} {
		if !strings.Contains(string(doc), quoted) {
			t.Errorf("docs/github-app.md no longer quotes %q; drop it from this test or restore the example", quoted)
		}
		if !strings.Contains(string(source), quoted) {
			t.Errorf("docs/github-app.md promises %q but github.go never produces it", quoted)
		}
	}
	// The attribute name is part of the example an operator greps for.
	if strings.Contains(string(doc), "logger=bosun.githubapp") {
		t.Error("docs quote a logger= attribute that no slog call sets")
	}
	if strings.Contains(string(doc), `expires="..."`) {
		t.Error("docs quote expires; the field is expires_at")
	}
}
