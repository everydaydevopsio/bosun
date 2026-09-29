package review

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

// Worker dependencies are replaceable for offline end-to-end tests. Production
// uses the fixed GitHub endpoints and the bridgectl client.
type Worker struct {
	APIBaseURL, CloneBaseURL, PromptPath string
	HTTPClient                           *http.Client
	Agent                                func(context.Context, string, string, string, time.Duration) (string, error)
	Output                               io.Writer
}

func Run(ctx context.Context, cfg config.Config) error { return (Worker{}).Run(ctx, cfg) }
func (w Worker) Run(ctx context.Context, cfg config.Config) (result error) {
	max := time.Duration(cfg.ReviewTimeoutSeconds) * time.Second
	if max <= 0 {
		return fmt.Errorf("review timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	req := Request{Repo: os.Getenv("BOSUN_REPO"), Ref: os.Getenv("BOSUN_REF"), SHA: os.Getenv("BOSUN_SHA"), Trigger: os.Getenv("BOSUN_TRIGGER")}
	if req.Repo == "" || req.Ref == "" {
		return fmt.Errorf("BOSUN_REPO and BOSUN_REF are required")
	}
	if number := os.Getenv("BOSUN_PR_NUMBER"); number != "" {
		n, err := strconv.Atoi(number)
		if err != nil || n < 0 {
			return fmt.Errorf("invalid BOSUN_PR_NUMBER")
		}
		req.PRNumber = n
	}
	if w.APIBaseURL == "" {
		w.APIBaseURL = "https://api.github.com"
	}
	if w.CloneBaseURL == "" {
		w.CloneBaseURL = "https://github.com"
	}
	if w.PromptPath == "" {
		w.PromptPath = os.Getenv("BOSUN_PROMPT_PATH")
	}
	if w.PromptPath == "" {
		w.PromptPath = "/app/bosun/prompts/code-review.md"
	}
	if w.HTTPClient == nil {
		w.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	client := *w.HTTPClient
	// Do not forward GitHub credentials to redirected hosts.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	github := githubClient{base: w.APIBaseURL, http: &client}
	if w.Agent == nil {
		w.Agent = runBridge
	}
	if w.Output == nil {
		w.Output = os.Stdout
	}
	workspace := os.Getenv("BOSUN_LOCAL_PATH")
	local := workspace != ""
	var token string
	defer func() {
		if result != nil {
			result = fmt.Errorf("%s", redact(result.Error(), token))
		}
	}()
	if !local {
		if !validRepository(req.Repo) {
			return fmt.Errorf("invalid repository name")
		}
		var err error
		token, err = github.token(ctx, req.Repo)
		if err != nil {
			return err
		}
		if req.PRNumber > 0 && (req.SHA == "" || req.Trigger == "comment") {
			req.Ref, req.SHA, err = github.resolvePR(ctx, req.Repo, req.PRNumber, token)
			if err != nil {
				return err
			}
		}
		workspace, err = os.MkdirTemp("", "bosun-review-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(workspace)
		req.SHA, err = cloneRepository(ctx, w.CloneBaseURL, workspace, req, token)
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return fmt.Errorf("review path is not a git repository: %w", err)
	}
	if err := materialiseCredentials(); err != nil {
		return err
	}
	instructions, err := os.ReadFile(w.PromptPath)
	if err != nil {
		return fmt.Errorf("read review prompt: %w", err)
	}
	prompt := fmt.Sprintf("%s\nRepository: %s\nBranch: %s\nCommit: %s\n", instructions, req.Repo, req.Ref, req.SHA)
	if local {
		prompt += "Include staged, unstaged, and untracked working-tree changes in this local review.\n"
	}
	output, err := w.Agent(ctx, workspace, cfg.ReviewProvider, prompt, max)
	if err != nil {
		return err
	}
	output = redact(output, token)
	if !local && req.PRNumber > 0 {
		if err := github.postReview(ctx, req.Repo, req.PRNumber, token, output); err != nil {
			return fmt.Errorf("publish review: %w", err)
		}
	}
	_, err = fmt.Fprintln(w.Output, output)
	return err
}
