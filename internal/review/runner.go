package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
	"github.com/everydaydevopsio/bosun/internal/version"
	"github.com/google/uuid"
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

// HostedWorkspaceRoot is the only /tmp subtree bridgectl may use for hosted
// review sessions. Keep it in sync with config/bridge-bosun.yaml.
const HostedWorkspaceRoot = "/tmp/bosun-review"

func makeHostedWorkspace() (string, error) {
	if err := os.MkdirAll(HostedWorkspaceRoot, 0700); err != nil {
		return "", err
	}
	return os.MkdirTemp(HostedWorkspaceRoot, "ws-")
}

func (w Worker) Run(ctx context.Context, cfg config.Config) (result error) {
	sanitize := diagnosticRedactor()
	var reporter *progress.Reporter
	started := time.Now()
	runID := os.Getenv("BOSUN_RUN_ID")
	if runID == "" {
		runID = uuid.NewString()
	}
	req := Request{Repo: os.Getenv("BOSUN_REPO"), Ref: os.Getenv("BOSUN_REF"), SHA: os.Getenv("BOSUN_SHA"), Trigger: os.Getenv("BOSUN_TRIGGER")}
	meter := newUsageRecorder(cfg.ReviewProvider)
	ctx = context.WithValue(ctx, usageKey{}, meter)
	credential := credentialMode(cfg.ReviewProvider)
	providerStarted, metricsEmitted := false, false
	var providerSeconds float64
	emitUsage := func() {
		if metricsEmitted {
			return
		}
		metricsEmitted = true
		now := time.Now()
		summary := UsageSummary{
			SchemaVersion: 1, RunID: sanitize(runID), PodUID: sanitize(os.Getenv("BOSUN_POD_UID")),
			Repo: sanitize(req.Repo), Ref: sanitize(req.Ref), HeadSHA: sanitize(req.SHA),
			BaseSHA: sanitize(os.Getenv("BOSUN_BASE_SHA")), PRNumber: req.PRNumber, Trigger: sanitize(req.Trigger),
			Provider: sanitize(cfg.ReviewProvider), WorkerVersion: version.Version,
			WorkerImage: sanitize(os.Getenv("BOSUN_WORKER_IMAGE")), CredentialMode: credential,
			Outcome: reviewOutcome(result), StartedAt: started.UTC(), FinishedAt: now.UTC(),
			WorkerSeconds: now.Sub(started).Seconds(), ProviderSeconds: providerSeconds,
			ProviderStarted: providerStarted, CostStatus: "unavailable", Usage: sanitizedUsage(meter.usage, sanitize),
		}
		var failure *Failure
		if errors.As(result, &failure) {
			summary.FailureStage = failure.Stage
		}
		if summary.FailureStage == StageProvider && summary.Usage.Status != "missing" {
			summary.Usage.Status = "partial"
		}
		if summary.Usage.ProviderReportedCostUSD != nil {
			summary.CostStatus = "provider_reported_estimate"
		}
		slog.Info("review usage", "event", "bosun.review.usage", "metrics", summary)
		encoded, _ := json.Marshal(summary)
		reporter.Emit("usage", string(encoded))
	}
	defer emitUsage()
	if os.Getenv("BOSUN_EVENTS") == "1" {
		events := w.Events
		if events == nil {
			events = os.Stdout
		}
		if path := os.Getenv("BOSUN_EVENT_FILE"); path != "" {
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				return failf(StageSetup, "open durable event log: %w", err)
			}
			defer file.Close()
			events = io.MultiWriter(events, file)
		}
		reporter = progress.New(events, runID)
		ctx = progress.With(ctx, reporter)
		heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
		heartbeatDone := make(chan struct{})
		defer func() { stopHeartbeat(); <-heartbeatDone }()
		go func() { defer close(heartbeatDone); reporter.Heartbeat(heartbeatCtx) }()
	}
	reporter.Emit("stage", "preparing repository")
	defer func() {
		if result != nil {
			emitUsage()
			kind := "failed"
			if errors.Is(result, context.DeadlineExceeded) {
				kind = "timed_out"
			}
			if errors.Is(result, context.Canceled) {
				kind = "cancelled"
			}
			reporter.Emit(kind, sanitize(Describe(result)))
		}
	}()
	max := time.Duration(cfg.ReviewTimeoutSeconds) * time.Second
	if max <= 0 {
		return failf(StageSetup, "review timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	if req.Repo == "" || req.Ref == "" {
		return failf(StageSetup, "BOSUN_REPO and BOSUN_REF are required")
	}
	if number := os.Getenv("BOSUN_PR_NUMBER"); number != "" {
		n, err := strconv.Atoi(number)
		if err != nil || n < 0 {
			return failf(StageSetup, "invalid BOSUN_PR_NUMBER")
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
	published := false
	// Registered before the redaction defer so it runs after it: defers unwind
	// in reverse, and this comment must carry the sanitized text rather than
	// the raw error, which can quote credentials.
	defer func() {
		// published guards the one-terminal-publication invariant: a review
		// that landed must not be contradicted by a failure comment when a
		// later step, such as writing the output stream, fails.
		if result == nil || published || local || req.PRNumber <= 0 || token == "" {
			return
		}
		// A timed-out review has an already-cancelled context, and that is
		// exactly the failure a consumer most needs to see, so the comment
		// gets its own deadline.
		publish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer stop()
		if err := github.postFailure(publish, req.Repo, req.PRNumber, token, req.Ref, req.SHA, result.Error()); err != nil {
			// Never mask the review failure with the failure of reporting it.
			slog.Warn("publish review failure comment", "repo", req.Repo, "pr", req.PRNumber, "error", sanitize(redact(err.Error(), token)))
		}
	}()
	defer func() {
		if result != nil {
			result = safeError{text: sanitize(redact(result.Error(), token)), cause: result}
		}
	}()
	if !local {
		if !validRepository(req.Repo) {
			return failf(StageSetup, "invalid repository name")
		}
		var err error
		token, err = github.token(ctx, req.Repo)
		if err != nil {
			return fail(StageSetup, err)
		}
		if req.PRNumber > 0 && (req.SHA == "" || req.Trigger == "comment") {
			req.Ref, req.SHA, err = github.resolvePR(ctx, req.Repo, req.PRNumber, token)
			if err != nil {
				return fail(StageClone, err)
			}
		}
		workspace, err = makeHostedWorkspace()
		if err != nil {
			return fail(StageSetup, err)
		}
		defer os.RemoveAll(workspace)
		// Assign only on success: a failed clone returns an empty SHA, and
		// overwriting the commit the webhook supplied would leave the failure
		// comment unable to name what it tried to review.
		cloned, err := cloneRepository(ctx, w.CloneBaseURL, workspace, req, token)
		if err != nil {
			return fail(StageClone, err)
		}
		req.SHA = cloned
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return failf(StageSetup, "review path is not a git repository: %w", err)
	}
	if err := materialiseCredentials(); err != nil {
		return fail(StageSetup, err)
	}
	instructions, err := os.ReadFile(w.PromptPath)
	if err != nil {
		return failf(StageSetup, "read review prompt: %w", err)
	}
	prompt := fmt.Sprintf("%s\nRepository: %s\nBranch: %s\nCommit: %s\n", instructions, req.Repo, req.Ref, req.SHA)
	if base := os.Getenv("BOSUN_BASE_SHA"); base != "" {
		prompt += "Compare the merge base of " + base + " and HEAD to HEAD. Base commit: " + base + "\n"
	}
	if local && os.Getenv("BOSUN_COMMITTED_ONLY") != "1" {
		prompt += "Include staged, unstaged, and untracked working-tree changes in this local review.\n"
	}
	reporter.Emit("stage", "starting provider")
	providerStarted = true
	providerStart := time.Now()
	output, err := w.Agent(ctx, workspace, cfg.ReviewProvider, prompt, max)
	providerSeconds = time.Since(providerStart).Seconds()
	output = meter.filter(output)
	if err != nil {
		return fail(agentFailureStage(err), err)
	}
	// sanitize as well as redact: redact knows the GitHub token, but provider
	// credentials only exist in the environment. A prompt-injected agent that
	// echoes one would otherwise have it published to the pull request.
	output = sanitize(redact(output, token))
	// Before publishing or reporting success: a refusal must fail the Job, not
	// reach a pull request and not be recorded as a completed review.
	if err := checkReviewed(output); err != nil {
		return fail(StageReview, err)
	}
	output = withoutMarker(output)
	if !local && req.PRNumber > 0 {
		reporter.Emit("stage", "publishing")
		if err := github.postReview(ctx, req.Repo, req.PRNumber, token, req.Ref, req.SHA, output); err != nil {
			return failf(StagePublish, "publish review: %w", err)
		}
		published = true
	}
	if reporter != nil {
		emitUsage()
		// The structured half goes out before the result so a consumer that
		// stops at the first terminal event still has the findings.
		if structured := StructuredResult(output); structured != "" {
			reporter.Emit("findings", structured)
		}
		reporter.Emit("result", humanReview(output))
		reporter.Emit("completed", "Review completed")
		return nil
	}
	_, err = fmt.Fprintln(w.Output, humanReview(output))
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
