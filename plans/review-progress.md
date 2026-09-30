# Review CLI, progress, timing, and error visibility

Status: implemented for the local Kind workflow, with limitations below.

## Implementation and validation

Implemented:

- Native `bosun review [PATH]` with branch/base/provider/timeout/context/namespace,
  JSON, detach, and `review-status --follow`; build/install Make targets and
  compatibility wrappers.
- Isolated snapshots for current dirty checkouts, explicit committed refs, and
  linked worktrees; typed local Jobs share admission and credential construction.
- Versioned worker events, elapsed/stage timing, provider activity tracking,
  quiet warnings, deadline display, pod state and Kubernetes warning events.
- gRPC health/provider preflight, session polling, prompt-acceptance validation,
  bounded attachment recovery with replay cursor/deduplication, restart detection,
  replay-gap rejection, and bounded/redacted startup diagnostics.
- Durable local event journals and private run summaries, reconnecting, terminal
  result caching, cancellation-aware cleanup, and concurrent-summary protection.
- History-based duration ranges after five comparable successful runs; deadline
  and estimated remaining time are displayed separately.
- Configurable slog output and level in the Go entrypoint.

Validation performed: Go suite with race detection; shell and Helm lint/template
checks; Kind success, provider exit-7 failure, detached submission, and result
retrieval using an isolated deterministic-provider image. The first Kind test
caught and fixed a pod security-context error (group set without pod user).
The actual paid Codex/Claude review paths have not been rerun as part of this
change; deterministic providers exercised the same pinned bridgectl gRPC path.

Remaining limits and follow-up scope:

- No fabricated file/tool progress: the vendored session API still lacks action
  events. The local image and API were inspected, but provider-specific adapters
  or an upstream bridgectl protocol extension remain separate work.
- History compares repository/provider/image tag/change size. Model identity and
  immutable image digest are not currently exposed in the run metadata; estimates
  explicitly remain approximate and may combine different model configurations.
- Session creation with an ambiguous RPC outcome fails clearly rather than
  retrying or attempting to recover a potentially started session. Attachment
  recovery reuses the original session and never resends a prompt.
- A detached run's journal survives Job TTL expiry; run `review-status` to collect
  its result. If the job disappeared, snapshot cleanup is conservative and may
  require inspection/manual cleanup. There is no background cleanup daemon.
- Snapshotting detects changed HEAD, index, status, and files changing during a
  copy. It cannot provide an atomic snapshot of a concurrently edited directory;
  stop edits during snapshot creation for consistent working-tree content.

The design below documents the implementation contract and follow-up targets.

Reviewed against the current Bosun source and vendored bridgectl protocol.
The findings below establish available API contracts, not verified behavior of
every provider in the running bridgectl image. Verify that image before enabling
provider-specific action parsing or recovery behavior.

## Original problem

The reviewer buffers agent output until completion, and `scripts/review-local.sh`
prints job logs only after waiting for completion or failure. A running pod does
not tell the user whether the agent is active, blocked, or failing. There are no
regular progress updates or duration estimates.

## Intended experience

- Show the repository, branch, provider, job identifier, and configured timeout.
- Report lifecycle stages immediately and elapsed time every 10 seconds.
- Distinguish total elapsed time, stage duration, last provider activity, and
  time remaining before the deadline.
- Display observed agent actions where supported; otherwise show reviewing and
  last activity without inventing a completion percentage.
- Report errors promptly with the failed stage, reason, and next diagnostic step.
- Print the final review separately from operational status.
- Estimate duration from comparable prior runs once enough history exists.

Illustrative output (the estimate is an example, not measured data):

```text
Reviewing bosun / feature-branch with codex-bosun
Estimated duration: 4–9m from 12 similar runs
Review timeout: 30m; startup allowance: 2m

00:00  Preparing repository snapshot
00:03  Waiting for reviewer pod
00:08  Starting provider
00:12  Reviewing
02:12  Reviewing | last provider activity 8s ago
04:31  Completed | total 4m31s

[final review]
```

## Phase 1: live status and failures

### 1. Track lifecycle stages

