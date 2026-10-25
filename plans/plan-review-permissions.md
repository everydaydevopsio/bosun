# Review command permissions

Status: Implemented
Branch: fix/skill-review-permissions
Created: 2026-10-25
Related ADRs: None

## Problem
Repeated command approvals interrupt review submission and polling.

## Approach
Use Claude skill-scoped grants and opt-in persistent Codex prefix rules.
Preserve edited rules and keep reset/credential commands outside the allowlist.
Use direct command calls so the host can match command prefixes.

## Files Affected
- internal/skills/: embedded skill, rules installer, regression tests.
- cmd/bosun/init.go: explicit permissions setup option.
- docs/local-development.md: setup and permission boundaries.

## Phases
- [x] Inspect skills and official permission documentation.
- [x] Implement scoped grants and optional persistent rules.
- [x] Add installer regression tests and documentation.
- [x] Record local validation limitations for CI.

## Verification
Targeted tests passed: `env -u CODEX_HOME /tmp/go/bin/go test ./internal/skills ./cmd/bosun`.
`git diff --check` passed. Independent Bosun review is blocked because Bosun
and a configured distinct reviewer are unavailable here.

## Alternatives Rejected
Disable approvals globally: grants unrelated commands too much access.
Skill prose alone: cannot grant Codex execution permissions.

## Open Questions
None.

## Change Log
| Date | Change |
| --- | --- |
| 2026-10-25 | Add review command permissions. |
