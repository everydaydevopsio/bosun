# GitHub App authentication

A personal access token is the wrong credential for Bosun in production: it
cannot be narrowed per repository at run time, it never expires, and rotating it
breaks every repository at once. A GitHub App signs a short-lived JWT with its
private key, exchanges that for an **installation token scoped to the single
repository under review**, and that token expires after an hour.

Bosun mints one per review. When `BOSUN_GITHUB_APP_ID` and
`BOSUN_GITHUB_PRIVATE_KEY` are set it uses the App; otherwise it falls back to
`GITHUB_TOKEN`, so you can migrate without downtime.

## 1. Create the App

**Organization settings → Developer settings → GitHub Apps → New GitHub App**

| Field | Value |
| --- | --- |
| **GitHub App name** | `Bosun Reviewer` (must be globally unique) |
| **Homepage URL** | your repository URL |
| **Webhook → Active** | ✅ checked |
| **Webhook URL** | `https://bosun.example.com/webhooks/github` |
| **Webhook secret** | the value you generated with `openssl rand -hex 32` |

### Repository permissions

| Permission | Access | Why |
| --- | --- | --- |
| **Contents** | Read-only | clone the ref under review |
| **Pull requests** | Read & write | post the review comment |
| **Metadata** | Read-only | mandatory, granted automatically |
| **Checks** | Read & write | *optional* — gives a branch-only review a home when no pull request exists |

Grant nothing else. Bosun never writes code, pushes, merges, or approves.

### Subscribe to events

- **Create** — branch creation, needed only when `review.triggers.branchCreated` is on
- **Pull request**
- **Issue comment**
- **Pull request review comment**

### Where can this App be installed?

"Only on this account" unless you are deliberately publishing it.

Create the App, then note the **App ID** shown at the top of its settings page.

## 2. Generate a private key

On the App's settings page: **Private keys → Generate a private key**. GitHub
downloads a `.pem` once — it cannot be retrieved later.

## 3. Install the App

**Install App → choose the account → Only select repositories**, and pick the
repositories Bosun may review. This selection is the real security boundary:
Bosun can only mint tokens for repositories where the App is installed.

## 4. Store the credentials

Replace the PAT-based secret with the App's credentials. Both live under the
same `bosun-github` secret, so nothing else in the chart changes:

```bash
kubectl -n bosun delete secret bosun-github --ignore-not-found
kubectl -n bosun create secret generic bosun-github \
  --from-literal=app-id='123456' \
  --from-file=private-key=/path/to/bosun-reviewer.private-key.pem
```

Keep the existing `--from-literal=token=...` alongside them during a migration;
Bosun prefers the App and falls back to the PAT only when minting fails.

Then update the webhook secret if you changed it:

```bash
kubectl -n bosun create secret generic bosun-webhook \
  --from-literal=secret='YOUR_WEBHOOK_SECRET' \
  --dry-run=client -o yaml | kubectl apply -f -
```

Restart to pick up the new secret:

```bash
kubectl -n bosun rollout restart deployment/bosun-bosun
```

## What the reviewer receives

The App's private key stays in the controller. For each review the controller
mints an installation token scoped to the one repository being reviewed, with
`contents: read` and `pull_requests: write`, and stores it in a Secret owned by
that reviewer Job — so Kubernetes garbage-collects the credential along with the
Job.

That matters because a reviewer Job runs an AI agent over the contents of a pull
request, which is attacker-supplied input the agent reads by design. The private
key could mint tokens for every repository the App is installed on; the token
the Job actually holds cannot, and it expires.

Bosun refuses to start a review whose deadline outlives its token, rather than
spending the model budget and then failing to publish.

## 5. Verify

Comment `@bosun-review review` on a test pull request and watch the reviewer Job:

```bash
kubectl -n bosun logs -l app.kubernetes.io/name=bosun-review --tail=50
```

A working App logs the mint before the clone:

