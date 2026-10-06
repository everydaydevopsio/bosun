package jobs

import (
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/review"
)

func envNames(cfg config.Config) map[string]bool {
	out := map[string]bool{}
	for _, v := range env(cfg, review.Request{Repo: "o/r", Ref: "refs/heads/main"}) {
		out[v.Name] = true
	}
	return out
}

// A reviewer Job runs an untrusted agent against untrusted repository content.
// Handing every Job every provider's credentials means a prompt injection in a
// Codex review can exfiltrate the Claude and Gemini keys, so each Job must get
// only the credentials its own provider can use.
func TestJobReceivesOnlyTheSelectedProvidersCredentials(t *testing.T) {
	const (
		openai      = "OPENAI_API_KEY"
		codexAuth   = "CODEX_AUTH"
		claudeOAuth = "CLAUDE_CODE_OAUTH_TOKEN"
		anthropic   = "ANTHROPIC_API_KEY"
		claudeCreds = "CLAUDE_CREDENTIALS"
		gemini      = "GEMINI_API_KEY"
	)
	all := []string{openai, codexAuth, claudeOAuth, anthropic, claudeCreds, gemini}

	tests := []struct {
		provider string
		want     []string
	}{
		{"codex-bosun", []string{openai, codexAuth}},
		{"codex", []string{openai, codexAuth}},
		{"claude-bosun", []string{claudeOAuth, anthropic, claudeCreds}},
		{"claude", []string{claudeOAuth, anthropic, claudeCreds}},
		{"gemini", []string{gemini}},
		// An unrecognised provider is fail-closed: it gets no model credentials
		// rather than all of them.
		{"something-custom", nil},
		// opencode is in that set despite being a real provider the base image
		// ships: nothing maps it to a credential, so it receives none. The docs
		// and scripts no longer claim otherwise; supporting it is #18.
		{"opencode", nil},
	}

	for _, tc := range tests {
		t.Run(tc.provider, func(t *testing.T) {
			got := envNames(config.Config{
				ReviewProvider:    tc.provider,
				GitHubTokenSecret: "bosun-github",
				AISecret:          "bosun-ai",
			})

			allowed := map[string]bool{}
			for _, name := range tc.want {
				allowed[name] = true
				if !got[name] {
					t.Errorf("provider %q is missing its own credential %s", tc.provider, name)
				}
			}
			for _, name := range all {
				if !allowed[name] && got[name] {
					t.Errorf("provider %q leaks another provider's credential %s", tc.provider, name)
				}
			}
		})
	}
}

// GitHub credentials are how the reviewer clones and posts, so they are not
// provider-scoped and must always be present.
func TestGitHubCredentialsAreAlwaysPresent(t *testing.T) {
	for _, provider := range []string{"codex-bosun", "claude-bosun", "gemini", "unknown"} {
		got := envNames(config.Config{ReviewProvider: provider, GitHubTokenSecret: "bosun-github", AISecret: "bosun-ai"})
		for _, name := range []string{"GITHUB_TOKEN", "BOSUN_GITHUB_APP_ID", "BOSUN_GITHUB_PRIVATE_KEY"} {
			if !got[name] {
				t.Errorf("provider %q is missing %s", provider, name)
			}
		}
	}
}

// Every credential key the chart documents must be reachable by some provider,
// or a configured secret silently never arrives.
func TestEveryAISecretKeyIsReachable(t *testing.T) {
	reachable := map[string]bool{}
	for _, provider := range []string{"codex-bosun", "claude-bosun", "gemini"} {
		for name := range envNames(config.Config{ReviewProvider: provider, GitHubTokenSecret: "g", AISecret: "a"}) {
			reachable[name] = true
		}
	}
	for _, name := range []string{"OPENAI_API_KEY", "CODEX_AUTH", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CLAUDE_CREDENTIALS", "GEMINI_API_KEY"} {
		if !reachable[name] {
			t.Errorf("%s is never delivered to any provider", name)
		}
	}
	_ = strings.TrimSpace
}
