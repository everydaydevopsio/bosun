package localreview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// probeLocalProvider makes a real, minimal request through the same headless
// CLI family as the reviewer. Account metadata and file presence are not proof
// that the provider will accept the saved credential.
func probeLocalProvider(ctx context.Context, provider string) error {
	var name string
	var args []string
	switch {
	case strings.HasPrefix(provider, "claude"):
		name = "claude"
		args = []string{"--print", "--output-format", "text", "--no-session-persistence", "--permission-mode", "dontAsk", "--tools", "", "--", "Reply OK"}
	case strings.HasPrefix(provider, "codex"):
		name = "codex"
		args = []string{"exec", "--skip-git-repo-check", "--sandbox", "read-only", "--color", "never", "Reply OK"}
	default:
		return fmt.Errorf("no headless credential check is configured for provider %q", provider)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, name, args...)
	cmd.Dir = os.TempDir()
	if name == "codex" && os.Getenv("OPENAI_API_KEY") != "" && os.Getenv("CODEX_API_KEY") == "" {
		// Match the codex-bosun wrapper's API-key alias.
		cmd.Env = append(os.Environ(), "CODEX_API_KEY="+os.Getenv("OPENAI_API_KEY"))
	}
	var output probeOutput
	cmd.Stdout, cmd.Stderr = &output, &output
	err := cmd.Run()
	if err == nil {
		if strings.TrimSpace(output.String()) == "" {
			return fmt.Errorf("headless %s credential check returned no response; retry the provider CLI directly before loading credentials", provider)
		}
		return nil
	}
	if probeCtx.Err() != nil {
		return fmt.Errorf("headless %s credential check timed out or was cancelled", provider)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("cannot check %s credentials: %s CLI is not installed on this machine", provider, name)
	}
	message := strings.ToLower(output.String())
	switch {
	case strings.Contains(message, "token has been revoked"):
		return fmt.Errorf("AUTH_REVOKED: headless %s request rejected the saved token; renew the provider credential, then retry bosun up --local-credentials", provider)
	case strings.Contains(message, "failed to authenticate"), strings.Contains(message, "authentication failed"), strings.Contains(message, "unauthorized"), strings.Contains(message, "api error: 401"), strings.Contains(message, "invalid_grant"):
		return fmt.Errorf("AUTH_REJECTED: headless %s request rejected the saved credential; renew the provider credential, then retry bosun up --local-credentials", provider)
	case strings.Contains(message, "rate limit"), strings.Contains(message, "api error: 429"):
		return fmt.Errorf("RATE_LIMITED: headless %s request reached the provider limit; retry after it resets", provider)
	default:
		return fmt.Errorf("headless %s credential check failed; run a provider CLI request to diagnose it before retrying bosun up --local-credentials", provider)
	}
}

// Provider output is untrusted and can contain secrets. Only fixed diagnoses
// derived from this bounded buffer are returned to the caller.
type probeOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *probeOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > 4096 {
		b.data = append([]byte(nil), b.data[len(b.data)-4096:]...)
	}
	return len(p), nil
}

func (b *probeOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
