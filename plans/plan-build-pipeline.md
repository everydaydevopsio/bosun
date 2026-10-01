# Plan: Build and Release Pipeline

- **Status**: In progress
- **Branch**: `codex/go-migration-foundation`
- **Created**: 2026-10-01
- **Related ADRs**: none yet

## Problem

Bosun has a minimal CI workflow and a publish workflow that only pushes a container
image and a Helm chart from a hand-created tag. There is no versioning workflow, no
Go lint or coverage gate, no signed CLI distribution, and no Dependabot. Bosun also
ships a native CLI (`cmd/bosun`) that users install outside Kubernetes, with no
release channel at all.

Ballast (`everydaydevopsio/ballast`) already runs the release shape this org
standardises on: a `workflow_dispatch` `release_type` bump, an AI-generated
changelog, a tag that is the point of no return, and per-target publish jobs that
check out that tag. Bosun should mirror it.

## Approach

Mirror the Ballast pipeline, scaled to Bosun's single Go module:

1. **`validate.yml`** — a `workflow_call` workflow that runs the full gate (lint,
   tests with coverage, CLI build, chart render, image build and smoke). It is the
   single definition of "is this tree releasable", called by `publish.yml` before
   anything is tagged, exactly as Ballast calls `cross-language-validate.yml`.
2. **`ci.yml`** — pull-request and `main` feedback: Go lint, Go tests with coverage
   uploaded to Codecov, CLI build, Helm chart contract checks, shell lint, and
   Dockerfile lint + image build + smoke + Trivy scan.
3. **`publish.yml`** — `workflow_dispatch` with a `release_type` choice plus `v*`
   tag pushes. `bump_and_tag` computes the next semver, bumps the chart version and
   appVersion, generates release notes and `CHANGELOG.md` with castoff, commits and
   tags. Publish jobs (`publish_image`, `publish_chart`, `publish_cli`) check out
   the tag, never the branch head.
4. **`.goreleaser.yaml`** — macOS release for the `bosun` CLI. Darwin and Linux,
   amd64 and arm64. Developer ID signing and notarization run as a GoReleaser
   post-build hook, before any upload, so a notarization failure aborts the release
   rather than publishing unnotarized archives. Formula and cask go to
   `everydaydevopsio/homebrew-bosun`.
5. **`bosun version`** — the CLI reports the version stamped by `-ldflags`, so the
   release artifact's version output matches the tag. The container build stamps the
   same variable.

Windows is deliberately out of scope for this change and tracked as a separate
issue.

## Files Affected

| Path | Reason |
| --- | --- |
| `.github/workflows/ci.yml` | Expand to lint, coverage, CLI build, image scan |
| `.github/workflows/validate.yml` | New reusable release gate |
| `.github/workflows/publish.yml` | Rewrite to the bump-and-tag release pattern |
| `.github/dependabot.yml` | New: gomod, github-actions, docker |
| `.goreleaser.yaml` | New: signed macOS and Linux CLI archives, Homebrew tap |
| `scripts/sign-and-notarize-darwin.sh` | New: GoReleaser post-build hook |
| `cmd/bosun/main.go` | Add `version` output stamped at build time |
| `cmd/bosun/main_test.go` | New: cover the meta commands |
| `Dockerfile` | Stamp the same version variable into the image binary |
| `Makefile` | `coverage`, `release-snapshot`, version-stamped `cli` |
| `CHANGELOG.md`, `LICENSE` | Release prerequisites |
| `README.md`, `docs/releasing.md`, `docs/README.md` | Badges, install, release runbook |

## Phases

- [x] Explore Ballast's `ci.yml`, `publish.yml`, `publish-cli.yml`, and `.goreleaser.yaml`
- [x] Stamp and test a CLI version command
- [x] Write the CI, validate, and publish workflows
- [x] Write the GoReleaser config and the notarization hook
- [x] Docs, badges, changelog, license, Dependabot
- [x] Verify locally (tests, gofmt, vet, helm, shellcheck, actionlint, goreleaser check)
- [x] File the Windows release issue (#5)

## Verification

- `go test ./... -covermode=atomic -coverprofile=coverage.out`
- `gofmt -l .`, `go vet ./...`
- `shellcheck -S warning scripts/*.sh`
- `helm lint charts/bosun`
- `goreleaser check --config .goreleaser.yaml`
- `actionlint` over `.github/workflows/`

## Alternatives Rejected

- **Tag-only releases (status quo)**: a human picks the version and hand-creates the
  tag, so the chart version, image tag, and CLI version can drift. Rejected for the
  same reason Ballast moved off it.
- **Reusing `everydaydevopsio/homebrew-ballast` as the tap**: works without a new
  repo, but installs read `everydaydevopsio/ballast/bosun`, which misnames the
  product. Rejected in favour of a per-product tap, matching Ballast.
- **Unsigned macOS archives**: no Apple secrets needed, but every user hits
  Gatekeeper. Rejected; Ballast parity includes notarization.
- **Publishing Windows archives in this change**: GoReleaser would build them for
  free, but they would be unsigned and untested, and `scripts/` and the Kind-based
  local review path are POSIX-only. Tracked as a separate issue instead.

## Open Questions

- `OPENAI_API_KEY` must be granted to `everydaydevopsio/bosun` for castoff release
  notes. The workflow fails loudly if it is missing, matching Ballast.
- `everydaydevopsio/homebrew-bosun` must exist and be public before the first
  release.

## Change Log

| Date | Change |
| --- | --- |
| 2026-10-01 | Initial plan |
