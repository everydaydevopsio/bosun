package main

import (
	"context"
	"fmt"
	"github.com/everydaydevopsio/bosun/internal/localreview"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/jobs"
	"github.com/everydaydevopsio/bosun/internal/review"
	"github.com/everydaydevopsio/bosun/internal/server"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

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
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "review", "review-status":
			os.Exit(localreview.Run(ctx, os.Args[1], os.Args[2:], os.Stdout, os.Stderr))
		case "help", "--help", "-h":
			fmt.Println("Usage: bosun review [PATH] [--branch REF] [--base REF] [--provider NAME] [--timeout 30m]\n       bosun review-status JOB [--follow]\n       bosun serve\n       bosun reviewer")
			return
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
	if err := http.ListenAndServe(":8080", server.Handler{Config: cfg, Submitter: submitter{client, cfg}}); err != nil {
		slog.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
