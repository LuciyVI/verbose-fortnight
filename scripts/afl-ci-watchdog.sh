#!/usr/bin/env bash
set -euo pipefail

# CI/CD defaults. Override any of these in the pipeline environment.
: "${AFL_STATS_DIR:=fuzz/out}"
: "${AFL_IDLE_TIMEOUT:=2h}"
: "${AFL_WATCHDOG_POLL_INTERVAL:=60s}"
: "${AFL_WATCHDOG_TERMINATE_TIMEOUT:=30s}"
: "${AFL_WATCHDOG_EXIT_CODE:=124}"

args=(
  go run ./cmd/afl-idle-watchdog
  --stats-dir "$AFL_STATS_DIR"
  --idle-timeout "$AFL_IDLE_TIMEOUT"
  --poll-interval "$AFL_WATCHDOG_POLL_INTERVAL"
  --terminate-timeout "$AFL_WATCHDOG_TERMINATE_TIMEOUT"
  --no-progress-exit-code "$AFL_WATCHDOG_EXIT_CODE"
)

if [[ $# -gt 0 ]]; then
  args+=(-- "$@")
fi

exec "${args[@]}"
