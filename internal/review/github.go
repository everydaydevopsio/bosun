package review

import (
	"bytes"
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
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var commitPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

func validRepository(repo string) bool {
	if !repositoryPattern.MatchString(repo) {
		return false
	}
	for _, part := range strings.Split(repo, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

type githubClient struct {
	base string
	http *http.Client
}

func (g githubClient) request(ctx context.Context, method, path, token string, body, result any) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(g.base, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GitHub %s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(result)
	}
	return nil
}
func appJWT(appID, key string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(key, `\n`, "\n")))
	if block == nil {
		return "", fmt.Errorf("invalid GitHub App PEM key")
	}
	var private *rsa.PrivateKey
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		private = k
	} else {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return "", fmt.Errorf("invalid GitHub App private key")
		}
		var ok bool
		private, ok = k.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("GitHub App key must be RSA")
		}
	}
	claims, _ := json.Marshal(map[string]any{"iat": now.Unix() - 60, "exp": now.Unix() + 540, "iss": appID})
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, private, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
func (g githubClient) token(ctx context.Context, repo string) (string, error) {
	fallback := os.Getenv("GITHUB_TOKEN")
	id, key := os.Getenv("BOSUN_GITHUB_APP_ID"), os.Getenv("BOSUN_GITHUB_PRIVATE_KEY")
	if path := os.Getenv("BOSUN_GITHUB_PRIVATE_KEY_FILE"); key == "" && path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read GitHub App key: %w", err)
		}
		key = string(b)
	}
	if id != "" || key != "" {
		token, err := g.installationToken(ctx, repo, id, key)
		if err == nil {
			return token, nil
		}
		if fallback == "" {
			return "", err
		}
		slog.Warn("GitHub App authentication failed; falling back to GITHUB_TOKEN")
	}
	if fallback == "" {
		return "", fmt.Errorf("GitHub reviews require GITHUB_TOKEN or GitHub App credentials")
	}
	return fallback, nil
}
func (g githubClient) installationToken(ctx context.Context, repo, id, key string) (string, error) {
	if id == "" || key == "" {
		return "", fmt.Errorf("GitHub App ID and private key are both required")
	}
	bearer, err := appJWT(id, key, time.Now())
	if err != nil {
		return "", err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err = g.request(ctx, "GET", "/repos/"+repo+"/installation", bearer, nil, &installation); err != nil {
		return "", err
	}
	if installation.ID <= 0 {
		return "", fmt.Errorf("GitHub returned an invalid installation ID")
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{strings.Split(repo, "/")[1]}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
	if err = g.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), bearer, body, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("GitHub returned an empty installation token")
	}
	slog.Info("minted installation token", "repo", repo, "installation", installation.ID, "expires_at", response.ExpiresAt)
	return response.Token, nil
}

// RepositoryToken is an installation token scoped to one repository, with the
// moment it stops working.
type RepositoryToken struct {
	Value     string
	ExpiresAt time.Time
}

// IssueRepositoryToken mints a token for a single repository.
//
// It exists so the controller can do this instead of the reviewer. A reviewer
// Job runs an AI agent over the contents of a pull request -- attacker-supplied
// input, read by design -- so the App private key must not be in it. The key
// can mint a token for every repository the App is installed on; this token can
// read one repository and comment on its pull requests, and it expires.
func IssueRepositoryToken(ctx context.Context, apiBaseURL string, client *http.Client, repo, appID, key string) (RepositoryToken, error) {
	if !validRepository(repo) {
		return RepositoryToken{}, fmt.Errorf("invalid repository name")
	}
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	g := githubClient{base: apiBaseURL, http: client}
	bearer, err := appJWT(appID, key, time.Now())
	if err != nil {
		return RepositoryToken{}, err
	}
	var installation struct {
		ID int64 `json:"id"`
	}
	if err = g.request(ctx, "GET", "/repos/"+repo+"/installation", bearer, nil, &installation); err != nil {
		return RepositoryToken{}, err
	}
	if installation.ID <= 0 {
		return RepositoryToken{}, fmt.Errorf("GitHub returned an invalid installation ID")
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{strings.Split(repo, "/")[1]}, "permissions": map[string]string{"contents": "read", "pull_requests": "write"}}
	if err = g.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), bearer, body, &response); err != nil {
		return RepositoryToken{}, err
	}
	if response.Token == "" {
		return RepositoryToken{}, fmt.Errorf("GitHub returned an empty installation token")
	}
	return RepositoryToken{Value: response.Token, ExpiresAt: response.ExpiresAt}, nil
}
func (g githubClient) resolvePR(ctx context.Context, repo string, number int, token string) (string, string, error) {
	var pr struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := g.request(ctx, "GET", fmt.Sprintf("/repos/%s/pulls/%d", repo, number), token, nil, &pr); err != nil {
		return "", "", err
	}
	if pr.Head.Ref == "" || !commitPattern.MatchString(pr.Head.SHA) {
		return "", "", fmt.Errorf("GitHub returned an invalid pull request head")
	}
	return pr.Head.Ref, pr.Head.SHA, nil
}

// Marker lines identify Bosun's output on a pull request. A consumer selects
// comments by these prefixes, so treat them as an interface: changing one is a
// breaking change for every agent driving a review loop.
const (
	reviewMarker  = "## Bosun code review"
	failureMarker = "## Bosun code review failed"
)

// failureReasonLimit keeps a runaway diagnostic from becoming a comment GitHub
// rejects. Reasons are one sentence in practice; this is a backstop.
const failureReasonLimit = 4000

// metadata is the correlation line every Bosun publication carries. A branch
// moves, so output that does not name its revision can only be matched to a
// push by timestamp, and two pushes in quick succession are ambiguous.
func metadata(ref, sha string) string {
	return "<sub>`" + codeSpan(ref) + "` @ `" + codeSpan(sha) + "`</sub>"
}

// codeSpan makes caller-supplied text safe to render inside a Markdown code
// span: a backtick would close the span early and let the remainder escape
// into markup, and a newline would end it outright.
func codeSpan(text string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch r {
		case '`':
			return '\''
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, text))
}

func (g githubClient) postComment(ctx context.Context, repo string, number int, token, body string) error {
	return g.request(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number), token, map[string]string{"body": body}, nil)
}

func (g githubClient) postReview(ctx context.Context, repo string, number int, token, ref, sha, output string) error {
	text := []rune(output)
	if len(text) > 60000 {
		text = append(text[:60000], []rune("\n\n[Review truncated]")...)
	}
	return g.postComment(ctx, repo, number, token, reviewMarker+"\n\n"+metadata(ref, sha)+"\n\n"+string(text))
}

// postFailure tells the pull request that a review died. The hosted path is
// fire-and-forget, so without it silence is the default experience of failure
// and a failed review is indistinguishable from one that never ran.
//
// reason must already be redacted: it reaches a pull request verbatim.
func (g githubClient) postFailure(ctx context.Context, repo string, number int, token, ref, sha, reason string) error {
	text := []rune(codeSpan(reason))
	if len(text) > failureReasonLimit {
		text = append(text[:failureReasonLimit], []rune(" [truncated]")...)
	}
	if len(text) == 0 {
		text = []rune("no reason reported")
	}
	return g.postComment(ctx, repo, number, token, failureMarker+"\n\n"+metadata(ref, sha)+"\n\n`"+string(text)+"`")
}
