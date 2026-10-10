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
