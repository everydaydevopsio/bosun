package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
)

type submitter struct{ request review.Request }

func (s *submitter) Submit(_ context.Context, r review.Request, _ string) (string, error) {
	s.request = r
	return "review-test", nil
}
func sign(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
func TestHealthz(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	Handler{}.ServeHTTP(w, r)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "{\"ok\":true}" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestWebhookRejectsInvalidSignature(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader("{}"))
	w := httptest.NewRecorder()
	Handler{Config: config.Config{WebhookSecret: "secret"}}.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

type failingSubmitter struct{ err error }

func (f failingSubmitter) Submit(context.Context, review.Request, string) (string, error) {
	return "", f.err
}

// A submission that fails answers the caller generically -- a webhook sender
// should learn nothing about the cluster -- but the operator needs the reason.
// Without the log line every failure looks identical from outside and from the
// logs alike, which is the state that made a stale-update conflict in
// SubmitAuthenticated take a probe Job to find.
func TestWebhookKeepsTheSubmissionFailureReasonOutOfTheResponse(t *testing.T) {
	const secret = "webhook-secret"
	payload := `{"ref_type":"branch","ref":"main","repository":{"full_name":"owner/repo"}}`

	r := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(payload))
	r.Header.Set("X-GitHub-Event", "create")
	r.Header.Set("X-Hub-Signature-256", sign(secret, payload))
	w := httptest.NewRecorder()

	Handler{
		Config:    config.Config{WebhookSecret: secret},
		Submitter: failingSubmitter{errors.New("start review: jobs.batch \"review-x\" the object has been modified")},
	}.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	for _, leaked := range []string{"jobs.batch", "review-x", "object has been modified"} {
		if strings.Contains(w.Body.String(), leaked) {
			t.Errorf("the response leaked %q to the caller: %s", leaked, w.Body.String())
		}
	}
}
