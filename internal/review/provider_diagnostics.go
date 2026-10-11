package review

import (
	"fmt"
	"os"
	"strings"
)

// providerExitError turns untrusted provider text into a bounded, actionable
// diagnosis. Never include the original text: providers can echo credentials,
// prompts, or request identifiers in their failure output.
func providerExitError(provider string, exitCode int32, eventError, output string) error {
	message := strings.ToLower(eventError + "\n" + output)
	local := os.Getenv("BOSUN_LOCAL_PATH") != ""
	var diagnosis string
	switch {
	case strings.Contains(message, "oauth access token has been revoked"), strings.Contains(message, "token has been revoked"):
		diagnosis = "AUTH_REVOKED: authentication failed: OAuth token revoked"
	case strings.Contains(message, "failed to authenticate"), strings.Contains(message, "authentication failed"), strings.Contains(message, "invalid api key"), strings.Contains(message, "unauthorized"), strings.Contains(message, "api error: 401"), strings.Contains(message, "invalid_grant"), strings.Contains(message, "oauth token has expired"):
		diagnosis = "AUTH_REJECTED: authentication failed: provider rejected the credential"
	case strings.Contains(message, "rate limit"), strings.Contains(message, "api error: 429"):
		diagnosis = "RATE_LIMITED: provider rate limit reached; retry after the provider's limit resets"
	default:
		diagnosis = "UNKNOWN_EXIT: provider exited without a recognizable diagnosis; check the provider sign-in and retry once"
	}
	if strings.Contains(diagnosis, "authentication failed") {
		switch {
		case strings.HasPrefix(provider, "claude") && local:
			diagnosis += "; Claude /status can show a saved account even when a headless request fails. Verify with 'claude --print -- \"Reply OK\"'; if it returns 401, renew the Claude sign-in (for example, 'claude auth login'). Then run 'bosun up --local-credentials --provider claude-bosun' to refresh the cluster credential before starting a new review"
		case strings.HasPrefix(provider, "codex") && local:
			diagnosis += "; run 'codex login' on this machine, then 'bosun up --local-credentials --provider codex-bosun' to refresh the cluster credential before starting a new review"
		case local:
			diagnosis += "; sign in to the provider on this machine, then run 'bosun up --local-credentials' before starting a new review"
		default:
			diagnosis += "; refresh the review provider credential in the cluster Secret, then start a new review"
		}
	}
	exit := fmt.Sprintf("exit %d", exitCode)
	if exitCode == 0 {
		exit = "exit status unavailable"
	}
	return fmt.Errorf("agent session failed (%s): %s; the failed job remains available in bosun review-status", exit, diagnosis)
}
