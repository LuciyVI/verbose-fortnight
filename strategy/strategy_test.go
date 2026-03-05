package strategy

import (
	"math"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...interface{})          {}
func (nopLogger) Info(string, ...interface{})           {}
func (nopLogger) Warning(string, ...interface{})        {}
func (nopLogger) Error(string, ...interface{})          {}
func (nopLogger) Fatal(string, ...interface{})          {}
func (nopLogger) Sync() error                           { return nil }
func (nopLogger) ChangeLogLevel(level logging.LogLevel) {}

func newTestTrader() *Trader {
	cfg := config.LoadConfig()
	state := &models.State{}
	state.Instr.TickSize = 0.1
	tr := NewTrader(api.NewRESTClient(cfg, nopLogger{}), cfg, state, nopLogger{})
	return tr
}

type beRuleOrderManagerStub struct{}

func (beRuleOrderManagerStub) PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error) {
	return "oid-test", nil
}

func (beRuleOrderManagerStub) PlaceTakeProfitOrder(side string, qty, price float64) error {
	return nil
}

type edgeOrderManagerSpy struct {
	mu    sync.Mutex
	calls int
}

func (s *edgeOrderManagerSpy) PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return "oid-edge", nil
}

func (s *edgeOrderManagerSpy) PlaceTakeProfitOrder(side string, qty, price float64) error {
	return nil
}

func (s *edgeOrderManagerSpy) marketCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type beRulePositionManagerStub struct {
	mu          sync.Mutex
	exists      bool
	side        string
	qty         float64
	tp          float64
	sl          float64
	entry       float64
	updateCalls int
}

func (s *beRulePositionManagerStub) HasOpenPosition() (bool, string, float64, float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exists, s.side, s.qty, s.tp, s.sl
}

func (s *beRulePositionManagerStub) NormalizeSide(side string) string {
	switch side {
	case "BUY", "Buy", "LONG", "Long", "long":
		return "LONG"
	case "SELL", "Sell", "SHORT", "Short", "short":
		return "SHORT"
	default:
		return ""
	}
}

func (s *beRulePositionManagerStub) GetLastEntryPrice() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entry
}

func (s *beRulePositionManagerStub) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tp = tp
	s.sl = sl
	s.updateCalls++
	return nil
}

func (s *beRulePositionManagerStub) GetLastBidPrice() float64 { return 0 }
func (s *beRulePositionManagerStub) GetLastAskPrice() float64 { return 0 }
func (s *beRulePositionManagerStub) CancelAllOrders(symbol string) {
}

func newPartialBERuleTestTrader() (*Trader, *beRulePositionManagerStub) {
	cfg := config.LoadConfig()
	state := &models.State{
		PositionState:  models.PositionStateOpen,
		StopIntentChan: make(chan models.StopIntent, 16),
		OrderTradeIDs:  make(map[string]string),
		ExecTradeIDs:   make(map[string]string),
		ProcessedExec:  make(map[string]time.Time),
		ExecDedupMax:   200,
		ExecDedupTTL:   time.Hour,
	}
	state.Instr.TickSize = 0.1
	state.MarketRegime = "range"
	pm := &beRulePositionManagerStub{
		exists: true,
		side:   "LONG",
		qty:    0.6,
		tp:     101,
		sl:     99,
		entry:  100,
	}
	tr := &Trader{
		Config:          cfg,
		State:           state,
		OrderManager:    beRuleOrderManagerStub{},
		PositionManager: pm,
		Logger:          nopLogger{},
	}
	return tr, pm
}

