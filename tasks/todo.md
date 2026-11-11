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

# Task: Verify the upstream base for local PR reviews

## Context
- Owner: Codex
- Date: 2026-10-10
- Mode: Autonomous
- PRD Section: GO-2
- Requirement IDs: issue #34

## Scope
- In scope: exact upstream PR base selection and visible comparison commits.
- Out of scope: general PR-aware CLI input (#36).

## Acceptance Criteria
- AC1: A fork PR uses the upstream base, even when fork `main` diverges.
- AC2: A failed or changed base fetch stops submission.
- AC3: The CLI displays resolved base and merge base.

## Constraints
- Preserve offline checkout reviews and do not switch the user's branch.

## Risks and Tradeoffs
- Risk: a private upstream without working Git credentials fails closed.
- Tradeoff: the skill fetches the upstream branch before review.

## Execution Checklist
- [x] Regression tests fail on the prior implementation and pass with the fix.
- [x] Full tests, lint, and build pass; coverage is measured at 36.4%.

## Test Strategy
- Unit: skill contract and fork-layout snapshot test.
- Integration: skill fetch against a fork-shaped local remote.
- Failure-path tests: fetch failure and moved base SHA.
- Requirement-to-test mapping: AC1 to fork test; AC2 to skill fetch test; AC3 to snapshot metadata test.

## Rollback Strategy
- Trigger: verified PR base resolution prevents legitimate review.
- Rollback steps: revert this PR.
- Validation after rollback: previous checkout review remains available.

## Outcome
- Result: implementation complete; review and merge gates pending. Coverage is below the 75% framework requirement.
- Evidence links/commands: focused regression test red before implementation, then green; `make test`, `make lint`, `make build` pass; `make coverage` reports 36.4%.
- PRD updates: GO-2 exact comparison requirement.

# Task: Restore the local snapshot path contract

## Context
- Owner: Codex
- Date: 2026-10-13
- Mode: Autonomous
- Requirement IDs: [issue #41](https://github.com/everydaydevopsio/bosun/issues/41), PRD GO-2

## Scope
- In scope: `/repos` bridgectl allowance, pinned-image contract test, setup diagnosis, skill guidance.
- Out of scope: PR-aware submission and compact polling (#36), structured results (#37).

## Acceptance Criteria
- Local and hosted workspace paths are accepted and an outside path is denied by pinned bridgectl.
- A path-policy mismatch reports a nonretryable setup error naming the image and config.
- The installed skill allows committed branch review with unrelated dirty files.

## Execution Checklist
- [x] Reproduce the released-image rejection and failing config regression test.
- [x] Implement the path allowance, diagnosis, image contract check, and guidance.
- [x] Verify the contract with the released image plus candidate config.

## Test Strategy
- Unit: config and StartSession classification tests.
- Integration: `scripts/smoke-bridge-path.sh` with both config versions.
- Failure-path tests: unrelated InvalidArgument and paths outside `/repos`.

## Rollback Strategy
- Trigger: valid local sessions fail or path scope broadens unexpectedly.
- Rollback steps: revert this change and select the preceding image.
- Validation after rollback: provider session and path denial checks.

## Outcome
- Result: local implementation complete; CI and released-image verification pending.
- Evidence links/commands: regression test red before fix; pinned-image script red on released config and green with candidate config.

# Task: Tell the pull request when a review fails

## Context
- Owner: Mark C Allen
- Date: 2026-10-21
- Mode: Autonomous
- Requirement IDs: [issue #28](https://github.com/everydaydevopsio/bosun/issues/28)
- Plan: `plans/pr-review-surface.md` (Phases 1 and 4)

## Scope
- In scope: the `<sub>` ref/commit metadata line on every publication, a failure
  comment on terminal error, the one-terminal-publication invariant, docs.
- Out of scope: inline review threads (#53), structured findings (#37),
  capacity rejections raised in the controller before a Job exists.

## Acceptance Criteria
- A published review names the ref and resolved commit it reviewed. ✅
- A failed hosted review publishes exactly one failure comment carrying the
  redacted reason and the commit it tried to review. ✅
- A review that landed is never contradicted by a failure comment. ✅

## Execution Checklist
- [x] Metadata line and code-span escaping in `postReview`.
- [x] `postFailure`, published from the redacted `safeError` text on its own
      deadline so a timed-out review can still report.
- [x] Keep the webhook's commit through a failed clone.
- [x] Guard the one-terminal-publication invariant with `published`.
- [x] Document the output contract in `docs/github-app.md`.

## Test Strategy
- Unit: marker shape, metadata correlation, code-span escaping.
- Integration: hosted clone failure publishes one failure comment with no
  credential material; hosted success plus a failing output writer publishes
  only the review.
- Failure-path tests: both of the above are failure paths.
- Requirement-to-test mapping: `internal/review/publication_test.go`.

## Rollback Strategy
- Trigger: failure comments become noisy, or a consumer breaks on the metadata
  line.
- Rollback steps: revert the branch; the comment format is additive, so no
  state needs cleaning up.
- Validation after rollback: `make test`.

## Outcome
- Result: complete. Bosun 0.1.6 reviewed the change and found two defects
  (clone failure clobbering the commit; a review contradicted by a failure
  comment); both were reproduced with tests and fixed.
- Evidence links/commands: `make test`, `make lint`;
  job `review-bosun-issue-28-failure-comment-67498b0536`.

# Task: Publish findings as a real pull-request review

## Context
- Owner: Mark C Allen
- Date: 2026-10-21
- Mode: Autonomous
- Requirement IDs: [issue #53](https://github.com/everydaydevopsio/bosun/issues/53), PRD GO-4
- Plan: `plans/pr-review-surface.md` (Phases 2, 3 and 5)

## Scope
- In scope: the findings output contract, its parser, publishing a review with
  inline comments, the diff filter, the 422 fallback, docs and PRD.
- Out of scope: linking repeated passes into one campaign (#42), capacity
  rejections raised before a Job exists, Checks API output, registering the
  GitHub App (Phase 0).

## Acceptance Criteria
- Anchorable findings become inline review comments on the reviewed commit. ✅
- A finding that cannot be anchored is published as text, never dropped. ✅
- A review rejected for an invalid position republishes body-only. ✅
- Output with no findings block still publishes. ✅

## Execution Checklist
- [x] Output contract in `prompts/code-review.md`, before the status line.
- [x] `parseFindings`: last fenced block, validation, bounds, severity order,
      absence tolerated.
- [x] Reviews API publication with `commit_id`, `prFiles` filtering, folded
      findings, and the 422 retry.
- [x] Render findings as text for the terminal and progress events.
- [x] Docs: `README.md`, `docs/setup.md`, `docs/github-app.md`, `PRD.md`.

## Test Strategy
- Unit: parser table tests (well-formed, absent, malformed, unrelated block,
  invalid entries, bounds, ordering).
- Integration: review payload shape, outside-the-diff folding, 422 fallback,
  repeated rejection, comment fallback, unavailable file list, local rendering.
- Failure-path tests: five of the seven publication tests are failure paths.
- Requirement-to-test mapping: `internal/review/findings_test.go`,
  `internal/review/review_publication_test.go`.

## Rollback Strategy
- Trigger: reviews are rejected in production, or anchors land on wrong lines.
- Rollback steps: revert the branch; published reviews are inert history.
- Validation after rollback: `make test` plus one real pull-request review.

## Outcome
- Result: complete. Bosun 0.1.6 reviewed the change and found one high-severity
  defect -- a missing `commit_id` letting a mid-review push reanchor findings;
  reproduced with assertions on both payloads, then fixed.
- Evidence links/commands: `make test`, `make lint`;
  job `review-bosun-issue-53-pull-request-review-4d09f1fd78`.
- Follow-up: ballast's `github-pr-review-cycle` polls `issues/comments` to
  decide a review has settled and must also poll `pulls/{n}/reviews`, or it
  will time out against the new surface. Not tracked yet.

# Task: Make a completed review structured and revision-aware

## Context
- Owner: Mark C Allen
- Date: 2026-10-21
- Mode: Autonomous
- Requirement IDs: [issue #37](https://github.com/everydaydevopsio/bosun/issues/37)

## Scope
- In scope: durable structured findings, revision provenance, the
  `review-result` command, `--against-ref` comparison, skill and CLI docs.
- Out of scope: the compact polling interface (#36) this should eventually feed,
  cross-pass campaigns (#42), and fork base resolution (#34).

## Acceptance Criteria
- AC1: A completed review is retrievable as one JSON document with outcome,
  reviewed head, merge base, structured findings, and the original Markdown,
  after Kubernetes logs expire. ✅
- AC2: A finding has a stable ID, severity, path, line range, remediation,
  confidence, and stated uncertainty; no-findings and not-reported are
  distinct. ✅
- AC3: Human output leads with a header and retains the full report. ✅
- AC4: Comparing against a local ref warns when it differs, without claiming a
  finding was fixed or re-running anything. ✅
- AC5: Unreadable structured output falls back to the Markdown with a
  diagnostic. ✅

## Constraints
- Records written before provenance existed must report `unknown`, never a SHA
  parsed out of the report text.
- The structured payload is stored verbatim, so a later schema change cannot
  silently rewrite history.

## Risks and Tradeoffs
- Tradeoff: findings travel as a new `findings` event rather than inside the
  result text, so the human result stays readable and the structured half stays
  machine-readable. Cost is one more event type for consumers to ignore.

## Execution Checklist
- [x] Stable finding IDs, confidence, and uncertainty in the contract.
- [x] `StructuredResult` plus the `findings` event; monitor persists it.
- [x] Revision provenance recorded at submit time.
- [x] `bosun review-result` with `--json`, `--summary`, `--against-ref`.
- [x] Skill bumped to version 2 with triage guidance; CLI docs updated.

## Test Strategy
- Unit: parser IDs and hedge fields; `StructuredResult` across its three cases.
- Integration: event stream carries the payload; `review-result` over stored
  records for findings, clean, unstructured, unreadable, and failed reviews.
- Failure-path tests: moved branch, unresolvable ref, reviewed commit absent,
  records predating provenance, dirty-review caveat.
- Requirement-to-test mapping: `internal/localreview/result_test.go`,
  `internal/review/findings_test.go`.

## Rollback Strategy
- Trigger: the new event or command misreports a review.
- Rollback steps: revert the branch; stored records remain readable because the
  added fields are optional.
- Validation after rollback: `make test`.

## Outcome
- Result: delivery steps 1 and 2 of the issue complete. Step 3, feeding the
  structured result into the compact status interface, waits on #36.
- Evidence links/commands: `make test`, `make lint`.


# Task: Measure review usage for cost comparison

## Context
- Date: 2026-10-27
- Mode: Autonomous
- Request: compare Bosun with Copilot and capture usage for metrics.
- Plan: `plans/plan-review-usage.md`.

## Scope
- In scope: numeric provider usage, outcome/timing logs, durable local records,
  correlation, failure handling, and a source-linked cost-comparison guide.
- Out of scope: production deployment, automatic repricing, billing credentials,
  dashboards, and paid benchmark runs.

## Acceptance Criteria
- AC1: Each handled worker exit emits one structured measurement summary.
- AC2: Provider usage is separate from review prose, secrets, and tool payloads.
- AC3: Missing and partial usage never imply zero spend; estimates are not invoices.
- AC4: Local usage survives log expiry and is exposed through `review-result --json`.
- AC5: Pricing and a matched-commit quality benchmark are documented.

## Execution Checklist
- [x] Verify official pricing and inspect current usage paths.
- [x] Reproduce missing summaries, missing durable usage, and missing Job identity with failing tests.
- [x] Capture Codex/Claude usage and handle malformed, failed, duplicate, and zeroed crash results.
- [x] Emit structured summaries and retain them in local result records.
- [x] Add sourced comparison and operational instructions.
- [x] Run offline regression checks; record the unavailable live-review gate below.

## Test Strategy
- Go: full tests with coverage; worker outcome/redaction, bridge replay/failure,
  Job metadata, local monitoring/storage, and old-record compatibility.
- Node: 24 adapter cases, including multi-turn counts, Claude helper totals,
  unknown pricing, zeroed crashes, malformed streams, and reserved-marker escaping.
- TDD: new regressions failed before implementation, then passed.

## Rollback Strategy
- Revert this PR and restore the preceding worker image. Added local record fields
  are optional and older readers ignore them.

## Outcome
- Passed: `env -u CODEX_HOME /tmp/go/bin/go test ./... -cover -count=1`,
  `node --test scripts/claude-review-adapter.test.js scripts/codex-review-adapter.test.js`,
  `go vet ./...`, a CLI build and JSON-log smoke check, formatting, and `git diff --check`.
- No actual model request or Kubernetes deployment was performed.
- Merge gate: AGENTS.md requires a Bosun review using a distinct provider. Docker,
  Kubernetes tooling, and a configured distinct reviewer are unavailable here.
  Keep the PR draft and run that review on its exact committed head before merge.

Follow-up (2026-11-03), from reviewing this branch against the merged main:
- `usageRecorder.filter` judged each payload independently, so a usage record
  split across two bridge payloads lost its first half to a failed parse and
  published the second half as review prose -- provider JSON in the review, usage
  reported missing. The bridge's own newline injection made the split permanent.
  A trailing fragment that could still become the marker now waits for the rest
  of its line; `flushPartial` judges it at end of stream, and the whole-output
  pass holds nothing back so the two passes cannot leak into each other.
- Anthropic list prices were absent even though `claude-bosun` is the provider
  whose per-model usage is captured in most detail. Added, verified 2026-11-03,
  with the non-uniform cache-read multiplier (0.025x / 0.05x / 0.10x) called out.
- The four OpenAI model names and every rate in that table were re-verified
  against the live pricing page and are correct as published.

# Task: Review command permissions

## Acceptance Criteria
- Claude grants cover review commands only; Codex rules require explicit setup.
- Repeat installation preserves local edits and honors CODEX_HOME via targets.

## Execution Checklist
- [x] Implement grants, installer, and documentation.
- [x] Add installer tests for repeated setup, preservation, and forced replacement.
- [x] Run targeted Go tests and record unavailable independent Bosun review.

## Outcome
Implementation complete. Targeted Go tests and `git diff --check` passed.
Independent Bosun review blocked: no installed Bosun binary or configured
distinct reviewer in this environment.

Follow-up (2026-11-03): the first Claude grants used a space-separated list of
`Bash(bosun review *)`, which is not the host's `Bash(<prefix>:*)` form and
would have matched nothing, and the skill still launched through
`${BASE:+--base "$BASE"}` and `bosun help | grep ... || echo ...`, which its own
prose forbids. Both are fixed and covered by tests that failed first. Codex rule
loading is now verified empirically against codex-cli 0.149.0.

Running the skill end to end then surfaced two more, both fixed with failing
tests first:
- Base verification compared the fetched branch *tip* to `baseRefOid`, which is
  the merge base and stays put as the base branch advances. It rejected this
  repository's own PR. It now checks containment.
- The version check told agents to look for `--local-credentials` in `bosun
  help`, but only `bosun up` advertised it, so the check passed on any binary
  with an `up` command. The review entry now lists `--detach` and
  `--local-credentials`.

Live verification: `bosun help`, `bosun review`, and `bosun review-status` all
ran unprompted under the installed v3 skill. Bosun review of afa8c129 with
`--provider codex-bosun` completed with no material findings.

Bosun review of 16323c1 (`--provider codex-bosun`) returned one high finding,
confirmed against the code and fixed:
- Buffering an unterminated fragment that merely looked like the marker was
  wrong, because bridgectl strips newlines (measured and documented in
  `internal/review/findings.go`). Both adapters emit the record first, so the
  record was buffered, the review was concatenated onto it and buffered too, and
  the combined text failed to decode at exit -- `runSession` reported "agent
  session produced no output" and the whole review was lost. `scan` now consumes
  a complete record on sight via a JSON decoder, keeps any text that followed it
  on the same payload, and buffers only a genuinely truncated record. The
  reviewer's suggested `runSession` regression with newline-free payloads is
  added and failed first for both providers.

Bosun review pass 2 (7c44105) returned two findings, both confirmed and fixed:
- Medium: `settled()` flushes the held fragment destructively, and `runSession`
  called it once for the emptiness check and again for the return value, so the
  fragment never reached the published review. Taken once and reused.
- Low: the worker logs the usage summary to stderr as well as emitting the
  progress event. That log is not a progress event, so the monitor printed the
  whole telemetry envelope through its diagnostic fallback -- the existing test
  fed only progress events and missed it. The monitor now suppresses logs whose
  `event` is `review.UsageLogEvent`, a constant now shared by producer and
  consumer so the two cannot drift. The monitor test feeds both representations.


# Task: Summary comment beside a findings review

## Context
- Date: 2026-11-03
- Mode: Autonomous
- Plan: `plans/plan-review-summary-comment.md`.

## Acceptance Criteria
- AC1: A findings review posts exactly one summary comment; a no-findings review
  still posts exactly one comment and no review.
- AC2: The summary carries the review marker and the reviewed ref and commit.
- AC3: The summary states the count and severities and does not repeat detail.
- AC4: A failed summary does not fail a review that already published.

## Execution Checklist
- [x] Reproduce the gap with failing tests, including the 422 fallback path.
- [x] Implement postSummary on both review paths; keep failure non-fatal.
- [x] Record the terminal-vs-pointer distinction in PRD GO-4 and criterion 6.
- [x] Document both surfaces and how to query them in docs/github-app.md.

## Test Strategy
- Go: summary content and discoverability, exactly-one on the 422 fallback,
  non-fatal summary failure, and the unchanged no-findings path.
- Requirement mapping: PRD GO-4 and acceptance criterion 6.

## Rollback Strategy
- Revert this PR. The summary is additive; removing it restores the previous
  surfaces without touching stored state.

## Outcome
- Result: implemented; full suite, vet, lint, and diff checks pass.
- Evidence: `env -u CODEX_HOME go test ./...`, `make lint`.

Bosun review of this branch found three issues across two passes, all confirmed
and fixed:
- Medium: the summary said every finding is a thread on Files changed. False
  after the 422 fallback and for findings outside the diff. It now links the
  published review and describes both placements.
- Low: the severity ranking duplicated `findings.go` and omitted `blocker`, so
  blockers sorted last. It reuses the one `severities` map.
- Medium: decoding the create-review response made publication depend on reading
  the body, so a truncated reply after a 2xx failed a review GitHub had created
  and Worker.Run would add a contradictory failure comment. Decode failures are
  now a distinct error that only this caller ignores.

# Task: Report reviewer terminations the reviewer cannot report

## Context
- Owner: Mark C Allen
- Date: 2026-11-03
- Mode: Autonomous
- Issue: #62
- Plan: `plans/plan-oom-termination-report.md`

## Scope
- In scope: publishing a failure comment for a reviewer Job killed for
  exceeding `review.resources.limits.memory`, without breaking the
  one-terminal-publication invariant.
- Out of scope: Jobs killed by `activeDeadlineSeconds` (#72); correlating a
  publication to the attempt that made it rather than to the commit (#74);
  reviews with no pull request to publish to (#65); the memory limit's default
  value (#60).

## Acceptance Criteria
- AC1: a reviewer Job killed for exceeding its memory limit results in exactly
  one publication on the pull request. ✅ `TestAnOOMKilledReviewProducesExactlyOnePublication`
- AC2: that publication names memory as the cause and points at
  `review.resources.limits.memory`. ✅ `TestTerminationCommentNamesMemoryAndTheChartValue`
- AC3: the invariant in `docs/github-app.md` still holds -- never both a review
  and a failure comment. ✅ `TestTerminationIsSilentWhenBosunAlreadyPublished`

## Constraints
- The controller must keep holding nothing per-review: it mints a narrow
  repository token on demand rather than retaining the Job's.
- No new reads of Secrets. The added RBAC is `pods: get, list, watch` only.

## Risks and Tradeoffs
- Risk: acting on every failed Job would double-publish over the reviewer's own
  failure comment. Mitigated by classifying only on `OOMKilled`, a reason the
  reviewer provably cannot survive.
- Risk: a controller restart re-reporting everything inside the Job TTL.
  Mitigated by the `bosun/termination-reported` annotation.
- Risk: two replicas racing between the remote check and the post. Mitigated by
  the `bosun-termination-sentinel` Lease, separate from admission so a webhook
  delivery never queues behind a GitHub call.
- Tradeoff: PAT installs must share the token with the controller to get a
  comment. Documented, optional, and it logs rather than failing silently.

## Execution Checklist
- [x] `internal/review` termination publisher with the existing-publication check
- [x] `internal/sentinel` reconcile loop with OOM classification
- [x] Lease seam so replicas cannot both publish
- [x] Wire into `bosun serve`; chart RBAC, values, deployment env
- [x] Chart/config drift guard for the new environment variable
- [x] Docs: `docs/github-app.md` publication section
- [x] Promote the `activeDeadlineSeconds` gap to an issue (#72)

## Test Strategy
- Unit: `internal/review/termination_test.go` over an httptest GitHub; table
  cases for each surface an existing publication can live on.
- Unit: `internal/sentinel/sentinel_test.go` over a fake clientset; table cases
  for every termination the reviewer reports itself.
- Integration: `TestTheSentinelReadsAJobBuiltBySubmission` drives `jobs.Submit`
  so the environment contract between submission and the sentinel cannot drift.
- End-to-end: `TestAnOOMKilledReviewProducesExactlyOnePublication` runs the real
  publisher and reconciles three times.
- Failure paths: a rejected POST, a lost lease, and a missing pull request each
  have a case; each asserts the Job is left unrecorded so the work is retried.
- Both guards were verified load-bearing by disabling each in turn and
  confirming the end-to-end test fails.

## Rollback Strategy
- Trigger: duplicate or incorrect failure comments on pull requests.
- Rollback: set `review.terminationSentinel.intervalSeconds: 0` and upgrade.
  The controller logs terminations instead of publishing them; no other
  behaviour changes.
- Validation after rollback: the controller logs
  "termination sentinel disabled" at startup and posts no further comments.

## Bosun review findings (pass 1, 3 findings)
- [x] **High** — comment-triggered Jobs record `BOSUN_REF=pr-N` and no commit,
  so the sentinel could neither name the reviewed commit nor recognise the
  reviewer's own publication, breaking AC3 for that trigger. Proven against the
  real Job that reviewed this PR. Fixed by resolving the identity at submission
  (`resolveIdentity`), stopping the reviewer re-resolving and drifting from it,
  and refusing to report a termination with no commit.
- [x] **Medium** — `publishedFor` matched a marker anywhere in a body, so a
  GitHub "Quote reply" suppressed the report. Fixed: a publication must open
  with the marker.
- [x] **Medium** — exhausting the page bound was treated as proof of absence.
  Fixed: it returns a retryable error instead.

## Bosun review findings (pass 2, 2 findings)
- [x] **High** — `publishedFor` correlated by commit alone, so an earlier
  attempt's failure comment suppressed a retry's OOM report. Fixed: a
  publication only counts if it was made after this review started, taken from
  the Job's `status.startTime` less a clock-skew margin.
- [x] **Medium** — `underLease` bound the operation to the 35s acquisition
  window, so the sentinel's 60s publication budget could never be honoured and
  shrank to whatever the wait left. Fixed: the wait and the operation budget
  are separate, with caller cancellation still propagated. `jobs.AdmissionBudget`
  now ties the webhook server's write timeout to the real worst case.

## Bosun review findings (pass 3, 2 findings)
- [x] **Medium** — resolving a comment-triggered ref moved the Job name, which
  is the delivery's deduplication key, so a redelivery after a failed
  resolution would create a second Job and a second review. A regression
  introduced by the pass-1 fix. Fixed: `Request.NameRef()` names a Job after
  the ref the trigger carried, not the resolved one.
- [x] **Medium** — publications correlate to a commit, not to the attempt that
  made them, so overlapping attempts and retries inside the clock-skew margin
  can suppress each other's reports. Promoted to #74: closing it needs a run
  identifier in every publication, which changes the documented metadata-line
  interface and the review skill that parses it. The time bound added in pass 2
  closes the common case.

## Bosun review findings (pass 4, 2 findings)
- [x] **Medium** — identity resolution ran before admission with no bound, and
  each GitHub call carries its own 30s client timeout: 90s of resolution plus
  40s of admission against a 60s write timeout, so a slow GitHub cost the
  delivery its acknowledgement while its Job was created anyway. Another
  regression from the pass-1 fix. Fixed: a 10s `identityResolveBudget`, with
  the server's write timeout now checked against resolution plus admission.
- [ ] **Medium** — publications correlate to a commit, not to the attempt. Same
  finding as pass 3; tracked in #74 and deliberately not fixed here.

## Outcome
- Result: implemented on `issue-62-report-oom-terminations`.
- Evidence: `go test ./...`, `make lint`, `helm template`.
- Remaining: a live review forced past its memory limit on a real cluster has
  not been run; the manual verification in the plan is still open.

# Task: Report a reviewer killed by its Job deadline

## Context
- Owner: Mark C Allen
- Date: 2026-11-03
- Mode: Autonomous
- Issue: #72
- Plan: `plans/plan-deadline-termination-report.md`

## Scope
- In scope: classifying a Job killed by `activeDeadlineSeconds` as a
  termination the reviewer could not report, and publishing it.
- Out of scope: attempt-level publication identity (#74); the value of
  `review.timeoutSeconds` or the startup allowance.

## Acceptance Criteria
- AC1: a Job killed by `activeDeadlineSeconds` results in exactly one
  publication. ✅ `TestADeadlineKilledReviewerIsReported`
- AC2: it names the timeout and points at `review.timeoutSeconds`.
  ✅ `TestDeadlineCauseNamesTheReviewTimeout`
- AC3: a reviewer that reported its own timeout first is not reported again.
  ✅ `TestADeadlineAfterTheReviewerPublishedIsNotReportedAgain`
- AC4: `docs/github-app.md` drops this from "What this does not do yet". ✅

## Risks and Tradeoffs
- Risk: reporting a failure the reviewer already published. Mitigated by
  classifying on the Job condition rather than the container reason -- a
  graceful non-zero exit is `BackoffLimitExceeded`, never `DeadlineExceeded` --
  and by the existing publish-once guard as a backstop.
- Tradeoff: a reviewer both out of memory and out of time is reported as out of
  memory only. That is the cause an operator can act on.

## Execution Checklist
- [x] `DeadlineExceededCause` naming `review.timeoutSeconds`
- [x] Job-condition branch in `Sentinel.cause`, ordered after `OOMKilled`
- [x] Table case per Job failure reason, including the ones to leave alone
- [x] Recover around per-Job inspection so one bad Job cannot kill the
      controller the webhook server shares a process with
- [x] Docs

## Test Strategy
- Unit: table cases over Job failure reasons; precedence when both causes hold.
- Unit: the reviewer-already-published case, driving the real guard.
- Failure path: a Job whose name is not a valid label selector, asserting the
  reconcile reports it instead of panicking the process.

## Rollback Strategy
- Trigger: duplicate comments on timed-out reviews.
- Rollback: `review.terminationSentinel.intervalSeconds: 0` disables all
  termination reporting, memory included.
- Validation after rollback: the controller logs "termination sentinel
  disabled" at startup.

## Outcome
- Result: implemented on `issue-72-report-deadline-terminations`.
- Evidence: `go test ./...`, `make lint`, `go vet ./...`.
- Remaining: a live review forced past its Job deadline on a real cluster has
  not been run.

# Task: Give branch-only reviews a home on the commit (#65)

## Context
- Owner: mark
- Date: 2026-11-04
- Mode: Approval-Required
- Requirement IDs: #65

## Scope
- In scope: publishing a `branch-created` review as a check run on the reviewed
  commit; resolving a pull request late so one opened mid-review still gets the
  normal inline review; a WARN when neither surface is available.
- Out of scope: persisting a review for replay (no store exists, and the
  controller deliberately cannot read Secrets); reporting a *killed* branch
  review as a check run (promoted, see Outcome).

## Acceptance Criteria
- AC1: a branch pushed without a pull request has its findings reach the pull
  request opened on that commit later, with no second review running.
- AC2: a review that genuinely cannot be published logs WARN naming repo, ref,
  SHA, and reason.
- AC3: published pull-request output keeps its current shape.
- AC4: regression coverage for the `BOSUN_PR_NUMBER=0` path.

## Constraints
- Checks is an optional App permission. Requesting a permission an installation
  does not hold makes GitHub reject the whole token mint with 422, so the grant
  must be read from the installation before it is asked for.
- A check run must never conclude `failure`: on a repository that requires
  checks, that would block a merge on an opinion nobody opted into.

## Risks and Tradeoffs
- Risk: an install that grants Checks starts seeing a new check on commits.
  Mitigated by `neutral`/`success` conclusions only.
- Tradeoff: a check run carries annotations rather than review threads. At
  branch-created time there is no diff view to anchor threads to, so this is
  the correct shape rather than a lesser one.

## Execution Checklist
- [x] Failing tests for the check-run publication, the cap invariant, and the WARN
- [x] Installation permission detection; `checks: write` requested only when granted
- [x] Check-run publisher with findings as annotations
- [x] Late pull-request resolution before falling back
- [x] End-to-end regression tests for `BOSUN_PR_NUMBER=0`
- [x] Docs: `docs/github-app.md`, `README.md`

## Test Strategy
- Unit: check-run body shape, conclusion per clean/findings, marker and
  metadata parity with a comment, annotation level per severity.
- Unit: `tokenPermissions` across granted / not granted / read-only.
- Unit: `associatedPR` for open and closed pull requests.
- Invariant: `findingLimit <= checkRunAnnotationLimit`, so no finding is ever
  silently dropped from a check run.
- Failure path: a 403 from the check-run write is reported as Checks being
  unavailable and does not fail a completed review.
- End-to-end: `Worker.Run` with `BOSUN_PR_NUMBER=0` for all three outcomes.

## Rollback Strategy
- Trigger: unwanted checks appearing on commits.
- Rollback: revoke the Checks permission on the App installation. The token
  stops requesting it and the review falls back to the WARN; no Bosun change
  or redeploy is needed.
- Validation after rollback: "review completed but was not published" at WARN.

## Outcome
- Result: implemented on `issue-65-branch-review-check-run`.
- Evidence: `go test ./...`, `gofmt -l .`, `go vet ./...`.
- Remaining: a reviewer *killed* before publishing a branch review still has
  nowhere to report. Promoted to #76 rather than widened into this change,
  because the sentinel would need to mint a Checks-scoped token for a
  repository whose installation may not grant it.

## Addendum 2026-11-04: branch review defaults off

The check run gave a branch review a home, but the workflow actually wanted is
"review on pull-request open, then on every push". That already worked; what
broke it was `branch-created` firing first.

- `submitLocked` rejects over `MaxConcurrentReviews` with `CapacityError`,
  which `server.go:56` answers with HTTP 503. GitHub does not redeliver a
  failed webhook, so that review is lost, not retried.
- `Active()` counts Jobs across the namespace with no repository filter, so the
  three slots are global.
- A duplicate branch review therefore holds a global slot for minutes and can
  starve the `opened` review of the pull request it duplicates.

So `review.triggers.branchCreated` now defaults false. The check-run work stays
for installs that opt in.

- [x] `FromWebhook` gates the `create` trigger
- [x] `BOSUN_REVIEW_BRANCH_CREATED`, parsed fail-safe to off
- [x] Chart value, default false, with a CI guard on the rendered default
- [x] Tests: default off, pull-request triggers unaffected, chart default pinned
- [x] README and `docs/github-app.md`

Not addressed: nothing supersedes an in-flight review when a newer push
arrives, so rapid pushes still queue and can exhaust the cap. #42 touches it.

# Task: Default the review comment trigger to `@bosun-review review` (#80)

## Context
- Owner: Mark C Allen
- Date: 2026-11-04
- Mode: Autonomous
- Requirement IDs: #80

## Scope
- In scope: the `BOSUN_REVIEW_COMMAND` default, a `review.command` chart value,
  docs, and test fixtures.
- Out of scope: accepting several phrases at once; a `review_requested`
  trigger for requesting `bosun-review` as a reviewer.

## Acceptance Criteria
- AC1: with no override, `@bosun-review review` on a PR starts a review.
- AC2: `review.command` renders `BOSUN_REVIEW_COMMAND`, so an install can keep
  `@bridgectl review`.

## Execution Checklist
- [x] Failing tests: config default, chart renders and defaults the value
- [x] Default changed in `config.Load`; chart value and env added
- [x] README, `docs/setup.md`, and every doc example updated
- [ ] Post-merge: release, deploy to mca-cluster, verify with a live comment (evidence goes on #80, which closes then)

## Test Strategy
- Unit: `TestTheDefaultReviewCommandMentionsTheBosunReviewAccount`,
  `TestTheChartRendersTheReviewCommand`.
- E2E: on mca-cluster, an auto review on a new PR, then a second review from a
  `@bosun-review review` comment.

## Rollback Strategy
- Trigger: comment reviews stop starting.
- Rollback steps: set `review.command: "@bridgectl review"` in the gitops
  values, or pin the previous release.

## Outcome
- Result: see PR for #80.

# Task: No-build quick start for local reviews (#50)

## Context
- Owner: Mark C Allen
- Date: 2026-11-04
- Mode: Autonomous
- Requirement IDs: #50

## Scope
- In scope: a quick start at the top of `docs/local-development.md`, and the
  README link to it.
- Out of scope: Docker Hub publishing (the issue notes GHCR is the only
  registry).

## Acceptance Criteria
- AC1: the first screenful runs a local review without cloning this repo.
- AC2: the quick start names only the tools it uses (Docker, Kind, a signed-in
  `codex` or `claude`, Homebrew).
- AC3: contributor setup follows, unchanged.
- AC4: the reviewer image and its registry are named.

## Execution Checklist
- [x] Quick start section with prerequisites, install, run, image, `bosun doctor`
- [x] "Developing Bosun itself" divider above the existing contributor material
- [x] README points at the quick start anchor

## Outcome
- Result: docs only; existing sections unchanged.

# Task: Publish every review on one surface

## Context
- Owner: Mark C Allen
- Date: 2026-11-09
- Mode: Autonomous
- Plan: plans/plan-single-review-surface.md

## Scope
- In scope: clean reviews publish as pull-request reviews; drop the pointer
  comment beside findings reviews; PRD GO-4 and docs.
- Out of scope: failure comments (unchanged, still issue comments).

## Acceptance Criteria
- AC1: A review with no findings publishes as one `COMMENT` review anchored to
  the reviewed commit, with no issue comment. ✅
- AC2: A findings review, direct or via the 422 fallback, posts no comment
  beside itself. ✅

## Execution Checklist
- [x] Failing tests for AC1 and AC2
- [x] Implement in `postReview`; remove `postSummary` and helpers
- [x] Update dependent tests, PRD, docs, plan

## Test Strategy
- Unit: `TestReviewWithoutFindingsPublishesAsAReview`,
  `TestFindingsReviewPostsNoComment` (direct and 422 fallback),
  `TestUnreadableResponseDoesNotFailAPublishedReview`.
- Integration: `TestWebhookToPinnedCheckoutAndPublishedReview` now expects the
  clean review on `/pulls/7/reviews`.
- Failure-path tests: 422 fallback, repeated rejection, truncated reply.

## Outcome
- Result: failing first (`comments=1 reviews=0, want 0 and 1`; `posted 1
  comments beside the review, want 0`), then `go test ./...` green.

# Task: Queue reviews at capacity instead of refusing them (#82)

## Context
- Owner: Mark C Allen
- Date: 2026-11-10
- Mode: Autonomous
- Plan: plans/plan-review-queue.md

## Scope
- In scope: queue deliveries that arrive at `maxConcurrent`; dispatcher; log
  capacity refusals; chart values and docs.
- Also in scope (added): replicas 2 + preStop sleep; #82 option 3, the
  missed-review sweep.
- Out of scope: #82 option 2 (check run on receipt) — remains on #82.

## Acceptance Criteria
- AC1: A delivery at capacity is accepted and queued as a suspended Job, not refused. ✅
- AC2: Queued reviews start oldest-first as slots free; running pods never exceed `maxConcurrent`. ✅
- AC3: A queued review gets its repository token when it starts. ✅
- AC4: A full queue refuses with 503 and logs at WARN. ✅
- AC5: An open, non-draft PR whose head has no Bosun publication and no Job gets a review from the sweep. ✅
- AC6: A rollout keeps a ready controller; terminating pods pause before draining. ✅

## Execution Checklist
- [x] `internal/jobs/queue.go` with admit + Dispatcher
- [x] Config, controller wiring, server log
- [x] Chart values, README, docs
- [x] Live verification after release: sweep recovered ballast#422; ballast#421 queued 44s then started (2026-11-10, 0.3.3/0.3.4 on mca-cluster). Graduated to ADR-002.

## Test Strategy
- Unit: `internal/jobs/queue_test.go` (8 tests covering AC1–AC4, supersede,
  start-failure isolation, expiry, queue disabled); `internal/sweep/sweep_test.go`
  (AC5, skips, dedupe across passes/replicas, once per commit, per-pass bound,
  unknown publication state); `internal/review/scan_test.go` (SHA-only match,
  quoted replies ignored).
- CI: "Deliveries are not dropped by default" pins AC6 and the queue/sweep defaults.
- Failure-path tests: token mint failure, full queue, expiry.
- Note: tests were written alongside the implementation, not strictly first.

## Outcome
- Result: `go test ./...` green; `golangci-lint run --new-from-rev=main` 0 issues; `helm lint` ok.

# Task: Fix the 0.3.3 queue and sweep findings missed before merge (#82)

## Context
- Owner: Mark C Allen
- Date: 2026-11-10
- Mode: Autonomous
- Plan: plans/plan-review-queue.md (0.3.4 follow-up)

## Acceptance Criteria
- AC1: An older request admitted after a newer one for the same PR is dropped and does not delete the newer head's queued review. ✅
- AC2: A newer request still supersedes an older queued one. ✅
- AC3: One PR whose publications cannot be searched does not stop the sweep from considering the rest of its repository. ✅
- AC4 (Bosun review of #89): a sweep request is dated when it read the head, so a webhook for a newer head received mid-pass is not dropped. ✅ (`TestASweepRequestIsDatedWhenItReadTheHead`, failing first with "superseded by newer")

## Execution Checklist
- [x] Failing tests first (confirmed failing: `err = <nil>, want … superseded`; `jobs = 0, want one review`)
- [x] `bosun/requested-at` ordering; `SupersededError`; server and sweep stamp `ReceivedAt`
- [x] Sweep retains the per-PR error and continues

## Test Strategy
- Unit: `TestAnOlderRequestAdmittedLastDoesNotReplaceTheNewerOne`,
  `TestANewerRequestStillSupersedesByReceiptTime`,
  `TestOneUnsearchablePullRequestDoesNotHideTheRest`.

## Outcome
- Result: `go test ./...` green; `golangci-lint run --new-from-rev=main` 0 issues.

# Task: Make review depth visible in results (#93)

## Context
- Owner: Mark C Allen
- Date: 2026-11-11
- Mode: Autonomous
- Requirement IDs: #93 proposals 1, 2, 4; #22 overlap; proposal 3 split to #95

## Scope
- In scope: residual risks in the findings contract, diff stats, measured read coverage from both adapters, coverage in `review-result` and hosted reviews, skill guidance, change-type prompt guidance.
- Out of scope: `--effort` and chunked review (#95).

## Acceptance Criteria
- AC1: An empty-findings result carries `coverage` (diff size with generated files separate, files opened vs changed, full-diff flag, context files) and `residual_risks`.
- AC2: Residual risks are change-specific; the prompt forbids generic "tests were not run" boilerplate.
- AC3: Coverage is reported even when the findings line is missing; read paths never carry file content.
- AC4: The bosun-review skill triages `findings: []` from coverage and residual risks, not elapsed time or tokens.

## Execution Checklist
- [ ] Failing tests written and confirmed failing
- [ ] Adapters track reads and emit `BOSUN_READS_V1`
- [ ] Worker computes diff stats and coverage, emits `coverage` event, hosted footer
- [ ] `review-result` shows coverage and residual risks
- [ ] Prompt, skill, docs updated
- [ ] `go test ./...`, `node --test`, `make lint` green

## Test Strategy
- Unit: JS tracker command parsing; Go parse of residual risks, diff stats on a temp repo, recorder stripping, coverage join, result document.
- Failure-path tests: no base resolvable, reads line missing, malformed/oversized reads line, impersonated marker in review text, paths outside the workspace.

## Outcome
- Result:
- Evidence links/commands:
