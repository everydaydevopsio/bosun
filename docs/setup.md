# Setup

Bosun receives GitHub webhooks and creates one Kubernetes Job per review. Each Job clones the requested ref, runs a bridgectl non-interactive agent session, and posts the result back to the pull request when a PR number is available.

## 1. Build and publish the image

```bash
docker build -t ghcr.io/YOUR_ORG/bosun:0.1.0 .
docker push ghcr.io/YOUR_ORG/bosun:0.1.0
```

Set both `image.repository/tag` and `review.image` to that image.

## 2. Create credentials

Generate a high-entropy webhook secret:

```bash
openssl rand -hex 32
```

For this first implementation, create a fine-grained GitHub token scoped only to repositories Bosun reviews. It needs **Contents: read** and **Pull requests: read/write**. Store it in Kubernetes rather than Helm values.

For Codex/OpenCode:

```bash
kubectl create namespace bosun
kubectl -n bosun create secret generic bosun-webhook --from-literal=secret='YOUR_WEBHOOK_SECRET'
kubectl -n bosun create secret generic bosun-github --from-literal=token='YOUR_FINE_GRAINED_GITHUB_TOKEN'
kubectl -n bosun create secret generic bosun-ai --from-literal=openai-api-key='YOUR_OPENAI_API_KEY'
```

For Claude, use:

```bash
kubectl -n bosun create secret generic bosun-ai --from-literal=claude-code-oauth-token='YOUR_CLAUDE_CODE_OAUTH_TOKEN'
```

Do not put provider keys, GitHub tokens, or the webhook secret in `values.yaml`.

## 3. Install

Create a values override:

```yaml
image:
  repository: ghcr.io/YOUR_ORG/bosun
  tag: 0.1.0
review:
  image: ghcr.io/YOUR_ORG/bosun:0.1.0
  provider: codex
ingress:
  enabled: true
  className: nginx
  host: bosun.example.com
  tlsSecretName: bosun-tls
```

Then install:

```bash
helm upgrade --install bosun ./charts/bosun -n bosun --create-namespace -f values.production.yaml
kubectl -n bosun rollout status deployment/bosun-bosun
```

The webhook endpoint is `https://bosun.example.com/webhooks/github`.

## 4. Configure GitHub

In each repository, or at the organization level, add a webhook:

- Payload URL: `https://bosun.example.com/webhooks/github`
- Content type: `application/json`
- Secret: the same value stored in `bosun-webhook`
- Events: **Branch or tag creation**, **Pull requests**, **Issue comments**, and **Pull request review comments**
- SSL verification: enabled

Bosun verifies `X-Hub-Signature-256` before accepting an event.

A review starts when:
- a branch is created;
- a PR is opened, reopened, synchronized, or marked ready for review; or
- someone comments `@bridgectl review` on a PR discussion or review comment.

Comment-triggered reviews resolve the current PR head at execution time.

## 5. Verify

```bash
kubectl -n bosun get pods
curl https://bosun.example.com/healthz
kubectl -n bosun get jobs
kubectl -n bosun logs job/REVIEW_JOB_NAME
```

Open a test PR and leave `@bridgectl review`. The resulting review appears as a PR conversation comment headed **Bosun code review**.

Branch-created reviews have no PR thread to post to, so their initial output is retained in the Job log. A production follow-up should publish these as GitHub Checks and attach the full review there.

## Security notes

The webhook Deployment can create Jobs only in its namespace. Review Jobs do not mount a Kubernetes service-account token, drop Linux capabilities, have bounded resources and runtime, and are automatically removed after the configured TTL.

Treat reviewed repositories as untrusted input. The AI is instructed not to modify code, but it can inspect repository text that contains prompt injection. For stronger isolation, add NetworkPolicies, a read-only clone handoff, dedicated review nodes/runtime class, and per-installation GitHub App credentials. A GitHub App is preferable to a long-lived token for a production installation.