func newEdgeFilterTestTrader() (*Trader, *edgeOrderManagerSpy, *beRulePositionManagerStub) {
	cfg := config.LoadConfig()
	state := &models.State{
		PositionState:  models.PositionStateOpen,
		StopIntentChan: make(chan models.StopIntent, 16),
		OrderTradeIDs:  make(map[string]string),
		ExecTradeIDs:   make(map[string]string),
		ProcessedExec:  make(map[string]time.Time),
		ExecDedupMax:   200,
		ExecDedupTTL:   time.Hour,
		BidsMap:        map[string]float64{"99.9": 1},
		AsksMap:        map[string]float64{"100.1": 10},
	}
	state.Instr.TickSize = 0.1
	pm := &beRulePositionManagerStub{
		exists: true,
		side:   "SHORT",
		qty:    0.8,
		tp:     99,
		sl:     101,
		entry:  100,
	}
	om := &edgeOrderManagerSpy{}
	tr := &Trader{
		Config:          cfg,
		State:           state,
		OrderManager:    om,
		PositionManager: pm,
		Logger:          nopLogger{},
	}
	return tr, om, pm
}

func TestFeeBufferUsesFeeMult(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RoundTripFeePerc = 0.001
	tr.Config.FeeBufferMult = 2
	state := tr.State
	state.Instr.TickSize = 0.1

	buf := tr.feeBuffer(10000)
	if buf <= 0 {
		t.Fatalf("expected positive buffer")
	}
	if buf < 2 { // 0.1%*2 rounded up to tick
		t.Fatalf("buffer too small: %f", buf)
	}
}

func TestHigherTimeframeBiasLong(t *testing.T) {
	tr := newTestTrader()
	tr.Config.HTFWindow = 5
	tr.Config.HTFMaLen = 3
	tr.State.Closes = []float64{10, 10.2, 10.4, 10.6, 10.8}
	bias := tr.higherTimeframeBias(0.01)
	if bias != "LONG" {
		t.Fatalf("expected LONG bias, got %s", bias)
	}
}

func TestRoundTPSLDirectional(t *testing.T) {
	tr := newTestTrader()
	cases := []struct {
		name   string
		side   string
		tpIn   float64
		slIn   float64
		tick   float64
		wantTP float64
		wantSL float64
	}{
		{
			name:   "long_floor_tp_ceil_sl",
			side:   "LONG",
			tpIn:   100.74,
			slIn:   99.26,
			tick:   0.5,
			wantTP: 100.5,
			wantSL: 99.0,
		},
		{
			name:   "short_ceil_tp_floor_sl",
			side:   "SHORT",
			tpIn:   99.26,
			slIn:   100.74,
			tick:   0.5,
			wantTP: 99.5,
			wantSL: 101.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTP := tr.roundTP(100, tc.tpIn, tc.tick, tc.side)
			gotSL := tr.roundSL(100, tc.slIn, tc.tick, tc.side)
			if gotTP != tc.wantTP {
				t.Fatalf("tp rounding got %f want %f", gotTP, tc.wantTP)
			}
			if gotSL != tc.wantSL {
				t.Fatalf("sl rounding got %f want %f", gotSL, tc.wantSL)
			}
		})
	}
}

func TestRoundSLMovesAwayFromEntry(t *testing.T) {
	tr := newTestTrader()
	entry := 100.0
	tick := 0.5

	longSL := 99.26
	roundedLong := tr.roundSL(entry, longSL, tick, "LONG")
	if entry-roundedLong < entry-longSL {
		t.Fatalf("long SL rounding moved closer: before %.2f after %.2f", longSL, roundedLong)
	}

	shortSL := 100.74
	roundedShort := tr.roundSL(entry, shortSL, tick, "SHORT")
	if roundedShort-entry < shortSL-entry {
		t.Fatalf("short SL rounding moved closer: before %.2f after %.2f", shortSL, roundedShort)
	}
}

func TestCalculateInitialTPSLUsesFixedSLDistance(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.EnableTPSLStage1 = true
	tr.Config.SLPocketPerc = 0
	tr.Config.PocketFeeMult = 0
	tr.Config.SLFeeFloorMult = 0

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	entry := 100.03
	tp, sl := tr.calculateInitialTPSL(entry, "LONG")
	if tp <= entry {
		t.Fatalf("expected long TP above entry: tp %.4f entry %.4f", tp, entry)
	}

	wantSLRaw := entry - entry*fixedStopLossFraction
	wantSL := tr.roundSL(entry, wantSLRaw, tr.State.Instr.TickSize, "LONG")
	if sl != wantSL {
		t.Fatalf("sl not fixed at 0.25%%: got %.4f want %.4f (entry %.4f)", sl, wantSL, entry)
	}
}

