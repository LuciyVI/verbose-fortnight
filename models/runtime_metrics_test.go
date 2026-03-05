package models

import (
	"math"
	"testing"
)

func TestMakerTakerCounters(t *testing.T) {
	state := &State{}

	state.RecordExecutionQuality(true, true, 0.25, 1.0, 0.75)
	state.RecordExecutionQuality(true, false, 0.15, -0.5, -0.65)
	state.RecordExecutionQuality(false, false, 0.10, 0, -0.10)

	counters, _ := state.RuntimeSnapshot()
	if counters.MakerFills != 1 {
		t.Fatalf("maker fills = %d want 1", counters.MakerFills)
	}
	if counters.TakerFills != 1 {
		t.Fatalf("taker fills = %d want 1", counters.TakerFills)
	}
	if math.Abs(counters.MakerRatio-0.5) > 1e-9 {
		t.Fatalf("maker ratio = %.6f want 0.5", counters.MakerRatio)
	}
}

func TestFeeToGrossRatio(t *testing.T) {
	state := &State{}
	state.RecordExecutionQuality(true, true, 0.25, 1.0, 0.75)
	state.RecordExecutionQuality(true, false, 0.15, -0.5, -0.65)
	state.RecordExecutionQuality(false, false, 0.10, 0, -0.10)

	counters, _ := state.RuntimeSnapshot()
	if math.Abs(counters.TotalExecFee-0.5) > 1e-9 {
		t.Fatalf("total exec fee = %.6f want 0.5", counters.TotalExecFee)
	}
	if math.Abs(counters.TotalGrossRealised-0.5) > 1e-9 {
		t.Fatalf("total gross realised = %.6f want 0.5", counters.TotalGrossRealised)
	}
	if math.Abs(counters.FeeToGrossRatio-1.0) > 1e-9 {
		t.Fatalf("fee_to_gross_ratio = %.6f want 1.0", counters.FeeToGrossRatio)
	}
}

func TestNetAggregation(t *testing.T) {
	state := &State{}
	state.RecordExecutionQuality(true, true, 0.2, 1.0, 0.8)
	state.RecordExecutionQuality(true, false, 0.1, -0.2, -0.3)

	counters, _ := state.RuntimeSnapshot()
	if math.Abs(counters.TotalNetRealised-0.5) > 1e-9 {
		t.Fatalf("total net realised = %.6f want 0.5", counters.TotalNetRealised)
	}

	state.RecordTradeOutcome(10, 2.0)
	state.RecordTradeOutcome(20, -1.0)
	state.RecordTradeOutcome(30, 3.0)

	counters, _ = state.RuntimeSnapshot()
	if math.Abs(counters.AvgTradeDuration-20.0) > 1e-9 {
		t.Fatalf("avg trade duration = %.6f want 20", counters.AvgTradeDuration)
	}
	if math.Abs(counters.AvgWinDuration-20.0) > 1e-9 {
		t.Fatalf("avg win duration = %.6f want 20", counters.AvgWinDuration)
	}
	if math.Abs(counters.AvgLossDuration-20.0) > 1e-9 {
		t.Fatalf("avg loss duration = %.6f want 20", counters.AvgLossDuration)
	}
	if math.Abs(counters.AvgNetPerTrade-(4.0/3.0)) > 1e-9 {
		t.Fatalf("avg_net_per_trade = %.6f want %.6f", counters.AvgNetPerTrade, 4.0/3.0)
	}
}
