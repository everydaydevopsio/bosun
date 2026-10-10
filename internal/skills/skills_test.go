package skills

import (
	"bytes"
	"os"
	"os/exec"
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

// internal/credentials honours CODEX_HOME when looking for a sign-in. If the
// installer does not, the same binary finds your credentials in one place and
// installs the skill somewhere Codex never reads.
func TestCodexHomeIsHonoured(t *testing.T) {
	home, relocated := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("unset uses the default location", func(t *testing.T) {
		t.Setenv("CODEX_HOME", "")
		found := Detect(home)
		if len(found) != 1 || found[0].Dir != filepath.Join(home, ".codex") {
			t.Fatalf("detected %+v, want ~/.codex", found)
		}
	})

	t.Run("set relocates detection and install", func(t *testing.T) {
		t.Setenv("CODEX_HOME", relocated)
		found := Detect(home)
		if len(found) != 1 || found[0].Dir != relocated {
			t.Fatalf("detected %+v, want %s", found, relocated)
		}
		if _, err := Install(home, found, false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(relocated, "skills", "bosun-review", "SKILL.md")); err != nil {
			t.Fatalf("skill not installed under CODEX_HOME: %v", err)
		}
		// And not in the place Codex is no longer reading.
		if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "bosun-review", "SKILL.md")); err == nil {
			t.Error("skill was also installed under ~/.codex, which Codex is not reading")
		}
	})

	// A relocated directory that does not exist is not a target; detection
	// reports what is there, it does not create it.
	t.Run("set to a missing directory detects nothing", func(t *testing.T) {
		t.Setenv("CODEX_HOME", filepath.Join(relocated, "nope"))
		if found := Detect(home); len(found) != 0 {
			t.Errorf("detected %+v, want nothing", found)
		}
	})
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

// Bosun's review of this skill found two launch commands that disagreed, and a
// $base computed into a variable nothing used. Checking that flag names appear
// somewhere in the document could not see either. Assert the assembled command.
func TestSkillHasOneCompleteLaunchCommand(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	var launches []string
	for _, line := range strings.Split(out.String(), "\n") {
		// The working-tree alternative is prose, not a launch; a real launch
		// captures the job name.
		if strings.Contains(line, "$(bosun review ") {
			launches = append(launches, line)
		}
	}
	if len(launches) != 1 {
		t.Fatalf("found %d launch commands, want exactly 1:\n%s", len(launches), strings.Join(launches, "\n"))
	}
	for _, want := range []string{
		`--branch "$BRANCH"`,      // or the branch is not reviewed at all
		`${BASE:+--base "$BASE"}`, // or a verified PR base is discarded
		"--detach",                // or it blocks past the agent's timeout
		"--local-credentials",
		`--provider "$PROVIDER"`, // or a Claude-only machine cannot run it
	} {
		if !strings.Contains(launches[0], want) {
			t.Errorf("launch command is missing %s:\n%s", want, launches[0])
		}
	}

	// Every variable the launch depends on must be assigned before it.
	launchAt := strings.Index(out.String(), launches[0])
	for _, v := range []string{"BRANCH=", "BASE=", "PROVIDER="} {
		at := strings.Index(out.String(), v)
		if at < 0 || at > launchAt {
			t.Errorf("%s is not assigned before the launch command", v)
		}
	}
}

func TestSkillVerifiesUpstreamPRBase(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"baseRefOid", "baseRefName", "FETCH_HEAD", `"$FETCHED_SHA" != "$BASE_SHA"`, `BASE="$BASE_SHA"`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("skill does not verify upstream PR base: missing %q", want)
		}
	}
	if strings.Contains(out.String(), `git fetch origin "$BASE"`) {
		t.Error("skill still fetches the comparison base from origin")
	}
}

