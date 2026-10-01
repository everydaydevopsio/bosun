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
- Out of scope: Windows release (#5), golangci-lint adoption (#6),
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
- [x] File the Windows release issue (#5) and the golangci-lint issue (#6)
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

# Task: Reviewer image preflight and release-aware image default

## Context
- Owner: Mark C Allen
- Date: 2026-10-01
- Mode: Autonomous
- PRD Section: n/a (local review UX)
- Requirement IDs: n/a

## Scope
- In scope: how `bosun review` chooses the reviewer image, a preflight that
  verifies that image on the Kind node, and an image label that makes the check
  possible.
- Out of scope: the hosted (webhook) path, which takes its image from
  `BOSUN_REVIEW_IMAGE` through the chart and pulls from a registry.

## Acceptance Criteria
- AC1: A Kind node holding a stale non-Bosun image under the reviewer tag fails
  before a snapshot is taken, with a message naming the fix. ✅
- AC2: A released CLI does not default to `bosun:dev`; it defaults to the image
  published alongside it. ✅
- AC3: A `make cli` build keeps using `bosun:dev`, since `git describe` names no
  published image. ✅

## Constraints
- The probe reads image metadata only; it never inspects container state or
  credentials.
- A check that cannot run must not block a review.

## Risks and Tradeoffs
- Risk: an image built before this change carries no `bosun` title label and is
  now refused. Accepted: such an image also predates the event protocol, so the
  review would have failed later and less clearly.
- Tradeoff: identity is read from an OCI label rather than by executing the
  binary, which cannot be done without starting a container.

## Execution Checklist
- [x] `internal/version` holds the stamped version so the CLI and the image
      resolver read one value; ldflags updated in Makefile, Dockerfile, GoReleaser
- [x] `reviewImage` resolves env override → published release image → dev tag
- [x] `checkImage` + `kindInspector` preflight, wired in before snapshotting
- [x] Dockerfile sets `org.opencontainers.image.*` so the image stops reporting
      the bridgectl base's identity
- [x] `docs/local-development.md` documents resolution order and the preflight

## Test Strategy
- Unit: `internal/localreview/image_test.go` — table tests over resolution
  (release, `git describe`, unstamped, both env overrides) and over every
  preflight outcome.
- Integration: ran the built CLI against a Kind cluster still holding the
  pre-Go-migration image and confirmed exit 2 before any snapshot.
- Failure-path tests: foreign image, unlabelled image, absent local tag, absent
  pullable image, probe that cannot run, unparseable probe output.
- Requirement-to-test mapping: AC1 → `TestCheckImage`, AC2/AC3 → `TestReviewImage`.

## Rollback Strategy
- Trigger: the preflight refuses an image that is in fact a working Bosun image.
- Rollback steps: set `BOSUN_DEV_IMAGE` to the same tag, which downgrades any
  refusal to a warning, or revert this change.
- Validation after rollback: `bosun review` reaches the snapshot stage again.

## Outcome
- Result: the opaque `stat /usr/local/bin/bosun: no such file or directory` Job
  failure is now a one-second preflight error that names the fix.
- Evidence: `go test ./...`, `gofmt -l .`, `go vet ./...`, `hadolint Dockerfile`,
  `make cli && ./bin/bosun version`, and a live run against the stale cluster.
- PRD updates: none.
