# Plan: Escalate review effort for large diffs

- **Status:** Implemented; PR pending
- **Branch:** `issue-105-effort-escalation`
- **Created:** 2026-11-13
- **Issue:** #105 (step 2 of #95). The threshold is recalibrated by #106.
- **Related ADRs:** none; builds on [plan-review-effort.md](plan-review-effort.md)

## Problem

#95 added `standard` and `deep`, but someone has to remember to ask for `deep`. A large change should get a deeper review automatically.

## Approach

A third setting, `auto`, becomes the default for `review.effort` and `--effort`. The reviewer resolves it once the checkout exists and before the provider starts. It sizes the change with the same `diffStats` the coverage block uses (added plus deleted reviewable lines from the merge base; lockfiles and generated files excluded). Above `review.escalateAboveLines` (env `BOSUN_REVIEW_ESCALATE_ABOVE_LINES`, default 800), it runs `deep`; otherwise `standard`. `0` disables escalation. An explicit `standard` or `deep` is never escalated. A change that cannot be sized runs `standard`. The usage summary, the coverage event, `review-result` and the hosted footer record `effort_escalation` (`reviewable_lines=1840 > 800`). `Config.ValidateReview` refuses an unknown effort or a negative threshold at controller start, at submission, and in the CLI before any snapshot.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/config/config.go` | `EffortAuto`, `ReviewEscalateAboveLines`, `ValidateReview` |
| `internal/jobs/jobs.go` | carry the threshold; validate |
| `internal/review/runner.go` | `resolveAutoEffort` before the provider starts |
| `internal/review/usage.go`, `coverage.go` | record and show the escalation |
| `internal/localreview/cli.go`, `result.go`; `cmd/bosun/main.go` | `--effort auto`, display, start-up validation |
| `charts/bosun/values.yaml`, `templates/deployment.yaml` | `effort: auto`, `escalateAboveLines` |
| `docs/local-development.md`, `docs/setup.md` | document escalation |

## Phases

- [x] Failing tests: config defaults and validation, Job env, worker escalation above, at and below the threshold, explicit override, unsizable change, footer, result display
- [x] Implementation
- [x] Docs
- [ ] Live escalated review on Kind
- [ ] Hosted Bosun review; merge

## Threshold

The issue asks for a threshold chosen from data. On 2026-11-13 the coverage footers published since 0.3.5 covered 10 bosun reviews, the largest 436 reviewable lines, all with "full diff read". That is not enough to calibrate, so 800 ships as a provisional default (the issue's own example) and #106's data check revisits it.

## Alternatives Rejected

- **Empty `review.effort` meaning auto, with `standard` as the chart default**: escalation would then be off for every chart user unless they cleared a value, so the feature would do nothing by default.
- **Escalating on changed-file count as well**: #93's 71-file, 665-line example suggests file count matters, but there is no data yet; lines alone match the issue and the footer.
- **Failing the review when sizing fails**: sizing is a heuristic; losing the review is worse than a standard one.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-13 | Plan created and implemented |
