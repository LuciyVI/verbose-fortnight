package strategy

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
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

func setTestOhlcv(state *models.State, closes, highs, lows []float64) {
	state.OhlcvLock.Lock()
	defer state.OhlcvLock.Unlock()

	state.Closes = append([]float64(nil), closes...)
	state.Highs = append([]float64(nil), highs...)
	state.Lows = append([]float64(nil), lows...)
	state.Volumes = nil
	state.HTFCloses = append([]float64(nil), closes...)
	state.CandleSeq.Store(uint64(len(closes)))
}

func newNoPositionServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/v5/position/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"side":"Buy","size":"0","takeProfit":"","stopLoss":"","avgPrice":"0"}]}}`))
	})
	mux.HandleFunc("/v5/order/create", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected order placement", http.StatusInternalServerError)
	})
	return httptest.NewServer(mux)
}

type executionTestExchange struct {
	t            *testing.T
	server       *httptest.Server
	mu           sync.Mutex
	positionSide string
	closeCount   int
	openCount    int
}

func newExecutionTestExchange(t *testing.T, initialSide string) *executionTestExchange {
	t.Helper()

	ex := &executionTestExchange{
		t:            t,
		positionSide: initialSide,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v5/position/list", ex.handlePositionList)
	mux.HandleFunc("/v5/account/wallet-balance", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"totalAvailableBalance":"100000"}]}}`))
	})
	mux.HandleFunc("/v5/order/create", ex.handleOrderCreate)
	mux.HandleFunc("/v5/position/trading-stop", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK"}`))
	})
	ex.server = httptest.NewServer(mux)
	return ex
}

func (e *executionTestExchange) close() {
	e.server.Close()
}

func (e *executionTestExchange) handlePositionList(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	side := e.positionSide
	e.mu.Unlock()

	switch side {
	case "LONG":
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"side":"Buy","size":"0.01","takeProfit":"120","stopLoss":"90","avgPrice":"100"}]}}`))
	case "SHORT":
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"side":"Sell","size":"0.01","takeProfit":"80","stopLoss":"110","avgPrice":"100"}]}}`))
	default:
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"side":"Buy","size":"0","takeProfit":"","stopLoss":"","avgPrice":"0"}]}}`))
	}
}

func (e *executionTestExchange) handleOrderCreate(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		e.t.Fatalf("unmarshal order body: %v", err)
	}

	side, _ := payload["side"].(string)
	reduceOnly, _ := payload["reduceOnly"].(bool)

	e.mu.Lock()
	defer e.mu.Unlock()
	if reduceOnly {
		e.closeCount++
		e.positionSide = ""
	} else {
		e.openCount++
		if side == "Buy" {
			e.positionSide = "LONG"
		} else if side == "Sell" {
			e.positionSide = "SHORT"
		}
	}

	_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK"}`))
}

func (e *executionTestExchange) counts() (int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closeCount, e.openCount
}

func newExecutionTestTrader(t *testing.T, initialSide string) (*Trader, *executionTestExchange) {
	t.Helper()

	ex := newExecutionTestExchange(t, initialSide)
	tr := newTestTrader()
	tr.Config.DemoRESTHost = ex.server.URL
	tr.Config.APIKey = "k"
	tr.Config.APISecret = "s"
	tr.Config.Symbol = "BTCUSDT"
	tr.Config.OrderBalancePct = 0.001
	tr.Config.SLSetDelaySec = 0
	tr.State.Instr = models.InstrumentInfo{
		MinNotional: 0,
		MinQty:      0.001,
		QtyStep:     0.001,
		TickSize:    0.1,
	}
	tr.State.BidsMap = map[string]float64{"99": 1}
	tr.State.AsksMap = map[string]float64{"101": 1}
	return tr, ex
}

func testSignal(direction string, candleSeq uint64) models.Signal {
	return models.Signal{
		Kind:       direction,
		Direction:  direction,
		Strength:   3,
		ClosePrice: 100,
		CandleSeq:  candleSeq,
		Time:       time.Now(),
	}
}

func reversalRuntimeSignal(candleSeq uint64) models.Signal {
	sig := testSignal("LONG", candleSeq)
	sig.Mode = models.ModeReversalLong
	sig.Debug = models.SignalDebug{
		LongScore:                      sig.Strength,
		ChosenDirection:                "LONG",
		Reasons:                        []string{models.SignalReasonReversalLong, ReversalReasonPipelineSelected},
		ReversalScore:                  0.95,
		ReversalState:                  string(RevCandidate),
		ReversalLowestLow:              99,
		ReversalStopPrice:              98.7,
		ReversalTimeoutBars:            3,
		ReversalPositionSizeMultiplier: 0.3,
		ReversalSoftOrderbookAllowed:   true,
		ReversalHTFOverrideAllowed:     true,
	}
	return sig
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func longBreakoutSnapshot(rsi float64, htfBias string) models.IndicatorSnapshot {
	return models.IndicatorSnapshot{
		Close:      105,
		SMA:        100,
		RSI:        rsi,
		MACDLine:   2,
		MACDSignal: 1,
		MACDHist:   1,
		BBUpper:    104,
		BBMiddle:   100,
		BBLower:    96,
		HTFBias:    htfBias,
	}
}

func passingOrderbookResult(side string) models.OrderbookResult {
	return models.OrderbookResult{
		Evaluated: true,
		Passed:    true,
		Ratio:     1.3,
		Side:      side,
		Threshold: 1.2,
		History:   strings.ToLower(side),
		Reason:    "ratio 1.30 ok",
	}
}

func TestChooseSignalDirectionTieDoesNotSelectDirection(t *testing.T) {
	strengths := []map[string]int{
		{"LONG": 2, "SHORT": 2},
		{"SHORT": 2, "LONG": 2},
	}

	for i := 0; i < 1000; i++ {
		dir, val, reason := chooseSignalDirection(strengths[i%len(strengths)])
		if dir != "" {
			t.Fatalf("tie selected direction %q", dir)
		}
		if val != 0 {
			t.Fatalf("tie returned strength %d, want 0", val)
		}
		if reason != tieStrengthNoSignalReason {
			t.Fatalf("tie reason got %q want %q", reason, tieStrengthNoSignalReason)
		}
	}
}

func TestChooseSignalDirectionSelectsStrictlyStrongerSide(t *testing.T) {
	cases := []struct {
		name     string
		strength map[string]int
		wantDir  string
		wantVal  int
	}{
		{
			name:     "long_stronger",
			strength: map[string]int{"LONG": 3, "SHORT": 2},
			wantDir:  "LONG",
			wantVal:  3,
		},
		{
			name:     "short_stronger",
			strength: map[string]int{"LONG": 1, "SHORT": 2},
			wantDir:  "SHORT",
			wantVal:  2,
		},
		{
			name:     "no_strength",
			strength: map[string]int{"LONG": 0, "SHORT": 0},
			wantDir:  "",
			wantVal:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, val, reason := chooseSignalDirection(tc.strength)
			if dir != tc.wantDir {
				t.Fatalf("dir got %q want %q", dir, tc.wantDir)
			}
			if val != tc.wantVal {
				t.Fatalf("strength got %d want %d", val, tc.wantVal)
			}
			if reason != "" {
				t.Fatalf("unexpected reason %q", reason)
			}
		})
	}
}

