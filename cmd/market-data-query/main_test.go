package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mdindex "verbose-fortnight/internal/marketdata/index"
	"verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/internal/marketdata/storage"
)

func TestOrderbookQueryExportsOpenPlainPartitionWhileWriterActive(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	cfg := storage.PartitionConfig{Root: dir, Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s", Compress: false}
	writer := storage.NewPartitionWriter(cfg)
	path := writeSnapshotWithWriter(t, writer, "BTCUSDT", base)
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush active writer: %v", err)
	}
	partition, err := db.OpenPartition(ctx, mdindex.Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     "BTCUSDT",
		Interval:   "1s",
		FilePath:   path,
		Compressed: false,
		Status:     mdindex.StatusOpen,
	})
	if err != nil {
		t.Fatalf("open partition: %v", err)
	}
	partition.TimeMin = base
	partition.TimeMax = base
	partition.RowsTotal = 1
	partition.ValidRows = 1
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("update open partition: %v", err)
	}
	defer writer.Close()
	defer db.Close()

	outPath := filepath.Join(dir, "open.jsonl")
	summary, err := runOrderbookQuery(ctx, orderbookQueryOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   base,
		To:     base,
		Root:   dir,
		Index:  indexPath,
		Out:    outPath,
		Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("query open partition: %v", err)
	}
	if summary.RowsExported != 1 || len(summary.Warnings) != 0 {
		t.Fatalf("unexpected open query summary: %+v", summary)
	}
	if lines := readLines(t, outPath); len(lines) != 1 {
		t.Fatalf("exported lines from open partition got %d: %+v", len(lines), lines)
	}
}

func TestOrderbookQueryOneDayFiltersTimestamps(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{base.Add(-time.Second), base, base.Add(time.Second)})
	db.Close()

	outPath := filepath.Join(dir, "out.jsonl")
	summary, err := runOrderbookQuery(ctx, orderbookQueryOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   base,
		To:     base,
		Root:   dir,
		Index:  indexPath,
		Out:    outPath,
		Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if summary.RowsScanned != 3 || summary.RowsExported != 1 || summary.ValidRows != 1 || summary.FilesUsed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	lines := readLines(t, outPath)
	if len(lines) != 1 || !strings.Contains(lines[0], `"timestamp":"2026-06-12T12:00:00Z"`) {
		t.Fatalf("unexpected exported lines: %+v", lines)
	}
}

func TestOrderbookQueryAcrossMultipleDays(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	day1 := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	day2 := day1.Add(time.Second)
	writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{day1})
	writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{day2})
	db.Close()

	outPath := filepath.Join(dir, "multi.jsonl")
	summary, err := runOrderbookQuery(ctx, orderbookQueryOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   day1,
		To:     day2,
		Root:   dir,
		Index:  indexPath,
		Out:    outPath,
		Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("query multi-day: %v", err)
	}
	if summary.RowsExported != 2 || summary.FilesUsed != 2 || len(summary.MissingPartitions) != 0 {
		t.Fatalf("unexpected multi-day summary: %+v", summary)
	}
	if lines := readLines(t, outPath); len(lines) != 2 {
		t.Fatalf("exported line count got %d: %+v", len(lines), lines)
	}
}

