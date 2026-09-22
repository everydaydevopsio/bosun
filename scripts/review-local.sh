#!/usr/bin/env bash
set -euo pipefail
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

cat <<EOF | kubectl -n bosun create -f -
apiVersion: batch/v1
kind: Job
metadata:
  generateName: bosun-local-review-
  labels:
    app.kubernetes.io/managed-by: bosun
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: 3600
  activeDeadlineSeconds: 1800
  template:
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      containers:
        - name: reviewer
          image: ${BOSUN_DEV_IMAGE:-bosun:dev}
          imagePullPolicy: IfNotPresent
          command: ["python3", "-m", "bosun.worker"]
          env:
            - {name: BOSUN_LOCAL_PATH, value: "/repos/$ID"}
            - {name: BOSUN_REPO, value: "$NAME"}
            - {name: BOSUN_REF, value: "$BRANCH"}
            - {name: BOSUN_SHA, value: ""}
            - {name: BOSUN_TRIGGER, value: "local-kind"}
            - {name: BOSUN_REVIEW_PROVIDER, value: "${BOSUN_REVIEW_PROVIDER:-codex}"}
            - {name: BOSUN_PR_NUMBER, value: ""}
            - name: OPENAI_API_KEY
              valueFrom: {secretKeyRef: {name: bosun-ai, key: openai-api-key, optional: true}}
            - name: CLAUDE_CODE_OAUTH_TOKEN
              valueFrom: {secretKeyRef: {name: bosun-ai, key: claude-code-oauth-token, optional: true}}
          volumeMounts:
            - {name: repos, mountPath: /repos}
      volumes:
        - name: repos
          hostPath: {path: /repos, type: Directory}
EOF

JOB="$(kubectl -n bosun get jobs -l app.kubernetes.io/managed-by=bosun --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1:].metadata.name}')"
echo "Started $JOB reviewing $REPO ($BRANCH)"
kubectl -n bosun wait --for=condition=complete --timeout=31m "job/$JOB" || true
kubectl -n bosun logs "job/$JOB"
rm -rf "$DEST"
