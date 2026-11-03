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
| **Checks** | Read & write | *optional* — required only for the Checks output that gives branch-only reviews a home |

Grant nothing else. Bosun never writes code, pushes, merges, or approves.

### Subscribe to events

- **Create** — branch creation
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

Comment `@bridgectl review` on a test pull request and watch the reviewer Job:

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

Findings are published as a pull-request review (`event: COMMENT`) with one
inline comment per finding, so the standard reply and resolve machinery works
against them. The review body carries the marker, the metadata line, and the
summary:

```markdown
## Bosun code review

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

<summary>
```

Each inline comment opens with its severity: `**high** — <title>`, then the
failure it causes and a remediation.

### Finding the review

A review lives under `/repos/OWNER/REPO/pulls/N/reviews`; a comment lives under
`/repos/OWNER/REPO/issues/N/comments`. Listing comments does not return reviews
-- `gh pr view N --json comments` included. A findings review therefore also
posts one short summary comment, so a plain comment listing shows that the
review happened and where to read it:

```markdown
## Bosun code review

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

**3 findings** — 2 high, 1 low.

[Read the review](https://github.com/OWNER/REPO/pull/7#pullrequestreview-99) —
findings are inline threads where they could be anchored to the diff, and in the
review body otherwise. This comment is a pointer; the detail is in the review.
```

It links the review rather than naming a location, because not every finding is
a thread: the 422 fallback folds them all into the review body, and a finding
outside the diff is never anchorable. Severities are listed in the same triage
order a consumer sorts by, so the summary and the review agree.

The summary is a pointer, not the review: it never repeats a finding's detail,
and it is not the terminal publication. One review yields exactly one comment
and exactly one review, so neither listing double-counts. If the summary fails
to post, the review still stands and the Job still succeeds -- the warning is in
the worker log.

To answer "did Bosun review this commit" without depending on which surface was
used, match the commit SHA in both listings:

```bash
gh api repos/OWNER/REPO/pulls/N/reviews --jq '.[].body'
gh api repos/OWNER/REPO/issues/N/comments --jq '.[].body'
```

Two cases fall back to a conversation comment under the same marker, because a
published review beats a lost one:

- the reviewer emitted no machine-readable findings block, so nothing can be
  anchored;
- GitHub rejected the review with HTTP 422, which it does for the whole review
  when any single comment position is invalid. Bosun retries once with every
  finding folded into the body under a `### Findings` heading.

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
agents select Bosun's comments by them.

### Failures the reviewer cannot report

The reviewer publishes its own failures from a deferred function, which covers
every error it can return and nothing it cannot survive. A reviewer that
exceeds `review.resources.limits.memory` is OOM-killed, and an OOM kill is
SIGKILL: no deferred function runs, the Job fails, its credential Secret is
garbage-collected with it, and after `review.ttlSecondsAfterFinished` the Job
and its logs are gone. Left alone, the pull request is told nothing and the
absence is indistinguishable from a review that never ran.

The controller closes that gap. Every `review.terminationSentinel.intervalSeconds`
it lists reviewer Jobs, and for one that failed with a container-termination
reason the reviewer could not have survived, it publishes the failure comment
the reviewer could not:

```markdown
## Bosun code review failed

<sub>`feature` @ `0123456789abcdef0123456789abcdef01234567`</sub>

The reviewer was killed for exceeding its memory limit of `1Gi`, so it could
not report the failure itself. Raise `review.resources.limits.memory` and
review this commit again.
```

The comment names memory and the chart value on purpose. "The review provider
failed" sends an operator looking at the agent, its credentials, and the model,
when the fix is one quantity in `values.yaml`.

This does not weaken the one-publication rule above. Before posting, the
controller looks for a Bosun marker already carrying this commit's metadata
line, across both comments and reviews, and stays silent if it finds one --
which covers the case it cannot see from Kubernetes alone, a reviewer that
published its review and was then killed while finishing up. It records the
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

A review with no pull request -- the `branch-created` trigger, which runs with
`BOSUN_PR_NUMBER=0` -- has nowhere to publish. The controller logs it at WARN
rather than discarding it silently; the discarded review itself is #65.

Set `review.terminationSentinel.intervalSeconds` to `0` to turn the check off
and return to publishing nothing when a reviewer is killed.

## What this does not do yet

Installation tokens request only `contents: read` and `pull_requests: write`;
no Checks permission is requested, so a review is not a status check. Branch-
only reviews, and failures that happen before a token is obtained, remain in
Job logs. The controller reports an out-of-memory kill; a Job killed by its
`activeDeadlineSeconds` -- which fires 120s after the review's own timeout, so
only when the reviewer has already hung past reporting -- is not yet classified
the same way. Repeated reviews are independent: Bosun does not resolve or supersede
the threads an earlier pass opened (#42).

## Notes

- The App JWT is signed RS256 and expires in 9 minutes; GitHub rejects anything
  older than 10. Bosun backdates `iat` by 60s to tolerate clock drift.
- Installation tokens last an hour, comfortably longer than the 1800s review cap.
- The private key is stripped from the environment handed to the AI provider,
  along with `GITHUB_TOKEN` — see `agentEnvironment` in `internal/review/clone.go`.
