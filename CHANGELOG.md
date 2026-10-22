# Changelog

All notable changes to this project are documented in this file.

Entries below `## [Unreleased]` are added automatically by the release workflow
(`.github/workflows/publish.yml`), which generates them from the commits since
the previous tag. Do not hand-edit released sections.

## [Unreleased]

- Build and release pipeline: CI lint/test/coverage gates, a reusable release
  validation workflow, semver bump-and-tag releases, GHCR image and Helm chart
  publishing, and signed and notarized macOS CLI archives distributed through
  the `everydaydevopsio/homebrew-bosun` tap.

## [0.2.3] - 2026-10-22

### Highlights

- Reviewer Jobs now support resource requests and limits, giving you control over their resource allocation. (#60)

### Fixes

- Review findings now use a transport-safe marker to prevent them from being lost during delivery. (#61)
- Invalid resource quantities are now rejected rather than silently ignored. (#60)

### Changes

- Added tests to catch environment variable naming mismatches between the Helm chart and application configuration. (#60)

**Full changelog:** [v0.2.2...v0.2.3](../../compare/v0.2.2...v0.2.3)

## [0.2.2] - 2026-10-22

### Highlights
- Completed the hosted review workflow by addressing job-patching permissions and Codex sandbox support. (#59)

### Fixes
- Corrected RBAC permissions to allow hosted review jobs to be patched.

### Changes
- Updated Codex sandbox handling to support the complete hosted review path.

## [0.2.1] - 2026-10-22

### Highlights
- Fixed reviewer Job startup when the controller has already modified the Job.

### Fixes
- Resolved a conflict when unsuspending reviewer Jobs, allowing them to start after controller updates. (#58)

### Changes
- Added server test coverage for the response returned when a submission fails.

**Full changelog:** `v0.2.0...v0.2.1`

## [0.2.0] - 2026-10-21

This release improves how Bosun reports review results, publishes findings, and communicates failures.

### Highlights

- **Structured, revision-aware results:** Completed reviews now use a structured format and track the revision reviewed. (#57)
- **Native pull-request reviews:** Bosun now publishes findings as a GitHub pull-request review. (#55)

### Fixes

- **Visible review failures:** Bosun now comments on the pull request when a review fails, making unsuccessful runs easier to spot. (#54)

### Changes

- Clarified which identity Bosun uses when publishing to GitHub. (#56)
- Added instructions for updating the Bosun CLI through Homebrew. (#49)
- Replaced the ASCII architecture diagram with Mermaid and added a diagram of the local-run workflow. (#49)

**Full changelog:** `v0.1.6...v0.2.0`

## [0.1.6] - 2026-10-19

### Highlights
- Added local cluster reset and doctor commands to support environment recovery and troubleshooting.

### Fixes
- Added safeguards to reset operations and protected shared snapshots.
- Scoped doctor skill checks to avoid checking beyond the intended context.

### Changes
- Improved doctor diagnostics to make troubleshooting more informative.
- Merged the reset and doctor updates in PR #48.

**Full changelog:** v0.1.5 → v0.1.6

## [0.1.5] - 2026-10-18

### Highlights

- **More reliable local reviews:** Review snapshots now use durable host storage, with added test coverage for snapshot paths and mount failures.
- **Improved Claude review troubleshooting:** Permission failures now provide clearer diagnostics without adding diagnostic noise to completed reviews.

### Fixes

- Stop pods whose mounts fail before cleaning up snapshots.
- Configure Git trust for the exact mounted checkout so Claude can inspect it.
- Upload Claude credentials after a headless refresh.
- Place permission-denial diagnostics before the length-limited review reason to keep troubleshooting details visible.

### Changes

- Updated `golang.org/x/sys` from `0.48.0` to `0.49.0` (#43).
- Incorporated final Bosun review fixes as part of the durable local-review mount work (#46).

## [0.1.4] - 2026-10-17

This patch release fixes review workspace handling in `bridgectl` and strengthens path validation tests.

### Highlights

- `bridgectl` now supports both local review snapshots and hosted review workspaces.

### Fixes

- Corrected path handling that prevented local review snapshots and hosted review workspaces from being accepted by `bridgectl`. (#41, #45)

### Changes

- Tightened tests for `bridgectl`’s expected path behavior to help prevent regressions. (#41)

**Full changelog:** [v0.1.3...v0.1.4](../../compare/v0.1.3...v0.1.4)

## [0.1.3] - 2026-10-12

### Highlights
- Updated the `bridgectl` base to **v1.4.4**. (#39)

### Fixes
- Added upstream base verification when reviewing pull requests from forks. (#38)

### Changes
- Updated documentation to resolve the release version dynamically rather than use a pinned version. (#35)

## [0.1.2] - 2026-10-10

This release improves CLI setup, isolates reviewer credentials, and makes review failures easier to diagnose.

### Highlights

- **Review skills installed during setup:** `bosun init` now installs the review skill for Claude Code and Codex. (#33)
- **Cluster lifecycle management:** The CLI now manages the cluster lifecycle and loads local provider credentials. (#17)
- **Repository-scoped credentials:** Each reviewer receives only the credential for the repository it is reviewing. (#32)
- **Clearer failure reporting:** Review errors now identify which part of Bosun failed. (#27)

### Fixes

- Reviews that perform no review work are now marked as failed rather than successful. (#26)
- Reviewer images are verified before a Kubernetes Job is submitted. (#16)
- Review prompts have been refined to address defects observed in real reviews. (#24)

### Changes

#### Documentation
- Added self-hosting guides for DigitalOcean and local tunnel setups. (#30)
- Corrected provider documentation to stop listing OpenCode as supported. (#21)
- Graduated ADR-001 alongside the empty-review failure fix. (#26)

#### CI and dependencies
- Added chart CI checks for Deployment fields that cannot change during an upgrade. (#20)
- CI now uses the runner-provided ShellCheck instead of installing it. (#25)
- Updated the Go container image to `1.27-bookworm`, `golang.org/x/sys` to `0.48.0`, Protobuf and Kubernetes dependencies, and `docker/setup-qemu-action` to v4. (#8, #9, #10, #12, #13)

**Full changelog:** [v0.1.1…v0.1.2](../../compare/v0.1.1...v0.1.2)

## [0.1.1] - 2026-10-01

### Highlights
Image scanning now reports findings without blocking releases.

### Fixes
- Prevented image scan results from gating the release workflow. (#15)

### Changes
- Switched the release image scan from an enforcement step to a reporting step.

## [0.1.0] - 2026-10-01

Bosun’s first release introduces automated GitHub reviews and GitHub-free local repository reviews, with a Go runtime, Helm deployment, and a Kind-based development workflow.

### Highlights

- **GitHub review automation:** Handle GitHub review triggers and execute review work through `bridgectl`.
- **Local review workflow:** Review repositories without GitHub using a local CLI, progress reporting, and a Kind environment. Development-mode Helm installs can run without a GitHub webhook secret.
- **Headless Codex support:** Run non-interactive reviews through the `codex-exec` provider, with provider credential checks and detection of incomplete review output.
- **Kubernetes deployment:** Ship a Helm chart alongside documentation for installation, GitHub App configuration, architecture, and local development.

### Fixes

- Corrected review execution to drive the `bridgectl` API and restored GitHub and headless Go review sessions.
- Improved Codex startup by resolving its executable independently of the review workspace and closing stdin for argument-driven reviews.
- Tightened admission handling and local snapshot safety. Local review pods use an explicit non-root user, with snapshot access support for unprivileged Kind reviewers.
- Added graceful shutdown handling to drain in-flight deliveries.
- Fixed cancellation test setup on fresh CI runners and allowed the release pipeline to run without an existing tag.

### Changes

- **Go runtime:** Established the Go implementation and removed the legacy Python runtime and automatic secret setup.
- **Build and release:** Added a Ballast-aligned build and release pipeline, Go image and test-suite validation, and repository-specific tooling configuration.
- **Dependencies:** Pinned `bridgectl` to v1.4.1 and cleaned up Go module metadata.
- **Tests and documentation:** Expanded validation for review triggers and Helm configurations, including GitHub-free development mode. Added guidance on provider selection, GitHub setup, and local reviews.
