# Self-hosting Bosun on DigitalOcean

Run Bosun as a GitHub App against your own repositories, on the cheapest
Kubernetes cluster that will hold it: one node, no high availability.

This is the hosted path — you comment `@bridgectl review` on a pull request and
Bosun posts the review back. For reviewing a local checkout with no GitHub at
all, use [local-development.md](local-development.md) instead; for running the
hosted path from your own machine, see
[self-hosted-local-tunnel.md](self-hosted-local-tunnel.md).

## What it costs

| Item | Monthly | Notes |
| --- | --- | --- |
| DOKS control plane | **$0** | free on the standard (non-HA) plan; HA is $40 |
| 1 × `s-2vcpu-4gb` worker | **$24** | the size this guide recommends |
| Load balancer | **$12** | *optional* — see [Skipping the load balancer](#skipping-the-load-balancer) |
| Bandwidth | **$0** | 2,000 GiB/month included per node, then $0.01/GiB |
| Block storage | **$0** | Bosun stores nothing durable |
| Container registry | **$0** | the image is public on ghcr.io |
| **Total** | **$24–36** | $24 without a load balancer, $36 with one |

A domain is extra and not a DigitalOcean cost — roughly $10–15/year, about $1/month.

**The model is the real bill.** Every review spends provider tokens, and that
cost scales with how many reviews you run and how large the diffs are. A quiet
repository may cost less per month than the cluster; a busy one will cost more.
Nothing in this guide caps it — `review.maxConcurrent` limits concurrency, not
spend.

A `s-1vcpu-2gb` node at $12 halves the cluster bill and is a false economy: the
reviewer image unpacks to well over a gigabyte, the agent is a Node process, and
reviewer Jobs have no memory limits of their own yet. On 2 GB the reviewer and
the controller compete, and the controller is what loses.

Prices: [DigitalOcean Kubernetes pricing](https://www.digitalocean.com/pricing/kubernetes).

## What you are signing up for

One node means **reviews stop during a node upgrade or a node failure**, and the
controller goes with them. GitHub retries webhook deliveries, but a review
in flight when the node goes is lost. That is the trade for $24.

A reviewer Job runs an AI agent over repository content with model credentials
in its environment. **Install the App only on repositories whose contributors
you trust.** A pull request from someone you do not trust is a prompt-injection
vector, and the network policy in this guide limits the blast radius without
eliminating it.

## 1. Create the cluster

```bash
doctl kubernetes cluster create bosun \
  --region nyc3 \
  --node-pool "name=pool;size=s-2vcpu-4gb;count=1;auto-scale=false" \
  --ha=false

doctl kubernetes cluster kubeconfig save bosun
kubectl get nodes
```

`--ha=false` is the default and is the $0 control plane. `count=1` with
autoscaling off is the whole cluster.

## 2. Ingress and TLS

GitHub will only deliver webhooks to an HTTPS URL with a valid certificate, so
you need an ingress controller, a certificate, and a DNS record.

```bash
helm upgrade --install ingress-nginx ingress-nginx \
  --repo https://kubernetes.github.io/ingress-nginx \
  --namespace ingress-nginx --create-namespace

helm upgrade --install cert-manager cert-manager \
  --repo https://charts.jetstack.io --namespace cert-manager \
  --create-namespace --set crds.enabled=true
```

The ingress controller's Service is of type `LoadBalancer`, so DigitalOcean
provisions a $12/month load balancer and gives it an external IP:

```bash
kubectl -n ingress-nginx get svc ingress-nginx-controller -w
```

Point an `A` record for your hostname at that IP, then create the issuer:

```bash
kubectl apply -f - <<'EOF'
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: you@example.com
    privateKeySecretRef:
      name: letsencrypt-account-key
    solvers:
      - http01:
          ingress:
            ingressClassName: nginx
EOF
```

### Skipping the load balancer

To save the $12, run the ingress controller on the node's own network and point
DNS at the node's public IP:

```bash
helm upgrade --install ingress-nginx ingress-nginx \
  --repo https://kubernetes.github.io/ingress-nginx \
  --namespace ingress-nginx --create-namespace \
  --set controller.service.type=ClusterIP \
  --set controller.hostNetwork=true

kubectl get nodes -o wide   # EXTERNAL-IP is your A record target
```

The cost is real but so is the downside: **the node's IP changes when the node
is replaced**, which happens on upgrades and on failure, and your webhook stops
working until you update DNS. On a single-node cluster you have already accepted
the downtime; this adds a manual step to recovering from it. Choose the load
balancer if you would rather not think about it.

## 3. Register the GitHub App

Follow [github-app.md](github-app.md) for the App itself — permissions, events,
and the private key. Two notes specific to a personal account:

- Under **Where can this App be installed?**, choose **Only on this account**.
  A personal App installed on your own account is the smallest blast radius
  available.
- The webhook URL is `https://YOUR_HOST/webhooks/github`. You can register the
  App before the cluster answers; deliveries will fail until it does, and GitHub
  will let you redeliver them.

Keep the **App ID**, the downloaded `.pem`, and the webhook secret you chose.

## 4. Create the secrets

```bash
kubectl create namespace bosun

kubectl -n bosun create secret generic bosun-github \
  --from-literal=app-id='YOUR_APP_ID' \
  --from-file=private-key=/path/to/your-app.private-key.pem

kubectl -n bosun create secret generic bosun-webhook \
  --from-literal=secret='YOUR_WEBHOOK_SECRET'

# The provider you selected in values. codex-bosun takes one of these:
kubectl -n bosun create secret generic bosun-ai \
  --from-literal=openai-api-key='YOUR_OPENAI_API_KEY'
```

For `claude-bosun`, the key is `claude-code-oauth-token`, `anthropic-api-key`, or
`claude-credentials`. Only the selected provider's keys are mounted into a
reviewer Job, so a prompt injection in one review cannot read another provider's
credentials.

## 5. Install Bosun

Copy [`deploy/values-digitalocean.yaml`](../deploy/values-digitalocean.yaml) and
change the two marked values — your hostname and, if you named it differently,
your ClusterIssuer:

```bash
helm upgrade --install bosun \
  oci://ghcr.io/everydaydevopsio/charts/bosun --version 0.1.1 \
  -n bosun -f deploy/values-digitalocean.yaml

kubectl -n bosun rollout status deployment/bosun-bosun
kubectl -n bosun get ingress,certificate
```

The certificate takes a minute or two. `kubectl -n bosun describe certificate
bosun-tls` explains itself if it does not arrive.

## 6. Verify

```bash
curl -i https://YOUR_HOST/healthz            # 200
curl -i -X POST https://YOUR_HOST/webhooks/github   # 401: signature verification works
```

A 401 on an unsigned POST is the correct answer — it means the endpoint is up
and refusing forgeries. Then redeliver a ping from the App's **Advanced** tab
and watch:

```bash
kubectl -n bosun logs deployment/bosun-bosun -f
```

Comment `@bridgectl review` on a pull request in an installed repository, as a
repository owner, member, or collaborator:

```bash
kubectl -n bosun get jobs,pods -w
```

## Operating it

- **Logs**: `kubectl -n bosun logs deployment/bosun-bosun`. Failures name which
  part failed — setup, clone, provider, review, or publish — and the reviewer
  Job's exit code carries the same classification once logs expire. See the
  exit-code table in [local-development.md](local-development.md).
- **Upgrades**: `helm upgrade` with a new `--version`. The single node means a
  brief outage; GitHub retries deliveries.
- **Reviews cost money even when they fail.** A review that runs for 20 minutes
  and then fails to publish has still spent the tokens.
- **Teardown stops the bill**: `doctl kubernetes cluster delete bosun`, then
  check for a leftover load balancer in the DigitalOcean control panel — a
  Service of type `LoadBalancer` can outlive its cluster and keep charging.

## Known limits

- **Reviewer Jobs have no CPU or memory limits.** On a single node a runaway
  review can starve the controller. `review.maxConcurrent: 1` in the supplied
  values is the mitigation available today. Tracked in
  [#29](https://github.com/everydaydevopsio/bosun/issues/29).
- **A failed review tells the pull request nothing** — the failure lands in the
  Job, not on GitHub. Tracked in
  [#28](https://github.com/everydaydevopsio/bosun/issues/28).
- **Every reviewer Job receives the same GitHub token.** Per-repository,
  per-Job installation tokens are not implemented yet, so the token mounted into
  a review is as broad as the App's installation. A design for this exists in
  [#4](https://github.com/everydaydevopsio/bosun/pull/4).
- **No durable queue.** At capacity, deliveries are rejected with 503 rather
  than queued, and GitHub's retries are the only recovery.