func TestCalculateInitialTPSLIgnoresConfiguredSLPerc(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.EnableTPSLStage1 = true

	// Make fee floors/pocket small and deterministic.
	tr.Config.RoundTripFeePerc = 0.0000001
	tr.Config.FeeBufferMult = 1.0
	tr.Config.MinProfitPerc = 0.0
	tr.Config.TPFeeFloorMult = 1.0
	tr.Config.SLPocketPerc = 0.0
	tr.Config.PocketFeeMult = 2.0

	// ATR should be available.
	closes := []float64{100, 100.01, 100.02, 100.01, 100.03, 100.02, 100.01, 100.02, 100.03, 100.02, 100.01, 100.02, 100.03, 100.02, 100.01}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 0.01
		lows[i] = v - 0.01
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	entry := 100.0

	tr.Config.SlPerc = 0.01 // Should be ignored: SL must stay at fixed 0.25%.
	_, sl := tr.calculateInitialTPSL(entry, "LONG")
	wantSLRaw := entry - entry*fixedStopLossFraction
	wantSL := tr.roundSL(entry, wantSLRaw, tr.State.Instr.TickSize, "LONG")
	if sl != wantSL {
		t.Fatalf("sl must ignore configured sl perc: got %.4f want %.4f", sl, wantSL)
	}
}

func TestCalculateInitialTPSLRespectsFeeFloors(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.TPFeeFloorMult = 3.0
	tr.Config.SLFeeFloorMult = 2.5
	entry := 100.0

	tp, sl := tr.calculateInitialTPSL(entry, "LONG")
	feeBuf := tr.feeBuffer(entry)

	tpDist := math.Abs(tp - entry)
	slDist := math.Abs(entry - sl)

	if tpDist < feeBuf*tr.Config.TPFeeFloorMult {
		t.Fatalf("tpDist %.4f below fee floor %.4f", tpDist, feeBuf*tr.Config.TPFeeFloorMult)
	}
	if slDist+1e-9 < entry*fixedStopLossFraction {
		t.Fatalf("slDist %.4f below fixed 0.25%% floor %.4f", slDist, entry*fixedStopLossFraction)
	}
}

func TestMinPocketDistanceUsesMax(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.PocketFeeMult = 2.0

	cases := []struct {
		name       string
		entry      float64
		pocketPerc float64
		feePerc    float64
		want       float64
	}{
		{
			name:       "fee_buffer_wins",
			entry:      100,
			pocketPerc: 0.001,
			feePerc:    0.01,
			want:       2.0,
		},
		{
			name:       "pocket_perc_wins",
			entry:      100,
			pocketPerc: 0.01,
			feePerc:    0.0001,
			want:       1.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr.Config.SLPocketPerc = tc.pocketPerc
			tr.Config.RoundTripFeePerc = tc.feePerc
			tr.Config.FeeBufferMult = 1.0
			got := tr.minPocketDistance(tc.entry)
			if got != tc.want {
				t.Fatalf("minPocket got %f want %f", got, tc.want)
			}
		})
	}
}

func TestCalcSLToBE_Long(t *testing.T) {
	got := calcSLToBE("LONG", 100, 0.2)
	if math.Abs(got-100.2) > 1e-9 {
		t.Fatalf("calcSLToBE long got %.8f want 100.2", got)
	}
}

func TestCalcSLToBE_Short(t *testing.T) {
	got := calcSLToBE("SHORT", 100, 0.2)
	if math.Abs(got-99.8) > 1e-9 {
		t.Fatalf("calcSLToBE short got %.8f want 99.8", got)
	}
}

