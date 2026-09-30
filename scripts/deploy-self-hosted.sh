#!/usr/bin/env bash
set -euo pipefail
# Deploy only to the explicitly selected kubeconfig context. No cloud resources
# or GitHub App registrations are created by this script.
: "${BOSUN_KUBE_CONTEXT:?set the target kubeconfig context}"
: "${BOSUN_HOST:?set the public HTTPS hostname}"
: "${BOSUN_IMAGE_TAG:?set a tested, immutable version tag (not latest)}"
: "${BOSUN_APP_ID:?set the GitHub App ID}"
: "${BOSUN_PRIVATE_KEY_FILE:?set the PEM file path}"
: "${BOSUN_WEBHOOK_SECRET_FILE:?set the webhook secret file path}"
[[ "$BOSUN_IMAGE_TAG" != latest ]] || { echo 'Use a tested version tag, not latest' >&2; exit 2; }
[[ "$BOSUN_APP_ID" =~ ^[0-9]+$ ]] || { echo 'App ID must be numeric' >&2; exit 2; }
[[ "$BOSUN_HOST" =~ ^[A-Za-z0-9.-]+$ ]] || { echo 'Host must be a DNS hostname' >&2; exit 2; }
for command in kubectl helm openssl; do command -v "$command" >/dev/null; done
[[ -s "$BOSUN_PRIVATE_KEY_FILE" && -s "$BOSUN_WEBHOOK_SECRET_FILE" ]]
openssl pkey -in "$BOSUN_PRIVATE_KEY_FILE" -noout >/dev/null
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
namespace=${BOSUN_NAMESPACE:-bosun}
release=${BOSUN_RELEASE:-bosun}
image=${BOSUN_IMAGE_REPOSITORY:-ghcr.io/everydaydevopsio/bosun}
provider=${BOSUN_REVIEW_PROVIDER:-codex-bosun}
kube=(kubectl --context "$BOSUN_KUBE_CONTEXT" -n "$namespace")
"${kube[@]}" cluster-info >/dev/null
printf 'Target context: %s; namespace: %s; image: %s:%s\n' "$BOSUN_KUBE_CONTEXT" "$namespace" "$image" "$BOSUN_IMAGE_TAG"
umask 077
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
printf '%s' "$BOSUN_APP_ID" > "$tmp/app-id"
kubectl --context "$BOSUN_KUBE_CONTEXT" create namespace "$namespace" --dry-run=client -o yaml | kubectl --context "$BOSUN_KUBE_CONTEXT" apply -f -
"${kube[@]}" create secret generic bosun-github --from-file=app-id="$tmp/app-id" --from-file=private-key="$BOSUN_PRIVATE_KEY_FILE" --dry-run=client -o yaml | "${kube[@]}" apply -f -
"${kube[@]}" create secret generic bosun-webhook --from-file=secret="$BOSUN_WEBHOOK_SECRET_FILE" --dry-run=client -o yaml | "${kube[@]}" apply -f -
# Remove a legacy PAT fallback from App-only production installations.
"${kube[@]}" patch secret bosun-github --type=merge -p '{"data":{"token":null}}' >/dev/null
if [[ "$provider" == codex-bosun && -n "${OPENAI_API_KEY:-}" ]]; then
 printf '%s' "$OPENAI_API_KEY" > "$tmp/model-key"
 "${kube[@]}" create secret generic bosun-ai --from-file=openai-api-key="$tmp/model-key" --dry-run=client -o yaml | "${kube[@]}" apply -f -
elif [[ "$provider" == claude-bosun && -n "${ANTHROPIC_API_KEY:-}" ]]; then
 printf '%s' "$ANTHROPIC_API_KEY" > "$tmp/model-key"
 "${kube[@]}" create secret generic bosun-ai --from-file=anthropic-api-key="$tmp/model-key" --dry-run=client -o yaml | "${kube[@]}" apply -f -
else
 "${kube[@]}" get secret bosun-ai -o name >/dev/null
fi
helm lint "$root/charts/bosun"
helm upgrade --install "$release" "$root/charts/bosun" --kube-context "$BOSUN_KUBE_CONTEXT" --namespace "$namespace" \
 -f "$root/deploy/values-self-hosted.yaml" --set-string "image.repository=$image" --set-string "image.tag=$BOSUN_IMAGE_TAG" \
 --set-string "review.image=$image:$BOSUN_IMAGE_TAG" --set-string "review.provider=$provider" --set-string "ingress.host=$BOSUN_HOST" \
 --wait --timeout 5m
"${kube[@]}" rollout restart "deployment/$release-bosun"
"${kube[@]}" rollout status "deployment/$release-bosun" --timeout=180s
printf 'Next: verify TLS, send a GitHub App ping, and run the real-PR smoke test in docs/self-hosted-github-app.md.\n'
