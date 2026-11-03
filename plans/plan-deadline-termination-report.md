# Plan: Report a reviewer killed by its Job deadline

- **Status**: In progress
- **Branch**: `issue-72-report-deadline-terminations`
- **Created**: 2026-11-03
- **Issue**: [#72](https://github.com/everydaydevopsio/bosun/issues/72)
- **Related**: [#62](https://github.com/everydaydevopsio/bosun/issues/62) (merged in #73), [#74](https://github.com/everydaydevopsio/bosun/issues/74)

## Problem

`internal/jobs/jobs.go` sets `activeDeadlineSeconds` to
`review.timeoutSeconds + 120`. When it fires, Kubernetes kills the pod. Like an
OOM kill, that is not something the reviewer can report: no deferred function
runs, so the pull request is told nothing.

#73 closed the same hole for `OOMKilled`. It deliberately left this case,
because distinguishing "the Job deadline fired" from "the reviewer failed and
already said so" needs a different signal and its own argument.

## Approach

The two are distinguishable, and that is what makes this safe to classify.

| Outcome | Reviewer published? | Kubernetes signal |
| --- | --- | --- |
| Reviewer's own timeout (`review.timeoutSeconds`) | Yes, from its deferred publication | Job `Failed`, reason `BackoffLimitExceeded` |
| Job deadline (`+120s` later) | No -- killed | Job `Failed`, reason `DeadlineExceeded` |
| Memory limit | No -- SIGKILL | container `state.terminated.reason: OOMKilled` |

The reviewer's own timeout fires 120 seconds before the Job's, so the deadline
only fires when the reviewer has already hung past reporting. A Job condition
of `DeadlineExceeded` therefore means, specifically, "the reviewer did not
finish unwinding", and never "the reviewer failed and said so".

`Sentinel.cause` was written in #73 so a second classification is a small
addition. It gains a Job-condition branch alongside the existing pod read.
`OOMKilled` keeps precedence: when a reviewer is killed for memory while the
deadline is also up, memory is the actionable cause.

The message names the timeout and points at `review.timeoutSeconds`, the way
the memory message points at `review.resources.limits.memory`.

### The double-publication case

A reviewer can publish its own timeout and *then* hang past the Job deadline.
The sentinel would see `DeadlineExceeded` and try to report. The guard from #73
already covers it: `PublishTermination` looks for a Bosun publication carrying
this commit and made after the review started, and the reviewer's own failure
comment is both. This plan adds a test pinning that, rather than new machinery.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/review/termination.go` | `DeadlineExceededCause`, naming `review.timeoutSeconds` |
| `internal/review/termination_test.go` | The cause, and the reviewer-already-published case |
| `internal/sentinel/sentinel.go` | Classify the Job condition; order it after `OOMKilled` |
| `internal/sentinel/sentinel_test.go` | Table cases per Job-condition reason |
| `docs/github-app.md` | Document it; drop it from "What this does not do yet" |

## Phases

- [x] Failing tests for the classification and its boundary with a graceful exit
- [x] `DeadlineExceededCause` and the Job-condition branch
- [x] Docs
- [x] Verification

## Verification

- `go test ./...`, `go vet`, `make lint`
- A table case per Job-condition reason, including `BackoffLimitExceeded`,
  which must stay unreported
- A case where the reviewer published its own timeout and the deadline fired
  afterwards, asserting exactly one publication

## Alternatives Rejected

| Option | Why not |
| --- | --- |
| Classify on the pod's termination reason, as the OOM path does | A deadline kill is a SIGTERM; the container reason is `Error`, indistinguishable from a reviewer that exited non-zero on its own |
| Report every failed Job and rely on the publish-once guard | The guard is a backstop, not a classifier: a reviewer that failed before obtaining a token never published, so there is nothing to suppress against, and it would be reported as a deadline kill it never hit |
| Shorten `activeDeadlineSeconds` toward the review timeout | Narrows the window in which the reviewer can publish its own, better-diagnosed failure |

## Open Questions

- None. Attempt-level correlation remains #74 and is unchanged by this.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-03 | Plan created from issue #72 |
| 2026-11-03 | Implemented; added a recover around per-Job inspection after a test showed client-go panics on an unparseable selector |
