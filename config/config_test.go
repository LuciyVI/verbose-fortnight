package config

import "testing"

func TestReversalLongDefaultsDisabled(t *testing.T) {
	t.Setenv("ORDERBOOK_SNAPSHOT_JSONL", "")
	t.Setenv("ORDERBOOK_SNAPSHOT_INTERVAL_MS", "")
	cfg := LoadConfig()
	rl := cfg.ReversalLong

	if cfg.OrderbookSnapshotJSONLPath != "" {
		t.Fatalf("OrderbookSnapshotJSONLPath default got %q want empty", cfg.OrderbookSnapshotJSONLPath)
	}
	if cfg.OrderbookSnapshotIntervalMS != 1000 {
		t.Fatalf("OrderbookSnapshotIntervalMS default got %d want 1000", cfg.OrderbookSnapshotIntervalMS)
	}
	if rl.Enabled {
		t.Fatalf("ReversalLong.Enabled default got true want false")
	}
	if rl.TradeEnabled {
		t.Fatalf("ReversalLong.TradeEnabled default got true want false")
	}
	if !rl.RequireLowerBBReclaim {
		t.Fatalf("RequireLowerBBReclaim default got false want true")
	}
	if !rl.RequireMACDHistRecovery {
		t.Fatalf("RequireMACDHistRecovery default got false want true")
	}
	if !rl.UseSoftOrderbookCheck {
		t.Fatalf("UseSoftOrderbookCheck default got false want true")
	}
	if !rl.AllowHTFBearish {
		t.Fatalf("AllowHTFBearish default got false want true")
	}
	if !rl.BlockExtremeHTFDowntrend {
		t.Fatalf("BlockExtremeHTFDowntrend default got false want true")
	}
	if rl.PositionSizeMultiplier != 0.3 {
		t.Fatalf("PositionSizeMultiplier default got %.2f want 0.30", rl.PositionSizeMultiplier)
	}
	if rl.TimeoutBars != 3 {
		t.Fatalf("TimeoutBars default got %d want 3", rl.TimeoutBars)
	}
	if rl.NoNewLowBars != 3 {
		t.Fatalf("NoNewLowBars default got %d want 3", rl.NoNewLowBars)
	}
	if rl.ScoreThreshold != 0.8 {
		t.Fatalf("ScoreThreshold default got %.2f want 0.80", rl.ScoreThreshold)
	}
	if rl.MaxSpreadBps != 20 {
		t.Fatalf("MaxSpreadBps default got %.2f want 20.00", rl.MaxSpreadBps)
	}
	if rl.MaxAskWallRatio != 3 {
		t.Fatalf("MaxAskWallRatio default got %.2f want 3.00", rl.MaxAskWallRatio)
	}
	if rl.MinBidStabilityRatio != 0.4 {
		t.Fatalf("MinBidStabilityRatio default got %.2f want 0.40", rl.MinBidStabilityRatio)
	}
	if rl.MinRuntimeStrength != 3 {
		t.Fatalf("MinRuntimeStrength default got %d want 3", rl.MinRuntimeStrength)
	}
	if !rl.RequireStop {
		t.Fatalf("RequireStop default got false want true")
	}
	if !rl.RequireSoftOrderbookAtRuntime {
		t.Fatalf("RequireSoftOrderbookAtRuntime default got false want true")
	}
	if rl.StopBelowLocalLowBps <= 0 {
		t.Fatalf("StopBelowLocalLowBps default got %.2f want positive", rl.StopBelowLocalLowBps)
	}
}

func TestReversalLongZeroValueStaysDisabled(t *testing.T) {
	var cfg Config
	if cfg.ReversalLong.Enabled {
		t.Fatalf("zero-value ReversalLong.Enabled got true want false")
	}
	if cfg.ReversalLong.TradeEnabled {
		t.Fatalf("zero-value ReversalLong.TradeEnabled got true want false")
	}
}
