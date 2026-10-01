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