```
level=INFO msg="minted installation token" repo=acme/widget installation=4242 expires_at=...
```

If the App is not installed on the repository you get a clear error rather than
a confusing 404 on clone:

```
the GitHub App is not installed on acme/widget
```

And if the PAT fallback is still present, Bosun says so instead of failing:

```
level=WARN msg="GitHub App authentication failed; falling back to GITHUB_TOKEN" repo=acme/widget reason=...
```

Once reviews post cleanly, delete the `token` key to complete the migration:

```bash
kubectl -n bosun patch secret bosun-github --type=json \
  -p='[{"op":"remove","path":"/data/token"}]'
```

## What Bosun publishes

Every hosted review with a pull-request number ends in exactly one terminal
publication: a review, or a failure comment. Both open with a marker line and a
metadata line naming the reviewed ref and commit, so a consumer can correlate
output to a specific push rather than to a timestamp.

Every review -- with findings or without -- is published as a pull-request
review (`event: COMMENT`). Findings become one inline comment each, so the
standard reply and resolve machinery works against them; a clean review is the
same review with no threads. Bosun never approves or requests changes, so a
review from Bosun can never satisfy branch protection. The review body carries
the marker, the metadata line, and the summary:

```markdown
## Bosun code review

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

<summary>
```

Each inline comment opens with its severity: `**high** — <title>`, then the
failure it causes and a remediation.

The summary ends with a **Residual risks** section -- what this change could
break that reading the code cannot settle, each with the check that settles it
-- and a measured coverage line written by Bosun, not by the reviewer:

```markdown
<sub>Review coverage: 71 changed files (+322/−343), plus 1 generated or lockfile (+19800/−19600) not counted · full diff read · 12 of 70 changed files opened · 55 not examined individually · 8 context files opened.</sub>
```

