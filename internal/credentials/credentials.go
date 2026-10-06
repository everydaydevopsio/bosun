// Package credentials discovers the provider credentials already present on a
// developer's machine and describes which of them each review provider can use.
//
// A reviewer Job reads its credentials from a Kubernetes Secret, so running a
// local review has always meant exporting them by hand first. Most machines
// that want to run a review have an authenticated `codex` or `claude` CLI on
// them already; this package finds that existing authentication so the CLI can
// load it into the cluster without the user transcribing anything.
package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Spec names one credential a provider can use: the environment variable the
// reviewer reads, and the Secret key it travels in.
type Spec struct{ Variable, Key string }

// ForProvider reports the credentials the named review provider can use.
//
// A reviewer Job runs an untrusted agent over untrusted repository content, so
// handing every Job every provider's credentials makes a prompt injection in
// one provider's review enough to exfiltrate the others' keys. An unrecognised
// provider gets none: failing with a missing-credential error is better than
// handing an unknown binary every key in the Secret.
func ForProvider(provider string) []Spec {
	switch {
	case strings.HasPrefix(provider, "codex"):
		return []Spec{{"OPENAI_API_KEY", "openai-api-key"}, {"CODEX_AUTH", "codex-auth"}}
	case strings.HasPrefix(provider, "claude"):
		return []Spec{{"CLAUDE_CODE_OAUTH_TOKEN", "claude-code-oauth-token"}, {"ANTHROPIC_API_KEY", "anthropic-api-key"}, {"CLAUDE_CREDENTIALS", "claude-credentials"}}
	case strings.HasPrefix(provider, "gemini"):
		return []Spec{{"GEMINI_API_KEY", "gemini-api-key"}}
	}
	return nil
}

// Credential is a discovered value, ready to be written to the Secret. From
// records where it came from so the CLI can tell the user what it picked up.
type Credential struct{ Key, Value, From string }

// jsonCredential reports whether a key holds a credential file's contents
// rather than a plain token. The reviewer writes these back out as files, and
// refuses anything that is not valid JSON, so reject it here instead.
func jsonCredential(key string) bool {
	return key == "codex-auth" || key == "claude-credentials"
}

// source is the machine being inspected. Tests supply their own.
type source struct {
	home     string
	keychain func(context.Context) (string, error)
}

// keychainCredential reads the login item Claude Code writes on macOS, where it
// stores credentials in the Keychain instead of the file used elsewhere. A
// refusal here is ordinary -- the user may decline the access prompt -- so the
// error is reported to the caller rather than treated as fatal.
func keychainCredential(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Discover reports every credential found on this machine.
func Discover(ctx context.Context) []Credential {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return discover(ctx, source{home: home, keychain: keychainCredential})
}

func discover(ctx context.Context, s source) []Credential {
	found := map[string]Credential{}
	add := func(key, value, from string) {
		if value = strings.TrimSpace(value); value == "" {
			return
		}
		if jsonCredential(key) && !json.Valid([]byte(value)) {
			return
		}
		if _, taken := found[key]; taken {
			return
		}
		found[key] = Credential{Key: key, Value: value, From: from}
	}

	// An exported variable is an explicit choice and outranks a file on disk.
	for _, spec := range []Spec{
		{"OPENAI_API_KEY", "openai-api-key"},
		{"ANTHROPIC_API_KEY", "anthropic-api-key"},
		{"CLAUDE_CODE_OAUTH_TOKEN", "claude-code-oauth-token"},
		{"GEMINI_API_KEY", "gemini-api-key"},
		{"CODEX_AUTH", "codex-auth"},
		{"CLAUDE_CREDENTIALS", "claude-credentials"},
	} {
		add(spec.Key, os.Getenv(spec.Variable), spec.Variable)
	}

	// `codex login` writes auth.json under CODEX_HOME, or ~/.codex by default.
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" && s.home != "" {
		codexHome = filepath.Join(s.home, ".codex")
	}
	if codexHome != "" {
		path := filepath.Join(codexHome, "auth.json")
		if b, err := os.ReadFile(path); err == nil {
			add("codex-auth", string(b), path)
		}
	}

	// `claude` writes .credentials.json everywhere except macOS, where the same
	// document lives in the Keychain.
	if s.home != "" {
		path := filepath.Join(s.home, ".claude", ".credentials.json")
		if b, err := os.ReadFile(path); err == nil {
			add("claude-credentials", string(b), path)
		}
	}
	if _, have := found["claude-credentials"]; !have && s.keychain != nil {
		if value, err := s.keychain(ctx); err == nil {
			add("claude-credentials", value, "the macOS keychain (Claude Code-credentials)")
		}
	}

	out := make([]Credential, 0, len(found))
	for _, c := range found {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Missing reports why the given provider cannot run with what was discovered.
func Missing(provider string, found []Credential) error {
	specs := ForProvider(provider)
	if len(specs) == 0 {
		return fmt.Errorf("provider %q has no credentials Bosun knows how to supply; use a codex, claude or gemini provider, or populate the AI Secret yourself", provider)
	}
	have := map[string]bool{}
	for _, c := range found {
		have[c.Key] = true
	}
	var keys, variables []string
	for _, s := range specs {
		if have[s.Key] {
			return nil
		}
		keys = append(keys, s.Key)
		variables = append(variables, s.Variable)
	}
	return fmt.Errorf("no usable credentials found for provider %q: it needs one of %s, so sign in with its CLI or export %s", provider, strings.Join(keys, ", "), strings.Join(variables, " or "))
}
