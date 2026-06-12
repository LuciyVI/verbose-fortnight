# Market Data Collection

This repository includes a data-only market data collector for replay enrichment. It records public Bybit orderbook snapshots, writes partitioned JSONL files, and maintains a SQLite partition index from the first write.

The collector never sends orders, never calls order placement APIs, and does not require trading to be enabled.

## Build

```bash
go build ./cmd/market-data-collector
go build ./cmd/market-data-query
```

## Collector

```bash
go run ./cmd/market-data-collector \
  --source bybit \
  --symbols BTCUSDT,ETHUSDT,SOLUSDT \
  --snapshot-interval 1s \
  --out-root artifacts/marketdata \
  --index artifacts/marketdata/index.sqlite \
  --compress=true \
  --depth 5
```

The collector writes one normalized JSON object per snapshot with `event="orderbook_diagnostic_snapshot"` and `source="live_orderbook_snapshot"`. The emitted fields are compatible with `cmd/replay-reversal` orderbook validation and enrichment.

## Storage Layout

Open partitions are UTC daily plain JSONL files:

```text
artifacts/marketdata/bybit/orderbook/<SYMBOL>/1s/YYYY/MM/DD.jsonl
```

On UTC daily rotation or graceful shutdown, the collector closes the plain JSONL file. When `--compress=true`, it then compresses the finalized partition to:

```text
artifacts/marketdata/bybit/orderbook/<SYMBOL>/1s/YYYY/MM/DD.jsonl.gz
```

The SQLite index points open rows at `.jsonl` with `compressed=false`, and closed compressed rows at `.jsonl.gz` with `compressed=true`. This keeps live-period queries readable while the collector is running.

## SQLite Index

Default index path:

```text
artifacts/marketdata/index.sqlite
```

The collector creates the schema automatically. The `marketdata_partitions` table tracks one row per partition file:

```text
source, kind, symbol, interval, file_path,
time_min, time_max,
rows_total, valid_rows, unavailable_rows, coverage_pct,
compressed, compression, sha256, file_size_bytes,
status, created_at, updated_at
```

`status` is one of `open`, `closed`, or `corrupted`. The collector opens the current partition row on startup, updates counters while writing, closes the old row on UTC day rotation, optionally compresses the finalized file, and finalizes the current row on shutdown.

## Query A Period

```bash
go run ./cmd/market-data-query orderbook \
  --source bybit \
  --symbol BTCUSDT \
  --from 2026-06-12T00:00:00Z \
  --to 2026-06-12T23:59:59Z \
  --root artifacts/marketdata \
  --index artifacts/marketdata/index.sqlite \
  --out artifacts/replay/BTCUSDT_orderbook_20260612.jsonl \
  --format jsonl \
  --validate
```

The query uses the SQLite index first, reads only overlapping partition files, filters records by timestamp in `[from, to]`, and prints a summary with coverage and a recommendation.

CSV export is also available:

```bash
go run ./cmd/market-data-query orderbook \
  --source bybit \
  --symbol BTCUSDT \
  --from 2026-06-12T00:00:00Z \
  --to 2026-06-12T23:59:59Z \
  --index artifacts/marketdata/index.sqlite \
  --out artifacts/marketdata/BTCUSDT_orderbook_20260612.csv \
  --format csv
```

## Coverage

```bash
go run ./cmd/market-data-query coverage \
  --source bybit \
  --symbol BTCUSDT \
  --from 2026-06-12T00:00:00Z \
  --to 2026-06-12T23:59:59Z \
  --index artifacts/marketdata/index.sqlite
```

Coverage uses index metadata only. Add `--deep-validate` to decompress matching files and check them before reporting.

Recommendations:

- `USABLE_FOR_ENRICHMENT`: indexed coverage is high enough and expected daily partitions exist.
- `PARTIAL_DATA`: data exists but has gaps, low coverage, or validation issues.
- `NO_DATA`: no matching indexed rows were found.

## Validate

```bash
go run ./cmd/market-data-query validate \
  --source bybit \
  --symbol BTCUSDT \
  --root artifacts/marketdata \
  --index artifacts/marketdata/index.sqlite
```

Validation checks that indexed files exist, gzip files are readable, file timestamps match indexed ranges, and counters match actual rows. To repair counters and mark unreadable files as corrupted:

```bash
go run ./cmd/market-data-query validate \
  --source bybit \
  --symbol BTCUSDT \
  --root artifacts/marketdata \
  --index artifacts/marketdata/index.sqlite \
  --repair
```

## Replay Enrichment

Export a replay-compatible JSONL first:

```bash
go run ./cmd/market-data-query orderbook \
  --source bybit \
  --symbol BTCUSDT \
  --from 2026-06-12T00:00:00Z \
  --to 2026-06-12T23:59:59Z \
  --index artifacts/marketdata/index.sqlite \
  --out artifacts/replay/BTCUSDT_orderbook_20260612.jsonl \
  --format jsonl
```

Validate it with the replay tool:

```bash
go run ./cmd/replay-reversal \
  --validate-orderbook-jsonl artifacts/replay/BTCUSDT_orderbook_20260612.jsonl
```

Enrich candles:

```bash
go run ./cmd/replay-reversal \
  --enrich-orderbook \
  --enrich-input-csv artifacts/replay/BTCUSDT_1m.csv \
  --enrich-orderbook-jsonl artifacts/replay/BTCUSDT_orderbook_20260612.jsonl \
  --enrich-output-csv artifacts/replay/BTCUSDT_1m_enriched.csv \
  --enrich-symbol BTCUSDT
```

## Dataset Run

Use the enriched CSV in the dataset manifest, then run replay with orders disabled:

```bash
go run ./cmd/replay-reversal \
  --dataset-manifest artifacts/replay/dataset_manifest.json \
  --orders-enabled=false
```

Replay still rejects `--orders-enabled=true`, and dataset/sensitivity replay paths keep `OrdersEnabled=false`.

## Systemd

A sample unit is available at `docs/systemd/market-data-collector.service`.

Install by copying it to `/etc/systemd/system/market-data-collector.service`, adjusting `WorkingDirectory` and `User`, then running:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now market-data-collector.service
```

## Operational Notes

- Monitor disk usage under `artifacts/marketdata`; daily gzip partitions make cleanup predictable.
- Keep `index.sqlite` on persistent storage with the partition files.
- Use `market-data-query coverage` before expensive replay jobs.
- Use `market-data-query validate --repair` after crashes or host restarts.
- The current partition is kept `open` as plain `.jsonl`; shutdown or rotation finalizes it as `closed`.
- Legacy open `.jsonl.gz` rows are still readable when complete, but query and validation commands warn with `open_gzip_partition_not_live_query_safe` because an actively written gzip file cannot be queried safely.
- If a write fails, the collector logs the error and continues collecting later snapshots when possible.
