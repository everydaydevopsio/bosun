# ADR-002: Queue reviews at capacity as suspended Jobs, and sweep for missed deliveries

- **Status**: Accepted
- **Date**: 2026-11-10
- **Branch**: `issue-82-queue-reviews-at-capacity`, `issue-82-supersede-by-receipt`
- **PR**: [#88](https://github.com/everydaydevopsio/bosun/pull/88) (0.3.3), [#89](https://github.com/everydaydevopsio/bosun/pull/89) (0.3.4)
- **Issue**: [#82](https://github.com/everydaydevopsio/bosun/issues/82)
- **Supersedes**: none
- **Superseded by**: none

## Context

A review request could vanish without a trace (#82). At `review.maxConcurrent`
the webhook answered **503**, GitHub never redelivers a failed App webhook, and
the controller logged nothing for a capacity refusal. The pull request simply
had no review, which looks the same as a review still running.

On mca-cluster `maxConcurrent` is 1 and the cap is counted across the
namespace, so any repository's review could take the slot. On 2026-11-09,
`ballast#425` opened 8 seconds after a `bilge-pump` review started and got
nothing; `ballast#422` collided with `#421` the same way. A delivery could also
be lost before it reached the controller, for example during a rollout of the
single controller replica.

Two constraints shaped the fix:

- The cap exists to bound cluster load, so "raise the cap" trades one failure
  for another.
- GitHub will not retry, so anything not durably stored when the webhook is
  answered is gone.

## Decision

**1. Queue instead of refuse, using the Jobs themselves as the queue.**
A delivery that arrives at capacity creates its reviewer Job **suspended** with
label `bosun/queued=true`, and the webhook answers 200. A suspended Job starts
no pod, so node load stays bounded by `maxConcurrent`. The queue lives in the
API server, so it survives controller restarts and is shared by replicas with
no store of its own. `Active()` counts only unqueued, unfinished Jobs.

A `jobs.Dispatcher` (every `review.queue.intervalSeconds`, default 10) starts
the oldest queued Jobs as slots free. Each pass has two halves:

- **Outside the admission lease:** pick candidates and mint each one's
  repository token, each on its own `MintBudget`. GitHub calls never run
  under the lease, so a slow mint can't stall admission or the entries behind
  it.
- **Under the lease:** expire stale entries, then start the prepared Jobs that
  still fit. This half makes local API calls only.

The webhook path mints its token before admission, for the same reason.

**2. Order requests for one pull request by when they were received.**
Each Job carries `bosun/requested-at`, stamped when the webhook arrived, or when
the sweep read the head. A newer request supersedes queued (never running)
Jobs for the same PR. A request older than an unfinished Job for its PR is
itself dropped as superseded and answered as a duplicate. Admission order is
not used, because a slower token mint can admit an older request after a newer
one.

**3. Sweep for requests that never became a Job.**
Every 5 minutes, under its own lease, the controller lists the App's
installations, their repositories, and open PRs updated in the last 24 hours.
It requests a review for any non-draft head that:

- was updated more than 5 minutes ago (newer changes are left to their webhook);
- has no Job;
- has no Bosun publication (review or failure comment), matched by commit SHA.

The request goes through the normal webhook submission path, with delivery
`sweep-<pr>-<sha>`, so the Job name dedupes across passes and replicas. A PR
whose publication history can't be searched is skipped and reported, never
treated as unreviewed. A pass requests at most 10 reviews, and each commit at
most once per controller process.

**4. Keep a ready endpoint during rollouts.**
The chart defaults are 2 replicas, a native `lifecycle.preStop.sleep` of 10
seconds, and `terminationGracePeriodSeconds: 45` (pause plus the 25-second
drain). CI pins these defaults.

## Alternatives Considered

| Option | Why not |
| --- | --- |
| Raise `maxConcurrent` | Narrows the window, still drops bursts, and gives up the load bound the cap exists for. |
| In-memory queue in the controller | Lost on restart and not shared between replicas. |
| Rely on `Retry-After` or redelivery | GitHub does not redeliver failed App webhooks. |
| Mint tokens under the admission lease | A slow mint exhausted the transaction's budget, and its cleanup then ran on a dead context. It left an unlabelled suspended Job holding a slot forever (Bosun review of #88). |
| Supersede by admission order | An older request admitted late deleted the newer head's queued review (Bosun review of `8f6b75a`, fixed in 0.3.4). |
| Sweep by repository + SHA only | Two PRs sharing a head (same branch, different bases) are each owed a review. |
| `exec` a `sleep` in preStop | Depends on the image shipping a shell. The native sleep action needs nothing. |

## Consequences

**Positive**

- A delivery at capacity waits instead of disappearing. Only a full queue
  (`review.queue.maxQueued`, default 50) still answers 503, and that is logged
  at WARN.
- A delivery lost before it was stored is recovered within about 10 minutes
  for any PR updated in the last day.
- The queue is durable and replica-safe without a database.
- Cluster load is unchanged: queued reviews have no pods.

**Negative**

- Reviews can now wait. With `maxConcurrent: 1` a burst runs one at a time,
  and an entry waiting longer than `review.queue.timeoutSeconds` (default 7200)
  is dropped with an ERROR log.
- The sweep needs a GitHub App to list installations. It uses read-only
  installation tokens but calls GitHub every 5 minutes per installation.
- A request arriving between dispatcher passes can take a freed slot ahead of
  an older queued entry. The window is at most 10 seconds and starvation was
  not observed.
- Clock skew between replicas can misorder two requests received within that
  skew of each other.
- A PR still shows nothing while its review is queued. Tracked as #91.

## Implementation Notes

- Code: `internal/jobs/queue.go` (admission, starters, Dispatcher),
  `internal/sweep/`, `internal/review/scan.go`.
- Configuration:
  - `review.queue.{maxQueued,intervalSeconds,timeoutSeconds}`
  - `review.sweep.{intervalSeconds,lookbackSeconds,graceSeconds,maxPerPass}`
  - `replicaCount`, `preStopSleepSeconds`, `terminationGracePeriodSeconds`
- `maxQueued: 0` restores refusal at capacity, and `sweep.intervalSeconds: 0`
  disables the sweep.
- Logs: `bosun.queue` (`review queued`, `queued review started`, `superseded`,
  `expired`) and `bosun.sweep` (`requested a review the webhook never
  delivered`, at WARN).

## Verification

- Unit tests cover:
  - queued at capacity; full queue; FIFO dispatch;
  - supersede by receipt time, including an older request admitted last;
  - token minted outside the lease, and a slow mint not starving the queue;
  - start-failure isolation, expiry, and the queue disabled;
  - sweep dedupe across passes, replicas and PRs sharing a head;
  - the per-pass bound, an unknown publication state, an unsearchable PR not
    hiding the rest, and a sweep request dated at head-read time.
- Live on mca-cluster (`maxConcurrent: 1`, 2 replicas, 0.3.3 then 0.3.4):
  - The sweep's first pass recovered `ballast#422`, whose review published 36s
    later. Later passes did not request it again.
  - On 2026-11-10 08:18 UTC a review for `ballast#421` was queued while
    another ran, and started 44s later.
  - Both rollouts kept two of two controllers ready.

## Lessons Learned

- **A cap that refuses is a cap that loses work** when the caller never
  retries. The bound belongs on running pods, not on accepted requests.
- **Read the review on the final head before merging.** A loop cap limits how
  many review rounds get fixed, not which reviews exist. A third automatic
  review of #88 was missed, and its two findings shipped in 0.3.3 and needed
  0.3.4.
- **Fakes hide context bugs.** `fake.NewSimpleClientset` ignores context
  cancellation. The token-budget regressions only reproduced once the fake
  issuer honoured `ctx.Err()` and the tests asserted elapsed time.
- **Build dependencies on live mirrors are a release dependency.** The 0.3.3
  image build was blocked for over half an hour by an Ubuntu mirror mid-sync.
  It now installs from a dated snapshot on amd64. Bumping that snapshot is
  tracked as #90.
