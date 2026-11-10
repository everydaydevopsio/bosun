# Architecture Decision Records

Decisions that shaped this repository, with the context that produced them and
the consequences accepted. Plans in `plans/` graduate here when they merge.

Numbering is sequential and zero-padded. An ADR is never deleted: a reversed
decision is marked superseded and a new record is added.

| ADR | Decision | Status |
| --- | --- | --- |
| [001](001-cli-owned-cluster-lifecycle.md) | The CLI owns the local cluster and loads local provider credentials | Accepted |
| [002](002-queue-reviews-and-sweep-for-missed-deliveries.md) | Queue reviews at capacity as suspended Jobs, and sweep for missed deliveries | Accepted |
