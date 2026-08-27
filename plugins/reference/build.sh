#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
OUT_DIR="$SCRIPT_DIR/bin"
OUT="$OUT_DIR/yuri-reference"

mkdir -p "$OUT_DIR"
(
	cd "$REPO_ROOT"
	CGO_ENABLED="${CGO_ENABLED:-0}" go build -trimpath -o "$OUT" ./plugins/reference
)

printf 'built %s\n' "$OUT"
printf 'sha256: '
shasum -a 256 "$OUT" | awk '{print $1}'
