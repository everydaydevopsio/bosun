package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}

// Discovery is what lets a user who has already authenticated `codex` or
// `claude` on this machine run a review without exporting anything by hand.
func TestDiscover(t *testing.T) {
	const codexAuth = `{"tokens":{"access_token":"a","refresh_token":"r"}}`
	const claudeAuth = `{"claudeAiOauth":{"accessToken":"a"}}`
	tests := []struct {
		name      string
		env       map[string]string
		files     map[string]string
		keychain  string
		keychainE error
		want      map[string]string
		wantFrom  map[string]string
	}{
		{
			name:  "codex auth file",
			files: map[string]string{".codex/auth.json": codexAuth},
			want:  map[string]string{"codex-auth": codexAuth},
			// Provenance is reported to the user, so it must name the real source.
			wantFrom: map[string]string{"codex-auth": ".codex/auth.json"},
		},
		{
			name:     "claude credentials file beats the keychain",
			files:    map[string]string{".claude/.credentials.json": claudeAuth},
			keychain: `{"claudeAiOauth":{"accessToken":"keychain"}}`,
			want:     map[string]string{"claude-credentials": claudeAuth},
			wantFrom: map[string]string{"claude-credentials": ".claude/.credentials.json"},
		},
		{
			name:     "keychain is used when no file exists",
			keychain: claudeAuth,
			want:     map[string]string{"claude-credentials": claudeAuth},
			wantFrom: map[string]string{"claude-credentials": "keychain"},
		},
		{
			name:  "exported environment wins over a file",
			env:   map[string]string{"CODEX_AUTH": `{"tokens":{"access_token":"env"}}`},
			files: map[string]string{".codex/auth.json": codexAuth},
			want:  map[string]string{"codex-auth": `{"tokens":{"access_token":"env"}}`},
			// The environment is the source; the file is never read.
			wantFrom: map[string]string{"codex-auth": "CODEX_AUTH"},
		},
		{
			name: "plain api keys are taken from the environment only",
			env:  map[string]string{"OPENAI_API_KEY": "sk-test", "GEMINI_API_KEY": "g-test"},
			want: map[string]string{"openai-api-key": "sk-test", "gemini-api-key": "g-test"},
		},
		{
			name:  "a credential file that is not JSON is skipped",
			files: map[string]string{".codex/auth.json": "not json"},
			want:  map[string]string{},
		},
		{
			name:  "an empty credential file is skipped",
			files: map[string]string{".codex/auth.json": "   "},
			want:  map[string]string{},
		},
		{
			name:      "a keychain that cannot be read is not fatal",
			keychainE: errors.New("user denied access"),
			want:      map[string]string{},
		},
		{
			name: "nothing configured discovers nothing",
			want: map[string]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, v := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "GEMINI_API_KEY", "CODEX_AUTH", "CLAUDE_CREDENTIALS", "CODEX_HOME"} {
				t.Setenv(v, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			for name, body := range tc.files {
				writeFile(t, filepath.Join(home, name), body)
			}
			found := discover(context.Background(), source{home: home, keychain: func(context.Context) (string, error) { return tc.keychain, tc.keychainE }})
			got := map[string]string{}
			from := map[string]string{}
			for _, c := range found {
				got[c.Key] = c.Value
				from[c.Key] = c.From
			}
			if len(got) != len(tc.want) {
				t.Fatalf("discovered %v, want %v", keys(got), keys(tc.want))
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			for k, v := range tc.wantFrom {
				if !strings.Contains(from[k], v) {
					t.Errorf("%s came from %q, want it to mention %q", k, from[k], v)
				}
			}
		})
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// CODEX_HOME relocates the codex configuration directory, and a user who sets it
// keeps their credentials somewhere other than ~/.codex.
func TestDiscoverHonoursCodexHome(t *testing.T) {
	home, elsewhere := t.TempDir(), t.TempDir()
	for _, v := range []string{"OPENAI_API_KEY", "CODEX_AUTH", "CLAUDE_CREDENTIALS"} {
		t.Setenv(v, "")
	}
	t.Setenv("CODEX_HOME", elsewhere)
	writeFile(t, filepath.Join(home, ".codex", "auth.json"), `{"tokens":{"access_token":"wrong"}}`)
	writeFile(t, filepath.Join(elsewhere, "auth.json"), `{"tokens":{"access_token":"right"}}`)

	for _, c := range discover(context.Background(), source{home: home, keychain: func(context.Context) (string, error) { return "", nil }}) {
		if c.Key == "codex-auth" {
			if !strings.Contains(c.Value, "right") {
				t.Fatalf("codex-auth = %q, want the CODEX_HOME copy", c.Value)
			}
			return
		}
	}
	t.Fatal("codex-auth was not discovered")
}

// The error a user sees when their provider has no credentials must name what
// to do about it, so the provider's accepted keys drive the message.
func TestMissing(t *testing.T) {
	tests := []struct {
		name, provider string
		found          []string
		wantMissing    bool
		wantMentions   []string
	}{
		{name: "codex satisfied by an api key", provider: "codex-bosun", found: []string{"openai-api-key"}},
		{name: "codex satisfied by saved auth", provider: "codex-bosun", found: []string{"codex-auth"}},
		{name: "claude satisfied by oauth token", provider: "claude-bosun", found: []string{"claude-code-oauth-token"}},
		{
			name: "codex with only a claude credential", provider: "codex-bosun", found: []string{"claude-credentials"},
			wantMissing: true, wantMentions: []string{"codex-bosun", "openai-api-key", "codex-auth"},
		},
		{
			name: "nothing at all", provider: "claude-bosun", found: nil,
			wantMissing: true, wantMentions: []string{"claude-bosun", "claude-code-oauth-token"},
		},
		{
			name: "an unknown provider cannot be satisfied", provider: "mystery", found: []string{"openai-api-key"},
			wantMissing: true, wantMentions: []string{"mystery"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var found []Credential
			for _, k := range tc.found {
				found = append(found, Credential{Key: k, Value: "x"})
			}
			e := Missing(tc.provider, found)
			if !tc.wantMissing {
				if e != nil {
					t.Fatalf("unexpected error: %v", e)
				}
				return
			}
			if e == nil {
				t.Fatal("expected an error")
			}
			for _, m := range tc.wantMentions {
				if !strings.Contains(e.Error(), m) {
					t.Errorf("error %q does not mention %q", e, m)
				}
			}
		})
	}
}
