package review

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
				binary = "/app/node_modules/@anthropic-ai/claude-code/bin/claude.exe"
				for _, flag := range []string{"--print", "--output-format text", "--permission-mode dontAsk"} {
					if !strings.Contains(command, flag) {
						t.Fatalf("missing %s", flag)
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