Add structured status reporting to `internal/review/runner.go` and
`internal/review/bridge.go`. Track preparing the repository, starting the
provider, reviewing, publishing when applicable, and terminal outcomes
(completed, failed, timed out, cancelled). Include run/session identifiers,
timestamps, stage elapsed time, and total elapsed time. Keep status out of the
review body. Wire up text/JSON logging configuration explicitly: the scripts
pass `BOSUN_LOG_FORMAT` and `BOSUN_LOG_LEVEL`, but the current Go config and main
do not configure a matching slog handler.

The local launcher owns snapshot and pod-startup timing; the worker owns its
review deadline. Display those separately rather than resetting total elapsed
time when the worker starts.

### 2. Add heartbeats and activity tracking

Emit a status update every 10 seconds even while the event stream is silent.
Track last observed provider activity separately from reporter heartbeats.
Use bounded `GetSession`/health queries to distinguish session state from stream
silence. A running process or healthy connection does not prove model progress.

After a configurable quiet interval, display a warning such as "No provider
activity received for 2m; session still running." Silence alone must not mark a
review failed. Stop tickers and monitoring goroutines when the run ends.

### 3. Surface supported agent activity

Verify which structured events the pinned bridgectl image actually forwards for
each supported provider. Prefer explicit tool start/completion and test activity
events. Do not infer completed work from output volume or expose raw reasoning.

The current protocol lacks dedicated file/tool/progress events. If provider
events are lost during normalization, extend bridgectl's event contract and
regenerate Bosun's bindings before promising detailed action reporting. Retain
a generic lifecycle/activity fallback for providers without those capabilities.

### 4. Stream terminal updates and support reconnecting

Implement the monitor in the Go CLI described below, with
`scripts/review-local.sh` becoming a compatibility wrapper. Follow logs while monitoring Job and Pod
state. Show scheduling and container-startup status before logs exist. Handle
log reconnects, avoid duplicate output, and preserve the actual review exit
status. Add `bosun review-status JOB --follow` for inspection from another
terminal; an optional Make target should delegate to that command.

On Ctrl-C, request job termination and wait for the pod to stop before removing
its snapshot. If termination cannot be confirmed, retain the snapshot and print
the reconnect/cleanup instructions. An explicit `--detach` keeps the job and
snapshot running and records their association for later inspection and cleanup.

### 5. Report actionable errors

Cover invalid credentials, provider startup failure, stream errors, nonzero
exits, empty results, timeouts, image failures, scheduling failures, OOM kills,
and publishing failures. Report the observed reason without guessing a cause.
Keep Kubernetes API connectivity failures distinct from review failures.

Capture bounded, redacted bridgectl startup diagnostics and check early process
exit; `connectBridge` currently discards subprocess output and ignores its exit
result. Do not log credentials or raw sensitive provider payloads. Report
terminal failures promptly and return a nonzero exit status. Incomplete output
must never be published as a successful review.

## Phase 2: duration estimates

Persist small local run summaries outside disposable snapshots and Jobs. Record
provider, repository, change size, startup time, review duration, and outcome.
Use comparable successful runs to calculate a typical duration range, and show
the sample count. Keep failed/timed-out runs separately so they do not masquerade
as fast successful reviews or hide a history of timeouts.

Until sufficient history exists, show "Estimate unavailable; first comparable
run" or an equivalent low-sample explanation. Distinguish estimates from the
hard timeout, update remaining-time guidance conservatively, and explicitly show
when a review exceeds its expected range. Never derive percentage complete from
elapsed time alone.

## Pre-implementation gRPC baseline

- Bosun directly creates a generated `BridgeServiceClient` in
  `internal/review/bridge.go`; it does not shell out to drive review sessions.
- It calls `StartSession`, `AttachSession`, `WriteInput` for providers other than
  `codex-bosun`, and `StopSession` during cleanup. For `codex-bosun`, the prompt is
  supplied through `agent_opts["arg:prompt"]`.
- The receive loop currently handles `ATTACHED`, `OUTPUT`, `SESSION_EXIT`, and
  `ERROR`. Output is buffered until successful exit.