func TestCalcWinMovePct_LongShort(t *testing.T) {
	if got, ok := calcWinMovePct("LONG", 100, 101.5); !ok || math.Abs(got-0.015) > 1e-9 {
		t.Fatalf("long move pct got=%v ok=%v want=0.015,true", got, ok)
	}
	if got, ok := calcWinMovePct("SHORT", 100, 98.5); !ok || math.Abs(got-0.015) > 1e-9 {
		t.Fatalf("short move pct got=%v ok=%v want=0.015,true", got, ok)
	}
	if got, ok := calcWinMovePct("LONG", 100, 99.5); ok || got != 0 {
		t.Fatalf("loss move must not be accepted, got=%v ok=%v", got, ok)
	}
}

func TestClampSLPct(t *testing.T) {
	if got := clampSLPct(0.001, 0.0025, 0.03); math.Abs(got-0.0025) > 1e-12 {
		t.Fatalf("expected min clamp 0.0025, got %.8f", got)
	}
	if got := clampSLPct(0.04, 0.0025, 0.03); math.Abs(got-0.03) > 1e-12 {
		t.Fatalf("expected max clamp 0.03, got %.8f", got)
	}
	if got := clampSLPct(0.01, 0.0025, 0.03); math.Abs(got-0.01) > 1e-12 {
		t.Fatalf("expected passthrough 0.01, got %.8f", got)
	}
}

func TestSLPriceFromPct(t *testing.T) {
	if got := slPriceFromPct("LONG", 100, 0.005); math.Abs(got-99.5) > 1e-9 {
		t.Fatalf("long sl price got %.8f want 99.5", got)
	}
	if got := slPriceFromPct("SHORT", 100, 0.005); math.Abs(got-100.5) > 1e-9 {
		t.Fatalf("short sl price got %.8f want 100.5", got)
	}
}

func TestCalculateInitialTPSLUsesHalfLastWinSL(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.State.RecordLastWinMove("LONG", 0.01, 1, 100, 101, "test", time.Now())

	entry := 100.0
	_, sl, details := tr.calculateInitialTPSLWithDetails(entry, "LONG")
	if details.slPolicy != "half_last_win" {
		t.Fatalf("expected sl policy half_last_win, got %q", details.slPolicy)
	}
	if math.Abs(details.desiredSLPct-0.005) > 1e-12 {
		t.Fatalf("desired sl pct got %.8f want 0.005", details.desiredSLPct)
	}
	if math.Abs(sl-99.5) > 1e-9 {
		t.Fatalf("sl got %.8f want 99.5", sl)
	}
	if math.Abs(entry-sl) <= entry*fixedStopLossFraction {
		t.Fatalf("expected wider-than-floor SL, got dist %.6f floor %.6f", math.Abs(entry-sl), entry*fixedStopLossFraction)
	}
}

func TestHalfLastWinSkippedWhenLastWinBelowMin(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.State.RecordLastWinMove("LONG", 0.002, 1, 100, 100.2, "test", time.Now())

	_, sl, details := tr.calculateInitialTPSLWithDetails(100, "LONG")
	if details.slPolicy == "half_last_win" {
		t.Fatalf("expected fallback policy for small last win, got %q", details.slPolicy)
	}
	if details.slPolicy != "atr_default_small_win_skip" {
		t.Fatalf("expected atr_default_small_win_skip, got %q", details.slPolicy)
	}
	if details.slPolicyReason != "small_last_win" {
		t.Fatalf("expected reason small_last_win, got %q", details.slPolicyReason)
	}
	if details.slPctFinal+1e-12 < fixedStopLossFraction {
		t.Fatalf("expected final sl pct >= fixed floor, got %.8f", details.slPctFinal)
	}
	if math.Abs(sl-99.7) > 1e-9 {
		t.Fatalf("expected floor-based sl=99.7, got %.8f", sl)
	}
}

