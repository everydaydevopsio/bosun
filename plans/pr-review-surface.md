# Publish Bosun findings as a real GitHub pull-request review

Status: proposed
Created: 2026-09-30
Observed branch: `codex/go-migration-foundation` (f43d54e)
Consumer: `everydaydevopsio/ballast` skill `github-pr-copilot-cycle`

## Problem

Ballast ships a skill, `github-pr-copilot-cycle`, that drives a bounded PR
review loop against GitHub Copilot. The goal is to make Bosun a drop-in
replacement so that repositories never need Copilot as a reviewer.

Bosun's GitHub App authentication is already implemented and is not the
blocker. `internal/review/github.go` mints a short-lived, repository-scoped
installation token per review (`token`, `installationToken`), requesting only
`contents: read` and `pull_requests: write`, with `GITHUB_TOKEN` fallback.
That is exactly what the skill needs, and `pull_requests: write` is already
sufficient for the Reviews API — no permission change is required.

The blocker is the **publishing surface**. Despite its name, `postReview`
posts to `/repos/{repo}/issues/{n}/comments`:

```go
return g.request(ctx, "POST", fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number),
    token, map[string]string{"body": "## Bosun code review\n\n" + string(text)}, nil)
```

That is a single conversation comment. It produces no review object, no review
threads, no thread IDs, and no path/line anchors — file and line survive only
as prose inside the Markdown, because `prompts/code-review.md` asks for
"findings in severity order ... including file and line/range".

The consuming skill is built almost entirely on the thread model: a GraphQL
`reviewThreads` query, `addPullRequestReviewThreadReply`,
`resolveReviewThread`, per-thread 0-3 scoring, and "resolve only after the fix
is pushed". Against Bosun as it stands, none of that has a counterpart, so the
agent has nowhere to record which findings it handled and a fresh session
cannot reconstruct the state. Three further gaps compound it:

1. **No commit correlation.** The published comment carries no ref or SHA, so a
   consumer polling for "the review of the commit I just pushed" can only use
   timestamps. Two pushes in quick succession are genuinely ambiguous.
2. **Silent failures.** The Python implementation posted
   `Bosun review failed: ...` to the PR. The Go rewrite dropped it —
   `Worker.Run` returns the error and publishes nothing. An automated consumer
   polls until timeout with no signal, and cannot distinguish "review failed"
   from "review still running" from "webhook never arrived".
3. **Capacity rejections are invisible on the PR.** At
   `BOSUN_MAX_CONCURRENT_REVIEWS` the webhook returns 503, GitHub marks the
   delivery failed, and no review is ever produced. Same symptom as (2) from
   the consumer's side.

## Approach

Make Bosun publish a real pull-request review with inline comments, and make
every terminal outcome visible on the PR. Once findings are review threads, the
existing Ballast skill mechanics work unchanged and the only skill-side edit is
the author-login matcher.

Keep the conversation-comment path as a fallback rather than deleting it: it is
the correct output when a finding cannot be anchored to the diff, and it keeps
the blast radius small if the Reviews API rejects a payload.

### Output contract

This section is the interface Ballast codes against. Treat changes to it as
breaking.

**Success — a review.** `POST /repos/{repo}/pulls/{n}/reviews` with
`event: "COMMENT"`:

- `body` starts with the marker line, then the summary:

  ```markdown
  ## Bosun code review

  <sub>`<ref>` @ `<sha>` · provider `<provider>`</sub>

  <summary paragraph>
  ```

- `comments[]` carries one entry per anchorable finding, each
  `{path, line, side: "RIGHT", body}`, where `body` opens with a severity tag:
  `**blocker** — <title>`, then impact and concrete remediation.
- Findings that cannot be anchored to the diff are appended to `body` under a
  `### Findings outside the diff` heading, preserving `path:line` in the text.

**Failure — a comment.** On any terminal error with a PR number and a usable
token, publish a conversation comment:

```markdown
## Bosun code review failed

<sub>`<ref>` @ `<sha>`</sub>

`<redacted reason>`
```

The reason must come from the already-redacted `safeError` text, never the raw
error. A consumer treats this as infrastructure failure, not as findings.

**Invariants.**

- Marker lines `## Bosun code review` and `## Bosun code review failed` are
  stable and are how a consumer identifies Bosun output.
- The `<sub>` metadata line always carries the reviewed ref and resolved SHA, so
  a consumer can correlate output to `$HEAD_OID`.
- Exactly one terminal publication per review run: a review, or a failure
  comment. Never both, never neither.

