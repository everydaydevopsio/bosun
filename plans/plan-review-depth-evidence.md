# Plan: Review depth evidence (coverage and residual risks)

- **Status:** Implemented; PR pending
- **Branch:** `issue-93-review-depth-evidence`
- **Created:** 2026-11-11
- **Issue:** #93 (proposals 1, 2, 4). Proposal 3 (effort scaling) split to #95.
- **Related ADRs:** none

## Problem

A completed review with `findings: []` cannot show whether the change was
examined thoroughly or skimmed, so consumers fall back on elapsed time and token
counts. `turns` is no help: one `codex exec` run always reports one turn. A clean
review also has nowhere to say what reading the code cannot settle (build, test,
runtime, visual behaviour), which is the most useful output for an upgrade.

## Approach

1. **Residual risks** (reviewer-authored). The `BOSUN-FINDINGS` line gains
   `residual_risks: [{area, risk, verify}]`, required even when there are no
   findings. The prompt requires risks specific to the change and forbids the
   boilerplate "tests were not run" (the reviewer can never run them, #22). A
   change-type section tells the reviewer what to check in dependency, framework,
   runtime, and toolchain upgrades.
2. **Diff stats** (measured by the worker). After the provider finishes, the
   worker runs `git diff --numstat` from the merge base and counts lockfiles,
   vendored files, and `linguist-generated` files separately from reviewable
   content.
3. **Read evidence** (measured by the adapters). The adapters already see tool
   calls and throw them away. They now record the paths that were read, never
   content, and emit one `BOSUN_READS_V1` control line. The worker strips it the
   same way it strips `BOSUN_USAGE_V1`. Claude: the `Read` tool's `file_path` plus
   parsed `git` commands. Codex: parsed shell commands (`cat`, `sed`, `head`,
   `git show REV:path`, `git diff [-- path]`). Both give a lower bound.
4. **Coverage** joins 2 and 3: changed files opened, diffed by path, or not
   individually examined; whether a full-diff command ran; and how many context
   files outside the diff were opened. It is emitted as its own `coverage` event,
   so it is present even when the findings line is missing. It is stored on the
   local record, exposed by `review-result --json` and the text view, and
   appended as a one-line footer to hosted PR reviews and check runs.
5. **Skill**: triaging `findings: []` reads `coverage` and `residual_risks`, not
   elapsed time, tokens, or turns.

## Files Affected

| Path | Reason |
| --- | --- |
| `prompts/code-review.md` | residual risks, change-type guidance |
| `scripts/provider-reads.js` (+test) | read tracking and command parsing |
| `scripts/provider-usage.js` | neutralize an impersonated reads line in review text |
| `scripts/claude-review-adapter.js`, `scripts/codex-review-adapter.js` (+tests) | feed tool calls to the tracker, emit the reads line |
| `Dockerfile` | copy and test the new script |
| `internal/review/usage.go` | strip and record the reads control line |
| `internal/review/coverage.go` (+test) | diff stats, generated classification, coverage join |
| `internal/review/findings.go` | parse `residual_risks` |
| `internal/review/runner.go` | compute and emit coverage, hosted footer |
| `internal/localreview/{cli,monitor,result}.go` | store and render coverage and residual risks |
| `internal/skills/assets/bosun-review/SKILL.md` | empty-result triage guidance |
| `docs/local-development.md`, `docs/github-app.md` | document the fields |

## Phases

- [x] Explore: transport, result storage, prompt contract
- [x] Failing tests (JS tracker, Go parse, diff stats, recorder, coverage, result doc)
- [x] Core implementation
- [x] Docs, skill, Dockerfile
- [x] Verification: `make test`, `make lint`, `node --test`, image build

## Verification

- `go test ./...`, `node --test scripts/*.test.js`, `make lint`
- A local review against a real branch shows `coverage` and `residual_risks`
  in `bosun review-result --json`.

## Alternatives Rejected

- **Reviewer self-rated `thorough/partial/skim`**: the same model grading its own
  depth adds nothing a measured count does not, and it invites the same doubt.
- **Using `turns`/tokens as depth**: `turns` is always 1 for Codex, and output
  tokens measure report length.
- **Putting read paths in the usage record**: the usage record is a cost
  measurement logged per review; paths do not belong in cost logs.

## Open Questions

- Codex command parsing misses reads through tools it does not recognise
  (`rg -n pattern file`, `python -c`). Reported as a lower bound.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-11 | Plan created |
| 2026-11-11 | Self-review 1 (Codex): full diff needs a revision and a patch; `||` earns no credit; budget in UTF-8 bytes |
| 2026-11-11 | A host capture of Claude's tool calls showed it never uses Read and diffs by directory; directory pathspecs now cover files beneath them |
| 2026-11-11 | Self-review 2 (Claude): binary files excluded; fsmonitor disabled and filter drivers refused for working-tree sizing; hosted PR base filed as #96 |
| 2026-11-11 | Self-review 3 (Claude): `markerLine` now takes the last marker at a line start (a residual risk naming the marker hid the payload); full diff verified against the merge base; untracked symlinks not followed; `--includes` |
| 2026-11-11 | Self-review 4 (Codex): path diffs credited only through a verified range; endpoint forms (`A..HEAD`, `A HEAD`, `A`) need A to be the merge base; only commands a successful exit proves ran are credited. This run reported 0 unexamined changed files |
| 2026-11-11 | Hosted review of #97: content-filtering diff options earn no credit; a dirty working-tree review needs a diff against the working tree |
| 2026-11-11 | Hosted review of #97 @ a1c0445: a chain containing `||` credits only its first unit; `--cached`/`--staged` diffs earn no credit |
