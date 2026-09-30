package review

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// GitHubToken is called by the trusted controller, never by an agent pod.
// Installation tokens are narrowed to the repository by installationToken.
func GitHubToken(ctx context.Context, repo string) (string, error) {
	if !validRepository(repo) {
		return "", fmt.Errorf("invalid repository")
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return (githubClient{base: "https://api.github.com", http: client}).token(ctx, repo)
}
