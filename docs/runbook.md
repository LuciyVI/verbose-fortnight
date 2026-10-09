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
In dry-run mode the process starts status/health/counters and internal tick workers without exchange connections. It validates startup and diagnostics; it does not simulate the trading strategy, orders, fills or TP/SL.
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
12. With `ENABLE_MAKER_FIRST=1`, entry/reduce logs show `maker_first_attempt`, `maker_first_timeout` and, only after terminal reconciliation, `maker_first_fallback_market` events.
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


## Order reconciliation and stop lifecycles

Build with `go build -o go-trade .`; run with `go run .` or `./go-trade`. Debug is a numeric level, for example `go run . -debug=0`. For diagnostics without exchange connections, use `ENABLE_DRY_RUN=1 ENABLE_STATUS_SERVER=0 go run . -debug=0`.

`ORDER_BALANCE_PCT` defaults to **1.0** (100% of available balance as target notional), not 0.01. A value of 0 uses the exchange minimum order size. This is position sizing, not a loss budget; SL distance, fills, fees and slippage determine realised loss. Sizing and strategy defaults were preserved.

| Setting | Default | Meaning |
| --- | --- | --- |
| `REST_TIMEOUT_MS` | 5000 | Per-request deadline, including headers and full response-body reading |
| `REST_MAX_RESPONSE_BYTES` | 1048576 | Maximum response size; hard cap 16 MiB |
| `ORDER_RECONCILE_TIMEOUT_MS` | 3000 | Total bounded wait for placement recovery or cancellation reconciliation |
| `MAKER_TIMEOUT_MS` | 800 | Existing post-only waiting policy; does not replace REST deadlines |

Zero REST/reconciliation settings use bounded defaults; negative values fail configuration validation. `api.RESTClient.HTTPClient` is injectable; even a client with `Timeout=0` is limited by the request deadline. Set its `Context` before starting workers to propagate cancellation. SIGINT/SIGTERM cancels startup and runtime REST calls.

Every order creation has a stable `orderLinkId`, including when lifecycle logging is disabled. Network failures, HTTP errors and unreadable placement replies do not establish rejection. The bot queries `/v5/order/realtime`, then `/v5/order/history` if absent. Server-timeout/error and duplicate-ID retCodes also require lookup. No order/create retries are performed. A returned order ID confirms acknowledgement, not execution.

Maker-first uses the matching order's `cumExecQty`, never total-position changes. It cancels that order only and waits for terminal state (`Filled`, `Cancelled`, `Rejected`, `Deactivated`, `PartiallyFilledCanceled`) before calculating a market remainder. Cancellation ACK alone is insufficient. Cumulative fill cannot decrease; inconsistent/regressing snapshots leave reconciliation pending. Late or duplicate WS execution events cannot increase the fallback quantity because it comes from the final cumulative snapshot. These fields and asynchronous cancellation are described in the official [order-state API](https://bybit-exchange.github.io/docs/v5/order/open-order) and [order history](https://bybit-exchange.github.io/docs/v5/order/order-list).

If the bounded wait cannot establish the result, `order.ErrOrderUncertain` is returned and a `FATAL_API` trading block prevents further orders. Each uncertainty registration receives a new monotonically increasing generation and is retained in `State.PendingOrders`; even registrations with identical order IDs remain distinct. `PendingOrderID`, `PendingOrderLinkID` and `PendingOrderGeneration` point to the most recent unresolved record. Registering B retains unresolved A instead of overwriting it. `/status.pendingOrders` lists all unresolved generations and identities.

`Trader.ReconcilePendingOrder(ctx)` observes the selected record without submitting a replacement. `ReconcilePendingOrderGeneration(ctx, generation)` allows integrations to resolve an older record. The result is correlated with the identity captured before Query and rechecked under the state lock afterward. A terminal reply can remove only that same registration; duplicate/stale replies cannot clear a newer record, including one using the same ID. Errors, timeouts, absence and nonterminal replies retain uncertainty. The pending flag stays set while any record remains. Once all records are resolved, the trading block still requires operator acknowledgement. Manual clear checks both the flag and retained records.

Durable operations are stored at `OPERATION_STATE_PATH` (default `state/operations.json`, relative to cwd). Set an absolute path on a persistent local volume when using a service/daemon; use a separate store for each account, environment and symbol, and keep the account binding unchanged. The process takes an exclusive nonblocking flock on `<path>.lock`; a second writer is blocked. Snapshot files use mode 0600. Neither API keys nor secrets/headers are stored.