func TestOrderbookQueryAcrossOpenJSONLAndClosedGzip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	day1 := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	day2 := day1.Add(time.Second)
	writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{day1})

	cfg := storage.PartitionConfig{Root: dir, Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s", Compress: false}
	writer := storage.NewPartitionWriter(cfg)
	openPath := writeSnapshotWithWriter(t, writer, "BTCUSDT", day2)
	if err := writer.Flush(); err != nil {
		t.Fatalf("flush active writer: %v", err)
	}
	partition, err := db.OpenPartition(ctx, mdindex.Partition{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s", FilePath: openPath, Status: mdindex.StatusOpen})
	if err != nil {
		t.Fatalf("open live partition: %v", err)
	}
	partition.TimeMin = day2
	partition.TimeMax = day2
	partition.RowsTotal = 1
	partition.ValidRows = 1
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("update live partition: %v", err)
	}
	defer writer.Close()
	defer db.Close()

	outPath := filepath.Join(dir, "mixed.jsonl")
	summary, err := runOrderbookQuery(ctx, orderbookQueryOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   day1,
		To:     day2,
		Root:   dir,
		Index:  indexPath,
		Out:    outPath,
		Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("query mixed partitions: %v", err)
	}
	if summary.RowsExported != 2 || summary.FilesUsed != 2 {
		t.Fatalf("unexpected mixed summary: %+v", summary)
	}
}

func TestOrderbookQueryWarnsForLegacyOpenGzipPartition(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	path := writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{base})
	partition, err := db.PartitionByPath(ctx, path)
	if err != nil {
		t.Fatalf("partition by path: %v", err)
	}
	partition.Status = mdindex.StatusOpen
	partition.Compressed = true
	partition.Compression = "gzip"
	if err := db.UpdatePartition(ctx, partition); err != nil {
		t.Fatalf("mark open gzip: %v", err)
	}
	db.Close()

	outPath := filepath.Join(dir, "legacy.jsonl")
	summary, err := runOrderbookQuery(ctx, orderbookQueryOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   base,
		To:     base,
		Root:   dir,
		Index:  indexPath,
		Out:    outPath,
		Format: "jsonl",
	})
	if err != nil {
		t.Fatalf("query legacy open gzip: %v", err)
	}
	if len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0], "open_gzip_partition_not_live_query_safe") {
		t.Fatalf("missing legacy warning: %+v", summary)
	}
}

func TestCoverageCommandSummary(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	indexPath := filepath.Join(dir, "index.sqlite")
	db, err := mdindex.Open(indexPath)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	writeQueryPartition(t, ctx, db, dir, "BTCUSDT", []time.Time{base, base.Add(time.Second)})
	db.Close()

	summary, err := runCoverage(ctx, coverageOptions{
		Source: "bybit",
		Symbol: "BTCUSDT",
		From:   base,
		To:     base.Add(time.Second),
		Index:  indexPath,
		Root:   dir,
	})
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	if summary.RowsTotal != 2 || summary.ValidRows != 2 || summary.CoveragePct != 100 || summary.Recommendation != mdindex.RecommendationUsableForEnrichment {
		t.Fatalf("unexpected coverage summary: %+v", summary)
	}
}

func writeQueryPartition(t *testing.T, ctx context.Context, db *mdindex.DB, root, symbol string, timestamps []time.Time) string {
	t.Helper()
	if len(timestamps) == 0 {
		t.Fatalf("timestamps required")
	}
	cfg := storage.PartitionConfig{Root: root, Source: "bybit", Kind: "orderbook", Symbol: symbol, Interval: "1s", Compress: true}
	writer := storage.NewPartitionWriter(cfg)
	var path string
	var validRows int64
	for _, ts := range timestamps {
		snapshot, err := orderbook.SnapshotFromLevels(symbol, ts, []orderbook.Level{{Price: 100, Size: 2}}, []orderbook.Level{{Price: 100.1, Size: 1}}, 1, "")
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		raw, err := snapshot.MarshalJSONLine()
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		path, _, err = writer.WriteLine(ts, raw)
		if err != nil {
			t.Fatalf("write: %v", err)
		}
		validRows++
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	partition, err := db.OpenPartition(ctx, mdindex.Partition{
		Source:     "bybit",
		Kind:       "orderbook",
		Symbol:     symbol,
		Interval:   "1s",
		FilePath:   path,
		Compressed: true,
	})
	if err != nil {
		t.Fatalf("open partition: %v", err)
	}
	partition.TimeMin = timestamps[0]
	partition.TimeMax = timestamps[len(timestamps)-1]
	partition.RowsTotal = int64(len(timestamps))
	partition.ValidRows = validRows
	partition.Status = mdindex.StatusClosed
	finalized, err := db.FinalizePartition(ctx, partition, mdindex.StatusClosed)
	if err != nil {
		t.Fatalf("finalize partition: %v", err)
	}
	if finalized.FileSizeBytes == 0 || finalized.SHA256 == "" {
		t.Fatalf("finalized metadata missing: %+v", finalized)
	}
	return finalized.FilePath
}

func writeSnapshotWithWriter(t *testing.T, writer *storage.PartitionWriter, symbol string, ts time.Time) string {
	t.Helper()
	snapshot, err := orderbook.SnapshotFromLevels(symbol, ts, []orderbook.Level{{Price: 100, Size: 2}}, []orderbook.Level{{Price: 100.1, Size: 1}}, 1, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := snapshot.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path, _, err := writer.WriteLine(ts, raw)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}
