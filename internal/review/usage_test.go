package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/everydaydevopsio/bosun/internal/bridgev1"
	"github.com/everydaydevopsio/bosun/internal/config"
	"github.com/everydaydevopsio/bosun/internal/progress"
)

const sampleUsage = `BOSUN_USAGE_V1 {"source":"codex","status":"available","input_tokens":1000,"cached_input_tokens":800,"output_tokens":100,"reasoning_output_tokens":40,"total_tokens":1100,"turns":1}`

// AC: usage survives failures and precedes the terminal event; missing usage
// must never turn into a free review in downstream cost metrics.
func TestWorkerUsageForSuccessFailureAndMissing(t *testing.T) {
	for _, tc := range []struct {
		name, output, outcome, usageStatus string
		err                                error
	}{
		{"success", sampleUsage + "\nA useful review.", "completed", "available", nil},
		{"provider failure", sampleUsage, "failed", "partial", errors.New("provider stopped")},
		{"timeout", "", "timed_out", "missing", context.DeadlineExceeded},
		{"cancellation", "", "cancelled", "missing", context.Canceled},
		{"older provider", "A useful review.", "completed", "missing", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
				t.Fatal(err)
			}
			prompt := filepath.Join(t.TempDir(), "prompt")
			if err := os.WriteFile(prompt, []byte("review"), 0600); err != nil {
				t.Fatal(err)
			}
			for k, v := range map[string]string{"BOSUN_EVENTS": "1", "BOSUN_EVENT_FILE": "", "BOSUN_RUN_ID": "review-123", "BOSUN_LOCAL_PATH": repo, "BOSUN_REPO": "acme/widget", "BOSUN_REF": "feature", "CODEX_AUTH": "", "CLAUDE_CREDENTIALS": "", "OPENAI_API_KEY": "sk-usage-test-secret"} {
				t.Setenv(k, v)
			}
			var logs, events bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			w := Worker{PromptPath: prompt, Events: &events, Agent: func(context.Context, string, string, string, time.Duration) (string, error) { return tc.output, tc.err }}
			_ = w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 10})
			var metrics map[string]any
			usageCount, terminalIndex, usageIndex := 0, -1, -1
			for i, line := range strings.Split(strings.TrimSpace(events.String()), "\n") {
				var e progress.Event
				if err := json.Unmarshal([]byte(line), &e); err != nil {
					t.Fatal(err)
				}
				if e.Type == "usage" {
					usageCount++
					usageIndex = i
					if err := json.Unmarshal([]byte(e.Message), &metrics); err != nil {
						t.Fatal(err)
					}
				}
				if e.Type == tc.outcome {
					terminalIndex = i
				}
				if e.Type == "result" && strings.Contains(e.Message, "BOSUN_USAGE") {
					t.Fatal("telemetry leaked into review")
				}
			}
			if usageCount != 1 || terminalIndex <= usageIndex {
				t.Fatalf("usage must occur once before terminal: %s", events.String())
			}
			if metrics["outcome"] != tc.outcome || metrics["run_id"] != "review-123" || metrics["actual_cost_usd"] != nil {
				t.Fatalf("bad metrics: %+v", metrics)
			}
			u, ok := metrics["usage"].(map[string]any)
			if !ok || u["status"] != tc.usageStatus {
				t.Fatalf("bad usage: %+v", metrics)
			}
			if tc.output == "" && u["input_tokens"] != nil {
				t.Fatal("missing tokens became zero")
			}
			if strings.Count(logs.String(), `"event":"bosun.review.usage"`) != 1 {
				t.Fatalf("missing/duplicate summary log: %s", logs.String())
			}
			if strings.Contains(logs.String(), "sk-usage-test-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestWorkerUsageLogsSetupFailureWithoutEvents(t *testing.T) {
	t.Setenv("BOSUN_EVENTS", "")
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	err := (Worker{}).Run(context.Background(), config.Config{})
	if err == nil || !strings.Contains(logs.String(), `"failure_stage":"setup"`) || !strings.Contains(logs.String(), `"status":"missing"`) {
		t.Fatalf("early failure not counted: err=%v logs=%s", err, logs.String())
	}
}

func TestUsageSurvivesBridgeFailureAndDuplicateReplay(t *testing.T) {
	meter := newUsageRecorder("codex-bosun")
	ctx := context.WithValue(context.Background(), usageKey{}, meter)
	f := &fakeBridge{events: []*pb.AttachSessionEvent{
		{Seq: 1, Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte(sampleUsage)},
		{Seq: 1, Type: pb.AttachEventType_ATTACH_EVENT_TYPE_OUTPUT, Payload: []byte(sampleUsage)},
		{Seq: 2, Type: pb.AttachEventType_ATTACH_EVENT_TYPE_SESSION_EXIT, ExitCode: 1},
	}}
	_, err := runSession(ctx, f, "/repo", "codex-bosun", "review")
	if err == nil || meter.usage.InputTokens == nil || *meter.usage.InputTokens != 1000 {
		t.Fatalf("lost or duplicated failed-run usage: %+v %v", meter.usage, err)
	}
	if strings.Contains(err.Error(), "BOSUN_USAGE") {
		t.Fatal("telemetry leaked into diagnostics")
	}
}

