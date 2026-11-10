package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const scanSHA = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"

func scanServer(t *testing.T, reviews, comments []string) *httptest.Server {
	t.Helper()
	body := func(items []string) []map[string]string {
		out := []map[string]string{}
		for _, b := range items {
			out = append(out, map[string]string{"body": b})
		}
		return out
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			_ = json.NewEncoder(w).Encode(body(reviews))
		case strings.HasSuffix(r.URL.Path, "/comments"):
			_ = json.NewEncoder(w).Encode(body(comments))
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestReviewedMatchesBosunPublicationsByCommit(t *testing.T) {
	published := reviewMarker + "\n\n" + metadata("some-other-ref", scanSHA) + "\n\nLooks fine."
	failed := failureMarker + "\n\n" + metadata("pr-7", scanSHA) + "\n\nboom"
	quoted := "> " + published
	cases := map[string]struct {
		reviews, comments []string
		want              bool
	}{
		"a review under a different ref": {[]string{published}, nil, true},
		"a failure comment":              {nil, []string{failed}, true},
		"a quoted reply is not Bosun":    {nil, []string{quoted}, false},
		"another commit":                 {[]string{strings.Replace(published, scanSHA, strings.Repeat("0", 40), 1)}, nil, false},
		"nothing":                        {nil, nil, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := scanServer(t, c.reviews, c.comments)
			defer srv.Close()
			got, err := Scanner{APIBaseURL: srv.URL}.Reviewed(context.Background(), "t", "acme/repo", 7, scanSHA)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Reviewed = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReviewedRejectsMalformedInput(t *testing.T) {
	if _, err := (Scanner{}).Reviewed(context.Background(), "t", "../x", 1, scanSHA); err == nil {
		t.Error("accepted an invalid repository")
	}
	if _, err := (Scanner{}).Reviewed(context.Background(), "t", "acme/repo", 1, "main"); err == nil {
		t.Error("accepted a non-commit")
	}
}

// Round 2: an eligible pull request past the first page must not be invisible
// to every pass. Listing pages until the lookback boundary.
func TestOpenPullRequestsPagesUntilTheLookbackBoundary(t *testing.T) {
	now := time.Now()
	page := func(start int, age time.Duration) []map[string]any {
		out := []map[string]any{}
		for i := 0; i < 100; i++ {
			out = append(out, map[string]any{"number": start + i, "updated_at": now.Add(-age).Format(time.RFC3339), "head": map[string]string{"ref": "b", "sha": scanSHA}})
		}
		return out
	}
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		switch r.URL.Query().Get("page") {
		case "1":
			_ = json.NewEncoder(w).Encode(page(1, time.Hour))
		case "2":
			_ = json.NewEncoder(w).Encode(page(101, 2*time.Hour))
		default:
			_ = json.NewEncoder(w).Encode(page(201, 72*time.Hour))
		}
	}))
	defer srv.Close()
	prs, err := Scanner{APIBaseURL: srv.URL}.OpenPullRequests(context.Background(), "t", "acme/repo", now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 200 {
		t.Errorf("got %d pull requests, want the 200 inside the window", len(prs))
	}
	if pages != 3 {
		t.Errorf("fetched %d pages, want to stop at the first page past the boundary", pages)
	}
}
