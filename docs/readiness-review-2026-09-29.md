# Bosun readiness review — 2026-09-29 (America/Vancouver)

## Scope and verdict

Reviewed implementation commit `c6606a76e092dd7e3de2c9e801e47a51e9bc40ed`, including
CLI/snapshots/monitoring, webhook handling, admission, Jobs, GitHub authentication,
clone/agent execution, chart, Dockerfile, documentation and CI configuration.
The default branch was `c22f0ff55d79775e1c2ca900d68f10314bc99239` and contained only
README.md. PR #1 was closed unmerged. PR #2 was merged **into the feature branch**,
not main. This deployment PR brings that work to main without reopening or
silently merging either earlier PR.

**CLI: usable as a developer beta with Docker + Kind, not a standalone reviewer
installed from main. Self-hosted App: controlled trusted-repository pilot, not
unattended production. Commercial SaaS: requires a separate control plane.**

The existing PR #2 head `f43d54eccf170de142c3cff7629e560e1e163640` had successful CI
run 36654425181. Its CI checks Go tests, scripts, Helm rendering and Docker build;
it does not prove a real model-powered Kind or GitHub review. The review session
has no Docker, Kind, Helm, provider keys or cluster credentials, and cannot fetch
Go modules locally (installed Go is 1.23.2; the repo uses 1.26). Do not describe
an offline inspection or a new CI run as a successful live acceptance test.

## Findings

| Priority | Finding at inspected commit | Treatment |
| --- | --- | --- |
| Blocker | Implementation absent from main | This PR includes the feature branch; merge only after checks/review |
| High | App signing key injected into every remote review pod (`internal/jobs/jobs.go`) | Fixed for the served webhook path: controller issues a repo token; suspended Job owns an immutable Secret |
| High | Remote Jobs lack resource requests/limits | Added bounded CPU, memory and temporary storage plus RuntimeDefault seccomp |
| High | Published results scrub GitHub credentials but not model credentials (`review/runner.go`, `clone.go`) | Extended result redaction to provider credentials/nested credential JSON; regression test |
| High | Local snapshot walks ignored files and excludes only root `.env`/`.git` (`localreview/snapshot.go`) | Still open: ignored build trees, nested dotenv files and secrets can enter snapshots. Use trusted sanitized repos; `--branch` avoids dirty-file copying but not Git history |
| High | Automatic PR triggers accept fork heads without an explicit approval policy | Still open for Bosun. Do not expose to untrusted contributions; Reviewer must reject forks by default |
| High | Capacity 503 has no durable enqueue/retry | Documented manual redelivery; a SaaS control plane needs a durable queue |
| Medium | Whole `/repos` hostPath is mounted into local Kind Jobs | Local development only; not a customer isolation boundary |
| Medium | Duplicate suppression ends when Job TTL deletes the Job | Keep a persistent delivery ledger in a hosted service |
| Medium | Remote PR review has no explicit base SHA supplied; publication has no latest-head check | Add canonical base/head resolution and stale-result fencing before broad use |
| Medium | Branch reviews live only in pod logs; no Checks/inline structured output | Current behavior documented; not complete Copilot feature parity |
| Medium | No live Kind/provider smoke in CI | Required operator acceptance test in deployment guide |
| Medium | HTTP server lacks timeouts/graceful shutdown | Added bounded server timeouts and SIGTERM shutdown |
| Medium | No explicit license file found in the inspected root | Owner must choose distribution/licensing terms before publishing a commercial distribution |

Removing environment variables from an agent's child process does not isolate
its parent's secrets within the same pod. The new controller/worker split narrows
the largest risk, but a repository-scoped token still exists in the trusted
worker. Reviewer should clone in a separate init-container with a read-only
repository token, keep publishing credentials in its control plane, and never
put the App key or a tenant-runner credential into review pods.

## Running the current CLI

From the implementation/deployment branch, not the old main:

```bash
make setup
export BOSUN_REVIEW_PROVIDER=codex-bosun
# Set OPENAI_API_KEY securely before kind-up.
make kind-up
make cli
./bin/bosun review /absolute/path/to/trusted-repo --base main --provider codex-bosun
# Optional: committed content only, instead of dirty/untracked working files.
./bin/bosun review /absolute/path/to/trusted-repo --branch feature/example --base main
```

`make install` installs the command on Go's binary path. `review-status JOB
--follow`, `--json`, cancellation, detached execution and persisted local history
are implemented. The CLI explicitly rejects a non-Kind context because its
snapshots use host mounts. It does not upload local checkouts to a remote cluster.
Use the GitHub App path for a remote Kubernetes cluster.

## Release gates

Run a real trusted-repo review with codex-bosun and, separately, claude-bosun;
verify correct base/head findings and clean cancellation. Validate fresh Kind
setup, Docker architecture support on the intended host, secret rotation,
resource limits, NetworkPolicy, duplicate deliveries and capacity failure
recovery. Finish snapshot secret filtering, fork policy and durable queueing
before treating it as unattended production infrastructure.
