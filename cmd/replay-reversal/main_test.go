package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verbose-fortnight/config"
	mdorderbook "verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/strategy"
)

func TestReplayRefusesOrdersEnabled(t *testing.T) {
	if err := validateReplaySafety(true); err == nil {
		t.Fatalf("orders-enabled=true should be refused")
	}
	if err := validateReplaySafety(false); err != nil {
		t.Fatalf("orders-enabled=false should pass: %v", err)
	}
}

func TestOrderbookEnrichmentWithinStalenessEnrichesRow(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 1, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	writeOrderbookEnrichmentJSONL(t, obPath, map[string]any{
		"event":               "orderbook_diagnostic_snapshot",
		"timestamp":           candleTime.Add(-500 * time.Millisecond).Format(time.RFC3339Nano),
		"symbol":              "BTCUSDT",
		"orderbook_available": true,
		"best_bid":            100.1,
		"best_ask":            100.2,
		"best_bid_size":       2.5,
		"best_ask_size":       1.25,
		"bid_ask_imbalance":   0.3333,
		"spread_bps":          9.98,
		"orderbook_ratio":     2.0,
		"depth_levels":        50,
	})

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.RowsTotal != 1 || output.Summary.RowsEnriched != 1 || output.Summary.OrderbookCoveragePct != 100 {
		t.Fatalf("unexpected enrichment summary: %+v", output.Summary)
	}
	if output.Summary.MinSpreadBps != 9.98 || output.Summary.MaxSpreadBps != 9.98 || output.Summary.AvgSpreadBps != 9.98 {
		t.Fatalf("unexpected spread summary: %+v", output.Summary)
	}
	if _, err := os.Stat(output.SummaryPath); err != nil {
		t.Fatalf("missing enrichment summary file: %v", err)
	}
	rows := readOrderbookEnrichmentCSV(t, outPath)
	if rows[1][6] != "100.1" || rows[1][7] != "100.2" || rows[1][8] != "2.5" || rows[1][9] != "1.25" ||
		rows[1][10] != "0.3333" || rows[1][11] != "9.98" || rows[1][12] != "2" {
		t.Fatalf("row was not enriched from snapshot: %+v", rows[1])
	}
	candles, err := strategy.LoadReversalReplayCandlesCSV(outPath)
	if err != nil {
		t.Fatalf("enriched csv should parse for replay: %v", err)
	}
	if candles[0].BestBid != 100.1 || candles[0].BestAskSize != 1.25 || candles[0].Ratio != 2 {
		t.Fatalf("enriched orderbook fields not replay-readable: %+v", candles[0])
	}
}

func TestOrderbookEnrichmentRejectsStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 2, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	writeOrderbookEnrichmentJSONL(t, obPath, validOrderbookSnapshotEvent(candleTime.Add(-2*time.Second)))

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.RowsEnriched != 0 || output.Summary.StaleSnapshotCount != 1 || output.Summary.MissingSnapshotCount != 0 {
		t.Fatalf("stale snapshot was not rejected: %+v", output.Summary)
	}
	rows := readOrderbookEnrichmentCSV(t, outPath)
	if rows[1][6] != "" || rows[1][12] != "" {
		t.Fatalf("stale snapshot should leave orderbook fields empty: %+v", rows[1])
	}
}

func TestOrderbookEnrichmentIgnoresUnavailableSnapshot(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 1, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	event := validOrderbookSnapshotEvent(candleTime.Add(-500 * time.Millisecond))
	event["orderbook_available"] = false
	event["unavailable_reason"] = "orderbook_data_not_available"
	writeOrderbookEnrichmentJSONL(t, obPath, event)

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.RowsEnriched != 0 || output.Summary.MissingSnapshotCount != 1 || output.Summary.StaleSnapshotCount != 0 {
		t.Fatalf("unavailable snapshot should be ignored: %+v", output.Summary)
	}
	rows := readOrderbookEnrichmentCSV(t, outPath)
	if rows[1][6] != "" || rows[1][7] != "" || rows[1][8] != "" || rows[1][9] != "" {
		t.Fatalf("unavailable snapshot should leave top-book fields empty: %+v", rows[1])
	}
}

func TestOrderbookEnrichmentMissingSnapshotLeavesFieldsEmpty(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 1, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	if err := os.WriteFile(obPath, nil, 0o644); err != nil {
		t.Fatalf("write empty orderbook jsonl: %v", err)
	}

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.RowsTotal != 1 || output.Summary.RowsEnriched != 0 || output.Summary.MissingSnapshotCount != 1 {
		t.Fatalf("unexpected missing snapshot summary: %+v", output.Summary)
	}
	rows := readOrderbookEnrichmentCSV(t, outPath)
	if rows[1][6] != "" || rows[1][12] != "" {
		t.Fatalf("missing snapshot should leave orderbook fields empty: %+v", rows[1])
	}
}

func TestMarketDataOrderbookSnapshotPassesReplayValidator(t *testing.T) {
	ts := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	snapshot, err := mdorderbook.SnapshotFromLevels("BTCUSDT", ts,
		[]mdorderbook.Level{{Price: 100, Size: 2}},
		[]mdorderbook.Level{{Price: 100.1, Size: 1}},
		1,
		"")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	raw, err := snapshot.MarshalJSONLine()
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	summary, err := validateOrderbookJSONLReader(strings.NewReader(string(raw)), "BTCUSDT")
	if err != nil {
		t.Fatalf("validate marketdata jsonl: %v", err)
	}
	if summary.TotalSnapshots != 1 ||
		summary.ValidSnapshots != 1 ||
		summary.LiveSnapshotCount != 1 ||
		summary.Recommendation != orderbookValidationUsableForEnrichment {
		t.Fatalf("unexpected validation summary: %+v", summary)
	}
}

func TestOrderbookEnrichmentCoveragePercentage(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, start.Add(time.Second), start.Add(3*time.Second))
	writeOrderbookEnrichmentJSONL(t, obPath, validOrderbookSnapshotEvent(start.Add(time.Second)))

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.RowsTotal != 2 || output.Summary.RowsEnriched != 1 || output.Summary.OrderbookCoveragePct != 50 || output.Summary.StaleSnapshotCount != 1 {
		t.Fatalf("unexpected coverage summary: %+v", output.Summary)
	}
}

func TestOrderbookEnrichedCSVReplayCompatible(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 1, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	writeOrderbookEnrichmentJSONL(t, obPath, validOrderbookSnapshotEvent(candleTime))
	if _, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	}); err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	candles, err := strategy.LoadReversalReplayCandlesCSV(outPath)
	if err != nil {
		t.Fatalf("load enriched csv: %v", err)
	}
	result, err := strategy.RunReversalReplayCandles(candles, strategy.ReversalReplayOptions{
		Symbol:          "BTCUSDT",
		From:            candleTime,
		To:              candleTime,
		ReversalEnabled: false,
		OrdersEnabled:   false,
	})
	if err != nil {
		t.Fatalf("replay enriched csv: %v", err)
	}
	if result.Summary.OrderbookDiagnosticCount != 1 || result.Summary.OrderbookAvailableCount != 1 {
		t.Fatalf("replay did not treat enriched orderbook as available: %+v", result.Summary)
	}
	if result.Summary.OrdersSent != 0 {
		t.Fatalf("replay sent orders for enriched csv: %+v", result.Summary)
	}
}

func TestValidateOrderbookJSONLUsableForEnrichment(t *testing.T) {
	dir := t.TempDir()
	obPath := filepath.Join(dir, "orderbook.jsonl")
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	live := validOrderbookSnapshotEvent(start)
	live["source"] = "live_orderbook_snapshot"
	replayUnavailable := validOrderbookSnapshotEvent(start.Add(time.Second))
	replayUnavailable["source"] = "replay_orderbook_snapshot"
	replayUnavailable["orderbook_available"] = false
	replayUnavailable["reason"] = "orderbook_data_not_available"
	writeOrderbookEnrichmentJSONL(t, obPath, live, replayUnavailable)

	summary, err := validateOrderbookJSONL(obPath, "BTCUSDT")
	if err != nil {
		t.Fatalf("validate orderbook jsonl: %v", err)
	}
	if summary.TotalSnapshots != 2 || summary.ValidSnapshots != 1 || summary.UnavailableSnapshots != 1 {
		t.Fatalf("unexpected validation counts: %+v", summary)
	}
	if summary.LiveSnapshotCount != 1 || summary.ReplaySnapshotCount != 1 || summary.ValidCoveragePct != 50 {
		t.Fatalf("unexpected validation coverage/source counts: %+v", summary)
	}
	if summary.Recommendation != orderbookValidationUsableForEnrichment {
		t.Fatalf("recommendation got %s", summary.Recommendation)
	}
	if len(summary.Symbols) != 1 || summary.Symbols[0] != "BTCUSDT" {
		t.Fatalf("unexpected symbols: %+v", summary.Symbols)
	}
	if !summary.TimeMin.Equal(start) || !summary.TimeMax.Equal(start.Add(time.Second)) {
		t.Fatalf("unexpected time range: min=%s max=%s", summary.TimeMin, summary.TimeMax)
	}
}

func TestValidateOrderbookJSONLReplayUnavailableRecommendsDataGap(t *testing.T) {
	dir := t.TempDir()
	obPath := filepath.Join(dir, "orderbook.jsonl")
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	first := validOrderbookSnapshotEvent(start)
	first["source"] = "replay_orderbook_snapshot"
	first["orderbook_available"] = false
	first["reason"] = "orderbook_data_not_available"
	second := validOrderbookSnapshotEvent(start.Add(time.Second))
	second["source"] = "replay_orderbook_snapshot"
	second["orderbook_available"] = false
	second["reason"] = "orderbook_data_not_available"
	writeOrderbookEnrichmentJSONL(t, obPath, first, second)

	summary, err := validateOrderbookJSONL(obPath, "BTCUSDT")
	if err != nil {
		t.Fatalf("validate orderbook jsonl: %v", err)
	}
	if summary.Recommendation != orderbookValidationDataGapReplayUnavailable {
		t.Fatalf("recommendation got %s want %s", summary.Recommendation, orderbookValidationDataGapReplayUnavailable)
	}
	if summary.ValidSnapshots != 0 || summary.ReplaySnapshotCount != 2 || summary.UnavailableSnapshots != 2 {
		t.Fatalf("unexpected replay unavailable counts: %+v", summary)
	}
	if len(summary.TopUnavailableReasons) != 1 || summary.TopUnavailableReasons[0].Reason != "orderbook_data_not_available" || summary.TopUnavailableReasons[0].Count != 2 {
		t.Fatalf("unexpected unavailable reasons: %+v", summary.TopUnavailableReasons)
	}
}

