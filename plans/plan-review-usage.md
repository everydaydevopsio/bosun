# Review usage measurement

Status: Implemented; live review pending  
Branch: `feat/review-usage-metrics`  
Created: 2026-10-27  
Related ADRs: none

## Problem

Bosun retains review text but discards provider token usage. Duration alone cannot
compare model costs with Copilot's AI credits and Actions minutes. Missing usage,
failed attempts, subscription entitlements, and caching can distort that comparison.

## Approach

Filter provider JSON locally into a bounded numeric usage envelope and the final
review. The worker emits one structured usage summary on every normal exit,
including failures. Persist the same summary through the existing local event
protocol and expose it in `review-result --json`. Keep unavailable numbers null.
Record provider cost estimates separately from actual invoiced cost. Publish a
dated comparison guide; do not infer exact Codex prices from incomplete CLI totals.

## Files affected

- `scripts/*review-adapter*.js`, `scripts/provider-usage.js`: extract usage without
  forwarding raw provider transcripts; exercise malformed and partial input.
- `config/bridge-bosun.yaml`, `Dockerfile`: run and package the adapters.
- `internal/review/`: collect usage, sanitize it, log outcomes and timing.
- `internal/jobs/`: supply stable run/pod identity and JSON logging.
- `internal/localreview/`: retain usage after logs expire and return it as JSON.
- `docs/cost-comparison.md`, `docs/README.md`: billing sources and measurement workflow.

## Phases

- [x] Inspect provider output and verify current primary pricing sources.
- [x] Add adapters, worker summaries, and durable local usage.
- [x] Verify failure handling, privacy, provider contracts, and regression suite.
- [x] Complete documentation and prepare the PR.

## Verification

Regression tests must fail before implementation. Run Node adapter tests, Go
tests with coverage, formatting, vet, and a CLI build. Real model billing and
image integration require the configured providers and container runtime.
The repository's separate Bosun review must use a distinct provider before merge.

Passed: `env -u CODEX_HOME /tmp/go/bin/go test ./... -cover -count=1`,
24 adapter cases across the two Node test files, `go vet ./...`, a CLI build,
an actual CLI JSON-log smoke check, and `git diff --check`.
`CODEX_HOME` is unset for tests because inherited agent configuration breaks the
existing installer-isolation tests. The container runtime, Kubernetes tooling,
and distinct reviewer are unavailable here; keep the PR draft until live validation.

## Alternatives rejected

- Parse prose token counts: unstable and can confuse answer text with usage.
- Log raw JSONL: exposes prompts, source, tool output, and possibly credentials.
- Hard-code an exact USD estimate: CLI totals omit pricing dimensions and may
  represent subscription usage. Published list rates belong in a dated guide.

## Open questions

Live benchmark results will determine which model offers acceptable review quality
and whether savings survive infrastructure allocation and current entitlements.

## Change log

| Date | Change |
| --- | --- |
| 2026-10-27 | Capture measured usage first; separate list-price comparison from actual billing. |
