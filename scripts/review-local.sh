#!/usr/bin/env bash
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"

# Provider keys come from AWS Secrets Manager unless already exported.
if [ -z "${BOSUN_SECRETS_LOADED:-}" ] && [ "${BOSUN_SKIP_SECRETS:-}" != "1" ]; then
  exec "$HERE/with-secrets.sh" "$0" "$@"
fi

REVIEW_TIMEOUT="${BOSUN_REVIEW_TIMEOUT_SECONDS:-1800}"
REPO="${1:-$PWD}"
REPO="$(cd "$REPO" && pwd)"
git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null
BRANCH="$(git -C "$REPO" branch --show-current)"
[ -n "$BRANCH" ] || BRANCH="$(git -C "$REPO" rev-parse --short HEAD)"
NAME="$(basename "$REPO")"
ID="${NAME}-$(date +%s)"
DEST="/tmp/bosun-repos/$ID"

mkdir -p "$DEST"
# Copy the working tree including uncommitted changes and .git metadata.
tar -C "$REPO" -cf - . | tar -C "$DEST" -xf -

JOB="$(kubectl -n bosun create -o name -f - <<EOF
apiVersion: batch/v1
kind: Job
metadata:
  generateName: bosun-local-review-
  labels:
    app.kubernetes.io/managed-by: bosun
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 3600
  activeDeadlineSeconds: $((REVIEW_TIMEOUT + 120))
  template:
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      containers:
        - name: reviewer
          image: ${BOSUN_DEV_IMAGE:-bosun:dev}
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/bosun", "reviewer"]
          env:
            - {name: BOSUN_LOCAL_PATH, value: "/repos/$ID"}
            - {name: BOSUN_REPO, value: "$NAME"}
            - {name: BOSUN_REF, value: "$BRANCH"}
            - {name: BOSUN_SHA, value: ""}
            - {name: BOSUN_TRIGGER, value: "local-kind"}
            - {name: BOSUN_REVIEW_PROVIDER, value: "${BOSUN_REVIEW_PROVIDER:-codex}"}
            - {name: BOSUN_PR_NUMBER, value: ""}
            - {name: BOSUN_REVIEW_TIMEOUT_SECONDS, value: "$REVIEW_TIMEOUT"}
            - {name: BOSUN_LOG_FORMAT, value: "${BOSUN_LOG_FORMAT:-text}"}
            - {name: BOSUN_LOG_LEVEL, value: "${BOSUN_LOG_LEVEL:-info}"}
            - name: OPENAI_API_KEY
              valueFrom: {secretKeyRef: {name: bosun-ai, key: openai-api-key, optional: true}}
            - name: CLAUDE_CODE_OAUTH_TOKEN
              valueFrom: {secretKeyRef: {name: bosun-ai, key: claude-code-oauth-token, optional: true}}
            - name: ANTHROPIC_API_KEY
              valueFrom: {secretKeyRef: {name: bosun-ai, key: anthropic-api-key, optional: true}}
            - name: GEMINI_API_KEY
              valueFrom: {secretKeyRef: {name: bosun-ai, key: gemini-api-key, optional: true}}
            - name: CODEX_AUTH
              valueFrom: {secretKeyRef: {name: bosun-ai, key: codex-auth, optional: true}}
            - name: CLAUDE_CREDENTIALS
              valueFrom: {secretKeyRef: {name: bosun-ai, key: claude-credentials, optional: true}}
          securityContext:
            allowPrivilegeEscalation: false
            capabilities: {drop: ["ALL"]}
          volumeMounts:
            - {name: repos, mountPath: /repos}
      volumes:
        - name: repos
          hostPath: {path: /repos, type: Directory}
EOF
)"

cleanup() { rm -rf "$DEST"; }
trap cleanup EXIT

echo "Started $JOB reviewing $REPO ($BRANCH)" >&2

# Wait for either outcome; `complete` alone hangs the full timeout on failure.
DEADLINE=$(( $(date +%s) + REVIEW_TIMEOUT + 180 ))
STATUS=timeout
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  if kubectl -n bosun wait --for=condition=complete --timeout=5s "$JOB" >/dev/null 2>&1; then
    STATUS=complete
    break
  fi
  if kubectl -n bosun wait --for=condition=failed --timeout=5s "$JOB" >/dev/null 2>&1; then
    STATUS=failed
    break
  fi
done

kubectl -n bosun logs --tail=-1 "$JOB"
[ "$STATUS" = complete ] || { echo "review job failed" >&2; exit 1; }
