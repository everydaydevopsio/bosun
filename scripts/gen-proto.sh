#!/usr/bin/env bash
# Generate the Python gRPC stubs Bosun uses to talk to a local bridge server.
# The proto is vendored from orchael/bridgectl; regenerate after updating it.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/bosun/gen"

rm -rf "$OUT"
mkdir -p "$OUT"
python3 -m grpc_tools.protoc \
  --proto_path="$ROOT/proto" \
  --python_out="$OUT" \
  --grpc_python_out="$OUT" \
  "$ROOT/proto/bridge/v1/bridge.proto"

# protoc emits namespace directories without __init__.py.
find "$OUT" -type d -exec touch {}/__init__.py \;
echo "generated stubs in $OUT"