## Files affected

| Path | Reason |
| --- | --- |
| `prompts/code-review.md` | Require a machine-readable findings block alongside the Markdown summary |
| `internal/review/findings.go` *(new)* | Parse, validate, and normalize the findings block; tolerate its absence |
| `internal/review/findings_test.go` *(new)* | Parser table tests: well-formed, absent, malformed, out-of-range line, oversized |
| `internal/review/github.go` | `postReview` → Reviews API; add `postComment`, `prFiles`, diff-anchor filtering; accept ref/SHA |
| `internal/review/github_test.go` | Review payload shape, 422 fallback, failure comment, metadata line |
| `internal/review/runner.go` | Pass ref/SHA/provider to the publisher; publish failure comment on terminal error |
| `internal/review/runner_test.go` or `internal/server/workflow_test.go` | End-to-end: agent output → published review against the httptest GitHub stub |
| `docs/github-app.md` | Remove the stale "What this does not do yet" paragraph; document the review surface |
| `docs/setup.md`, `README.md` | Update "Current scope"; document the output contract |
| `PRD.md` | Add requirements for the review surface and failure visibility |

## Phases

### Phase 0 — Register the GitHub App (ops, blocks end-to-end validation)

- [ ] Create the `Bosun Reviewer` App per `docs/github-app.md`
- [ ] Permissions: Contents read, Pull requests read & write, Metadata read
- [ ] Subscribe: Create, Pull request, Issue comment, Pull request review comment
- [ ] Install on selected repositories only; record the App ID and bot login
      (`<app-slug>[bot]`) — Ballast needs the exact login to match authorship
- [ ] Deploy the webhook endpoint over TLS and confirm a delivery reaches
      `/webhooks/github`

No Bosun App is installed on the `everydaydevopsio` org today — as of
2026-09-30 the org has only `codecov`, `chatgpt-codex-connector`, and
`everydaydevops-argocd`. Phases 1-3 can be developed and unit-tested without
this; only the real-PR smoke test depends on it.

### Phase 1 — Commit correlation (small, independently useful)

- [ ] Change `postReview` to take the reviewed ref and SHA and emit the
      `<sub>` metadata line
- [ ] Thread `req.Ref` / `req.SHA` from the `runner.go` call site — both are
      already resolved there, after `cloneRepository` returns the concrete SHA
- [ ] Test: published body contains the resolved SHA, not the empty-string or
      `pr-N` placeholder form

### Phase 2 — Structured findings

