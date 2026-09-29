# Local development with Kind

You can exercise Bosun's Kubernetes reviewer without configuring GitHub, creating a webhook, or pushing the repository being reviewed anywhere.

## Requirements

Install Go 1.26 or newer for local builds and tests. Run `make setup` to check
development tools (Docker, Kind, kubectl, Helm and
shellcheck), installing missing tools through Homebrew when available.
Setup does not fetch credentials or write `.env`.

```bash
make setup
```

Configure credentials manually by exporting the variables for your provider
before `make kind-up`, using the examples below. `make kind-up` copies the
exported credentials into the local cluster's `bosun-ai` Secret; reviews use
that Secret without fetching credentials from an external service. After
changing credentials, run `make kind-up` again to update the cluster.

Recognised credentials:

| Variable | Used by | Delivered as |
| --- | --- | --- |
| `OPENAI_API_KEY` | codex, opencode | environment variable |
| `CODEX_AUTH` | codex | written to `~/.codex/auth.json` |
| `CLAUDE_CODE_OAUTH_TOKEN` / `ANTHROPIC_API_KEY` | claude | environment variable |
| `CLAUDE_CREDENTIALS` | claude | written to `~/.claude/.credentials.json` |
| `GEMINI_API_KEY` | gemini | environment variable |

Pick a provider that produces structured output — see
[bridgectl.md](bridgectl.md#choosing-a-provider). Use `codex-exec` or `opencode`;
`codex` renders a terminal UI and its output is rejected.

Codex/OpenCode:

```bash
export OPENAI_API_KEY=...
export BOSUN_REVIEW_PROVIDER=codex-exec
```

Claude:

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...
export BOSUN_REVIEW_PROVIDER=claude
```

## Start the environment

From the Bosun repository:

```bash
./scripts/kind-up.sh
```

The script:

1. creates a `bosun` Kind cluster;
2. mounts `/tmp/bosun-repos` from the host into the Kind node;
3. builds `bosun:dev` against the pinned bridgectl version (override with `BRIDGECTL_VERSION`);
4. loads the image directly into Kind, so no registry is required;
5. creates only the AI-provider Kubernetes Secret;
6. installs the Bosun Helm chart in development mode.

No GitHub credential or webhook secret is required.

## Review any local Git repository

```bash
./scripts/review-local.sh ~/src/my-project
```

Or review the current directory:

```bash
./scripts/review-local.sh .
```

The script snapshots the selected repository into Kind's mounted development area, including its `.git` directory and current uncommitted changes. It creates a one-shot reviewer Job, waits for it to succeed or fail, prints the review to your terminal, and exits non-zero if the Job failed — so a calling agent can tell a failed run from a clean review.

This means a coding agent can trigger Bosun locally with:

```bash
/path/to/bosun/scripts/review-local.sh "$PWD"
```

The repository does not need a GitHub remote. Bosun does not push, commit, or modify the developer's original checkout.

## Tuning the session

The reviewer holds bridgectl's stdin open until the provider's output goes quiet.
Two environment variables on the Job control that:

- `BOSUN_REVIEW_IDLE_SECONDS` (default 90) — quiet period that ends the session.
- `BOSUN_REVIEW_MAX_SECONDS` (default 1500) — hard cap on a single review.

Lower both when iterating with the credential-free `echo` provider:

```bash
BOSUN_REVIEW_PROVIDER=echo ./scripts/review-local.sh .
```

## Inspecting a run

While a review is running:

```bash
kubectl -n bosun get jobs,pods
kubectl -n bosun logs -f job/<job-name>
```

## Rebuild after changing Bosun

Run `./scripts/kind-up.sh` again. It rebuilds and reloads `bosun:dev` and upgrades the Helm release.

## Remove everything

```bash
./scripts/kind-down.sh
```

This deletes the Kind cluster and the temporary repository snapshots under `/tmp/bosun-repos`.
