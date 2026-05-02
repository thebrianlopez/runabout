#!/bin/sh
# Bundle xterm-mirror.js into a self-contained Node script.
# Output: internal/gateway/mirror/mirror_bundle.js (embedded via //go:embed)
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OUT="$SCRIPT_DIR/../mirror_bundle.js"
cp "$SCRIPT_DIR/xterm-mirror.js" "$OUT"
echo "mirror_bundle.js written to $OUT"
