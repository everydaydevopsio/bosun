package review

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everydaydevopsio/bosun/internal/config"
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
