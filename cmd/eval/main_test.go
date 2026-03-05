package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sharedforensics "verbose-fortnight/internal/forensics"
)

func TestEvaluationHarnessDeterministic(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fixture.log")
	content := "" +
		"2026/02/25 00:44:10.000001 main.go:1 [INFO] execution_fill {\"ts\":\"2026-02-25T00:44:10Z\",\"symbol\":\"BTCUSDT\",\"execId\":\"e1\",\"isMaker\":true,\"execFee\":0.10}\n" +
		"2026/02/25 00:44:11.000001 main.go:1 [INFO] execution_fill {\"ts\":\"2026-02-25T00:44:11Z\",\"symbol\":\"BTCUSDT\",\"execId\":\"e2\",\"isMaker\":false,\"execFee\":0.20}\n" +
		"2026/02/25 00:50:00.000001 main.go:1 [INFO] trade_close_summary {\"ts\":\"2026-02-25T00:50:00Z\",\"symbol\":\"BTCUSDT\",\"traceKey\":\"lc-1\",\"gross_calc\":2.0,\"fee_total\":0.3,\"net_calc\":1.7,\"net_source\":\"calculated\",\"duration_sec\":60}\n" +
		"2026/02/25 01:10:00.000001 main.go:1 [INFO] trade_close_summary {\"ts\":\"2026-02-25T01:10:00Z\",\"symbol\":\"BTCUSDT\",\"traceKey\":\"lc-2\",\"gross_calc\":-1.0,\"fee_total\":0.2,\"net_calc\":-1.2,\"net_source\":\"calculated\",\"duration_sec\":120}\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := evalConfig{
		From:   time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol: "BTCUSDT",
		Logs:   dir,
		Mode:   "baseline",
		Out:    filepath.Join(dir, "out"),
	}
	res1, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval #1: %v", err)
	}
	res2, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval #2: %v", err)
	}

	b1, _ := json.Marshal(res1.Metrics)
	b2, _ := json.Marshal(res2.Metrics)
	if string(b1) != string(b2) {
		t.Fatalf("metrics are not deterministic:\n%s\n%s", string(b1), string(b2))
	}

	m := res1.Metrics
	if m.Trades != 2 {
		t.Fatalf("trades=%d want 2", m.Trades)
	}
	if m.WinRate != 0.5 {
		t.Fatalf("winrate=%f want 0.5", m.WinRate)
	}
	if m.MakerRatio != 0.5 {
		t.Fatalf("makerRatio=%f want 0.5", m.MakerRatio)
	}
	if m.FeeToGrossRatio != 0.5 {
		t.Fatalf("feeToGrossRatio=%f want 0.5", m.FeeToGrossRatio)
	}
	if m.AvgNetAfterFee != 0.25 {
		t.Fatalf("avgNetAfterFee=%f want 0.25", m.AvgNetAfterFee)
	}
	if m.AvgDuration != 90 {
		t.Fatalf("avgDuration=%f want 90", m.AvgDuration)
	}
	if m.AvgDurationWin != 60 || m.AvgDurationLoss != 120 {
		t.Fatalf("duration win/loss mismatch: %+v", m)
	}
	if m.Markers.TradeCloseSummaryLines != 2 {
		t.Fatalf("tradeCloseSummaryLines=%d want 2", m.Markers.TradeCloseSummaryLines)
	}
	if m.Markers.ExecutionFillLines != 2 {
		t.Fatalf("executionFillLines=%d want 2", m.Markers.ExecutionFillLines)
	}

	if err := writeEvalOutputs(cfg, res1); err != nil {
		t.Fatalf("writeEvalOutputs: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Out, "p6_eval_summary.md")); err != nil {
		t.Fatalf("missing summary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Out, "p6_eval_metrics.json")); err != nil {
		t.Fatalf("missing metrics json: %v", err)
	}
}

func TestEvalSkipsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	goodPath := filepath.Join(dir, "good.log")
	missingTarget := filepath.Join(dir, "gone.log")
	missingLink := filepath.Join(dir, "missing-link.log")

	content := "2026/02/25 00:50:00.000001 main.go:1 [INFO] trade_close_summary {\"ts\":\"2026-02-25T00:50:00Z\",\"symbol\":\"BTCUSDT\",\"traceKey\":\"lc-1\",\"gross_calc\":2.0,\"fee_total\":0.3,\"net_calc\":1.7,\"duration_sec\":60}\n"
	if err := os.WriteFile(goodPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write good log: %v", err)
	}
	if err := os.WriteFile(missingTarget, []byte("x"), 0o644); err != nil {
		t.Fatalf("write missing target: %v", err)
	}
	if err := os.Symlink(missingTarget, missingLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := os.Remove(missingTarget); err != nil {
		t.Fatalf("remove missing target: %v", err)
	}

	cfg := evalConfig{
		From:   time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol: "BTCUSDT",
		Logs:   dir,
		Mode:   "baseline",
		Out:    filepath.Join(dir, "out"),
	}

	res, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if res.Metrics.IO.FilesSkippedMissing != 1 {
		t.Fatalf("filesSkippedMissing=%d want 1", res.Metrics.IO.FilesSkippedMissing)
	}
	if res.Metrics.Trades != 1 {
		t.Fatalf("trades=%d want 1", res.Metrics.Trades)
	}
}

func TestEvalZeroTradesDiagnostics(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "no-trades.log")
	content := "" +
		"2026/02/25 00:44:10.000001 main.go:1 [INFO] orderbook update BTCUSDT\n" +
		"2026/02/25 00:44:11.000001 main.go:1 [INFO] kline update BTCUSDT\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := evalConfig{
		From:   time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol: "BTCUSDT",
		Logs:   dir,
		Mode:   "baseline",
		Out:    filepath.Join(dir, "out"),
	}
	res, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if res.Metrics.Trades != 0 {
		t.Fatalf("trades=%d want 0", res.Metrics.Trades)
	}
	if res.Metrics.Diagnostics.ZeroTradesReason != "no_trade_close_summary" {
		t.Fatalf("zeroTradesReason=%q want no_trade_close_summary", res.Metrics.Diagnostics.ZeroTradesReason)
	}
}