func TestOrderbookEnrichmentWarnsForReplayUnavailableOnly(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "candles.csv")
	obPath := filepath.Join(dir, "orderbook.jsonl")
	outPath := filepath.Join(dir, "enriched.csv")
	candleTime := time.Date(2026, 5, 20, 13, 0, 1, 0, time.UTC)
	writeOrderbookEnrichmentCandlesCSV(t, csvPath, candleTime)
	event := validOrderbookSnapshotEvent(candleTime)
	event["source"] = "replay_orderbook_snapshot"
	event["orderbook_available"] = false
	event["reason"] = "orderbook_data_not_available"
	writeOrderbookEnrichmentJSONL(t, obPath, event)

	output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
		InputCSV:       csvPath,
		OrderbookJSONL: obPath,
		OutputCSV:      outPath,
		Symbol:         "BTCUSDT",
		MaxStaleness:   1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("enrich orderbook: %v", err)
	}
	if output.Summary.InputSnapshots.Recommendation != orderbookValidationDataGapReplayUnavailable {
		t.Fatalf("unexpected input snapshot summary: %+v", output.Summary.InputSnapshots)
	}
	if len(output.Summary.Warnings) != 1 || !strings.Contains(output.Summary.Warnings[0], "only replay_orderbook_snapshot") {
		t.Fatalf("missing replay-unavailable warning: %+v", output.Summary.Warnings)
	}
}

func TestSensitivityFlagsSeedReplaySetupParamsWhenListsAbsent(t *testing.T) {
	spec, err := parseSensitivityFlags("", "", "", "", "", "", "", "", "", "", "", "", "", 0, 0, 5, 0.2)
	if err != nil {
		t.Fatalf("parse sensitivity flags: %v", err)
	}
	if len(spec.SetupMemoryBarsList) != 1 || spec.SetupMemoryBarsList[0] != 5 {
		t.Fatalf("setup memory list was not seeded from replay flag: %+v", spec.SetupMemoryBarsList)
	}
	if len(spec.InvalidationATRMultList) != 1 || spec.InvalidationATRMultList[0] != 0.2 {
		t.Fatalf("invalidation ATR list was not seeded from replay flag: %+v", spec.InvalidationATRMultList)
	}

	explicit, err := parseSensitivityFlags("", "", "", "", "0,2", "0.1,0.3", "", "", "", "", "", "", "", 0, 0, 5, 0.2)
	if err != nil {
		t.Fatalf("parse explicit sensitivity flags: %v", err)
	}
	if len(explicit.SetupMemoryBarsList) != 2 || explicit.SetupMemoryBarsList[0] != 0 || explicit.SetupMemoryBarsList[1] != 2 {
		t.Fatalf("explicit setup memory list was not preserved: %+v", explicit.SetupMemoryBarsList)
	}
	if len(explicit.InvalidationATRMultList) != 2 || explicit.InvalidationATRMultList[0] != 0.1 || explicit.InvalidationATRMultList[1] != 0.3 {
		t.Fatalf("explicit invalidation ATR list was not preserved: %+v", explicit.InvalidationATRMultList)
	}

	withOutcome, err := parseSensitivityFlags("", "", "", "", "", "", "", "", "", "", "", "40,60", "20,30", 1.5, 0.5, 0, 0)
	if err != nil {
		t.Fatalf("parse outcome sensitivity flags: %v", err)
	}
	if len(withOutcome.OutcomeTPBpsList) != 2 || withOutcome.OutcomeTPBpsList[0] != 40 || withOutcome.OutcomeTPBpsList[1] != 60 {
		t.Fatalf("outcome TP list was not parsed: %+v", withOutcome.OutcomeTPBpsList)
	}
	if len(withOutcome.OutcomeSLBpsList) != 2 || withOutcome.OutcomeSLBpsList[0] != 20 || withOutcome.OutcomeSLBpsList[1] != 30 {
		t.Fatalf("outcome SL list was not parsed: %+v", withOutcome.OutcomeSLBpsList)
	}
	if withOutcome.OutcomeFeeBps != 1.5 || withOutcome.OutcomeSlippageBps != 0.5 {
		t.Fatalf("outcome costs were not preserved: %+v", withOutcome)
	}

	withFilters, err := parseSensitivityFlags("", "", "", "", "", "", "true,false", "1.2,1.5", "2,3", "true", "false,true", "", "", 0, 0, 0, 0)
	if err != nil {
		t.Fatalf("parse filter sensitivity flags: %v", err)
	}
	if len(withFilters.RequireOrderbookNotHostileList) != 2 || !withFilters.RequireOrderbookNotHostileList[0] || withFilters.RequireOrderbookNotHostileList[1] {
		t.Fatalf("require orderbook list was not parsed: %+v", withFilters.RequireOrderbookNotHostileList)
	}
	if len(withFilters.MinOrderbookRatioList) != 2 || withFilters.MinOrderbookRatioList[0] != 1.2 || withFilters.MinOrderbookRatioList[1] != 1.5 {
		t.Fatalf("min orderbook ratio list was not parsed: %+v", withFilters.MinOrderbookRatioList)
	}
	if len(withFilters.MinBarsWithoutNewLowAfterReclaimList) != 2 || withFilters.MinBarsWithoutNewLowAfterReclaimList[0] != 2 || withFilters.MinBarsWithoutNewLowAfterReclaimList[1] != 3 {
		t.Fatalf("min bars after reclaim list was not parsed: %+v", withFilters.MinBarsWithoutNewLowAfterReclaimList)
	}
	if len(withFilters.RequireCloseAboveMiddleBBList) != 1 || !withFilters.RequireCloseAboveMiddleBBList[0] {
		t.Fatalf("require close above middle BB list was not parsed: %+v", withFilters.RequireCloseAboveMiddleBBList)
	}
	if len(withFilters.RequireHigherLowAfterReclaimList) != 2 || withFilters.RequireHigherLowAfterReclaimList[0] || !withFilters.RequireHigherLowAfterReclaimList[1] {
		t.Fatalf("require higher low after reclaim list was not parsed: %+v", withFilters.RequireHigherLowAfterReclaimList)
	}
}

func TestDatasetManifestParsingFormats(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.csv")
	if err := os.WriteFile(inputPath, []byte("time,open,high,low,close,volume\n2026-05-20T13:00:00Z,1,1,1,1,1\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	jsonPath := filepath.Join(dir, "manifest.json")
	jsonRaw := `[{"symbol":"BTCUSDT","timeframe":"1m","input_csv":"input.csv","start":"2026-05-20T13:00:00Z","end":"2026-05-20T13:10:00Z","warmup":"5"}]`
	if err := os.WriteFile(jsonPath, []byte(jsonRaw), 0o644); err != nil {
		t.Fatalf("write json manifest: %v", err)
	}
	yamlPath := filepath.Join(dir, "manifest.yaml")
	yamlRaw := `episodes:
  - symbol: BTCUSDT
    timeframe: 1m
    input_csv: input.csv
    start: "2026-05-20T13:00:00Z"
    end: "2026-05-20T13:10:00Z"
    warmup: 5m
`
	if err := os.WriteFile(yamlPath, []byte(yamlRaw), 0o644); err != nil {
		t.Fatalf("write yaml manifest: %v", err)
	}
	csvPath := filepath.Join(dir, "manifest.csv")
	csvRaw := "symbol,timeframe,input_csv,start,end,warmup\nBTCUSDT,1m,input.csv,2026-05-20T13:00:00Z,2026-05-20T13:10:00Z,5\n"
	if err := os.WriteFile(csvPath, []byte(csvRaw), 0o644); err != nil {
		t.Fatalf("write csv manifest: %v", err)
	}

	for _, path := range []string{jsonPath, yamlPath, csvPath} {
		episodes, err := readDatasetManifest(path)
		if err != nil {
			t.Fatalf("read manifest %s: %v", filepath.Base(path), err)
		}
		if len(episodes) != 1 {
			t.Fatalf("manifest %s episodes got %d want 1", filepath.Base(path), len(episodes))
		}
		if episodes[0].Symbol != "BTCUSDT" || episodes[0].Timeframe != "1m" || episodes[0].InputCSV != inputPath {
			t.Fatalf("manifest %s parsed incorrectly: %+v", filepath.Base(path), episodes[0])
		}
		if episodes[0].Warmup == "" {
			t.Fatalf("manifest %s did not parse warmup: %+v", filepath.Base(path), episodes[0])
		}
	}
}

func TestDatasetManifestWarmupFields(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "warmup",
			raw:  `[{"symbol":"BTCUSDT","timeframe":"1m","input_csv":"input.csv","start":"2026-05-20T13:00:00Z","end":"2026-05-20T13:10:00Z","warmup":5}]`,
			want: "5",
		},
		{
			name: "warmup_candles",
			raw:  `[{"symbol":"BTCUSDT","timeframe":"1m","input_csv":"input.csv","start":"2026-05-20T13:00:00Z","end":"2026-05-20T13:10:00Z","warmup_candles":219}]`,
			want: "219",
		},
		{
			name: "both_prefers_warmup",
			raw:  `[{"symbol":"BTCUSDT","timeframe":"1m","input_csv":"input.csv","start":"2026-05-20T13:00:00Z","end":"2026-05-20T13:10:00Z","warmup":7,"warmup_candles":219}]`,
			want: "7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name+".json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o644); err != nil {
				t.Fatalf("write manifest: %v", err)
			}
			episodes, err := readDatasetManifest(path)
			if err != nil {
				t.Fatalf("read manifest: %v", err)
			}
			if len(episodes) != 1 {
				t.Fatalf("episodes got %d want 1", len(episodes))
			}
			if episodes[0].Warmup != tc.want {
				t.Fatalf("warmup got %q want %q", episodes[0].Warmup, tc.want)
			}
		})
	}
}

func TestEpisodeMinerDetectsLowerBBReclaimZone(t *testing.T) {
	reason := classifyMiningReason(minedEpisodeFeatures{
		RSIMin:                 42,
		BBTouchCount:           1,
		ReclaimSeen:            true,
		ForwardReturnBps:       2,
		ForwardMaxAdverseBps:   -5,
		ForwardMaxFavorableBps: 8,
	})
	if reason != miningReasonLowerBBReclaimZone {
		t.Fatalf("reason got %s want %s", reason, miningReasonLowerBBReclaimZone)
	}
}

func TestEpisodeMinerDetectsRSIOversoldZone(t *testing.T) {
	reason := classifyMiningReason(minedEpisodeFeatures{
		RSIMin:                 24,
		ForwardReturnBps:       -2,
		ForwardMaxAdverseBps:   -5,
		ForwardMaxFavorableBps: 4,
	})
	if reason != miningReasonRSIOversoldZone {
		t.Fatalf("reason got %s want %s", reason, miningReasonRSIOversoldZone)
	}
}

