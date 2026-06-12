package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrepareReversalInputWritesReplaySchema(t *testing.T) {
	out := filepath.Join(t.TempDir(), "replay.csv")
	klines := []kline{{
		Time:   time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		Open:   100,
		High:   101,
		Low:    99,
		Close:  100.5,
		Volume: 42,
	}}
	if err := writeReplayCSV(out, klines); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "time,open,high,low,close,volume,best_bid,best_ask,best_bid_size,best_ask_size,bid_ask_imbalance,spread_bps") {
		t.Fatalf("missing replay schema header: %s", text)
	}
	if !strings.Contains(text, "2026-05-20T10:00:00Z,100,101,99,100.5,42") {
		t.Fatalf("missing kline row: %s", text)
	}
}

func TestPrepareReversalInputHostGuard(t *testing.T) {
	if !stringsHasHTTPPrefix("https://api.bybit.com") {
		t.Fatalf("expected https host to pass")
	}
	if stringsHasHTTPPrefix("/v5/order/create") {
		t.Fatalf("private path should not pass host guard")
	}
}
