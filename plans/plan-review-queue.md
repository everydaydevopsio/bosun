# Plan: Queue reviews at capacity instead of refusing them

- Status: Implemented; live verification pending
- Branch: `issue-82-queue-reviews-at-capacity`
- Created: 2026-11-10
- Related: #82, ADR none yet

## Problem

At `review.maxConcurrent` a delivery was refused with 503, and GitHub never
redelivers a failed App webhook, so the review was lost without a trace.
Observed 2026-11-09 on mca-cluster (`maxConcurrent: 1`): `ballast#425` opened
8s after a `bilge-pump` review took the only slot and got no review; `ballast#422`
collided with `#421` the same way. The controller logged nothing.

## Approach

The queue is the Job objects. A delivery at capacity creates its reviewer Job
**suspended** and labelled `bosun/queued=true` (no pod, so no node load), and
answers 200. A `jobs.Dispatcher` loop in the controller, under the existing
admission lease, starts the oldest queued Jobs as slots free. Durable across
controller restarts and shared by replicas with no new store.

- `Active()` excludes queued Jobs; `maxConcurrent` still bounds running pods.
- Per-repository tokens are minted when a Job **starts**, not when queued, so a
  queued review never holds a token that expires while it waits.
- A newer request for the same repo + PR deletes its queued (never running)
  predecessor.
- Bounded: `review.queue.maxQueued` (default 50); a full queue still answers 503,
  now logged at WARN. `review.queue.timeoutSeconds` (default 7200) drops stale
  entries with an ERROR log. `maxQueued: 0` restores the old refusal.
- A start failure (e.g. token mint) leaves the entry queued for retry and moves
  on to the next, so one broken repository cannot block the queue.

### Delivery hardening and sweep (added 2026-11-10)

- `replicaCount: 2`, `preStopSleepSeconds: 10` (native `lifecycle.preStop.sleep`,
  k8s ≥1.30), `terminationGracePeriodSeconds: 45` so a rollout never leaves the
  webhook without a ready endpoint.
- `internal/sweep`: every 5 min, under a `bosun-missed-review-sweep` lease, list
  App installations → repos → open PRs updated within 24h and not within 5 min;
  skip drafts, heads with a Job, and heads Bosun already published for (matched
  by SHA only); submit through the webhook path with delivery `sweep-<sha>` so
  the Job name dedupes across passes and replicas. At most 10 per pass; each
  commit at most once per controller process.

### 0.3.4 follow-up (added 2026-11-10)

The Bosun review of `8f6b75a` landed before #88 merged and was missed; its two
findings shipped in 0.3.3:

- **Supersede by receipt time, not admission order.** Tokens are minted before
  admission, so a slower mint could admit an older request after a newer one
  and delete the newer head's queued review. Each Job now carries
  `bosun/requested-at` (stamped when the webhook arrived, or the sweep pass
  time), and a request older than an unfinished Job for its PR is dropped as
  superseded (answered 200, duplicate). Clock skew between replicas can only
  misorder requests received within that skew of each other.
- **Sweep skips an unsearchable PR instead of ending the repository scan.**

## Files Affected

- `internal/jobs/queue.go` — admission transaction, starters, Dispatcher
- `internal/jobs/jobs.go`, `internal/jobs/authenticated.go` — route through `admit`
- `internal/config/config.go` — `MaxQueuedReviews`, `QueueIntervalSeconds`, `QueueTimeoutSeconds`
- `cmd/bosun/main.go` — start the dispatcher
- `internal/server/server.go` — log capacity refusals
- `charts/bosun/values.yaml`, `templates/deployment.yaml` — `review.queue.*`
- `README.md`, `docs/github-app.md` — behaviour docs
- `internal/review/scan.go`, `internal/sweep/` — missed-review sweep
- `.github/workflows/ci.yml` — pin queue, sweep, preStop, grace, replicas defaults

## Phases

- [x] Explore admission, sentinel, and the dropped-delivery evidence
- [x] Core: queue on suspended Jobs, dispatcher, token minted at start
- [x] Tests: queued at capacity, full queue, FIFO dispatch, supersede, token at
      start, start failure does not block, expiry, queue disabled
- [x] Docs and chart values
- [x] Replicas 2, preStop sleep, grace period; sweep with tests
- [ ] Release and bump mca-cluster pins; verify a burst of PRs all get reviews

## Verification

- `go test ./...` green; `golangci-lint run --new-from-rev=main` 0 issues; `helm lint`.
- Live: open two PRs within seconds on a `maxConcurrent: 1` install; both get
  reviews, controller logs `review queued` then `queued review started`.

## Alternatives Rejected

- **Raise `maxConcurrent`** — narrows the window but still drops bursts, and
  trades away the load bound.
- **In-memory queue in the controller** — lost on restart, not shared by replicas.
- **Rely on redelivery / `Retry-After`** — GitHub does not redeliver App webhooks.
- **Exec `sleep` in preStop** — depends on the image shipping a shell; the
  native sleep action needs nothing.

## Open Questions

- A new arrival can take a slot freed between dispatcher passes ahead of an
  older queued entry (≤10s window). Accepted; no starvation in practice.

## Change Log

| Date | Change |
| --- | --- |
| 2026-11-10 | Plan created and implemented |
| 2026-11-10 | Added replicas/preStop and the missed-review sweep (#82 option 3) to this PR |
| 2026-11-10 | 0.3.4: supersede by receipt time; sweep continues past an unsearchable PR |
