# Plan: CLI-owned cluster lifecycle and local credential injection

- **Status**: In progress
- **Branch**: `cli-cluster-lifecycle`
- **Created**: 2026-10-01
- **Related ADRs**: none yet

## Problem

`bosun review` requires a Kind cluster that only `scripts/kind-up.sh` knows how to
build, and credentials that only that script knows how to load. A user who
installs the released CLI has neither the repository nor the scripts, so the
documented first-run path is "clone the repo and run a shell script" — and the
script hard-fails when provider credentials are absent from the environment,
even though the same machine usually has an authenticated `codex` or `claude`
CLI sitting right there.

Two consequences:

1. A released CLI cannot reach a working state on its own.
2. Credentials must be exported by hand (`export CODEX_AUTH="$(cat ...)"`), and
   on macOS the Claude credential is not even a file — it lives in the Keychain.

## Approach

Give the CLI the three things the scripts own, and nothing more:

- `bosun up` — ensure the Kind cluster (created with the `/repos` mount), the
  namespace, and, with `--local-credentials`, the `bosun-ai` Secret built from
  credentials discovered on this machine. Idempotent.
- `bosun down` — delete the cluster and the snapshot directory.
- `bosun review` — run `up`'s ensure path implicitly when the cluster or
  namespace is missing, unless `--no-bootstrap` is given.

The Helm chart is deliberately **not** installed by the CLI. A local review
submits its Job directly through the user's kubeconfig and holds its own
admission lease; the chart's Deployment, Service, and RBAC exist for the webhook
path and are not read by `SubmitLocal`. Skipping it keeps the CLI's external
dependencies to `docker` and `kind`, with no `helm` requirement.

Credential discovery is opt-in behind `--local-credentials` because it copies
real account credentials into a cluster Secret. Discovery order per provider
key: an already-exported environment variable, then the provider CLI's own
credential file, then (macOS only) the Keychain item Claude Code writes instead
of a file.

Windows is explicitly out of scope here and recorded on #5, which already owns
the `unix.Flock` blocker; this plan adds the cluster and credential specifics to
that issue rather than widening its scope into this branch.

## Files Affected

| Path | Reason |
| --- | --- |
| `internal/credentials/credentials.go` | New: discover local provider credentials |
| `internal/cluster/cluster.go` | New: Kind lifecycle through the `kind` binary |
| `internal/cluster/kind-config.yaml` | New: embedded cluster config, kept equal to the repo root copy by test |
| `internal/localreview/cli.go` | `up`/`down` dispatch, bootstrap, `--local-credentials` |
| `internal/jobs/jobs.go` | Export the provider→Secret-key mapping as the single source of truth |
| `cmd/bosun/main.go` | Dispatch `up`/`down`, extend usage |
| `scripts/kind-up.sh`, `scripts/kind-down.sh` | Delegate to the CLI; keep the dev-image build |
| `docs/local-development.md`, `README.md` | Document the new first-run path |

## Phases

- [x] Explore: confirm what a local review actually requires from the cluster
- [x] Credential discovery with tests (env, files, Keychain)
- [x] Kind lifecycle + namespace/Secret ensure, with tests
- [x] CLI surface: `up`, `down`, `--local-credentials`, implicit bootstrap
- [x] Scripts delegate to the CLI; docs and README
- [x] Windows: comment on #5 with the cluster and credential specifics

## Verification

- `go test ./...`, `gofmt -l .`, `go vet ./...`, `hadolint`, `shellcheck`
- `BOSUN_KIND_TEST=1` live checks against a real Kind cluster
- A full teardown/bootstrap round trip: `bosun down` then
  `bosun review --local-credentials` with no scripts involved

## Alternatives Rejected

- **Install the Helm chart from the CLI** — adds a `helm` dependency and a
  controller Deployment that a local review never reads.
- **Shell out to `kubectl`** — the CLI already holds a typed client; the
  namespace and Secret are two API calls.
- **Discover credentials by default, with no flag** — copying account
  credentials into a cluster should be a deliberate act.
- **Vendor Kind as a library** — a large dependency for three commands that the
  `kind` binary already exposes.

## Open Questions

- Whether `bosun down` should also remove the dev image from the node. Current
  answer: no, deleting the cluster removes it with the node.

## Change Log

| Date | Change |
| --- | --- |
| 2026-10-01 | Plan created |
| 2026-10-01 | Implemented; verified a full create/review/delete round trip on macOS |
