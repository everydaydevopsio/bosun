---
name: bosun-review
description: >
  Run an independent AI code review of the current repository with Bosun, then
  triage the findings. Use this skill when the user asks to: review my changes,
  review this branch, review this PR, get a second opinion on the code, check
  this before I push, run bosun, or any variation of "what's wrong with this
  change". The review runs in a Kubernetes Job and takes minutes, so it must be
  started detached and polled, never waited on in a single command.
---

# Bosun code review

Bosun reviews a Git checkout with an independent AI reviewer running in a Kind
cluster, and prints findings. It is a second reader, not a test runner.

## Before running

Check the CLI is present and new enough. A `bosun` from before these flags
existed fails with `unknown flag`, which looks like your mistake rather than a
stale install:

```bash
command -v bosun || echo "Bosun is not installed: see https://github.com/everydaydevopsio/bosun"
bosun help | grep -q -- --local-credentials || echo "This bosun is too old; upgrade it"
```

If either check fails, tell the user how to fix it and stop. Do not try to work
around a missing or old binary.

## Decide what to review

The default is the committed work on this branch, compared with the branch it
was created from — the same change a reviewer sees in a pull request. Confirm
three things first, in order, and stop at the first one that fails.

**1. The tree is clean.**

```bash
git status --porcelain
```

Any output means uncommitted work. Stop and tell the user which files, then let
them choose rather than choosing for them:

- commit them and review the branch, which is what the rest of this assumes; or
- review the working tree as it stands, with `bosun review "$PWD"` and no
  `--branch` — useful before committing, but it is not what a reviewer will see.

**2. There is a branch to compare.**

```bash
git branch --show-current
git rev-parse --abbrev-ref origin/HEAD    # e.g. origin/main
```

If the current branch *is* the default branch, there is no branch-to-base
comparison to make. Say so and offer the working-tree review, or suggest
creating a branch for the work first.

**3. The branch has commits of its own.**

```bash
git rev-list --count origin/HEAD..HEAD
```

Zero means nothing to review yet.

## Resolve the arguments before launching

Work these out first and keep them in variables. Launching before they are
settled is how a review ends up against the wrong base while looking fine.

```bash
BRANCH="$(git branch --show-current)"

# The PR's base when this branch has one, because that is what its reviewers
# will see. Empty otherwise, and Bosun resolves the default branch itself --
# the fork point for a branch cut from it.
BASE="$(gh pr view --json baseRefName --jq .baseRefName 2>/dev/null)"
[ -n "$BASE" ] && { git fetch origin "$BASE" --quiet || echo "cannot fetch base $BASE; stop and tell the user"; }

# An explicit choice wins; otherwise match the credentials on this machine.
PROVIDER="${BOSUN_REVIEW_PROVIDER:-}"
if [ -z "$PROVIDER" ]; then
  if [ -f "${CODEX_HOME:-$HOME/.codex}/auth.json" ]; then
    PROVIDER=codex-bosun
  else
    PROVIDER=claude-bosun
  fi
fi
```

Tell the user which base was used. A review against the wrong base is worse than
no review, because the diff looks plausible.

