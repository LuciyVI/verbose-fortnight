package strategy

import (
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestCalcRRAfterFee(t *testing.T) {
	rr := calcRRAfterFee(3.0, 2.0, 0.5)
	if rr <= 0 {
		t.Fatalf("expected positive rr_after_fee, got %.6f", rr)
	}
	if rr >= 1.5 {
		t.Fatalf("rr_after_fee must be below raw rr=1.5, got %.6f", rr)
	}

	rrNegative := calcRRAfterFee(0.2, 1.0, 0.5)
	if rrNegative >= 0 {
		t.Fatalf("expected negative rr_after_fee when tp_net<=0, got %.6f", rrNegative)
	}
}

func TestCalcEntryEdgeGuardMetrics(t *testing.T) {
	bids := map[string]float64{"99": 10, "98": 8}
	asks := map[string]float64{"101": 2, "102": 2}

	res := calcEntryEdgeGuardMetrics("LONG", 3, bids, asks, 2, 0.001, 0.4)
	if !res.WouldBlock {
		t.Fatalf("expected would_block=true, got %+v", res)
	}
	if res.Reason == "ok" {
		t.Fatalf("expected non-ok reason, got %+v", res)
	}
	if res.LevelsUsed != 2 {
		t.Fatalf("levelsUsed=%d want=2", res.LevelsUsed)
	}

	resOk := calcEntryEdgeGuardMetrics("SHORT", 1, bids, asks, 1, 0.05, 0.9)
	if resOk.WouldBlock {
		t.Fatalf("expected no block, got %+v", resOk)
	}
}

func TestPlaceOrderWithExecutionPolicyFallbackToMarketWhenMakerUnsupported(t *testing.T) {
	cfg := config.LoadConfig()
	cfg.EnableMakerFirst = true
	state := &models.State{
		OrderTradeIDs: make(map[string]string),
		ExecTradeIDs:  make(map[string]string),
		ProcessedExec: make(map[string]time.Time),
	}
	om := &edgeOrderManagerSpy{}
	pm := &beRulePositionManagerStub{}
	tr := &Trader{
		Config:          cfg,
		State:           state,
		OrderManager:    om,
		PositionManager: pm,
		Logger:          nopLogger{},
	}

	if _, _, err := tr.placeOrderWithExecutionPolicy("Buy", 0.01, false, "lc-test", "entry"); err != nil {
		t.Fatalf("placeOrderWithExecutionPolicy error: %v", err)
	}
	if om.marketCalls() != 1 {
		t.Fatalf("expected fallback market call, got %d", om.marketCalls())
	}
}