- The vendored protocol also has `THINKING`, `REPLAY_GAP`, and writer ownership
  events. Bosun currently ignores those. A thinking event can count as activity
  without displaying its contents. A replay gap should be treated explicitly as
  incomplete observability/output, not silently ignored.
- `GetSession` exposes lifecycle state, errors, exit status, timestamps, and
  sequence positions. Bosun currently does not call it or `Health`.
- No dedicated tool-call, file-progress, test-progress, percent-complete, or ETA
  fields exist in the vendored session API. `OUTPUT` is opaque bytes, so payloads
  could carry provider-specific data, but the current consumer does not decode
  such data.
- The separate `TelemetryCollectorService` accepts opaque JSONL segments for
  collection; this schema is not a live action subscription used by Bosun.
- The configured `codex-bosun` command emits plain output, not an explicitly
  configured structured action stream. `stream_json: true` disables PTY
  allocation here; that flag alone does not add action-level visibility.

Conclusion: direct gRPC integration exists, but exact agent activity requires
additional provider instrumentation/event support and consumer handling. Session
lifecycle visibility and elapsed timing can be added with the existing API.

## Plan review: original gaps and integration priorities

Using every bridgectl feature is not the objective. Use features that improve
review correctness, diagnosis, and recovery; leave unrelated functionality out.

| API or feature | Current Bosun behavior | Planned use |
| --- | --- | --- |
| `Health`, including `server_instance_id` | Not called; socket/address discovery is treated as readiness | Bounded readiness checks; distinguish stale discovery files from a healthy server; detect daemon restarts without silently creating a replacement review |
| `ListProviders` and provider health | Not checked before session creation | Verify the selected provider exists and its executable is available; availability does not prove credentials work or that a provider is suitable for headless reviews |
| `StartSession` response | Status/timestamp ignored | Validate returned session identity/state and capture startup timing; reconcile an ambiguous RPC result using the original session ID before retrying |
| `GetSession` | Not called | Poll state, errors, recorded exit, and sequence positions during quiet periods; expose loss of observability separately from agent failure |
| `AttachSession.after_seq`, event sequence and replay metadata | No cursor/resume handling | Resume the same session with bounded retries and deduplicate events; never start a second review merely because attachment failed |
| `REPLAY_GAP` | Ignored | Mark output incomplete; fail the review if completeness cannot be recovered, even if the process later exits successfully |
| `THINKING` | Ignored | Record activity timestamps only; do not display or persist raw thinking content |
| `WriteInput` response | RPC error checked, acceptance and byte count ignored | Verify accepted input and expected byte count; fail clearly on rejection/partial acceptance and avoid automatic resend after ambiguous delivery |
| Writer ownership events and observer role | Always attaches as writer; ownership events ignored | Keep one worker as writer; use observer role for any future direct session viewer; never seize writer ownership from a status command |
| `StopSession` result | Cleanup error ignored | Report cleanup failure separately from the primary review outcome and confirm shutdown before deleting shared workspace data |
| `ListSessions` | Not called | Optional diagnostics/recovery scoped to Bosun sessions; not required for normal review monitoring |
| `ResizeSession`, enrollment, JWT registration | Unused | Out of scope for headless reviews over the existing local connection; revisit authentication only if a remote gRPC transport is introduced |
| `TelemetryCollectorService` | Unused | Do not use as a live progress API; collection is a separate concern |

### Recovery and correctness details

- Retain session ID, server instance ID, and the last processed sequence. Treat
  daemon replacement as loss of the original session, not a retryable invitation
  to run the review twice.
- Reattachment must not resend the prompt on another `ATTACHED` event. Track
  prompt delivery independently from stream attachment. Test writer attachment
  recovery against the pinned server; do not force ownership on conflict.
- A recorded successful exit can help reconcile a disconnected stream, but it
  cannot prove that all review output was collected. Require complete output and
  a successful exit before accepting or publishing a review.
- Bound readiness, status, and reconnect calls by the remaining review deadline.
  Use a receive goroutine/channel plus a select loop so heartbeats continue while
  `Recv` blocks, with cancellation and goroutine cleanup covered by tests.