func TestRSIPolicyLogModeDoesNotBlockSignal(t *testing.T) {
	decision := evaluateRSIPolicy(50, 45, 55, config.RSIModeLog)

	if decision.Mode != config.RSIModeLog {
		t.Fatalf("mode got %q want %q", decision.Mode, config.RSIModeLog)
	}
	if !decision.AllowLong || !decision.AllowShort {
		t.Fatalf("log mode should not block: %+v", decision)
	}
	if decision.LongScore != 0 || decision.ShortScore != 0 {
		t.Fatalf("log mode should not score: %+v", decision)
	}
	if decision.Impact != "log_only" {
		t.Fatalf("impact got %q want log_only", decision.Impact)
	}
}

func TestRSIPolicyFilterModeBlocksWhenThresholdsDoNotMatch(t *testing.T) {
	longBlocked := evaluateRSIPolicy(60, 45, 55, config.RSIModeFilter)
	if longBlocked.AllowLong {
		t.Fatalf("filter mode should block LONG when RSI is above low threshold: %+v", longBlocked)
	}
	if !longBlocked.AllowShort {
		t.Fatalf("filter mode should allow SHORT when RSI is above high threshold: %+v", longBlocked)
	}

	shortBlocked := evaluateRSIPolicy(40, 45, 55, config.RSIModeFilter)
	if !shortBlocked.AllowLong {
		t.Fatalf("filter mode should allow LONG when RSI is below low threshold: %+v", shortBlocked)
	}
	if shortBlocked.AllowShort {
		t.Fatalf("filter mode should block SHORT when RSI is below high threshold: %+v", shortBlocked)
	}
}

func TestRSIPolicyScoreModeChangesStrengthWithoutHardBlock(t *testing.T) {
	decision := evaluateRSIPolicy(60, 45, 55, config.RSIModeScore)
	if !decision.AllowLong || !decision.AllowShort {
		t.Fatalf("score mode should not hard-block either side: %+v", decision)
	}
	if decision.ShortScore != 1 || decision.ShortContrib != "rsi-overbought" {
		t.Fatalf("score mode should score SHORT overbought RSI: %+v", decision)
	}

	strength := map[string]int{"LONG": 2, "SHORT": 2}
	contrib := map[string][]string{"LONG": {}, "SHORT": {}}
	applyRSIScore(strength, contrib, decision)

	if strength["LONG"] != 2 {
		t.Fatalf("LONG strength changed in score mode: got %d want 2", strength["LONG"])
	}
	if strength["SHORT"] != 3 {
		t.Fatalf("SHORT strength got %d want 3", strength["SHORT"])
	}
	if len(contrib["SHORT"]) != 1 || contrib["SHORT"][0] != "rsi-overbought" {
		t.Fatalf("SHORT contribs got %v want [rsi-overbought]", contrib["SHORT"])
	}
}

func TestRSIPolicyOffModeDoesNotAffectSignal(t *testing.T) {
	decision := evaluateRSIPolicy(60, 45, 55, config.RSIModeOff)
	if !decision.AllowLong || !decision.AllowShort {
		t.Fatalf("off mode should not block: %+v", decision)
	}
	if decision.LongScore != 0 || decision.ShortScore != 0 {
		t.Fatalf("off mode should not score: %+v", decision)
	}

	strength := map[string]int{"LONG": 2, "SHORT": 2}
	contrib := map[string][]string{"LONG": {}, "SHORT": {}}
	applyRSIScore(strength, contrib, decision)

	if strength["LONG"] != 2 || strength["SHORT"] != 2 {
		t.Fatalf("off mode changed strength: %v", strength)
	}
	if len(contrib["LONG"]) != 0 || len(contrib["SHORT"]) != 0 {
		t.Fatalf("off mode changed contribs: %v", contrib)
	}
}

func TestScoreSignalDoesNotApplyRSIOrHTFFilters(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RSIMode = config.RSIModeFilter
	tr.Config.RSILow = 45
	tr.Config.RSIHigh = 55
	tr.State.SetMarketRegime("trend", "trend", 0)

	scored := tr.ScoreSignal(longBreakoutSnapshot(60, "SHORT"), 7, []float64{100, 102, 105})

	if scored.LongScore != 3 {
		t.Fatalf("long score got %d want 3, reasons=%v", scored.LongScore, scored.LongReasons)
	}
	if scored.ShortScore != 0 {
		t.Fatalf("short score got %d want 0, reasons=%v", scored.ShortScore, scored.ShortReasons)
	}
	if scored.RSIDecision.Mode != config.RSIModeFilter || scored.RSIDecision.AllowLong {
		t.Fatalf("expected RSI filter to block LONG only at filter stage: %+v", scored.RSIDecision)
	}
	if !containsString(scored.LongReasons, "sma-breakout") || !containsString(scored.LongReasons, "macd-trend") || !containsString(scored.LongReasons, "bb-breakout") {
		t.Fatalf("missing scoring reasons: %v", scored.LongReasons)
	}
}

