# Reversal Long Blocker Analysis

This workflow is offline-only. It is intended to explain why a replay did or did not produce
`ModeReversalLong` without changing production defaults.

## Blocker Timeline

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --out artifacts/replay/reversal_signal_dryrun.jsonl \
  --blocker-report artifacts/replay/reversal_blockers.md
```

The markdown report includes a per-candle target-window timeline and a closest-candidate summary.
The timeline shows RSI/MACD deltas, BB reclaim state, no-new-low progress, score, HTF status,
orderbook availability, and reject reason.

Optional setup memory can be enabled for replay analysis only:

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --setup-memory-bars 5 \
  --invalidation-atr-mult 0.2 \
  --out artifacts/replay/reversal_signal_dryrun.jsonl \
  --blocker-report artifacts/replay/reversal_blockers.md
```

## Setup/Trigger Split

The replay-only setup memory separates reversal detection into two phases:

- setup phase: capitulation and lower-BB reclaim are observed;
- trigger phase: later candles can use the remembered reclaim while checking RSI recovery,
  MACD histogram recovery, no-new-low stabilization, HTF, orderbook, and score threshold.

`setup_memory_bars=0` disables this overlay. When enabled, the report adds:

- `setup_detected_at`
- `trigger_checked_at`
- `setup_age_bars`
- `setup_invalidated`
- `invalidation_reason`
- `memory_reclaim_used`
- `main_blocker_detail`
- `first_setup_detected_at`
- `first_invalidation_at`
- `invalidation_after_bars`
- `reclaim_was_remembered`

Setup memory is invalidated by memory expiry, a repeated close below the lower Bollinger Band,
a substantial lower low, or explicit momentum worsening. This overlay is not wired into production
signal generation.

If setup memory is enabled, a lower-BB reclaim is detected, and the setup is invalidated before
a trigger can form, `main_blocker` is reported as `setup_invalidated` instead of falling back to
`no_lower_bb_reclaim`. The concrete invalidation is kept in `main_blocker_detail`.

## Sensitivity Matrix

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

Sensitivity is a report only:

- it does not modify `config.DefaultReversalLongConfig`;
- it does not write config files;
- it does not enable live orders;
- setup memory exists only in replay/sensitivity reports;
- sensitivity setup-memory aggregation uses `--sensitivity-setup-memory-bars-list`
  and `--sensitivity-invalidation-atr-mult-list`; the single replay flags
  `--setup-memory-bars` and `--invalidation-atr-mult` affect replay/blocker
  parameters and seed a single sensitivity value only when the corresponding
  sensitivity list flag is omitted;
- `orders_sent` must remain `0` for every combination.

Do not calibrate from one episode. A single missed setup can show which gates were binding, but
stable parameters require more episodes and historical orderbook snapshots.

## Episode Dataset

```bash
go run ./cmd/replay-reversal \
  --symbol BTCUSDT \
  --from 2026-05-20T13:39:00Z \
  --to 2026-05-20T13:50:52Z \
  --input artifacts/replay/reversal_BTCUSDT_20260520.csv \
  --reversal-enabled \
  --reversal-trade-enabled=true \
  --orders-enabled=false \
  --setup-memory-bars 5 \
  --invalidation-atr-mult 0.2 \
  --out artifacts/replay/reversal_signal_dryrun.jsonl \
  --episodes-out artifacts/replay/reversal_episodes.json
```

Each episode summary includes classification:

- `true_reversal_candidate`
- `false_reversal_invalidated`
- `no_setup`
- `data_gap_orderbook_unavailable`

Episode datasets include the summary, target timeline, replay decisions, setup/trigger fields,
invalidation details, and orderbook diagnostics when present. The raw warmup candles remain in
the replay CSV.

For dataset manifests, use `warmup` as the canonical field. `warmup_candles` is
accepted as a backward-compatible alias, and `warmup` wins when both are present.
Missing or zero warmup produces an explicit diagnostic when the target-window
indicator state starts unready.

## Orderbook Enrichment

The replay CSV supports optional orderbook columns. Missing fields remain unavailable.
Do not fill blanks with neutral or synthetic values.

Useful real fields are:

- `best_bid`
- `best_ask`
- `best_bid_size`
- `best_ask_size`
- `bid_ask_imbalance`
- `spread_bps`
- `orderbook_ratio`

If logs contain only orderbook deltas without a prior full snapshot, top-book reconstruction is
not reliable enough for replay enrichment. In that case leave orderbook columns blank and treat
runtime orderbook calibration as unavailable.

## Safety

`cmd/replay-reversal` refuses `--orders-enabled=true`. Replay does not call the live order endpoint,
does not run a separate executor, and does not create a live order path.
