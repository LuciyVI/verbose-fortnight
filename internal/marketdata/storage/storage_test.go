package storage

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPartitionPathGeneration(t *testing.T) {
	ts := time.Date(2026, 6, 12, 15, 4, 5, 0, time.FixedZone("local", -5*3600))
	path := PartitionPath(PartitionConfig{
		Root:     "artifacts/marketdata",
		Source:   "bybit",
		Kind:     "orderbook",
		Symbol:   "btcusdt",
		Interval: "1s",
		Compress: true,
	}, ts)
	want := filepath.Join("artifacts", "marketdata", "bybit", "orderbook", "BTCUSDT", "1s", "2026", "06", "12.jsonl.gz")
	if path != want {
		t.Fatalf("path got %q want %q", path, want)
	}
}

func TestUTCDailyRotation(t *testing.T) {
	dir := t.TempDir()
	writer := NewPartitionWriter(PartitionConfig{Root: dir, Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s", Compress: true})
	first := time.Date(2026, 6, 12, 23, 59, 59, 0, time.UTC)
	second := first.Add(time.Second)
	path1, rotated, err := writer.WriteLine(first, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	if rotated {
		t.Fatalf("first write should not report rotation")
	}
	path2, rotated, err := writer.WriteLine(second, []byte(`{"n":2}`))
	if err != nil {
		t.Fatalf("write second: %v", err)
	}
	if !rotated {
		t.Fatalf("second write should rotate across UTC day")
	}
	if path1 == path2 {
		t.Fatalf("rotation kept same path %q", path1)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := readAllLines(t, path1); len(got) != 1 || !strings.Contains(got[0], `"n":1`) {
		t.Fatalf("old partition lines: %+v", got)
	}
	if got := readAllLines(t, path2); len(got) != 1 || !strings.Contains(got[0], `"n":2`) {
		t.Fatalf("new partition lines: %+v", got)
	}
}

func TestAppendExistingCurrentDayPartition(t *testing.T) {
	dir := t.TempDir()
	cfg := PartitionConfig{Root: dir, Source: "bybit", Kind: "orderbook", Symbol: "BTCUSDT", Interval: "1s", Compress: true}
	ts := time.Date(2026, 6, 12, 1, 0, 0, 0, time.UTC)
	writer := NewPartitionWriter(cfg)
	path, _, err := writer.WriteLine(ts, []byte(`{"n":1}`))
	if err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	writer = NewPartitionWriter(cfg)
	if _, _, err := writer.WriteLine(ts.Add(time.Second), []byte(`{"n":2}`)); err != nil {
		t.Fatalf("write append: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close append: %v", err)
	}
	lines := readAllLines(t, path)
	if len(lines) != 2 || !strings.Contains(lines[0], `"n":1`) || !strings.Contains(lines[1], `"n":2`) {
		t.Fatalf("append lines: %+v", lines)
	}
}

func TestGzipWriteRead(t *testing.T) {
	dir := t.TempDir()
	writer := NewPartitionWriter(PartitionConfig{Root: dir, Source: "bybit", Kind: "orderbook", Symbol: "ETHUSDT", Interval: "1s", Compress: true})
	path, _, err := writer.WriteLine(time.Date(2026, 6, 12, 2, 0, 0, 0, time.UTC), []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reader, err := OpenReader(path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if strings.TrimSpace(string(raw)) != `{"ok":true}` {
		t.Fatalf("gzip content %q", raw)
	}
	meta, err := Metadata(path)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if meta.SHA256 == "" || meta.FileSizeBytes <= 0 {
		t.Fatalf("metadata not populated: %+v", meta)
	}
}

func readAllLines(t *testing.T, path string) []string {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	reader, err := OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}
