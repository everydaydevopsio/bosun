# Local development with Kind

You can exercise Bosun's Kubernetes reviewer without configuring GitHub, creating a webhook, or pushing the repository being reviewed anywhere.

## Requirements

Install Docker, Kind, kubectl, and Helm. Then choose an AI provider and export its credential.

Codex/OpenCode:

```bash
export OPENAI_API_KEY=...
export BOSUN_REVIEW_PROVIDER=codex
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
3. builds `bosun:dev`;
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

The script snapshots the selected repository into Kind's mounted development area, including its `.git` directory and current uncommitted changes. It creates a one-shot reviewer Job and waits for it to finish. The completed review is printed directly to your terminal.

This means a coding agent can trigger Bosun locally with:

```bash
/path/to/bosun/scripts/review-local.sh "$PWD"
```

The repository does not need a GitHub remote. Bosun does not push, commit, or modify the developer's original checkout.

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