func TestFilterSignalDoesNotMutateScore(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RSIMode = config.RSIModeFilter
	tr.Config.RSILow = 45
	tr.Config.RSIHigh = 55
	tr.State.SetMarketRegime("trend", "trend", 0)

	scored := tr.ScoreSignal(longBreakoutSnapshot(60, ""), 1, []float64{100, 102, 105})
	filtered := tr.FilterSignal(scored, func(side string, lastPrice float64) models.OrderbookResult {
		t.Fatalf("orderbook should not be evaluated when RSI already rejects the only scored side")
		return models.OrderbookResult{}
	})

	if filtered.Accepted {
		t.Fatalf("expected RSI filter to reject signal")
	}
	if scored.LongScore != 3 || filtered.Signal.Debug.LongScore != 3 {
		t.Fatalf("filter changed long score: scored=%d debug=%d", scored.LongScore, filtered.Signal.Debug.LongScore)
	}
	if filtered.Signal.Strength != 0 {
		t.Fatalf("rejected filtered signal strength got %d want 0", filtered.Signal.Strength)
	}
	if !containsString(filtered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("missing RSI applied filter: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, filterRSIConfirmation) {
		t.Fatalf("missing RSI rejectedBy: %+v", filtered.Signal.Debug)
	}
}

func TestFilterSignalRejectedSignalContainsRejectedBy(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RSIMode = config.RSIModeLog
	tr.State.SetMarketRegime("trend", "trend", 0)

	scored := tr.ScoreSignal(longBreakoutSnapshot(50, ""), 1, []float64{100, 102, 105})
	filtered := tr.FilterSignal(scored, func(side string, lastPrice float64) models.OrderbookResult {
		return models.OrderbookResult{
			Evaluated: true,
			Passed:    false,
			Ratio:     0.8,
			Side:      side,
			Threshold: 1.2,
			Reason:    "ratio 0.80 below 1.20",
		}
	})

	if filtered.Accepted {
		t.Fatalf("expected orderbook rejection")
	}
	if filtered.Signal.Orderbook == nil || filtered.Signal.Orderbook.Passed {
		t.Fatalf("expected rejected orderbook on signal: %+v", filtered.Signal.Orderbook)
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("missing orderbook rejectedBy: %+v", filtered.Signal.Debug)
	}
}

func TestEmitSignalContainsFullReasonDebug(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RSIMode = config.RSIModeFilter
	tr.Config.RSILow = 45
	tr.Config.RSIHigh = 55
	tr.State.SetMarketRegime("trend", "trend", 0)
	tr.State.SigChan = make(chan models.Signal, 1)

	scored := tr.ScoreSignal(longBreakoutSnapshot(40, "LONG"), 5, []float64{100, 102, 105})
	filtered := tr.FilterSignal(scored, func(side string, lastPrice float64) models.OrderbookResult {
		return passingOrderbookResult(side)
	})
	if !filtered.Accepted {
		t.Fatalf("expected filtered signal to pass: %+v", filtered.Signal.Debug)
	}
	if !tr.EmitSignal(filtered) {
		t.Fatalf("expected signal to be emitted")
	}

	sig := <-tr.State.SigChan
	if sig.Debug.LongScore != 3 || sig.Debug.ShortScore != 0 {
		t.Fatalf("debug scores got L/S=%d/%d want 3/0", sig.Debug.LongScore, sig.Debug.ShortScore)
	}
	if sig.Debug.ChosenDirection != "LONG" {
		t.Fatalf("chosen direction got %q want LONG", sig.Debug.ChosenDirection)
	}
	for _, filterName := range []string{filterRSIConfirmation, filterHTFConfirmation, filterOrderbookConfirmation} {
		if !containsString(sig.Debug.AppliedFilters, filterName) {
			t.Fatalf("missing applied filter %s in %+v", filterName, sig.Debug)
		}
	}
	if len(sig.Debug.RejectedBy) != 0 {
		t.Fatalf("emitted signal should not be rejected: %+v", sig.Debug)
	}
	if !containsString(sig.Debug.Reasons, "LONG:sma-breakout") {
		t.Fatalf("missing score reason in debug: %+v", sig.Debug)
	}

	tr.State.StatusLock.RLock()
	last := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if last.Debug.ChosenDirection != "LONG" || !containsString(last.Debug.AppliedFilters, filterOrderbookConfirmation) {
		t.Fatalf("last signal snapshot missing debug context: %+v", last.Debug)
	}
}

func TestSignalModeCarriedToStatusSnapshotWhenSet(t *testing.T) {
	tr := newTestTrader()
	sig := models.Signal{
		Kind:       "LONG",
		Direction:  "LONG",
		Mode:       models.ModeReversalLong,
		Strength:   1,
		ClosePrice: 100,
		Time:       time.Now(),
		Debug: models.SignalDebug{
			ChosenDirection: "LONG",
			Reasons:         []string{models.SignalReasonReversalLong},
		},
	}

	tr.updateLastSignalSnapshot(sig)

	tr.State.StatusLock.RLock()
	last := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if last.Mode != models.ModeReversalLong {
		t.Fatalf("snapshot mode got %q want %q", last.Mode, models.ModeReversalLong)
	}
	if !containsString(last.Debug.Reasons, models.SignalReasonReversalLong) {
		t.Fatalf("snapshot missing reversal reason: %+v", last.Debug)
	}
}

func TestReversalLongPositionSizeMultiplier(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderBalancePct = 0.001
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.PositionSizeMultiplier = 0.3
	tr.State.Instr = models.InstrumentInfo{MinQty: 0.001, QtyStep: 0.001}

	ordinary, err := tr.calculatePositionSize(100, 100000, testSignal("LONG", 1))
	if err != nil {
		t.Fatalf("ordinary size error: %v", err)
	}
	reversal, err := tr.calculatePositionSize(100, 100000, reversalRuntimeSignal(1))
	if err != nil {
		t.Fatalf("reversal size error: %v", err)
	}
	if ordinary.FinalQty != ordinary.NormalQty {
		t.Fatalf("ordinary size changed: %+v", ordinary)
	}
	want := 0.3
	if math.Abs(reversal.FinalQty-want) > 1e-9 {
		t.Fatalf("reversal final qty got %.8f want %.8f decision=%+v", reversal.FinalQty, want, reversal)
	}
	if reversal.FinalQty >= ordinary.FinalQty {
		t.Fatalf("reversal size not reduced ordinary=%+v reversal=%+v", ordinary, reversal)
	}
}

func TestReversalLongInvalidPositionSizeMultiplierUsesSafeDefault(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderBalancePct = 0.001
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.PositionSizeMultiplier = 2
	tr.State.Instr = models.InstrumentInfo{MinQty: 0.001, QtyStep: 0.001}

	decision, err := tr.calculatePositionSize(100, 100000, reversalRuntimeSignal(1))
	if err != nil {
		t.Fatalf("size error: %v", err)
	}
	if !decision.InvalidMultiplier {
		t.Fatalf("expected invalid multiplier flag: %+v", decision)
	}
	if math.Abs(decision.Multiplier-0.3) > 1e-9 || math.Abs(decision.FinalQty-0.3) > 1e-9 {
		t.Fatalf("invalid multiplier did not use safe default: %+v", decision)
	}
}

func TestReversalLongStopRequiredBlocksMissingLocalLow(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.State.SigChan = make(chan models.Signal, 1)

	sig := reversalRuntimeSignal(201)
	sig.Debug.ReversalLowestLow = 0
	sig.Debug.ReversalStopPrice = 0
	runTraderWithSignal(tr, sig)

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("missing local low opened reversal position")
	}
	tr.State.StatusLock.RLock()
	last := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if last.Mode != models.ModeReversalLong {
		t.Fatalf("mode lost in runtime rejection: %+v", last)
	}
	if !containsString(last.Debug.RejectedBy, filterReversalRiskProfile) {
		t.Fatalf("missing reversal risk rejection: %+v", last.Debug)
	}
	if !containsString(last.Debug.Reasons, ReversalReasonLocalLowNoStop) {
		t.Fatalf("missing local-low stop reason: %+v", last.Debug)
	}
}

