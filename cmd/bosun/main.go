package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/everydaydevopsio/bosun/internal/localreview"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	"github.com/everydaydevopsio/bosun/internal/sentinel"
	"github.com/everydaydevopsio/bosun/internal/server"
	"github.com/everydaydevopsio/bosun/internal/sweep"
	"github.com/everydaydevopsio/bosun/internal/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const usage = `Usage: bosun review [PATH] [--branch REF] [--base REF] [--provider NAME] [--detach]
                           [--local-credentials] [--timeout 30m]
       bosun review-status JOB [--follow]
       bosun review-result JOB [--json] [--summary] [--against-ref REF]
       bosun init [--target claude,codex] [--force] [--print]
       bosun up [--local-credentials] [--cluster NAME]
       bosun down [--keep-snapshots] [--cluster NAME]
       bosun reset [--cluster NAME] [--provider NAME] [--purge-snapshots] [--force]
       bosun doctor [--cluster NAME] [--provider NAME] [--json]
       bosun serve
       bosun reviewer
       bosun version

bosun init installs the review skill for the coding agents on this machine, so
you can ask Claude Code or Codex to review your changes instead of running the
command yourself.

bosun review creates the local Kind cluster when it is missing, so bosun up is
only needed to prepare one ahead of time or to refresh credentials. bosun up
checks the selected provider credential in the cluster. Pass --local-credentials
to verify a headless provider request before copying this machine's sign-in;
bosun down removes the cluster and its snapshots.
bosun reset recreates the cluster and preserves snapshots by default.
bosun doctor checks local review readiness without changing the cluster.
`

// metaCommand handles the commands that need neither configuration nor a
// Kubernetes client, so a released binary can answer them anywhere. It reports
// whether args named one of them.
func metaCommand(args []string, stdout io.Writer) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return true
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "bosun %s\n", version.Version)
		return true
	}
	return false
}

type submitter struct {
	client kubernetes.Interface
	cfg    config.Config
}

// Submit routes a delivery to the narrowest credential available.
//
// With a GitHub App configured, the controller mints a token for the single
// repository under review and hands the Job only that. The App's private key
// stays here and never enters a Job -- it can mint tokens for every repository
// the App is installed on, and a reviewer Job reads attacker-supplied pull
// request content by design.
//
// Without an App, the shared secret path is unchanged, because a personal
// access token cannot be narrowed at submission time. Narrowing it is the
// user's job, through the token's own scopes.
func (s submitter) Submit(ctx context.Context, r review.Request, d string) (string, error) {
	r = resolveIdentity(ctx, r)
	issue := appTokenIssuer()
	if issue == nil {
		return jobs.Submit(ctx, s.client, s.cfg, r, d)
	}
	return jobs.SubmitAuthenticated(ctx, s.client, s.cfg, r, d, issue)
}

// appTokenIssuer mints repository-scoped tokens from the GitHub App, or is nil
// when no App is configured and Jobs read the shared secret instead.
func appTokenIssuer() jobs.TokenIssuer {
	appID, key := os.Getenv("BOSUN_GITHUB_APP_ID"), os.Getenv("BOSUN_GITHUB_PRIVATE_KEY")
	if appID == "" || key == "" {
		return nil
	}
	return func(ctx context.Context, repo string) (review.RepositoryToken, error) {
		return review.IssueRepositoryToken(ctx, os.Getenv("BOSUN_GITHUB_API_URL"), nil, repo, appID, key)
	}
}

// startDispatcher runs the loop that starts queued reviews as slots free up.
// Without it a review queued at capacity would wait until it expired.
func startDispatcher(ctx context.Context, client kubernetes.Interface, cfg config.Config) {
	if cfg.MaxQueuedReviews <= 0 {
		slog.Warn("review queue disabled; a delivery arriving while every slot is busy is refused with 503 and GitHub will not redeliver it",
			"logger", "bosun.queue")
		return
	}
	d := jobs.Dispatcher{
		Client: client, Config: cfg, Issue: appTokenIssuer(),
		Interval: time.Duration(cfg.QueueIntervalSeconds) * time.Second,
	}
	go d.Run(ctx)
}

// controllerToken mints a credential the controller can publish with.
//
// The App path is the same one submission uses and keeps the same property:
// the controller holds the key, mints narrowly on demand, and retains nothing
// per-review. The fallback exists because an install using a personal access
// token has no key to mint from; it publishes with the shared token when the
// operator has chosen to make it available, and otherwise cannot publish at
// all, which the caller reports rather than hides.
func controllerToken(ctx context.Context, repo string) (string, error) {
	appID, key := os.Getenv("BOSUN_GITHUB_APP_ID"), os.Getenv("BOSUN_GITHUB_PRIVATE_KEY")
	if appID != "" && key != "" {
		token, err := review.IssueRepositoryToken(ctx, os.Getenv("BOSUN_GITHUB_API_URL"), nil, repo, appID, key)
		if err != nil {
			return "", err
		}
		return token.Value, nil
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token, nil
	}
	return "", errors.New("the controller has no GitHub credential: configure a GitHub App, or give the controller the shared token")
}

// identityResolveBudget bounds the GitHub work done before admission. The
// webhook server's write timeout has to cover this plus jobs.AdmissionBudget
// plus writing the reply.
const identityResolveBudget = 10 * time.Second

// resolveIdentity fills in the branch and commit a review is about, when the
// trigger did not carry them.
//
// A comment trigger knows only a pull-request number, so the Request it
// produces names "pr-N" and no commit. The reviewer resolves the real head for
// itself at startup, but only in memory: the Job it runs in still records the
// placeholder. Anything outside that process -- the sentinel that reports a
// reviewer killed before it could publish -- then has no commit to name, and
// no way to recognise the reviewer's own publication in order to avoid
// contradicting it.
//
// Resolving here makes the Job self-describing. It is an improvement rather
// than a precondition: the reviewer still resolves for itself, so a failure
// costs the review nothing and only leaves that one termination unreportable.
func resolveIdentity(ctx context.Context, r review.Request) review.Request {
	if r.PRNumber <= 0 || r.SHA != "" {
		return r
	}
	// This runs inside the webhook response, ahead of admission, and each
	// GitHub call it makes carries its own 30s client timeout: minting a
	// token is two of them and the lookup is a third. Unbounded, a slow
	// GitHub costs the delivery its acknowledgement while its Job is created
	// anyway. Give up instead -- the reviewer still resolves for itself.
	resolveCtx, cancel := context.WithTimeout(ctx, identityResolveBudget)
	defer cancel()
	ctx = resolveCtx
	token, err := controllerToken(ctx, r.Repo)
	if err == nil {
		var ref, sha, base string
		ref, sha, base, err = review.ResolvePullRequest(ctx, os.Getenv("BOSUN_GITHUB_API_URL"), nil, r.Repo, r.PRNumber, token)
		if err == nil {
			// The same lookup names the pull request's base (#96), so a
			// comment-triggered review compares against it like any other.
			if r.BaseSHA == "" {
				r.BaseSHA = base
			}
			// Keep the ref the trigger carried: the Job's name derives from
			// it and is the deduplication key for this delivery, so a
			// resolution that succeeds on a redelivery after failing once
			// must not produce a second Job.
			r.TriggerRef, r.Ref, r.SHA = r.Ref, ref, sha
			return r
		}
	}
	slog.Warn("could not resolve the pull request head before submission; a termination of this review will not be reportable",
		"logger", "bosun.server", "repo", r.Repo, "pr", r.PRNumber, "error", err)
	return r
}

// startSentinel runs the loop that reports reviewer deaths the reviewer could
// not report itself. Without it an OOM-killed review publishes nothing at all
// and is indistinguishable, from the pull request, from a review that never
// ran.
func startSentinel(ctx context.Context, client kubernetes.Interface, cfg config.Config) {
	if cfg.SentinelIntervalSeconds <= 0 {
		slog.Warn("termination sentinel disabled; a reviewer killed for exceeding its memory limit will publish nothing",
			"logger", "bosun.sentinel")
		return
	}
	s := sentinel.Sentinel{
		Client: client, Config: cfg,
		Interval: time.Duration(cfg.SentinelIntervalSeconds) * time.Second,
		Publish: func(ctx context.Context, t review.Termination) (bool, error) {
			token, err := controllerToken(ctx, t.Repo)
			if err != nil {
				return false, err
			}
			return review.PublishTermination(ctx, os.Getenv("BOSUN_GITHUB_API_URL"), nil, token, t)
		},
		Lock: func(ctx context.Context, op func(context.Context) error) error {
			return jobs.Serialize(ctx, client, cfg.Namespace, jobs.SentinelLease, 60*time.Second, op)
		},
	}
	go s.Run(ctx)
}

// sweepLease serializes sweep passes across replicas, so two controllers do not
// walk every installation at the same moment.
const sweepLease = "bosun-missed-review-sweep"

// startSweep runs the pass that requests reviews for pull requests whose
// webhook never became a Job. Without it a delivery lost before it was stored
// -- during a rollout, or while no controller was ready -- leaves its pull
// request unreviewed with nothing to say so.
func startSweep(ctx context.Context, client kubernetes.Interface, cfg config.Config) {
	if cfg.SweepIntervalSeconds <= 0 {
		slog.Warn("missed-review sweep disabled; a webhook lost before it was stored leaves its pull request unreviewed",
			"logger", "bosun.sweep")
		return
	}
	appID, key := os.Getenv("BOSUN_GITHUB_APP_ID"), os.Getenv("BOSUN_GITHUB_PRIVATE_KEY")
	if appID == "" || key == "" {
		slog.Warn("missed-review sweep needs a GitHub App to list installations; it is off for this install",
			"logger", "bosun.sweep")
		return
	}
	s := &sweep.Sweeper{
		Client: client, Config: cfg,
		GitHub: review.Scanner{APIBaseURL: os.Getenv("BOSUN_GITHUB_API_URL"), AppID: appID, Key: key},
		Submit: submitter{client, cfg}.Submit,
		Lock: func(ctx context.Context, op func(context.Context) error) error {
			return jobs.Serialize(ctx, client, cfg.Namespace, sweepLease, 4*time.Minute, op)
		},
	}
	go s.Run(ctx)
}

func kubeClient() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cfg, err = clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
	}
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// newWebhookServer bounds every phase of a request. The endpoint is
// internet-facing and verifies the GitHub signature only after reading the
// body, so without these a client can hold connections open with slow headers
// or a slow body and exhaust the pod's file descriptors before any
// authentication runs.
//
// WriteTimeout sits above identity resolution, the repository-token mint, and
// the 35s admission wait in internal/jobs combined: a shorter one would cut off
// a delivery that is still legitimately being admitted.
func newWebhookServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func main() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("BOSUN_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	if os.Getenv("BOSUN_LOG_FORMAT") == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, opts)))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if metaCommand(os.Args[1:], os.Stdout) {
		return
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "init":
			os.Exit(runInit(os.Args[2:], os.Stdout, os.Stderr))
		case "review", "review-status", "review-result", "up", "down", "reset", "doctor":
			os.Exit(localreview.Run(ctx, os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
		case "serve", "reviewer":
			if len(os.Args) > 2 {
				fmt.Fprintln(os.Stderr, "unexpected arguments")
				os.Exit(2)
			}
		default:
			fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
			os.Exit(2)
		}
	}
	cfg := config.Load()
	if len(os.Args) > 1 && os.Args[1] == "reviewer" {
		if err := review.Run(ctx, cfg); err != nil {
			// The exit code carries the stage out of the process, so a pod's
			// terminated exitCode still says what failed once events and logs
			// are gone.
			slog.Error("review failed", "error", review.Describe(err))
			os.Exit(review.ExitCode(err))
		}
		return
	}
	client, err := kubeClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure Kubernetes client:", err)
		os.Exit(1)
	}
	slog.Info("bosun starting", "namespace", cfg.Namespace, "max_concurrent_reviews", cfg.MaxConcurrentReviews, "max_queued_reviews", cfg.MaxQueuedReviews)
	startSentinel(ctx, client, cfg)
	startDispatcher(ctx, client, cfg)
	startSweep(ctx, client, cfg)
	srv := newWebhookServer(server.Handler{Config: cfg, Submitter: submitter{client, cfg}})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		// Restore default signal handling so a second interrupt exits now.
		stop()
		slog.Info("shutdown requested; draining in-flight deliveries")
		// Finish inside the chart's termination grace period, after preStop: a delivery
		// still holding the admission lease must release it before SIGKILL.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("drain did not finish", "error", err)
			return
		}
		slog.Info("drain complete")
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
	<-drained
}
