package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
)

// Worker dependencies are replaceable for offline end-to-end tests. Production
// uses the fixed GitHub endpoints and the bridgectl client.
type Worker struct {
	APIBaseURL, CloneBaseURL, PromptPath string
	HTTPClient                           *http.Client
	Agent                                func(context.Context, string, string, string, time.Duration) (string, error)
	Output                               io.Writer
	Events                               io.Writer
}

func Run(ctx context.Context, cfg config.Config) error { return (Worker{}).Run(ctx, cfg) }
func (w Worker) Run(ctx context.Context, cfg config.Config) (result error) {
	sanitize := diagnosticRedactor()
	var reporter *progress.Reporter
	if os.Getenv("BOSUN_EVENTS") == "1" {
		events := w.Events
		if events == nil {
			events = os.Stdout
		}
		if path := os.Getenv("BOSUN_EVENT_FILE"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return fmt.Errorf("open durable event log: %w", err)
			}
			defer file.Close()
			events = io.MultiWriter(events, file)
		}
		reporter = progress.New(events, os.Getenv("BOSUN_RUN_ID"))
		ctx = progress.With(ctx, reporter)
		heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
		heartbeatDone := make(chan struct{})
		defer func() { stopHeartbeat(); <-heartbeatDone }()
		go func() { defer close(heartbeatDone); reporter.Heartbeat(heartbeatCtx) }()
	}
	reporter.Emit("stage", "preparing repository")
	defer func() {
		if result != nil {
			kind := "failed"
			if errors.Is(result, context.DeadlineExceeded) {
				kind = "timed_out"
			}
			if errors.Is(result, context.Canceled) {
				kind = "cancelled"
			}
			reporter.Emit(kind, sanitize(result.Error()))
		}
	}()
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
			result = safeError{text: sanitize(redact(result.Error(), token)), cause: result}
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
	if base := os.Getenv("BOSUN_BASE_SHA"); base != "" {
		prompt += "Compare the merge base of " + base + " and HEAD to HEAD. Base commit: " + base + "\n"
	}
	if local && os.Getenv("BOSUN_COMMITTED_ONLY") != "1" {
		prompt += "Include staged, unstaged, and untracked working-tree changes in this local review.\n"
	}
	reporter.Emit("stage", "starting provider")
	output, err := w.Agent(ctx, workspace, cfg.ReviewProvider, prompt, max)
	if err != nil {
		return err
	}
	// sanitize as well as redact: redact knows the GitHub token, but provider
	// credentials only exist in the environment. A prompt-injected agent that
	// echoes one would otherwise have it published to the pull request.
	output = sanitize(redact(output, token))
	// Before publishing or reporting success: a refusal must fail the Job, not
	// reach a pull request and not be recorded as a completed review.
	if err := checkReviewed(output); err != nil {
		return err
	}
	output = withoutMarker(output)
	if !local && req.PRNumber > 0 {
		reporter.Emit("stage", "publishing")
		if err := github.postReview(ctx, req.Repo, req.PRNumber, token, output); err != nil {
			return fmt.Errorf("publish review: %w", err)
		}
	}
	if reporter != nil {
		reporter.Emit("result", output)
		reporter.Emit("completed", "Review completed")
		return nil
	}
	_, err = fmt.Fprintln(w.Output, output)
	return err
}

// Preserve cancellation identity without reintroducing unredacted text.
type safeError struct {
	text  string
	cause error
}

func (e safeError) Error() string { return e.text }
func (e safeError) Unwrap() error { return e.cause }
func diagnosticRedactor() func(string) string {
	var values []string
	for _, key := range []string{"GITHUB_TOKEN", "BOSUN_GITHUB_PRIVATE_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "CODEX_AUTH", "CLAUDE_CREDENTIALS"} {
		if v := os.Getenv(key); v != "" {
			values = append(values, v)
			var object any
			if json.Unmarshal([]byte(v), &object) == nil {
				var walk func(any)
				walk = func(x any) {
					switch x := x.(type) {
					case string:
						if len(x) > 6 {
							values = append(values, x)
						}
					case map[string]any:
						for _, v := range x {
							walk(v)
						}
					case []any:
						for _, v := range x {
							walk(v)
						}
					}
				}
				walk(object)
			}
		}
	}
	return func(s string) string {
		for _, v := range values {
			s = strings.ReplaceAll(s, v, "[REDACTED]")
		}
		return s
	}
}