func TestEpisodeMinerDetectsFalseReversalZone(t *testing.T) {
	reason := classifyMiningReason(minedEpisodeFeatures{
		RSIMin:                 24,
		BBTouchCount:           2,
		ReclaimSeen:            true,
		SubsequentLowerLow:     true,
		ForwardReturnBps:       -40,
		ForwardMaxAdverseBps:   -75,
		ForwardMaxFavorableBps: 12,
	})
	if reason != miningReasonFalseReversalZone {
		t.Fatalf("reason got %s want %s", reason, miningReasonFalseReversalZone)
	}
}

func TestEpisodeMinerDetectsTrueReversalLikeZone(t *testing.T) {
	reason := classifyMiningReason(minedEpisodeFeatures{
		RSIMin:                 26,
		BBTouchCount:           1,
		ReclaimSeen:            true,
		ForwardReturnBps:       45,
		ForwardMaxAdverseBps:   -8,
		ForwardMaxFavorableBps: 90,
	})
	if reason != miningReasonTrueReversalLikeZone {
		t.Fatalf("reason got %s want %s", reason, miningReasonTrueReversalLikeZone)
	}
}

func TestEpisodeMinerClassifiesWeakReboundSeparately(t *testing.T) {
	reason := classifyMiningReason(minedEpisodeFeatures{
		RSIMin:                 36.45,
		BBTouchCount:           0,
		ReclaimSeen:            false,
		ForwardReturnBps:       35,
		ForwardMaxAdverseBps:   -4,
		ForwardMaxFavorableBps: 60,
	})
	if reason != miningReasonWeakReboundZone {
		t.Fatalf("reason got %s want %s", reason, miningReasonWeakReboundZone)
	}
}

func TestEpisodeMinerDeduplicatesNearbyAnchors(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	candidates := []miningCandidate{
		{AnchorIdx: 10, AnchorTime: start, Reason: miningReasonRSIOversoldZone, Score: 5},
		{AnchorIdx: 12, AnchorTime: start.Add(2 * time.Minute), Reason: miningReasonFalseReversalZone, Score: 50},
		{AnchorIdx: 25, AnchorTime: start.Add(15 * time.Minute), Reason: miningReasonNoSetupControlWindow, Score: 1},
	}
	deduped, count := deduplicateMiningCandidates(candidates, 5)
	if count != 1 || len(deduped) != 2 {
		t.Fatalf("dedupe count=%d candidates=%+v", count, deduped)
	}
	if deduped[0].AnchorIdx != 12 {
		t.Fatalf("nearby weaker anchor was kept: %+v", deduped)
	}
}

func TestEpisodeMinerSkipsInsufficientWarmupByDefault(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "mine.csv")
	writeEpisodeMiningTestCSV(t, inputPath)
	manifestPath := filepath.Join(dir, "mined_manifest.json")

	output, err := runEpisodeMining(episodeMiningOptions{
		InputCSV:        inputPath,
		OutManifest:     manifestPath,
		Symbol:          "BTCUSDT",
		Timeframe:       "1m",
		WindowBefore:    "10",
		WindowAfter:     "5",
		MinSpacingBars:  5,
		WarmupCandles:   200,
		IncludeControls: true,
	})
	if err != nil {
		t.Fatalf("mine episodes: %v", err)
	}
	if output.Summary.EpisodesMined != 0 {
		t.Fatalf("expected insufficient-warmup candidates to be skipped: %+v", output.Summary)
	}
	if output.Summary.WarmupInsufficientCount == 0 || output.Summary.SkippedInsufficientWarmupCount == 0 {
		t.Fatalf("expected warmup skip diagnostics: %+v", output.Summary)
	}
	raw, err := os.ReadFile(output.SummaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if !strings.Contains(string(raw), "skipped_insufficient_warmup_count") {
		t.Fatalf("summary missing skipped_insufficient_warmup_count:\n%s", string(raw))
	}
}

func TestEpisodeMinerAllowsInsufficientWarmupWhenRequested(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "mine.csv")
	writeEpisodeMiningTestCSV(t, inputPath)
	manifestPath := filepath.Join(dir, "mined_manifest.json")

	output, err := runEpisodeMining(episodeMiningOptions{
		InputCSV:                inputPath,
		OutManifest:             manifestPath,
		Symbol:                  "BTCUSDT",
		Timeframe:               "1m",
		WindowBefore:            "10",
		WindowAfter:             "5",
		MinSpacingBars:          5,
		WarmupCandles:           200,
		AllowInsufficientWarmup: true,
		IncludeControls:         true,
	})
	if err != nil {
		t.Fatalf("mine episodes: %v", err)
	}
	if output.Summary.EpisodesMined == 0 || output.Summary.SkippedInsufficientWarmupCount != 0 {
		t.Fatalf("expected insufficient warmup candidates to be included: %+v", output.Summary)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var mined []minedManifestEpisode
	if err := json.Unmarshal(raw, &mined); err != nil {
		t.Fatalf("unmarshal mined manifest: %v", err)
	}
	if len(mined) == 0 || !mined[0].MinedFeatures.WarmupInsufficient || mined[0].MinedFeatures.WarmupAvailable >= mined[0].MinedFeatures.WarmupRequired {
		t.Fatalf("expected warmup insufficiency in mined features: %+v", mined)
	}
}

func TestEpisodeMinerBalancedControlsIncludedWhenRequested(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "controls.csv")
	writeNoSetupMiningCSV(t, inputPath)
	output, err := runEpisodeMining(episodeMiningOptions{
		InputCSV:          inputPath,
		OutManifest:       filepath.Join(dir, "mined_manifest.json"),
		Symbol:            "BTCUSDT",
		Timeframe:         "1m",
		WindowBefore:      "5",
		WindowAfter:       "2",
		MinSpacingBars:    1,
		WarmupCandles:     5,
		IncludeControls:   true,
		MinControlWindows: 3,
		MaxPerClass:       2,
	})
	if err != nil {
		t.Fatalf("mine controls: %v", err)
	}
	if output.Summary.NoSetupControlCount < 3 {
		t.Fatalf("expected requested controls, got %+v", output.Summary)
	}
	if output.Summary.ClassDistributionBeforeDedup[miningReasonNoSetupControlWindow] == 0 ||
		output.Summary.ClassDistributionAfterDedup[miningReasonNoSetupControlWindow] == 0 {
		t.Fatalf("expected control distributions before/after dedupe: %+v", output.Summary)
	}
}

func TestEpisodeMinerWritesValidDatasetManifest(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "mine.csv")
	writeEpisodeMiningTestCSV(t, inputPath)
	manifestPath := filepath.Join(dir, "mined_manifest.json")

	output, err := runEpisodeMining(episodeMiningOptions{
		InputCSV:        inputPath,
		OutManifest:     manifestPath,
		Symbol:          "BTCUSDT",
		Timeframe:       "1m",
		WindowBefore:    "10",
		WindowAfter:     "5",
		MinSpacingBars:  5,
		WarmupCandles:   30,
		IncludeControls: true,
	})
	if err != nil {
		t.Fatalf("mine episodes: %v", err)
	}
	if output.Summary.EpisodesMined == 0 {
		t.Fatalf("expected mined episodes: %+v", output.Summary)
	}
	episodes, err := readDatasetManifest(manifestPath)
	if err != nil {
		t.Fatalf("mined manifest should be readable by dataset runner: %v", err)
	}
	if len(episodes) != output.Summary.EpisodesMined {
		t.Fatalf("manifest episodes=%d summary=%d", len(episodes), output.Summary.EpisodesMined)
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var mined []minedManifestEpisode
	if err := json.Unmarshal(raw, &mined); err != nil {
		t.Fatalf("unmarshal mined manifest: %v", err)
	}
	if mined[0].ID == "" || mined[0].MinedReason == "" || mined[0].MinedAnchorTime == "" || mined[0].Warmup == 0 {
		t.Fatalf("mined metadata missing: %+v", mined[0])
	}
	if mined[0].MinedFeatures.WarmupRequired == 0 || mined[0].MinedFeatures.WarmupAvailable == 0 {
		t.Fatalf("mined warmup metadata missing: %+v", mined[0])
	}
	if output.Summary.CSVStartTime == "" || output.Summary.CSVEndTime == "" || output.Summary.EarliestValidAnchorTime == "" {
		t.Fatalf("summary missing CSV/warmup diagnostics: %+v", output.Summary)
	}
	for _, path := range []string{output.SummaryPath, output.ReportPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing mining artifact %s: %v", path, err)
		}
	}
}

func TestRunDatasetAfterMineDoesNotEnableOrders(t *testing.T) {
	before := config.DefaultReversalLongConfig()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "mine.csv")
	writeEpisodeMiningTestCSV(t, inputPath)
	runID := "mine-run"
	mining, output, err := runEpisodeMiningAndDataset(episodeMiningOptions{
		InputCSV:        inputPath,
		OutManifest:     filepath.Join(dir, "dataset", runID, "mined_manifest.json"),
		Symbol:          "BTCUSDT",
		Timeframe:       "1m",
		WindowBefore:    "10",
		WindowAfter:     "5",
		MinSpacingBars:  5,
		WarmupCandles:   30,
		IncludeControls: true,
		DatasetRunID:    runID,
		DatasetOutRoot:  filepath.Join(dir, "dataset"),
		RunDatasetAfter: true,
		DatasetRunOptions: datasetRunnerOptions{
			RunID:                runID,
			OutRoot:              filepath.Join(dir, "dataset"),
			ReversalEnabled:      true,
			ReversalTradeEnabled: false,
			MinEpisodes:          1,
			MinOrderbookCoverage: 0.80,
			MinCandidateEpisodes: 1,
			TrainRatio:           0.70,
		},
	})
	if err != nil {
		t.Fatalf("mine and run dataset: %v", err)
	}
	if mining.Summary.DatasetOrdersSent != 0 || output.Summary.OrdersSent != 0 {
		t.Fatalf("run-dataset-after-mine sent orders: mining=%+v dataset=%+v", mining.Summary, output.Summary)
	}
	if !output.Summary.SafetyConstraintsPassed {
		t.Fatalf("dataset safety failed unexpectedly: %+v", output.Summary)
	}
	if after := config.DefaultReversalLongConfig(); after != before {
		t.Fatalf("run-dataset-after-mine changed production defaults: before=%+v after=%+v", before, after)
	}
}