func TestReversalLongRuntimeTradeDisabledBlocksManualSignal(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = false
	tr.State.SigChan = make(chan models.Signal, 1)

	runTraderWithSignal(tr, reversalRuntimeSignal(202))

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("TradeEnabled=false opened reversal position")
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterReversalRiskProfile) {
		t.Fatalf("missing reversal trade gate rejection: %+v", debug)
	}
	if !containsString(debug.Reasons, ReversalReasonTradeDisabled) {
		t.Fatalf("missing trade disabled reason: %+v", debug)
	}
}

func TestReversalLongStopBelowLocalLow(t *testing.T) {
	tr := newTestTrader()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.State.Instr.TickSize = 0.1

	risk := tr.reversalLongRuntimeRisk(reversalRuntimeSignal(1), 100)
	if !risk.StopValid {
		t.Fatalf("expected valid reversal stop: %+v", risk)
	}
	if risk.StopPrice >= risk.LocalLow {
		t.Fatalf("stop %.8f should be below local low %.8f", risk.StopPrice, risk.LocalLow)
	}
}

func TestReversalLongSLCannotBeLoosenedBeyondLocalLowStop(t *testing.T) {
	tr := newTestTrader()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.State.Instr.TickSize = 0.1
	sig := reversalRuntimeSignal(1)
	risk := tr.reversalLongRuntimeRisk(sig, 100)
	if !risk.StopValid {
		t.Fatalf("expected valid reversal stop: %+v", risk)
	}

	normalTP, normalSL, _ := tr.calculateInitialTPSLWithDiagnostics(100, "LONG", tpslDiagnosticContext{})
	diag := tr.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
		Symbol: "BTCUSDT",
		Reason: "open_position_reversal",
		Qty:    1,
	}, "LONG", 1, 100, normalTP, risk.StopPrice)
	if diag.FinalSL != risk.StopPrice {
		t.Fatalf("reversal SL got %.8f want local-low stop %.8f normalSL=%.8f", diag.FinalSL, risk.StopPrice, normalSL)
	}
	if diag.FinalSL < risk.StopPrice {
		t.Fatalf("reversal SL loosened below local-low stop: %.8f < %.8f", diag.FinalSL, risk.StopPrice)
	}
}

func TestExecuteConfirmedLongSignalClosesShortOnceAndOpensOnce(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "SHORT")
	defer ex.close()

	action := tr.executeConfirmedSignal(testSignal("LONG", 101), "LONG", map[string]struct{}{})
	if action != "LONG" {
		t.Fatalf("action got %q want LONG", action)
	}

	closeCount, openCount := ex.counts()
	if closeCount != 1 {
		t.Fatalf("close count got %d want 1", closeCount)
	}
	if openCount != 1 {
		t.Fatalf("open count got %d want 1", openCount)
	}
}

func TestExecuteConfirmedShortSignalClosesLongOnceAndOpensOnce(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "LONG")
	defer ex.close()

	action := tr.executeConfirmedSignal(testSignal("SHORT", 102), "SHORT", map[string]struct{}{})
	if action != "SHORT" {
		t.Fatalf("action got %q want SHORT", action)
	}

	closeCount, openCount := ex.counts()
	if closeCount != 1 {
		t.Fatalf("close count got %d want 1", closeCount)
	}
	if openCount != 1 {
		t.Fatalf("open count got %d want 1", openCount)
	}
}

func TestExecuteConfirmedLongSignalWithoutPositionDoesNotClose(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()

	action := tr.executeConfirmedSignal(testSignal("LONG", 103), "LONG", map[string]struct{}{})
	if action != "LONG" {
		t.Fatalf("action got %q want LONG", action)
	}

	closeCount, openCount := ex.counts()
	if closeCount != 0 {
		t.Fatalf("close count got %d want 0", closeCount)
	}
	if openCount != 1 {
		t.Fatalf("open count got %d want 1", openCount)
	}
}

func TestExecuteConfirmedSameSideSignalDoesNotCloseOpposite(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "LONG")
	defer ex.close()

	action := tr.executeConfirmedSignal(testSignal("LONG", 104), "LONG", map[string]struct{}{})
	if action != "LONG" {
		t.Fatalf("action got %q want LONG", action)
	}

	closeCount, openCount := ex.counts()
	if closeCount != 0 {
		t.Fatalf("close count got %d want 0", closeCount)
	}
	if openCount != 0 {
		t.Fatalf("open count got %d want 0", openCount)
	}
}

func TestExecuteConfirmedSignalSkipsDuplicateReversalCloseForSameCandle(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "SHORT")
	defer ex.close()

	sig := testSignal("LONG", 105)
	keys := map[string]struct{}{
		signalReversalKey(sig, "LONG"): {},
	}
	action := tr.executeConfirmedSignal(sig, "LONG", keys)
	if action != "" {
		t.Fatalf("action got %q want empty for duplicate reversal", action)
	}

	closeCount, openCount := ex.counts()
	if closeCount != 0 {
		t.Fatalf("close count got %d want 0", closeCount)
	}
	if openCount != 0 {
		t.Fatalf("open count got %d want 0", openCount)
	}
}

