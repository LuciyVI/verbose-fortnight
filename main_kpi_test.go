package main

import (
	"testing"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestComputeKPISnapshot(t *testing.T) {
	counters := models.RuntimeCounters{
		MakerRatio:      0.4,
		FeeToGrossRatio: 0.3,
		AvgNetPerTrade:  1.2,
		EdgePass:        3,
		EdgeReject:      1,
		AvgWinDuration:  80,
		AvgLossDuration: 120,
	}
	s := computeKPISnapshot(counters)
	if s.WouldBlockRate != 0.25 {
		t.Fatalf("would_block_rate=%f want=0.25", s.WouldBlockRate)
	}
}

func TestEvaluateKPIViolations(t *testing.T) {
	cfg := &config.Config{
		KPIMinMakerRatio:    0.5,
		KPIMaxFeeToGross:    0.2,
		KPIMinNetPerTrade:   0.1,
		KPIMaxEdgeBlockRate: 0.3,
	}
	s := kpiSnapshot{
		MakerRatio:      0.2,
		FeeToGrossRatio: 0.5,
		AvgNetAfterFee:  -0.1,
		WouldBlockRate:  0.6,
	}
	violations := evaluateKPIViolations(cfg, s)
	if len(violations) != 4 {
		t.Fatalf("violations=%d want 4", len(violations))
	}
}
