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
