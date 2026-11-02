package review

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"strings"
	"time"
)

const usagePrefix = "BOSUN_USAGE_V1 "

// UsageLogEvent names the worker's structured usage log. The monitor matches on
// it to keep the measurement envelope out of human terminal output, so producer
// and consumer share this constant rather than repeating the string.
const UsageLogEvent = "bosun.review.usage"

// TokenUsage uses total input (including cache reads/writes) and total output
// (including reasoning). Null means unreported, never free. Cache lifetimes
// are provider-specific; the 30m/12h fields apply only when actually reported.
type TokenUsage struct {
	Model                   *string  `json:"model"`
	InputTokens             *int64   `json:"input_tokens"`
	CachedInputTokens       *int64   `json:"cached_input_tokens"`
	CacheWriteInputTokens   *int64   `json:"cache_write_input_tokens"`
	CacheWrite30mTokens     *int64   `json:"cache_write_30m_tokens"`
	CacheWrite12hTokens     *int64   `json:"cache_write_12h_tokens"`
	OutputTokens            *int64   `json:"output_tokens"`
	ReasoningOutputTokens   *int64   `json:"reasoning_output_tokens"`
	TotalTokens             *int64   `json:"total_tokens"`
	Turns                   *int64   `json:"turns"`
	ProviderReportedCostUSD *float64 `json:"provider_reported_cost_usd"`
	CostBasis               string   `json:"cost_basis,omitempty"`
}

type ProviderUsage struct {
	Source string `json:"source"`
	Status string `json:"status"`
	TokenUsage
	ModelUsage []TokenUsage `json:"model_usage,omitempty"`
}

// UsageSummary is a measurement record, not an invoice. ActualCostUSD stays
// null until an external billing reconciliation can establish that amount.
type UsageSummary struct {
	SchemaVersion   int           `json:"schema_version"`
	RunID           string        `json:"run_id"`
	PodUID          string        `json:"pod_uid,omitempty"`
	Repo            string        `json:"repo"`
	Ref             string        `json:"ref"`
	HeadSHA         string        `json:"head_sha"`
	BaseSHA         string        `json:"base_sha,omitempty"`
	PRNumber        int           `json:"pr_number,omitempty"`
	Trigger         string        `json:"trigger,omitempty"`
	Provider        string        `json:"provider"`
	WorkerVersion   string        `json:"worker_version"`
	WorkerImage     string        `json:"worker_image,omitempty"`
	CredentialMode  string        `json:"credential_mode"`
	Outcome         string        `json:"outcome"`
	FailureStage    Stage         `json:"failure_stage,omitempty"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	WorkerSeconds   float64       `json:"worker_seconds"`
	ProviderSeconds float64       `json:"provider_seconds"`
	ProviderStarted bool          `json:"provider_started"`
	CostStatus      string        `json:"cost_status"`
	ActualCostUSD   *float64      `json:"actual_cost_usd"`
	Usage           ProviderUsage `json:"usage"`
}

type usageKey struct{}
type usageRecorder struct {
	usage ProviderUsage
	seen  bool
	// partial holds a trailing fragment that could still become the reserved
	// marker once the rest of the line arrives.
	partial string
}

func newUsageRecorder(provider string) *usageRecorder {
	source := "unknown"
	if provider == "codex-bosun" {
		source = "codex"
	}
	if provider == "claude-bosun" {
		source = "claude"
	}
	return &usageRecorder{usage: ProviderUsage{Source: source, Status: "missing"}}
}

// Strip the adapter's reserved control lines before diagnostics or review
// publication. Replayed samples replace nothing: the adapter emits one final
// aggregate and bridge sequence deduplication handles reattachment. Never sum
// aggregates.
//
// bridgectl's stream_json transport is documented to forward stdout a line at a
// time, but nothing enforces that, and the bridge appends a newline to any
// argument-provider payload lacking one -- which makes a split permanent. So a
// trailing fragment that could still become the marker waits for the rest of
// the line instead of being judged on its own. Ordinary text never waits.
func (r *usageRecorder) filter(raw string) string { return r.scan(raw, false) }

// filterFinal reads a complete, already-terminated output. Nothing more is
// coming, so no fragment is held back -- there is no later flush to release it.
func (r *usageRecorder) filterFinal(raw string) string { return r.scan(raw, true) }

// flushPartial ends the stream: it judges any held fragment now that nothing
// more is coming. A fragment that turned out to be a complete record is
// recorded, not emitted -- an unterminated final line must not become review
// prose. Anything else was review text all along and is returned.
func (r *usageRecorder) flushPartial() string {
	if r == nil || r.partial == "" {
		return ""
	}
	held := r.partial
	r.partial = ""
	return r.scan(held, true)
}

// maybeMarkerStart reports whether s is a strict prefix of the reserved marker,
// so it may still grow into one once more of the line arrives.
func maybeMarkerStart(s string) bool { return s != "" && strings.HasPrefix(usagePrefix, s) }

// consumeMarker reads one usage record from a line already known to carry the
// reserved prefix. It records a valid aggregate and returns any bytes that
// followed the record, so output coalesced onto the same payload survives.
// incomplete distinguishes a record truncated mid-transport, which more input
// may finish, from a malformed one, which never will.
func (r *usageRecorder) consumeMarker(line string) (rest string, parsed, incomplete bool) {
	body := strings.TrimPrefix(line, usagePrefix)
	decoder := json.NewDecoder(strings.NewReader(body))
	var u ProviderUsage
	if err := decoder.Decode(&u); err != nil {
		return "", false, errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
	}
	if !r.seen && u.Source == r.usage.Source && validUsage(&u) {
		r.usage, r.seen = u, true
	}
	return body[decoder.InputOffset():], true, false
}

func (r *usageRecorder) scan(raw string, final bool) string {
	if r == nil || r.usage.Source == "unknown" {
		return raw
	}
	raw, r.partial = r.partial+raw, ""
	var output strings.Builder
	for _, line := range strings.SplitAfter(raw, "\n") {
		if line == "" {
			continue
		}
		body := strings.TrimSuffix(line, "\n")
		terminator := line[len(body):]
		// Oversized input is never buffered, so a fragment cannot grow without
		// bound while it waits for a terminator that may never come.
		holdable := !final && terminator == "" && len(line) <= 64*1024
		if strings.HasPrefix(body, usagePrefix) {
			if len(line) > 64*1024 {
				continue
			}
			rest, parsed, incomplete := r.consumeMarker(body)
			if parsed {
				// A complete record is consumed on sight, whatever followed it
				// on the same payload is review text.
				if rest != "" {
					output.WriteString(rest + terminator)
				}
				continue
			}
			if incomplete && holdable {
				r.partial = line
				break
			}
			continue
		}
		if holdable && maybeMarkerStart(body) {
			r.partial = line
			break
		}
		output.WriteString(line)
	}
	return output.String()
}

var usageModelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,127}$`)