func TestUsageRejectsInvalidCountersAndDoesNotEchoFields(t *testing.T) {
	for _, value := range []string{
		`{"source":"codex","status":"available","input_tokens":-1,"output_tokens":2}`,
		`{"source":"codex","status":"available","input_tokens":1.5,"output_tokens":2}`,
		`{"source":"codex","status":"available","input_tokens":1,"output_tokens":2,"cached_input_tokens":3}`,
		`{"source":"codex","status":"available","input_tokens":1,"output_tokens":2,"reasoning_output_tokens":3}`,
		`{"source":"codex","status":"available","input_tokens":1,"output_tokens":2,"total_tokens":5}`,
		`{"source":"claude","status":"available","input_tokens":1,"output_tokens":2}`,
		`{"source":"codex","status":"available","provider_reported_cost_usd":-1}`,
		`not JSON containing private prompt text`,
	} {
		meter := newUsageRecorder("codex-bosun")
		out := meter.filter(usagePrefix + value + "\nReview.\n")
		if meter.seen || out != "Review.\n" {
			t.Fatalf("bad usage accepted or leaked: %+v %q", meter.usage, out)
		}
	}
	meter := newUsageRecorder("codex-bosun")
	meter.filter(sampleUsage[:len(sampleUsage)-1] + `,"prompt":"private source","api_key":"secret"}`)
	encoded, err := json.Marshal(meter.usage)
	if err != nil || strings.Contains(string(encoded), "private source") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("unrecognized fields escaped: %s %v", encoded, err)
	}
}

func TestUsageModelRedactionDoesNotCorruptNumbers(t *testing.T) {
	model := "sk-model-secret"
	u := ProviderUsage{Source: "codex", Status: "available", TokenUsage: TokenUsage{Model: &model}, ModelUsage: []TokenUsage{{Model: &model}}}
	clean := sanitizedUsage(u, func(s string) string { return strings.ReplaceAll(s, model, "[REDACTED]") })
	data, err := json.Marshal(clean)
	if err != nil || strings.Contains(string(data), model) {
		t.Fatalf("model credential leaked: %s", data)
	}
	if *u.Model != model {
		t.Fatal("redaction modified collected usage")
	}
}

func TestUnknownModelPriceIsNotAZeroCost(t *testing.T) {
	meter := newUsageRecorder("claude-bosun")
	meter.filter(usagePrefix + `{"source":"claude","status":"available","input_tokens":100,"output_tokens":20,"provider_reported_cost_usd":0,"model_usage":[{"model":"claude-new","input_tokens":100,"output_tokens":20,"cost_basis":"unknown","provider_reported_cost_usd":0}]}` + "\n")
	if !meter.seen || meter.usage.ProviderReportedCostUSD != nil || meter.usage.CostBasis != "unknown" {
		t.Fatalf("unknown price presented as free: %+v", meter.usage)
	}
}

// bridgectl is documented to forward stdout a line at a time, but nothing
// enforces it, and bridge.go appends a newline to any argument-provider payload
// that lacks one -- which bisects a split line permanently. A usage record
// split across two payloads then lost its first half to a failed parse and
// emitted the second half as review prose: provider JSON in the published
// review, and usage reported as missing.
func TestSplitUsageLineIsReassembledAndNeverLeaks(t *testing.T) {
	line := `BOSUN_USAGE_V1 {"source":"codex","status":"available","input_tokens":120,"output_tokens":30}` + "\n"
	for _, at := range []int{1, 15, 40, len(line) - 2} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			r := newUsageRecorder("codex-bosun")
			var out strings.Builder
			out.WriteString(r.filter("Review text.\n" + line[:at]))
			out.WriteString(r.filter(line[at:]))
			out.WriteString(r.flushPartial())
			if got := out.String(); got != "Review text.\n" {
				t.Fatalf("output %q; provider JSON must not reach the review", got)
			}
			if !r.seen || r.usage.InputTokens == nil || *r.usage.InputTokens != 120 {
				t.Fatalf("usage lost across the split: %+v", r.usage)
			}
		})
	}
}

// Holding back every unterminated chunk would stall ordinary review text behind
// the next payload. Only a fragment that could still become the marker waits.
func TestOrdinaryTextIsNotDelayedByUsageBuffering(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	if got := r.filter("a partial sentence with no newline"); got != "a partial sentence with no newline" {
		t.Fatalf("ordinary text was withheld: %q", got)
	}
	if r.partial != "" {
		t.Fatalf("ordinary text buffered: %q", r.partial)
	}
}

