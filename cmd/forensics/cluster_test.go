package main

import (
	"testing"
	"time"
)

func TestMatchReportsTier1(t *testing.T) {
	base := time.Date(2026, 2, 25, 0, 44, 0, 0, time.UTC)
	events := []Event{
		{
			TS:           base.Add(-2 * time.Second),
			Type:         eventExecutionFill,
			Symbol:       "BTCUSDT",
			TraceKey:     "lc_1",
			LifecycleID:  "lc_1",
			OrderID:      "o_1",
			ExecID:       "e_1",
			PositionSide: "LONG",
			Qty:          0.013,
			Price:        64232.5,
			ClosedSize:   0.013,
			ReduceOnly:   true,
			ExecFee:      0.5004,
		},
		{
			TS:             base,
			Type:           eventTradeCloseSummary,
			Symbol:         "BTCUSDT",
			TraceKey:       "lc_1",
			LifecycleID:    "lc_1",
			OrderID:        "o_1",
			ExecID:         "e_1",
			PositionSide:   "LONG",
			QtyClosedTotal: 0.013,
			EntryVWAP:      64069,
			ExitVWAP:       64232.5,
			GrossCalc:      2.1255,
			GrossSource:    "position_side",
			FeeOpenAlloc:   0.5004,
			FeeClose:       0.5004,
			FeeTotal:       1.0008,
			NetCalc:        1.1247,
			NetSource:      "calculated",
		},
	}
	clusters, pos := buildClusters(events, "BTCUSDT")
	if len(pos) != 0 {
		t.Fatalf("unexpected position events")
	}
	reports := []ReportTrade{{
		Index:        1,
		Time:         base,
		Symbol:       "BTCUSDT",
		PositionSide: "LONG",
		Qty:          0.013,
		Entry:        64069,
		Exit:         64232.5,
		Gross:        2.1255,
		Fee:          1.0008,
		Net:          -3.0312,
	}}
	matches := matchReports(reports, clusters, pos)
	if len(matches) != 1 || !matches[0].Matched {
		t.Fatalf("expected one matched result: %+v", matches)
	}
	if matches[0].Tier != "tier1" {
		t.Fatalf("expected tier1 got %s", matches[0].Tier)
	}
	if matches[0].Confidence < 0.95 {
		t.Fatalf("expected high confidence got %.2f", matches[0].Confidence)
	}
}

func TestBuildDeltaOnlyCluster(t *testing.T) {
	base := time.Date(2026, 2, 25, 0, 44, 0, 0, time.UTC)
	rep := ReportTrade{
		Index: 1, Time: base, Symbol: "BTCUSDT", Gross: 2.1, Fee: 1.0, Net: -3.0,
	}
	pos := []Event{
		{TS: base.Add(-10 * time.Minute), Type: eventPositionListResp, Symbol: "BTCUSDT", CumRealisedPnl: 10},
		{TS: base.Add(5 * time.Minute), Type: eventPositionListResp, Symbol: "BTCUSDT", CumRealisedPnl: 7},
	}
	c := buildDeltaOnlyCluster(rep, pos)
	if c == nil {
		t.Fatalf("expected delta-only cluster")
	}
	if c.RealisedDelta != -3 {
		t.Fatalf("unexpected realised delta: %.6f", c.RealisedDelta)
	}
}