func validTokenUsage(u *TokenUsage) bool {
	if u.CostBasis != "" && u.CostBasis != "list" && u.CostBasis != "managed" {
		u.CostBasis = "unknown"
	}
	if u.CostBasis == "unknown" {
		u.ProviderReportedCostUSD = nil
	}
	for _, n := range []*int64{u.InputTokens, u.CachedInputTokens, u.CacheWriteInputTokens, u.CacheWrite30mTokens, u.CacheWrite12hTokens, u.OutputTokens, u.ReasoningOutputTokens, u.TotalTokens, u.Turns} {
		if n != nil && (*n < 0 || *n > 9007199254740991) {
			return false
		}
	}
	if u.ProviderReportedCostUSD != nil && (math.IsNaN(*u.ProviderReportedCostUSD) || math.IsInf(*u.ProviderReportedCostUSD, 0) || *u.ProviderReportedCostUSD < 0) {
		return false
	}
	if u.Model != nil && !usageModelPattern.MatchString(*u.Model) {
		u.Model = nil
	}
	if u.InputTokens != nil {
		var cache int64
		if u.CachedInputTokens != nil {
			cache += *u.CachedInputTokens
		}
		if u.CacheWriteInputTokens != nil {
			cache += *u.CacheWriteInputTokens
		}
		if cache > *u.InputTokens {
			return false
		}
	}
	if u.OutputTokens != nil && u.ReasoningOutputTokens != nil && *u.ReasoningOutputTokens > *u.OutputTokens {
		return false
	}
	if u.TotalTokens != nil && u.InputTokens != nil && u.OutputTokens != nil && *u.TotalTokens != *u.InputTokens+*u.OutputTokens {
		return false
	}
	if u.CacheWriteInputTokens != nil && u.CacheWrite30mTokens != nil && u.CacheWrite12hTokens != nil && *u.CacheWriteInputTokens != *u.CacheWrite30mTokens+*u.CacheWrite12hTokens {
		return false
	}
	return true
}

func validUsage(u *ProviderUsage) bool {
	if u.Status != "available" && u.Status != "partial" && u.Status != "missing" {
		return false
	}
	if !validTokenUsage(&u.TokenUsage) || len(u.ModelUsage) > 32 {
		return false
	}
	for i := range u.ModelUsage {
		if !validTokenUsage(&u.ModelUsage[i]) {
			return false
		}
		if u.ModelUsage[i].CostBasis == "unknown" {
			u.ProviderReportedCostUSD = nil
			u.CostBasis = "unknown"
		}
	}
	if u.Status == "available" && (u.InputTokens == nil || u.OutputTokens == nil) {
		u.Status = "partial"
	}
	return true
}

func sanitizedUsage(u ProviderUsage, sanitize func(string) string) ProviderUsage {
	clean := func(tokens *TokenUsage) {
		if tokens.Model != nil {
			model := sanitize(*tokens.Model)
			tokens.Model = &model
		}
	}
	clean(&u.TokenUsage)
	u.ModelUsage = append([]TokenUsage(nil), u.ModelUsage...)
	for i := range u.ModelUsage {
		clean(&u.ModelUsage[i])
	}
	return u
}

func usageFrom(ctx context.Context) *usageRecorder {
	r, _ := ctx.Value(usageKey{}).(*usageRecorder)
	return r
}

func reviewOutcome(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "failed"
	}
}

// These are credential hints, not an assertion about the account's billing
// contract or the provider CLI's effective authentication selection.
func credentialMode(provider string) string {
	var key, saved bool
	switch provider {
	case "codex-bosun":
		key = os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("CODEX_API_KEY") != ""
		saved = os.Getenv("CODEX_AUTH") != ""
	case "claude-bosun":
		key = os.Getenv("ANTHROPIC_API_KEY") != ""
		saved = os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "" || os.Getenv("CLAUDE_CREDENTIALS") != ""
	}
	if key && saved {
		return "mixed_credentials_present"
	}
	if key {
		return "api_key_present"
	}
	if saved {
		return "saved_credentials_present"
	}
	return "unknown"
}
