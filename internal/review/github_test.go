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
	err := (githubClient{api.URL, api.Client()}).postReview(context.Background(), "acme/widget", 7, "test-token", "review")
	if err == nil || strings.Contains(err.Error(), "sensitive token") {
		t.Fatalf("unexpected error: %v", err)
	}
}
