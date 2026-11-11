# Plan: Hosted reviews compare against the pull request's base

- **Status:** Implemented; PR pending
- **Branch:** `issue-96-hosted-pr-base`
- **Created:** 2026-11-12
- **Issue:** #96
- **Related ADRs:** none

## Problem

Hosted review Jobs never set `BOSUN_BASE_SHA`, so the reviewer and the #93 coverage sizing both compared against `origin/HEAD`. A pull request into a release branch was reviewed against main: every release-only commit appeared as part of the change, and the coverage footer listed files outside the PR as unexamined.

## Approach

Carry the pull request's `base.sha` on `review.Request.BaseSHA` from every entry point: the `pull_request` webhook payload, `GET /pulls/N` for comment triggers (controller `resolveIdentity`, and the worker's own `resolvePR`), and the pull-request listing for the sweep. `jobs.Submit` passes it as `BOSUN_BASE_SHA`. The hosted clone already has every branch; `fetchBase` fetches the commit by SHA if it is missing. If it still cannot be had, the worker logs a warning and falls back to the default branch instead of failing the review. The prompt and coverage take the base from the request.

Not changed: the publication metadata line. `termination.go` recognises earlier publications by exact match on it, and the review text already names its merge base.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/review/request.go` | `BaseSHA`, parsed from `pull_request.base.sha`; `validBase` |
| `internal/review/scan.go` | `OpenPullRequest.BaseSHA` from the listing |
| `internal/sweep/sweep.go` | pass the base to the request |
| `internal/review/github.go` | `resolvePR`/`ResolvePullRequest` return the base |
| `cmd/bosun/main.go` | comment-trigger resolution sets the base |
| `internal/jobs/jobs.go` | `BOSUN_BASE_SHA` on hosted Jobs |
| `internal/review/clone.go`, `runner.go` | fetch the base; fall back when unavailable; prompt and coverage use it |
| `docs/github-app.md`, `docs/local-development.md` | document the comparison base |

## Phases

- [x] Failing tests per entry point plus an end-to-end hosted test with a release branch
- [x] Implementation
- [x] Docs
- [ ] Bosun review, CI, merge

## Verification

- `go test ./...`: a pull request into a release branch that `main` has moved past is reviewed and sized against the release branch (1 changed file, not the release-only commit); an unfetchable base falls back without failing.

## Alternatives Rejected

- **Failing the review when the base cannot be fetched**: loses a review that the default branch still covers adequately in most cases.
- **Adding the base to the metadata line**: breaks the sentinel's exact-match lookup of earlier publications.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-12 | Plan created and implemented |
