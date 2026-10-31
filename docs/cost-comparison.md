# Measuring Bosun's cost against GitHub Copilot

Compare the cost of a useful review on the same commit, including failed attempts,
repeat reviews, and infrastructure. Elapsed minutes alone do not measure model cost.
This guide records public USD list prices checked on **2026-10-27**. Preserve that
date with any calculation and refresh the sources before making pricing claims.

## Measurement workflow

1. Select a representative set of PRs, split by size, language, and risk. Record
   repository, PR number, base SHA, head SHA, and review configuration.
2. Run Bosun and Copilot against those same changes. Record Copilot's effort level
   and Bosun's provider, configured model, reasoning effort, and execution environment.
   Keep independent runs from seeing each other's findings.
3. Collect Bosun usage events and provider billing records. Keep unavailable values
   unknown; do not substitute zero. Account for unsuccessful runs and repeated reviews.
4. Collect Copilot AI usage and Actions billing for the same period, following the
   extraction instructions below. Label allocated costs separately from measured costs.
5. Have a reviewer assess unique actionable findings, false positives, missed seeded
   defects, and time spent checking suggestions. Report cost alongside review quality.
6. Compare both gross workload costs and the change in the customer's actual bill.
   Start with a pilot of 30–50 PRs, then expand if results vary substantially by cohort.

Useful dashboard measures include cost per successful review, cost per merged PR
including re-reviews, cost per validated finding, p50/p95 duration, failure rate,
cache-read share, and the percentage of runs with complete usage and cost evidence.
Treat a cohort with no validated findings as having an undefined cost per finding.
Calculate cost per validated finding as all cohort costs, including failed attempts,
divided by unique findings a human confirmed useful. Keep severity and false-positive
rates alongside that number; raw comment counts are not an equivalent denominator.

## Copilot: AI credits, minutes, and subscriptions

