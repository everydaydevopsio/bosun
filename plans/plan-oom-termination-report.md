# Plan: Report reviewer terminations the reviewer cannot report

- **Status**: In progress
- **Branch**: `issue-62-report-oom-terminations`
- **Created**: 2026-11-03
- **Issue**: [#62](https://github.com/everydaydevopsio/bosun/issues/62)
- **Related ADRs**: none yet

## Problem

A reviewer Job that exceeds `review.resources.limits.memory` is OOM-killed.
OOM kill is SIGKILL, so the deferred `postFailure` in
`internal/review/runner.go` never runs. The Job goes to `Failed`, its
credential Secret is garbage-collected with it, and after
`review.ttlSecondsAfterFinished` the Job and its logs are gone. From the pull
request's side the review simply never happened.

This breaks the invariant stated in `docs/github-app.md`: "Every hosted review
with a pull-request number ends in exactly one terminal publication: a review,
or a failure comment." An OOM-killed review ends in neither.

#60 made this reachable by configuration rather than by luck: reviewer Jobs now
carry a memory limit whose `1Gi` default was derived from a single measured
review (223Mi peak on a 14-file diff).

## Approach

The reviewer cannot report its own SIGKILL, so the controller reports it.

Add a **sentinel**: a reconcile loop in the long-lived `bosun serve` process
that periodically lists reviewer Jobs, finds the ones that failed for a reason
the reviewer provably could not report, and publishes the failure comment on
the reviewer's behalf.

The issue weighed three mechanisms. `preStop` is rejected upstream — it runs on
graceful termination, not on OOM kill. Publishing a placeholder up front and
editing it trades the absence for a stale "in progress" comment and gives up
the one-publication property. The controller observing the Job is the remaining
option, and its stated cost — "the controller needs a token for the repository
after the Job's own credential is gone" — turns out not to be a new cost: the
controller already holds the App private key and already mints a repository
token per review in `SubmitAuthenticated`. Minting one more on demand keeps the
existing property that the controller holds nothing *per-review*.

### Detection

A `Failed` Job alone is not enough: a reviewer that fails gracefully also exits
non-zero, having already published its own failure comment. Acting on that
would double-publish. The sentinel therefore acts only on terminations that
carry a Kubernetes-visible reason the reviewer could not have survived —
`OOMKilled` on a container status. A graceful failure terminates with reason
`Error` and is left alone.

### Keeping the one-publication invariant

Three guards, because a single one is not enough:

1. **Remote check.** Before posting, list the PR's comments and reviews and
   look for a Bosun marker carrying this commit's metadata line. This covers
   the case the annotation cannot: a reviewer that published its review and was
   then OOM-killed while finishing up. It also makes the sentinel safe across a
   controller restart that loses in-memory state.
2. **Durable annotation.** After publishing, annotate the Job
   `bosun/termination-reported`, so a later reconcile skips it without another
   GitHub round trip.
3. **Admission lease.** The publish step runs under the namespace's existing
   `bosun-review-admission` Lease, so two controller replicas cannot both pass
   the remote check and post.

### Message

The comment names memory specifically and points at
`review.resources.limits.memory`, including the configured value. Per the
issue: "The review provider failed" sends an operator looking at the agent when
the fix is a chart value.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/sentinel/sentinel.go` | New: the reconcile loop, OOM classification, publish-once guards |
| `internal/sentinel/sentinel_test.go` | New: table-driven tests over a fake clientset |
| `internal/review/termination.go` | New: exported publisher; existing-publication check by marker + commit |
| `internal/review/termination_test.go` | New: tests over an httptest GitHub |
| `internal/review/github.go` | Expose what the publisher needs from the marker/metadata helpers |
| `internal/jobs/admission.go` | Let the lease helper take an operation timeout; GitHub calls exceed the 5s that submission uses |
| `cmd/bosun/main.go` | Start the sentinel alongside the webhook server in `serve` |
| `charts/bosun/templates/rbac.yaml` | `pods: get, list` — reading a container's termination reason |
| `charts/bosun/templates/deployment.yaml` | Optional `GITHUB_TOKEN` so PAT installs can publish too |
| `charts/bosun/values.yaml` | Sentinel interval, and the opt-out |
| `internal/config/config.go` | Carry the sentinel interval and the configured memory limit |
| `docs/github-app.md` | Document the behaviour; keep the invariant statement true |

## Phases

- [x] Explore: confirm the controller's credentials, RBAC, and the Job metadata the sentinel needs
- [x] Failing tests for OOM classification and the publish-once guards
- [x] `internal/review` termination publisher
- [x] `internal/sentinel` reconcile loop
- [x] Wire into `bosun serve`; chart RBAC, values, and deployment env
- [x] Docs; plan graduates to an ADR on merge

## Verification

- `go test ./...` with the new table-driven cases
- `make lint` (shell + Helm chart)
- `helm template` renders the added RBAC and env without error
- Manual: set `review.resources.limits.memory` low enough to force an OOM on a
  real review and confirm exactly one comment lands

## Alternatives Rejected

| Option | Why not |
| --- | --- |
| `preStop` lifecycle hook | Does not run on OOM kill, only on graceful termination |
| Placeholder comment edited on completion | Gives up the one-publication property; leaves a stale "in progress" comment when the controller also dies |
| Reviewer self-reports via a memory watchdog | A process cannot reliably outrun its own SIGKILL, and a watchdog that polls its own RSS duplicates what the kubelet already knows |
| Informer instead of periodic list | More machinery for no benefit: reviews run for minutes and the Job TTL is an hour, so a 30s reconcile is timely, and a list is far easier to test |

## Open Questions

- Should `DeadlineExceeded` (the Job's `activeDeadlineSeconds` firing 120s after
  the review's own timeout) also be reported? Same mechanism, same invariant
  hole, but outside this issue's acceptance criteria. Classification is written
  so adding it is a one-line change; tracking separately.
- PAT installs: the controller has no GitHub credential today. Mounting the
  shared token is optional and documented, but an install that declines it gets
  logging instead of a comment.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-03 | Plan created from issue #62 |
| 2026-11-03 | Implemented; `activeDeadlineSeconds` promoted to #72 |
| 2026-11-03 | Bosun pass 1: resolve review identity at submission; anchor marker matching; make an exhausted search retryable |
| 2026-11-03 | Bosun pass 2: bound the publication search below by the review's start; separate lease acquisition from the operation budget |