func TestHalfLastWinSkippedWhenClampViolatesInvariant(t *testing.T) {
	desired, final, reason, applied := resolveHalfLastWinSL(0.003, 0.0025, 0.03, 1.0)
	if applied {
		t.Fatalf("expected fallback when final>=last win")
	}
	if reason != "clamp_violation" {
		t.Fatalf("expected clamp_violation, got %q", reason)
	}
	if desired != 0.003 || final != 0.003 {
		t.Fatalf("unexpected desired/final: desired=%.8f final=%.8f", desired, final)
	}
}

func TestCalculateInitialTPSLFallbackWithoutLastWin(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"

	entry := 100.0
	_, sl, details := tr.calculateInitialTPSLWithDetails(entry, "LONG")
	if details.slPolicy != "atr_default" {
		t.Fatalf("expected fallback sl policy atr_default, got %q", details.slPolicy)
	}
	wantSLRaw := entry - entry*fixedStopLossFraction
	wantSL := tr.roundSL(entry, wantSLRaw, tr.State.Instr.TickSize, "LONG")
	if sl != wantSL {
		t.Fatalf("fallback sl mismatch: got %.4f want %.4f", sl, wantSL)
	}
}

func TestCalcEdgeScore_MinDepth(t *testing.T) {
	bids := map[string]float64{"100.0": 1}
	asks := map[string]float64{"100.1": 1}
	score, ok, reason := calcEdgeScore("LONG", bids, asks, 3, 10)
	if ok {
		t.Fatalf("expected min_depth reject, got ok=true score=%.4f reason=%s", score, reason)
	}
	if reason != "min_depth" {
		t.Fatalf("expected reason=min_depth, got %q", reason)
	}
}

func TestCalcEdgeScore_ImbalanceAndSpread(t *testing.T) {
	bids := map[string]float64{"100.0": 10, "99.9": 8}
	asks := map[string]float64{"100.1": 4, "100.2": 3}
	score, ok, reason := calcEdgeScore("LONG", bids, asks, 2, 0)
	if !ok {
		t.Fatalf("expected strong long edge, got ok=false score=%.4f reason=%s", score, reason)
	}
	if score <= 1.0 {
		t.Fatalf("expected score > 1.0 for strong imbalance, got %.4f", score)
	}

	bidsWide := map[string]float64{"100.0": 10}
	asksWide := map[string]float64{"101.0": 8}
	scoreWide, okWide, reasonWide := calcEdgeScore("LONG", bidsWide, asksWide, 1, 0)
	if okWide {
		t.Fatalf("expected wide_spread reject, got ok=true score=%.4f reason=%s", scoreWide, reasonWide)
	}
	if reasonWide != "wide_spread" {
		t.Fatalf("expected reason=wide_spread, got %q", reasonWide)
	}
}

func TestEdgeFilterFeatureGate(t *testing.T) {
	tr, om, pm := newEdgeFilterTestTrader()
	tr.Config.EnableEdgeFilter = false
	tr.State.PositionState = models.PositionStateOpen
	pm.exists = true
	pm.side = "SHORT"
	pm.qty = 0.8

	tr.handleDirectionalSignal("LONG", 100)
	if got := om.marketCalls(); got == 0 {
		t.Fatalf("expected close order to be placed when edge filter is off")
	}
	if tr.currentFSMState() != models.PositionStateClosing {
		t.Fatalf("expected FSM to move to CLOSING when edge filter is off, got %s", tr.currentFSMState())
	}
}

func TestEdgeFilterRejectsWeakEdgeOnEntry(t *testing.T) {
	tr, om, pm := newEdgeFilterTestTrader()
	tr.Config.EnableEdgeFilter = true
	tr.Config.OrderbookStrengthThreshold = 0.5
	tr.State.PositionState = models.PositionStateFlat
	tr.State.BidsMap = map[string]float64{"100.0": 1}
	tr.State.AsksMap = map[string]float64{"100.1": 10}
	pm.exists = false
	pm.side = ""
	pm.qty = 0

	tr.handleDirectionalSignal("LONG", 100)
	if got := om.marketCalls(); got != 0 {
		t.Fatalf("expected no order on weak edge entry, got %d", got)
	}
	if tr.currentFSMState() != models.PositionStateFlat {
		t.Fatalf("expected FSM to remain FLAT on edge reject, got %s", tr.currentFSMState())
	}
}

