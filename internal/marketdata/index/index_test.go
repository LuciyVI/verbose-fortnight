package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/internal/marketdata/storage"
)

func TestSQLiteSchemaCreation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	var name string
	if err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='marketdata_partitions'`).Scan(&name); err != nil {
		t.Fatalf("schema table missing: %v", err)
	}
}

func TestIndexInsertUpdateFinalize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	ts := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	filePath := writeSnapshotPartition(t, dir, "BTCUSDT", ts)
	partition, err := db.OpenPartition(ctx, Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     "BTCUSDT",
		Interval:   "1s",
		FilePath:   filePath,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("open partition: %v", err)
	}
	partition.TimeMin = ts
	partition.TimeMax = ts
	partition.RowsTotal = 1
	partition.ValidRows = 1
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("update partition: %v", err)
	}
	finalized, err := db.FinalizePartition(ctx, partition, StatusClosed)
	if err != nil {
		t.Fatalf("finalize partition: %v", err)
	}
	if finalized.Status != StatusClosed || finalized.CoveragePct != 100 || finalized.FileSizeBytes <= 0 || finalized.SHA256 == "" {
		t.Fatalf("unexpected finalized partition: %+v", finalized)
	}
}

func TestQueryPartitionsByTimeRange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	day1 := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	addIndexedPartition(t, ctx, db, dir, "BTCUSDT", day1, day1.Add(time.Minute), 60, 60)
	addIndexedPartition(t, ctx, db, dir, "BTCUSDT", day2, day2.Add(time.Minute), 60, 30)
	items, err := db.QueryPartitions(ctx, PartitionFilter{
		Source:   "bybit",
		Kind:     "orderbook",
		Symbol:   "BTCUSDT",
		Interval: "1s",
		From:     day2.Add(-time.Second),
		To:       day2.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != 1 || !items[0].TimeMin.Equal(day2) {
		t.Fatalf("unexpected query items: %+v", items)
	}
}

func TestCoverageSummary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	day := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	addIndexedPartition(t, ctx, db, dir, "BTCUSDT", day, day.Add(time.Hour), 100, 80)
	summary, err := db.Coverage(ctx, PartitionFilter{
		Source:   "bybit",
		Kind:     "orderbook",
		Symbol:   "BTCUSDT",
		Interval: "1s",
		From:     day,
		To:       day.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if summary.RowsTotal != 100 || summary.ValidRows != 80 || summary.CoveragePct != 80 || summary.Recommendation != RecommendationUsableForEnrichment {
		t.Fatalf("unexpected coverage summary: %+v", summary)
	}
}

func TestValidationDetectsMissingFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	missingPath := filepath.Join(dir, "missing.jsonl.gz")
	if _, err := db.OpenPartition(ctx, Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     "BTCUSDT",
		Interval:   "1s",
		FilePath:   missingPath,
		Compressed: true,
	}); err != nil {
		t.Fatalf("open missing partition: %v", err)
	}
	summary, err := db.Validate(ctx, ValidationOptions{Filter: PartitionFilter{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s"}})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if summary.MissingFiles != 1 || len(summary.Issues) != 1 {
		t.Fatalf("missing file not detected: %+v", summary)
	}
}

func TestValidationDetectsCorruptedGzip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	path := filepath.Join(dir, "bad.jsonl.gz")
	if err := os.WriteFile(path, []byte("not gzip"), 0o644); err != nil {
		t.Fatalf("write corrupt gzip: %v", err)
	}
	partition, err := db.OpenPartition(ctx, Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     "BTCUSDT",
		Interval:   "1s",
		FilePath:   path,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("open corrupt partition: %v", err)
	}
	partition.Status = StatusClosed
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("mark corrupt partition closed: %v", err)
	}
	summary, err := db.Validate(ctx, ValidationOptions{Filter: PartitionFilter{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s"}, Repair: true})
	if err != nil {
		t.Fatalf("validate corrupt: %v", err)
	}
	if summary.CorruptedFiles != 1 || summary.RepairedPartitions != 1 {
		t.Fatalf("corruption not detected/repaired: %+v", summary)
	}
	partition, err = db.PartitionByPath(ctx, path)
	if err != nil {
		t.Fatalf("read repaired partition: %v", err)
	}
	if partition.Status != StatusCorrupted {
		t.Fatalf("corrupt partition status got %q", partition.Status)
	}
}

func TestValidationWarnsForOpenGzipPartition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := openTestDBAt(t, filepath.Join(dir, "index.sqlite"))
	defer db.Close()
	ts := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	path := writeSnapshotPartition(t, dir, "BTCUSDT", ts)
	partition, err := db.OpenPartition(ctx, Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     "BTCUSDT",
		Interval:   "1s",
		FilePath:   path,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("open partition: %v", err)
	}
	partition.TimeMin = ts
	partition.TimeMax = ts
	partition.RowsTotal = 1
	partition.ValidRows = 1
	partition.Status = StatusOpen
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("update open gzip: %v", err)
	}
	summary, err := db.Validate(ctx, ValidationOptions{Filter: PartitionFilter{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s"}})
	if err != nil {
		t.Fatalf("validate open gzip: %v", err)
	}
	if len(summary.Warnings) != 1 || summary.Warnings[0].Message != "open_gzip_partition_not_live_query_safe" {
		t.Fatalf("missing open gzip warning: %+v", summary)
	}
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	return openTestDBAt(t, filepath.Join(t.TempDir(), "index.sqlite"))
}

func openTestDBAt(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func addIndexedPartition(t *testing.T, ctx context.Context, db *DB, root, symbol string, start, end time.Time, rows, valid int64) Partition {
	t.Helper()
	filePath := writeSnapshotPartition(t, root, symbol, start)
	partition, err := db.OpenPartition(ctx, Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     symbol,
		Interval:   "1s",
		FilePath:   filePath,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("open partition: %v", err)
	}
	partition.TimeMin = start
	partition.TimeMax = end
	partition.RowsTotal = rows
	partition.ValidRows = valid
	partition.UnavailableRows = rows - valid
	partition.Status = StatusClosed
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("update partition: %v", err)
	}
	return partition
}

func writeSnapshotPartition(t *testing.T, root, symbol string, ts time.Time) string {
	t.Helper()
	cfg := storage.PartitionConfig{Root: root, Source: "bybit", Kind: "orderbook", Symbol: symbol, Interval: "1s", Compress: true}
	writer := storage.NewPartitionWriter(cfg)
	snapshot, err := orderbook.SnapshotFromLevels(symbol, ts, []orderbook.Level{{Price: 100, Size: 2}}, []orderbook.Level{{Price: 100.1, Size: 1}}, 1, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := snapshot.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	path, _, err := writer.WriteLine(ts, raw)
	if err != nil {
		t.Fatalf("write partition: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close partition: %v", err)
	}
	return path
}