func TestSkillResolvesForkBaseAndFailsClosed(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	start := strings.Index(out.String(), "BRANCH=\"$(git branch --show-current)\"")
	if start < 0 {
		t.Fatal("cannot locate base resolution in installed skill")
	}
	end := strings.Index(out.String()[start:], "# An explicit choice wins")
	if end < 0 {
		t.Fatal("cannot locate base resolution in installed skill")
	}
	baseScript := out.String()[start : start+end]
	upstream := t.TempDir()
	gitForSkillTest(t, upstream, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(upstream, "common"), []byte("common"), 0600); err != nil {
		t.Fatal(err)
	}
	gitForSkillTest(t, upstream, "add", ".")
	gitForSkillTest(t, upstream, "commit", "-m", "common")
	fork := filepath.Join(t.TempDir(), "fork")
	gitForSkillTest(t, upstream, "clone", upstream, fork)
	gitForSkillTest(t, fork, "checkout", "-b", "feature")
	gitForSkillTest(t, fork, "commit", "--allow-empty", "-m", "feature")
	gitForSkillTest(t, fork, "checkout", "main")
	gitForSkillTest(t, fork, "commit", "--allow-empty", "-m", "fork main")
	gitForSkillTest(t, upstream, "commit", "--allow-empty", "-m", "upstream main")
	baseSHA := gitForSkillTest(t, upstream, "rev-parse", "HEAD")
	forkSHA := gitForSkillTest(t, fork, "rev-parse", "main")
	if baseSHA == forkSHA {
		t.Fatal("test setup did not diverge the bases")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$MOCK_PR_DATA\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, sha, url string
		wantSuccess    bool
	}{
		{name: "upstream base", sha: baseSHA, url: "https://github.com/upstream/repo/pull/1", wantSuccess: true},
		{name: "moved base", sha: forkSHA, url: "https://github.com/upstream/repo/pull/1"},
		{name: "failed fetch", sha: baseSHA, url: "https://github.com/missing/repo/pull/1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", baseScript+"\nprintf 'BASE=%s\\n' \"$BASE\"\n")
			cmd.Dir = fork
			cmd.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"MOCK_PR_DATA="+tc.url+"\tmain\t"+tc.sha,
				"GIT_CONFIG_COUNT=1",
				"GIT_CONFIG_KEY_0=url."+upstream+".insteadOf",
				"GIT_CONFIG_VALUE_0=https://github.com/upstream/repo.git",
			)
			result, err := cmd.CombinedOutput()
			if tc.wantSuccess {
				if err != nil || !strings.Contains(string(result), "BASE="+baseSHA) {
					t.Fatalf("base resolution failed: %v: %s", err, result)
				}
			} else if err == nil {
				t.Fatalf("base resolution accepted invalid upstream: %s", result)
			}
		})
	}
}

func gitForSkillTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return strings.TrimSpace(string(b))
}

// The skill drives the CLI, so its instructions have to match the CLI that
// ships with it. These are the contracts that would break silently.
func TestSkillMatchesTheCLIContract(t *testing.T) {
	var out bytes.Buffer
	if err := Print(&out); err != nil {
		t.Fatal(err)
	}
	// The document wraps, so collapse whitespace before looking for sentences:
	// a contract that breaks when a line rewraps is testing the formatting.
	body := strings.Join(strings.Fields(out.String()), " ")
	for _, want := range []string{
		"bosun review",
		"--detach",
		"--local-credentials",
		"bosun review-status",
		"--json",
		// Found by reviewing this skill with Bosun: the default provider is
		// codex-bosun whatever credentials the machine has, so the skill has to
		// offer the Claude selection. The flag itself takes "$PROVIDER", so
		// assert the value is reachable rather than a literal flag spelling.
		"claude-bosun",
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
	// A named PR is identified by its commit; a branch name can be stale or
	// belong to another fork.
	if !strings.Contains(body, "headRefOid") {
		t.Error("skill identifies a named PR by branch name rather than commit")
	}
	// Hardcoding the provider silently overrode an operator's explicit choice
	// and re-broke Claude-only machines, which an earlier review had fixed.
	if !strings.Contains(body, `PROVIDER="${BOSUN_REVIEW_PROVIDER:-}"`) {
		t.Error("skill does not resolve the provider from BOSUN_REVIEW_PROVIDER")
	}
	// review-status exits non-zero with no terminal event when it cannot load a
	// job, and a pipeline hides that, so polling would never end.
	for _, want := range []string{"Check it actually submitted before polling", "Bound it."} {
		if !strings.Contains(body, want) {
			t.Errorf("skill does not guard polling: missing %q", want)
		}
	}
}