- [ ] Extend `prompts/code-review.md`: keep the Markdown summary, and require a
      trailing fenced ` ```json ` block, `{"findings": [...]}`, each finding
      `{path, line, end_line?, severity, title, body, remediation}` with
      severity in `blocker|high|medium|low`
- [ ] Add `internal/review/findings.go`: extract the last fenced JSON block,
      unmarshal, validate, drop invalid entries with a logged reason, and
      return the summary with the block stripped
- [ ] Absence of the block is **not** an error: return zero findings and the
      full output as summary, so Phase 3 falls back to a conversation comment
- [ ] Bound the parse: cap finding count and per-field length before anything
      reaches the GitHub API

### Phase 3 — Publish a review

- [ ] Add `postReview` against `POST /repos/{repo}/pulls/{n}/reviews`,
      `event: "COMMENT"`, body + `comments[]`
- [ ] Add `prFiles` (`GET /repos/{repo}/pulls/{n}/files`) and filter findings to
      paths present in the diff before the first attempt
- [ ] On HTTP 422, retry once with `comments: []` and every finding folded into
      the body. GitHub rejects the *entire* review if any single comment
      position is invalid, so an unconditional fallback is required — this is
      the most likely production failure and must not lose the review
- [ ] Zero findings, or no parsable block: publish a conversation comment as
      today, preserving the existing 60000-rune truncation
- [ ] Keep output redaction on every path (`redact(output, token)` already runs
      before publication; do not bypass it when building comment bodies)

### Phase 4 — Failure visibility

- [ ] On terminal error with `PRNumber > 0` and a usable token, publish the
      failure comment from the redacted `safeError` text
- [ ] Do not let a failed publish mask the original error; log and return the
      review error
- [ ] Test: provider failure produces exactly one failure comment, containing
      no credential material (extend the existing
      `TestGitHubFailureDoesNotEchoResponseSecrets` pattern)
- [ ] Decide whether capacity rejection (503 at
      `BOSUN_MAX_CONCURRENT_REVIEWS`) should also surface on the PR. It is
      raised in the controller, before any Job exists, so it is a separate code
      path from the reviewer — see Open Questions

### Phase 5 — Docs

- [ ] `docs/github-app.md`: delete "What this does not do yet"; state that
      reviews are published as PR reviews with inline comments
- [ ] `README.md`: rewrite the "Current scope" paragraph
- [ ] `docs/setup.md`: document the output contract and the bot login
- [ ] `PRD.md`: add the review-surface and failure-visibility requirements with
      acceptance criteria, per this repo's existing PRD convention

## Verification

- `make test` (`go test ./...`), extended with the new parser, payload, 422
  fallback, and failure-comment cases
- `go test ./... -race` for the publishing paths
- `make lint` (gofmt, shellcheck, helm lint)
- `make build`
- `make review REPO=~/src/<repo>` still prints to the terminal and publishes
  nothing — the local path skips GitHub entirely, so it does **not** cover this
  work. GitHub publishing is covered only by the `httptest` stubs plus the
  Phase 0 smoke test
- Smoke test once the App exists: open a PR with a deliberate defect, confirm a
  review appears with inline comments anchored to the right lines, the metadata
  line carries the pushed SHA, and threads are resolvable
- Negative smoke test: force a provider failure and confirm exactly one failure
  comment appears

## Alternatives rejected

**Leave Bosun posting a conversation comment; teach the Ballast skill to parse
it.** Workable — the skill already has a body-parsing path for Copilot's
`Suppressed comments` — but it loses all GitHub-side state. Nothing to resolve
means the finding-to-fix ledger lives only in the agent's context, so a fresh
session cannot tell which findings were handled, and a bounded three-cycle loop
cannot prove it converged. It also pushes brittle Markdown parsing into every
consumer rather than solving it once at the source.

**Publish through the Checks API instead.** Attractive, and the reserved
`checks` permission in `docs/github-app.md` anticipates it. Rejected for now
because it does not produce resolvable review threads, which is the specific
thing the consuming skill needs. It remains the right answer for branch-only
reviews, which currently have no GitHub surface at all — note that
`installationToken` would need `checks: write` added to its requested
permissions, which is a code change, not just an App setting.

**Have the reviewing agent call the GitHub API itself.** Rejected on security
grounds, and it contradicts the existing design: `agentEnv` and `SECRET_ENV`
deliberately strip GitHub credentials before the provider runs, because the
provider executes untrusted repository content.

## Open questions

1. **Severity-to-action mapping.** Bosun emits `blocker|high|medium|low`; the
   Ballast skill scores `0 - no action` through `3 - human input required`. Does
   Bosun map it, or does the skill? Recommendation: Bosun emits severity only
   and the skill owns the policy mapping, so review policy stays in the
   consuming repository.
2. **Capacity rejection visibility.** Surfacing the 503 on the PR requires the
   controller to post, which means the controller needs a token-minting path it
   does not currently have. Possibly out of scope; at minimum the consumer must
   be told that "no review arrived" is a real and expected outcome.
3. **Review churn.** Every push fires `pull_request.synchronize`, so a
   three-cycle loop produces at least three reviews. Acceptable, or should
   Bosun dismiss its own prior review when publishing a new one?
4. **Fork PRs.** `cloneRepository` already handles fork heads via
   `refs/pull/N/head`. Confirm that an installation token scoped to the base
   repository can post review comments on a fork-sourced PR.

## Downstream: what Ballast changes once this lands

Not work for this repo — recorded so the interface is agreed before either side
builds against it.

- Author matching switches from the Copilot logins to `<app-slug>[bot]`
- Triggering switches from `gh pr edit --add-reviewer "@copilot"` to
  `gh pr comment --body '@bridgectl review'`, and mostly disappears: Bosun
  auto-reviews on `opened`, `reopened`, `synchronize`, and `ready_for_review`,
  so every push in the fix step already fires the next review
- The skill must run as a human. `FromWebhook` drops any delivery where
  `sender.type == "Bot"`, and gates on `author_association` in
  `OWNER,MEMBER,COLLABORATOR`. A local `gh` session qualifies; an Actions
  `GITHUB_TOKEN` is silently ignored
- Settle timeout rises from ~10 minutes to ~35, covering
  `BOSUN_REVIEW_TIMEOUT_SECONDS` (default 1800) plus queue time
- The skill gains two new terminal states: failure comment, and no-review-arrived

## Change log

| Date | Change |
| --- | --- |
| 2026-09-30 | Initial plan |