func TestEvalMarkerCounters(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "markers.log")
	content := "" +
		"2026/02/25 00:44:10.000001 main.go:1 [INFO] execution_fill {\"ts\":\"2026-02-25T00:44:10Z\",\"symbol\":\"BTCUSDT\",\"execId\":\"e1\",\"isMaker\":true,\"execFee\":0.10}\n" +
		"2026/02/25 00:44:11.000001 main.go:1 [INFO] execution_list_response {\"retCode\":0,\"list_len\":1}\n" +
		"2026/02/25 00:44:12.000001 main.go:1 [INFO] trade_event {\"event\":\"signal_accepted\"}\n" +
		"2026/02/25 00:50:00.000001 main.go:1 [INFO] trade_close_summary {\"ts\":\"2026-02-25T00:50:00Z\",\"symbol\":\"BTCUSDT\",\"traceKey\":\"lc-1\",\"gross_calc\":2.0,\"fee_total\":0.3,\"net_calc\":1.7,\"duration_sec\":60}\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg := evalConfig{
		From:   time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol: "BTCUSDT",
		Logs:   dir,
		Mode:   "baseline",
		Out:    filepath.Join(dir, "out"),
	}
	res, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if res.Metrics.Markers.TradeCloseSummaryLines != 1 {
		t.Fatalf("tradeCloseSummaryLines=%d want 1", res.Metrics.Markers.TradeCloseSummaryLines)
	}
	if res.Metrics.Markers.ExecutionFillLines != 1 {
		t.Fatalf("executionFillLines=%d want 1", res.Metrics.Markers.ExecutionFillLines)
	}
	if res.Metrics.Markers.ExecutionListResponseLine != 1 {
		t.Fatalf("executionListResponseLines=%d want 1", res.Metrics.Markers.ExecutionListResponseLine)
	}
	if res.Metrics.Markers.TradeEventLines != 1 {
		t.Fatalf("tradeEventLines=%d want 1", res.Metrics.Markers.TradeEventLines)
	}
	if res.Metrics.Trades != 1 {
		t.Fatalf("trades=%d want 1", res.Metrics.Trades)
	}
	if res.Metrics.ExecutionSamples != 1 {
		t.Fatalf("executionSamples=%d want 1", res.Metrics.ExecutionSamples)
	}
}

