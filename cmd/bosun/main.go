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
	"github.com/everydaydevopsio/bosun/internal/server"
	"github.com/everydaydevopsio/bosun/internal/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const usage = `Usage: bosun review [PATH] [--branch REF] [--base REF] [--provider NAME] [--timeout 30m]
       bosun review-status JOB [--follow]
       bosun up [--local-credentials] [--cluster NAME]
       bosun down [--keep-snapshots] [--cluster NAME]
       bosun serve
       bosun reviewer
       bosun version

bosun review creates the local Kind cluster when it is missing, so bosun up is
only needed to prepare one ahead of time or to refresh credentials. Pass
--local-credentials to copy this machine's codex or claude sign-in into the
cluster; bosun down removes the cluster and its snapshots.
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

func (s submitter) Submit(ctx context.Context, r review.Request, d string) (string, error) {
	return jobs.Submit(ctx, s.client, s.cfg, r, d)
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
// WriteTimeout sits above the 35s admission wait in internal/jobs: a shorter
// one would cut off a delivery that is still legitimately queued for capacity.
func newWebhookServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
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
		case "review", "review-status", "up", "down":
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
			slog.Error("review failed", "error", err)
			os.Exit(1)
		}
		return
	}
	client, err := kubeClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, "configure Kubernetes client:", err)
		os.Exit(1)
	}
	slog.Info("bosun starting", "namespace", cfg.Namespace, "max_concurrent_reviews", cfg.MaxConcurrentReviews)
	srv := newWebhookServer(server.Handler{Config: cfg, Submitter: submitter{client, cfg}})
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		// Restore default signal handling so a second interrupt exits now.
		stop()
		slog.Info("shutdown requested; draining in-flight deliveries")
		// Finish inside the default 30s termination grace period: a delivery
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
