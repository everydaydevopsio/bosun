package skills

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A skill is only useful where the agent looks for it, and the two agents look
// in different places.
func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		dirs []string
		want []string
	}{
		{name: "both agents present", dirs: []string{".claude", ".codex"}, want: []string{"claude", "codex"}},
		{name: "only claude", dirs: []string{".claude"}, want: []string{"claude"}},
		{name: "only codex", dirs: []string{".codex"}, want: []string{"codex"}},
		{name: "neither", dirs: nil, want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			for _, d := range tc.dirs {
				if e := os.MkdirAll(filepath.Join(home, d), 0700); e != nil {
					t.Fatal(e)
				}
			}
			var got []string
			for _, target := range Detect(home) {
				got = append(got, target.Name)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("Detect() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestInstall(t *testing.T) {
	home := t.TempDir()
	if e := os.MkdirAll(filepath.Join(home, ".claude"), 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(home, ".claude", "skills", "bosun-review", "SKILL.md")

	results, err := Install(home, Detect(home), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != Installed {
		t.Fatalf("results = %+v, want one install", results)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("skill was not written: %v", err)
	}
	// The frontmatter is what makes an agent able to find it at all.
	for _, want := range []string{"---", "name: bosun-review", "description:"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("installed skill is missing %q", want)
		}
	}

	// Re-running is normal -- after an upgrade, or because the user forgot.
	results, err = Install(home, Detect(home), false)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != Unchanged {
		t.Errorf("second install = %q, want unchanged", results[0].Action)
	}

	// A local edit must survive an accidental re-run: someone tuned the skill
	// for their own workflow and would not expect `bosun init` to discard it.
	if e := os.WriteFile(path, []byte("my own version\n"), 0644); e != nil {
		t.Fatal(e)
	}
	results, err = Install(home, Detect(home), false)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != Kept {
		t.Errorf("edited skill = %q, want kept", results[0].Action)
	}
	if b, _ := os.ReadFile(path); string(b) != "my own version\n" {
		t.Error("an edited skill was overwritten without --force")
	}

	// --force is the escape hatch, and must actually replace it.
	results, err = Install(home, Detect(home), true)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Action != Replaced {
		t.Errorf("forced install = %q, want replaced", results[0].Action)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "name: bosun-review") {
		t.Error("--force did not restore the shipped skill")
	}
}

// Installing must not require the agent's skills directory to exist already --
// a fresh Claude Code install has ~/.claude but no skills/ in it.
func TestInstallCreatesTheSkillsDirectory(t *testing.T) {
	home := t.TempDir()
	if e := os.MkdirAll(filepath.Join(home, ".codex"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, err := Install(home, Detect(home), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "bosun-review", "SKILL.md")); err != nil {
		t.Fatalf("skill not installed: %v", err)
	}
}

// --print exists so the content can be inspected or redirected without
// touching the filesystem.
func TestPrint(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "---\n") {
		t.Errorf("printed skill does not start with frontmatter: %.40q", out.String())
	}
	for _, want := range []string{"--detach", "review-status", "--follow"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("printed skill does not mention %q", want)
		}
	}
}

// The skill drives the CLI, so its instructions have to match the CLI that
// ships with it. These are the contracts that would break silently.
func TestSkillMatchesTheCLIContract(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		"bosun review",
		"--detach",
		"--local-credentials",
		"bosun review-status",
		"--json",
		// Found by reviewing this skill with Bosun: the default provider is
		// codex-bosun whatever credentials the machine has.
		"--provider claude-bosun",
		// The default review is committed branch work, which requires a clean
		// tree and a branch that is not the default branch.
		"git status --porcelain",
		"git rev-parse --abbrev-ref origin/HEAD",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("skill does not use %q", want)
		}
	}
	// Blocking is the failure mode this skill exists to avoid.
	if !strings.Contains(body, "never run `bosun review` in the foreground and never use") {
		t.Error("skill does not warn against blocking on a review")
	}
	// A one-shot agent session that defers polling to "later" never reports.
	if !strings.Contains(body, "Keep polling in this turn") {
		t.Error("skill does not require polling to finish within the turn")
	}
	// Checking out a pull request moves the user's working branch. An agent
	// must not do that on its own initiative.
	if !strings.Contains(body, "ask before doing it, and wait for an answer") {
		t.Error("skill does not require consent before moving the user's branch")
	}
	if !strings.Contains(body, "Do not move the user's branch") {
		t.Error("skill does not forbid moving the branch unasked")
	}
}
