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

## Choose what to review

- **Working tree** (default): `bosun review "$PWD"` — includes staged, unstaged
  and untracked changes. This is what the user usually means by "review my
  changes".
- **A branch**: `bosun review "$PWD" --branch <branch> --base <base>` — committed
  content only.

**Fetch a pull request before reviewing it.** Bosun resolves refs in the local
checkout with `git rev-parse` and never fetches, so a branch name taken from
`gh pr view` may not exist locally, may be an unrelated local branch with the
same name, or may be stale — and a fork's branch will not be there at all. Each
of those reviews the wrong code and looks like it worked:

```bash
gh pr checkout <number>                                           # fetches the head, forks included
base="$(gh pr view <number> --json baseRefName --jq .baseRefName)"
git fetch origin "$base"
bosun review "$PWD" --branch "$(git branch --show-current)" --base "origin/$base"
```

## Choose the provider

`--local-credentials` loads whatever sign-in this machine has, but the default
provider is `codex-bosun`, so a machine with only Claude authenticated fails
with "no usable credentials found for provider". Choose to match:

- `BOSUN_REVIEW_PROVIDER` is set — honour it and pass no `--provider`.
- `~/.codex/auth.json` exists — `--provider codex-bosun`.
- otherwise, if Claude is signed in — `--provider claude-bosun`.

Pass the same `--provider` on the review command itself; it is not remembered
between commands. Bosun also creates the cluster on first use, which takes a
few minutes.

## Run it detached, then poll

A review can run for up to 30 minutes. Your shell tool will time out long
before that, so **never run `bosun review` in the foreground and never use
`review-status --follow`**. Start it detached and poll instead:

```bash
JOB="$(bosun review "$PWD" --detach --local-credentials --provider codex-bosun)"
echo "$JOB"
```

The job name goes to stdout; progress goes to stderr. Then poll, waiting
between checks. Each poll returns immediately:

```bash
bosun review-status "$JOB" --json | tail -5
```

Look for an event whose `"type"` is `completed`, `failed`, `timed_out` or
`cancelled`. Until one appears the review is still running — wait about 30
seconds and poll again.

**Keep polling in this turn until a terminal event appears.** Do not end your
turn with the review running, and do not hand it to a background mechanism that
will wake you later: in a one-shot session nothing will, and the user gets "it
is running" instead of a review. Tell the user it is still going rather than
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
- Do not pass a branch name you have not fetched and verified.
- Do not end your turn while the review is still running.
- Do not re-run a review to "get a better answer"; each run spends model credits.
- Do not treat an absent finding as proof that an area is correct. The review
  lists the files it read and found nothing material in; anything outside that
  list was not examined.
