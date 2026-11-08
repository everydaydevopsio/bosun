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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
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

// httpError carries the status code so a caller can act on it. The message
// deliberately omits the response body: GitHub echoes request material, and a
// publish failure must not reintroduce a credential into a diagnostic.
type httpError struct {
	status       int
	method, path string
}

func (e httpError) Error() string {
	return fmt.Sprintf("GitHub %s %s returned HTTP %d", e.method, e.path, e.status)
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
		return httpError{status: resp.StatusCode, method: method, path: path}
	}
	if result != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(result); err != nil {
			return decodeError{err}
		}
	}
	return nil
}

// decodeError separates "the request did not happen" from "the request
// happened and the reply was unreadable". A caller that only wanted a field
// out of the body can ignore it; one that needs the body must not.
type decodeError struct{ err error }

func (e decodeError) Error() string { return "decode GitHub response: " + e.err.Error() }
func (e decodeError) Unwrap() error { return e.err }
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
		slog.Warn("GitHub App authentication failed; falling back to GITHUB_TOKEN", "repo", repo, "reason", err)
	}
	if fallback == "" {
		return "", fmt.Errorf("GitHub reviews require GITHUB_TOKEN or GitHub App credentials")
	}
	return fallback, nil
}

// lookupInstallation resolves the installation covering repo.
//
// A 404 here means one specific thing: the App authenticated, and is not
// installed on that repository. It is the most common way a correctly
// configured deployment still fails, and returning the transport error
// unchanged makes it indistinguishable from any other 404 -- a wrong owner, a
// renamed repository, a revoked key. Observed on a real deployment: the
// webhook returned HTTP 500 and diagnosing it meant minting an App JWT by
// hand to replay this one call.
//
// The granted permissions come back on the same call. They decide whether the
// token may ask for `checks` -- requesting a permission the installation does
// not hold makes GitHub reject the whole mint with 422, which would break
// every install that has not opted into Checks. See tokenPermissions.
func (g githubClient) lookupInstallation(ctx context.Context, repo, bearer string) (installation, error) {
	var found installation
	if err := g.request(ctx, "GET", "/repos/"+repo+"/installation", bearer, nil, &found); err != nil {
		var status httpError
		if errors.As(err, &status) && status.status == http.StatusNotFound {
			return installation{}, fmt.Errorf("the GitHub App is not installed on %s", repo)
		}
		return installation{}, err
	}
	if found.ID <= 0 {
		return installation{}, fmt.Errorf("GitHub returned an invalid installation ID")
	}
	return found, nil
}

// installation is the App installation covering one repository, with the
// permissions the operator granted it.
type installation struct {
	ID          int64             `json:"id"`
	Permissions map[string]string `json:"permissions"`
}