The diff size comes from `git diff --numstat` against the merge base;
lockfiles, vendored, and `linguist-generated` files are counted separately.
The read counts come from the reviewer's tool calls and are a lower bound. See
[Review coverage](local-development.md#review-coverage-and-residual-risks) for
the fields.

### What a review compares against

A pull-request review compares the head with its merge base against the pull
request's **base commit** (`base.sha`), not the repository's default branch, so
a pull request into `release/1.x` is reviewed as that change alone. The base
comes from the webhook payload, from `GET /pulls/N` for a comment-triggered
review, or from the pull-request listing for a sweep, and reaches the reviewer
Job as `BOSUN_BASE_SHA`. If the clone cannot fetch that commit, the review falls
back to the default branch with a warning in the worker log rather than failing.
A branch review with no pull request always uses the default branch.

### Finding the review

Reviews live under `/repos/OWNER/REPO/pulls/N/reviews`; comments live under
`/repos/OWNER/REPO/issues/N/comments`, and listing one never returns the other.
Because every review publishes on the same surface, one query answers "did Bosun
review this commit" -- match the commit SHA in the review bodies:

```bash
gh pr view N --json reviews --jq '.reviews[].body'
# or
gh api repos/OWNER/REPO/pulls/N/reviews --jq '.[].body'
```

Failures are the exception: a failure is not a review, so it publishes as an
issue comment under its own marker. To see every Bosun publication, including
failures, read both:

```bash
gh api repos/OWNER/REPO/issues/N/comments --jq '.[].body'
```

Before 0.3.2, a clean review published as an issue comment and a findings
review also posted a summary comment pointing at it. Pull requests reviewed by
an older release still carry those comments.

A review whose output has no machine-readable findings block still publishes,
as a review with the reviewer's text as its body. When GitHub rejects a review
with HTTP 422 -- which it does for the whole review when any single comment
position is invalid -- Bosun retries once with every finding folded into the
body under a `### Findings` heading.

Findings on files the pull request does not touch are never sent as inline
comments; they are folded into the body under `### Findings outside the diff`,
keeping `path:line` in the text.

```markdown
## Bosun code review failed

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

`provider: context deadline exceeded`
```

The failure reason is the redacted diagnostic, prefixed with the stage that
failed (`setup`, `clone`, `provider`, `review`, or `publish`). It reports
infrastructure failure, not findings. Treat both marker lines as an interface:
agents select Bosun's reviews and comments by them.

### Failures the reviewer cannot report

The reviewer publishes its own failures from a deferred function, which covers
every error it can return and nothing it cannot survive. A reviewer that
exceeds `review.resources.limits.memory` is OOM-killed, and an OOM kill is
SIGKILL: no deferred function runs, the Job fails, its credential Secret is
garbage-collected with it, and after `review.ttlSecondsAfterFinished` the Job
and its logs are gone. Left alone, the pull request is told nothing and the
absence is indistinguishable from a review that never ran.

The same is true of the Job's own deadline, `review.timeoutSeconds` plus a
startup allowance. The reviewer's internal timeout fires first and publishes a
properly diagnosed failure; the Job deadline only fires when the reviewer has
run past that without unwinding, and it kills the pod.

The controller closes both gaps. Every `review.terminationSentinel.intervalSeconds`
it lists reviewer Jobs, and for one that died in a way the reviewer could not
have reported, it publishes the failure comment the reviewer could not:

```markdown
## Bosun code review failed

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

The reviewer was killed for exceeding its memory limit of `1Gi`, so it could
not report the failure itself. Raise `review.resources.limits.memory` and
review this commit again.
```

The comment names memory and the chart value on purpose. "The review provider
failed" sends an operator looking at the agent, its credentials, and the model,
when the fix is one quantity in `values.yaml`. A deadline kill reads the same
way and points at `review.timeoutSeconds`.

Only deaths the reviewer provably could not report are published, and the two
are told apart by different signals:

| Outcome | Reviewer published? | Signal the controller reads |
| --- | --- | --- |
| Reviewer's own timeout | yes, with the stage that failed | Job `Failed`, reason `BackoffLimitExceeded` |
| Job deadline, a startup allowance later | no -- killed | Job `Failed`, reason `DeadlineExceeded` |
| Memory limit | no -- SIGKILL | container `state.terminated.reason: OOMKilled` |

A deadline kill is a SIGTERM, so its container reason is `Error` --
indistinguishable from a reviewer that exited non-zero on its own and already
published. Only the Job condition separates them, which is why it is read
rather than the pod. When a reviewer is both out of memory and out of time,
memory is reported: it is the cause an operator can act on.

This does not weaken the one-publication rule above. Before posting, the
controller looks for a Bosun publication already carrying this commit's
metadata line, across both comments and reviews, and stays silent if it finds
one -- which covers the case it cannot see from Kubernetes alone, a reviewer
that published its review and was then killed while finishing up. A
publication is a body that *opens* with a marker: GitHub's "Quote reply"
reproduces a whole comment behind `> `, and matching a marker anywhere would
let an ordinary human reply pass as Bosun's own output and suppress a report
the pull request is owed. If the search for one runs past its page bound
without concluding, that is reported as an unfinished search and retried, never
treated as proof that nothing was published. The search is also bounded below
by the review's own start: correlating by commit alone would let an earlier
attempt's failure comment suppress a retry's, leaving the pull request holding
a stale reason for a different failure. It records the
outcome on the Job as `bosun/termination-reported`, so neither a later pass nor
a restarted controller reports it twice, and it holds the
`bosun-termination-sentinel` Lease while publishing, so two controller replicas
cannot both decide to speak. Only a failure to publish is left unrecorded,
because a transient GitHub error must stay retryable.

Reporting needs a credential after the Job's own one is gone. With a GitHub App
the controller mints a narrow repository token on demand, exactly as it does at
submission, and still holds nothing per-review. An install using a personal
access token has nothing to mint from: the chart offers the controller the
shared `token` key from the same Secret, and an install that declines it logs
the termination instead of publishing it.

Correlation needs the commit, so the controller resolves it before the Job is
built. A comment trigger (`@bosun-review review`) knows only a pull-request
number, and the reviewer used to resolve the real branch and head for itself at
startup -- in memory, leaving its Job recording `pr-N` and no commit. The
controller now resolves it at submission, so the Job records the same identity
the reviewer publishes under. That resolution is an improvement, not a
precondition: if it fails, the review still runs and still publishes for
itself, and only that review's termination becomes unreportable. The controller
says so at WARN and reports nothing rather than posting a comment naming no
commit.

A review with no pull request -- the `branch-created` trigger, off by default,
which runs with `BOSUN_PR_NUMBER=0` -- publishes as a check run instead. See
[Branch-only reviews](#branch-only-reviews) below.

Set `review.terminationSentinel.intervalSeconds` to `0` to turn the check off
and return to publishing nothing when a reviewer is killed.

## Branch-only reviews

**Off by default.** Set `review.triggers.branchCreated: true` to enable it.

A branch push and the pull request opened from it review the same commit, so
leaving this on makes the ordinary `git push -u` then `gh pr create` flow pay
for two identical reviews. `review.maxConcurrent` is counted across the
namespace rather than per repository, so the duplicate takes a slot and the
pull request opened moments later waits in the review queue behind it. With
the queue disabled (`review.queue.maxQueued: 0`) it is rejected with 503
instead, and GitHub does not redeliver a failed webhook. Enable it when branches are pushed that will not become pull requests
soon; the pull-request triggers already cover the ordinary flow.

When it is on, the review runs before any pull request exists, so there is no
pull request to post to. It publishes as a **check run on the reviewed commit**
instead.

A check run is keyed by commit rather than by pull request, which is what makes
this work without storing anything: when a pull request opens on that commit
later, GitHub surfaces the existing check run on it. The findings arrive
without a second review running.

| | |
| --- | --- |
| Name | `Bosun review` |
| Anchored to | the commit the reviewer actually read |
| Conclusion | `success` when the review is clean, otherwise `neutral` |
| Findings | one annotation each, anchored to file and line |
| Summary | the same `## Bosun code review` marker and ``` `ref` @ `sha` ``` metadata line a comment carries |

The conclusion is never `failure`. A review reports; it does not gate. On a
repository that requires checks to pass, concluding `failure` would block a
merge on an opinion nobody opted into.

Two things happen before the check run, in order:

1. **The pull request is resolved again.** The number is fixed when the Job is
   built, which is before the review runs. In the ordinary `git push -u` then
   `gh pr create` flow the pull request often exists by the time the review
   finishes, and that review publishes as a normal inline review.
2. **The check run is written** when there is still no open pull request.

If the installation has not granted **Checks**, the token never requests it --
asking for a permission an installation does not hold makes GitHub reject the
whole token mint -- and the review is reported at WARN naming the repository,
ref, commit, and reason, rather than being discarded silently. Granting Checks
to an existing installation needs no Bosun change; the next review picks it up.

## What this does not do yet

A review is not a required status check: the check run reports, and nothing
consumes its conclusion. Failures that happen before a token is obtained remain
in Job logs. Repeated reviews of one commit share a publication identity, so two
overlapping attempts can suppress each other's termination report (#74). Repeated reviews are independent: Bosun does not resolve or supersede
the threads an earlier pass opened (#42).

## Notes

- The App JWT is signed RS256 and expires in 9 minutes; GitHub rejects anything
  older than 10. Bosun backdates `iat` by 60s to tolerate clock drift.
- Installation tokens last an hour, comfortably longer than the 1800s review cap.
- The private key is stripped from the environment handed to the AI provider,
  along with `GITHUB_TOKEN` — see `agentEnvironment` in `internal/review/clone.go`.
