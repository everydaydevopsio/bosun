# Local development with Kind

You can exercise Bosun's Kubernetes reviewer without configuring GitHub, creating a webhook, or pushing the repository being reviewed anywhere.

## Requirements

Install Go 1.26 or newer for local builds and tests. Run `make setup` to check
development tools (Docker, Kind, kubectl, Helm and
shellcheck), installing missing tools through Homebrew when available.
Setup does not fetch credentials or write `.env`.

```bash
make setup
```

## Credentials

The reviewer Job reads its provider credentials from the `bosun-ai` Secret in the
cluster. There are two ways to get them there.

### Let the CLI find them

If `codex` or `claude` is already signed in on this machine, `--local-credentials`
copies that sign-in into the cluster:

```bash
bosun up --local-credentials          # prepare a cluster, or refresh credentials
bosun review "$PWD" --local-credentials
```

`bosun up` checks that the selected provider has a credential in the Kind
Secret. Without `--local-credentials`, this is an offline presence and expiry
check; it cannot prove the provider will accept the token. With
`--local-credentials`, Bosun checks saved credential metadata and makes one
short headless request through the selected provider's local CLI. It copies
credentials only if that request
succeeds, and reports a fixed authentication diagnosis without printing
provider output if it fails. This live check can consume a small amount of
provider usage and is limited to one minute. Use `--provider` to choose the
provider whose credential is checked.
An exported `CLAUDE_CREDENTIALS` or `CODEX_AUTH` JSON value cannot be verified
by a different saved CLI login; use the CLI's own sign-in file or a provider API
key/OAuth environment variable for the live check.

Discovery order, first match wins per credential:

1. An exported environment variable, which is always an explicit choice.
2. `codex`'s `auth.json`, under `$CODEX_HOME` when set, otherwise `~/.codex`.
3. `claude`'s credentials: the `Claude Code-credentials` Keychain item on macOS,
   or `~/.claude/.credentials.json` on Linux.

The CLI reports which source each credential came from and never prints a value.
Credentials are copied, not linked: these are OAuth tokens that your local CLIs
refresh in place, so re-run `bosun up --local-credentials` after re-authenticating.
An existing local sign-in file can still contain a rejected or revoked token.
If `review-status` reports `AUTH_REVOKED` or `AUTH_REJECTED` for `claude-bosun`,
verify an actual request with `claude --print -- "Reply OK"`. If it returns
HTTP 401, renew the Claude credential (for example, with `claude auth login`),
then run `bosun up --local-credentials --provider claude-bosun` and start a new review.
For `codex-bosun`, run `codex login`, then
`bosun up --local-credentials --provider codex-bosun`. A CLI status that says
"logged in" does not prove the provider will accept the stored token.
Bosun also stops `--local-credentials` before Secret upload if Claude's saved
access token has expired and the credential has no refresh token.

Bosun refuses to continue when the selected provider has no discovered credential,
naming the ones that provider accepts. Provider acceptance is checked when the
review session starts. Only that provider's keys are mounted into
the Job even when the Secret holds more — a review cannot read another provider's
token.

### Export them yourself

Export the variables for your provider before `make kind-up`, using the examples
below. `make kind-up` copies the exported credentials into the `bosun-ai` Secret;
when nothing is exported it falls back to the discovery path above. After changing
credentials, run `make kind-up` again, or `bosun up --local-credentials`.

Recognised credentials:

| Variable | Used by | Delivered as |
| --- | --- | --- |
| `OPENAI_API_KEY` | codex-bosun | environment variable |
| `CODEX_AUTH` | codex-bosun | written to `~/.codex/auth.json` |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | claude-bosun | environment variable |
| `CLAUDE_CREDENTIALS` | claude-bosun | written to `~/.claude/.credentials.json` |
| `GEMINI_API_KEY` | gemini | environment variable |

