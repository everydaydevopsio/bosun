#!/usr/bin/env bash
set -euo pipefail
# Compatibility entrypoint; the native CLI owns the cluster lifecycle.
#
# This runs the CLI from the checkout it ships in rather than one found on PATH:
# an installed bosun can predate the commands and flags these scripts pass it,
# and the resulting "unknown command" names neither the real problem nor the fix.
# BOSUN_BIN overrides, which is also the seam the script tests drive.
HERE="$(cd "$(dirname "$0")" && pwd)"
ARGS=(down --cluster "${BOSUN_KIND_CLUSTER:-bosun}" "$@")
if [ -n "${BOSUN_BIN:-}" ]; then
  exec "$BOSUN_BIN" "${ARGS[@]}"
fi
cd "$HERE/.."
exec go run ./cmd/bosun "${ARGS[@]}"
