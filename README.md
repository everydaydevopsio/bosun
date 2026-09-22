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

Bosun accepts branch creation, selected pull-request lifecycle events, and `@bridgectl review` comments. A human, CI automation, or another AI agent can request a review without receiving the model credential.

## Why bridgectl

Bosun is orchestration, not another agent runtime. The same review primitive works locally through the bridgectl code-review example and remotely as a Kubernetes Job. Provider selection remains a bridgectl concern.

## Quick start

1. Build and publish this image.
2. Create the `bosun-webhook`, `bosun-github`, and `bosun-ai` Kubernetes Secrets.
3. Install `charts/bosun`.
4. Expose `/webhooks/github` over TLS.
5. Configure the GitHub webhook.
6. Comment `@bridgectl review` on a test PR.

See [docs/setup.md](docs/setup.md) for exact commands, permissions, Helm values, and security guidance.

## Current scope

The first working version posts PR reviews as conversation comments. Branch-created reviews run immediately but remain in Job logs until a PR exists. The next production hardening step is GitHub App authentication plus Checks API output, which removes the long-lived GitHub token and gives branch-only reviews a first-class GitHub surface.
