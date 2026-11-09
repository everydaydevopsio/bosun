package review

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Scanner reads what the review sweep needs from GitHub with the App's own
// credentials: which repositories the App is installed on, which pull requests
// are open there, and whether Bosun has already published for a head commit.
//
// It never writes. The sweep that uses it submits through the same path a
// webhook delivery does, so a review it starts is minted a repository-scoped
// token and runs exactly like any other.
type Scanner struct {
	APIBaseURL string
	HTTP       *http.Client
	AppID, Key string
}

// Installation is one installation of the App.
type Installation struct {
	ID          int64             `json:"id"`
	Permissions map[string]string `json:"permissions"`
	Account     struct {
		Login string `json:"login"`
	} `json:"account"`
}

// OpenPullRequest is the part of an open pull request the sweep decides on.
type OpenPullRequest struct {
	Repo      string
	Number    int
	Ref, SHA  string
	Draft     bool
	UpdatedAt time.Time
}

// scanPages bounds every listing, so one enormous installation cannot turn a
// sweep pass into an unbounded walk of the API.
const scanPages = 10

func (s Scanner) client() githubClient {
	base := s.APIBaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	h := s.HTTP
	if h == nil {
		h = &http.Client{Timeout: 30 * time.Second}
	}
	return githubClient{base: base, http: h}
}

// Installations lists every installation of the App.
func (s Scanner) Installations(ctx context.Context) ([]Installation, error) {
	bearer, err := appJWT(s.AppID, s.Key, time.Now())
	if err != nil {
		return nil, err
	}
	var out []Installation
	for page := 1; page <= scanPages; page++ {
		var items []Installation
		if err := s.client().request(ctx, "GET", fmt.Sprintf("/app/installations?per_page=100&page=%d", page), bearer, nil, &items); err != nil {
			return nil, err
		}
		out = append(out, items...)
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

// Token mints a read-only token for one installation. It asks only for the
// read half of what the installation was granted, so the sweep cannot write
// anything even though it covers every repository in the installation.
func (s Scanner) Token(ctx context.Context, inst Installation) (string, error) {
	bearer, err := appJWT(s.AppID, s.Key, time.Now())
	if err != nil {
		return "", err
	}
	permissions := map[string]string{}
	for _, name := range []string{"metadata", "pull_requests", "issues"} {
		if inst.Permissions[name] != "" {
			permissions[name] = "read"
		}
	}
	var response struct {
		Token string `json:"token"`
	}
	if err := s.client().request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", inst.ID), bearer, map[string]any{"permissions": permissions}, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("GitHub returned an empty installation token")
	}
	return response.Token, nil
}

// Repositories lists the repositories an installation token covers.
func (s Scanner) Repositories(ctx context.Context, token string) ([]string, error) {
	var out []string
	for page := 1; page <= scanPages; page++ {
		var body struct {
			Repositories []struct {
				FullName string `json:"full_name"`
				Archived bool   `json:"archived"`
			} `json:"repositories"`
		}
		if err := s.client().request(ctx, "GET", fmt.Sprintf("/installation/repositories?per_page=100&page=%d", page), token, nil, &body); err != nil {
			return nil, err
		}
		for _, r := range body.Repositories {
			if !r.Archived && validRepository(r.FullName) {
				out = append(out, r.FullName)
			}
		}
		if len(body.Repositories) < 100 {
			break
		}
	}
	return out, nil
}

// OpenPullRequests lists a repository's open pull requests, most recently
// updated first. One page: the sweep only looks back a bounded window, and a
// repository with more than a hundred pull requests updated inside it is
// already beyond what a single pass should start reviews for.
func (s Scanner) OpenPullRequests(ctx context.Context, token, repo string) ([]OpenPullRequest, error) {
	if !validRepository(repo) {
		return nil, fmt.Errorf("invalid repository name")
	}
	var items []struct {
		Number    int       `json:"number"`
		Draft     bool      `json:"draft"`
		UpdatedAt time.Time `json:"updated_at"`
		Head      struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := s.client().request(ctx, "GET", "/repos/"+repo+"/pulls?state=open&sort=updated&direction=desc&per_page=100", token, nil, &items); err != nil {
		return nil, err
	}
	out := make([]OpenPullRequest, 0, len(items))
	for _, p := range items {
		out = append(out, OpenPullRequest{Repo: repo, Number: p.Number, Ref: p.Head.Ref, SHA: p.Head.SHA, Draft: p.Draft, UpdatedAt: p.UpdatedAt})
	}
	return out, nil
}

// Reviewed reports whether Bosun has published anything -- a review or a
// failure -- for this head commit of the pull request.
//
// It matches on the commit alone, not the ref beside it: a review requested
// by comment can name its ref differently from the branch the sweep sees, and
// a mismatch here costs a whole duplicate review.
func (s Scanner) Reviewed(ctx context.Context, token, repo string, number int, sha string) (bool, error) {
	if !validRepository(repo) {
		return false, fmt.Errorf("invalid repository name")
	}
	if !commitPattern.MatchString(sha) {
		return false, fmt.Errorf("invalid commit")
	}
	want := "` @ `" + codeSpan(sha) + "`</sub>"
	g := s.client()
	for _, path := range []string{
		fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number),
		fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number),
	} {
		done := false
		for page := 1; page <= scanPages; page++ {
			var items []struct {
				Body string `json:"body"`
			}
			if err := g.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), token, nil, &items); err != nil {
				return false, err
			}
			for _, item := range items {
				body := strings.TrimSpace(item.Body)
				if (strings.HasPrefix(body, reviewMarker) || strings.HasPrefix(body, failureMarker)) && strings.Contains(body, want) {
					return true, nil
				}
			}
			if len(items) < 100 {
				done = true
				break
			}
		}
		if !done {
			// Not found in the bound is not "never published"; starting a
			// review on that guess would duplicate one past the boundary.
			return false, errSearchIncomplete
		}
	}
	return false, nil
}
