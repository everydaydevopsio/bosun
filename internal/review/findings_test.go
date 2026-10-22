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

func TestParseFindings(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         int
		wantSummary  string
	}{
		{
			name: "well formed",
			output: "Summary line.\n\n```json\n" +
				`{"findings":[{"path":"a.go","line":3,"end_line":5,"severity":"high","title":"T","body":"B","remediation":"R"}]}` +
				"\n```",
			want: 1, wantSummary: "Summary line.",
		},
		{
			// A model that skips the output contract must not lose its review.
			name: "absent", output: "Summary line.\n\nNo findings.", want: 0,
			wantSummary: "Summary line.\n\nNo findings.",
		},
		{
			name: "malformed json", output: "Summary.\n\n```json\n{\"findings\":[\n```", want: 0,
			wantSummary: "Summary.\n\n```json\n{\"findings\":[\n```",
		},
		{
			// Markdown that happens to contain a JSON sample is not a contract.
			name: "unrelated json block", output: "Summary.\n\n```json\n{\"other\":1}\n```", want: 0,
			wantSummary: "Summary.\n\n```json\n{\"other\":1}\n```",
		},
		{
			name: "drops entries that cannot anchor",
			output: "S.\n\n```json\n" +
				`{"findings":[{"path":"","line":1,"severity":"high","title":"T"},` +
				`{"path":"a.go","line":0,"severity":"high","title":"T"},` +
				`{"path":"../etc/passwd","line":1,"severity":"high","title":"T"},` +
				`{"path":"/abs.go","line":1,"severity":"high","title":"T"},` +
				`{"path":"a.go","line":1,"severity":"urgent","title":"T"},` +
				`{"path":"a.go","line":1,"severity":"high","title":""},` +
				`{"path":"ok.go","line":1,"severity":"low","title":"T"}]}` +
				"\n```",
			want: 1, wantSummary: "S.",
		},
		{
			name: "end_line before line is dropped",
			output: "S.\n\n```json\n" +
				`{"findings":[{"path":"a.go","line":9,"end_line":2,"severity":"low","title":"T"}]}` +
				"\n```",
			want: 0, wantSummary: "S.",
		},
		{
			// The block the contract defines is the last one, so a review that
			// quotes JSON in its prose still publishes its real findings.
			name: "takes the last block",
			output: "S.\n\n```json\n{\"other\":1}\n```\n\n```json\n" +
				`{"findings":[{"path":"a.go","line":1,"severity":"low","title":"T"}]}` +
				"\n```",
			want: 1, wantSummary: "S.\n\n```json\n{\"other\":1}\n```",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			summary, findings := parseFindings(tc.output)
			if len(findings) != tc.want {
				t.Fatalf("got %d findings, want %d: %+v", len(findings), tc.want, findings)
			}
			if strings.TrimSpace(summary) != strings.TrimSpace(tc.wantSummary) {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummary)
			}
		})
	}
}

// Nothing bounds what a provider emits, and every field reaches the GitHub API.
func TestParseFindingsBoundsTheBlock(t *testing.T) {
	var entries []string
	for i := 0; i < findingLimit+10; i++ {
		entries = append(entries, `{"path":"a.go","line":1,"severity":"low","title":"T","body":"`+strings.Repeat("x", fieldLimit+100)+`"}`)
	}
	_, findings := parseFindings("S.\n\n```json\n{\"findings\":[" + strings.Join(entries, ",") + "]}\n```")
	if len(findings) != findingLimit {
		t.Fatalf("got %d findings, want the limit %d", len(findings), findingLimit)
	}
	for _, f := range findings {
		if len([]rune(f.Body)) > fieldLimit {
			t.Fatalf("field not bounded: %d runes", len([]rune(f.Body)))
		}
	}
}

// Severity drives what a consuming agent does first, so the order published
// must not depend on the order the model happened to emit.
func TestFindingsAreOrderedBySeverity(t *testing.T) {
	_, findings := parseFindings("S.\n\n```json\n" +
		`{"findings":[{"path":"a.go","line":1,"severity":"low","title":"L"},` +
		`{"path":"a.go","line":2,"severity":"blocker","title":"X"},` +
		`{"path":"a.go","line":3,"severity":"medium","title":"M"},` +
		`{"path":"a.go","line":4,"severity":"high","title":"H"}]}` + "\n```")
	var got []string
	for _, f := range findings {
		got = append(got, f.Severity)
	}
	if strings.Join(got, ",") != "blocker,high,medium,low" {
		t.Fatalf("severity order = %v", got)
	}
}

