# Plan: Give branch-only reviews a home on the commit

- **Status**: Implemented; live verification pending
- **Branch**: `issue-65-branch-review-check-run`
- **Created**: 2026-11-04
- **Issue**: #65
- **Related ADRs**: none yet

## Problem

A review triggered by `branch-created` runs with `BOSUN_PR_NUMBER=0`, produces
full findings, and discards them. `internal/review/runner.go:265` publishes only
when `req.PRNumber > 0`; otherwise the output dies with the pod behind a
`Complete` Job status. Nothing warns, so the absence is invisible.

The obvious fix -- persist the review and replay it when a pull request opens --
has nowhere to live. The controller cannot read Secrets (`get`/`list` are
deliberately absent from `rbac.yaml`, asserted in CI), has no `pods/log` access,
and reviewer pods have no Kubernetes API access at all.

## Approach

Publish a branch-only review as a **check run on the commit**. A check run is
keyed by SHA rather than by pull request, so it needs no store: when a pull
request opens on that commit later, GitHub surfaces the existing check run on
it. `docs/github-app.md:30` already reserves the optional `Checks` permission
for exactly this -- "required only for the Checks output that gives branch-only
reviews a home".

Three ordered attempts when a review finishes with no pull request:

1. **Resolve late.** Ask GitHub whether a pull request now exists for the
   reviewed commit. A pull request opened while the review was running gets the
   normal inline review, unchanged.
2. **Check run.** Otherwise publish findings as a check run on the SHA, with
   each finding as an annotation anchored to its file and line.
3. **Warn.** Otherwise log at WARN naming repo, ref, SHA, and reason.

Installs that do not grant `Checks` keep today's behaviour and gain step 3.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/review/github.go` | read installation permissions; request `checks: write` only when granted; add the check-run publisher and the commit-to-pull-request lookup |
| `internal/review/runner.go` | branch the publish decision; emit the WARN |
| `internal/review/checkrun_test.go` | new behaviour |
| `internal/review/github_test.go` | permission scoping |
| `docs/github-app.md` | document the publication surface and the optional permission |

## Phases

- [x] Failing tests for the check-run publication and the WARN
- [x] Installation permission detection and conditional `checks: write`
- [x] Check-run publisher with findings as annotations
- [x] Late pull-request resolution before falling back
- [x] Docs

## Verification

`go test ./...`, `gofmt -l .`, `go vet ./...`. The acceptance criteria in #65,
with the note that criterion 1's "without a second provider call" is met by the
check run already being attached to the commit rather than by replaying a
stored review.

## Alternatives Rejected

- **Persist and replay (issue option 1).** Needs a new durable store, new
  controller RBAC, and a decision about letting reviewer pods write to the
  Kubernetes API. Larger, and weakens a deliberate security boundary.
- **Commit comments.** Attach to a SHA like a check run, but need
  `contents: write`, a materially broader grant than `Checks`.
- **Retire the `branch-created` trigger.** Removes the waste but gives up
  pre-pull-request feedback, and does not answer what #65 asks.

## Open Questions

- Whether a check run should ever conclude `failure` rather than `neutral`.
  Starting at `neutral` for findings and `success` for a clean review, so a
  review never blocks a merge by surprise.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-04 | Created; approach chosen over persist-and-replay after the storage paths were ruled out |
| 2026-11-04 | Implemented. Annotation truncation turned out unreachable -- `findingLimit` is already 50 -- so the test pins that invariant instead of the symptom. Killed branch reviews promoted to #76. |
