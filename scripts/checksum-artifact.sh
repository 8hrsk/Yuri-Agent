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

digest_for() {
  LC_ALL=C shasum -a 256 "$1" | awk '{ print $1 }'
}

if [ "$mode" = verify ]; then
  [ -f "$checksum_file" ] || { echo "checksum file not found: $checksum_file" >&2; exit 1; }
  manifest=$(awk '
    NF {
      lines++
      if (NF != 2) malformed=1
      else { digest=$1; name=$2; sub(/\r$/, "", name) }
    }
    END {
      if (lines != 1 || malformed || digest == "" || name == "") exit 1
      printf "%s\n%s\n", digest, name
    }
  ' "$checksum_file") || {
    echo "invalid SHA-256 manifest: $checksum_file" >&2
    exit 1
  }
  expected=$(printf '%s\n' "$manifest" | sed -n '1p')
  expected_name=$(printf '%s\n' "$manifest" | sed -n '2p')
  case "$expected" in
    ""|*[!0123456789abcdefABCDEF]*)
      echo "invalid SHA-256 digest in $checksum_file" >&2
      exit 1
      ;;
  esac
  [ "${#expected}" -eq 64 ] || {
    echo "invalid SHA-256 digest in $checksum_file" >&2
    exit 1
  }
  [ "$expected_name" = "$(basename "$artifact")" ] || {
    echo "checksum manifest names $expected_name, expected $(basename "$artifact")" >&2
    exit 1
  }
  actual=$(digest_for "$artifact")
  [ "$expected" = "$actual" ] || {
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
digest=$(digest_for "$artifact")
printf '%s  %s\n' "$digest" "$(basename "$artifact")" > "$temporary"
chmod 600 "$temporary"
mv "$temporary" "$checksum_file"
trap - EXIT HUP INT TERM
echo "SHA-256: $digest"
