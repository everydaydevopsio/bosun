# Lessons

## 2026-10-10 — A failed PR lookup must stop review

- Incident/bug: a failed `gh pr view` became empty metadata and allowed a fork review to use its local default base.
- Root cause pattern: collapsing an API error into the same value as an optional input silently weakens a fail-closed workflow.
- Early signal missed: the first test covered failed fetches but not failed PR lookup.
- Preventative rule: require an explicit offline mode when PR metadata is unavailable.
- Validation added: a failing `gh` mock must stop; explicit offline mode remains testable.
- Next trigger to detect sooner: any new fallback after a remote metadata lookup.

## 2026-10-10 — Failure tests need controlled transports

- Incident/bug: the failed-fetch test sent traffic to a nonexistent GitHub repository.
- Root cause pattern: the success URL was rewritten to a local fixture, but the failure URL was not.
- Early signal missed: fast failure on this machine concealed the network dependency.
- Preventative rule: route every URL in network failure tests to a local fixture and bound subprocess duration.
- Validation added: a local nonexistent-repository rewrite, disabled Git prompts, and a five-second context deadline.
- Next trigger to detect sooner: adding a new URL or subprocess to an integration test.

## 2026-10-10 — Credentialed fetches must not prompt in agent workflows

- Incident/bug: the PR base fetch could request Git credentials interactively and hold a detached review before submission.
- Root cause pattern: a noninteractive test environment did not prove the shipped fetch could not prompt.
- Early signal missed: the test harness disabled Git prompts, while the shipped skill did not.
- Preventative rule: set `GIT_TERMINAL_PROMPT=0` on agent-driven Git fetches that must fail closed.
- Validation added: the installed skill contract asserts the noninteractive fetch setting.
- Next trigger to detect sooner: constructing a new Git URL inside an agent workflow.

## 2026-09-24 — Build prerequisites must be explicit

`make test` invoked protobuf generation before it ensured the Python virtualenv
containing `grpcio-tools` existed. The Go migration will make generated-code
dependencies an explicit Makefile prerequisite so a clean checkout can run the
documented test command.

## 2026-10-01 — A cached test result is not a test run

- Incident/bug: `scripts/kind-up.sh` was changed to invoke the CLI with a path
  relative to the caller's working directory. `go test ./...` reported
  `ok internal/review (cached)` locally, and CI failed on
  `TestLocalCommandsSelectKindContext` with
  `stat .../internal/review/cmd/bosun: directory not found`.
- Root cause pattern: the test exercises a shell script, not Go source, so no
  input Go's cache tracks had changed. The package's cached result stayed valid
  while the thing it tests was broken.
- Early signal missed: `(cached)` next to the one package whose behaviour the
  change actually affected, in a change that touched no Go file in it.
- Preventative rule: when a change touches files a test reads at runtime —
  scripts, embedded assets, fixtures, config — verify with `go test ./... -count=1`.
  Treat `(cached)` on a package related to the change as unverified.
- Validation added: the script now resolves the repository root itself and takes
  the CLI through a `BOSUN_BIN` seam, and the test drives that seam with a mock,
  so the delegation is asserted rather than assumed.
- Next trigger to detect sooner: any script or embedded file edited in the same
  commit as a Go test that shells out to it.