func TestReversalLongRuntimeUsesSoftOrderbookInsteadOfStrictFallback(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.State.BidsMap = map[string]float64{"99.95": 1}
	tr.State.AsksMap = map[string]float64{"100.05": 1}

	runTraderWithSignal(tr, reversalRuntimeSignal(301))

	_, openCount := ex.counts()
	if openCount != 1 {
		t.Fatalf("soft orderbook should allow reversal despite strict ratio below threshold, openCount=%d", openCount)
	}
	tr.State.StatusLock.RLock()
	last := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if last.Mode != models.ModeReversalLong {
		t.Fatalf("reversal mode was not propagated to runtime LastSignal: %+v", last)
	}
	if !containsString(last.Debug.AppliedFilters, filterOrderbookConfirmation) {
		t.Fatalf("runtime did not apply orderbook gate: %+v", last.Debug)
	}
	if last.Debug.ReversalStopPrice <= 0 || last.Debug.ReversalLowestLow <= 0 {
		t.Fatalf("runtime reversal signal missing stop metadata: %+v", last.Debug)
	}
}

func TestOrdinaryLongRuntimeStillUsesStrictOrderbookFallback(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.State.BidsMap = map[string]float64{"99.95": 1}
	tr.State.AsksMap = map[string]float64{"100.05": 1}

	runTraderWithSignal(tr, testSignal("LONG", 302))

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("ordinary LONG bypassed strict orderbook fallback")
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("ordinary LONG missing strict orderbook rejection: %+v", debug)
	}
}

func TestReversalLongRuntimeHostileOrderbookBlocks(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.State.BidsMap = map[string]float64{"99.95": 1}
	tr.State.AsksMap = map[string]float64{"100.05": 10}

	runTraderWithSignal(tr, reversalRuntimeSignal(303))

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("hostile orderbook opened reversal position")
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("missing orderbook rejection: %+v", debug)
	}
	if !containsString(debug.Reasons, ReversalReasonAskWallTooLarge) {
		t.Fatalf("missing hostile orderbook reason: %+v", debug)
	}
}

func TestReversalLongRuntimeOrderbookUnavailableBlocksWhenRequired(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.Config.ReversalLong.RequireSoftOrderbookAtRuntime = true
	tr.State.BidsMap = nil
	tr.State.AsksMap = nil

	runTraderWithSignal(tr, reversalRuntimeSignal(305))

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("unavailable runtime orderbook opened reversal position")
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("missing orderbook rejection for unavailable reversal book: %+v", debug)
	}
	if !containsString(debug.Reasons, ReversalReasonRuntimeOBUnavailable) {
		t.Fatalf("missing unavailable orderbook reason: %+v", debug)
	}
}

func TestReversalLongRuntimeOrderbookUnavailableAllowedWhenNotRequired(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.Config.ReversalLong.RequireSoftOrderbookAtRuntime = false
	tr.State.BidsMap = nil
	tr.State.AsksMap = nil

	runTraderWithSignal(tr, reversalRuntimeSignal(306))

	_, openCount := ex.counts()
	if openCount != 1 {
		t.Fatalf("unavailable runtime orderbook should not hard-block when disabled, openCount=%d", openCount)
	}
	tr.State.StatusLock.RLock()
	last := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if last.Mode != models.ModeReversalLong {
		t.Fatalf("mode lost for allowed unavailable orderbook reversal: %+v", last)
	}
	if containsString(last.Debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("unavailable orderbook rejected despite RequireSoftOrderbookAtRuntime=false: %+v", last.Debug)
	}
}

func TestReversalLongRuntimeStrengthThresholdUsesModeConfig(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Config.ReversalLong = config.DefaultReversalLongConfig()
	tr.Config.ReversalLong.Enabled = true
	tr.Config.ReversalLong.TradeEnabled = true
	tr.Config.ReversalLong.MinRuntimeStrength = 4
	tr.State.BidsMap = map[string]float64{"99.95": 1}
	tr.State.AsksMap = map[string]float64{"100.05": 1}

	runTraderWithSignal(tr, reversalRuntimeSignal(304))

	_, openCount := ex.counts()
	if openCount != 0 {
		t.Fatalf("reversal opened below mode runtime strength threshold")
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterMinimumStrength) {
		t.Fatalf("missing mode strength rejection: %+v", debug)
	}
}

func TestOhlcvSnapshotConcurrentAppendAndIndicatorCalculation(t *testing.T) {
	tr := newTestTrader()
	tr.Config.SmaLen = 5
	tr.Config.HTFWindow = 5
	tr.Config.HTFMaLen = 3

	done := make(chan struct{})
	errCh := make(chan string, 32)
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}

				snapshot := tr.State.GetOhlcvSnapshot()
				if len(snapshot.Closes) != len(snapshot.Highs) || len(snapshot.Closes) != len(snapshot.Lows) || len(snapshot.Closes) != len(snapshot.HTFCloses) {
					errCh <- fmt.Sprintf("inconsistent snapshot lengths: closes=%d highs=%d lows=%d htf=%d", len(snapshot.Closes), len(snapshot.Highs), len(snapshot.Lows), len(snapshot.HTFCloses))
					return
				}
				if len(snapshot.Volumes) > len(snapshot.Closes) {
					errCh <- fmt.Sprintf("volumes longer than closes: volumes=%d closes=%d", len(snapshot.Volumes), len(snapshot.Closes))
					return
				}
				if len(snapshot.Closes) >= tr.Config.SmaLen {
					_ = tr.buildIndicatorSnapshot(snapshot)
					_ = tr.calcTPSLDetails(snapshot.Closes[len(snapshot.Closes)-1], "LONG")
				}
			}
		}()
	}

	for i := 0; i < 200; i++ {
		price := 100.0 + float64(i)*0.1
		tr.OnClosedCandle(models.KlineData{
			Open:    fmt.Sprintf("%.2f", price-0.05),
			High:    fmt.Sprintf("%.2f", price+0.2),
			Low:     fmt.Sprintf("%.2f", price-0.2),
			Close:   fmt.Sprintf("%.2f", price),
			Volume:  "1",
			Confirm: true,
		})
	}
	close(done)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatal(err)
	}

	snapshot := tr.State.GetOhlcvSnapshot()
	if len(snapshot.Closes) == 0 {
		t.Fatalf("expected candles in snapshot")
	}
	originalFirstClose := snapshot.Closes[0]
	snapshot.Closes[0] = originalFirstClose + 1000
	nextSnapshot := tr.State.GetOhlcvSnapshot()
	if nextSnapshot.Closes[0] != originalFirstClose {
		t.Fatalf("snapshot exposes internal closes slice: got %.2f want %.2f", nextSnapshot.Closes[0], originalFirstClose)
	}
}

