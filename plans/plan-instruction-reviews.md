# Plan: Review agent instructions as executable procedures

- **Status:** Implemented; PR pending
- **Branch:** `issue-110-instruction-reviews`
- **Created:** 2026-11-13
- **Issue:** #110
- **Related ADRs:** none; extends [plan-effort-escalation.md](plan-effort-escalation.md)

## Problem

A 0.3.5 review of a PR adding a ~200-line agent skill found one high but missed the same kind of defect elsewhere in the skill: an unhandled fallback path and a hard-coded `origin/master` in a repo whose base is `main`. The prompt had no lens for instruction files. Its "material" wording let the reviewer drop low findings without saying so. And a small, dense diff never triggers size-based escalation.

## Approach

- **Prompt (`prompts/code-review.md`).**
  - A new kind-of-change section treats agent instructions, skills, runbooks, prompts and CI workflows as programs, with five checks:
    - step order and the artifacts each step uses
    - both paths of every fallback
    - consistency with sibling instruction files and repo-wide rules
    - hard-coded values
    - tools the environment may lack
  - "Report every finding at `low` or above" replaces the implicit "material" bar.
  - "A first finding does not end the review."
  - A clean review says "No findings."
- **Escalation.** An `auto` review also runs `deep` when instruction files make up at least 50 changed lines and more than half the reviewable lines. The escalation reason is recorded as `instruction_lines=N of M`. `escalateAboveLines: 0` disables this trigger too.
- **Surfaces.**
  - The check-run title on a clean review becomes "No findings".
  - The skill moves to v6 and tells callers to expect low findings and how instruction changes are reviewed.

## Files Affected

| Path | Reason |
| --- | --- |
| `prompts/code-review.md` | instruction lens, explicit severities, keep going |
| `internal/review/instructions.go` | `isInstructionFile`, `instructionHeavy` |
| `internal/review/runner.go` | second escalation trigger |
| `internal/review/checkrun.go` | clean title |
| `internal/skills/assets/bosun-review/SKILL.md`, `internal/skills/skills.go` | skill v6 |
| `docs/local-development.md`, `docs/setup.md` | document the trigger |

## Phases

- [x] Failing tests: prompt contract, instruction path matching, escalation by share and minimum
- [x] Implementation, docs, skill v6
- [ ] Live comparison on a fixture reproducing the issue (0.4.0 prompt vs this prompt, standard effort). Blocked on 2026-11-13: the host disk was full and OrbStack stopped.
- [ ] Hosted Bosun review; merge

## Alternatives Rejected

- **A cap on low findings**: the issue allows one if noise becomes a problem, but there is no evidence of noise yet. Severity ordering already puts lows last.
- **Escalating on any instruction-file change**: a one-line Dependabot action bump in `.github/workflows/` would pay for a deep review. The 50-line minimum and majority share avoid that.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-13 | Plan created and implemented |
