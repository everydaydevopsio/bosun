package review

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeTree(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, body)
}

// coverageRepo builds a base commit and a branch commit that edits source,
// rewrites a lockfile, adds a generated file, and deletes a file.
func coverageRepo(t *testing.T) (dir, base string) {
	t.Helper()
	dir = t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	writeTree(t, filepath.Join(dir, "src", "app.ts"), "one\ntwo\n")
	writeTree(t, filepath.Join(dir, "src", "old.ts"), "gone\n")
	writeTree(t, filepath.Join(dir, "package-lock.json"), "{}\n")
	writeTree(t, filepath.Join(dir, ".gitattributes"), "gen/** linguist-generated\n")
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	base = gitRun(t, dir, "rev-parse", "HEAD")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeTree(t, filepath.Join(dir, "src", "app.ts"), "one\nTWO\nthree\n")
	writeTree(t, filepath.Join(dir, "src", "util.ts"), "u\n")
	writeTree(t, filepath.Join(dir, "package-lock.json"), strings.Repeat("{\"x\":1}\n", 500))
	writeTree(t, filepath.Join(dir, "gen", "api.ts"), "a\nb\n")
	if err := os.Remove(filepath.Join(dir, "src", "old.ts")); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "feature")
	return dir, base
}

// #93 AC1: diff size separates lockfile and generated content from what a
// reviewer has to read.
func TestDiffStatsSeparatesGeneratedContent(t *testing.T) {
	dir, base := coverageRepo(t)
	stats, err := diffStats(context.Background(), dir, base, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats.MergeBase != base || stats.WorkingTree {
		t.Fatalf("stats = %+v", stats)
	}
	// app.ts (+2 -1), util.ts (+1), old.ts (-1)
	if got := stats.Reviewable; got.Files != 3 || got.Additions != 3 || got.Deletions != 2 {
		t.Fatalf("reviewable = %+v", got)
	}
	if got := stats.Generated; got.Files != 2 || got.Additions != 502 || got.Deletions != 1 {
		t.Fatalf("generated = %+v", got)
	}
	if strings.Join(stats.GeneratedPaths, ",") != "gen/api.ts,package-lock.json" {
		t.Fatalf("generated paths = %v", stats.GeneratedPaths)
	}
}

func TestDiffStatsIncludesWorkingTreeChanges(t *testing.T) {
	dir, base := coverageRepo(t)
	writeTree(t, filepath.Join(dir, "src", "app.ts"), "one\nTWO\nthree\nfour\n")
	writeTree(t, filepath.Join(dir, "src", "new.ts"), "n1\nn2\n")
	stats, err := diffStats(context.Background(), dir, base, true)
	if err != nil {
		t.Fatal(err)
	}
	// app.ts (+3 -1), util.ts (+1), old.ts (-1), new.ts untracked (+2)
	if got := stats.Reviewable; !stats.WorkingTree || got.Files != 4 || got.Additions != 6 || got.Deletions != 2 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestDiffStatsResolvesTheDefaultBranchWhenNoBaseIsGiven(t *testing.T) {
	dir, base := coverageRepo(t)
	if _, err := diffStats(context.Background(), dir, "", false); err == nil {
		t.Fatal("no base and no origin/HEAD should fail, not guess")
	}
	gitRun(t, dir, "update-ref", "refs/remotes/origin/main", base)
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	stats, err := diffStats(context.Background(), dir, "", false)
	if err != nil || stats.MergeBase != base {
		t.Fatalf("stats = %+v, err = %v", stats, err)
	}
}

func TestCoverageJoinsReadsWithTheDiff(t *testing.T) {
	dir, base := coverageRepo(t)
	stats, err := diffStats(context.Background(), dir, base, false)
	if err != nil {
		t.Fatal(err)
	}
	reads := &readRecord{Source: "codex", Method: "command_parsing", Commands: 4,
		Opened: []string{"src/app.ts", "README.md", "docs/a.md"}, Diffed: []string{"package-lock.json"}}
	c := buildCoverage(stats, nil, reads)
	r := c.Reads
	if r == nil || c.Diff == nil {
		t.Fatalf("coverage = %+v", c)
	}
	// util.ts is unexamined; old.ts is deleted (its whole content is the diff);
	// the lockfile is generated and never counts against coverage.
	if r.ChangedFilesOpened != 1 || r.ChangedFilesDiffed != 0 || r.ChangedFilesUnexamined != 1 ||
		strings.Join(r.UnexaminedPaths, ",") != "src/util.ts" || r.ContextFilesOpened != 2 {
		t.Fatalf("reads = %+v", r)
	}
	if !strings.Contains(c.Footer(), "1 of 2 changed files opened") {
		t.Fatalf("footer = %q", c.Footer())
	}
}

func TestCoverageSaysWhatIsMissingInsteadOfGuessing(t *testing.T) {
	c := buildCoverage(nil, os.ErrNotExist, nil)
	if c.Diff != nil || c.Reads != nil || c.DiffNote == "" || c.ReadsNote == "" {
		t.Fatalf("coverage = %+v", c)
	}
	if c.SchemaVersion != CoverageSchemaVersion {
		t.Fatalf("schema = %d", c.SchemaVersion)
	}
}

const sampleReads = `BOSUN_READS_V1 {"source":"codex","method":"command_parsing","commands":3,"full_diff":true,"opened":["a.go","../etc/passwd","/abs","ok/b.go"],"diffed":[],"truncated":false}`

// The reads line is a control line like the usage line: recorded, never
// published, and never accepted from the wrong provider.
func TestRecorderStripsAndRecordsTheReadsLine(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	out := r.filterFinal(sampleUsage + "\n" + sampleReads + "\nReview text\n")
	if out != "Review text\n" {
		t.Fatalf("published %q", out)
	}
	if r.reads == nil || !r.reads.FullDiff || strings.Join(r.reads.Opened, ",") != "a.go,ok/b.go" {
		t.Fatalf("reads = %+v", r.reads)
	}

	other := newUsageRecorder("claude-bosun")
	other.filterFinal(sampleReads + "\n")
	if other.reads != nil {
		t.Fatal("accepted a reads record from another provider")
	}
}

func TestSplitReadsLineIsReassembled(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	half := len(sampleReads) / 2
	out := r.filter(sampleReads[:half]) + r.filter(sampleReads[half:]+"\nok\n")
	if out != "ok\n" || r.reads == nil {
		t.Fatalf("out = %q, reads = %+v", out, r.reads)
	}
}

// #93 AC3: coverage is reported even when the reviewer ignored the findings
// contract, because it is measured, not reported by the model.
func TestWorkerEmitsCoverageWithoutAFindingsBlock(t *testing.T) {
	dir, base := coverageRepo(t)
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_EVENTS", "1")
	t.Setenv("BOSUN_RUN_ID", "run")
	t.Setenv("BOSUN_LOCAL_PATH", dir)
	t.Setenv("BOSUN_BASE_SHA", base)
	t.Setenv("BOSUN_COMMITTED_ONLY", "1")
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/feature")
	var events, out bytes.Buffer
	w := Worker{PromptPath: prompt, Events: &events, Output: &out, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		return sampleReads + "\nProse review with no findings line.\n", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var coverage string
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var e progress.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "coverage" {
			coverage = e.Message
		}
		if strings.Contains(e.Message, "BOSUN_READS_V1") {
			t.Fatalf("control line leaked into %s event", e.Type)
		}
	}
	var c Coverage
	if err := json.Unmarshal([]byte(coverage), &c); err != nil {
		t.Fatalf("coverage event %q: %v", coverage, err)
	}
	if c.Diff == nil || c.Diff.Reviewable.Files != 3 || c.Reads == nil || !c.Reads.FullDiffViewed {
		t.Fatalf("coverage = %+v", c)
	}
}
