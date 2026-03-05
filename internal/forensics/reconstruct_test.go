package forensics

import (
	"testing"
	"time"
)

func TestReconstructTradeFromCumRealisedPnlDeltaOnly(t *testing.T) {
	base := time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC)
	events := []Event{
		{
			TS:             base,
			Type:           EventPositionListResp,
			Symbol:         "BTCUSDT",
			Side:           "Buy",
			PositionSize:   0.013,
			CumRealisedPnl: 10.0,
		},
		{
			TS:             base.Add(5 * time.Minute),
			Type:           EventPositionListResp,
			Symbol:         "BTCUSDT",
			Side:           "Buy",
			PositionSize:   0.0,
			CumRealisedPnl: 7.5,
		},
	}
	trades, stats := ReconstructTrades(events, "BTCUSDT")
	if len(trades) != 1 {
		t.Fatalf("trades=%d want 1", len(trades))
	}
	if trades[0].Tier != "tier3" {
		t.Fatalf("tier=%s want tier3", trades[0].Tier)
	}
	if trades[0].Realised != -2.5 {
		t.Fatalf("realised=%.6f want -2.5", trades[0].Realised)
	}
	if stats.Tier3 != 1 {
		t.Fatalf("tier3=%d want 1", stats.Tier3)
	}
}

func TestReconstructTier1FromExecutionFill(t *testing.T) {
	base := time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC)
	isMaker := true
	events := []Event{
		{
			TS:           base,
			Type:         EventExecutionFill,
			Symbol:       "BTCUSDT",
			TraceKey:     "lc_1",
			LifecycleID:  "lc_1",
			OrderID:      "o_1",
			TradeID:      "t_1",
			PositionSide: "LONG",
			Qty:          0.013,
			Price:        64000,
			ReduceOnly:   false,
			ExecFee:      0.20,
			ExecPnl:      0,
			IsMaker:      &isMaker,
		},
		{
			TS:           base.Add(10 * time.Minute),
			Type:         EventExecutionFill,
			Symbol:       "BTCUSDT",
			TraceKey:     "lc_1",
			LifecycleID:  "lc_1",
			OrderID:      "o_2",
			TradeID:      "t_2",
			PositionSide: "LONG",
			Qty:          0.013,
			Price:        64200,
			ClosedSize:   0.013,
			ReduceOnly:   true,
			ExecFee:      0.21,
			ExecPnl:      2.6,
			IsMaker:      &isMaker,
		},
	}
	trades, stats := ReconstructTrades(events, "BTCUSDT")
	if len(trades) != 1 {
		t.Fatalf("trades=%d want 1", len(trades))
	}
	if trades[0].Tier != "tier1" {
		t.Fatalf("tier=%s want tier1", trades[0].Tier)
	}
	if trades[0].MakerFills == 0 {
		t.Fatalf("expected maker fills > 0")
	}
	if stats.Tier1 != 1 {
		t.Fatalf("tier1=%d want 1", stats.Tier1)
	}
}
