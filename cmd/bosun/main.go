package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

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
	cfg := config.Load()
	if len(os.Args) > 1 && os.Args[1] == "reviewer" {
		if err := review.Run(context.Background(), cfg); err != nil {
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