func TestDatasetRunnerCreatesPerEpisodeOutputs(t *testing.T) {
	before := config.DefaultReversalLongConfig()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "candles.csv")
	writeDatasetRunnerTestCSV(t, inputPath)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := []map[string]string{{
		"symbol":    "BTCUSDT",
		"timeframe": "1m",
		"input_csv": "candles.csv",
		"start":     "2026-05-20T13:20:00Z",
		"end":       "2026-05-20T13:25:00Z",
		"warmup":    "10",
	}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	output, err := runDatasetRunner(datasetRunnerOptions{
		ManifestPath:         manifestPath,
		RunID:                "unit-test",
		OutRoot:              filepath.Join(dir, "dataset"),
		ReversalEnabled:      true,
		ReversalTradeEnabled: false,
		SensitivitySpec: strategy.ReversalSensitivityOptions{
			MinRSIDeltaList:         []float64{5},
			NoNewLowBarsList:        []int{3},
			ScoreThresholdList:      []float64{0.8},
			MinVolumeRatioList:      []float64{2},
			SetupMemoryBarsList:     []int{0},
			InvalidationATRMultList: []float64{0},
		},
		MinEpisodes:          1,
		MinOrderbookCoverage: 0.80,
		MinCandidateEpisodes: 1,
		TrainRatio:           0.70,
	})
	if err != nil {
		t.Fatalf("run dataset: %v", err)
	}
	if output.Summary.TotalEpisodes == 0 {
		t.Fatalf("expected extracted episode output: %+v", output.Summary)
	}
	for _, pattern := range []string{
		filepath.Join(output.RunDir, "episodes", "*.json"),
		filepath.Join(output.RunDir, "blockers", "*.md"),
		filepath.Join(output.RunDir, "sensitivity", "*.jsonl"),
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(matches) == 0 {
			t.Fatalf("expected output matching %s", pattern)
		}
	}
	for _, path := range []string{output.SummaryPath, output.ReportPath, output.AcceptancePath, output.EpisodesIndexPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing dataset artifact %s: %v", path, err)
		}
	}
	rawIndex, err := os.ReadFile(output.EpisodesIndexPath)
	if err != nil {
		t.Fatalf("read episodes index: %v", err)
	}
	indexLines := strings.Split(strings.TrimSpace(string(rawIndex)), "\n")
	if len(indexLines) == 0 || strings.TrimSpace(indexLines[0]) == "" {
		t.Fatalf("expected episodes index rows")
	}
	var indexRow datasetEpisodeIndexRow
	if err := json.Unmarshal([]byte(indexLines[0]), &indexRow); err != nil {
		t.Fatalf("unmarshal episodes index row: %v", err)
	}
	if indexRow.SourceFile == "" || indexRow.Event == "" || indexRow.Symbol != "BTCUSDT" || indexRow.Timeframe != "1m" {
		t.Fatalf("episodes index row missing expected metadata: %+v", indexRow)
	}
	episodeMatches, err := filepath.Glob(filepath.Join(output.RunDir, "episodes", "*.json"))
	if err != nil {
		t.Fatalf("glob episode outputs: %v", err)
	}
	if len(episodeMatches) == 0 {
		t.Fatalf("expected episode output")
	}
	rawEpisode, err := os.ReadFile(episodeMatches[0])
	if err != nil {
		t.Fatalf("read episode output: %v", err)
	}
	var episodes []strategy.ReversalEpisodeDataset
	if err := json.Unmarshal(rawEpisode, &episodes); err != nil {
		t.Fatalf("unmarshal episode output: %v", err)
	}
	if len(episodes) == 0 || episodes[0].Summary.WarmupCandles == 0 {
		t.Fatalf("expected non-zero warmup_candles in episode summary: %+v", episodes)
	}
	checkedReplayEvents := 0
	for _, episode := range episodes {
		for i, event := range episode.Events {
			if boolEventValue(event["orders_enabled"]) {
				t.Fatalf("dataset runner must force orders_enabled=false, episode event %d: %+v", i, event)
			}
			if boolEventValue(event["order_sent"]) {
				t.Fatalf("dataset runner must not send orders, episode event %d: %+v", i, event)
			}
			checkedReplayEvents++
		}
	}
	if checkedReplayEvents == 0 {
		t.Fatalf("expected replay events to verify order safety")
	}
	if output.Summary.OrdersSent != 0 || !output.Summary.SafetyConstraintsPassed {
		t.Fatalf("dataset runner safety changed: %+v", output.Summary)
	}
	if after := config.DefaultReversalLongConfig(); after != before {
		t.Fatalf("dataset runner changed production defaults: before=%+v after=%+v", before, after)
	}
}

func TestDatasetRunnerZeroWarmupDiagnosesUnreadyIndicators(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "candles.csv")
	writeDatasetRunnerTestCSV(t, inputPath)
	manifestPath := filepath.Join(dir, "manifest.json")
	manifest := []map[string]string{{
		"symbol":    "BTCUSDT",
		"timeframe": "1m",
		"input_csv": "candles.csv",
		"start":     "2026-05-20T13:20:00Z",
		"end":       "2026-05-20T13:25:00Z",
		"warmup":    "0",
	}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	_, err = runDatasetRunner(datasetRunnerOptions{
		ManifestPath:         manifestPath,
		RunID:                "zero-warmup",
		OutRoot:              filepath.Join(dir, "dataset"),
		ReversalEnabled:      true,
		ReversalTradeEnabled: false,
		MinEpisodes:          1,
		MinOrderbookCoverage: 0.80,
		MinCandidateEpisodes: 1,
		TrainRatio:           0.70,
	})
	if err == nil {
		t.Fatalf("expected zero warmup diagnostic")
	}
	msg := err.Error()
	for _, want := range []string{"warmup is 0", "indicators are not ready", "bb_position=not_available"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("diagnostic %q missing %q", msg, want)
		}
	}
}

func TestDatasetSummaryAggregatesClassifications(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	records := []datasetEpisodeRecord{
		datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.9, "candidate_ready", ""),
		datasetRecord("BTCUSDT", "1m", start.Add(time.Minute), strategy.EpisodeFalseReversalInvalidated, 0, 1, 1, 50, 0.55, "setup_invalidated", "substantial_new_low"),
		datasetRecord("ETHUSDT", "5m", start.Add(2*time.Minute), strategy.EpisodeNoSetup, 0, 0, 0, 100, 0.2, "no_capitulation", ""),
		datasetRecord("ETHUSDT", "5m", start.Add(3*time.Minute), strategy.EpisodeOrderbookDataGap, 0, 0, 0, 0, 0.1, "no_capitulation", ""),
	}
	for i := range records {
		records[i].ManifestIndex = i
	}
	assignDatasetSplits(records, nil, datasetRunnerOptions{TrainRatio: 0.5})
	summary := buildDatasetSummary("unit", records, nil, 0, nil, datasetRunnerOptions{TrainRatio: 0.5})
	if summary.TotalEpisodes != 4 ||
		summary.TrueReversalCandidateCount != 1 ||
		summary.FalseReversalInvalidatedCount != 1 ||
		summary.NoSetupCount != 1 ||
		summary.DataGapOrderbookUnavailableCount != 1 {
		t.Fatalf("unexpected classification counts: %+v", summary)
	}
	if summary.CandidateReadyEpisodes != 1 || summary.SetupDetectedEpisodes != 2 || summary.SetupInvalidatedEpisodes != 1 {
		t.Fatalf("unexpected setup/candidate counts: %+v", summary)
	}
	if summary.AvgOrderbookCoveragePct != 62.5 {
		t.Fatalf("coverage got %.2f want 62.50", summary.AvgOrderbookCoveragePct)
	}
	if summary.TopInvalidationReasons[0].Value != "substantial_new_low" {
		t.Fatalf("unexpected invalidation reasons: %+v", summary.TopInvalidationReasons)
	}
}

func TestDatasetSummarySeparatesCandidateReadyEventCounts(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	record := datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.8, "candidate_ready", "")
	record.EpisodePath = "/tmp/episode.json"
	record.Dataset.Events = []map[string]any{
		{
			"event":           "reversal_long_evaluation",
			"time":            start,
			"candidate_ready": true,
		},
		{
			"event":           "reversal_long_pipeline_decision",
			"time":            start,
			"candidate_ready": true,
		},
		{
			"event":           "reversal_long_evaluation",
			"time":            start.Add(time.Minute),
			"candidate_ready": false,
		},
	}
	records := []datasetEpisodeRecord{record}
	assignDatasetSplits(records, nil, datasetRunnerOptions{TrainRatio: 0.7})
	summary := buildDatasetSummary("unit", records, nil, 0, nil, datasetRunnerOptions{TrainRatio: 0.7})
	if summary.CandidateReadyEpisodes != 1 || summary.CandidateReadyExtractedEpisodes != 1 {
		t.Fatalf("unexpected extracted candidate counts: %+v", summary)
	}
	if summary.CandidateReadyEvaluationEvents != 1 {
		t.Fatalf("candidate_ready_evaluation_events got %d want 1", summary.CandidateReadyEvaluationEvents)
	}
	if summary.CandidateReadyPipelineDecisions != 1 {
		t.Fatalf("candidate_ready_pipeline_decisions got %d want 1", summary.CandidateReadyPipelineDecisions)
	}
	if summary.CandidateReadyUniqueEventTimes != 1 {
		t.Fatalf("candidate_ready_unique_event_times got %d want 1", summary.CandidateReadyUniqueEventTimes)
	}
}

