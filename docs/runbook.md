# Runtime Runbook (P2.x Hardening)

## Purpose
Operational checks for runtime observability and safe degradation with:
- `ENABLE_LIFECYCLE_ID`
- `ENABLE_EXECUTION_BACKFILL`
- `ENABLE_PARTIAL_BE_RULE`
- `ENABLE_EDGE_FILTER`

No strategy defaults are changed by this runbook.

## Start Commands

### Full feature set (staging/prod-like)
```bash
ENABLE_LIFECYCLE_ID=1 \
ENABLE_EXECUTION_BACKFILL=1 \
ENABLE_PARTIAL_BE_RULE=1 \
ENABLE_EDGE_FILTER=1 \
ENABLE_FILL_JSON_LOG=1 \
go run .
```

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
- `health.lastBackfillCycleTs`
- `health.lastBackfillError`
- `health.lastBackfillErrorTs`
- `health.lastWsExecutionTs`
- `health.lastEdgeDecisionTs`
- `health.statusServerError`
- `health.statusServerStartedTs`

## Key Logs

```bash
rg -n "execution_fill|Execution backfill cycle|next_backoff|gap detected|edge_filter_(pass|reject|summary|reject_burst)|stop_intent move_sl_to_be|Status server" logs/trading_bot*.log
```

## Typical Errors
- DNS/network:
  - `API authentication failed ... lookup ...`
  - expected in offline/sandbox environments.
- Local bind denied:
  - `Status server degraded (continuing without /status): ... operation not permitted`
  - app keeps running.
- Auth:
  - `Please check your API credentials`

## Smoke-check Checklist
1. `go test ./...` is green.
2. Bot starts with required feature flags.
3. `/status` responds (or `statusServerError` is set when bind is denied).
4. `counters.*` fields are present and non-negative.
5. `health.*` timestamps are valid RFC3339 values.
6. Backfill errors increase `counters.backfillErrors` and update `health.lastBackfillError*`.
7. `edge_filter_summary` appears at a bounded cadence (not per decision).
8. `Execution backfill cycle failed ... next_backoff=...` is rate-limited.

## Rollback
- Fast rollback by flags:
  - `ENABLE_EDGE_FILTER=0`
  - `ENABLE_PARTIAL_BE_RULE=0`
  - `ENABLE_EXECUTION_BACKFILL=0`
  - `ENABLE_LIFECYCLE_ID=0`
  - `ENABLE_STATUS_SERVER=0`
- Lifecycle storage reset (if needed):
  - remove `runtime/lifecycle_map.json` when bot is stopped.
