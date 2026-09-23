# Bosun

Bosun is an event-driven AI code-review controller built on [orchael/bridgectl](https://github.com/orchael/bridgectl).

It replaces the hosted-reviewer shape with infrastructure you control:

```text
developer / AI agent
        |
        | push branch or "@bridgectl review"
        v
      GitHub
        |
        | signed webhook
        v
  Bosun webhook service
        |
        | create Kubernetes Job
        v
 disposable reviewer pod
   | clone exact ref/SHA
   | bridgectl run --no-tty
   | configured AI provider
   v
 GitHub PR review comment
```

## Triggers

Bosun accepts branch creation, selected pull-request lifecycle events, and `@bridgectl review` comments on pull requests. A human, CI automation, or another AI agent can request a review without receiving the model credential.

Comment triggers are restricted to the author associations in `review.allowedAssociations` (`OWNER,MEMBER,COLLABORATOR` by default), and comments from bots are ignored so a review cannot trigger itself.

## Why bridgectl

Bosun is orchestration, not another agent runtime. The same review primitive runs locally through `scripts/review-local.sh` and remotely as a Kubernetes Job. Provider selection remains a bridgectl concern.

Bosun speaks bridgectl's gRPC `BridgeService` directly rather than shelling out: it starts a session, delivers the prompt with `WriteInput`, and ends the session with `StopSession`. See [docs/bridgectl.md](docs/bridgectl.md).

## Local development — no GitHub required

For the fastest development loop, Bosun includes a Kind environment that can review any local Git checkout, including uncommitted changes:

```bash
make deps && make setup
source ./scripts/activate.sh
make kind-up
make review REPO=~/src/project-to-review
```

Provider credentials are pulled from AWS Secrets Manager at run time; nothing
needs to be exported by hand.

The result is printed to the terminal. The reviewed repository does not need to exist on GitHub. See [docs/local-development.md](docs/local-development.md).

Use `codex-exec` (or `opencode`). The packaged `codex` provider runs Codex's
interactive TUI, so its output is screen repaints and Bosun rejects it rather
than post that to a pull request; `codex-exec` runs `codex exec` headless
instead. See [docs/bridgectl.md](docs/bridgectl.md#choosing-a-provider).

## Quick start

1. Build and publish this image, pinning the bridgectl version you run (see [docs/bridgectl.md](docs/bridgectl.md)).
2. Create the `bosun-webhook`, `bosun-github`, and `bosun-ai` Kubernetes Secrets.
3. Install `charts/bosun`.
4. Expose `/webhooks/github` over TLS.
5. Configure the GitHub webhook.
6. Comment `@bridgectl review` on a test PR as a repository owner, member, or collaborator.

See [docs/setup.md](docs/setup.md) for exact commands, permissions, Helm values, and security guidance.

## Operations

- `review.maxConcurrent` (default 3) caps simultaneous reviewer Jobs; further deliveries get a 503 rather than queueing.
- `review.timeoutSeconds` (default 1800) is the single timeout for a review; the Job deadline is derived from it.
- `logging.format` (`text` or `json`) and `logging.level` mirror bridgectl's slog setup.
- Production installs should use a GitHub App rather than a PAT — see [docs/github-app.md](docs/github-app.md).

## Current scope

The first working version posts PR reviews as conversation comments. Branch-created reviews run immediately but remain in Job logs until a PR exists. The next production hardening step is GitHub App authentication plus Checks API output, which removes the long-lived GitHub token and gives branch-only reviews a first-class GitHub surface.