func TestDatasetEpisodesIndexIncludesEvaluationDiagnostics(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	setupDetectedAt := start.Add(-2 * time.Minute)
	record := datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeFalseReversalInvalidated, 0, 1, 1, 100, 0.85, "setup_invalidated", "substantial_new_low")
	record.EpisodePath = filepath.Join(t.TempDir(), "episode.json")
	record.Dataset.Timeline = []strategy.ReversalReplayTimelineRow{
		{
			Time:               start,
			State:              "momentum_turn",
			RejectReason:       "new_low_detected",
			SetupDetectedAt:    &setupDetectedAt,
			SetupAgeBars:       2,
			SetupInvalidated:   true,
			InvalidationReason: "substantial_new_low",
			MemoryReclaimUsed:  true,
			OrderbookRatio:     1.7,
			OrderbookImbalance: -0.12,
			SpreadBps:          0.42,
			OrderbookHostile:   true,
			OrderbookAvailable: true,
			HTFBias:            "SHORT",
			HTFAllowed:         true,
			HTFOverrideAllowed: true,
			Score:              0.85,
			CandidateReady:     false,
			ScoreBBReclaim:     0.20,
			ScoreMACDRecovery:  0.15,
			ScoreNoNewLow:      0.15,
			ScoreRSIOversold:   0.20,
			ScoreRSIRecovery:   0,
			ScoreVolumeSpike:   0.15,
			RSI:                24.5,
			RSIDelta:           4.2,
			BarsWithoutNewLow:  1,
			NoNewLowBars:       3,
			VolumeRatio:        2.4,
		},
	}
	record.Dataset.Events = []map[string]any{
		{
			"event":                        "reversal_long_evaluation",
			"time":                         start,
			"candidate_ready":              false,
			"state":                        "momentum_turn",
			"reject_reason":                "new_low_detected",
			"score":                        0.85,
			"score_bb_reclaim":             0.20,
			"score_macd_recovery":          0.15,
			"score_no_new_low":             0.15,
			"score_rsi_oversold":           0.20,
			"score_rsi_recovery":           0.0,
			"score_volume_spike":           0.15,
			"rsi":                          24.5,
			"rsi_delta":                    4.2,
			"rsi_delta_available":          true,
			"bars_without_new_low":         1,
			"no_new_low_bars":              3,
			"volume_ratio":                 2.4,
			"min_volume_ratio":             2.0,
			"volume_ratio_available":       true,
			"volume_spike_passed":          true,
			"orderbook_available":          true,
			"best_bid":                     100.1,
			"best_ask":                     100.2,
			"best_bid_size":                3.5,
			"best_ask_size":                2.5,
			"orderbook_ratio":              1.7,
			"orderbook_imbalance":          -0.12,
			"spread_bps":                   0.42,
			"orderbook_hostile":            true,
			"orderbook_reject_reason":      "bid_unstable",
			"orderbook_unavailable_reason": "",
			"htf_bias":                     "SHORT",
			"htf_allowed":                  true,
			"htf_override_allowed":         true,
			"order_sent":                   false,
		},
		{
			"event":                "reversal_long_pipeline_decision",
			"time":                 start,
			"candidate_ready":      false,
			"score":                0.85,
			"pipeline_allowed":     false,
			"pipeline_skip_reason": "new_low_detected",
			"order_sent":           false,
		},
	}
	path := filepath.Join(t.TempDir(), "episodes_index.jsonl")
	if err := writeDatasetEpisodesIndex(path, []datasetEpisodeRecord{record}); err != nil {
		t.Fatalf("write episodes index: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read episodes index: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("index rows got %d want 2:\n%s", len(lines), string(raw))
	}
	var evalRow datasetEpisodeIndexRow
	if err := json.Unmarshal([]byte(lines[0]), &evalRow); err != nil {
		t.Fatalf("unmarshal evaluation index row: %v", err)
	}
	if evalRow.SourceFile != record.EpisodePath || evalRow.RecordIndex != 0 || evalRow.Event != "reversal_long_evaluation" {
		t.Fatalf("unexpected evaluation index row identity: %+v", evalRow)
	}
	if evalRow.State != "momentum_turn" || evalRow.RejectReason != "new_low_detected" || !evalRow.SetupDetected || !evalRow.SetupInvalidated || evalRow.InvalidationReason != "substantial_new_low" {
		t.Fatalf("setup/reject diagnostics missing: %+v", evalRow)
	}
	if evalRow.SetupDetectedAt != setupDetectedAt.Format(time.RFC3339Nano) || evalRow.SetupAgeBars != 2 || !evalRow.MemoryReclaimUsed {
		t.Fatalf("setup memory diagnostics missing: %+v", evalRow)
	}
	if evalRow.RSI != 24.5 || evalRow.RSIDelta != 4.2 || !evalRow.RSIDeltaAvailable || evalRow.BarsWithoutNewLow != 1 || evalRow.NoNewLowBars != 3 {
		t.Fatalf("momentum diagnostics missing: %+v", evalRow)
	}
	if evalRow.VolumeRatio != 2.4 || evalRow.MinVolumeRatio != 2.0 || !evalRow.VolumeRatioAvailable || !evalRow.VolumeSpikePassed || evalRow.ScoreVolumeSpike != 0.15 {
		t.Fatalf("unexpected volume diagnostics: %+v", evalRow)
	}
	if evalRow.Score != 0.85 || evalRow.ScoreBBReclaim != 0.20 || evalRow.ScoreMACDRecovery != 0.15 || evalRow.ScoreNoNewLow != 0.15 || evalRow.ScoreRSIOversold != 0.20 || evalRow.ScoreRSIRecovery != 0 {
		t.Fatalf("score components missing: %+v", evalRow)
	}
	if evalRow.BestBid != 100.1 || evalRow.BestAsk != 100.2 || evalRow.BestBidSize != 3.5 || evalRow.BestAskSize != 2.5 ||
		evalRow.OrderbookRatio != 1.7 || evalRow.BidAskImbalance != -0.12 || evalRow.SpreadBps != 0.42 || !evalRow.OrderbookHostile || evalRow.OrderbookRejectReason != "bid_unstable" {
		t.Fatalf("unexpected orderbook diagnostic: %+v", evalRow)
	}
	if evalRow.HTFBias != "SHORT" || !evalRow.HTFAllowed || !evalRow.HTFOverrideAllowed {
		t.Fatalf("htf diagnostics missing: %+v", evalRow)
	}
	var pipelineRow datasetEpisodeIndexRow
	if err := json.Unmarshal([]byte(lines[1]), &pipelineRow); err != nil {
		t.Fatalf("unmarshal pipeline index row: %v", err)
	}
	if pipelineRow.Event != "reversal_long_pipeline_decision" || pipelineRow.PipelineAllowed || pipelineRow.PipelineSkipReason != "new_low_detected" {
		t.Fatalf("unexpected pipeline index row: %+v", pipelineRow)
	}
}

