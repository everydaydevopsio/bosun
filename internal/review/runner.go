package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

func Run(ctx context.Context, cfg config.Config) error {
	repo, ref := os.Getenv("BOSUN_REPO"), os.Getenv("BOSUN_REF")
	if repo == "" || ref == "" {
		return fmt.Errorf("BOSUN_REPO and BOSUN_REF are required")
	}
	workspace := os.Getenv("BOSUN_LOCAL_PATH")
	if workspace == "" {
		return fmt.Errorf("remote GitHub review cloning is not yet implemented")
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return fmt.Errorf("local review path is not a git repository: %w", err)
	}
	if err := materialiseCredentials(); err != nil {
		return err
	}
	instructions, err := os.ReadFile("/app/bosun/prompts/code-review.md")
	if err != nil {
		return fmt.Errorf("read review prompt: %w", err)
	}
	prompt := fmt.Sprintf("%s\nRepository: %s\nBranch: %s\n", instructions, repo, ref)
	output, err := runBridge(ctx, workspace, cfg.ReviewProvider, prompt, time.Duration(cfg.ReviewTimeoutSeconds)*time.Second)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}
