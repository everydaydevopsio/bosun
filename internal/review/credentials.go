package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// The headless provider needs either an invocation key or saved CLI auth.
// Check after materialisation so CODEX_AUTH is represented by its auth file.
func checkCodexCredentials() error {
	if os.Getenv("CODEX_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		return nil
	}
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, ".codex")
	}
	data, err := os.ReadFile(filepath.Join(dir, "auth.json"))
	if err == nil && json.Valid(data) {
		var auth struct {
			APIKey string `json:"OPENAI_API_KEY"`
			Tokens struct {
				AccessToken  string `json:"access_token"`
				RefreshToken string `json:"refresh_token"`
			} `json:"tokens"`
		}
		if json.Unmarshal(data, &auth) == nil && (auth.APIKey != "" || auth.Tokens.AccessToken != "" || auth.Tokens.RefreshToken != "") {
			return nil
		}
	}
	return fmt.Errorf("codex-bosun has no usable credentials: configure openai-api-key or codex-auth in the Bosun AI Kubernetes Secret (OPENAI_API_KEY or CODEX_AUTH); --provider selects a provider but does not provision credentials")
}

func materialiseCredentials() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	for variable, relative := range map[string]string{
		"CODEX_AUTH":         ".codex/auth.json",
		"CLAUDE_CREDENTIALS": ".claude/.credentials.json",
	} {
		value := os.Getenv(variable)
		if value == "" {
			continue
		}
		if !json.Valid([]byte(value)) {
			return fmt.Errorf("%s must contain valid JSON", variable)
		}
		target := filepath.Join(home, relative)
		if variable == "CODEX_AUTH" && os.Getenv("CODEX_HOME") != "" {
			target = filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(target, []byte(value), 0600); err != nil {
			return err
		}
		if err := os.Chmod(target, 0600); err != nil {
			return err
		}
		os.Unsetenv(variable)
	}
	return nil
}