Before each order create/cancel/cancel-all or trading-stop POST, the exact permitted payload and immutable owner/generation are saved as `intent`, followed by `sending`. Both commits complete file fsync, atomic rename and directory fsync before network access. A crash at `intent` means that mutation was durably never sent; recovery resolves it without replay. `sending` is ambiguous even if the process died before the socket send. Lost replies and result-write errors retain uncertainty. A successful order ACK is persisted but remains subject to terminal-order reconciliation after restart. TP/SL ACKs record success; startup still checks actual position/stops. OrderLinkId is sent for creation; other endpoints use local correlation IDs and their available exchange target identifiers, not an unsupported idempotency field.

The checksum/versioned snapshot retains all records, completed and unresolved, plus a monotonic generation high-water mark. Previous generations cannot be reused after a sent operation. The snapshot update is atomic on a local filesystem implementing fsync/rename/flock correctly; NFS/object-backed mounts and hardware that does not honor fsync are outside this guarantee. There is a 16 MiB snapshot limit and no automatic compaction: reaching it blocks new writes. Leftover `.tmp-*` candidates block loading and are retained for inspection. If the snapshot is missing but its existing lock file remains, the store reports state loss and does not create a new empty snapshot. A genuinely fresh path still requires the complete exchange audit. A checksum is integrity detection, not protection against intentional tampering.

Every startup begins blocked. `STARTUP_RECONCILE_TIMEOUT_MS` defaults to 30000 (zero uses this bounded default; negative is invalid). Recovery enumerates paginated active orders and executions, queries local operations by their original ID/link (realtime/history), verifies cumulative quantities against deduplicated per-order executions, and observes actual position and TP/SL. It checks the position against the last successful checkpoint plus newly observed executions. Missing boundaries, malformed/incomplete pages, repeated cursors, pagination limits (100 pages/20000 records per scan), nonterminal orders or unknown results keep the gate blocked. Failed scans save separate diagnostic evidence and never replace the last successful checkpoint. An absent file triggers the same complete exchange scan. Unknown active exchange orders are durably discovered and block trading. Per-order executions must also appear in the global execution snapshot; inconsistent observation times block the attempt. A newer recovery attempt supersedes older results, and a previously prepared request cannot cross its sending barrier while recovery is active. A live position requires a recoverable lifecycle and valid protective stops; unknown/manual positions require operator reconciliation. Private WS resync repeats the durable audit before clearing its block.

Recovery sends GETs only. It does not repeat create/cancel/TP-SL POSTs, invent fills or automatically submit the remaining quantity of an interrupted action. A terminal partial fill can be recovered as a smaller protected position. Decide separately what to do with its unfilled quantity. The live maker-first policy still submits only the confirmed remainder after terminal cancellation; recovery never resumes that interrupted placement automatically.

Diagnostics: `/status.recovery` exposes store path, readiness, revision, error and every durable operation. `/status.tradingBlocked` and `blockMessage` show whether trading is allowed. `/status.pendingOrders` remains the compatibility view for runtime uncertainty; the durable operation list is the restart source of truth. Manual clear checks both the in-memory uncertainty registry and the durable/startup gate. Once all required recovery checks and the durable checkpoint commit succeed, startup restores the position/lifecycle and applied stop levels. The existing private WS startup resync must also succeed before strategy workers start.

Run `go run . -reconcile-only -debug=0` (or `./go-trade -reconcile-only -debug=0`) with the normal account configuration and existing state path to perform the read-only recovery scan. It persists observations and exits 0 when the durable gate is ready, 1 otherwise, without WS or trading workers. A blocked normal startup keeps diagnostics available and waits for SIGTERM; it does not poll/retry mutations. After external state has been reconciled, restart or run the recovery command again. Exchange unavailability leaves the gate blocked.

For damaged/unsupported state or leftover temp candidates: stop all bot instances; copy the snapshot, lock file and every temp candidate before any repair. Inspect checksums, version, operation IDs, exact targets, order history/executions, active remainders, position and stops in the same account/environment. Do not delete the state or replace it with an empty snapshot to bypass the block. Restore a verified backup and reconcile every operation/candidate created after it; a restored old generation/ID must never be reused for a possibly sent operation. The loader has no automatic migration, salvage or generation-renumbering command. If no verifiable reconstruction/history exists, recovery remains blocked and requires a reviewed repair; preserve the evidence. Do not restart unattended after changing or losing the state volume. An accepted operation can remain live after process termination.

Stop intents capture the source trade/lifecycle before scheduling. The final REST observation verifies the requested symbol, one-way `positionIdx=0`, an open position, direction and current local lifecycle. Explicit mismatching symbol/index responses are rejected. Legacy responses omitting symbol are scoped to the exact symbol filter of the GET; omitted positionIdx is the one-way zero value. Hedge positions are not used by this bot. The actual observed entry is used for final price validation. Requests and cached levels belong to the lifecycle; changes cancel the request context and the controller checks identity again after the response. No shared state mutex is held during network work.

