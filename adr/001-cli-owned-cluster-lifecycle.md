# ADR-001: The CLI owns the local cluster and loads local provider credentials

- **Status**: Accepted
- **Date**: 2026-10-01
- **Branch**: `cli-cluster-lifecycle`
- **PR**: [#17](https://github.com/everydaydevopsio/bosun/pull/17)
- **Supersedes**: none
- **Superseded by**: none

## Context

Running a local review required a repository checkout and two shell scripts.
`scripts/kind-up.sh` was the only thing that knew how to build the Kind cluster,
and the only thing that knew how to load provider credentials into it. A user who
installed the released CLI from the tap had neither, so the documented first-run
path was "clone the repository and run a shell script".

The credential half was worse. The script hard-failed when nothing was exported:

```
No provider credentials found in the environment.
Expected one of OPENAI_API_KEY, CODEX_AUTH, CLAUDE_CODE_OAUTH_TOKEN, ...
```

It said that on machines with an authenticated `codex` and `claude` sitting right
there. And on macOS the Claude credential is not a file at all — it lives in the
Keychain, so there was nothing to `cat` into a variable.

## Decision

The CLI owns the cluster lifecycle and credential discovery.

- `bosun up` ensures the Kind cluster (created with `/tmp/bosun-repos` mounted at
  `/repos`), the namespace, and — with `--local-credentials` — the `bosun-ai`
  Secret built from credentials found on this machine.
- `bosun down` deletes the cluster and the snapshot directory.
- `bosun review` runs the same ensure path implicitly, so the first review on a
  clean machine creates what it needs. `--no-bootstrap` opts out.

Credential discovery takes the first match per credential: an exported variable,
then `codex`'s `auth.json` (under `$CODEX_HOME` or `~/.codex`), then `claude`'s
credentials — the `Claude Code-credentials` Keychain item on macOS, or
`~/.claude/.credentials.json` elsewhere. Only the source of each credential is
reported, never its value.

Three boundaries are deliberate:

1. **Discovery is opt-in.** It copies real account credentials into a cluster.
   That should be an explicit act, not a side effect of running a review.
2. **The CLI does not install the Helm chart.** A local review submits its Job
   through the user's kubeconfig and holds its own admission lease, so the
   chart's Deployment, Service, and RBAC — which serve the GitHub webhook path —
   are never read by `SubmitLocal`. Skipping it keeps the dependency surface at
   `kind` and `docker`, with no `helm` or `kubectl`.
3. **One provider-to-Secret-key mapping.** It lives in `internal/credentials` and
   is read by both the Job builder and the CLI's credential check, so the two
   cannot disagree about which credentials a provider may receive.

## Alternatives Considered

- **Install the Helm chart from the CLI.** Adds a `helm` dependency and a
  controller Deployment that a local review never reads.
- **Shell out to `kubectl`.** The CLI already holds a typed client; the namespace
  and Secret are two API calls.
- **Discover credentials by default, with no flag.** Copying account credentials
  into a cluster should be deliberate.
- **Vendor Kind as a library.** A large dependency for three commands the `kind`
  binary already exposes.

## Consequences

Positive:

- A released CLI reaches a working review with one command on a machine that has
  only `kind` and `docker`.
- `kind-up.sh` and `kind-down.sh` delegate instead of carrying a second
  implementation, so the contributor path and the released path are the same code.
- Per-provider credential scoping is enforced from a single table.

Negative:

- Real account credentials now land in a Kind Secret, which is base64 at rest,
  and the reviewer runs untrusted repository content. Mitigated by per-provider
  scoping and documented as local-only.
- Credentials are copied, not linked. OAuth tokens refresh locally and the Secret
  does not follow, so `bosun up --local-credentials` must be re-run after
  re-authenticating.
- The controller remains a `kind-up.sh` concern, so "the CLI does everything" is
  true for local review and not for the webhook path.

## Implementation Notes

- `internal/cluster` embeds `kind-config.yaml` so a released binary can create a
  cluster with no checkout. A test pins the embedded copy equal to the repository
  copy, and a second pins the mount contract (`HostRepos` → `ContainerRepos`).
- `internal/credentials` takes the home directory and the Keychain reader as
  injected fields, which is also the seam a Windows implementation would use.
- `scripts/kind-up.sh` reorders: `kind load` now follows cluster creation,
  because the CLI creates the cluster it loads into.

## Verification

- Created a throwaway `bosun-test` cluster from nothing, loaded credentials into
  it, confirmed the `/repos` mount, then deleted it without touching the default
  cluster.
- `bosun review --detach --local-credentials` submitted a real Job whose
  secret-backed environment was `OPENAI_API_KEY` and `CODEX_AUTH` only, with a
  Claude credential present in the same Secret — per-provider scoping holding end
  to end.
- `go test ./...`, `gofmt -l .`, `go vet ./...`, `shellcheck -S warning scripts/*.sh`.

## Lessons Learned

- **A cached test result is not a test run.** `go test ./...` reported
  `internal/review` as cached while the shell script that test exercises was
  broken; CI caught it. Changes touching files a test reads at runtime need
  `-count=1`. Recorded in `tasks/lessons.md`.
- **A script must run the CLI from the checkout it ships in.** `kind-down.sh`
  preferred a PATH binary and failed with `unknown command: down` against an
  installed 0.1.1. A script and the CLI it drives are versioned together.
- **Shipping assets in the binary is what makes a released CLI self-sufficient.**
  The `go:embed` of the cluster config is the same move later proposed for
  installing agent skills (#23).

## Follow-ups

- [#18](https://github.com/everydaydevopsio/bosun/issues/18) — `opencode` was
  documented and validated but receives no credentials. Closed by removing the
  claim.
- [#5](https://github.com/everydaydevopsio/bosun/issues/5) — Windows. `unix.Flock`
  now gates `up` and `down` as well as `review`; credential discovery needs a
  third branch; `HostRepos` must become Windows- and WSL2-shaped.