func TestDatasetReportIncludesHighScoreRejectedEvaluations(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	setupDetectedAt := start.Add(-time.Minute)
	record := datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeFalseReversalInvalidated, 0, 1, 1, 100, 0.85, "setup_invalidated", "substantial_new_low")
	record.EpisodePath = filepath.Join(t.TempDir(), "episode.json")
	record.Dataset.Timeline = []strategy.ReversalReplayTimelineRow{{
		Time:               start,
		State:              "momentum_turn",
		RejectReason:       "new_low_detected",
		SetupDetectedAt:    &setupDetectedAt,
		SetupAgeBars:       1,
		SetupInvalidated:   true,
		InvalidationReason: "substantial_new_low",
		OrderbookRatio:     1.3,
		SpreadBps:          0.25,
	}}
	record.Dataset.Events = []map[string]any{{
		"event":                   "reversal_long_evaluation",
		"time":                    start,
		"candidate_ready":         false,
		"state":                   "momentum_turn",
		"reject_reason":           "new_low_detected",
		"score":                   0.85,
		"orderbook_available":     true,
		"orderbook_ratio":         1.3,
		"spread_bps":              0.25,
		"orderbook_reject_reason": "",
		"order_sent":              false,
	}}
	summary := buildDatasetSummary("unit", []datasetEpisodeRecord{record}, nil, 0, nil, datasetRunnerOptions{TrainRatio: 0.7}, 0.8)
	if summary.HighScoreRejectedCount != 1 || len(summary.HighScoreRejectedGroups) != 1 || len(summary.HighScoreRejectedExamples) != 1 {
		t.Fatalf("unexpected high-score rejected summary: %+v", summary)
	}
	if summary.HighScoreRejectedGroups[0].RejectReason != "new_low_detected" || summary.HighScoreRejectedGroups[0].InvalidationReason != "substantial_new_low" {
		t.Fatalf("unexpected high-score rejected grouping: %+v", summary.HighScoreRejectedGroups)
	}
	path := filepath.Join(t.TempDir(), "dataset_report.md")
	if err := writeDatasetReport(path, summary, datasetAcceptanceResult{Recommendation: acceptanceDoNotCalibrate}); err != nil {
		t.Fatalf("write report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(raw)
	for _, want := range []string{
		"## High-Score Rejected Evaluations",
		"score >= threshold and candidate_ready=false: `1`",
		"| new_low_detected | substantial_new_low | 1 |",
		"| " + record.EpisodePath,
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestDatasetAcceptanceLowOrderbookCoverageDoNotCalibrate(t *testing.T) {
	summary := datasetSummary{
		TotalEpisodes:           50,
		AvgOrderbookCoveragePct: 10,
		CandidateReadyEpisodes:  10,
		SafetyConstraintsPassed: true,
	}
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceDoNotCalibrate || acceptance.OrderbookCoveragePass {
		t.Fatalf("unexpected acceptance for low coverage: %+v", acceptance)
	}
	var out strings.Builder
	path := filepath.Join(t.TempDir(), "acceptance_report.md")
	if err := writeDatasetAcceptanceReport(path, acceptance); err != nil {
		t.Fatalf("write acceptance report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read acceptance report: %v", err)
	}
	out.Write(raw)
	if !strings.Contains(out.String(), "DO_NOT_CALIBRATE") || !strings.Contains(out.String(), "orderbook coverage: FAIL") {
		t.Fatalf("acceptance report missing low coverage failure:\n%s", out.String())
	}
}

func TestDatasetAcceptanceNeedsMoreDataWhenEpisodeThresholdFails(t *testing.T) {
	summary := datasetSummary{
		TotalEpisodes:           3,
		AvgOrderbookCoveragePct: 100,
		CandidateReadyEpisodes:  10,
		SafetyConstraintsPassed: true,
	}
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceNeedMoreData || acceptance.MinEpisodesPass {
		t.Fatalf("unexpected acceptance for low episode count: %+v", acceptance)
	}
}

func TestDatasetTrainValidationSplitWorks(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	records := []datasetEpisodeRecord{
		datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeNoSetup, 0, 1, 0, 100, 0.1, "", ""),
		datasetRecord("BTCUSDT", "1m", start.Add(time.Minute), strategy.EpisodeFalseReversalInvalidated, 0, 1, 1, 100, 0.2, "setup_invalidated", "substantial_new_low"),
		datasetRecord("BTCUSDT", "1m", start.Add(2*time.Minute), strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.3, "candidate_ready", ""),
		datasetRecord("BTCUSDT", "1m", start.Add(3*time.Minute), strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.4, "candidate_ready", ""),
	}
	for i := range records {
		records[i].ManifestIndex = 0
	}
	runs := []datasetSensitivityRun{
		sensitivityRunForRecord(records, 0, 0, 1, 0, 0.1),
		sensitivityRunForRecord(records, 1, 0, 1, 1, 0.2),
		sensitivityRunForRecord(records, 2, 1, 1, 0, 0.3),
		sensitivityRunForRecord(records, 3, 1, 1, 0, 0.4),
	}
	assignDatasetSplits(records, runs, datasetRunnerOptions{TrainRatio: 0.5})
	summary := buildDatasetSummary("unit", records, runs, 0, nil, datasetRunnerOptions{TrainRatio: 0.5})
	if summary.Split.TrainEpisodes != 2 || summary.Split.ValidationEpisodes != 2 {
		t.Fatalf("unexpected split summary: %+v", summary.Split)
	}
	if summary.AggregationUnit != datasetAggregationUnitExtractedEpisode || summary.ExtractedEpisodesTotal != 4 || summary.SensitivityEpisodesTotal != 4 {
		t.Fatalf("unexpected aggregation diagnostics: %+v", summary)
	}
	if len(summary.AggregationWarnings) != 0 {
		t.Fatalf("unexpected aggregation warnings: %+v", summary.AggregationWarnings)
	}
	if len(summary.SensitivityAggregation[datasetSplitAll]) != 1 || summary.SensitivityAggregation[datasetSplitAll][0].TotalEpisodes != summary.TotalEpisodes {
		t.Fatalf("unexpected all sensitivity aggregation: %+v", summary.SensitivityAggregation[datasetSplitAll])
	}
	if len(summary.SensitivityAggregation[datasetSplitTrain]) != 1 || summary.SensitivityAggregation[datasetSplitTrain][0].TotalEpisodes != summary.Split.TrainEpisodes {
		t.Fatalf("unexpected train sensitivity aggregation: %+v", summary.SensitivityAggregation[datasetSplitTrain])
	}
	if len(summary.SensitivityAggregation[datasetSplitValidation]) != 1 || summary.SensitivityAggregation[datasetSplitValidation][0].TotalEpisodes != summary.Split.ValidationEpisodes {
		t.Fatalf("unexpected validation sensitivity aggregation: %+v", summary.SensitivityAggregation[datasetSplitValidation])
	}
	all := summary.SensitivityAggregation[datasetSplitAll][0]
	if all.CandidateReadyEpisodes != summary.CandidateReadyEpisodes ||
		all.SetupDetectedEpisodes != summary.SetupDetectedEpisodes ||
		all.SetupInvalidatedEpisodes != summary.SetupInvalidatedEpisodes {
		t.Fatalf("summary and aggregation counts diverged: summary=%+v aggregation=%+v", summary, all)
	}
}

func TestDatasetCandidateOutcomeAggregationAndReport(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	records := []datasetEpisodeRecord{
		datasetRecord("BTCUSDT", "1m", start, strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.9, "candidate_ready", ""),
		datasetRecord("BTCUSDT", "1m", start.Add(time.Hour), strategy.EpisodeTrueReversalCandidate, 1, 1, 0, 100, 0.9, "candidate_ready", ""),
	}
	records[0].Split = datasetSplitTrain
	records[1].Split = datasetSplitValidation
	runs := []datasetSensitivityRun{
		{
			RunKey:        "train-run",
			SourceFile:    "a_train_episode.json",
			ManifestIndex: 0,
			RecordIndex:   0,
			Split:         datasetSplitTrain,
			Manifest:      datasetManifestEpisode{Symbol: "BTCUSDT", Timeframe: "1m"},
			Results: []strategy.ReversalSensitivityResult{{
				MinRSIDelta:         5,
				NoNewLowBars:        3,
				ScoreThreshold:      0.8,
				MinVolumeRatio:      2,
				SetupMemoryBars:     4,
				InvalidationATRMult: 0.2,
				CandidateOutcomes: []strategy.ReversalCandidateOutcome{{
					Time:                start,
					TPBps:               50,
					SLBps:               30,
					FeeBps:              1,
					SlippageBps:         1,
					ForwardReturn5mBps:  20,
					ForwardReturn15mBps: 40,
					ForwardReturn30mBps: 50,
					MaxFavorable30mBps:  80,
					MaxAdverse30mBps:    -20,
					TimeToMFE:           "10m0s",
					TimeToMAE:           "2m0s",
					WouldHitTP:          true,
					HitOrder:            "tp_first",
					NetReturnBps:        48,
					NetR:                1.6,
				}},
			}},
		},
		{
			RunKey:        "validation-run",
			SourceFile:    "b_validation_episode.json",
			ManifestIndex: 0,
			RecordIndex:   1,
			Split:         datasetSplitValidation,
			Manifest:      datasetManifestEpisode{Symbol: "BTCUSDT", Timeframe: "1m"},
			Results: []strategy.ReversalSensitivityResult{{
				MinRSIDelta:         5,
				NoNewLowBars:        3,
				ScoreThreshold:      0.8,
				MinVolumeRatio:      2,
				SetupMemoryBars:     4,
				InvalidationATRMult: 0.2,
				CandidateOutcomes: []strategy.ReversalCandidateOutcome{
					{
						Time:                start.Add(time.Hour),
						TPBps:               50,
						SLBps:               30,
						FeeBps:              1,
						SlippageBps:         1,
						ForwardReturn5mBps:  30,
						ForwardReturn15mBps: 60,
						ForwardReturn30mBps: 70,
						MaxFavorable30mBps:  100,
						MaxAdverse30mBps:    -10,
						TimeToMFE:           "12m0s",
						TimeToMAE:           "1m0s",
						WouldHitTP:          true,
						HitOrder:            "tp_first",
						NetReturnBps:        48,
						NetR:                1.6,
					},
					{
						Time:                start.Add(time.Hour + time.Minute),
						TPBps:               50,
						SLBps:               30,
						FeeBps:              1,
						SlippageBps:         1,
						ForwardReturn5mBps:  -10,
						ForwardReturn15mBps: -20,
						ForwardReturn30mBps: -30,
						MaxFavorable30mBps:  20,
						MaxAdverse30mBps:    -40,
						TimeToMFE:           "3m0s",
						TimeToMAE:           "8m0s",
						WouldHitSL:          true,
						HitOrder:            "sl_first",
						NetReturnBps:        -32,
						NetR:                -1.0666666666666667,
					},
				},
			}},
		},
		{
			RunKey:        "validation-run-duplicate",
			SourceFile:    "c_validation_episode.json",
			ManifestIndex: 0,
			RecordIndex:   2,
			Split:         datasetSplitValidation,
			Manifest:      datasetManifestEpisode{Symbol: "BTCUSDT", Timeframe: "1m"},
			Results: []strategy.ReversalSensitivityResult{{
				MinRSIDelta:         5,
				NoNewLowBars:        3,
				ScoreThreshold:      0.8,
				MinVolumeRatio:      2,
				SetupMemoryBars:     4,
				InvalidationATRMult: 0.2,
				CandidateOutcomes: []strategy.ReversalCandidateOutcome{{
					Time:                start.Add(time.Hour),
					TPBps:               50,
					SLBps:               30,
					FeeBps:              1,
					SlippageBps:         1,
					ForwardReturn5mBps:  -10,
					ForwardReturn15mBps: -20,
					ForwardReturn30mBps: -30,
					MaxFavorable30mBps:  20,
					MaxAdverse30mBps:    -40,
					TimeToMFE:           "3m0s",
					TimeToMAE:           "8m0s",
					WouldHitSL:          true,
					HitOrder:            "sl_first",
					NetReturnBps:        -32,
					NetR:                -1.0666666666666667,
				}},
			}},
		},
	}
	summary := buildDatasetSummary("unit", records, runs, 0, nil, datasetRunnerOptions{TrainRatio: 0.5})
	if summary.CandidateOutcomeCount != 4 {
		t.Fatalf("candidate outcome count got %d want 4", summary.CandidateOutcomeCount)
	}
	if summary.RawMarketCandidateCount != 4 || summary.UniqueMarketCandidateCount != 3 || summary.DuplicateCandidateCount != 1 {
		t.Fatalf("unexpected market candidate counts: %+v", summary)
	}
	validation := summary.CandidateOutcomeAggregation[datasetSplitValidation]
	if len(validation) != 1 {
		t.Fatalf("validation outcome aggregation len got %d: %+v", len(validation), validation)
	}
	if validation[0].RawCandidateCount != 3 || validation[0].CandidateCount != 3 || validation[0].RawWinningCandidateCount != 1 || validation[0].RawWinRate != 1.0/3.0 {
		t.Fatalf("unexpected validation outcome aggregate: %+v", validation[0])
	}
	if validation[0].UniqueMarketCandidateCount != 2 || validation[0].UniqueMarketWinningCandidateCount != 1 || validation[0].UniqueMarketWinRate != 0.5 {
		t.Fatalf("unexpected deduplicated validation outcome aggregate: %+v", validation[0])
	}
	wantUniqueAvg := (1.6 - 1.0666666666666667) / 2
	if math.Abs(validation[0].UniqueMarketAvgNetR-wantUniqueAvg) > 1e-9 {
		t.Fatalf("deduplicated validation avg_net_r got %.12f want %.12f: %+v", validation[0].UniqueMarketAvgNetR, wantUniqueAvg, validation[0])
	}
	wantRawAvg := (1.6 - 1.0666666666666667 - 1.0666666666666667) / 3
	if math.Abs(validation[0].RawAvgNetR-wantRawAvg) > 1e-9 {
		t.Fatalf("raw validation avg_net_r got %.12f want %.12f: %+v", validation[0].RawAvgNetR, wantRawAvg, validation[0])
	}
	if len(summary.CandidateOutcomeExamples) != 4 || summary.CandidateOutcomeExamples[0].Split != datasetSplitValidation {
		t.Fatalf("unexpected candidate examples: %+v", summary.CandidateOutcomeExamples)
	}
	path := filepath.Join(t.TempDir(), "dataset_report.md")
	if err := writeDatasetReport(path, summary, datasetAcceptanceResult{Recommendation: acceptanceDoNotCalibrate}); err != nil {
		t.Fatalf("write report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(raw)
	for _, want := range []string{
		"## Candidate Outcome Summary",
		"## Raw Candidate Outcome Summary",
		"## Deduplicated Market-Time Outcome Summary",
		"## Offline Filter Rejected Candidate Reasons",
		"## Best eligible parameter sets by validation avg_net_r",
		"## Best raw parameter sets by validation avg_net_r",
		"## Worst parameter sets",
		"## Candidate examples",
		"candidate_outcome_count: `4`",
		"duplicate_candidate_count: `1`",
		"Deduplication key: symbol + event_time + sensitivity parameter set.",
		"validation_unique_market_candidates",
		"train_validation_avg_net_r_delta",
		"duplicate_candidate_ratio",
		"validation_avg_mfe_bps",
		"| 5.000 | 3 | 0.800 | 2.000 | 4 | 0.200 | false | 0.000 | 0 | false | false | 50.00 | 30.00 | 2 | 0.2667 | 0.2667 | 60.00 | -25.00 | 1.6000 | 1.3333 | 0.3333 | 0.5000 | 3 |",
		"| validation | validation-run |",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestDatasetReportEligibleOutcomeRankingExcludesLowSampleRawLeader(t *testing.T) {
	singleCandidate := testOutcomeAggregate(1, 2.0, 2.0)
	singleCandidate.TPBps = 60
	singleCandidate.UniqueMarketAvgMFEBps = 90
	singleCandidate.UniqueMarketAvgMAEBps = -5
	singleCandidate.AvgMFEBps = singleCandidate.UniqueMarketAvgMFEBps
	singleCandidate.AvgMAEBps = singleCandidate.UniqueMarketAvgMAEBps

	eligible := testOutcomeAggregate(10, 0.5, 0.4)
	eligible.TPBps = 50
	eligible.UniqueMarketAvgMFEBps = 70
	eligible.UniqueMarketAvgMAEBps = -20
	eligible.AvgMFEBps = eligible.UniqueMarketAvgMFEBps
	eligible.AvgMAEBps = eligible.UniqueMarketAvgMAEBps

	trainSingle := singleCandidate
	trainSingle.UniqueMarketAvgNetR = 0.20
	trainSingle.AvgNetR = trainSingle.UniqueMarketAvgNetR
	trainEligible := eligible
	trainEligible.UniqueMarketAvgNetR = 0.30
	trainEligible.AvgNetR = trainEligible.UniqueMarketAvgNetR

	summary := datasetSummary{
		RunID:                      "unit",
		SafetyConstraintsPassed:    true,
		RawMarketCandidateCount:    11,
		UniqueMarketCandidateCount: 11,
		CandidateOutcomeAggregation: map[string][]datasetCandidateOutcomeAggregate{
			datasetSplitTrain:      []datasetCandidateOutcomeAggregate{trainSingle, trainEligible},
			datasetSplitValidation: []datasetCandidateOutcomeAggregate{singleCandidate, eligible},
		},
	}
	acceptance := datasetAcceptanceResult{
		Recommendation:                       acceptanceReadyOfflineCalibration,
		MinValidationUniqueMarketCandidates:  10,
		MaxDuplicateCandidateRatio:           0.75,
		SafetyConstraintsPass:                true,
		ValidationUniqueMarketCandidatesPass: true,
		DuplicateCandidateRatioPass:          true,
	}

	path := filepath.Join(t.TempDir(), "dataset_report.md")
	if err := writeDatasetReport(path, summary, acceptance); err != nil {
		t.Fatalf("write report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(raw)
	eligibleSection := markdownSection(report, "## Best eligible parameter sets by validation avg_net_r")
	rawSection := markdownSection(report, "## Best raw parameter sets by validation avg_net_r")
	if eligibleSection == "" || rawSection == "" {
		t.Fatalf("missing ranking sections:\n%s", report)
	}

	singleCandidateRow := "| 5.000 | 3 | 0.800 | 2.000 | 4 | 0.200 | false | 0.000 | 0 | false | false | 60.00 | 30.00 | 1 | 2.0000 |"
	eligibleRow := "| 5.000 | 3 | 0.800 | 2.000 | 4 | 0.200 | false | 0.000 | 0 | false | false | 50.00 | 30.00 | 10 | 0.5000 |"
	if strings.Contains(eligibleSection, singleCandidateRow) {
		t.Fatalf("eligible ranking included low-sample raw leader:\n%s", eligibleSection)
	}
	if !strings.Contains(eligibleSection, eligibleRow) {
		t.Fatalf("eligible ranking missing eligible row:\n%s", eligibleSection)
	}
	if !strings.Contains(rawSection, singleCandidateRow) {
		t.Fatalf("raw ranking did not include low-sample row:\n%s", rawSection)
	}
	if !strings.Contains(rawSection, "Warning:") || !strings.Contains(rawSection, "validation_unique_market_candidates below threshold 10") {
		t.Fatalf("raw ranking missing low-sample warning:\n%s", rawSection)
	}
}

func TestDatasetAggregationsIncludeOfflineFilterParameters(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	baseResult := strategy.ReversalSensitivityResult{
		MinRSIDelta:                   5,
		NoNewLowBars:                  3,
		ScoreThreshold:                0.8,
		MinVolumeRatio:                2,
		SetupMemoryBars:               4,
		InvalidationATRMult:           0.2,
		UnfilteredCandidateReadyCount: 2,
		CandidateReadyCount:           1,
		CandidateOutcomes: []strategy.ReversalCandidateOutcome{{
			Time:         start,
			TPBps:        50,
			SLBps:        30,
			NetReturnBps: 48,
			NetR:         1.6,
		}},
	}
	filteredResult := baseResult
	filteredResult.RequireOrderbookNotHostile = true
	filteredResult.MinOrderbookRatio = 1.2
	filteredResult.MinBarsWithoutNewLowAfterReclaim = 2
	filteredResult.RequireCloseAboveMiddleBB = true
	filteredResult.RequireHigherLowAfterReclaim = true
	filteredResult.OfflineFilterRejectedCandidateCount = 1
	filteredResult.OfflineFilterRejectReasons = map[string]int{"orderbook_hostile": 1}

	runs := []datasetSensitivityRun{{
		RunKey:      "plain",
		SourceFile:  "a.json",
		RecordIndex: 0,
		Manifest:    datasetManifestEpisode{Symbol: "BTCUSDT", Timeframe: "1m"},
		Results:     []strategy.ReversalSensitivityResult{baseResult},
	}, {
		RunKey:      "filtered",
		SourceFile:  "b.json",
		RecordIndex: 1,
		Manifest:    datasetManifestEpisode{Symbol: "BTCUSDT", Timeframe: "1m"},
		Results:     []strategy.ReversalSensitivityResult{filteredResult},
	}}

	sensitivity := aggregateSensitivityRuns(runs, "")
	if len(sensitivity) != 2 {
		t.Fatalf("sensitivity aggregation should keep filter params distinct: %+v", sensitivity)
	}
	if sensitivity[1].UnfilteredCandidateReadyCount != 2 || sensitivity[1].FilteredCandidateReadyCount != 1 || sensitivity[1].OfflineFilterRejectedCandidateCount != 1 {
		t.Fatalf("candidate count impact not aggregated: %+v", sensitivity[1])
	}

	outcomes := aggregateCandidateOutcomeRuns(runs, "")
	if len(outcomes) != 2 {
		t.Fatalf("outcome aggregation should keep filter params distinct: %+v", outcomes)
	}
	if !outcomes[1].RequireOrderbookNotHostile || outcomes[1].MinOrderbookRatio != 1.2 || outcomes[1].UniqueMarketCandidateCount != 1 {
		t.Fatalf("filtered outcome aggregate missing filter params: %+v", outcomes[1])
	}

	total, reasons := offlineFilterRejectReasonSummary(runs, "")
	if total != 1 || len(reasons) != 1 || reasons[0].Value != "orderbook_hostile" || reasons[0].Count != 1 {
		t.Fatalf("unexpected filter reject summary total=%d reasons=%+v", total, reasons)
	}
	if key := candidateMarketKeyString(candidateOutcomeMarketKey(runs[1], sensitivityKeyFromResult(filteredResult), filteredResult.CandidateOutcomes[0])); !strings.Contains(key, "require_orderbook_not_hostile=true") {
		t.Fatalf("unique market key did not include filter params: %s", key)
	}
}

func TestDatasetAcceptanceUsesDeduplicatedMarketCandidatesWhenAvailable(t *testing.T) {
	summary := datasetSummary{
		TotalEpisodes:              50,
		AvgOrderbookCoveragePct:    100,
		CandidateReadyEpisodes:     12,
		RawMarketCandidateCount:    12,
		UniqueMarketCandidateCount: 9,
		DuplicateCandidateCount:    3,
		SafetyConstraintsPassed:    true,
	}
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceNeedMoreData || acceptance.CandidateCountPass {
		t.Fatalf("acceptance should use deduplicated candidate count: %+v", acceptance)
	}
	if acceptance.ActualCandidateEpisodes != 9 || acceptance.CandidateCountUnit != "unique_market_candidate" {
		t.Fatalf("unexpected candidate count unit: %+v", acceptance)
	}
}

func TestDatasetAcceptanceSafetyStillDominatesDeduplicatedCandidates(t *testing.T) {
	summary := datasetSummary{
		TotalEpisodes:              50,
		AvgOrderbookCoveragePct:    100,
		CandidateReadyEpisodes:     50,
		RawMarketCandidateCount:    20,
		UniqueMarketCandidateCount: 20,
		SafetyConstraintsPassed:    false,
		OrdersSent:                 1,
		CandidateOutcomeAggregation: map[string][]datasetCandidateOutcomeAggregate{
			datasetSplitTrain:      []datasetCandidateOutcomeAggregate{testOutcomeAggregate(20, 0.2, 0.1)},
			datasetSplitValidation: []datasetCandidateOutcomeAggregate{testOutcomeAggregate(20, 0.3, 0.2)},
		},
	}
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceDoNotCalibrate || acceptance.SafetyConstraintsPass {
		t.Fatalf("safety constraint should be unchanged: %+v", acceptance)
	}
}

func TestDatasetAcceptanceOutcomePositiveReady(t *testing.T) {
	summary := testOutcomeAcceptanceSummary(20, 20, 0, testOutcomeAggregate(12, 0.35, 0.20))
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceReadyOfflineCalibration {
		t.Fatalf("positive validation outcome should be calibration-ready: %+v", acceptance)
	}
	if !acceptance.OutcomeEnabled ||
		!acceptance.ValidationUniqueMarketCandidatesPass ||
		!acceptance.ValidationAvgNetRPass ||
		!acceptance.ValidationMedianNetRPass ||
		!acceptance.DuplicateCandidateRatioPass {
		t.Fatalf("unexpected outcome gates: %+v", acceptance)
	}

	path := filepath.Join(t.TempDir(), "acceptance_report.md")
	if err := writeDatasetAcceptanceReport(path, acceptance); err != nil {
		t.Fatalf("write acceptance report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read acceptance report: %v", err)
	}
	report := string(raw)
	for _, want := range []string{
		"## Outcome Gates",
		"- outcome enabled: true",
		"validation unique candidates: PASS",
		"validation avg_net_r: PASS",
		"validation median_net_r: PASS",
		"duplicate candidate ratio: PASS",
		"recommendation: `READY_FOR_OFFLINE_CALIBRATION`",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("acceptance report missing %q:\n%s", want, report)
		}
	}
}

func TestDatasetAcceptanceNegativeValidationOutcomeRejectsEdge(t *testing.T) {
	summary := testOutcomeAcceptanceSummary(20, 20, 0, testOutcomeAggregate(12, -0.40, -1.20))
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceRejectNegativeEdge {
		t.Fatalf("negative validation outcome should reject edge: %+v", acceptance)
	}
	if acceptance.ValidationAvgNetRPass || acceptance.ValidationMedianNetRPass {
		t.Fatalf("negative validation metrics unexpectedly passed: %+v", acceptance)
	}
}

func TestDatasetAcceptanceLowValidationOutcomeSampleNeedsMoreData(t *testing.T) {
	summary := testOutcomeAcceptanceSummary(20, 20, 0, testOutcomeAggregate(7, 0.50, 0.25))
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceNeedMoreData {
		t.Fatalf("low validation outcome sample should need more data: %+v", acceptance)
	}
	if acceptance.ValidationUniqueMarketCandidatesPass {
		t.Fatalf("validation unique candidate gate unexpectedly passed: %+v", acceptance)
	}
}

func TestDatasetAcceptanceHighDuplicateOutcomeRatioNeedsMoreData(t *testing.T) {
	summary := testOutcomeAcceptanceSummary(100, 20, 80, testOutcomeAggregate(20, 0.50, 0.25))
	acceptance := evaluateDatasetAcceptance(summary, 50, 0.80, 10)
	if acceptance.Recommendation != acceptanceNeedMoreData {
		t.Fatalf("high duplicate outcome ratio should need more data: %+v", acceptance)
	}
	if acceptance.DuplicateCandidateRatioPass {
		t.Fatalf("duplicate candidate ratio gate unexpectedly passed: %+v", acceptance)
	}
}

func TestDatasetReportWarnsWhenSensitivityTotalsMismatch(t *testing.T) {
	summary := datasetSummary{
		RunID:                       "unit",
		TotalEpisodes:               2,
		ExtractedEpisodesTotal:      2,
		SensitivityEpisodesTotal:    1,
		AggregationUnit:             datasetAggregationUnitExtractedEpisode,
		AggregationWarnings:         []string{"sensitivity episode total mismatch: extracted_episodes_total=2 sensitivity_episodes_total=1 aggregation_unit=extracted_episode"},
		MaxScoreDistribution:        map[string]int{},
		SensitivityAggregation:      map[string][]datasetSensitivityAggregate{},
		SafetyConstraintsPassed:     true,
		AcceptanceRecommendation:    acceptanceDoNotCalibrate,
		StableSensitivityParameters: nil,
	}
	for _, split := range []string{datasetSplitAll, datasetSplitTrain, datasetSplitValidation} {
		summary.SensitivityAggregation[split] = nil
	}
	path := filepath.Join(t.TempDir(), "dataset_report.md")
	if err := writeDatasetReport(path, summary, datasetAcceptanceResult{Recommendation: acceptanceDoNotCalibrate}); err != nil {
		t.Fatalf("write report: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(raw)
	for _, want := range []string{"extracted_episodes_total: `2`", "sensitivity_episodes_total: `1`", "aggregation_unit: `extracted_episode`", "aggregation_warnings:", "sensitivity episode total mismatch"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

func TestDatasetAggregationWarningsFlagDifferentUnit(t *testing.T) {
	warnings := datasetAggregationWarnings(datasetSummary{
		ExtractedEpisodesTotal:   2,
		SensitivityEpisodesTotal: 2,
		AggregationUnit:          "manifest_window",
		Split: datasetSplitSummary{
			TrainEpisodes:      0,
			ValidationEpisodes: 0,
		},
	}, nil)
	if len(warnings) == 0 || !strings.Contains(warnings[0], "manifest_window") {
		t.Fatalf("expected aggregation unit warning, got %+v", warnings)
	}
}

func markdownSection(report, heading string) string {
	start := strings.Index(report, heading)
	if start < 0 {
		return ""
	}
	section := report[start+len(heading):]
	if next := strings.Index(section, "\n## "); next >= 0 {
		section = section[:next]
	}
	return section
}

func testOutcomeAcceptanceSummary(rawMarkets, uniqueMarkets, duplicateMarkets int, validation datasetCandidateOutcomeAggregate) datasetSummary {
	train := validation
	train.UniqueMarketAvgNetR = 0.20
	train.UniqueMarketMedianNetR = 0.10
	train.AvgNetR = train.UniqueMarketAvgNetR
	train.MedianNetR = train.UniqueMarketMedianNetR
	return datasetSummary{
		TotalEpisodes:              50,
		AvgOrderbookCoveragePct:    100,
		CandidateReadyEpisodes:     50,
		RawMarketCandidateCount:    rawMarkets,
		UniqueMarketCandidateCount: uniqueMarkets,
		DuplicateCandidateCount:    duplicateMarkets,
		SafetyConstraintsPassed:    true,
		CandidateOutcomeAggregation: map[string][]datasetCandidateOutcomeAggregate{
			datasetSplitTrain:      []datasetCandidateOutcomeAggregate{train},
			datasetSplitValidation: []datasetCandidateOutcomeAggregate{validation},
		},
	}
}

func testOutcomeAggregate(uniqueCandidates int, avgNetR, medianNetR float64) datasetCandidateOutcomeAggregate {
	return datasetCandidateOutcomeAggregate{
		MinRSIDelta:                       5,
		NoNewLowBars:                      3,
		ScoreThreshold:                    0.8,
		MinVolumeRatio:                    2,
		SetupMemoryBars:                   4,
		InvalidationATRMult:               0.2,
		TPBps:                             50,
		SLBps:                             30,
		RawCandidateCount:                 uniqueCandidates,
		RawWinningCandidateCount:          uniqueCandidates,
		RawWinRate:                        1,
		RawAvgNetR:                        avgNetR,
		RawMedianNetR:                     medianNetR,
		UniqueMarketCandidateCount:        uniqueCandidates,
		UniqueMarketWinningCandidateCount: uniqueCandidates,
		UniqueMarketWinRate:               1,
		UniqueMarketAvgNetR:               avgNetR,
		UniqueMarketMedianNetR:            medianNetR,
		CandidateCount:                    uniqueCandidates,
		WinningCandidateCount:             uniqueCandidates,
		WinRate:                           1,
		AvgNetR:                           avgNetR,
		MedianNetR:                        medianNetR,
	}
}

func datasetRecord(symbol, timeframe string, start time.Time, class strategy.ReversalEpisodeClass, candidates, setups, invalidations int, coverage, maxScore float64, blocker, invalidationReason string) datasetEpisodeRecord {
	return datasetEpisodeRecord{
		ManifestIndex: 0,
		RunKey:        start.Format("150405"),
		Dataset: strategy.ReversalEpisodeDataset{Summary: strategy.ReversalEpisodeSummary{
			EpisodeID:              start.Format("150405"),
			Classification:         class,
			Symbol:                 symbol,
			Timeframe:              timeframe,
			Start:                  start,
			End:                    start.Add(time.Minute),
			CandidateReadyCount:    candidates,
			SetupDetectedCount:     setups,
			SetupInvalidatedCount:  invalidations,
			OrderbookCoveragePct:   coverage,
			MaxScore:               maxScore,
			MainBlocker:            blocker,
			MainInvalidationReason: invalidationReason,
		}},
	}
}

func sensitivityRunForRecord(records []datasetEpisodeRecord, idx, candidates, setups, invalidations int, maxScore float64) datasetSensitivityRun {
	return datasetSensitivityRun{
		RunKey:        records[idx].RunKey,
		ManifestIndex: records[idx].ManifestIndex,
		RecordIndex:   idx,
		Results: []strategy.ReversalSensitivityResult{{
			MinRSIDelta:           5,
			NoNewLowBars:          3,
			ScoreThreshold:        0.8,
			MinVolumeRatio:        2,
			MaxScore:              maxScore,
			CandidateReadyCount:   candidates,
			SetupDetectedCount:    setups,
			SetupInvalidatedCount: invalidations,
		}},
	}
}

func writeDatasetRunnerTestCSV(t *testing.T, path string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("time,open,high,low,close,volume,best_bid,best_ask,best_bid_size,best_ask_size\n")
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 40; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		price := 100.0 + float64(i)*0.1
		fmtLine := fmt.Sprintf("%s,%.2f,%.2f,%.2f,%.2f,10,%.2f,%.2f,2,1\n",
			ts.Format(time.RFC3339), price, price+0.5, price-0.5, price+0.1, price, price+0.1)
		b.WriteString(fmtLine)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write dataset csv: %v", err)
	}
}

func writeOrderbookEnrichmentCandlesCSV(t *testing.T, path string, times ...time.Time) {
	t.Helper()
	var b strings.Builder
	b.WriteString("time,open,high,low,close,volume\n")
	for i, ts := range times {
		price := 100 + float64(i)
		fmt.Fprintf(&b, "%s,%.2f,%.2f,%.2f,%.2f,%.2f\n",
			ts.Format(time.RFC3339Nano), price, price+1, price-1, price+0.5, 10+float64(i))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write enrichment candle csv: %v", err)
	}
}

func writeOrderbookEnrichmentJSONL(t *testing.T, path string, events ...map[string]any) {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, event := range events {
		if err := enc.Encode(event); err != nil {
			t.Fatalf("encode orderbook event: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write orderbook jsonl: %v", err)
	}
}

func validOrderbookSnapshotEvent(ts time.Time) map[string]any {
	return map[string]any{
		"event":               "orderbook_diagnostic_snapshot",
		"timestamp":           ts.Format(time.RFC3339Nano),
		"symbol":              "BTCUSDT",
		"orderbook_available": true,
		"best_bid":            100.1,
		"best_ask":            100.2,
		"best_bid_size":       2.5,
		"best_ask_size":       1.25,
		"bid_ask_imbalance":   0.3333,
		"spread_bps":          9.98,
		"orderbook_ratio":     2.0,
		"depth_levels":        50,
	}
}

func readOrderbookEnrichmentCSV(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read enrichment csv: %v", err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatalf("parse enrichment csv: %v", err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected enrichment csv data row, got %d rows", len(rows))
	}
	return rows
}

func writeEpisodeMiningTestCSV(t *testing.T, path string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("time,open,high,low,close,volume,best_bid,best_ask,best_bid_size,best_ask_size\n")
	start := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	prevClose := 100.0
	for i := 0; i < 180; i++ {
		closePrice := 100.0
		switch {
		case i == 70:
			closePrice = 80
		case i == 71:
			closePrice = 96
		case i == 72:
			closePrice = 103
		case i > 72 && i < 105:
			closePrice = 101 + float64(i%3)
		case i == 110:
			closePrice = 82
		case i == 111:
			closePrice = 95
		case i == 112:
			closePrice = 74
		case i > 112 && i < 125:
			closePrice = 76 + float64(i%2)
		}
		high := closePrice + 1
		low := closePrice - 1
		switch i {
		case 70:
			low = 78
			high = 101
		case 71:
			low = 94
			high = 98
		case 72:
			low = 95
			high = 106
		case 110:
			low = 80
			high = 100
		case 111:
			low = 93
			high = 97
		case 112:
			low = 70
			high = 96
		}
		ts := start.Add(time.Duration(i) * time.Minute)
		fmt.Fprintf(&b, "%s,%.2f,%.2f,%.2f,%.2f,10,,,,\n",
			ts.Format(time.RFC3339), prevClose, high, low, closePrice)
		prevClose = closePrice
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write mining csv: %v", err)
	}
}

func writeNoSetupMiningCSV(t *testing.T, path string) {
	t.Helper()
	var b strings.Builder
	b.WriteString("time,open,high,low,close,volume,best_bid,best_ask,best_bid_size,best_ask_size\n")
	start := time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC)
	prevClose := 100.0
	for i := 0; i < 120; i++ {
		closePrice := 100.0 + float64(i)*0.02
		high := closePrice + 0.05
		low := closePrice - 0.05
		ts := start.Add(time.Duration(i) * time.Minute)
		fmt.Fprintf(&b, "%s,%.2f,%.2f,%.2f,%.2f,10,,,,\n",
			ts.Format(time.RFC3339), prevClose, high, low, closePrice)
		prevClose = closePrice
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write no-setup mining csv: %v", err)
	}
}
