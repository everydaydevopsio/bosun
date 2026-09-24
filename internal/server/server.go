package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
)

type Submitter interface {
	Submit(context.Context, review.Request, string) (string, error)
}
type Handler struct {
	Config    config.Config
	Submitter Submitter
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
		jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/webhooks/github" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "payload too large", http.StatusBadRequest)
		return
	}
	if !ValidSignature(h.Config.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return
	}
	var raw json.RawMessage
	if json.Unmarshal(body, &raw) != nil || len(raw) == 0 || raw[0] != '{' {
		http.Error(w, "payload is not valid JSON object", http.StatusBadRequest)
		return
	}
	req, ok := review.FromWebhook(r.Header.Get("X-GitHub-Event"), raw, h.Config.ReviewCommand, h.Config.AllowedAssociations)
	if !ok {
		jsonResponse(w, http.StatusOK, map[string]any{"accepted": false, "reason": "event does not request a review"})
		return
	}
	job, err := h.Submitter.Submit(r.Context(), req, header(r, "X-GitHub-Delivery", "manual"))
	if err != nil {
		switch err.(type) {
		case interface{ Capacity() }:
			w.Header().Set("Retry-After", "120")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		default:
			if _, ok := err.(interface{ Duplicate() }); ok {
				jsonResponse(w, http.StatusOK, map[string]any{"accepted": true, "duplicate": true})
				return
			}
			http.Error(w, "could not create review job", http.StatusInternalServerError)
		}
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"accepted": true, "job": job})
}
func header(r *http.Request, k, fallback string) string {
	if v := r.Header.Get(k); v != "" {
		return v
	}
	return fallback
}
func ValidSignature(secret string, body []byte, signature string) bool {
	if secret == "" || len(signature) != 71 || signature[:7] != "sha256=" {
		return false
	}
	got, err := hex.DecodeString(signature[7:])
	if err != nil {
		return false
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hmac.Equal(got, m.Sum(nil))
}
func jsonResponse(w http.ResponseWriter, status int, p any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}
