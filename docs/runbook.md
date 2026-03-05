# Runtime Runbook (P2.x Hardening)

## Purpose
Operational checks for runtime observability and safe degradation with:
- `ENABLE_LIFECYCLE_ID`
- `ENABLE_EXECUTION_BACKFILL`
- `ENABLE_EXECUTION_RESPONSE_LOG`
- `ENABLE_PARTIAL_BE_RULE`
- `ENABLE_EDGE_FILTER`
- `ENABLE_MAKER_FIRST`
- `ENABLE_KPI_MONITORING`

No strategy defaults are changed by this runbook.

## Start Commands

### Build/version info
```bash
go run . --version
```
Expected output:
```text
build_info commit=<sha> build_time=<rfc3339> go=<go-version>
```

For reproducible release builds, inject metadata via ldflags:
```bash
go build -ldflags "-X main.gitCommit=$(git rev-parse --short HEAD) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o go-trade .
```

### Full feature set (staging/prod-like)
```bash
ENABLE_LIFECYCLE_ID=1 \
ENABLE_EXECUTION_BACKFILL=1 \
ENABLE_EXECUTION_RESPONSE_LOG=1 \
ENABLE_PARTIAL_BE_RULE=1 \
ENABLE_EDGE_FILTER=1 \
ENABLE_MAKER_FIRST=1 \
ENABLE_KPI_MONITORING=1 \
ENABLE_TRADE_SUMMARY_LOG=1 \
ENABLE_FILL_JSON_LOG=1 \
ENABLE_CONFIG_ENDPOINT=1 \
go run .
```

### Release hardening dry-run (no REST/WS network)
```bash
ENABLE_DRY_RUN=1 \
ENABLE_STATUS_SERVER=1 \
ENABLE_EXECUTION_BACKFILL=1 \
go run .
```
In dry-run mode the process starts status/health/counters and internal tick workers without exchange connections.
If `ENABLE_EXECUTION_BACKFILL=1` in dry-run, only `health.lastBackfillCycleTs` heartbeat is updated; real backfill counters are not incremented.

### Restricted environment (no local bind allowed)
```bash
ENABLE_STATUS_SERVER=0 go run .
```
or
```bash
STATUS_ADDR="" go run .
```

## Status Endpoint

Default:
```bash
STATUS_ADDR=127.0.0.1:6061
```

Check:
```bash
curl -s http://127.0.0.1:6061/status
```

Optional redacted config:
```bash
curl -s http://127.0.0.1:6061/config
```
`/config` is served only when `ENABLE_CONFIG_ENDPOINT=1`.
Sensitive fields are always masked as `***redacted***`.

Important fields:
- `counters.edgePass`
- `counters.edgeReject`
- `counters.edgeRejectMinDepth`
- `counters.edgeRejectWideSpread`
- `counters.edgeRejectLowImbalance`
- `counters.beIntentSent`
- `counters.beIntentSkippedAlreadyBetter`
- `counters.backfillFetched`
- `counters.backfillProcessed`
- `counters.backfillDeduped`
- `counters.backfillGaps`
- `counters.backfillErrors`
- `counters.dryRunTicks`
- `counters.makerFills`
- `counters.takerFills`
- `counters.totalExecFee`
- `counters.totalGrossRealised`
- `counters.feeToGrossRatio`
- `counters.avgTradeDuration`
- `counters.avgWinDuration`
- `counters.avgLossDuration`
- `counters.netAfterFeePerTrade`
- `health.lastBackfillCycleTs`
- `health.lastBackfillError`
- `health.lastBackfillErrorTs`
- `health.lastWsExecutionTs`
- `health.lastEdgeDecisionTs`
- `health.statusServerError`
- `health.statusServerStartedTs`
- `health.lastDryRunTickTs`
- `features.fillJsonLog`
- `features.lifecycleId`
- `features.executionBackfill`
- `features.executionResponseLog`
- `features.partialBERule`
- `features.edgeFilter`
- `features.makerFirst`
- `features.tradeSummaryLog`
- `features.statusServer`
- `features.configEndpoint`
- `features.dryRun`
`features.statusServer` means the process attempted to start status server (based on config gate), not that bind definitely succeeded.

## Key Logs

```bash
rg -n "execution_fill|execution_fill_anomaly|execution_list_response|gross_sign_mismatch|fee_outlier|missing_exec_for_close|trade_close_summary|Execution backfill cycle|next_backoff|gap detected|edge_filter_(pass|reject|summary|reject_burst)|entry_edge_guard|maker_first_|kpi_summary|kpi_violation|stop_intent move_sl_to_be|Status server" logs/trading_bot*.log
```

