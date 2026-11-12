# Plan: Review effort setting

- **Status:** Implemented; PR pending
- **Branch:** `issue-95-review-effort`
- **Created:** 2026-11-12
- **Issue:** #95 (step 1). Step 2 is #105 (automatic escalation); step 3 is #106 (chunking decision).
- **Related ADRs:** none

## Problem

Bosun set no model or reasoning effort, so every review ran at the provider's default, whatever the size or risk of the change, and nobody could pay for a deeper review.

## Approach

A single setting with two levels, `standard` and `deep`, set by chart value `review.effort` (env `BOSUN_REVIEW_EFFORT`) or `bosun review --effort`. `config.NormalizeEffort` validates it at submission (`jobs.build`) and again in the worker. An unknown level is an error, never a silent fallback. The worker exports the normalized value, and the adapters, which inherit the environment through bridgectl, map `deep` to Codex `-c model_reasoning_effort=high` and Claude `--effort high`. `standard` passes nothing, so today's behaviour is unchanged. The usage summary records `effort`, and `review-result` shows it.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/config/config.go` | `ReviewEffort`, levels, `NormalizeEffort` |
| `internal/jobs/jobs.go` | validate; `BOSUN_REVIEW_EFFORT` on the Job |
| `internal/review/runner.go`, `usage.go` | validate in the worker; record `effort` |
| `internal/localreview/cli.go`, `result.go` | `--effort`; show the effort that ran |
| `scripts/codex-review-adapter.js`, `scripts/claude-review-adapter.js` | provider flags |
| `charts/bosun/values.yaml`, `templates/deployment.yaml` | `review.effort` |
| `docs/local-development.md`, `docs/setup.md` | document the setting |

## Phases

- [x] Confirm each CLI's control in the image: Claude Code 2.1.283 `--effort low|medium|high|xhigh|max`; Codex 0.157.1 `-c model_reasoning_effort=…` (echoed as "reasoning effort: high")
- [x] Failing tests, then implementation
- [x] Docs
- [x] Live deep review per provider on Kind: Codex (2m, 426k in) and Claude (1m42s) both completed with `effort: deep` recorded
- [ ] Hosted Bosun review; merge

## Alternatives Rejected

- **Exposing each provider's raw levels (`xhigh`, `max`)**: ties the chart to one provider's vocabulary. Two portable levels are enough until #106's data says otherwise.
- **Falling back to standard on an unknown level**: a typo would silently buy a shallower review than was asked for.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-12 | Plan created and implemented |
| 2026-11-12 | Local Claude review: the controller now refuses an unknown `review.effort` at start (`validateServer`), and `bosun review` validates `--effort` before any snapshot |
