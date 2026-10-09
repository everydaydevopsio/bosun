package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectTargets(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		names   []string
		want    string
		wantErr string
	}{
		{name: "no choice detects what is installed", want: "codex"},
		{name: "explicit single", names: []string{"claude"}, want: "claude"},
		{name: "comma separated", names: []string{"claude,codex"}, want: "claude,codex"},
		{name: "repeated flag", names: []string{"claude", "codex"}, want: "claude,codex"},
		{name: "case and spacing", names: []string{" Claude , CODEX "}, want: "claude,codex"},
		{name: "unknown target", names: []string{"cursor"}, wantErr: "cursor"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectTargets(home, tc.names)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want one naming %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, target := range got {
				names = append(names, target.Name)
			}
			if strings.Join(names, ",") != tc.want {
				t.Errorf("selectTargets(%v) = %v, want %s", tc.names, names, tc.want)
			}
		})
	}
}

func TestRunInit(t *testing.T) {
	t.Run("installs for a detected agent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		var out, errout bytes.Buffer
		if code := runInit(nil, &out, &errout); code != 0 {
			t.Fatalf("exit = %d: %s", code, errout.String())
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "bosun-review", "SKILL.md")); err != nil {
			t.Fatalf("skill not installed: %v", err)
		}
		if !strings.Contains(out.String(), "installed") {
			t.Errorf("stdout did not report the install: %q", out.String())
		}
	})

	// Nothing to install for is a configuration problem, not a crash: say which
	// directories were looked for.
	t.Run("no agent configured", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		var out, errout bytes.Buffer
		if code := runInit(nil, &out, &errout); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		for _, want := range []string{"~/.claude", "~/.codex"} {
			if !strings.Contains(errout.String(), want) {
				t.Errorf("message %q does not mention %q", errout.String(), want)
			}
		}
	})

	// --print must not touch the filesystem, so it works before any agent is
	// installed and can be piped somewhere else.
	t.Run("print writes the skill without installing", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		var out, errout bytes.Buffer
		if code := runInit([]string{"--print"}, &out, &errout); code != 0 {
			t.Fatalf("exit = %d: %s", code, errout.String())
		}
		if !strings.HasPrefix(out.String(), "---\n") {
			t.Errorf("printed skill does not start with frontmatter: %.20q", out.String())
		}
		if entries, _ := os.ReadDir(home); len(entries) != 0 {
			t.Errorf("--print wrote to the filesystem: %v", entries)
		}
	})

	t.Run("an edited skill is kept and reported", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".codex", "skills", "bosun-review", "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("mine\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, errout bytes.Buffer
		if code := runInit(nil, &out, &errout); code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(errout.String(), "--force") {
			t.Errorf("kept skill did not explain how to replace it: %q", errout.String())
		}
		if b, _ := os.ReadFile(path); string(b) != "mine\n" {
			t.Error("an edited skill was overwritten")
		}
	})

	t.Run("rejects bad arguments", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		for _, args := range [][]string{{"extra"}, {"--target", "cursor"}} {
			var out, errout bytes.Buffer
			if code := runInit(args, &out, &errout); code != 2 {
				t.Errorf("runInit(%v) = %d, want 2", args, code)
			}
		}
	})
}