func TestEvalAutoFallsBackToReconstructWhenNoTradeCloseSummary(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "reconstruct.log.gz")
	f, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create gz: %v", err)
	}
	gw := gzip.NewWriter(f)
	content := "" +
		"2026/02/25 00:40:00.000001 main.go:1 [INFO] trade_event {\"event\":\"entry_intent\",\"symbol\":\"BTCUSDT\",\"trace_key\":\"lc_1\",\"side\":\"LONG\",\"qty\":0.01}\n" +
		"2026/02/25 00:45:00.000001 main.go:1 [INFO] Received response from exchange for /v5/position/list: Status 200, Body: {\"retCode\":0,\"result\":{\"list\":[{\"symbol\":\"BTCUSDT\",\"side\":\"Buy\",\"size\":\"0.01\",\"avgPrice\":\"64000\",\"curRealisedPnl\":\"0\",\"cumRealisedPnl\":\"10.0\"}]}}\n" +
		"2026/02/25 00:55:00.000001 main.go:1 [INFO] Received response from exchange for /v5/position/list: Status 200, Body: {\"retCode\":0,\"result\":{\"list\":[{\"symbol\":\"BTCUSDT\",\"side\":\"Buy\",\"size\":\"0\",\"avgPrice\":\"0\",\"curRealisedPnl\":\"-2.1\",\"cumRealisedPnl\":\"7.9\"}]}}\n"
	if _, err := gw.Write([]byte(content)); err != nil {
		t.Fatalf("write gz payload: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close file: %v", err)
	}

	cfg := evalConfig{
		From:        time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:          time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol:      "BTCUSDT",
		Logs:        dir,
		Mode:        "baseline",
		TradeSource: "auto",
		Out:         filepath.Join(dir, "out"),
	}
	res, err := runEval(cfg)
	if err != nil {
		t.Fatalf("runEval: %v", err)
	}
	if res.Metrics.TradesSource != "reconstruct" {
		t.Fatalf("tradesSource=%s want reconstruct", res.Metrics.TradesSource)
	}
	if res.Metrics.Trades == 0 {
		t.Fatalf("expected reconstructed trades > 0")
	}
}

func TestMetricsComputedFromReconstructedTrades(t *testing.T) {
	cfg := evalConfig{
		From:   time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC),
		To:     time.Date(2026, 2, 25, 2, 0, 0, 0, time.UTC),
		Symbol: "BTCUSDT",
		Mode:   "baseline",
	}
	trades := []sharedforensics.ReconstructedTrade{
		{
			Tier:        "tier1",
			Confidence:  0.95,
			OpenTS:      cfg.From,
			CloseTS:     cfg.From.Add(60 * time.Second),
			DurationSec: 60,
			Side:        "LONG",
			Realised:    1.5,
			FeeClose:    0.2,
			GrossCalc:   1.7,
			NetCalc:     1.3,
			MakerFills:  1,
		},
		{
			Tier:         "tier3",
			Confidence:   0.55,
			OpenTS:       cfg.From.Add(120 * time.Second),
			CloseTS:      cfg.From.Add(240 * time.Second),
			DurationSec:  120,
			Side:         "SHORT",
			Realised:     -2.0,
			FeeClose:     0.1,
			GrossCalc:    -1.9,
			NetCalc:      -2.1,
			UnknownFills: 1,
		},
	}
	stats := sharedforensics.ReconstructStats{
		Tier1:         1,
		Tier3:         1,
		ConfidenceAvg: 0.75,
	}
	m := computeMetricsFromReconstructed(cfg, nil, trades, stats)
	if m.Trades != 2 {
		t.Fatalf("trades=%d want 2", m.Trades)
	}
	if m.WinRate != 0.5 {
		t.Fatalf("winrate=%f want 0.5", m.WinRate)
	}
	if m.ProfitFactor <= 0 {
		t.Fatalf("profit_factor must be >0")
	}
	if m.Reconstruct.Tier1 != 1 || m.Reconstruct.Tier3 != 1 {
		t.Fatalf("unexpected reconstruct tiers: %+v", m.Reconstruct)
	}
}