// The person who typed `bosun review` reads the result. The contract block is
// for the publisher, and raw JSON in a terminal is a worse read than the prose
// it duplicates.
func TestLocalOutputRendersFindingsAsText(t *testing.T) {
	workspace := t.TempDir()
	testGit(t, workspace, "init")
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_LOCAL_PATH", workspace)
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/main")

	var out bytes.Buffer
	w := Worker{PromptPath: prompt, Output: &out, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		return "Summary.\n\n```json\n" +
			`{"findings":[{"path":"a.go","line":3,"severity":"high","title":"Leaks the token","remediation":"Redact it."}]}` +
			"\n```", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(out.String(), "```json") {
		t.Fatalf("contract block reached the terminal: %q", out.String())
	}
	for _, want := range []string{"Summary.", "a.go:3", "Leaks the token", "Redact it."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output lost %q: %q", want, out.String())
		}
	}
}

// A finding needs a name a person can quote back. The model does not choose
// it: an identifier it invented would not be stable across passes.
func TestFindingIDsAreAssignedInSeverityOrder(t *testing.T) {
	_, findings := parseFindings("S.\n\n```json\n" +
		`{"findings":[{"id":"model-chosen","path":"a.go","line":1,"severity":"low","title":"L"},` +
		`{"path":"a.go","line":2,"severity":"blocker","title":"X","confidence":"high","uncertainty":"Not run."}]}` + "\n```")
	if len(findings) != 2 {
		t.Fatalf("got %d findings", len(findings))
	}
	if findings[0].ID != "F1" || findings[0].Severity != "blocker" {
		t.Fatalf("first finding = %+v", findings[0])
	}
	if findings[1].ID != "F2" {
		t.Fatalf("model-chosen id survived: %+v", findings[1])
	}
	if findings[0].Confidence != "high" || findings[0].Uncertainty != "Not run." {
		t.Fatalf("hedge dropped: %+v", findings[0])
	}
}

// The structured half must reach the event stream, because that is what the
// local CLI stores and what survives after the Job and its logs are gone.
func TestEventStreamCarriesStructuredFindings(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_EVENTS", "1")
	t.Setenv("BOSUN_RUN_ID", "run")
	t.Setenv("BOSUN_LOCAL_PATH", repo)
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/main")

	var events, out bytes.Buffer
	w := Worker{PromptPath: prompt, Events: &events, Output: &out, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		return "Summary.\n\n```json\n" +
			`{"findings":[{"path":"a.go","line":3,"severity":"high","title":"T","confidence":"high"}]}` +
			"\n```", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatalf("run: %v", err)
	}
	var structured, result string
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var e progress.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		switch e.Type {
		case "findings":
			structured = e.Message
		case "result":
			result = e.Message
		}
	}
	if structured == "" {
		t.Fatal("no findings event emitted")
	}
	var parsed Result
	if err := json.Unmarshal([]byte(structured), &parsed); err != nil {
		t.Fatalf("findings event is not the structured result: %v", err)
	}
	if parsed.SchemaVersion != ResultSchemaVersion || len(parsed.Findings) != 1 || parsed.Findings[0].ID != "F1" {
		t.Fatalf("payload = %+v", parsed)
	}
	if strings.Contains(result, "```json") {
		t.Fatalf("contract block duplicated into the human result: %q", result)
	}
}

// A clean review has to survive the whole path -- prompt contract, worker,
// event stream -- as a clean review. If the empty block is lost anywhere, it
// arrives as "the reviewer reported nothing readable", which is a different
// claim about a different review.
func TestEventStreamReportsACleanReviewAsClean(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_EVENTS", "1")
	t.Setenv("BOSUN_RUN_ID", "run")
	t.Setenv("BOSUN_LOCAL_PATH", repo)
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/main")

	var events, out bytes.Buffer
	w := Worker{PromptPath: prompt, Events: &events, Output: &out, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		return "No material findings.\n\n```json\n{\"findings\":[]}\n```", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatalf("run: %v", err)
	}
	found := false
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var e progress.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type != "findings" {
			continue
		}
		found = true
		var parsed Result
		if err := json.Unmarshal([]byte(e.Message), &parsed); err != nil {
			t.Fatal(err)
		}
		if len(parsed.Findings) != 0 {
			t.Fatalf("clean review carried findings: %+v", parsed)
		}
	}
	if !found {
		t.Fatal("a clean review emitted no findings event, so it cannot be told apart from an unreported one")
	}
}

