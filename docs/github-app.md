# GitHub App authentication

Use [the self-hosted deployment guide](self-hosted-github-app.md) for the full
cluster workflow. The App signing key now belongs **only to the controller**.
The controller mints a one-repository installation token, creates a suspended
Job and an immutable Secret owned by that Job, then starts the Job. Kubernetes
garbage collection removes the token Secret with its owner.

## Register and install

Create an organization-owned GitHub App with a globally unique name:

| Setting | Value |
| --- | --- |
| Homepage | Your Bosun deployment or repository |
| Webhook URL | `https://YOUR_HOST/webhooks/github` |
| Webhook secret | A randomly generated value stored out-of-band |
| Contents permission | Read |
| Pull requests permission | Read/write |
| Metadata permission | Read (automatic) |
| Events | Pull request, issue comment, pull request review comment |
| Installation visibility | Only this account for self-hosting |

Optionally subscribe to Create, but branch-only results currently stay in Job
logs. Do not request Checks permission: Bosun does not publish Checks yet.
Install on **selected trusted repositories**, not every repository by default.
Record the App ID and generate a PEM private key. App registration cannot be
completed by merely applying Kubernetes manifests.

Store `app-id` and `private-key` in `bosun-github`, `secret` in `bosun-webhook`,
and the selected provider credential in `bosun-ai`. The deployment helper uses
files rather than placing secret values in command arguments. Restart the
controller after rotating its credentials.

Installation tokens expire after an hour and are narrowed to one repository
and `contents:read,pull_requests:write`. Keep review timeouts below this lifetime.
A PAT fallback remains for development/migration; remove its `token` key from
the production secret so App authentication failures fail closed. PATs can have
expiration dates; they are not inherently non-expiring.

A repository token is still visible to the trusted worker process. Environment
filtering is not a security boundary against hostile code within the same pod.
The signing key no longer enters that pod, but self-hosted Bosun is still for
trusted repositories, not mutually untrusted tenants. Orchael Reviewer uses a
separate clone init-container and control-plane publisher for that separation.

References:
- https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app
- https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries
