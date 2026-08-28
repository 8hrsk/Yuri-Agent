#!/bin/sh
set -eu

usage() {
  echo "usage: $0 [--verify] ARTIFACT CHECKSUM_FILE" >&2
  exit 2
}

mode=create
if [ "${1:-}" = "--verify" ]; then
  mode=verify
  shift
fi
[ "$#" -eq 2 ] || usage

artifact=$1
checksum_file=$2
[ -f "$artifact" ] || { echo "artifact not found: $artifact" >&2; exit 1; }

if [ "$mode" = verify ]; then
  [ -f "$checksum_file" ] || { echo "checksum file not found: $checksum_file" >&2; exit 1; }
  expected=$(awk 'NR == 1 { print $1 }' "$checksum_file")
  actual=$(shasum -a 256 "$artifact" | awk '{ print $1 }')
  [ -n "$expected" ] && [ "$expected" = "$actual" ] || {
    echo "SHA-256 mismatch for $artifact" >&2
    exit 1
  }
  echo "SHA-256 verified: $actual"
  exit 0
fi

directory=$(dirname "$checksum_file")
mkdir -p "$directory"
temporary=$(mktemp "$directory/.checksum.XXXXXX")
trap 'rm -f "$temporary"' EXIT HUP INT TERM
digest=$(shasum -a 256 "$artifact" | awk '{ print $1 }')
printf '%s  %s\n' "$digest" "$(basename "$artifact")" > "$temporary"
chmod 600 "$temporary"
mv "$temporary" "$checksum_file"
trap - EXIT HUP INT TERM
echo "SHA-256: $digest"
