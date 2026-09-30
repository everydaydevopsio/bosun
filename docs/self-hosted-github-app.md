# Run a Bosun GitHub App on your Kubernetes cluster

This is the **single-organization, trusted-repository** deployment. It is not a
multi-tenant SaaS and it does not run the control plane for Orchael Reviewer.
No GitHub App, DNS record, TLS certificate or live cluster is created by opening
this PR. Those external operations require the operator's credentials.

## Prerequisites

Use a dedicated namespace on an existing Kubernetes cluster, an ingress
controller, working public DNS, a TLS Secret named `bosun-tls`, a CNI that enforces
NetworkPolicy, Docker/buildx, Helm, kubectl and a model-provider API credential.
The example assumes an ingress class called `nginx`; change the values for your
cluster. Verify the NetworkPolicy with your actual Pod/Service CIDRs and DNS
implementation. It allows broad public HTTPS, not only approved API hostnames.

Register and install an App using [github-app.md](github-app.md). Subscribe to PR
and comment events first; leave branch creation off until you want reviews whose
only output is in Job logs. Enable access only to selected trusted repositories.
Automatic PR events do not currently implement a maintainer-approval gate for
fork contributions: do not enable this on repositories accepting hostile code.

## Build the exact code you reviewed

Before this work is merged, `main` does not contain the implementation. Check out
the deployment PR branch rather than installing from `main`.

```bash
export BOSUN_IMAGE_REPOSITORY=ghcr.io/everydaydevopsio/bosun
export BOSUN_IMAGE_TAG="sha-$(git rev-parse --short=12 HEAD)"
docker build --build-arg BRIDGECTL_VERSION=v1.3.0 \
  -t "$BOSUN_IMAGE_REPOSITORY:$BOSUN_IMAGE_TAG" .
docker push "$BOSUN_IMAGE_REPOSITORY:$BOSUN_IMAGE_TAG"
```

The existing base pins bridgectl v1.3.0. Do not silently switch it to another
version: test that version's image, provider paths and gRPC behavior first. The
controller and reviewer must use the same Bosun build. Make the image readable
by cluster nodes; configure registry credentials out-of-band for private images.
A tag named after a commit is a release convention, not cryptographic immutability.
After promotion, use registry immutability policy and record the image digest.

## Supply credentials and deploy

Keep secret files outside the repository. Generate the webhook value **without a
trailing newline**, and paste the same value into the App's webhook settings.
The helper uses the explicitly named kubeconfig context, not the current default.

```bash
umask 077
mkdir -p "$HOME/.config/bosun"
printf '%s' "$(openssl rand -hex 32)" > "$HOME/.config/bosun/webhook-secret"
export BOSUN_KUBE_CONTEXT=your-cluster-context
export BOSUN_HOST=bosun.your-domain.example
export BOSUN_APP_ID=123456
export BOSUN_PRIVATE_KEY_FILE="$HOME/.config/bosun/github-app.pem"
export BOSUN_WEBHOOK_SECRET_FILE="$HOME/.config/bosun/webhook-secret"
export BOSUN_REVIEW_PROVIDER=codex-bosun
# Set OPENAI_API_KEY securely in the shell, or pre-create bosun-ai.
./scripts/deploy-self-hosted.sh
```

For Claude use `BOSUN_REVIEW_PROVIDER=claude-bosun` and `ANTHROPIC_API_KEY`.
The helper updates App/webhook Secrets, removes a legacy PAT fallback, installs
Helm and restarts the controller for credential rotation. It never commits secret
values. Existing AI secrets can be provided with External Secrets or another
secret manager instead of shell environment variables.

Create DNS and the `bosun-tls` certificate before the live smoke test. Example
manual TLS import (skip when cert-manager owns this Secret):

```bash
kubectl --context "$BOSUN_KUBE_CONTEXT" -n bosun create secret tls bosun-tls \
  --cert=/secure/path/fullchain.pem --key=/secure/path/privkey.pem
```

## Acceptance test: a real PR

1. Verify `https://YOUR_HOST/healthz` responds and TLS validates. Health is process
   liveness, not proof that GitHub, credentials or the model work.
2. Send a GitHub App ping from its delivery page. Invalid HMAC requests must fail.
3. As an owner/member/collaborator, comment `@bridgectl review` on a small trusted
   test PR. Confirm the webhook delivery succeeded.
4. Inspect the Job, wait for completion and verify the review comment appears on
   that exact PR. A green image build is not a substitute for this test.
5. Redeliver the same webhook while the Job still exists: no second Job/token
   should be created. Confirm the token Secret has a Job owner reference and
   that the pod environment references no App ID/private-key Secret.
6. Rotate the App key and webhook secret, restart the controller and repeat.

```bash
kubectl --context "$BOSUN_KUBE_CONTEXT" -n bosun get jobs
kubectl --context "$BOSUN_KUBE_CONTEXT" -n bosun logs deployment/bosun-bosun --tail=100
kubectl --context "$BOSUN_KUBE_CONTEXT" -n bosun logs job/REVIEW_JOB --tail=100
kubectl --context "$BOSUN_KUBE_CONTEXT" -n bosun get job REVIEW_JOB -o yaml
```

Never print Secret data or paste model output from a confidential review into a
public issue. Remote jobs now request 250m CPU/512Mi memory/1Gi temporary storage
and are limited to 2 CPU/2Gi memory/4Gi temporary storage. These are code defaults
in `hardenRemoteJob`, not configurable chart values yet. Add a namespace quota
appropriate to the configured concurrency. Pods have no Kubernetes API token.

## Known operational limits

Bosun submits Jobs synchronously, rather than writing a durable queue. At capacity
it returns 503; **GitHub does not automatically redeliver failed webhooks**. Check
the App delivery page and explicitly redeliver failures after capacity returns.
An interrupted admission can also leave a suspended Job; inspect and delete that
Job before redelivery. Deduplication lasts only as long as the Job exists, so a
redelivery after Job TTL cleanup can start another review.

Reviews are advisory conversation comments, not approval decisions, inline
findings or GitHub Checks. There is no durable audit/result store, stale-head
publication guard or comprehensive tenant boundary. Keep scope to a small,
trusted installation. See [the readiness review](readiness-review-2026-09-29.md)
for the remaining release gates, including the live Kind/model smoke test.

Rollback: use `helm history bosun -n bosun` then `helm rollback bosun REVISION -n
bosun --wait` with your explicit kube context. Disable the GitHub App webhook
before uninstalling. `helm uninstall` does not necessarily remove dynamically
created review Jobs; list/cancel them deliberately, and let their owner-linked
credential Secrets be collected. Do not delete a shared namespace.

Reference: https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries
