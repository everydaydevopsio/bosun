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

# Task: CLI-owned cluster lifecycle and local credential injection

## Context
- Owner: Mark C Allen
- Date: 2026-10-01
- Mode: Autonomous
- PRD Section: n/a (local review UX)
- Plan: `plans/plan-cli-cluster-lifecycle.md`

## Scope
- In scope: `bosun up`, `bosun down`, `--local-credentials`, implicit bootstrap from
  `bosun review`, discovery of codex/claude sign-in on macOS and Linux.
- Out of scope: Windows (tracked on #5, which this branch updates with the new
  surface), installing the Helm chart from the CLI, the GitHub webhook path.

## Acceptance Criteria
- AC1: A machine with `kind` and `docker` and an authenticated provider CLI can
  run a review with no scripts and nothing exported. ✅ verified end to end.
- AC2: Credentials are discovered from the Keychain on macOS and from
  `~/.claude/.credentials.json` elsewhere, and from `codex`'s `auth.json`. ✅
- AC3: Only the selected provider's credentials reach the Job. ✅ the submitted
  Job referenced `openai-api-key` and `codex-auth` only, with a Claude credential
  present in the same Secret.
- AC4: `bosun down` removes the cluster; a named cluster leaves others alone. ✅

## Constraints
- No `helm` or `kubectl` dependency for the CLI path; `kind` and `docker` only.
- Credential values are never printed, only their source.

## Risks and Tradeoffs
- Risk: real account credentials now land in a Kind Secret, which is base64 at
  rest, and the reviewer runs untrusted repository content. Mitigated by
  per-provider scoping; documented as local-only.
- Tradeoff: `bosun up` does not install the chart, so the controller is still a
  `kind-up.sh` concern. Keeps the dependency surface small.

## Execution Checklist
- [x] `internal/credentials` discovery with tests
- [x] `internal/cluster` Kind lifecycle, namespace and Secret ensure, with tests
- [x] `up`/`down` commands, `--local-credentials`, `--no-bootstrap`, `--keep-snapshots`
- [x] One provider→Secret-key mapping, shared by the Job builder and the CLI
- [x] Scripts delegate to the CLI; `kind-up.sh` reordered so `kind load` follows creation
- [x] README and `docs/local-development.md`
- [x] Comment on #5 with the Windows plan for the new surface

## Test Strategy
- Unit: discovery order, Keychain fallback, `CODEX_HOME`, malformed credential
  files, provider satisfaction; Kind argv construction, embedded-config drift,
  namespace/Secret merge semantics; flag parsing for every new command.
- Integration: created a throwaway `bosun-test` cluster from nothing, loaded
  credentials into it, confirmed the `/repos` mount, then deleted it.
- E2E: `bosun review --detach --local-credentials` on the default cluster,
  inspected the resulting Job, deleted it.
- Failure-path tests: provider with no credential, unreadable Keychain, invalid
  JSON, missing `kind`/`docker`, dev image absent from a fresh cluster.

## Rollback Strategy
- Trigger: bootstrap creates or deletes a cluster a user did not expect.
- Rollback steps: `--no-bootstrap` restores the previous behaviour for review;
  `kind-up.sh`/`kind-down.sh` still work.
- Validation after rollback: `bosun review --no-bootstrap` fails as before when
  the cluster is absent.

## Outcome
- Result: a released CLI reaches a working review with one command.
- Evidence: `go test ./...`, `gofmt -l .`, `go vet ./...`, `shellcheck -S warning
  scripts/*.sh`, plus the live round trip recorded above.
