# CLAUDE.md

This file provides guidance to Claude Code for working in this repository.

## Repository Facts

Use this section for durable repo-specific facts that agents repeatedly need. Prefer facts stored here over re-deriving them with shell commands on every task.

Keep only stable, reviewable metadata here. Do not store secrets, credentials, or ephemeral runtime state.

Suggested facts to record:

- Canonical GitHub repo: `everydaydevopsio/bosun`
- Default branch: `main`
- Primary package manager: `go`
- Version-file locations agents should check first: `go.mod`
- Canonical config files: `go.mod`
- Primary CI workflows: `ci.yml`
- Primary release/publish workflows: `publish.yml`
- Preferred build/test/lint/format/coverage commands: `make test, make lint, make build`
- Coverage threshold: `<value>`
- Generated or protected paths agents should avoid editing directly: `.ballast/`

Update this section when those facts change. If live runtime state is required, discover it separately instead of treating it as a durable repo fact.

## Installed agent rules

Created by Ballast. Do not edit this section.

### Repository Tool Policy

- Check `.rulesrc.json` `tools` before adding, installing, or running language tooling.
- Configured tools: docker=docker,hadolint,trivy; go=go,gofumpt,golangci-lint.

Read and follow these rule files in `.claude/rules/` when they apply:

- `.claude/rules/common/local-dev-autonomy.md` — Rules for common/local-dev-autonomy
- `.claude/rules/common/local-dev-badges.md` — Rules for common/local-dev-badges
- `.claude/rules/common/local-dev-env.md` — Rules for common/local-dev-env
- `.claude/rules/common/local-dev-license.md` — Rules for common/local-dev-license
- `.claude/rules/common/docs.md` — Rules for common/docs
- `.claude/rules/common/cicd.md` — Rules for common/cicd
- `.claude/rules/common/observability.md` — Rules for common/observability
- `.claude/rules/common/publishing.md` — Rules for common/publishing
- `.claude/rules/common/publishing-apps.md` — Rules for common/publishing-apps
- `.claude/rules/common/publishing-cli.md` — Rules for common/publishing-cli
- `.claude/rules/common/publishing-libraries.md` — Rules for common/publishing-libraries
- `.claude/rules/common/publishing-sdks.md` — Rules for common/publishing-sdks
- `.claude/rules/common/git-hooks.md` — Rules for common/git-hooks
- `.claude/rules/common/tasks-task-system.md` — Rules for common/tasks-task-system
- `.claude/rules/common/tasks-todo.md` — Rules for common/tasks-todo
- `.claude/rules/common/plan-lifecycle.md` — Rules for common/plan-lifecycle
- `.claude/rules/common/spec-kit.md` — Rules for common/spec-kit
- `.claude/rules/common/testing-process.md` — Rules for common/testing-process
- `.claude/rules/common/core.md` — Rules for common/core
- `.claude/rules/go/go-linting.md` — Rules for go/linting
- `.claude/rules/go/go-logging.md` — Rules for go/logging
- `.claude/rules/go/go-testing.md` — Rules for go/testing
- `.claude/rules/docker/docker-linting.md` — Rules for docker/linting
- `.claude/rules/docker/docker-logging.md` — Rules for docker/logging
- `.claude/rules/docker/docker-testing.md` — Rules for docker/testing

## Installed skills

Created by Ballast. Do not edit this section.

These skills are registered with Claude Code. Invoke one by name (for example `/ballast-audit`) when it is relevant:

- `/owasp-security-scan` — run an OWASP-aligned security audit across Go, TypeScript, and Python projects
- `/aws-health-review` — run a weekly read-only AWS health review covering configuration, performance, errors, and warnings
- `/aws-live-health-review` — run a read-only AWS live health review for current EC2, RDS, ALB, CloudWatch alarms, and logs
- `/aws-weekly-security-review` — run a weekly read-only AWS security baseline review and generate a prioritized findings report
- `/github-health-check` — run a comprehensive GitHub repository health check covering CI status, code quality, branch hygiene, and repo configuration
- `/github-pr-copilot-cycle` — create or update a GitHub PR, request Copilot review, triage and fix Copilot comments, push fixes, check CI, and repeat up to three cycles
- `/ballast-audit` — audit a Ballast installation for stale, unowned, oversized, and irrelevant rules and skills, and report the narrowest config that still covers the repository
- `/agent-performance-audit` — use distilled bridgectl agent-performance findings to audit Ballast rules and skills for evidence-backed improvements
- `/ballast-project-maintenance` — inspect, bootstrap, and repair Ballast-managed repository state including .ballast/ local tools
- `/speckit-bootstrap` — initialize or repair GitHub Spec Kit in an existing repository using native agent skills
- `/speckit-reverse-engineer` — reverse-engineer an existing application into a high-level GitHub Spec Kit baseline
- `/speckit-delivery` — orchestrate GitHub Spec Kit's native skills for a bounded product change
- `/docker-registry-publish` — set up Docker image publishing to GHCR or Docker Hub with public or private registry visibility
