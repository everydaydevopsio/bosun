# Task: Build and release pipeline

## Context
- Owner: Mark C Allen
- Date: 2026-10-01
- Mode: Autonomous
- PRD Section: n/a (infrastructure)
- Requirement IDs: n/a
- Plan: `plans/plan-build-pipeline.md`

## Scope
- In scope: CI gates, a reusable release-validation workflow, semver bump-and-tag
  releases, GHCR image and Helm chart publishing, signed and notarized macOS CLI
  archives with a Homebrew tap, Dependabot, release docs, badges, license.
- Out of scope: Windows release (issue #1), golangci-lint adoption (issue #2),
  the Go migration items tracked below.

## Acceptance Criteria
- AC1: A `workflow_dispatch` with a release type tags the repo and publishes the
  image, chart, and CLI archives at that one version. ✅ workflows written and
  actionlint-clean; first live run still pending repository secrets.
- AC2: `bosun version` in the release archive and in the image equals the tag. ✅
  verified from a snapshot archive and an image smoke test.
- AC3: macOS archives are signed and notarized before any upload. ✅ enforced as a
  GoReleaser post-build hook; snapshots skip it explicitly.
- AC4: An installed chart pulls the image released with it. ✅ `image.tag` now
  defaults to `appVersion`; previously it defaulted to a `latest` tag that is
  never published.

## Constraints
- Mirror `everydaydevopsio/ballast`'s release shape.
- Nothing is tagged before validation passes; the tag is the point of no return.

## Risks and Tradeoffs
- Risk: `OPENAI_API_KEY` is not yet granted to this repo, so `bump_and_tag` fails
  at the castoff gate. Deliberate — it fails loudly rather than tagging a release
  with no changelog.
- Risk: `everydaydevopsio/homebrew-bosun` does not exist yet; `publish_cli` fails
  at the tap write, after the GitHub Release is published.
- Tradeoff: image CVE scanning reports without failing in CI (the base image is
  bridgectl's) but fails the publish on a fixable HIGH/CRITICAL.

## Execution Checklist
- [x] Stamp and test `bosun version`; container and Makefile builds pass it through
- [x] `.github/workflows/validate.yml` — reusable release gate
- [x] `.github/workflows/ci.yml` — lint, coverage, CLI, chart, scripts, image, scan
- [x] `.github/workflows/publish.yml` — bump, tag, image, chart, signed CLI
- [x] `.goreleaser.yaml` + `scripts/sign-and-notarize-darwin.sh`
- [x] Chart image tag defaults to appVersion instead of an unpublished `latest`
- [x] Dockerfile passes hadolint with documented ignores
- [x] `.github/dependabot.yml`, `LICENSE`, `CHANGELOG.md`, README badges
- [x] `docs/releasing.md` and `docs/README.md`
- [x] File the Windows release issue
- [ ] Grant `OPENAI_API_KEY`, `APPLE_*`, `HOMEBREW_TAP_GITHUB_TOKEN`, `CODECOV_TOKEN`
      to `everydaydevopsio/bosun` (repository configuration, not a code change)
- [ ] Create the public `everydaydevopsio/homebrew-bosun` repository

## Test Strategy
- Unit: `cmd/bosun/main_test.go` covers `version`, `help`, their flag aliases, the
  unstamped default, and that the usage text lists every dispatched command.
- Integration: `validate.yml` builds the image and asserts `bosun version` inside it.
- E2E: `make release-snapshot` builds all four archives, the checksums, and the cask.
- Failure-path tests: `metaCommand` must not claim `review`, `serve`, or an unknown
  command; the smoke test fails on a version mismatch, not just a non-zero exit.
- Requirement-to-test mapping: AC2 → `TestMetaCommand` + the image smoke step.

## Rollback Strategy
- Trigger: a release job fails after the tag is pushed.
- Rollback steps: do not retry the same version. Classify per the table in
  `docs/releasing.md`; roll forward with a new patch release when a partial
  outward write happened.
- Validation after rollback: the GitHub Release, the GHCR image tag, the chart
  version, and the cask all name the same new version.

## Outcome
- Result: pipeline implemented and verified locally; first live release is blocked
  only on repository secrets and the tap repository.
- Evidence: `go test ./...`, `gofmt -l .`, `go vet ./...`, `shellcheck -S warning
  scripts/*.sh`, `helm lint charts/bosun`, `goreleaser check`, `make
  release-snapshot`, `actionlint`, `hadolint Dockerfile`.
- PRD updates: none.

# Task: Go migration plan

- [ ] Map the Python controller and reviewer contracts to Go packages (GO-1, GO-2).
- [ ] Add Go module, generated protobuf bindings, and red tests for controller behavior.
- [ ] Implement the Go controller, Kubernetes Job submission, and GitHub auth.
- [ ] Implement the Go reviewer, cloning, bridgectl session client, and output posting.
- [ ] Replace Dockerfile, Makefile, scripts, and docs with Go runtime commands (GO-3).
- [ ] Run unit tests, lint, image build, Helm rendering, and the credential-free local smoke path.
- [ ] Record evidence, rollback guidance, and lessons.