## Forensics Automation

Generate reproducible forensics artifacts for a fixed window:

```bash
go run ./cmd/forensics \
  --from "2026-02-24 00:00" \
  --to "2026-02-28 23:59" \
  --symbol BTCUSDT \
  --report /path/to/pnl_report.csv \
  --logs ./logs \
  --out /tmp/forensics
```

Artifacts:
- `/tmp/forensics/forensics_match_table.md`
- `/tmp/forensics/forensics_pnl_mismatch.md`
- `/tmp/forensics/forensics_clusters.json`

Notes:
- Parser reads both `.log` and `.gz`.
- Primary clustering key is `traceKey`/`lifecycleId`, fallback is time-window + position deltas.

## P6 Evaluation Harness

```bash
go run ./cmd/eval \
  --from "2026-02-24 00:00" \
  --to "2026-02-28 23:59" \
  --symbol BTCUSDT \
  --logs ./logs \
  --trade-source auto \
  --mode all_flags \
  --out /tmp/eval
```

Artifacts:
- `/tmp/eval/p6_eval_summary.md`
- `/tmp/eval/p6_eval_metrics.json`
- `/tmp/eval/p8_reconstructed_trades.tsv`

Trade source options:
- `--trade-source summary_only`:
  uses only `trade_close_summary` events.
- `--trade-source reconstruct`:
  forces reconstruction from fills/trade_event/position realised deltas (Tier-1/2/3).
- `--trade-source auto` (recommended):
  uses summaries when present, otherwise falls back to reconstruction.

Reconstruction interpretation:
- `tier1`: direct execution-based reconstruction (highest confidence).
- `tier2`: trade_event + realised delta inferred linkage.
- `tier3`: realised delta only (lowest confidence).

If `maker_ratio=0` with non-zero trades:
- check `diagnostics.makerCoverageAssessment` in `p6_eval_metrics.json`.
- this typically means fills lacked reliable `isMaker` flags.

## Typical Errors
- DNS/network:
  - `API authentication failed ... lookup ...`
  - expected in offline/sandbox environments.
- Local bind denied:
  - `Status server degraded (continuing without /status): ... operation not permitted`
  - app keeps running.
- Auth:
  - `Please check your API credentials`
- Config validation:
  - `Invalid configuration: ...`
  - app fails fast only on clearly invalid values (for example negative fee percent, invalid status addr format, non-positive orderbook levels).

## Smoke-check Checklist
1. `go test ./...` is green.
2. Bot starts with required feature flags.
3. `/status` responds (or `statusServerError` is set when bind is denied).
4. `counters.*` fields are present and non-negative.
5. `health.*` timestamps are valid RFC3339 values.
6. Backfill errors increase `counters.backfillErrors` and update `health.lastBackfillError*`.
7. `edge_filter_summary` appears at a bounded cadence (not per decision).
8. `Execution backfill cycle failed ... next_backoff=...` is rate-limited.
9. `execution_list_response {json}` appears when `ENABLE_EXECUTION_RESPONSE_LOG=1`.
10. In `ENABLE_DRY_RUN=1`, `counters.dryRunTicks` and `health.lastDryRunTickTs` advance without network.
11. With `ENABLE_TRADE_SUMMARY_LOG=1`, closed trades emit one-line `trade_close_summary {json}` with trace IDs and PnL breakdown fields.
12. With `ENABLE_MAKER_FIRST=1`, entry/reduce logs show `Maker-first order placed ...` and timeout fallback messages.
13. With `ENABLE_KPI_MONITORING=1`, periodic `kpi_summary {json}` and threshold `kpi_violation {json}` logs appear.

## Rollback
- Fast rollback by flags:
  - `ENABLE_EDGE_FILTER=0`
  - `ENABLE_MAKER_FIRST=0`
  - `ENABLE_KPI_MONITORING=0`
  - `ENABLE_PARTIAL_BE_RULE=0`
  - `ENABLE_EXECUTION_BACKFILL=0`
  - `ENABLE_LIFECYCLE_ID=0`
  - `ENABLE_TRADE_SUMMARY_LOG=0`
  - `ENABLE_CONFIG_ENDPOINT=0`
  - `ENABLE_DRY_RUN=0`
  - `ENABLE_STATUS_SERVER=0`
- Lifecycle storage reset (if needed):
  - remove `runtime/lifecycle_map.json` when bot is stopped.