// The prompt is half of the contract: if it tells the reviewer to omit the
// block when it finds nothing, every clean review arrives unreadable.
func TestPromptRequiresAnEmptyBlockForACleanReview(t *testing.T) {
	prompt, err := os.ReadFile(filepath.Join("..", "..", "prompts", "code-review.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), `{"findings":[]}`) {
		t.Fatal("the prompt does not require an empty findings block for a clean review")
	}
}

// A review that honoured the contract and found nothing is not the same as one
// that ignored it, and the event stream has to preserve the difference.
func TestStructuredResultDistinguishesCleanFromUnreported(t *testing.T) {
	if got := StructuredResult("Summary.\n\n```json\n{\"findings\":[]}\n```"); got == "" {
		t.Fatal("a clean review reported nothing structured")
	} else if !strings.Contains(got, `"findings":[]`) {
		t.Fatalf("payload = %q", got)
	}
	if got := StructuredResult("Summary with no contract block."); got != "" {
		t.Fatalf("invented a clean review: %q", got)
	}
	if got := StructuredResult("S.\n\n```json\n{not json\n```"); got != "" {
		t.Fatalf("invented a clean review from a malformed block: %q", got)
	}
}

// The transport between the provider and the publisher consumes any output
// line that parses as JSON, so the contract payload travels behind a marker.
// Measured against the shipped image: a provider printing BEFORE, then
// {"findings":[]}, then AFTER delivers "BEFOREAFTER".
func TestParseReviewReadsTheMarkerLine(t *testing.T) {
	output := strings.Join([]string{
		"Reviewed commit `abc` against merge base `def`.",
		"- **High — Something** (`a.go:1`). It breaks.",
		`BOSUN-FINDINGS {"findings":[{"path":"a.go","line":1,"severity":"high","title":"Something","body":"It breaks."}]}`,
		"BOSUN-REVIEW: complete",
	}, "\n")

	summary, findings, reported, _ := parseReview(output)
	if !reported {
		t.Fatal("the marker line was not recognised as the contract payload")
	}
	if len(findings) != 1 || findings[0].Path != "a.go" {
		t.Fatalf("findings = %+v", findings)
	}
	if strings.Contains(summary, "BOSUN-FINDINGS") {
		t.Errorf("the payload leaked into the human summary:\n%s", summary)
	}
	if !strings.Contains(summary, "It breaks.") || !strings.Contains(summary, "BOSUN-REVIEW: complete") {
		t.Errorf("summary lost prose around the payload:\n%s", summary)
	}
}

// An empty findings list from the marker is a clean review, and must stay
// distinguishable from a reviewer that emitted no payload at all.
func TestParseReviewMarkerDistinguishesCleanFromSilent(t *testing.T) {
	_, findings, reported, _ := parseReview("All good.\n" + `BOSUN-FINDINGS {"findings":[]}`)
	if !reported {
		t.Error("an explicit empty payload should count as reporting")
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v", findings)
	}

	if _, _, silent, _ := parseReview("All good, trust me."); silent {
		t.Error("prose with no payload must not count as reporting")
	}
}

// The word appearing mid-sentence -- a reviewer quoting the contract back, or
// this repository's own prompt being reviewed -- is not the payload.
func TestParseReviewIgnoresTheMarkerInsideProse(t *testing.T) {
	output := "The prompt tells the model to emit BOSUN-FINDINGS {\"findings\":[]} on its own line."
	if _, _, reported, _ := parseReview(output); reported {
		t.Error("a mid-line mention was treated as the contract payload")
	}
}

// Transcripts and adapters that never passed through the line filter still
// carry a fenced block, and must keep parsing.
func TestParseReviewStillAcceptsTheFencedBlock(t *testing.T) {
	output := "Summary.\n\n```json\n{\"findings\":[{\"path\":\"b.go\",\"line\":2,\"severity\":\"low\",\"title\":\"T\",\"body\":\"B\"}]}\n```\n"
	_, findings, reported, _ := parseReview(output)
	if !reported || len(findings) != 1 || findings[0].Path != "b.go" {
		t.Fatalf("fenced fallback broke: reported=%v findings=%+v", reported, findings)
	}
}