- Worker health checks run inside the reviewer pod. The host CLI watches
  Kubernetes state and worker events; it does not need a public gRPC endpoint or
  port forwarding into every disposable pod.

### Event transport and output separation

Kubernetes pod logs combine stdout and stderr. Merely writing progress to stderr
inside the pod will not give the host CLI a clean final-review stdout stream.
Define a versioned worker event envelope with run ID, increasing event sequence,
timestamp, event type, stage, and a safe payload. Emit status, activity, terminal
error, and final-review events. The host renders status to stderr and the final
review to stdout; `--json` exposes the event envelope for automation. Treat
unknown diagnostic lines as diagnostics, never as review content.

Use event IDs to deduplicate log replay after reconnection and persist terminal
summary/result locally before the Kubernetes Job TTL expires. A viewer started
after expiry should show the stored result or explicitly say it is unavailable.
Keep provider activity parsing separate from this Bosun-owned event envelope.

## Single-command review interface

The command is feasible using the existing Go binary and client-go dependency.
Previously `cmd/bosun/main.go` recognized only `reviewer`; all other arguments
fell through to starting the webhook server. Explicit command dispatch now
provides help and rejects unknown commands before connecting or starting a server.

Implemented interface:

```bash
bosun review "$PWD"
bosun review "$PWD" --branch feature/login --provider claude-bosun
bosun review "$PWD" --branch feature/login --base main --provider codex-bosun
bosun review "$PWD" --timeout 20m --json
bosun review "$PWD" --detach
bosun review-status bosun-local-review-xxxxx --follow
```

| Argument/flag | Defined behavior |
| --- | --- |
| `[PATH]` | Defaults to the current directory; accepts spaces and linked worktrees; resolves and validates the Git root |
| `--branch NAME` | Review a specific local branch or existing remote-tracking ref, resolved to a commit SHA before snapshotting; no checkout switch or implicit fetch |
| `--base REF` | Comparison base, distinct from the reviewed branch; otherwise resolve the available default-branch ref, with an actionable error if ambiguous |
| `--provider NAME` | Flag overrides `BOSUN_REVIEW_PROVIDER`, then defaults to `codex-bosun`; validate against the running provider list and headless support |
| `--timeout DURATION` | Positive review execution limit; normalize to worker seconds and explicitly show startup allowance |
| `--context NAME` | Defaults to `kind-${BOSUN_KIND_CLUSTER:-bosun}`; never silently uses the active Kubernetes context |
| `--namespace NAME` | Flag overrides `BOSUN_NAMESPACE`, then defaults to `bosun` |
| `--json` | Machine-readable versioned events instead of terminal rendering |
| `--detach` | Submit and print the job ID; preserve snapshot and run metadata for status/result retrieval |

Accept flags before or after PATH, including the requested `bosun review PATH
--provider NAME` form. Standard Go `flag` parsing stops at the first positional
argument, so explicitly choose a parser or parsing strategy supporting this and
test `--` and paths containing spaces. Do not pass user values through shell
interpolation.

### Branch and snapshot semantics

- With no `--branch`, preserve the existing behavior: snapshot current HEAD,
  staged, unstaged, and untracked changes, including detached HEAD checkouts.
- With explicit `--branch`, review committed content at that resolved ref only,
  even when it names the current branch. Print this scope clearly; do not overlay
  another checkout's dirty files. Working-tree inclusion for explicit refs can be
  added later as a separate, validated option.
- Resolve and display head SHA and base SHA; carry them through the worker's
  request/prompt instead of just changing the displayed `BOSUN_REF` string.
  Define committed comparison as merge-base(base, head) to head, plus working
  changes only in the default current-checkout mode.
- Create a disposable self-contained snapshot without changing source refs,
  index, or working files. Preserve linked-worktree handling and the existing
  `.env` exclusion. Check refs as Git arguments, reject invalid options, and
  detect source HEAD/index changes during snapshot creation with a clear retry
  message rather than silently reviewing mixed revisions.

### Implementation boundaries and setup

