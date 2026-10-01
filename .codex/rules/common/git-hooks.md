<!-- ballast:rule id="go/git-hooks" version="5.21.3" checksum="74b9e9d9a59eded8c7e8052988b3de0481a5c9c8101afed9e891e0fc003027d9" -->
# Git Hooks Rules

These rules keep local Git hook orchestration consistent with the repository layout and testing strategy.

---
## Your Responsibilities

1. Select the correct hook tool for the repository layout.
2. Configure fast checks for the commit-time hook.
3. Configure unit tests for `pre-push`.
4. Keep hook configuration current as commands and repo layout evolve.
5. Keep hook scripts executable and easy to audit when a hook backend requires scripts.

## Hook Strategy

- Use `pre-commit` for Go projects, and fan out to language-local configs with `sub-pre-commit` when needed.
- Create or update `.pre-commit-config.yaml` at the repo root.
- Use `sub-pre-commit` hooks to invoke nested `.pre-commit-config.yaml` files in Go subprojects.
- Install hooks with `pre-commit install` and `pre-commit install --hook-type pre-push`.
- Configure the pre-push stage to run Go unit tests for each module.
- Add the official `gitleaks` pre-commit hook in `.pre-commit-config.yaml` for secret detection; do not generate or call a repo-local no-secrets shell script.
- Keep `govulncheck`, fuzzing, and `go test -race` in CI, pre-push, or explicit security-review workflows unless this repository opts into running them at commit time.
- Keep the configuration current with `pre-commit autoupdate`.
- Verify the hook configuration with `pre-commit run --all-files`.

## Important Notes

- Keep commit-time hooks fast enough that developers do not bypass them.
- Keep `pre-push` focused on the repo's unit test command and required build step.
- Keep language-specific dependency audits, SAST, IaC scans, fuzzing, race detection, and manual secure-review guidance in CI or review workflows unless the repository explicitly opts into running them from hooks.
- Update hook commands when lint, format, build, or test scripts change.
- Verify the hook setup after changes before handing off the repo.

## When Completed

1. Show the user the hook files and commands you added or updated.
2. Explain how commit-time checks differ from push-time checks.
3. Explain how to verify the hook setup locally.