- Discovered, promoted to GitHub as
  [#18](https://github.com/everydaydevopsio/bosun/issues/18): the `opencode`
  provider is documented as using `OPENAI_API_KEY` but `credentials.ForProvider`
  matches only `codex*`, `claude*` and `gemini*` prefixes, so an opencode Job
  receives no credentials at all.
- Windows follow-up recorded on
  [#5](https://github.com/everydaydevopsio/bosun/issues/5#issuecomment-5942148215).
- Discovered while running `scripts/kind-up.sh` against a cluster holding the
  0.1.0 release, promoted to
  [#19](https://github.com/everydaydevopsio/bosun/issues/19): the chart's
  Deployment `spec.selector` gained a label between 0.1.0 and 0.1.1, and that
  field is immutable, so every 0.1.0 install fails to upgrade.

# Task: Guard the chart against immutable-field drift

## Context
- Owner: Mark C Allen
- Date: 2026-10-06
- Mode: Autonomous
- Issue: https://github.com/everydaydevopsio/bosun/issues/19

## Scope
- In scope: a CI guard on the Deployment fields an upgrade cannot change, and a
  troubleshooting note for the clusters that can hit the failure.
- Out of scope: renaming the Deployment. The investigation below shows no
  published chart is affected, so a rename would force a migration on the only
  real install base to fix a problem it does not have.

## Investigation
- `app.kubernetes.io/instance` entered the selector in `bc305c0`, inside the Go
  migration (#7), before either release tag was cut. `v0.1.0` and `v0.1.1`
  therefore render identical selectors.
- Chart 0.1.0 was never published: `helm pull oci://ghcr.io/everydaydevopsio/charts/bosun --version 0.1.0`
  returns `not found`. The only published chart is 0.1.1, whose rendered
  Deployment matches `main`.
- The observed failure came from a development cluster installed on 2026-09-23
  from the pre-migration working tree, which Helm recorded as chart 0.1.0
  because `Chart.yaml` carried that version at the time.
- Issue #19's original claim, that every 0.1.0 install is stuck, was wrong and is
  corrected on the issue.

## Acceptance Criteria
- AC1: CI fails if the Deployment's name or selector changes. ✅ verified by
  running the guard against a deliberately altered selector.
- AC2: An operator who hits the failure finds the recovery. ✅ documented in
  `docs/local-development.md`.
- AC3: No change to the chart's rendered output. ✅ the guard passes against the
  chart unmodified.

## Outcome
- Result: the real exposure is a stale development cluster, with a one-line
  recovery; the regression that would affect published installs is now guarded.
- Evidence: `helm lint`, the guard run locally in both directions, and the
  published-chart comparison recorded above.

# Task: Stop claiming opencode is supported

## Context
- Owner: Mark C Allen
- Date: 2026-10-06
- Mode: Autonomous
- Issue: https://github.com/everydaydevopsio/bosun/issues/18

## Scope
- In scope: removing the claim, in docs and in `scripts/kind-up.sh`, that
  `opencode` is a usable provider.
- Out of scope: making it work. That needs a decision about which credentials it
  should receive, which stays on #18.

## Acceptance Criteria
- AC1: No document or script tells a user opencode is configured or validated. ✅
- AC2: The reason is stated where someone would look for the provider list. ✅
  `docs/bridgectl.md` says it is unsupported and why.
- AC3: A regression test fails if opencode silently gains credentials without
  the docs changing. ✅ added to the provider-scoping table test.

## Outcome
- Result: `kind-up.sh` no longer validates a credential the Job never receives,
  so the setup path stops promising something the review cannot deliver.
- Evidence: `go test ./internal/jobs/ -count=1`, `shellcheck -S warning scripts/*.sh`,
  case-insensitive sweep of docs, README, scripts and charts.

# Task: Improve the review prompt from observed output

## Context
- Owner: Mark C Allen
- Date: 2026-10-06
- Mode: Autonomous
- Evidence: a real review of orchael/bridgectl PR #284 (`b619a722` vs merge base
  `9d31196e`), whose two findings were verified against the diff before judging
  the prompt.

## Scope
- In scope: `prompts/code-review.md`, changed from defects observed in that output.
- Out of scope: the execution boundary itself (#22). This states the boundary in
  the prompt; whether the reviewer should ever execute code stays there.

## Observed defects
1. Findings linked `/repos/review.940875040/internal/localserver/pki.go:587` --
   the container snapshot path. Unusable to a reader, worse than no link in a PR
   comment. The prompt asked for "file and line/range" without saying which path.
2. The diff touched eight files; every finding was in one. Nothing distinguished
   "reviewed and clean" from "not read".
3. A finding that can leave the server unable to start (certificate paired with
   the wrong key) was rated the same Medium as a detection gap. Four severity
   levels were named and none defined.
4. The verification section reported attempting tests that the environment
   cannot run -- invited by "Run relevant tests or static checks when practical".
5. "Skipped mutating setup steps; no files or GitHub state changed" -- the agent
   reporting its compliance rather than reviewing code.
6. The summary described the findings, not what the change does.

## Acceptance Criteria
- AC1: Findings carry repository-relative paths. Pending re-run.
- AC2: Output distinguishes clean files from unreviewed ones. Pending re-run.
- AC3: No reported command attempts. Pending re-run.
- AC4: Severity matches the stated rubric. Pending re-run.

## Test Strategy
- The prompt is prose; `go test ./...` only proves nothing was coupled to its
  wording. The real check is a re-run of the same bridgectl PR against the new
  prompt, diffed against the recorded output above.

## Defect found by the first re-run
The rewrite opened with "You cannot execute anything", and the qualifying list
after it did not survive: the agent concluded it could not run `git` either,
and since `internal/review/runner.go:152` supplies only identifiers, it had
nothing to review. It asked the operator to paste the diff in and exited:

```
Commit identifiers alone do not establish what the change does or support code findings.
Please provide the complete merge-base-to-HEAD diff, applicable repository-local rule files, and surrounding source for changed functions.
Review status: incomplete. No source files reviewed; no conclusions about material findings.
```

Both providers permit read-only git -- `claude-bosun` allowlists `git diff`,
`git show`, `git log`, `git merge-base`; `codex-bosun` runs read-only rather
than no-exec -- so the restriction was wrong, not just badly worded. The prompt
now states the reading method affirmatively and first, scopes the restriction to
builds, tests, linters and installs, and says the session is unattended so the
agent never asks for material.

## Second defect found by the re-run
The clean run fixed all six targeted defects, but dropped something the original
output had: it never named the commit it reviewed. The first review opened with
"Reviewed `b619a722` against merge base `9d31196e`"; folding that instruction
into the reading paragraph stopped it appearing in the output. The branch moved
248 lines between the two runs, which is exactly when a reviewed-revision line
matters. Restored as the first required output element.

## Third defect, found by reviewing this branch with Bosun itself
Running `bosun review` against this branch caught a regression the bridgectl
re-runs could not: those pass `--base`, so `BOSUN_BASE_SHA` is set and
`internal/review/runner.go:153` appends the base commit to the prompt. Hosted
reviews get no such variable -- `internal/jobs/jobs.go` sets `BOSUN_BASE_SHA`
only in `SubmitLocal` -- so the old line "Compare the checked-out commit with
the repository default branch" was the hosted path's only baseline. The rewrite
deleted it and said "merge-base-to-HEAD", which names no second revision. A
hosted review would have had to guess its comparison range.

The prompt now resolves the default branch when no base is supplied, and says
why reviewing HEAD alone is not an option.

The same review also caught `yarn-error.log`, swept into commit 5d93201 by
`git add -A`. Removed and ignored.

## Outcome
- Result: prompt rewritten; three regressions found and fixed, two by re-running
  against bridgectl and one by running Bosun against this branch. A prose change
  has no unit test, so running it is the only verification there is.
- Evidence: `go test ./internal/review/ ./internal/server/ -count=1`, plus the
  failed run above.
- Discovered, not fixed here: a review that reviewed nothing exits 0 and is
  recorded `completed`. A refusal is indistinguishable from a clean review.

# Task: Stop installing a preinstalled shellcheck in CI

## Context
- Owner: Mark C Allen
- Date: 2026-10-07
- Mode: Autonomous

## Scope
- In scope: the `apt-get` install of shellcheck in `ci.yml` and `validate.yml`,
  and a timeout on the job that hung.
- Out of scope: actions/runner#3667, an open upstream report of shellcheck
  itself hanging on 24.04 runners. The timeout bounds it; nothing here fixes it.

## Evidence
- `shellcheck 0.9.0-1` is listed in the ubuntu-24.04 runner image's installed
  apt packages, so the install step reinstalled what was already present.
- The Shell scripts job on PR #24 sat 12m42s inside `apt-get update` before
  cancellation, with every `azure.archive.ubuntu.com` index returning `Ign:`.
  shellcheck never ran. An in-run retry hung the same way; a fresh run passed.

## Acceptance Criteria
- AC1: The job no longer contacts an Ubuntu mirror. ✅
- AC2: `validate.yml`, which gates releases, no longer has a mirror in its path. ✅
- AC3: A hang fails in minutes rather than tens of minutes. ✅ `timeout-minutes: 5`.
- AC4: The shellcheck version actually used is recorded in the log. ✅

## Outcome
- Result: one network dependency removed from a pure static-analysis job, and
  from the release validation path.
- Evidence: both workflows parse; `shellcheck -S warning scripts/*.sh` and
  `bash -n scripts/*.sh` clean locally.

# Task: A review that reviewed nothing must not succeed

## Context
- Owner: Mark C Allen
- Date: 2026-10-07
- Mode: Autonomous

## Scope
- In scope: detecting a reviewer that did not review, and failing the run.
- Out of scope: why it could not review. The observed instance was a prompt bug,
  fixed in #24; this is about the outcome being indistinguishable from success.

## Evidence
A run against bridgectl ended with the reviewer asking for the diff to be pasted
in and stating "Review status: incomplete. No source files reviewed". The Job
exited 0, the pod reported `Succeeded`, the CLI printed "Review completed", and
the run was eligible to count toward duration estimates.

## Acceptance Criteria
- AC1: A reviewer that reports incompleteness fails the Job. ✅
- AC2: A refusal is never published to a pull request. ✅ the check runs before
  `postReview`.
- AC3: Prose about incompleteness in a real review does not fail it. ✅
- AC4: Output from an image predating the marker still succeeds. ✅
- AC5: The status trailer never appears in a published review. ✅

## Test Strategy
- Unit: the verbatim refusal that motivated this, empty and whitespace output, a
  clean review with no findings, prose containing "incomplete", a marker inside a
  sentence, mixed case and spacing, and output with no marker at all.

## Outcome
- Result: a refusal now fails the run instead of being recorded as a review.
- Evidence: `go test ./... -count=1`.

# Task: Let Bosun fail distinguishably from the review failing

## Context
- Owner: Mark C Allen
- Date: 2026-10-07
- Mode: Autonomous

## Scope
- In scope: classifying reviewer-Job failures by stage, carrying the
  classification to the user through events, exit codes, and stored run records.
- Out of scope: telling a pull request that its review failed. On the hosted
  path a failed review posts nothing, so the person who asked sees silence.
  Noted as a follow-up.

## Problem
Every failure rendered as one line of error text. A missing credential, a clone
that could not authenticate, a dead provider session, and a reviewer that read
the change and declined all looked identical, and all exited 1. Those call for
four different responses and only one of them is the user's to act on.

## Acceptance Criteria
- AC1: A failure names which part failed and why. ✅ `Bosun could not prepare the
  review: BOSUN_REPO and BOSUN_REF are required`, exit 10.
- AC2: The stage survives without events or logs. ✅ exit codes 10-14 land in the
  pod's terminated exitCode.
- AC3: Wrapping does not hide a timeout or cancellation. ✅ `Failure.Unwrap`,
  with a test, so a timed-out review is still reported as timed out.
- AC4: A reconnect to a finished failed run says why. ✅ the reason is stored on
  the record instead of "Stored review result".
- AC5: An unclassified error reaches the user unchanged. ✅

## Test Strategy
- Unit: stage-to-message and stage-to-exit-code mapping, sentinel preservation
  through `errors.Is`, stage recovery through `errors.As`, unclassified
  passthrough, stored-failure rendering, and that a successful run's wording is
  unchanged.
- E2E: ran `bosun reviewer` with no repository configured and confirmed the
  message and exit 10.

## Outcome
- Result: a failed review says whose failure it was.
- Evidence: `go test ./... -count=1`, plus the live exit-10 run above.

# Task: Document self-hosting, and what it costs

## Context
- Owner: Mark C Allen
- Date: 2026-10-07
- Mode: Autonomous
- Related: #4 (stale self-hosted deployment PR), #28, #29

## Scope
- In scope: an end-to-end GitHub App path on a cheap DigitalOcean cluster with
  real monthly costs, the same path through a tunnel to a local Kind cluster,
  and the chart change those guides need.
- Out of scope: the credential isolation in #4, which is a security change that
  deserves its own PR and verification rather than riding along with docs.

## Acceptance Criteria
- AC1: A reader can go from nothing to reviews on their own pull requests. ✅
- AC2: Monthly cost is stated with sources, including what is *not* included. ✅
  the model bill is called out as the one that scales.
- AC3: A free stable webhook URL is documented, not just paid ones. ✅
  Cloudflare Tunnel; tunnelto at $4 and ngrok at $10 are compared honestly.
- AC4: The guides state what is missing rather than implying completeness. ✅
  four known limits, two of them newly filed as issues.

## Notes on #4
Opened 2026-09-30 to bring the Go implementation to `main`; that landed instead
through #7, and sixteen PRs have merged since. Of its 75 files, four are not on
`main`: `internal/jobs/authenticated.go` (per-Job repository-scoped installation
tokens), `deploy/values-self-hosted.yaml`, `docs/self-hosted-github-app.md`, and
a readiness review dated 2026-09-29. This branch supersedes the deployment
values and the deployment guide with versions written against current `main`.
What remains worth salvaging is the credential isolation.

## Outcome
- Result: two guides, a values file, `ingress.annotations` in the chart, and two
  issues filed for limits the guides would otherwise have glossed over.
- Evidence: `helm lint`, `helm template` with the new values, relative-link
  check across both guides, `go test ./... -count=1`.

# Task: Give each reviewer Job only its own repository's credential

## Context
- Owner: Mark C Allen
- Date: 2026-10-08
- Mode: Autonomous
- Issue: https://github.com/everydaydevopsio/bosun/issues/31

## Problem
Every reviewer Job received `GITHUB_TOKEN`, `BOSUN_GITHUB_APP_ID` and
`BOSUN_GITHUB_PRIVATE_KEY` from one shared Secret. The private key mints tokens
for every repository the App is installed on, and the Job it sat in runs an AI
agent over pull request content -- attacker-supplied input the agent reads by
design. Model credentials were scoped per provider in #17; the GitHub
credential, which can write to repositories, was not.

## Acceptance Criteria
- AC1: The App private key never enters a reviewer Job. ✅ stripped by
  `useJobToken`, asserted by test.
- AC2: A Job receives a token for the single repository under review. ✅ the
  existing `installationToken` already scoped it; the change is minting it in
  the controller.
- AC3: The token Secret is owned by its Job. ✅ controller owner reference, so
  Kubernetes deletes the credential with the Job.
- AC4: A Job never starts before its credential exists. ✅ created suspended,
  released only after the Secret is created.
- AC5: Token expiry is handled deliberately. ✅ submission is refused when the
  token expires before the Job deadline, naming `review.timeoutSeconds`.
- AC6: Personal access token installs still work. ✅ the shared path is taken
  when no App is configured, with a test pinning it.

## Risks and Tradeoffs
- Risk: a Job left suspended would hold a concurrency slot forever. Mitigated by
  deleting it when the Secret cannot be created, with a test.
- Tradeoff: the controller gains `secrets: create, delete`. Deliberately not
  `get` or `list`, so it cannot read the namespace's credentials, and CI fails
  if that widens.

## Test Strategy
- Unit: App credentials absent from the Job, `GITHUB_TOKEN` pointing at the
  Job's own Secret, controller owner reference, unsuspension, refusal on a short
  token, cleanup on a failed mint, and the unchanged shared-secret path.
- Not covered: a real App-to-Kubernetes-to-pull-request run. The fake clientset
  does not prove GitHub accepts the minted token.

## Outcome
- Result: a review holds a credential for one repository that expires, instead
  of a key to every repository the App can see.
- Evidence: `go test ./... -count=1`, `helm lint`, the rendered RBAC guard.

# Task: bosun init — install the review skill for Claude Code and Codex

## Context
- Owner: Mark C Allen
- Date: 2026-10-08
- Mode: Autonomous
- Issue: https://github.com/everydaydevopsio/bosun/issues/23

## Scope
- In scope: `bosun init`, the embedded skill, target detection, idempotence.
- Out of scope: `--project` installs into a repository's `.claude/skills/`.
  That directory is Ballast-managed here via `.rulesrc.json` and a hand-added
  skill may be pruned on a config refresh; user-level is where the skill is
  wanted anyway, since its audience is every repository you review.

## Acceptance Criteria
- AC1: A released binary installs a working skill with no checkout. ✅ embedded.
- AC2: Re-running is idempotent and never discards a local edit. ✅
- AC3: The skill drives a real review end to end without hitting the agent's
  tool timeout. Verified by running it from both agents against this PR.
- AC4: The skill fails loudly against an older `bosun`. ✅ it checks `bosun help`
  for the flags it needs before running anything.

## Test Strategy
- Unit: detection with each combination of agent directories, install, re-install,
  edited-file refusal, `--force`, directory creation, `--print`, and a contract
  test pinning the CLI flags the skill tells an agent to use.
- End to end: review this PR from Claude Code and from Codex using the skill.

## Outcome
- Result: `bosun init` installs for both agents; the skill encodes detach-and-poll,
  the stdout/stderr split, and that findings are claims rather than test results.

# Task: Stop documentation pinning a release version

## Context
- Owner: Mark C Allen
- Date: 2026-10-09
- Mode: Autonomous

## Problem
`docs/self-hosted-digitalocean.md` pinned `--version 0.1.1`. The moment 0.1.2
shipped it was wrong, and silently: a reader following it installs the previous
release and nothing says so. `docs/setup.md` used `0.1.0` for the reader's own
image, which reads like a Bosun release number and ages the same way.

## Acceptance Criteria
- AC1: The install command resolves the version rather than carrying one. ✅ and
  the documented command was run: it resolves 0.1.2 and pulls a chart whose
  version and appVersion both match.
- AC2: Pinning is still the advice; only the hardcoding is gone. ✅
- AC3: Reintroducing a pin fails CI. ✅ a new docs job, verified in both
  directions locally.

## Outcome
- Result: the guides stay correct across releases instead of for one.
- Evidence: `helm show chart` against the resolved version; guard checked
  against the current tree and against a deliberately reintroduced pin.
