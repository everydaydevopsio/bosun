package review

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
)

// Successful review output is published to a pull request. redact alone covers
// GitHub credentials; provider credentials live in the environment and reach
// the output only if a prompt-injected agent echoes them, so the diagnostic
// redactor must run on success as well as on failure.
func TestSuccessfulOutputRedactsProviderCredentials(t *testing.T) {
	workspace := t.TempDir()
	testGit(t, workspace, "-C", workspace, "init")

	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")

	for _, tc := range []struct{ name, variable, secret string }{
		{"openai", "OPENAI_API_KEY", "sk-openai-secret-value"},
		{"anthropic", "ANTHROPIC_API_KEY", "sk-ant-secret-value"},
		{"gemini", "GEMINI_API_KEY", "gemini-secret-value"},
		{"claude oauth", "CLAUDE_CODE_OAUTH_TOKEN", "oauth-secret-value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.variable, tc.secret)
			t.Setenv("BOSUN_LOCAL_PATH", workspace)
			t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
			t.Setenv("BOSUN_REF", "refs/heads/main")

			var out bytes.Buffer
			w := Worker{
				PromptPath: prompt,
				Output:     &out,
				Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
					return "the key is " + tc.secret + " and that is all", nil
				},
			}
			if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
				t.Fatalf("run: %v", err)
			}
			if strings.Contains(out.String(), tc.secret) {
				t.Fatalf("published output leaked %s: %q", tc.variable, out.String())
			}
			if !strings.Contains(out.String(), "and that is all") {
				t.Fatalf("redaction destroyed the surrounding output: %q", out.String())
			}
		})
	}
}

// Guards the shape of the environment the test above depends on: every
// credential the reviewer Job can receive must be known to the redactor.
func TestDiagnosticRedactorCoversEveryProviderCredential(t *testing.T) {
	for _, variable := range []string{
		"OPENAI_API_KEY", "CODEX_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN",
		"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GITHUB_TOKEN",
	} {
		t.Run(variable, func(t *testing.T) {
			os.Setenv(variable, "supersecretvalue")
			defer os.Unsetenv(variable)
			if got := diagnosticRedactor()("leak supersecretvalue here"); strings.Contains(got, "supersecretvalue") {
				t.Fatalf("%s is not redacted: %q", variable, got)
			}
		})
	}
}
