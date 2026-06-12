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

type fakeFetcher struct {
	snapshot  orderbook.Snapshot
	snapshots []orderbook.Snapshot
	err       error
	calls     int
}

func (f *fakeFetcher) FetchOrderbook(ctx context.Context, symbol string, depth int) (orderbook.Snapshot, error) {
	f.calls++
	if f.err != nil {
		return orderbook.Snapshot{}, f.err
	}
	if len(f.snapshots) > 0 {
		out := f.snapshots[0]
		f.snapshots = f.snapshots[1:]
		out.Symbol = symbol
		out.DepthLevels = depth
		return out, nil
	}
	out := f.snapshot
	out.Symbol = symbol
	out.DepthLevels = depth
	return out, nil
}

func TestCollectorCollectOnceWritesDataOnlySnapshot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := mdindex.Open(filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer db.Close()
	ts := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	snapshot, err := orderbook.SnapshotFromLevels("BTCUSDT", ts, []orderbook.Level{{Price: 100, Size: 2}}, []orderbook.Level{{Price: 100.1, Size: 1}}, 5, "")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fetcher := &fakeFetcher{snapshot: snapshot}
	c := newCollector(collectorOptions{
		Source:           "bybit",
		Symbols:          []string{"BTCUSDT"},
		SnapshotInterval: time.Second,
		OutRoot:          dir,
		IndexPath:        filepath.Join(dir, "index.sqlite"),
		Compress:         true,
		Depth:            5,
	}, db, fetcher, nil)
	c.collectOnce(ctx, ts)
	partitions, err := db.QueryPartitions(ctx, mdindex.PartitionFilter{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s"})
	if err != nil {
		t.Fatalf("query partitions: %v", err)
	}
	if len(partitions) != 1 || partitions[0].RowsTotal != 1 || partitions[0].ValidRows != 1 ||
		partitions[0].Status != mdindex.StatusOpen || partitions[0].Compressed || partitions[0].Compression != "" ||
		!strings.HasSuffix(partitions[0].FilePath, ".jsonl") || strings.HasSuffix(partitions[0].FilePath, ".gz") {
		t.Fatalf("unexpected partitions: %+v", partitions)
	}
	lines := readCollectorPartitionLines(t, partitions[0].FilePath)
	if len(lines) != 1 || !strings.Contains(lines[0], `"order_sent":false`) || !strings.Contains(lines[0], `"dry_run":false`) {
		t.Fatalf("collector snapshot is not data-only: %+v", lines)
	}
	if err := c.shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestCollectorRotationCompressesPreviousDayAndUpdatesIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := mdindex.Open(filepath.Join(dir, "index.sqlite"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer db.Close()
	day1 := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	day2 := day1.Add(time.Second)
	snapshot1, err := orderbook.SnapshotFromLevels("BTCUSDT", day1, []orderbook.Level{{Price: 100, Size: 2}}, []orderbook.Level{{Price: 100.1, Size: 1}}, 5, "")
	if err != nil {
		t.Fatalf("snapshot1: %v", err)
	}
	snapshot2, err := orderbook.SnapshotFromLevels("BTCUSDT", day2, []orderbook.Level{{Price: 101, Size: 2}}, []orderbook.Level{{Price: 101.1, Size: 1}}, 5, "")
	if err != nil {
		t.Fatalf("snapshot2: %v", err)
	}
	fetcher := &fakeFetcher{snapshots: []orderbook.Snapshot{snapshot1, snapshot2}}
	c := newCollector(collectorOptions{
		Source:           "bybit",
		Symbols:          []string{"BTCUSDT"},
		SnapshotInterval: time.Second,
		OutRoot:          dir,
		IndexPath:        filepath.Join(dir, "index.sqlite"),
		Compress:         true,
		Depth:            5,
	}, db, fetcher, nil)
	c.collectOnce(ctx, day1)
	firstOpenPath := c.states["BTCUSDT"].partition.FilePath
	c.collectOnce(ctx, day2)
	partitions, err := db.QueryPartitions(ctx, mdindex.PartitionFilter{Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s"})
	if err != nil {
		t.Fatalf("query partitions: %v", err)
	}
	if len(partitions) != 2 {
		t.Fatalf("partition count got %d: %+v", len(partitions), partitions)
	}
	if partitions[0].Status != mdindex.StatusClosed || !partitions[0].Compressed || partitions[0].Compression != "gzip" || !strings.HasSuffix(partitions[0].FilePath, ".jsonl.gz") {
		t.Fatalf("previous day not finalized as gzip: %+v", partitions[0])
	}
	if _, err := os.Stat(firstOpenPath); !os.IsNotExist(err) {
		t.Fatalf("plain previous-day path should be removed after compression, stat err=%v", err)
	}
	if _, err := os.Stat(partitions[0].FilePath); err != nil {
		t.Fatalf("compressed previous-day file missing: %v", err)
	}
	if partitions[1].Status != mdindex.StatusOpen || partitions[1].Compressed || !strings.HasSuffix(partitions[1].FilePath, ".jsonl") {
		t.Fatalf("current day should remain open plain jsonl: %+v", partitions[1])
	}
	if err := c.shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestCollectorDoesNotExposeOrderPlacementFlag(t *testing.T) {
	if _, err := parseFlags([]string{"--orders-enabled=true"}); err == nil {
		t.Fatalf("collector accepted an order-enabling flag")
	}
	if _, err := parseFlags([]string{"--source", "bybit", "--symbols", "BTCUSDT"}); err != nil {
		t.Fatalf("valid data-only flags failed: %v", err)
	}
}

func readCollectorPartitionLines(t *testing.T, path string) []string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat partition: %v", err)
	}
	reader, err := storage.OpenReader(path)
	if err != nil {
		t.Fatalf("open partition reader: %v", err)
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan partition: %v", err)
	}
	return lines
}
