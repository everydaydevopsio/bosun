package review

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaterialiseCredentials(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CODEX_HOME", filepath.Join(dir, "codex"))
	t.Setenv("CODEX_AUTH", `{"tokens":{"access_token":"test-only"}}`)
	t.Setenv("CLAUDE_CREDENTIALS", "")
	if err := materialiseCredentials(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "codex", "auth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("credential permissions: %v", info.Mode())
	}
	if os.Getenv("CODEX_AUTH") != "" {
		t.Fatal("credential JSON remains in child environment")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"tokens":{"access_token":"test-only"}}` {
		t.Fatalf("credential content mismatch: %v", err)
	}
}
func TestInvalidCredentialsFailWithoutLeakingValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_AUTH", "invalid-secret-value")
	t.Setenv("CLAUDE_CREDENTIALS", "")
	err := materialiseCredentials()
	if err == nil || err.Error() != "CODEX_AUTH must contain valid JSON" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckCodexCredentials(t *testing.T) {
	for _, tc := range []struct {
		name, key, invocationKey, auth string
		valid                          bool
	}{
		{name: "missing"},
		{name: "API key", key: "test-key", valid: true},
		{name: "invocation key", invocationKey: "test-key", valid: true},
		{name: "saved API key", auth: `{"OPENAI_API_KEY":"test-key"}`, valid: true},
		{name: "OAuth", auth: `{"tokens":{"access_token":"test-access"}}`, valid: true},
		{name: "refreshable OAuth", auth: `{"tokens":{"refresh_token":"test-refresh"}}`, valid: true},
		{name: "empty auth", auth: `{}`},
		{name: "null auth", auth: `null`},
		{name: "invalid auth", auth: `secret-not-json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CODEX_HOME", dir)
			t.Setenv("OPENAI_API_KEY", tc.key)
			t.Setenv("CODEX_API_KEY", tc.invocationKey)
			if tc.auth != "" {
				if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(tc.auth), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := checkCodexCredentials(); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}