Pick a provider that produces structured output — see
[bridgectl.md](bridgectl.md#choosing-a-provider). Use `codex-bosun` or `claude-bosun`;
`codex` renders a terminal UI and its output is rejected.

Codex:

```bash
export OPENAI_API_KEY=...
export BOSUN_REVIEW_PROVIDER=codex-bosun
```

Claude:

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...
export BOSUN_REVIEW_PROVIDER=claude-bosun
```

## Start the environment

With a released CLI there is nothing to start: `bosun review` creates what it
needs. `bosun up` does the same work ahead of time.

| Command | Creates | Needs |
| --- | --- | --- |
| `bosun up` | Kind cluster with the durable per-user snapshot directory mounted at `/repos`, the `bosun` namespace, and with `--local-credentials` the `bosun-ai` Secret | `kind`, `docker` |
| `bosun down` | removes the cluster and the snapshot directory (`--keep-snapshots` to leave it) | `kind`, `docker` |
| `./scripts/kind-up.sh` | everything `bosun up` does, plus the locally built `bosun:dev` image and the Helm chart | also `kubectl`, `helm`, a source checkout |

The CLI does not install the Helm chart. A local review submits its Job through
your kubeconfig and holds its own admission lease, so the chart's Deployment,
Service, and RBAC — which exist for the GitHub webhook path — are not involved.
Run `./scripts/kind-up.sh` when you want the controller too, or when you are
working from source and need `bosun:dev` built and loaded.

From the Bosun repository:

```bash
./scripts/kind-up.sh
```

The script:

1. collects any exported provider credentials;
2. runs `bosun up` to create the cluster, the namespace, and the Secret, falling
   back to credential discovery when nothing was exported;
3. builds `bosun:dev` against the pinned bridgectl version (override with `BRIDGECTL_VERSION`);
4. loads the image directly into Kind, so no registry is required;
5. installs the Bosun Helm chart in development mode.

No GitHub credential or webhook secret is required.

## Review any local Git repository

```bash
./scripts/review-local.sh ~/src/my-project
```

Or review the current directory:

```bash
./scripts/review-local.sh .
```

The script creates a self-contained Git snapshot in Kind's mounted development area, including linked worktrees, default-branch refs, staged changes, unstaged changes, and untracked files. `.env` is excluded. It creates a one-shot reviewer Job, waits for it to succeed or fail, prints the review to your terminal, and exits non-zero if the Job failed.

Setup and review commands explicitly select `kind-bosun` (or
`kind-$BOSUN_KIND_CLUSTER`). They do not use your active Kubernetes context.

This means a coding agent can trigger Bosun locally with:

```bash
/path/to/bosun/scripts/review-local.sh "$PWD"
```

The repository does not need a GitHub remote. Bosun does not push, commit, or modify the developer's original checkout.

## Tuning the session

The headless provider exits when it finishes. The review only succeeds after
a successful session exit; partial output and deadlines count as failures.
`BOSUN_REVIEW_TIMEOUT_SECONDS` (default 1800) limits the review, and the Job's
deadline includes an additional 120 seconds for startup:

```bash
BOSUN_REVIEW_TIMEOUT_SECONDS=600 ./scripts/review-local.sh .
```

## Inspecting a run

While a review is running:

```bash
kubectl -n bosun get jobs,pods
kubectl -n bosun logs -f job/<job-name>
```

## Rebuild after changing Bosun

Run `./scripts/kind-up.sh` again. It rebuilds and reloads `bosun:dev` and upgrades the Helm release.

## A cluster from before the Go migration

`./scripts/kind-up.sh` can fail on a long-lived Kind cluster with:

```
Error: UPGRADE FAILED: cannot patch "bosun-bosun" with kind Deployment:
Deployment.apps "bosun-bosun" is invalid: spec.selector: ... field is immutable
```

This means the cluster still holds a release installed before the Go migration,
when the chart's Deployment selector did not carry
`app.kubernetes.io/instance`. `spec.selector` is immutable, so Helm cannot patch
it. Published charts are unaffected — every tagged chart has the current
selector — so this only reaches a development cluster that has been alive since
before that change.

The failure is quiet: the old pod keeps running, and only `helm history bosun -n
bosun` shows the release in `failed`.

Delete the Deployment and run the script again. Helm recreates it, and nothing
else in the release is immutable:

```bash
kubectl -n bosun delete deployment bosun-bosun
./scripts/kind-up.sh
```

`bosun down && bosun up` works too, and costs a cluster rebuild.

## Remove everything

```bash
bosun down                  # or ./scripts/kind-down.sh, which calls it
bosun down --keep-snapshots # keep local review snapshots
```

This deletes the Kind cluster and, unless you keep them, repository snapshots
under `~/Library/Application Support/bosun/repos` on macOS or
`~/.local/share/bosun/repos` on Linux. `XDG_DATA_HOME/bosun/repos` takes
precedence over those defaults on either platform. Set `BOSUN_REPOS_DIR`
to an absolute path before creating the cluster to use another disk; `bosun down`
keeps a custom directory for manual cleanup. The snapshot directory is shared by every
Bosun cluster on the host, so `--keep-snapshots` matters when you run more than
one.

Clusters created before this path change keep their `/tmp/bosun-repos` mount.
Recreate one with `bosun down --keep-snapshots && bosun up` before submitting a
review. If a long-lived node reports `hostPath type check failed` or remains in
`ContainerCreating`, run `docker restart bosun-control-plane` to restore its
bind mount, then retry; recreating the cluster is another remedy.

## Reviewing from inside Claude Code or Codex

`bosun init` installs a review skill for the coding agents configured on this
machine, so you can ask the agent you are already working in to run a review
instead of switching to a terminal:

```bash
bosun init                      # installs for ~/.claude and ~/.codex
bosun init --target claude      # one agent
bosun init --print              # inspect the skill without installing it
```

The skill is embedded in the binary, so a Homebrew install can place it with no
repository checked out, and the instructions always match the CLI that shipped
them. Agents pick it up on their next session. `CODEX_HOME` is honoured, so a
relocated Codex configuration gets the skill where that Codex actually reads it.

A skill that differs from the one the binary ships is left alone — it may be a
local edit — and `--force` replaces it.

The skill exists mostly to encode three things an agent gets wrong unaided: a
review can run for 30 minutes and must be started with `--detach` and polled
rather than waited on, the review arrives on stdout while progress goes to
stderr, and the reviewer reads code without executing it, so its findings are
claims to judge rather than test results to apply.

## Native review command

Build the CLI with `make cli` (creates `bin/bosun`), or install it on your Go
binary path with `make install`. Run `make kind-up` once after updating Bosun to
rebuild the worker image. The CLI and worker image must both include the event
protocol; an old image cannot provide a complete structured result.

```bash
bosun review "$PWD"
bosun review "$PWD" --branch feature/login --base main --provider claude-bosun
bosun review "$PWD" --provider codex-bosun --timeout 20m
bosun review "$PWD" --json
bosun review "$PWD" --detach
bosun review-status <job-name> --follow
```

Without `--branch`, the snapshot includes the current commit and staged,
unstaged, and untracked files. An explicit `--branch` selects committed content
only, even if it names the current branch. It never switches your checkout.
`--base` selects the comparison base; Bosun resolves the default branch when
possible and requests an explicit base when ambiguous. No implicit fetch occurs.
The submission message prints the resolved base commit and merge base. For a
pull request from a fork, use the installed Bosun review skill: it fetches the
target repository's base branch, verifies it against GitHub's exact base commit,
and stops if the fetch fails or the branch has moved. Passing the fork's local
`main` as `--base` can silently review the wrong diff.
For a branch with no PR, the skill requires an explicit offline choice:
`BOSUN_REVIEW_OFFLINE=true`. In that mode, Bosun resolves the checkout's default
branch and reports the comparison it chose.

Flags work before or after the path. `--provider` overrides
`BOSUN_REVIEW_PROVIDER`; it does not change cluster credentials. Export the
credentials and rerun `make kind-up` when adding or refreshing them.

### Which image a review runs

`bosun review` resolves the reviewer image in this order:

1. `BOSUN_DEV_IMAGE`, then `BOSUN_REVIEW_IMAGE`, when either is set;
2. `ghcr.io/everydaydevopsio/bosun:<version>` when the CLI is a release build,
   that is when `bosun version` reports an exact `MAJOR.MINOR.PATCH`;
3. `bosun:dev` otherwise, the tag `./scripts/kind-up.sh` builds and loads.

A binary from `make cli` reports a `git describe` version such as
`v0.1.1-2-gabc1234`, which names no published image, so it stays on `bosun:dev`.

Before submitting any work, the CLI inspects that image on the Kind node and
refuses to run when it is not a Bosun image:

```text
image bosun:dev on the Kind node is a "bridgectl" image, so it has no bosun
binary to run; rebuild and reload it with ./scripts/kind-up.sh
```

That is what a Kind cluster left over from an earlier version of Bosun looks
like. Without the check the Job is created and the kubelet fails it with
`exec: "/usr/local/bin/bosun": stat ...: no such file or directory`, after the
snapshot has already been taken. A locally built tag that is missing from the
node is refused the same way, since nothing can pull it.

Two cases warn instead of failing: an image whose version label differs from the
CLI version, and a check that cannot run at all because `docker` or `crictl` is
unavailable. An image you named yourself through the environment is never
refused, only reported.

The backend is the configured local Kind cluster, whose node must mount the
configured durable snapshot directory at `/repos`. Bosun probes a marker from
inside the node before submitting a review. Remote clusters are not supported by this snapshot
transport. The CLI honors `KUBECONFIG`, explicitly selects `kind-bosun` (or
`kind-$BOSUN_KIND_CLUSTER`), and accepts `--context` and `--namespace` overrides.
It never silently chooses the active Kubernetes context.

With `--branch`, Bosun snapshots the named committed ref and ignores unrelated
modified or untracked files in your checkout. It does not switch your branch.
Without `--branch`, it includes working-tree changes; check the intended subject
before choosing that mode. A `repo_path ... not under any allowed path` error for
`/repos/review.*` means the worker image's bridgectl config is incompatible.
Update or rebuild the image with `/repos` in `config/bridge-bosun.yaml`; retrying
the same image cannot fix it.

Progress goes to stderr and the final review to stdout. JSON mode emits
versioned `bosun_event: 1` records on stdout. Updates include stage, elapsed time,
quiet time, session state, pod warnings, and deadline remaining. Provider
activity is distinct from a heartbeat; no percentage is fabricated. Detailed
file/tool actions are not available in the current bridgectl session schema.

Run metadata and completed results are stored privately in
`$XDG_STATE_HOME/bosun` (default `~/.local/state/bosun`), overridable with
`BOSUN_STATE_DIR`. Worker event journals beside snapshots preserve detached-run
results even after Kubernetes logs expire. Invoke `review-status` to collect a
detached result. Snapshots are removed only after pod termination is confirmed;
if cleanup cannot be confirmed, the path is retained for inspection.

Ctrl-C in `bosun review` cancels its job and waits for pod termination before
cleanup. Ctrl-C in `review-status --follow` only stops watching. `--detach`
returns the job name and leaves the review running.

Duration estimates require at least five comparable successful runs for the
same repository, provider, image, and similar change size. They are approximate:
model configuration and provider load can differ. Until sufficient history
exists, the command reports that an estimate is unavailable. The execution
limit is always displayed separately.

Exit codes: 0 for a successful review (including one with findings), 1 for an
execution failure, 2 for invalid input/configuration, 124 for timeout, and 130
for interruption.

A failed review names which part failed, because the responses differ: a setup
or clone failure is usually yours to fix, a provider failure is usually worth
retrying, and a reviewer that declined to review is neither. The reviewer Job
exits with a code for the stage, so a pod's terminated `exitCode` still carries
the classification after events and logs have expired:

| Exit | Stage | Message | Typically |
| --- | --- | --- | --- |
| 10 | setup | Bosun could not prepare the review | configuration, credentials, or workspace — check the message |
| 11 | clone | Bosun could not obtain the repository | token scope, repository name, or network |
| 12 | provider | The review provider failed | check the diagnostic code and follow its action |
| 13 | review | The reviewer did not review the change | the agent declined; the reason it gave is in the message |
| 14 | publish | The review completed but could not be published | the review succeeded; posting to GitHub did not |

`bosun review` itself still exits 1 for any of these; the stage is in the
message it prints and in `bosun review-status`, which reports the reason rather
than a stored result that does not exist. `make review REPO=... ARGS='--provider claude-bosun'` and
`scripts/review-local.sh` delegate to the native CLI.

Provider exits include a stable diagnostic code: `AUTH_REVOKED`,
`AUTH_REJECTED`, `RATE_LIMITED`, or `UNKNOWN_EXIT`. Bosun classifies provider
output without copying raw provider text into the status, since errors can
contain credential or request details. The failed Job is not resumed after
credential repair; submit a new review. An `UNKNOWN_EXIT` means Bosun could not
identify the cause from the provider response. Check the provider sign-in and
retry once; if it repeats, inspect the provider runtime and bridgectl logs.

Set `BOSUN_QUIET_WARNING_AFTER=2m` to change when silent-provider updates are
flagged as unconfirmed progress. A quiet warning never kills the review by itself.
