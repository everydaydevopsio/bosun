# Lessons

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