func TestPartialBERuleFeatureGate(t *testing.T) {
	tr, pm := newPartialBERuleTestTrader()
	tr.Config.EnablePartialBERule = false
	tr.State.ActiveTradeID = "trade-be-off"
	pm.tp, pm.sl = tr.calculateInitialTPSL(pm.entry, "LONG")

	evt := models.ExecutionEvent{
		TradeID:           "trade-be-off",
		ExecID:            "exec-be-off-1",
		OrderID:           "order-be-off-1",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               0.4,
		Price:             101.0,
		ClosedSize:        0.4,
		PositionSizeAfter: 0.6,
	}
	if !tr.ProcessExecutionEvent(evt, "ws") {
		t.Fatalf("expected execution to be processed")
	}
	select {
	case intent := <-tr.State.StopIntentChan:
		if intent.Kind == "MoveSLToBE" {
			t.Fatalf("MoveSLToBE intent must not be emitted when feature flag is off")
		}
	default:
	}
}

func TestPartialBERuleReduceFillMovesSLViaStopController(t *testing.T) {
	tr, pm := newPartialBERuleTestTrader()
	tr.Config.EnablePartialBERule = true
	tr.Config.SLPocketPerc = 0.0005
	tr.Config.PocketFeeMult = 1.0
	tr.Config.SLFeeFloorMult = 1.0
	tr.Config.RoundTripFeePerc = 0.001
	tr.State.ActiveTradeID = "trade-be-on"

	done := make(chan struct{})
	go func() {
		tr.StartStopController()
		close(done)
	}()

	evt := models.ExecutionEvent{
		TradeID:           "trade-be-on",
		ExecID:            "exec-be-on-1",
		OrderID:           "order-be-on-1",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               0.4,
		Price:             101.2,
		ClosedSize:        0.4,
		PositionSizeAfter: 0.6,
	}
	if !tr.ProcessExecutionEvent(evt, "ws") {
		t.Fatalf("expected execution to be processed")
	}

	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		pm.mu.Lock()
		applied := pm.updateCalls
		pm.mu.Unlock()
		if applied > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	pm.mu.Lock()
	applied := pm.updateCalls
	pm.mu.Unlock()
	if applied == 0 {
		t.Fatalf("expected stop controller to apply BE update")
	}

	tr.State.RLock()
	ctrl := tr.State.StopCtrl
	partialDone := tr.State.PartialTPDone
	tr.State.RUnlock()
	if !partialDone {
		t.Fatalf("PartialTPDone must be true after confirmed partial reduce fill")
	}
	if ctrl.Reason != "partial_tp" {
		t.Fatalf("expected stop reason partial_tp, got %q", ctrl.Reason)
	}
	if ctrl.DesiredSL <= pm.entry {
		t.Fatalf("expected DesiredSL above entry for LONG BE+pocket, got %.4f", ctrl.DesiredSL)
	}

	close(tr.State.StopIntentChan)
	<-done
}

