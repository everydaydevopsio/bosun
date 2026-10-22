package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everydaydevopsio/bosun/internal/cluster"
	"sigs.k8s.io/yaml"
)

func TestBosunProviderWrappersPreservePromptAndCloseInput(t *testing.T) {
	data, err := os.ReadFile("../../config/bridge-bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]struct {
			Binary     string   `json:"binary"`
			Args       []string `json:"args"`
			StreamJSON bool     `json:"stream_json"`
		}
	}
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex-bosun", "claude-bosun"} {
		t.Run(name, func(t *testing.T) {
			provider, ok := cfg.Providers[name]
			if !ok || !provider.StreamJSON || provider.Binary != "sh" || len(provider.Args) != 3 {
				t.Fatalf("invalid provider: %+v", provider)
			}
			dir := t.TempDir()
			fake := filepath.Join(dir, "fake-agent")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nif read -r unexpected; then exit 42; fi\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			command := provider.Args[1]
			if name == "codex-bosun" {
				t.Setenv("OPENAI_API_KEY", "test-provider-key")
				t.Setenv("CODEX_API_KEY", "")
				if err := os.WriteFile(fake, []byte("#!/bin/sh\n[ \"$CODEX_API_KEY\" = test-provider-key ] || exit 45\nprintf '%s\\n' \"$@\"\nif read -r unexpected; then exit 42; fi\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			binary := "node /app/node_modules/@openai/codex/bin/codex.js"
			if name == "claude-bosun" {
				binary = "node /app/bosun/scripts/claude-review-adapter.js"
				if !strings.Contains(command, `git config --global --add safe.directory "$PWD"`) {
					t.Fatal("Claude wrapper must trust the exact mounted review path")
				}
				command = strings.Replace(command, `git config --global --add safe.directory "$PWD"`, "true", 1)
				adapter, err := os.ReadFile("../../scripts/claude-review-adapter.js")
				if err != nil {
					t.Fatal(err)
				}
				for _, flag := range []string{"--output-format', 'stream-json", "--permission-mode', 'dontAsk", "Bash(git merge-base *)"} {
					if !strings.Contains(string(adapter), flag) {
						t.Fatalf("adapter missing %s", flag)
					}
				}
			}
			command = strings.Replace(command, binary, "'"+fake+"'", 1)
			prompt := "Review this branch\nLiteral $HOME $(exit 43) `exit 44` 'quotes' --flags"
			cmd := exec.Command("sh", "-c", command, provider.Args[2], prompt)
			cmd.Stdin = strings.NewReader("input must not reach the agent\n")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("wrapper: %v: %s", err, out)
			}
			if !strings.HasSuffix(string(out), prompt+"\n") {
				t.Fatalf("prompt changed: %q", out)
			}
		})
	}
}

func TestBridgeConfigAllowsOnlyReviewRoots(t *testing.T) {
	data, err := os.ReadFile("../../config/bridge-bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		AllowedPaths []string `json:"allowed_paths"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.AllowedPaths) != 2 || cfg.AllowedPaths[0] != cluster.ContainerRepos || cfg.AllowedPaths[1] != HostedWorkspaceRoot {
		t.Fatalf("bridgectl must allow only the local and hosted workspace roots, got %v", cfg.AllowedPaths)
	}
}

func TestHostedWorkspaceUsesDedicatedRoot(t *testing.T) {
	workspace, err := makeHostedWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(workspace) })
	if filepath.Dir(workspace) != HostedWorkspaceRoot {
		t.Fatalf("hosted workspace %q is outside %q", workspace, HostedWorkspaceRoot)
	}
}

// Codex's read-only and workspace-write sandboxes are bubblewrap, which needs
// to create a mount namespace. The reviewer Job drops every capability,
// forbids privilege escalation and runs as UID 1001, so bwrap aborts at
// startup and the review reads nothing at all:
//
//	bwrap: Failed to make / slave: Permission denied
//
// The isolation the hosted path relies on is the Job itself -- disposable, a
// repository-scoped token that expires within the hour, egress limited to DNS
// and 443 by the chart's NetworkPolicy, no Kubernetes API access. This test
// exists so that reinstating a bwrap-backed mode is a deliberate act with a
// failing test attached, rather than a plausible-looking hardening tweak.
func TestHostedCodexProviderDoesNotRelyOnBubblewrap(t *testing.T) {
	data, err := os.ReadFile("../../config/bridge-bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Providers map[string]struct {
			Args []string `json:"args"`
		}
	}
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	command := strings.Join(cfg.Providers["codex-bosun"].Args, " ")
	for _, bwrapBacked := range []string{"--sandbox read-only", "--sandbox workspace-write"} {
		if strings.Contains(command, bwrapBacked) {
			t.Errorf("codex-bosun uses %q, which bubblewrap backs and the reviewer Job cannot run", bwrapBacked)
		}
	}
	if !strings.Contains(command, "--sandbox danger-full-access") {
		t.Error("codex-bosun must select a sandbox mode explicitly; the default is bwrap-backed")
	}
}

// The local probe runs on a developer's own machine, where bwrap works and the
// Job's isolation does not exist. It must keep its sandbox.
func TestLocalProviderProbeKeepsItsSandbox(t *testing.T) {
	data, err := os.ReadFile("../localreview/provider_probe.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"--sandbox", "read-only"`) {
		t.Error("the local credential probe must stay sandboxed; only the hosted path drops bwrap")
	}
}