func TestTraderUsesPrecomputedOrderbookResultWithoutReevaluation(t *testing.T) {
	tr := newTestTrader()
	srv := newNoPositionServer(t)
	defer srv.Close()

	tr.Config.DemoRESTHost = srv.URL
	tr.Config.APIKey = "k"
	tr.Config.APISecret = "s"
	tr.Config.SignalStrengthThreshold = 99
	tr.Config.GracePeriodSec = 0
	tr.Config.ReentryCooldownSec = 0
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.Config.OrderbookLevels = 1
	tr.Config.OrderbookStabilityLookback = 8
	tr.Config.OrderbookStabilityRange = 10
	tr.Config.OrderbookMedianRatioMult = 0.01

	tr.State.SigChan = make(chan models.Signal, 1)
	tr.State.BidsMap = map[string]float64{"101": 10}
	tr.State.AsksMap = map[string]float64{"102": 5}
	setTestOhlcv(tr.State, []float64{100, 101}, nil, nil)

	orderbook := tr.EvaluateOrderbookStrength("LONG", 101)
	if !orderbook.Passed {
		t.Fatalf("expected precomputed orderbook to pass: %+v", orderbook)
	}
	if got := len(tr.State.ObImbalanceHistoryLong); got != 1 {
		t.Fatalf("long orderbook history after precompute got %d want 1", got)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 0 {
		t.Fatalf("short orderbook history after precompute got %d want 0", got)
	}

	tr.State.SigChan <- models.Signal{
		Kind:       "LONG",
		Direction:  "LONG",
		Strength:   1,
		ClosePrice: 101,
		CandleSeq:  1,
		Orderbook:  &orderbook,
	}
	close(tr.State.SigChan)

	tr.Trader()

	if got := len(tr.State.ObImbalanceHistoryLong); got != 1 {
		t.Fatalf("precomputed signal was re-evaluated: long orderbook history length got %d want 1", got)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 0 {
		t.Fatalf("precomputed signal touched short orderbook history: got %d want 0", got)
	}

	tr.State.StatusLock.RLock()
	lastDebug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(lastDebug.AppliedFilters, filterMinimumStrength) {
		t.Fatalf("minimum strength filter was not recorded: %+v", lastDebug)
	}
	if !containsString(lastDebug.RejectedBy, filterMinimumStrength) {
		t.Fatalf("minimum strength rejection was not recorded: %+v", lastDebug)
	}
	if lastDebug.LongScore != 1 {
		t.Fatalf("fallback debug long score got %d want 1", lastDebug.LongScore)
	}
}

func TestOrderbookLongEvaluationDoesNotTouchShortHistory(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.Config.OrderbookLevels = 1
	tr.Config.OrderbookStabilityLookback = 8
	tr.Config.OrderbookStabilityRange = 10
	tr.Config.OrderbookMedianRatioMult = 0.01
	tr.State.BidsMap = map[string]float64{"101": 10}
	tr.State.AsksMap = map[string]float64{"102": 5}

	result := tr.EvaluateOrderbookStrength("LONG", 0)
	if !result.Passed {
		t.Fatalf("expected LONG orderbook to pass: %+v", result)
	}
	if result.History != "long" {
		t.Fatalf("history got %q want long", result.History)
	}
	if got := len(tr.State.ObImbalanceHistoryLong); got != 1 {
		t.Fatalf("long history length got %d want 1", got)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 0 {
		t.Fatalf("short history length got %d want 0", got)
	}
}

func TestOrderbookShortEvaluationDoesNotTouchLongHistory(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.Config.OrderbookLevels = 1
	tr.Config.OrderbookStabilityLookback = 8
	tr.Config.OrderbookStabilityRange = 10
	tr.Config.OrderbookMedianRatioMult = 0.01
	tr.State.BidsMap = map[string]float64{"101": 5}
	tr.State.AsksMap = map[string]float64{"102": 10}

	result := tr.EvaluateOrderbookStrength("SHORT", 0)
	if !result.Passed {
		t.Fatalf("expected SHORT orderbook to pass: %+v", result)
	}
	if result.History != "short" {
		t.Fatalf("history got %q want short", result.History)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 1 {
		t.Fatalf("short history length got %d want 1", got)
	}
	if got := len(tr.State.ObImbalanceHistoryLong); got != 0 {
		t.Fatalf("long history length got %d want 0", got)
	}
}

func TestOrderbookStrengthThresholdRequiresSideDominance(t *testing.T) {
	cases := []struct {
		name       string
		side       string
		bidDepth   float64
		askDepth   float64
		wantPassed bool
		wantRatio  float64
	}{
		{
			name:       "long_rejects_weaker_bid_side",
			side:       "LONG",
			bidDepth:   5,
			askDepth:   10,
			wantPassed: false,
			wantRatio:  0.5,
		},
		{
			name:       "long_accepts_bid_dominance",
			side:       "LONG",
			bidDepth:   12,
			askDepth:   10,
			wantPassed: true,
			wantRatio:  1.2,
		},
		{
			name:       "short_rejects_weaker_ask_side",
			side:       "SHORT",
			bidDepth:   10,
			askDepth:   5,
			wantPassed: false,
			wantRatio:  0.5,
		},
		{
			name:       "short_accepts_ask_dominance",
			side:       "SHORT",
			bidDepth:   10,
			askDepth:   12,
			wantPassed: true,
			wantRatio:  1.2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTestTrader()
			tr.Config.OrderbookStrengthThreshold = 1.2
			tr.Config.OrderbookLevels = 1
			tr.Config.OrderbookStabilityLookback = 8
			tr.Config.OrderbookStabilityRange = 10
			tr.Config.OrderbookMedianRatioMult = 0.01
			tr.State.BidsMap = map[string]float64{"101": tc.bidDepth}
			tr.State.AsksMap = map[string]float64{"102": tc.askDepth}

			result := tr.EvaluateOrderbookStrength(tc.side, 0)
			if result.Passed != tc.wantPassed {
				t.Fatalf("passed got %t want %t: %+v", result.Passed, tc.wantPassed, result)
			}
			if math.Abs(result.Ratio-tc.wantRatio) > 1e-9 {
				t.Fatalf("ratio got %.4f want %.4f", result.Ratio, tc.wantRatio)
			}
			if !tc.wantPassed && result.Reason != "ratio 0.50 below 1.20" {
				t.Fatalf("reject reason got %q want ratio 0.50 below 1.20", result.Reason)
			}
		})
	}
}

func TestOrderbookStabilityIsSideSpecific(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderbookStrengthThreshold = 1.2
	tr.Config.OrderbookLevels = 1
	tr.Config.OrderbookStabilityLookback = 8
	tr.Config.OrderbookStabilityRange = 0.5
	tr.Config.OrderbookMedianRatioMult = 0.01

	tr.State.ObImbalanceHistoryLong = []float64{1, 10}
	tr.State.ObImbalanceHistoryShort = []float64{2, 2}

	tr.State.BidsMap = map[string]float64{"101": 20}
	tr.State.AsksMap = map[string]float64{"102": 10}
	longResult := tr.EvaluateOrderbookStrength("LONG", 0)
	if longResult.Passed {
		t.Fatalf("expected LONG to fail stability on long history: %+v", longResult)
	}
	if longResult.History != "long" {
		t.Fatalf("long history got %q want long", longResult.History)
	}
	if longResult.Reason != "ratio unstable 2.00" {
		t.Fatalf("long reason got %q want ratio unstable 2.00", longResult.Reason)
	}
	if got := len(tr.State.ObImbalanceHistoryLong); got != 3 {
		t.Fatalf("long history length got %d want 3", got)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 2 {
		t.Fatalf("short history changed after LONG evaluation: got %d want 2", got)
	}

	tr.State.BidsMap = map[string]float64{"101": 5}
	tr.State.AsksMap = map[string]float64{"102": 10}
	shortResult := tr.EvaluateOrderbookStrength("SHORT", 0)
	if !shortResult.Passed {
		t.Fatalf("expected SHORT to pass using short history only: %+v", shortResult)
	}
	if shortResult.History != "short" {
		t.Fatalf("short history got %q want short", shortResult.History)
	}
	if got := len(tr.State.ObImbalanceHistoryShort); got != 3 {
		t.Fatalf("short history length got %d want 3", got)
	}
	if got := len(tr.State.ObImbalanceHistoryLong); got != 3 {
		t.Fatalf("long history changed after SHORT evaluation: got %d want 3", got)
	}
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
	setTestOhlcv(tr.State, []float64{10, 10.2, 10.4, 10.6, 10.8}, nil, nil)
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
			wantSL: 99.5,
		},
		{
			name:   "short_ceil_tp_floor_sl",
			side:   "SHORT",
			tpIn:   99.26,
			slIn:   100.74,
			tick:   0.5,
			wantTP: 99.5,
			wantSL: 100.5,
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
	if entry-roundedLong > entry-longSL {
		t.Fatalf("long SL rounding moved farther: before %.2f after %.2f", longSL, roundedLong)
	}

	shortSL := 100.74
	roundedShort := tr.roundSL(entry, shortSL, tick, "SHORT")
	if roundedShort-entry > shortSL-entry {
		t.Fatalf("short SL rounding moved farther: before %.2f after %.2f", shortSL, roundedShort)
	}
}

func TestCalculateInitialTPSLDerivesSLFromTP(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.SetMarketRegime("range", "range", 0)
	tr.Config.EnableTPSLStage1 = true

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	setTestOhlcv(tr.State, closes, highs, lows)

	entry := 100.03
	tp, sl := tr.calculateInitialTPSL(entry, "LONG")

	tpDist := math.Abs(tp - entry)
	wantSLRaw := entry - tpDist/2
	wantSL := tr.roundSL(entry, wantSLRaw, tr.State.Instr.TickSize, "LONG")
	if sl != wantSL {
		t.Fatalf("sl not derived from tp: got %.4f want %.4f (tp %.4f entry %.4f)", sl, wantSL, tp, entry)
	}
}

func TestCalculateInitialTPSLIgnoresSlPercWhenDerivingSL(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.SetMarketRegime("range", "range", 0)
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
	setTestOhlcv(tr.State, closes, highs, lows)

	entry := 100.0

	// SlPerc can be wide, but should not force a huge TP when SL is derived from TP.
	tr.Config.SlPerc = 0.2
	tp, _ := tr.calculateInitialTPSL(entry, "LONG")
	tpDist := tp - entry
	if tpDist >= 1.0 {
		t.Fatalf("tpDist too large despite small fee floors: got %.4f", tpDist)
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
	setTestOhlcv(tr.State, nil, nil, nil)

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
			if slDist > 0 && tpDist/slDist < 2.0-1e-9 {
				t.Fatalf("rr invariant failed: got %.6f want >= 2.0", tpDist/slDist)
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

func TestTPSLDiagnosticsLongRawRR(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "LONG",
		Qty:              1,
		Entry:            100,
		RawTP:            104,
		RawSL:            98,
		RoundedTP:        104,
		RoundedSL:        98,
		FinalTP:          104,
		FinalSL:          98,
		TargetRR:         2,
		TickSize:         0.1,
		RoundTripFeePerc: 0.0012,
	})

	if math.Abs(d.RawRR-2.0) > 1e-9 {
		t.Fatalf("rawRR got %.8f want 2.0", d.RawRR)
	}
	if math.Abs(d.TPDistAbs-4.0) > 1e-9 || math.Abs(d.SLDistAbs-2.0) > 1e-9 {
		t.Fatalf("distances got tp %.8f sl %.8f want 4/2", d.TPDistAbs, d.SLDistAbs)
	}
}

func TestTPSLDiagnosticsShortRawRR(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "SHORT",
		Qty:              1,
		Entry:            100,
		RawTP:            96,
		RawSL:            102,
		RoundedTP:        96,
		RoundedSL:        102,
		FinalTP:          96,
		FinalSL:          102,
		TargetRR:         2,
		TickSize:         0.1,
		RoundTripFeePerc: 0.0012,
	})

	if math.Abs(d.RawRR-2.0) > 1e-9 {
		t.Fatalf("rawRR got %.8f want 2.0", d.RawRR)
	}
	if math.Abs(d.TPDistAbs-4.0) > 1e-9 || math.Abs(d.SLDistAbs-2.0) > 1e-9 {
		t.Fatalf("distances got tp %.8f sl %.8f want 4/2", d.TPDistAbs, d.SLDistAbs)
	}
}

func TestTPSLDiagnosticsNetRRWithFees(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "LONG",
		Qty:              1,
		Entry:            100,
		RawTP:            104,
		RawSL:            98,
		FinalTP:          104,
		FinalSL:          98,
		TargetRR:         1,
		TickSize:         0.1,
		RoundTripFeePerc: 0.002,
	})

	if d.NetTPPnL >= d.GrossTPPnL {
		t.Fatalf("net TP PnL should be lower than gross after fees: net %.8f gross %.8f", d.NetTPPnL, d.GrossTPPnL)
	}
	if d.NetSLLoss <= d.GrossSLLoss {
		t.Fatalf("net SL loss should be higher than gross after fees: net %.8f gross %.8f", d.NetSLLoss, d.GrossSLLoss)
	}
	if d.NetRR >= d.RawRR {
		t.Fatalf("netRR should be lower than rawRR after fees: net %.8f raw %.8f", d.NetRR, d.RawRR)
	}
}

func TestTPSLDiagnosticsInvalidLongTPSLWarning(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "LONG",
		Qty:              1,
		Entry:            100,
		RawTP:            99,
		RawSL:            101,
		FinalTP:          99,
		FinalSL:          101,
		TargetRR:         1,
		TickSize:         0.1,
		RoundTripFeePerc: 0.0012,
	})

	if !containsString(d.Warnings, tpslWarningWrongSideTP) {
		t.Fatalf("expected %s warning, got %v", tpslWarningWrongSideTP, d.Warnings)
	}
	if !containsString(d.Warnings, tpslWarningWrongSideSL) {
		t.Fatalf("expected %s warning, got %v", tpslWarningWrongSideSL, d.Warnings)
	}
}

