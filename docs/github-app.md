# GitHub App authentication

A personal access token is the wrong credential for Bosun in production: it
cannot be narrowed per repository at run time, it never expires, and rotating it
breaks every repository at once. A GitHub App signs a short-lived JWT with its
private key, exchanges that for an **installation token scoped to the single
repository under review**, and that token expires after an hour.

Bosun mints one per review. When `BOSUN_GITHUB_APP_ID` and
`BOSUN_GITHUB_PRIVATE_KEY` are set it uses the App; otherwise it falls back to
`GITHUB_TOKEN`, so you can migrate without downtime.

## 1. Create the App

**Organization settings → Developer settings → GitHub Apps → New GitHub App**

| Field | Value |
| --- | --- |
| **GitHub App name** | `Bosun Reviewer` (must be globally unique) |
| **Homepage URL** | your repository URL |
| **Webhook → Active** | ✅ checked |
| **Webhook URL** | `https://bosun.example.com/webhooks/github` |
| **Webhook secret** | the value you generated with `openssl rand -hex 32` |

### Repository permissions

| Permission | Access | Why |
| --- | --- | --- |
| **Contents** | Read-only | clone the ref under review |
| **Pull requests** | Read & write | post the review comment |
| **Metadata** | Read-only | mandatory, granted automatically |
| **Checks** | Read & write | *optional* — required only for the Checks output that gives branch-only reviews a home |

Grant nothing else. Bosun never writes code, pushes, merges, or approves.

### Subscribe to events

- **Create** — branch creation
- **Pull request**
- **Issue comment**
- **Pull request review comment**

### Where can this App be installed?

"Only on this account" unless you are deliberately publishing it.

Create the App, then note the **App ID** shown at the top of its settings page.

## 2. Generate a private key

On the App's settings page: **Private keys → Generate a private key**. GitHub
downloads a `.pem` once — it cannot be retrieved later.

## 3. Install the App

**Install App → choose the account → Only select repositories**, and pick the
repositories Bosun may review. This selection is the real security boundary:
Bosun can only mint tokens for repositories where the App is installed.

## 4. Store the credentials

Replace the PAT-based secret with the App's credentials. Both live under the
same `bosun-github` secret, so nothing else in the chart changes:

```bash
kubectl -n bosun delete secret bosun-github --ignore-not-found
kubectl -n bosun create secret generic bosun-github \
  --from-literal=app-id='123456' \
  --from-file=private-key=/path/to/bosun-reviewer.private-key.pem
```

Keep the existing `--from-literal=token=...` alongside them during a migration;
Bosun prefers the App and falls back to the PAT only when minting fails.

Then update the webhook secret if you changed it:

```bash
kubectl -n bosun create secret generic bosun-webhook \
  --from-literal=secret='YOUR_WEBHOOK_SECRET' \
  --dry-run=client -o yaml | kubectl apply -f -
```

Restart to pick up the new secret:

```bash
kubectl -n bosun rollout restart deployment/bosun-bosun
```

## 5. Verify

Comment `@bridgectl review` on a test pull request and watch the reviewer Job:

```bash
kubectl -n bosun logs -l app.kubernetes.io/name=bosun-review --tail=50
```

A working App logs the mint before the clone:

```
level=INFO msg="minted installation token" logger=bosun.githubapp repo="acme/widget" installation=4242 expires="..."
```

If the App is not installed on the repository you get a clear error rather than
a confusing 404 on clone:

```
the GitHub App is not installed on acme/widget
```

And if the PAT fallback is still present, Bosun says so instead of failing:

```
level=WARNING msg="falling back to GITHUB_TOKEN" logger=bosun.githubapp repo="acme/widget" reason="..."
```

Once reviews post cleanly, delete the `token` key to complete the migration:

```bash
kubectl -n bosun patch secret bosun-github --type=json \
  -p='[{"op":"remove","path":"/data/token"}]'
```

## What this does not do yet

Bosun still posts reviews as pull-request conversation comments. The `checks`
permission above is requested so that publishing reviews through the Checks API
— which is what would finally give branch-created reviews a first-class GitHub
surface instead of leaving them in Job logs — needs no permission change when it
lands.

## Notes

- The App JWT is signed RS256 and expires in 9 minutes; GitHub rejects anything
  older than 10. Bosun backdates `iat` by 60s to tolerate clock drift.
- Installation tokens last an hour, comfortably longer than the 1800s review cap.
- The private key is stripped from the environment handed to the AI provider,
  along with `GITHUB_TOKEN` — see `SECRET_ENV` in `bosun/worker.py`.
