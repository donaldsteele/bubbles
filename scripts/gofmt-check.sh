#!/usr/bin/env bash
# Lists Go files that are not gofmt-clean and exits 1 if there are any.
# Scans only the Go source directories; web/ (npm tree) and tray/ (separate
# module with its own gate) are deliberately excluded.
set -euo pipefail

cd "$(dirname "$0")/.."

dirs=()
for d in cmd internal pkg examples tools; do
  if [ -d "$d" ]; then
    dirs+=("$d")
  fi
done

if [ "${#dirs[@]}" -eq 0 ]; then
  exit 0
fi

offending="$(gofmt -l "${dirs[@]}")"
if [ -n "$offending" ]; then
  echo "$offending"
  echo "gofmt: the files above need formatting (run: gofmt -w <file>)" >&2
  exit 1
fi