// A fragment that looks like the marker's start but never completes is review
// text after all; flushing must return it rather than silently dropping it.
func TestUnfinishedMarkerFragmentIsFlushedNotDropped(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	if got := r.filter("BOSUN_US"); got != "" {
		t.Fatalf("fragment emitted before it could be judged: %q", got)
	}
	if got := r.flushPartial(); got != "BOSUN_US" {
		t.Fatalf("flush returned %q, want the held fragment", got)
	}
	if got := r.flushPartial(); got != "" {
		t.Fatalf("flush is not idempotent: %q", got)
	}
}

// runner.go re-filters the fully assembled output with the same recorder. A
// complete output has no "more data coming", so holding a trailing fragment
// there would drop it -- nothing flushes after that pass.
func TestFinalFilterHoldsNothingBack(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	if got := r.filterFinal("Review text.\nBOSUN_US"); got != "Review text.\nBOSUN_US" {
		t.Fatalf("final pass withheld %q", got)
	}
	if r.partial != "" {
		t.Fatalf("final pass buffered %q", r.partial)
	}
}

// The adapters terminate the marker, but a stream can still end without a
// newline. Such a line is a record, not prose: flushing must record it rather
// than publish provider JSON as the tail of the review.
func TestUnterminatedFinalUsageLineIsRecordedNotPublished(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	held := r.filter(`BOSUN_USAGE_V1 {"source":"codex","status":"available","input_tokens":7,"output_tokens":3}`)
	if held != "" {
		t.Fatalf("emitted %q mid-stream", held)
	}
	if got := r.flushPartial(); got != "" {
		t.Fatalf("flush published provider JSON: %q", got)
	}
	if !r.seen || r.usage.InputTokens == nil || *r.usage.InputTokens != 7 {
		t.Fatalf("unterminated record was not captured: %+v", r.usage)
	}
}

// With newlines stripped, a record and the text that followed it can arrive as
// one payload. The record is consumed and the trailing text survives as review
// prose -- dropping the whole payload would lose the review.
func TestRecordAndReviewTextInOnePayloadBothSurvive(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	got := r.filter(`BOSUN_USAGE_V1 {"source":"codex","status":"available","input_tokens":11,"output_tokens":2}` + "Trailing review text.")
	if got != "Trailing review text." {
		t.Fatalf("output %q", got)
	}
	if !r.seen || r.usage.InputTokens == nil || *r.usage.InputTokens != 11 {
		t.Fatalf("record not consumed: %+v", r.usage)
	}
}

// A malformed record is dropped, not published: the point of the reserved line
// is that provider JSON never reaches the review, valid or not.
func TestMalformedRecordIsDroppedNotPublished(t *testing.T) {
	r := newUsageRecorder("codex-bosun")
	if got := r.filter(`BOSUN_USAGE_V1 {"source":"codex",,,}` + "\n"); got != "" {
		t.Fatalf("malformed record published: %q", got)
	}
	if r.seen {
		t.Fatal("malformed record was recorded")
	}
}

// #95: the usage summary records which effort ran, so cost comparisons
// between reviews are honest.
func TestUsageRecordsTheReviewEffort(t *testing.T) {
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
	t.Setenv("BOSUN_REVIEW_EFFORT", "deep")
	var events bytes.Buffer
	w := Worker{PromptPath: prompt, Events: &events, Output: &bytes.Buffer{}, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		return "Clean.", nil
	}}
	if err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&events)
	for decoder.More() {
		var e progress.Event
		if err := decoder.Decode(&e); err != nil {
			t.Fatal(err)
		}
		if e.Type == "usage" {
			var u UsageSummary
			if err := json.Unmarshal([]byte(e.Message), &u); err != nil || u.Effort != "deep" {
				t.Fatalf("usage = %s (%v)", e.Message, err)
			}
			return
		}
	}
	t.Fatal("no usage event")
}

func TestAnUnknownEffortFailsSetup(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(t.TempDir(), "prompt.md")
	writeTestFile(t, prompt, "review this")
	t.Setenv("BOSUN_LOCAL_PATH", repo)
	t.Setenv("BOSUN_REPO", "everydaydevopsio/bosun")
	t.Setenv("BOSUN_REF", "refs/heads/main")
	t.Setenv("BOSUN_REVIEW_EFFORT", "max")
	ran := false
	w := Worker{PromptPath: prompt, Output: &bytes.Buffer{}, Agent: func(context.Context, string, string, string, time.Duration) (string, error) {
		ran = true
		return "Clean.", nil
	}}
	err := w.Run(context.Background(), config.Config{ReviewProvider: "codex-bosun", ReviewTimeoutSeconds: 60})
	if err == nil || ran || !strings.Contains(err.Error(), "effort") {
		t.Fatalf("err = %v, provider ran = %v", err, ran)
	}
}