[GitHub's code-review documentation](https://docs.github.com/en/copilot/concepts/agent-capabilities/agent-powered-workflows-and-operations/code-review)
estimates the following AI usage. These are published ranges, not measured averages
or guaranteed prices for a particular PR.

| Review effort | Estimated AI-credit value per review |
| --- | ---: |
| Lite | $0.05–$1.00 |
| Balanced, GitHub's default | $0.25–$5.00 |

Actions execution is additional. GitHub chooses the review model internally and does
not disclose it; do not infer its token rate from a model selected for Copilot Chat.
See [Copilot models and pricing](https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing).

### Execution minutes

The [Actions runner price list](https://docs.github.com/en/billing/reference/actions-runner-pricing)
lists **$0.006 per minute** for standard Linux x64 with two cores. GitHub rounds
each job up separately to a whole minute. A 3m10s job therefore represents four
billed minutes, or **$0.024** before allowances. Larger runner rates differ.

Since [1 June 2026](https://github.blog/changelog/2026-04-27-github-copilot-code-review-will-start-consuming-github-actions-minutes-on-june-1-2026/),
private-repository code reviews consume the account's Actions allowance. Standard
hosted runners remain free in public repositories. Self-hosted review runners do
not consume Actions minutes, but their owner still pays infrastructure costs.
Larger runners are charged even in public repositories and cannot use included minutes.

Included minutes depend on the underlying GitHub plan, separately from Copilot:
Free organization: 2,000/month; Team: 3,000/month; Enterprise Cloud: 50,000/month.
These are shared with CI and other Actions workloads. See
[included product usage](https://docs.github.com/en/billing/reference/product-usage-included).

For illustration, four billed standard Linux minutes make the published ranges
**$0.074–$1.024 for Lite** and **$0.274–$5.024 for Balanced**, before allowances.
This arithmetic is not a forecast of a customer's invoice.

### Subscription allowances

Current [Copilot plans](https://docs.github.com/en/copilot/get-started/plans):

| Plan | Monthly subscription | Included AI credits | Credit face value |
| --- | ---: | ---: | ---: |
| Pro | $10 | 1,500 | $15 |
| Pro+ | $39 | 7,000 | $70 |
| Max | $100 | 20,000 | $200 |
| Business | $19/seat | 1,900/seat | $19/seat |
| Enterprise | $39/seat | 3,900/seat | $39/seat |

One AI credit is $0.01. Individual totals include variable flex allotments.
Business/Enterprise credits pool at the billing entity; unused credits expire.
See [individual billing](https://docs.github.com/en/copilot/concepts/billing-and-usage/individuals/billing)
and [organization billing](https://docs.github.com/en/copilot/concepts/billing-and-usage/organizations-and-enterprises/billing).

An existing subscription may cover a review's incremental AI charge. Replacing
reviews may leave the subscription unchanged if developers still use Copilot for
other work. Avoid counting both the subscription and the full face value of its
included credits as additional cash expenditure.

Billing attribution also matters: reviews for unlicensed organization members can
be billed directly as paid additional usage. Do not assume every review draws from
the licensed-user pool; check the code-review policy and actual billing source.

Some existing annual Pro/Pro+ customers remain on legacy request billing. The
[legacy review multiplier](https://docs.github.com/en/copilot/reference/copilot-billing/request-based-billing-legacy/model-multipliers-for-annual-plans)
is currently 13 premium requests per review. Check the account's billing model
before applying the AI-credit comparison.

## OpenAI API model costs

[OpenAI's Standard API prices](https://developers.openai.com/api/docs/pricing),
in USD per million tokens for requests with at most 272,000 input tokens:

| Model | Ordinary input | Cache read | 30m cache write | 12h cache write | Output |
| --- | ---: | ---: | ---: | ---: | ---: |
| `gpt-6-luna` | $0.10 | $0.01 | $0.125 | $0.25 | $0.50 |
| `gpt-6.1-terra` | $1.00 | $0.05 | $1.25 | $2.50 | $5.00 |
| `gpt-6.1-sol` | $2.00 | $0.10 | $2.50 | $5.00 | $10.00 |
| `gpt-6-astra` | $10.00 | $1.00 | $12.50 | $25.00 | $50.00 |

For these models, a request above 272,000 input tokens uses 2× input/cache rates
and 1.5× output rates for the **whole request**. Evaluate this threshold per request,
not against the summed tokens of a multi-turn review. See the
[model pricing details](https://developers.openai.com/api/docs/models/gpt-6.1-sol)
and the pricing page's long-context table. Processing tier, region, tools, and
contract discounts can change the applicable rates.

For OpenAI API usage, input tokens include cache reads and cache writes. Following
the [prompt-caching accounting guidance](https://developers.openai.com/api/docs/guides/prompt-caching):

```text
ordinary_input = input_tokens - cache_read_tokens - write_30m_tokens - write_12h_tokens
request_model_cost_usd = (
    ordinary_input * input_rate
  + cache_read_tokens * cache_read_rate
  + write_30m_tokens * write_30m_rate
  + write_12h_tokens * write_12h_rate
  + output_tokens * output_rate
) / 1_000_000
review_model_cost_usd = sum(request_model_cost_usd across all attempts and turns)
```

Use the provider's billed output count, including reasoning tokens when they are
already included. Do not add them twice. Preserve request boundaries, model,
service tier, cache-write lifetime, and price date when estimating costs.
Missing cache-write data prevents an exact calculation for these API models.

### Hypothetical token scenarios

The following calculations assume 50,000 total input tokens and 5,000 billed output
tokens, Standard API pricing, short requests, and no tool or infrastructure charges.
They are workload illustrations, not Bosun measurements or equivalent-quality claims.

| Model | 50k ordinary input, no cache | 10k ordinary + 40k cache reads | 50k written to 30m cache |
| --- | ---: | ---: | ---: |
| `gpt-6-luna` | $0.0075 | $0.0039 | $0.00875 |
| `gpt-6.1-terra` | $0.0750 | $0.0370 | $0.08750 |
| `gpt-6.1-sol` | $0.1500 | $0.0740 | $0.17500 |
| `gpt-6-astra` | $0.7500 | $0.3900 | $0.87500 |

The cached example excludes the earlier write that populated the cache; charge that
write to the run or period that incurred it. A real agent may send the same context
many times, so the diff size alone does not determine total input usage.

Codex authenticated through a ChatGPT subscription has different allowance/credit
accounting. These API prices apply to API-key billing; they do not establish the
cash cost of subscription-backed reviews. See [Codex pricing](https://developers.openai.com/codex/pricing).

## Include Kubernetes and operating costs

For a dedicated deployment, allocate fixed monthly infrastructure across successful
reviews, including the capacity consumed by failed attempts:

```text
infra_usd_per_success = monthly_allocated_infrastructure_usd / successful_reviews
fully_loaded_usd_per_success = (
    provider_spend_for_all_attempts + allocated_infrastructure + operating_costs
) / successful_reviews
```

For example, an assumed $60/month deployment contributes $0.60 per successful
review at 100 reviews, $0.06 at 1,000, and $0.006 at 10,000. Those are illustrative
budgets, not a cloud quote. Include node, control-plane, storage, network, and logging
costs where incurred. For a shared cluster, document the allocation basis and idle
capacity separately. Do not add per-minute compute estimates on top of the same
monthly infrastructure allocation.

Report incremental cash savings separately: subtract the bill with Bosun from the
bill without it, keeping existing subscriptions and available credits consistent.

## Extracting actual Copilot billing

1. In Actions metrics, filter to `copilot-pull-request-reviewer`.
2. Download the detailed metered-usage report and filter `workflow_path` to
   `dynamic/agents/copilot-pull-request-reviewer`. Preserve repository, UTC date,
   quantity, unit rate, gross amount, discount amount, and net amount.
3. Download the AI usage report for the same UTC period. It provides model/user/day
   aggregates and input, output, cache-read, and cache-write counts.
4. Reconcile gross usage with discounts and the final net billed amount. Keep seat
   charges separate, and record whether the user or organization paid for the review.

GitHub documents the workflow filters in
[Copilot pricing](https://docs.github.com/en/copilot/reference/copilot-billing/models-and-pricing).
The [billing-report reference](https://docs.github.com/en/billing/reference/billing-reports)
defines the fields: `net_amount = gross_amount - discount_amount`. Detailed reports
containing `workflow_path` require the web interface; REST `/usage` is summarized.

For automated AI-credit totals, the [billing REST API](https://docs.github.com/en/rest/billing/usage)
provides `GET /organizations/{org}/settings/billing/ai_credit/usage`, with date, user,
model, and product filters. It requires organization Administration read permission.
Preserve `grossQuantity`, `grossAmount`, `discountAmount`, and `netAmount`.
This endpoint has no documented PR identifier or review-feature dimension.

[GitHub's monitoring guidance](https://docs.github.com/en/copilot/how-tos/manage-and-track-spending/monitor-ai-usage)
says AI usage generally cannot be traced to individual prompts, sessions, or exact
features. Use a controlled review-only account/cohort/time window where possible.
Otherwise, label per-review AI charges as allocations and disclose the method.

## Capture Bosun usage

The worker image containing this change captures usage automatically. Update the
worker image as well as the CLI: an older worker continues to report unknown usage.
There are no model API calls made solely for metrics, and this change does not
select a different model.

Every worker exit it can handle emits an info-level structured log with
`event: "bosun.review.usage"` and a `metrics` object. Hosted and local reviewer Jobs
set `BOSUN_LOG_FORMAT=json`. A directly launched `bosun reviewer` should use that
setting too. Keep info-level logging enabled when collecting these summaries.

```sh
kubectl logs -n bosun job/REVIEW_JOB_NAME |
  jq -R 'fromjson? | select(.event == "bosun.review.usage") | .metrics'
```

Local reviews also send the same object as a `usage` event, before the terminal
result. The existing monitor persists it alongside the report. After following a
review to completion, retrieve it without depending on live Kubernetes logs:

```sh
bosun review-status REVIEW_JOB_NAME --follow
bosun review-result REVIEW_JOB_NAME --json | jq '.usage'
```

Older records return `usage: null`. The report remains readable if usage data is
absent or uses an unsupported schema. Normal human-readable review output does not
include the telemetry envelope.

### Measurement fields

| Field inside `metrics` | Meaning |
| --- | --- |
| `schema_version`, `run_id`, `pod_uid` | Schema and correlation; deduplicate collected summaries by run and pod attempt |
| `repo`, `ref`, `head_sha`, `base_sha`, `pr_number` | Reviewed revision and PR where known |
| `provider`, `worker_version`, `worker_image` | Implementation used for the measurement |
| `credential_mode` | Presence of API keys, saved credentials, both, or unknown; a hint, not confirmed billing selection |
| `outcome`, `failure_stage` | Completed, failed, timed out, or cancelled; failure stage when available |
| `worker_seconds` | Worker elapsed time, including repository preparation and publication; excludes scheduling and image pulling |
| `provider_seconds`, `provider_started` | Agent-call elapsed time, including bridge startup; whether invocation was attempted |
| `usage.source`, `usage.status` | Codex/Claude/unknown; available, partial, or missing token usage |
| `usage.model`, `usage.model_usage` | Provider-reported model and optional per-model totals; unknown models remain null |
| `usage.input_tokens` | Total observed input, including cache reads and writes |
| `usage.cached_input_tokens`, `usage.cache_write_input_tokens` | Cache breakdown, only when reported |
| `usage.cache_write_30m_tokens`, `usage.cache_write_12h_tokens` | OpenAI lifetime categories when reported; Claude's different lifetimes are not mapped to them |
| `usage.output_tokens`, `usage.reasoning_output_tokens` | Total output and its reasoning subset; do not add the subset again |
| `usage.total_tokens`, `usage.turns` | Observed total input plus output, and completed turns when reported |
| `usage.provider_reported_cost_usd`, `usage.cost_basis` | Optional CLI estimate and known pricing basis; never actual invoiced dollars |
| `cost_status`, `actual_cost_usd` | Provider-reported estimate or unavailable; actual billed cost remains null pending reconciliation |

`available` describes reported token totals, not complete pricing evidence. For
example, Codex can report total input/output while omitting model, service tier,
cache writes, and per-request context size. Its cost stays unknown. Missing numeric
fields are null; reported zero counts remain zero. No raw provider JSON, prompts,
tool results, or source snippets enter the usage record.

Claude normalization prefers `modelUsage`, which includes helper/subagent calls.
Its main-loop `usage` alone is marked partial. The adapter retains previous known
counts after an execution crash rather than replacing them with zeroed crash data.
An explicit unknown model price makes the estimate null, even when the CLI returned
zero. [Claude's cost-tracking documentation](https://code.claude.com/docs/en/agent-sdk/cost-tracking)
describes these scope and pricing distinctions.

### Build metrics from these records

Start with attempt count, successful review count, failure rate, p50/p95 worker
and provider duration, observed input/output/cache totals, and usage coverage.
Calculate cache-read share only over records where both input and cached counts
exist. Record the coverage denominator beside every token or cost aggregate.

Treat partial counts as observed lower bounds where counters exist. Never coalesce
unknown cost to zero. Provider cost estimates may also be partial; do not label
`cost_status: provider_reported_estimate` as fully priced. Keep provider and model
as metric labels; keep repository, PR, SHA, and run IDs in logs to avoid excessive
metric cardinality.

A pod killed before graceful completion, an image-pull failure, or an admission
rejection may produce no worker summary. Join worker logs with Kubernetes Job/pod
outcomes and controller submission logs to count these gaps. Store hosted logs
before the Job TTL removes them. CPU/memory resource requests are reservations,
not measured resource consumption or an invoice.

## Limits

- Bosun's telemetry captures measured usage and provider-reported cost estimates
  when available. Missing data remains null; a reported estimate is not an invoice.
- This change does not automatically price Codex usage. Its CLI events do not provide
  all required model, processing-tier, and cache-write detail for reliable API costing.
- Logs alone do not measure Kubernetes expenditure, subscription consumption,
  review quality, or human time. Combine them with billing and assessment records.
- Agent failures can occur before a usage summary is emitted. Missing usage can mean
  unobserved spend. Report coverage and reconcile totals before publishing savings.
- Compare only matching review scope and base/head commits. More findings can mean
  more false positives; validate usefulness before claiming a cost advantage.
- Rates and billing policies change. Preserve the observed price version and refresh
  source links before using this guide for product pricing or marketing.