func TestPartialBERuleDedupsMoveSLIntent(t *testing.T) {
	tr, _ := newPartialBERuleTestTrader()
	tr.Config.EnablePartialBERule = true
	tr.Config.SLPocketPerc = 0.0005
	tr.Config.PocketFeeMult = 1.0
	tr.Config.SLFeeFloorMult = 1.0
	tr.Config.RoundTripFeePerc = 0.001
	tr.State.ActiveTradeID = "trade-be-dedup"

	evt1 := models.ExecutionEvent{
		TradeID:           "trade-be-dedup",
		ExecID:            "exec-be-dedup-1",
		OrderID:           "order-be-dedup-1",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               0.4,
		Price:             101.0,
		ClosedSize:        0.4,
		PositionSizeAfter: 0.6,
	}
	if !tr.ProcessExecutionEvent(evt1, "ws") {
		t.Fatalf("expected first execution to be processed")
	}
	if got := len(tr.State.StopIntentChan); got != 1 {
		t.Fatalf("expected exactly one BE intent after first reduce fill, got %d", got)
	}

	evt2 := evt1
	evt2.ExecID = "exec-be-dedup-2"
	evt2.OrderID = "order-be-dedup-2"
	if !tr.ProcessExecutionEvent(evt2, "ws") {
		t.Fatalf("expected second execution to be processed")
	}
	if got := len(tr.State.StopIntentChan); got != 1 {
		t.Fatalf("expected BE intent dedup to keep queue size=1, got %d", got)
	}
	intent := <-tr.State.StopIntentChan
	if intent.Kind != "MoveSLToBE" {
		t.Fatalf("expected MoveSLToBE intent, got %q", intent.Kind)
	}

	counters, _ := tr.State.RuntimeSnapshot()
	if counters.BEIntentSent != 1 {
		t.Fatalf("expected BEIntentSent=1, got %d", counters.BEIntentSent)
	}
	if counters.BEIntentSkippedAlreadyBetter == 0 {
		t.Fatalf("expected BEIntentSkippedAlreadyBetter > 0 after duplicate reduce fills")
	}
}

func TestDynamicTPClamps(t *testing.T) {
	tr := newTestTrader()
	tr.Config.DynamicTPK = 1.0
	tr.Config.DynamicTPVolatilityFactor = 1.0
	tr.Config.DynamicTPMinPerc = 0.4
	tr.Config.DynamicTPMaxPerc = 1.8

	low := tr.dynamicTP(100, 0.01, "range") // ATR% = 0.01%
	if math.Abs(low.ClampedPercent-0.4) > 1e-9 {
		t.Fatalf("expected clamp to min: got %.6f", low.ClampedPercent)
	}

	high := tr.dynamicTP(100, 5.0, "range") // ATR% = 5%
	if math.Abs(high.ClampedPercent-1.8) > 1e-9 {
		t.Fatalf("expected clamp to max: got %.6f", high.ClampedPercent)
	}

	rangeMid := tr.dynamicTP(100, 0.5, "range") // ATR% = 0.5%
	trendMid := tr.dynamicTP(100, 0.5, "trend")
	if trendMid.RawPercent <= rangeMid.RawPercent {
		t.Fatalf("expected trend raw TP > range raw TP: trend %.6f range %.6f", trendMid.RawPercent, rangeMid.RawPercent)
	}
}

func TestTPSLInvariants(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.DynamicTPK = 1.0
	tr.Config.DynamicTPVolatilityFactor = 1.0
	tr.Config.DynamicTPMinPerc = 0.4
	tr.Config.DynamicTPMaxPerc = 1.8
	tr.Config.RoundTripFeePerc = 0.001
	tr.Config.FeeBufferMult = 1.0
	// No ATR available -> TP is clamped to min (then adjusted for pocket + rounding).
	tr.State.Closes = nil
	tr.State.Highs = nil
	tr.State.Lows = nil

	cases := []struct {
		name string
		side string
	}{
		{name: "long", side: "LONG"},
		{name: "short", side: "SHORT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := 100.0
			tp, sl := tr.calculateInitialTPSL(entry, tc.side)
			minPocket := tr.minPocketDistance(entry)
			tpDist := math.Abs(tp - entry)
			slDist := math.Abs(entry - sl)
			if slDist <= minPocket {
				t.Fatalf("sl pocket invariant failed: slDist %.4f <= minPocket %.4f", slDist, minPocket)
			}
			if tpDist <= 2*minPocket {
				t.Fatalf("tp pocket invariant failed: tpDist %.4f <= 2*minPocket %.4f", tpDist, 2*minPocket)
			}
			if slDist+1e-9 < entry*fixedStopLossFraction {
				t.Fatalf("sl fixed floor invariant failed: slDist %.4f < fixed floor %.4f", slDist, entry*fixedStopLossFraction)
			}
			if tc.side == "LONG" {
				if sl >= entry-minPocket {
					t.Fatalf("sl invariant failed: sl %f max %f", sl, entry-minPocket)
				}
			} else {
				if sl <= entry+minPocket {
					t.Fatalf("sl invariant failed: sl %f min %f", sl, entry+minPocket)
				}
			}
		})
	}
}