- Add a local-review orchestration package for snapshotting, typed Job creation,
  watching state/logs, cancellation, and durable local run metadata. Keep worker
  execution in `internal/review` and CLI rendering separate from both.
- Reuse/refactor common Job construction and credential references from
  `internal/jobs`; do not blindly reuse the GitHub submission path, which has
  different repository/authentication and volume needs. Include local reviews in
  the existing admission/concurrency controls.
- Use client-go loading rules honoring `KUBECONFIG` with the explicit context
  override. The existing `kubeClient` fallback hardcodes the recommended home
  config path and is insufficient for this interface.
- Keep the initial execution backend as the configured local Kind cluster with
  its `/tmp/bosun-repos` host mount. Arbitrary remote clusters cannot access the
  local snapshot; validate this backend requirement and reject unsupported
  contexts with a clear explanation. A future upload backend is separate work.
- `bosun review` assumes one-time Kind/image/credential setup has completed. It
  should report missing prerequisites and the exact setup command, not silently
  install software, build an image, or replace cluster credentials per review.
  Credentials continue to come from the configured Kubernetes Secret. Changing
  `--provider` selects a provider for that job without a Helm upgrade when the
  corresponding credentials already exist.
- Add a documented local binary build/install target and release packaging so
  the command works from any directory without relying on repository-relative
  shell scripts. Preserve `bosun reviewer` for Jobs; add `bosun serve` while
  retaining the current no-argument server behavior for deployment compatibility.
- Make `make review` and `scripts/review-local.sh` thin compatibility entrypoints
  once the native CLI is ready, keeping one implementation of monitoring.
- Define exit statuses: 0 successful review (including findings), 1 execution
  failure, 2 invalid usage/configuration, 124 timeout, and 130 interruption.
  A findings-based CI failure policy is separate from execution success.

## Revised delivery order

1. Implement the worker event contract, lifecycle timing, immediate diagnostics,
   provider readiness checks, and input-acceptance validation.
2. Add the native `bosun review` CLI, snapshot/ref semantics, live Kubernetes
   monitoring, output rendering, and compatibility wrappers.
3. Add bounded session recovery, sequence/replay handling, status reconnection,
   detached-run cleanup, and durable terminal summaries.
4. Verify provider activity capabilities in the pinned image; add safe structured
   activity adapters or an upstream bridgectl extension only where needed.
5. Add history-based estimates after timing data is trustworthy. Include model
   and provider/image version when available so materially different runtimes
   are not treated as equivalent samples.

## Validation and acceptance criteria

Extend the fake gRPC and shell tests for slow/silent providers, stage transitions,
startup failure, stream disconnects/replay gaps, nonzero exits, timeouts, and
cancellation. Exercise Kubernetes pending/image/OOM/API-error reporting and log
reconnection. Test that secrets and status messages never enter final reviews.
Test cold-start estimates and history-based estimates with deterministic data.

Run the appropriate Go tests and shell checks, then a Kind smoke test with a
successful review and a controlled failure. Verify:

- Updates remain visible throughout startup and review execution.
- Elapsed time is accurate and includes launcher startup.
- Activity timestamps reflect provider events, not heartbeats.
- Failures include a reason and are reported within the monitoring interval
  once observable, without waiting for the overall timeout.
- The final review appears once, with status kept separate.
- Estimates disclose uncertainty; no fabricated percentage is shown.
- Reconnecting does not start a second review.
- Provider absence, stale sockets, daemon restart, rejected/partial input, writer
  conflict, and replay gaps produce explicit outcomes without duplicate prompts.
- Flags work before and after PATH; invalid commands do not launch the server.
- Explicit branch reviews use the requested SHA and correct base while leaving
  dirty source checkouts and linked worktrees unchanged.
- Context selection honors KUBECONFIG and never falls back to an unrelated
  cluster; unsupported snapshot backends fail before submitting a job.
- stdout contains only the final review in normal mode; JSON events are
  parseable and log replay does not duplicate results.
- Cancellation confirms termination before cleanup; detached runs retain their
  workspace and remain inspectable until explicit or verified-terminal cleanup.