In a fork checkout `origin` is your fork, not the repository the pull request
targets, so `origin/$BASE` may be a diverged branch or absent. Stop and say so
if the fetch fails rather than reviewing against whatever `--base` resolves to.
Tracked in [#34](https://github.com/everydaydevopsio/bosun/issues/34).

## Reviewing a named pull request

If the user names a PR, check whether the PR's revision is already checked out.
Compare commits, not branch names: a local branch with the same name may be
stale, or belong to a different fork, and reviewing it looks like it satisfied
the request.

```bash
gh pr view <number> --json headRefOid --jq .headRefOid
git rev-parse HEAD
```

**If those commits match, review it as above. If they do not, getting the PR's
revision moves the user's working branch — ask before doing it, and wait for an
answer.** That includes the case where the branch name matches but the commit
does not, which means the local copy is behind or is someone else's branch. Say
plainly what will happen:

> Reviewing PR #123 means checking out `fix-the-thing`; you are currently on
> `my-work`. Shall I switch, or would you rather I review something else?

Only after they accept:

```bash
git status --porcelain     # must be empty; never switch over uncommitted work
gh pr checkout <number>    # fetches the head, forks included
```

Offer to return them to their original branch when the review is done, and name
the branch you will return them to.

Never check out, switch, stash, reset, or fetch over a dirty tree without being
asked to. The review is not worth losing someone's work in progress.

## Choose the provider

`--local-credentials` loads whatever sign-in this machine has, but the default
provider is `codex-bosun`, so a machine with only Claude authenticated fails
with "no usable credentials found for provider". That is why `PROVIDER` is
resolved above rather than assumed:

- `BOSUN_REVIEW_PROVIDER` is set — use it. An explicit choice wins, and passing
  it as `--provider` is the same selection, not an override of it.
- otherwise `$CODEX_HOME/auth.json` or `~/.codex/auth.json` exists —
  `codex-bosun`.
- otherwise Claude is signed in — `claude-bosun`.

Pass it on the review command itself; the selection is not remembered between
commands. Bosun also creates the cluster on first use, which takes a
few minutes.

## Run it detached, then poll

A review can run for up to 30 minutes. Your shell tool will time out long
before that, so **never run `bosun review` in the foreground and never use
`review-status --follow`**. Start it detached and poll instead:

This is the only launch command in this skill. Run it once, with the variables
resolved above:

```bash
JOB="$(bosun review "$PWD" --branch "$BRANCH" ${BASE:+--base "origin/$BASE"} --detach --local-credentials --provider "$PROVIDER")" || JOB=""
echo "JOB=$JOB"
```

**Check it actually submitted before polling.** An empty `JOB` means submission
failed -- the message is on stderr and names which part failed. Report that and
stop; do not poll, because there is nothing to poll for and no terminal event
will ever arrive:

```bash
[ -n "$JOB" ] || { echo "submission failed; not polling"; }
```

The job name goes to stdout; progress goes to stderr. Then poll, waiting
between checks. Each poll returns immediately:

```bash
bosun review-status "$JOB" --json
```

Do not pipe that through `tail` or `head` when you need to know whether it
worked: a pipeline reports the exit status of its last command, so `| tail`
turns a failed poll into a successful one. `review-status` exits non-zero and
prints no terminal event when it cannot load the job, which is indistinguishable
from "still running" if you only read stdout.

Look for an event whose `"type"` is `completed`, `failed`, `timed_out` or
`cancelled`. Until one appears the review is still running — wait about 30
seconds and poll again.

**Keep polling in this turn until a terminal event appears.** Do not end your
turn with the review running, and do not hand it to a background mechanism that
will wake you later unless your harness reliably wakes you: in a one-shot
session nothing will, and the user gets "it is running" instead of a review.

Bound it. Stop polling and report if the poll command fails twice in a row, or
once the Job deadline reported in the status output has passed -- the review
cannot outlive it. Polling forever is the failure this bound exists to prevent. Tell the user it is still going rather than
going silent; first runs are slower because the cluster and image are being
prepared.

When it completes, read the review:

```bash
bosun review-status "$JOB"
```

## What the reviewer is

It reads code. It cannot run tests, builds or linters, so it will not tell you
whether anything passes — pair it with the repository's own CI. A finding that
says "add a test for this" is a reading of the diff, not a test run.

Each finding carries a repository-relative path and line range, a severity, the
concrete failure it causes, and the reviewer's confidence. Findings are
claims, not verdicts.

## After the review

Do not apply findings blindly. For each one:

1. Read the code it points at and judge whether it is right. The reviewer is
   working from reading alone and says so in its confidence line.
2. Tell the user which findings you agree with and why, and which you think are
   wrong — a wrong finding confidently applied is worse than no review.
3. Offer to fix the ones that hold up. Fix them as separate, reviewable changes
   rather than one sweep.

If the review fails rather than finishing, the message names which part failed:
`setup` and `clone` are usually configuration you can fix, `provider` is often
worth one retry, and `review` means the reviewer declined and said why.

## Do not

- Do not run the review in the foreground or with `--follow`.
- Do not end your turn while the review is still running.
- Do not move the user's branch, stash, or discard anything without being asked.
- Do not review a dirty tree as though it were the branch; they are different
  changes and the user chooses which one they want.
- Do not re-run a review to "get a better answer"; each run spends model credits.
- Do not treat an absent finding as proof that an area is correct. The review
  lists the files it read and found nothing material in; anything outside that
  list was not examined.