func TestReverseScenarioLongToShortRequiresFlatConfirm(t *testing.T) {
	tr := newTestTrader()
	tr.State.PositionState = models.PositionStateOpen
	tr.State.PosSide = "LONG"
	tr.State.OrderQty = 1
	tr.State.ActiveTradeID = "trade-long"

	if !tr.transitionFSM(models.PositionStateClosing, "test_reverse_close_intent", "trade-long") {
		t.Fatalf("expected OPEN -> CLOSING transition")
	}
	tr.queuePendingReverse("SHORT", 100, "trade-short")
	tr.syncFSMFromPositionSnapshot(false, "", 0)

	if tr.currentFSMState() != models.PositionStateFlat {
		t.Fatalf("expected FLAT after closing snapshot, got %s", tr.currentFSMState())
	}
	side, price, tradeID, ok := tr.popPendingReverseIfFlat()
	if !ok {
		t.Fatalf("expected pending reverse after FLAT confirmation")
	}
	if side != "SHORT" || price != 100 || tradeID != "trade-short" {
		t.Fatalf("unexpected pending reverse payload: side=%s price=%f tradeID=%s", side, price, tradeID)
	}
	if allowedFSMTransition(models.PositionStateOpen, models.PositionStateOpening) {
		t.Fatalf("OPEN -> OPENING must be disallowed")
	}
}

func TestPartialTPAndSLRemainderPlan(t *testing.T) {
	closeQty, remainder := partialClosePlan(10, 0.4)
	if closeQty != 4 || remainder != 6 {
		t.Fatalf("unexpected partial plan: close=%f remainder=%f", closeQty, remainder)
	}
	if err := validateStopInvariants("LONG", 100, 101, 99, false); err != nil {
		t.Fatalf("expected valid remainder stop setup, got err: %v", err)
	}
}

func TestBreakevenScenarioInvariant(t *testing.T) {
	if err := validateStopInvariants("LONG", 100, 101, 100, true); err != nil {
		t.Fatalf("breakeven long should be valid: %v", err)
	}
	if err := validateStopInvariants("LONG", 100, 101, 100.2, true); err != nil {
		t.Fatalf("be-pocket long should be valid: %v", err)
	}
	if err := validateStopInvariants("SHORT", 100, 99, 100, true); err != nil {
		t.Fatalf("breakeven short should be valid: %v", err)
	}
	if err := validateStopInvariants("SHORT", 100, 99, 99.8, true); err != nil {
		t.Fatalf("be-pocket short should be valid: %v", err)
	}
	if err := validateStopInvariants("LONG", 100, 101, 100, false); err == nil {
		t.Fatalf("sl==entry without breakeven must fail")
	}
}

func TestTPInversionPrevention(t *testing.T) {
	if err := validateStopInvariants("LONG", 100, 99, 98, false); err == nil {
		t.Fatalf("LONG with TP below entry must fail")
	}
	if err := validateStopInvariants("LONG", 100, 101, 101, false); err == nil {
		t.Fatalf("LONG with SL above entry must fail")
	}
	if err := validateStopInvariants("SHORT", 100, 101, 102, false); err == nil {
		t.Fatalf("SHORT with TP above entry must fail")
	}
	if err := validateStopInvariants("SHORT", 100, 99, 99, false); err == nil {
		t.Fatalf("SHORT with SL below entry must fail")
	}
}
