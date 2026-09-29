package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
