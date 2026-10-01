<!-- ballast:rule id="go/core" version="5.21.3" checksum="b081fb2e3b760cf28ec8bac1444bf4ed18394b3b571f2cc2ea675aeb4c211bc3" -->
# Ballast Core Rules

Compact engineering invariants for this repository (`ruleProfile: minimal`). The full Ballast rule set is not emitted in this profile; switch `ruleProfile` to `full` in `.rulesrc.json` and re-run `ballast install --refresh-config` when detailed guidance should be installed.

## Invariants

- **Branch before code**: never edit files on the default branch; create a task branch (`issue-<n>-<slug>` when an issue exists) before changes. Read-only investigation needs no branch.
- **TDD for behavioral changes**: write a failing test first, confirm it fails for the right reason, implement the minimum to pass, then refactor green. Cover failure paths, not only the happy path.
- **Branch TODO triage**: track branch work in `tasks/todo.md`; before creating a PR, resolve, promote (to the configured task system), or remove every unchecked item.
- **Releases**: publish only from `v`-prefixed semver tags created by the release workflow; artifact version must equal the tag; CI workflows cancel superseded runs, publish workflows never cancel in-flight runs.
- **Generated outputs**: do not edit generated files directly; change the source and regenerate. Respect the Repository Tool Policy in this file's manifest.

## Commands — Go

- Lint/format: `gofmt -w .` (or `gofumpt`), `golangci-lint run`.
- Test: `go test ./...` with `-cover`; coverage gate enforced in CI.

## Commands — Docker

- Lint: `hadolint Dockerfile`, `docker compose config`, `trivy config .`.
- Test: build the real Dockerfile, smoke-test the built image, and scan with `trivy image` before publishing.
