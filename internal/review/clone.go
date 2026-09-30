package review

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var withheld = map[string]bool{
	"GITHUB_TOKEN": true, "GH_TOKEN": true, "BOSUN_GIT_TOKEN": true,
	"BOSUN_GITHUB_APP_ID": true, "BOSUN_GITHUB_PRIVATE_KEY": true, "BOSUN_GITHUB_PRIVATE_KEY_FILE": true,
	"GIT_ASKPASS": true, "SSH_ASKPASS": true,
}

func agentEnvironment() []string {
	var result []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !withheld[key] {
			result = append(result, entry)
		}
	}
	return result
}
func redact(text string, extra ...string) string {
	for key := range withheld {
		extra = append(extra, os.Getenv(key))
	}
	for _, value := range extra {
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return text
}
func cloneRepository(ctx context.Context, base, workspace string, req Request, token string) (string, error) {
	if !validRepository(req.Repo) {
		return "", fmt.Errorf("invalid repository name")
	}
	if req.SHA != "" && !commitPattern.MatchString(req.SHA) {
		return "", fmt.Errorf("invalid commit SHA")
	}
	if req.PRNumber < 0 {
		return "", fmt.Errorf("invalid pull request number")
	}
	helper, err := os.MkdirTemp("", "bosun-git-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(helper)
	askpass := filepath.Join(helper, "askpass")
	script := "#!/bin/sh\ncase \"$1\" in\n  Username*) printf '%s\\n' x-access-token ;;\n  *) printf '%s\\n' \"$BOSUN_GIT_TOKEN\" ;;\nesac\n"
	if err = os.WriteFile(askpass, []byte(script), 0700); err != nil {
		return "", err
	}
	// Ignore inherited Git overrides and credential helpers during authentication.
	var env []string
	for _, entry := range agentEnvironment() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			env = append(env, entry)
		}
	}
	env = append(env, "GIT_ASKPASS="+askpass, "BOSUN_GIT_TOKEN="+token, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	git := func(dir string, args ...string) (string, error) {
		args = append([]string{"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null"}, args...)
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git operation failed: %w: %s", err, redact(string(output), token))
		}
		return strings.TrimSpace(string(output)), nil
	}
	if _, err = git("", "clone", "--no-checkout", "--no-tags", "--", strings.TrimRight(base, "/")+"/"+req.Repo+".git", workspace); err != nil {
		return "", err
	}
	fetchRef := "refs/heads/" + req.Ref
	if req.PRNumber > 0 {
		fetchRef = fmt.Sprintf("refs/pull/%d/head", req.PRNumber)
	} else if _, err = git(workspace, "check-ref-format", fetchRef); err != nil {
		return "", fmt.Errorf("invalid branch ref")
	}
	if _, err = git(workspace, "fetch", "--no-tags", "origin", fetchRef); err != nil {
		return "", err
	}
	revision := "FETCH_HEAD"
	if req.SHA != "" {
		// Pin the event's exact commit even if the branch/PR advanced meanwhile.
		if _, err = git(workspace, "fetch", "--no-tags", "origin", req.SHA); err != nil {
			return "", err
		}
		revision = req.SHA
	}
	if _, err = git(workspace, "checkout", "--detach", revision); err != nil {
		return "", err
	}
	return git(workspace, "rev-parse", "HEAD")
}