func TestTPSLDiagnosticsInvalidShortTPSLWarning(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "SHORT",
		Qty:              1,
		Entry:            100,
		RawTP:            101,
		RawSL:            99,
		FinalTP:          101,
		FinalSL:          99,
		TargetRR:         1,
		TickSize:         0.1,
		RoundTripFeePerc: 0.0012,
	})

	if !containsString(d.Warnings, tpslWarningWrongSideTP) {
		t.Fatalf("expected %s warning, got %v", tpslWarningWrongSideTP, d.Warnings)
	}
	if !containsString(d.Warnings, tpslWarningWrongSideSL) {
		t.Fatalf("expected %s warning, got %v", tpslWarningWrongSideSL, d.Warnings)
	}
}

func TestTPSLDiagnosticsFeeAdjustedBreakevenWarning(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "LONG",
		Qty:              1,
		Entry:            100,
		RawTP:            102,
		RawSL:            100,
		FinalTP:          102,
		FinalSL:          100,
		TargetRR:         1,
		TickSize:         0.1,
		RoundTripFeePerc: 0.002,
	})

	if d.BreakevenSafe {
		t.Fatalf("SL at entry should not be fee-adjusted breakeven safe")
	}
	if d.FeeAdjustedBreakeven <= d.Entry {
		t.Fatalf("long fee-adjusted breakeven got %.8f want above entry %.8f", d.FeeAdjustedBreakeven, d.Entry)
	}
	if !containsString(d.Warnings, tpslWarningNegativeBreakeven) {
		t.Fatalf("expected %s warning, got %v", tpslWarningNegativeBreakeven, d.Warnings)
	}
}

