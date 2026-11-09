package review

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
