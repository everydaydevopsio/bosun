# Summary comment beside a findings review

Status: Implemented
Branch: feat/summary-comment-with-findings
Created: 2026-11-03
Related ADRs: None

## Problem
Bosun's terminal publication surface depends on the outcome. A review with no
findings posts an issue comment; a review with findings posts a pull-request
review with inline comments. Listing issue comments does not return reviews, so
the cheapest check for "did Bosun review this" — `gh pr view N --json comments`
— succeeds when there was nothing to act on and silently reports nothing when
there was. The answer is inverted exactly when it matters.

This is not hypothetical: on 2026-11-02 three published reviews on PR #69 were
read as no review at all, and the investigation only resolved by reading
reviewer Job logs on the cluster.

## Approach
After a findings review publishes, post one short summary comment carrying the
existing review marker and metadata line, the finding count, a severity
breakdown, and a pointer to the threads. Never duplicate finding detail — two
copies is two places to read and two places to drift.

The summary is not a terminal publication. The review is the artifact; a summary
that fails to post must not fail a review that already reached the pull request,
because that would report failure for visibly published findings and re-run the
review on retry, spending credits to reproduce what is already there.

## Files Affected
- internal/review/github.go: postSummary, finding count, severity breakdown.
- internal/review/review_publication_test.go: regressions; stub records bodies.
- PRD.md: GO-4 terminal-vs-pointer distinction; acceptance criterion 6.
- docs/github-app.md: the two surfaces and how to query both.

## Phases
- [x] Reproduce the gap as a failing test.
- [x] Post the summary on both the direct and 422-fallback review paths.
- [x] Keep summary failure non-fatal and logged.
- [x] Record the contract in the PRD and the operator docs.

## Verification
`env -u CODEX_HOME go test ./...`, `go vet ./...`, `make lint`, `gofmt`,
`git diff --check`. New tests failed first.

## Alternatives Rejected
Only document the two surfaces: leaves every consumer to get it right, and the
failure mode is silent.
Publish findings as a comment instead of a review: loses reply and resolve
machinery, which GO-4 exists to protect.
Repeat each finding in the summary: two places to read, two places to drift,
and a comment that competes with the threads it points at.

## Open Questions
None.

## Change Log
| Date | Change |
| --- | --- |
| 2026-11-03 | Post a summary comment beside a findings review. |
