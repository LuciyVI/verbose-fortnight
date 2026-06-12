# Reversal Long Replay

`cmd/replay-reversal` runs an offline dry-run replay for `ModeReversalLong`.
It never sends live orders and refuses `--orders-enabled=true`.

Prepare public market-data input:

```bash
go run ./cmd/prepare-reversal-input \
  --symbol BTCUSDT \
  --from 2026-05-20T10:00:00Z \
  --to 2026-05-20T13:50:52Z \
  --out artifacts/replay/reversal_BTCUSDT_20260520.csv
```

The preparer calls only Bybit public `/v5/market/kline`.
It does not call private/authenticated endpoints and does not fetch historical orderbook snapshots.
If no local orderbook enrichment is available, orderbook columns are left blank and replay reports orderbook as unavailable.

Input is a CSV file with required columns:

- `time`
- `open`
- `high`
- `low`
- `close`
- `volume`

Optional orderbook columns:

- `best_bid`
- `best_ask`
- `best_bid_size`
- `best_ask_size`
- `bid_ask_imbalance`
- `spread_bps`
- `orderbook_ratio`

When `ReversalLong.Enabled=true`, live diagnostics also log `event=orderbook_diagnostic_snapshot`
from the in-memory top book. The recorder is diagnostic-only: it records `source=live_orderbook_snapshot`,
top bid/ask, spread, top sizes, imbalance, ratio, depth levels, and explicit unavailable reasons.
It is not used to place orders.

Set `ORDERBOOK_SNAPSHOT_JSONL` to write those live snapshots as raw JSONL for later replay enrichment.
The default is empty, so no production logging path changes unless the variable is explicitly set.
`ORDERBOOK_SNAPSHOT_INTERVAL_MS` controls the periodic recorder cadence and defaults to `1000`.
The `/status` response exposes recorder health:
`orderbook_snapshot_recorded_total`, `orderbook_snapshot_available_total`,
`orderbook_snapshot_unavailable_total`, `last_orderbook_snapshot_time`, and
`last_orderbook_unavailable_reason`.

Validate candidate orderbook snapshot JSONL before enrichment:

```bash
go run ./cmd/replay-reversal \
  --validate-orderbook-jsonl artifacts/replay/BTCUSDT_orderbook_snapshots.jsonl \
  --enrich-symbol BTCUSDT \
  --orders-enabled=false
```

The validator reports total and valid snapshots, live vs replay source counts,
time range, symbols, top unavailable reasons, and one of:
`USABLE_FOR_ENRICHMENT`, `DATA_GAP_ONLY_REPLAY_UNAVAILABLE`, or `NO_VALID_SNAPSHOTS`.
`DATA_GAP_ONLY_REPLAY_UNAVAILABLE` means the file only contains replay placeholders
such as `source=replay_orderbook_snapshot`, `orderbook_available=false`, and cannot
improve orderbook coverage.

Enrich a replay CSV from live snapshot JSONL:

```bash
go run ./cmd/replay-reversal \
  --enrich-orderbook \
  --enrich-input-csv artifacts/replay/reversal_BTCUSDT.csv \
  --enrich-orderbook-jsonl artifacts/replay/BTCUSDT_orderbook_snapshots.jsonl \
  --enrich-output-csv artifacts/replay/reversal_BTCUSDT_enriched.csv \
  --enrich-symbol BTCUSDT \
  --enrich-max-staleness-ms 1500 \
  --orders-enabled=false
```

Enrichment attaches only the nearest previous valid live top-book snapshot within
the staleness window. It leaves orderbook columns empty when snapshots are missing,
stale, or unavailable, and it never synthesizes orderbook values.

Diagnostics-only mode:

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=false \
  --orders-enabled=false \
  --out artifacts/replay/reversal_diag.jsonl
```

Signal dry-run mode:

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --out artifacts/replay/reversal_signal_dryrun.jsonl
```

Blocker timeline report:

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --setup-memory-bars 0 \
  --invalidation-atr-mult 0 \
  --out artifacts/replay/reversal_signal_dryrun.jsonl \
  --blocker-report artifacts/replay/reversal_blockers.md \
  --episodes-out artifacts/replay/reversal_episodes.json
```

Offline sensitivity matrix:

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --sensitivity \
  --sensitivity-min-rsi-delta-list 0.5,1.0,1.5,2.0,2.5 \
  --sensitivity-no-new-low-bars-list 1,2,3 \
  --sensitivity-score-threshold-list 0.55,0.6,0.7,0.8 \
  --sensitivity-min-volume-ratio-list 1.0,1.2,1.5,2.0 \
  --sensitivity-setup-memory-bars-list 0,2,3,5,7 \
  --sensitivity-invalidation-atr-mult-list 0.1,0.2,0.3 \
  --out artifacts/replay/reversal_sensitivity.jsonl \
  --blocker-report artifacts/replay/reversal_sensitivity.md \
  --multi-episode-out artifacts/replay/reversal_multi_episode_sensitivity.jsonl
```

`--setup-memory-bars` and `--invalidation-atr-mult` affect the normal replay,
blocker report, and episode extraction parameters. Sensitivity aggregation uses
`--sensitivity-setup-memory-bars-list` and `--sensitivity-invalidation-atr-mult-list`.
When a sensitivity list flag is omitted, a non-zero single replay flag seeds a
single-value sensitivity list; explicit sensitivity lists always take precedence.

Dataset manifest mode:

```json
[
  {
    "symbol": "BTCUSDT",
    "timeframe": "1m",
    "input_csv": "reversal_BTCUSDT_20260520.csv",
    "start": "2026-05-20T13:30:00Z",
    "end": "2026-05-20T13:45:00Z",
    "warmup": 219
  }
]
```

Use `warmup` as the canonical field. It accepts a candle count such as `219` or a
duration such as `219m`. `warmup_candles` is accepted as a backward-compatible
alias; if both fields are present, `warmup` is used. A missing or zero warmup
produces an explicit dataset diagnostic when the first target-window indicators
are not ready.

`--episodes-out` writes offline episode datasets with summary, target-window timeline rows,
blocker/setup/invalidation events, and orderbook diagnostic events when available. The original
CSV remains the source for warmup and raw candle data.

`--multi-episode-out` writes aggregate sensitivity diagnostics across extracted episodes:
total episodes, candidate-ready episodes, false-invalidated episodes, average max score,
orderbook availability, candidate rate, and invalidation rate. It does not choose parameters.

Safety guarantees:

- `TradeEnabled=false` cannot emit `ModeReversalLong`.
- `TradeEnabled=true` can only produce a dry-run signal through the common pipeline.
- replay does not run `Trader()` and does not call the live order endpoint.
- `orders_sent` must remain `0`.
- sensitivity reports never write production config and do not change defaults.
- setup memory is replay-only and is disabled when `setup_memory_bars=0`.
- episode extraction and multi-episode sensitivity are offline diagnostics only.
- runtime simulation records `reversal_long_runtime_gate`, `reversal_long_risk`, and `reversal_long_tpsl` events.
- detector evaluation alone cannot write to `SigChan` or open positions.

The replay summary reports candidate counts, pipeline selection counts, runtime pass/block counts, blockers, and `orders_sent`.
