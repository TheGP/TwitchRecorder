#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "Usage: $0 FILENAME SHA256" >&2
  exit 2
fi

filename=$1
expected_hash=$2
if [[ ! $filename =~ ^[a-z0-9_]+-[0-9]{4}-[0-9]{2}-[0-9]{2}(-[0-9]+)?\.(ts|chat\.jsonl)$ ]] ||
   [[ ! $expected_hash =~ ^[a-f0-9]{64}$ ]]; then
  echo "Invalid filename or checksum" >&2
  exit 2
fi

cd "$(dirname "$0")/recordings"
if [ ! -f "$filename" ] || [ -L "$filename" ]; then
  echo "Recording is missing or not a regular file: $filename" >&2
  exit 1
fi

actual_hash=$(sha256sum -- "$filename")
actual_hash=${actual_hash%% *}
if [ "$actual_hash" != "$expected_hash" ]; then
  echo "Checksum changed; keeping $filename" >&2
  exit 1
fi

rm -- "$filename"
echo "Removed verified recording: $filename"
