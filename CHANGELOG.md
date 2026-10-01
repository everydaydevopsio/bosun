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
