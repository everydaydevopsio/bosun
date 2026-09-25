package server

import (
	"context"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type submitter struct{ request review.Request }

func (s *submitter) Submit(_ context.Context, r review.Request, _ string) (string, error) {
	s.request = r
	return "review-test", nil
}
func sign(body string) string {
	return "sha256=5ce34debd7e54dd4cc9ec1e4d730e43283f844c59d9ef98d5f64e579d6167f0b"
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
