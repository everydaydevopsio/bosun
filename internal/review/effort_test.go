package review

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
)

// escalationRun reviews coverageRepo's branch (5 reviewable lines: +3/−2)
// with the given effort setting and threshold. It returns the effort the
// provider saw in its environment, the usage summary, and the coverage.
func escalationRun(t *testing.T, effort string, threshold int, withBase bool) (string, UsageSummary, Coverage) {
	t.Helper()
	dir, base := coverageRepo(t)
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_EVENTS", "1")
	t.Setenv("BOSUN_RUN_ID", "run")
	t.Setenv("BOSUN_LOCAL_PATH", dir)
	if withBase {
		t.Setenv("BOSUN_BASE_SHA", base)
	} else {
		t.Setenv("BOSUN_BASE_SHA", "")
	}
	t.Setenv("BOSUN_COMMITTED_ONLY", "1")
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/feature")
	t.Setenv("BOSUN_REVIEW_EFFORT", effort)
	var seen string
	var events bytes.Buffer
	w := Worker{PromptPath: prompt, Events: &events, Output: &bytes.Buffer{}, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		seen = os.Getenv("BOSUN_REVIEW_EFFORT")
		return "Clean.", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60, ReviewEscalateAboveLines: threshold}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var usage UsageSummary
	var coverage Coverage
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var e progress.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case "usage":
			if err := json.Unmarshal([]byte(e.Message), &usage); err != nil {
				t.Fatal(err)
			}
		case "coverage":
			if err := json.Unmarshal([]byte(e.Message), &coverage); err != nil {
				t.Fatal(err)
			}
		}
	}
	return seen, usage, coverage
}

// #105 AC1: a diff above the threshold runs at deep, and the result says it
// was escalated and why.
func TestAutoEffortEscalatesALargeDiff(t *testing.T) {
	seen, usage, coverage := escalationRun(t, "auto", 4, true)
	const why = "reviewable_lines=5 > 4"
	if seen != "deep" || usage.Effort != "deep" || usage.EffortEscalation != why {
		t.Fatalf("provider saw %q; usage effort %q, escalation %q", seen, usage.Effort, usage.EffortEscalation)
	}
	if coverage.Effort != "deep" || coverage.EffortEscalation != why {
		t.Fatalf("coverage effort %q, escalation %q", coverage.Effort, coverage.EffortEscalation)
	}
}

// At or below the threshold, and with escalation off, auto runs standard.
func TestAutoEffortStaysStandardOtherwise(t *testing.T) {
	for _, threshold := range []int{5, 0} {
		seen, usage, coverage := escalationRun(t, "", threshold, true)
		if seen != "standard" || usage.Effort != "standard" || usage.EffortEscalation != "" || coverage.EffortEscalation != "" {
			t.Fatalf("threshold %d: provider saw %q; usage %q/%q", threshold, seen, usage.Effort, usage.EffortEscalation)
		}
	}
}

// #105 AC2: an explicit effort always overrides escalation.
func TestExplicitEffortOverridesEscalation(t *testing.T) {
	seen, usage, _ := escalationRun(t, "standard", 1, true)
	if seen != "standard" || usage.Effort != "standard" || usage.EffortEscalation != "" {
		t.Fatalf("provider saw %q; usage %q/%q", seen, usage.Effort, usage.EffortEscalation)
	}
}

// A change that cannot be sized is not escalated: sizing is a heuristic, and
// failing the review over it would lose the review entirely.
func TestUnsizableChangeRunsStandard(t *testing.T) {
	seen, usage, _ := escalationRun(t, "auto", 1, false)
	if seen != "standard" || usage.Effort != "standard" || usage.EffortEscalation != "" {
		t.Fatalf("provider saw %q; usage %q/%q", seen, usage.Effort, usage.EffortEscalation)
	}
}

// The hosted footer names a deep review, and an escalation's reason.
func TestFooterNamesTheEffort(t *testing.T) {
	c := Coverage{Diff: &DiffStats{Reviewable: DiffCount{Files: 2, Additions: 1500, Deletions: 340}}, Effort: "deep", EffortEscalation: "reviewable_lines=1840 > 800"}
	if got := c.Footer(); !strings.Contains(got, "deep review, escalated: reviewable_lines=1840 > 800") {
		t.Fatalf("footer = %s", got)
	}
	c.EffortEscalation = ""
	if got := c.Footer(); !strings.Contains(got, "deep review") || strings.Contains(got, "escalated") {
		t.Fatalf("footer = %s", got)
	}
	c.Effort = "standard"
	if got := c.Footer(); strings.Contains(got, "review,") || strings.Contains(got, "deep") || strings.Contains(got, "standard") {
		t.Fatalf("footer = %s", got)
	}
}

func TestInstructionFilesAreRecognised(t *testing.T) {
	for _, p := range []string{"AGENTS.md", "CLAUDE.md", "docs/CLAUDE.md", "skills/release/SKILL.md", ".cursor/rules/go.mdc",
		".claude/commands/ship.md", ".claude/rules/core.md", ".github/workflows/ci.yml", ".github/copilot-instructions.md",
		"docs/runbook-deploy.md", "ops/runbooks/restore.md", "runbooks/restore.md", "Runbooks/Restore.md", "prompts/code-review.md", ".agents/AGENTS.md"} {
		if !isInstructionFile(p) {
			t.Errorf("%s not recognised", p)
		}
	}
	for _, p := range []string{"README.md", "docs/setup.md", "internal/review/runner.go", "skills/release/helper.sh", "agents.go"} {
		if isInstructionFile(p) {
			t.Errorf("%s wrongly recognised", p)
		}
	}
}

// instructionRepo builds a branch whose change is mostly instruction prose.
func instructionRepo(t *testing.T, instructionLines, codeLines int) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	writeTree(t, filepath.Join(dir, "README.md"), "r\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	base = gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeTree(t, filepath.Join(dir, "skills", "ship", "SKILL.md"), strings.Repeat("step\n", instructionLines))
	writeTree(t, filepath.Join(dir, "main.go"), strings.Repeat("x\n", codeLines))
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "feature")
	return dir, base
}

// #110: a small change that is mostly agent instructions runs deep even
// below the size threshold; a small or code-dominated one does not.
func TestInstructionHeavyChangeEscalates(t *testing.T) {
	cases := []struct {
		instr, code int
		effort, why string
	}{
		{120, 30, "deep", "instruction_lines=120 of 150"},
		{20, 0, "standard", ""},
		{60, 90, "standard", ""},
	}
	for _, c := range cases {
		dir, base := instructionRepo(t, c.instr, c.code)
		t.Setenv("BOSUN_COMMITTED_ONLY", "1")
		effort, why := resolveAutoEffort(context.Background(), dir, base, true, 800)
		if effort != c.effort || why != c.why {
			t.Errorf("%d instruction + %d code lines: %q, %q; want %q, %q", c.instr, c.code, effort, why, c.effort, c.why)
		}
	}
	// Escalation off disables this trigger too.
	dir, base := instructionRepo(t, 120, 0)
	if effort, _ := resolveAutoEffort(context.Background(), dir, base, true, 0); effort != "standard" {
		t.Fatalf("threshold 0 escalated: %s", effort)
	}
}
