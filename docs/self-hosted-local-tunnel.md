# Running the GitHub App against a Kind cluster on your own machine

The same hosted path as [self-hosted-digitalocean.md](self-hosted-digitalocean.md)
— a GitHub App posting reviews to pull requests — but the cluster is Kind on
your laptop and a tunnel carries GitHub's webhooks to it. No cloud bill.

Use this to try the App flow before paying for a cluster, or to develop against
real deliveries. It is not a way to run a review service: it works only while
your machine is awake and the tunnel is up.

## Choosing a tunnel

GitHub needs a **stable HTTPS URL**. A webhook URL you have to re-enter every
time your tunnel restarts is the thing that makes this annoying, so the real
question is what a stable hostname costs.

| | Stable URL | Cost | Catch |
| --- | --- | --- | --- |
| **Cloudflare Tunnel** | yes, on your own domain | **free** | needs a domain with DNS on Cloudflare |
| **tunnelto.dev** | reserved subdomain | **$4/user/month** | project looks dormant — last commit 2022 |
| **ngrok** | ngrok-branded domain | **$10/month** | free tier's URL changes on every restart |

**Cloudflare Tunnel is the only genuinely free stable URL** here, and the
catch is mild if you already own a domain — the connector is free with no
bandwidth metering, and a *named* tunnel keeps its hostname across restarts.
([pricing](https://toolradar.com/tools/cloudflare-tunnel/pricing))

**tunnelto.dev** is cheaper than ngrok at
[$4/month to reserve up to 20 subdomains](https://www.tunnelto.dev/), and it is
open source, so you can self-host it and pay nothing but the server. Its
hosted free tier does not document a reserved subdomain, so treat a stable
hostname there as the paid feature. Weigh the dormancy before putting a webhook
you rely on behind it: the last tagged release was May 2021 and the last commit
September 2022, though the hosted service still runs.

**ngrok** is the quickest to start and the most expensive to make stable. Its
[free plan](https://ngrok.com/pricing) gives an assigned dev domain, 1 GB of
transfer and 20k requests, and shows an interstitial page; the $10/month
Hobbyist plan removes the interstitial and adds ngrok-branded domains. For a
one-off experiment the free tier is fine — you will just re-enter the webhook
URL each time.

## 1. Bring up the cluster with the controller

The CLI's own `bosun up` deliberately does **not** install the Helm chart,
because a local `bosun review` does not need a controller. The webhook path
does, so use the script:

```bash
cd /path/to/bosun
./scripts/kind-up.sh
```

That builds `bosun:dev`, loads it into Kind, and installs the chart in
development mode. Development mode has no webhook secret and fails signature
verification closed, which is wrong for this guide — reinstall with the secret
once you have one, in step 4.

## 2. Start the tunnel

Pick one. Each exposes the controller's Service on port 8080 locally:

```bash
kubectl -n bosun port-forward svc/bosun-bosun 8080:80
```

Leave that running, and in another terminal:

**Cloudflare Tunnel** — stable hostname, free:

```bash
cloudflared tunnel login
cloudflared tunnel create bosun
cloudflared tunnel route dns bosun bosun.example.com
cloudflared tunnel run --url http://localhost:8080 bosun
```

**tunnelto.dev** — `--subdomain` is the reserved name you pay for:

```bash
tunnelto --subdomain yourname --port 8080
```

**ngrok**:

```bash
ngrok http 8080            # free: the URL changes on every restart
ngrok http --domain=yourname.ngrok.app 8080   # paid: stable
```

Whichever you choose, the webhook URL is that hostname plus
`/webhooks/github`.

## 3. Register the GitHub App

Follow [github-app.md](github-app.md), with **Only on this account** and the
tunnel hostname as the webhook URL. Install it on one throwaway repository
first — this setup goes down whenever your laptop sleeps, and you do not want
that on anything you rely on.

## 4. Supply the secrets and reinstall

```bash
kubectl -n bosun create secret generic bosun-github \
  --from-literal=app-id='YOUR_APP_ID' \
  --from-file=private-key=/path/to/your-app.private-key.pem

kubectl -n bosun create secret generic bosun-webhook \
  --from-literal=secret='YOUR_WEBHOOK_SECRET'
```

`kind-up.sh` already created `bosun-ai` from whatever provider credentials it
found. Now reinstall without development mode so the webhook secret is required
and signatures are actually verified:

```bash
helm --kube-context kind-bosun upgrade --install bosun ./charts/bosun -n bosun \
  --set image.repository=bosun --set image.tag=dev \
  --set image.pullPolicy=IfNotPresent \
  --set review.image=bosun:dev \
  --set development.enabled=false

kubectl -n bosun rollout status deployment/bosun-bosun
```

## 5. Verify

```bash
curl -i http://localhost:8080/healthz                  # 200
curl -i -X POST https://YOUR_TUNNEL_HOST/webhooks/github   # 401
```

The 401 is the signature check refusing an unsigned request, through the tunnel
— which proves the whole path. Then redeliver a ping from the App's **Advanced**
tab, watch `kubectl -n bosun logs deployment/bosun-bosun -f`, and comment
`@bridgectl review` on a pull request in the installed repository.

## What will bite you

- **Your laptop sleeping** ends the tunnel and the cluster. GitHub retries for a
  while and then gives up; redeliver from the Advanced tab.
- **A rotating free-tier URL** means re-entering the webhook URL in the App
  settings every restart. This is the thing worth $4 or $10 a month.
- **The image is rebuilt, not pulled.** After changing Bosun, re-run
  `./scripts/kind-up.sh` or the reviewer runs stale code. `bosun review` checks
  this for local reviews; the webhook path does not.
- **Your real credentials are in this cluster.** A GitHub App private key and a
  model key sit in a Kind cluster on a laptop, reachable by anything with
  `kubectl` access to it. Use a throwaway repository and revoke the key when you
  are done experimenting.
- **`kubectl port-forward` dies quietly.** If deliveries stop arriving, check
  that it is still running before suspecting anything else.
