# Upstream pull-request base

- Status: Review pending
- Branch: `issue-34-upstream-pr-base`
- Created: 2026-10-10
- Related ADRs: none

## Problem

The installed review skill can fetch `main` from a contributor fork and present that comparison as a PR review. A failed fetch can also fall through to an unverified ref.

## Approach

Resolve the PR's upstream repository URL and exact base OID through `gh`, fetch its named base branch, verify the fetched OID, and give Bosun the verified OID. Expose both resolved base and merge base in CLI submission output. Preserve offline checkout review.

## Files Affected

- `PRD.md`: define correct PR comparison.
- `internal/skills/assets/bosun-review/SKILL.md`: fail-closed upstream resolution.
- `internal/localreview/snapshot.go`, `cli.go`: record and report merge base.
- Tests in the corresponding packages: fork layout and output contracts.

## Phases

- [x] Add regression tests that fail on the current behavior.
- [x] Resolve and verify the upstream base; report comparison commits.
- [x] Run unit tests, full suite, coverage, and lint.
- [ ] Run two detached Bosun reviews, triage findings, and merge after CI.

## Verification

The fork-shaped test distinguishes the upstream commit from the fork's diverged `main`. CLI output tests show the selected base and merge base. `make test`, `make lint`, and `make build` pass. `make coverage` reports 36.4% overall, below the repository framework's 75% merge requirement; that gate remains open.

## Alternatives Rejected

- Trusting `origin/main`: `origin` can be a contributor fork.
- Merely checking that a base and head share an ancestor: a diverged fork `main` passes that check.

## Open Questions

None for this fix. Full PR-aware input belongs to #36.

## Change Log

| Date | Change |
| --- | --- |
| 2026-10-10 | Initial plan and PRD requirement. |
| 2026-10-10 | Verified the exact upstream SHA and added fail-closed fork tests; recorded the pre-existing coverage gap. |
| 2026-10-10 | First Bosun review found PR lookup error fallback; required explicit offline mode and added a failing-lookup regression test. |