func TestTPSLDiagnosticsPreservesRoundingValues(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:           "BTCUSDT",
		Side:             "LONG",
		Qty:              1,
		Entry:            100,
		RawTP:            104.06,
		RawSL:            98.04,
		RoundedTP:        104.00,
		RoundedSL:        98.10,
		FinalTP:          104.00,
		FinalSL:          98.10,
		TargetRR:         1,
		TickSize:         0.1,
		RoundTripFeePerc: 0.0012,
	})

	if d.RawTP != 104.06 || d.RawSL != 98.04 {
		t.Fatalf("raw TP/SL not preserved: rawTP %.8f rawSL %.8f", d.RawTP, d.RawSL)
	}
	if d.RoundedTP != 104.00 || d.RoundedSL != 98.10 {
		t.Fatalf("rounded TP/SL not preserved: roundedTP %.8f roundedSL %.8f", d.RoundedTP, d.RoundedSL)
	}
	if d.FinalTP != 104.00 || d.FinalSL != 98.10 {
		t.Fatalf("final TP/SL not preserved: finalTP %.8f finalSL %.8f", d.FinalTP, d.FinalSL)
	}
}

func TestTPSLDiagnosticsConfigUnusedWarnings(t *testing.T) {
	d := BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:                "BTCUSDT",
		Side:                  "LONG",
		Qty:                   1,
		Entry:                 100,
		RawTP:                 104,
		RawSL:                 98,
		FinalTP:               104,
		FinalSL:               98,
		DynamicTPEnabled:      false,
		DynamicTPUsed:         true,
		TargetRRConfigPresent: true,
		TargetRRUsed:          false,
		ATRCapConfigPresent:   true,
		ATRCapUsed:            false,
		MinProfitPerc:         0.0025,
		TPFeeFloorMult:        5.0,
		SLFeeFloorMult:        4.0,
		TargetRR:              1,
		TickSize:              0.1,
		RoundTripFeePerc:      0.0012,
	})

	for _, warning := range []string{
		tpslWarningDynamicTPDisabledButUsed,
		tpslWarningConfigUnusedTargetRR,
		tpslWarningConfigUnusedATRCap,
		tpslWarningConfigUnusedMinProfit,
		tpslWarningConfigUnusedTPFeeFloor,
		tpslWarningConfigUnusedSLFeeFloor,
	} {
		if !containsString(d.Warnings, warning) {
			t.Fatalf("expected %s warning, got %v", warning, d.Warnings)
		}
	}
}
