# Publish every review on one surface

Status: Implemented
Branch: clean-review-as-pr-review
Created: 2026-11-09
Related ADRs: None
Supersedes: [plan-review-summary-comment.md](plan-review-summary-comment.md)

## Problem
A clean review published as an issue comment and a findings review as a
pull-request review. To keep comment-only checks from missing findings, a
findings review also posted a pointer comment (plan-review-summary-comment).
On a pull request that reads as two Bosun messages for one review, the second
referring to the first (seen on everydaydevopsio/bilge-pump#89).

## Approach
Publish every review, clean or not, as a pull-request review with event
`COMMENT` anchored to the reviewed commit. A clean review has no threads, so it
skips the file lookup and the 422 fallback. Drop the pointer comment and its
helpers; with one surface it has nothing to fix. Never use `APPROVE`: an App
approval could satisfy branch protection. Failures stay issue comments.

The reply to the review POST is no longer read: the pointer comment needed its
URL, and nothing else does. That also removes the case where a truncated reply
after a 2xx made a published review look failed.

## Files Affected
- `internal/review/github.go` — `postReview` posts clean reviews as reviews;
  `postSummary`, `findingCount`, `severityBreakdown`, `publishedReview` removed.
- `internal/review/termination.go` — doc comment on the surfaces it reads.
- `internal/review/review_publication_test.go`, `publication_test.go`,
  `internal/server/workflow_test.go` — tests for the new surface.
- `PRD.md` GO-4 and acceptance criterion 6; `docs/github-app.md`; `docs/setup.md`.

## Phases
- [x] Explore publication code, tests, and consumers
- [x] Failing tests: clean review publishes as a review; no comment beside any review
- [x] Implement and update dependent tests
- [x] PRD and docs

## Verification
- `go test ./...`, `make lint`, `make test`
- Live: after the release is deployed, a clean Bosun review on a pull request
  appears under `gh pr view N --json reviews` and no Bosun issue comment follows.

## Alternatives Rejected
- Keep the pointer and also post clean reviews as reviews: every review becomes
  two messages, including clean ones.
- Leave as is: the duplicated message that prompted this.

## Open Questions
- None.

## Change Log
| Date | Change |
| --- | --- |
| 2026-11-09 | Created and implemented. |