func (g githubClient) installationToken(ctx context.Context, repo, id, key string) (string, error) {
	if id == "" || key == "" {
		return "", fmt.Errorf("GitHub App ID and private key are both required")
	}
	bearer, err := appJWT(id, key, time.Now())
	if err != nil {
		return "", err
	}
	app, err := g.lookupInstallation(ctx, repo, bearer)
	if err != nil {
		return "", err
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{strings.Split(repo, "/")[1]}, "permissions": tokenPermissions(app.Permissions)}
	if err = g.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", app.ID), bearer, body, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", fmt.Errorf("GitHub returned an empty installation token")
	}
	slog.Info("minted installation token", "repo", repo, "installation", app.ID, "expires_at", response.ExpiresAt)
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
	app, err := g.lookupInstallation(ctx, repo, bearer)
	if err != nil {
		return RepositoryToken{}, err
	}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]any{"repositories": []string{strings.Split(repo, "/")[1]}, "permissions": tokenPermissions(app.Permissions)}
	if err = g.request(ctx, "POST", fmt.Sprintf("/app/installations/%d/access_tokens", app.ID), bearer, body, &response); err != nil {
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

// ResolvePullRequest returns a pull request's head branch and commit.
//
// A comment-triggered review is created knowing only a pull-request number, so
// the identity has to be looked up before anything can name what was reviewed.
// The reviewer does this for itself; the controller does it too, so the Job it
// creates records the same answer rather than leaving it in the reviewer's
// memory where nothing else can reach it.
func ResolvePullRequest(ctx context.Context, apiBaseURL string, client *http.Client, repo string, number int, token string) (string, string, error) {
	if !validRepository(repo) {
		return "", "", fmt.Errorf("invalid repository name")
	}
	if apiBaseURL == "" {
		apiBaseURL = "https://api.github.com"
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return githubClient{base: apiBaseURL, http: client}.resolvePR(ctx, repo, number, token)
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

// postReview publishes every review, with or without findings, as a
// pull-request review; findings become inline comments.
//
// Review threads are what a consuming agent needs: a conversation comment has
// nothing to reply to and nothing to resolve, so the record of which findings
// were handled lives only in that agent's context and cannot survive a fresh
// session. Output with no parsable findings is still a review -- losing one
// because a model skipped the output contract would be a worse failure than
// publishing it unanchored.
//
// A clean review goes to the same surface. It used to be an issue comment,
// which inverted the answer for any check that read only one surface (clean
// reviews visible, findings hidden), and the fix for that was a pointer comment
// beside every findings review: two messages for one review. One surface for
// every outcome needs neither.
func (g githubClient) postReview(ctx context.Context, repo string, number int, token, ref, sha, output string) error {
	summary, findings := parseFindings(output)
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number)
	// A clean review has nothing to anchor, so it has no position GitHub can
	// reject: publish it whole and skip the file lookup.
	if len(findings) == 0 {
		return g.request(ctx, "POST", path, token, commentReview(sha, reviewBody(ref, sha, summary, "", nil), nil), nil)
	}
	// A finding on a file the pull request does not touch cannot be a thread.
	// An unavailable file list is not a reason to lose the review: publish
	// unfiltered and let the fallback below handle a rejected position.
	inline, outside := findings, []Finding(nil)
	if files, err := g.prFiles(ctx, repo, number, token); err == nil {
		inline, outside = nil, nil
		for _, f := range findings {
			if files[f.Path] {
				inline = append(inline, f)
			} else {
				outside = append(outside, f)
			}
		}
	}
	// The reply is never read. GitHub's status says whether the review exists;
	// decoding the body would let a truncated reply after a 2xx report a
	// visibly published review as failed, and Worker.Run would then add a
	// failure comment beside it -- the "never both" that GO-4 forbids.
	err := g.request(ctx, "POST", path, token, commentReview(sha, reviewBody(ref, sha, summary, "Findings outside the diff", outside), inlineComments(inline)), nil)
	var status httpError
	if !errors.As(err, &status) || status.status != http.StatusUnprocessableEntity {
		return err
	}
	// GitHub rejects the whole review when any single comment position is
	// invalid, and a line the reviewer read is not necessarily a line the diff
	// exposes. Retry once with everything in the body rather than lose it.
	//
	// commit_id is kept: with no positions to place, it is the record of what
	// was read. A rejection because the commit itself left the pull request --
	// a force-push mid-review -- fails here, and that is the right outcome,
	// because the push that removed it has already triggered its own review.
	return g.request(ctx, "POST", path, token, commentReview(sha, reviewBody(ref, sha, summary, "Findings", findings), nil), nil)
}

// commentReview is a COMMENT review anchored to the commit that was read.
// GitHub defaults commit_id to the pull request's latest commit, so a push
// landing mid-review would attach the review to lines it never described,
// under a summary naming a different SHA. The event is never APPROVE or
// REQUEST_CHANGES: a verdict is not Bosun's to give, and an approval from the
// App could satisfy branch protection.
func commentReview(sha, body string, comments []map[string]any) map[string]any {
	if comments == nil {
		comments = []map[string]any{}
	}
	return map[string]any{
		"event":     "COMMENT",
		"commit_id": sha,
		"body":      body,
		"comments":  comments,
	}
}

// prFiles lists the paths the pull request touches.
func (g githubClient) prFiles(ctx context.Context, repo string, number int, token string) (map[string]bool, error) {
	files := map[string]bool{}
	for page := 1; page <= 10; page++ {
		var batch []struct {
			Filename string `json:"filename"`
		}
		path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, number, page)
		if err := g.request(ctx, "GET", path, token, nil, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			files[f.Filename] = true
		}
		if len(batch) < 100 {
			break
		}
	}
	return files, nil
}

func inlineComments(findings []Finding) []map[string]any {
	comments := make([]map[string]any, 0, len(findings))
	for _, f := range findings {
		comment := map[string]any{"path": f.Path, "line": f.Line, "side": "RIGHT", "body": findingBody(f)}
		if f.EndLine > f.Line {
			comment["line"], comment["start_line"] = f.EndLine, f.Line
			comment["start_side"] = "RIGHT"
		}
		comments = append(comments, comment)
	}
	return comments
}

// findingBody opens with the severity so a reader, and a consuming agent
// scoring threads, can triage without parsing prose.
func findingBody(f Finding) string {
	body := "**" + f.Severity + "** — " + f.Title
	if f.Body != "" {
		body += "\n\n" + f.Body
	}
	if f.Remediation != "" {
		body += "\n\nRemediation: " + f.Remediation
	}
	return body
}

// reviewBody renders the summary, plus any findings that could not become
// threads, under the heading the caller names.
func reviewBody(ref, sha, summary, heading string, folded []Finding) string {
	body := summary
	if len(folded) > 0 {
		body = strings.TrimRight(body, " \t\n") + "\n\n### " + heading + "\n"
		for _, f := range folded {
			body += "\n`" + codeSpan(location(f)) + "` — " + findingBody(f) + "\n"
		}
	}
	return reviewMarker + "\n\n" + metadata(ref, sha) + "\n\n" + truncateOutput(body)
}

func location(f Finding) string {
	at := f.Path + ":" + strconv.Itoa(f.Line)
	if f.EndLine > f.Line {
		at += "-" + strconv.Itoa(f.EndLine)
	}
	return at
}

// truncateOutput bounds what reaches GitHub. A review body has a hard limit
// there, and a truncated review is better than a rejected one.
func truncateOutput(output string) string {
	text := []rune(output)
	if len(text) > 60000 {
		text = append(text[:60000], []rune("\n\n[Review truncated]")...)
	}
	return string(text)
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
