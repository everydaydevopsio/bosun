#!/usr/bin/env bash
set -euo pipefail
# Compatibility entrypoint; native CLI owns snapshots, monitoring, and cleanup.
HERE="$(cd "$(dirname "$0")" && pwd)"
if [ -n "${BOSUN_BIN:-}" ]; then
  exec "$BOSUN_BIN" review "$@"
fi
# Built from this checkout rather than taken from PATH: an installed bosun can
# predate the flags this script is asked to forward, and "unknown flag" would
# point at the caller instead of at the version skew.
# Preserve the caller's working directory when building from the source tree.
BIN="$(mktemp /tmp/bosun-cli.XXXXXXXX)"
trap 'rm -f "$BIN"' EXIT
(cd "$HERE/.." && go build -o "$BIN" ./cmd/bosun)
"$BIN" review "$@"
