#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

echo "[release-check] go test ./..."
go test ./...

echo "[release-check] go run . --version"
go run . --version

echo "[release-check] gofmt -l check"
UNFORMATTED="$(gofmt -l .)"
if [[ -n "$UNFORMATTED" ]]; then
  echo "[release-check] unformatted files:" >&2
  echo "$UNFORMATTED" >&2
  exit 1
fi

echo "[release-check] OK"