Prices are quantized using decimal arithmetic and the instrument `tickSize`, including nondecimal steps such as 0.25. The policy moves levels toward entry: LONG TP rounds down and SL rounds up; SHORT TP rounds up and SL rounds down. Invariants are checked again using values parsed from the exact JSON price strings. Ordinary levels collapsing to entry are rejected. Explicit breakeven intents may retain SL equal to entry; TP must remain beyond entry and SL must remain on the other side of TP. The controller commits Desired/Applied together only after successful REST acknowledgement, using the actual sent levels. Errors and stale replies leave those fields unchanged. Standalone protective orders use the same tick policy and validate against an observed entry. Maker limit prices also use tick precision, with Buy down/Sell up.

Remote limitation: the final GET is **not an atomic exchange-side precondition** on the later POST. A remote close/reopen can happen between observation, sending, acceptance and application. Context cancellation cannot undo a mutation already accepted by the exchange. The trading-stop request addresses symbol/position index and has no conditional lifecycle parameter. Local stale-intent rejection and response isolation are tested, but do not establish remote atomicity. Reconcile the new position's stops after such uncertainty.


Instrument `qtyStep`, `minOrderQty` and `tickSize` must be finite and positive; `minNotionalValue` must be finite and nonnegative. Invalid values fail loading rather than receiving a local fallback. Order quantity is validated before sending and formatted with decimal arithmetic, rounding down to an exact multiple of the instrument step (including 0.25 and very small steps).


## Offline verification of order-safety fixes (2026-10-09)

Baseline HEAD: `f74dea6777f0c7536eab27c448b2a055744d30d1`; initial `git status --short` was empty. Existing tests passed, including a fresh `go test -count=1 ./...` on an archived copy of that HEAD. No user changes were present. No commits, push, merge or exchange trading were performed.

Final checks passed: `go test ./...`, `go test -race ./...`, `go vet ./...`, `bash scripts/release-check.sh`, and `go build -o /tmp/verbose-fortnight-verified-go-trade .`. The release check also passed `go run . --version` and the repository-wide gofmt check. Two pre-existing formatting issues in `daemon/daemon.go` and `indicators/indicators_test.go` were fixed by gofmt. Loopback-server tests and Go cache writes required execution outside the restricted sandbox; no exchange endpoints were contacted.

Bounded fuzz command: `go test ./order -run '^$' -fuzz '^FuzzFormatQty$' -fuzztime=5s -parallel=2` passed with 79,727 executions. Lifecycle cancellation/processing tests also passed ten repeated runs.

Identical compatible regression tests were copied to an archived baseline under `/tmp/verbose-fortnight-baseline` and run against the unchanged original production code. The baseline runs intentionally failed; the corrected runs passed without cache:

| Regression | Original HEAD | Corrected code |
| --- | --- | --- |
| `TestStopIntentLifecycleIsolation` | Stale A intents overwrite B's desired/applied state; closed/wrong-side intents alter desired levels | PASS |
| `TestDelayedStopCapturesLifecycleAtCreation` | Anonymous delayed A task is rebound to B | PASS |
| `TestStopLifecycleChangesDuringProcessing` | Old position-read/response overwrites B's applied cache | PASS |
| `TestStopCurrentLifecycleAndCache` | A's cached levels suppress B's required API write | PASS |
| `TestMakerOrderJournalRegressions` | Partial/no fills mistaken for completion; fallback ignores order fills and terminal cancellation | PASS |
| `TestRESTHTTPFailureNotSuccess` | HTTP 429/500 accepted with a success-shaped JSON body | PASS |
| `TestProtectiveOrderRejectionNotSuccess` | TP/SL placement returns nil for rejection/malformed JSON | PASS |
| `TestInstrumentNumericValidation` | Invalid numeric fields accepted or silently replaced | PASS |
| `TestFormatQtyDecimalSteps` | Rounds above requested quantity and misformats non-power-of-ten steps | PASS |
| `TestFormatQtyInvalidInputs` | Hangs at step=0; aborted by explicit `-timeout=2s` | PASS |

Additional tests cover stalled headers/body reads, context cancellation, malformed/missing retCode, oversized responses, read failures, cancellation refusal/timeout/lost responses for both entry and reduction, acceptance with lost response, server-timeout/duplicate-ID recovery, actual HTTP cancellation on lifecycle change and process termination during stalled startup REST. The fake exchange maintains its own order and execution journal with execution-ID deduplication. These tests establish local behavior; they do not prove real-exchange timing or simulate the full strategy.

Local evidence files for this run: `/tmp/verbose-fortnight-regression-before.log`, `/tmp/verbose-fortnight-quantity-before.log`, `/tmp/verbose-fortnight-regression-after.log`, `/tmp/verbose-fortnight-final-test.log`, `/tmp/verbose-fortnight-final-race.log`, `/tmp/verbose-fortnight-final-vet.log`, `/tmp/verbose-fortnight-final-release.log`. They are temporary and are not committed.
