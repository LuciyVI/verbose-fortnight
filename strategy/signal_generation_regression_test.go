package strategy

import (
	"sync"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

var regressionNow = time.Unix(1700000000, 0)

func newSignalRegressionTrader() *Trader {
	tr := newTestTrader()
	tr.Now = func() time.Time { return regressionNow }
	tr.Config.SmaLen = 5
	tr.Config.HTFWindow = 0
	tr.Config.HTFMaLen = 0
	tr.Config.RSIMode = config.RSIModeLog
	tr.Config.RSILow = 45
	tr.Config.RSIHigh = 55
	tr.Config.SignalStrengthThreshold = 1
	tr.Config.GracePeriodSec = 0
	tr.Config.ReentryCooldownSec = 0
	tr.State.SetMarketRegime("trend", "trend", 0)
	tr.State.SigChan = make(chan models.Signal, 4)
	return tr
}

func regressionLongSnapshot(rsi float64, htfBias string) models.IndicatorSnapshot {
	return models.IndicatorSnapshot{
		Time:       regressionNow,
		Close:      105,
		SMA:        100,
		RSI:        rsi,
		MACDLine:   2,
		MACDSignal: 1,
		MACDHist:   1,
		ATR:        0,
		BBUpper:    104,
		BBMiddle:   100,
		BBLower:    96,
		HTFBias:    htfBias,
	}
}

func regressionShortSnapshot(rsi float64, htfBias string) models.IndicatorSnapshot {
	return models.IndicatorSnapshot{
		Time:       regressionNow,
		Close:      95,
		SMA:        100,
		RSI:        rsi,
		MACDLine:   -2,
		MACDSignal: -1,
		MACDHist:   -1,
		ATR:        0,
		BBUpper:    104,
		BBMiddle:   100,
		BBLower:    96,
		HTFBias:    htfBias,
	}
}

func regressionCloses() []float64 {
	closes := make([]float64, 40)
	for i := range closes {
		closes[i] = 100 + float64(i)*0.5
	}
	closes[len(closes)-1] += 3
	return closes
}

func regressionOhlcv() ([]float64, []float64, []float64) {
	closes := regressionCloses()
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, closePrice := range closes {
		highs[i] = closePrice + 1
		lows[i] = closePrice - 1
	}
	return closes, highs, lows
}

func regressionPassingOrderbook(side string, _ float64) models.OrderbookResult {
	return passingOrderbookResult(side)
}

func regressionFailingOrderbook(side string, _ float64) models.OrderbookResult {
	return models.OrderbookResult{
		Evaluated: true,
		Passed:    false,
		Ratio:     0.8,
		Side:      side,
		Threshold: 1.2,
		History:   "test",
		Reason:    "ratio 0.80 below 1.20",
	}
}

func regressionRuntimeSignal(direction string, strength int, seq uint64) models.Signal {
	orderbook := passingOrderbookResult(direction)
	debug := models.SignalDebug{
		ChosenDirection: direction,
		Reasons:         []string{direction + ":regression"},
	}
	if direction == "LONG" {
		debug.LongScore = strength
	} else if direction == "SHORT" {
		debug.ShortScore = strength
	}
	return models.Signal{
		Kind:       direction,
		Direction:  direction,
		Strength:   strength,
		ClosePrice: 100,
		CandleSeq:  seq,
		Time:       regressionNow,
		Orderbook:  &orderbook,
		Debug:      debug,
	}
}

func runTraderWithSignal(tr *Trader, sig models.Signal) {
	tr.State.SigChan = make(chan models.Signal, 1)
	tr.State.SigChan <- sig
	close(tr.State.SigChan)
	tr.Trader()
}

func TestSignalGenerationRegressionLongScoreWinsAndPassesFilters(t *testing.T) {
	tr := newSignalRegressionTrader()

	scored := tr.ScoreSignal(regressionLongSnapshot(60, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("expected LONG signal to pass filters: %+v", filtered.Signal.Debug)
	}
	if !tr.EmitSignal(filtered) {
		t.Fatalf("expected LONG signal to emit")
	}

	sig := <-tr.State.SigChan
	if sig.Direction != "LONG" {
		t.Fatalf("direction got %q want LONG", sig.Direction)
	}
	if sig.Debug.LongScore <= sig.Debug.ShortScore {
		t.Fatalf("expected longScore > shortScore, debug=%+v", sig.Debug)
	}
	if !containsString(sig.Contribs, "sma-breakout") || !containsString(sig.Contribs, "macd-trend") {
		t.Fatalf("missing LONG scoring reasons: %v", sig.Contribs)
	}
}

func TestSignalGenerationRegressionReversalLongDisabledDoesNotTagCurrentSignals(t *testing.T) {
	tr := newSignalRegressionTrader()
	if tr.Config.ReversalLong.Enabled {
		t.Fatalf("reversal_long default should be disabled")
	}

	scored := tr.ScoreSignal(regressionLongSnapshot(60, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("expected existing LONG signal to pass filters: %+v", filtered.Signal.Debug)
	}
	if filtered.Signal.Direction != "LONG" {
		t.Fatalf("direction got %q want LONG", filtered.Signal.Direction)
	}
	if filtered.Signal.Mode != "" {
		t.Fatalf("disabled reversal_long should not tag existing signal mode: %q", filtered.Signal.Mode)
	}
	if containsString(filtered.Signal.Debug.Reasons, models.SignalReasonReversalLong) {
		t.Fatalf("disabled reversal_long should not add reversal reason: %+v", filtered.Signal.Debug)
	}
	if containsString(filtered.Signal.Debug.Reasons, models.SignalReasonReversalDisabled) {
		t.Fatalf("disabled reversal_long should not add disabled reason to current pipeline: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionShortScoreWinsAndPassesFilters(t *testing.T) {
	tr := newSignalRegressionTrader()

	scored := tr.ScoreSignal(regressionShortSnapshot(40, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("expected SHORT signal to pass filters: %+v", filtered.Signal.Debug)
	}
	if !tr.EmitSignal(filtered) {
		t.Fatalf("expected SHORT signal to emit")
	}

	sig := <-tr.State.SigChan
	if sig.Direction != "SHORT" {
		t.Fatalf("direction got %q want SHORT", sig.Direction)
	}
	if sig.Debug.ShortScore <= sig.Debug.LongScore {
		t.Fatalf("expected shortScore > longScore, debug=%+v", sig.Debug)
	}
	if !containsString(sig.Contribs, "sma-breakdown") || !containsString(sig.Contribs, "macd-trend") {
		t.Fatalf("missing SHORT scoring reasons: %v", sig.Contribs)
	}
}

func TestSignalGenerationRegressionEqualScoreTieRejected(t *testing.T) {
	tr := newSignalRegressionTrader()
	scored := scoredSignal{
		Snapshot:  models.IndicatorSnapshot{Time: regressionNow, Close: 100},
		CandleSeq: 1,
		Time:      regressionNow,
		Regime:    "trend",
		RSIDecision: rsiPolicyDecision{
			Mode:       config.RSIModeLog,
			AllowLong:  true,
			AllowShort: true,
			Impact:     "log_only",
		},
		LongScore:    2,
		ShortScore:   2,
		LongReasons:  []string{"long-a", "long-b"},
		ShortReasons: []string{"short-a", "short-b"},
	}

	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if filtered.Accepted {
		t.Fatalf("expected tie to be rejected")
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, tieStrengthNoSignalReason) {
		t.Fatalf("missing tie rejection: %+v", filtered.Signal.Debug)
	}
	if tr.EmitSignal(filtered) {
		t.Fatalf("tie should not emit signal")
	}
	if len(tr.State.SigChan) != 0 {
		t.Fatalf("tie emitted unexpected signal")
	}
}

func TestSignalGenerationRegressionOrderbookNotConfirmedRejected(t *testing.T) {
	tr := newSignalRegressionTrader()

	scored := tr.ScoreSignal(regressionLongSnapshot(50, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionFailingOrderbook)
	if filtered.Accepted {
		t.Fatalf("expected orderbook rejection")
	}
	if filtered.Signal.Orderbook == nil || filtered.Signal.Orderbook.Passed {
		t.Fatalf("expected failed orderbook result on signal: %+v", filtered.Signal.Orderbook)
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, filterOrderbookConfirmation) {
		t.Fatalf("missing orderbook rejectedBy: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSILogModeDoesNotBlock(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.RSIMode = config.RSIModeLog

	scored := tr.ScoreSignal(regressionLongSnapshot(90, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("RSI log mode should not block signal: %+v", filtered.Signal.Debug)
	}
	if containsString(filtered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("RSI log mode should not apply RSI hard filter: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSIFilterModeBlocksUnsuitableLong(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.RSIMode = config.RSIModeFilter

	scored := tr.ScoreSignal(regressionLongSnapshot(90, ""), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if filtered.Accepted {
		t.Fatalf("RSI filter mode should block unsuitable LONG")
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, filterRSIConfirmation) {
		t.Fatalf("missing RSI rejectedBy: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSIOffModeDoesNotAffectScoreOrFilter(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.RSIMode = config.RSIModeOff

	scored := tr.ScoreSignal(regressionLongSnapshot(40, ""), 1, regressionCloses())
	if scored.LongScore != 3 {
		t.Fatalf("RSI off mode changed long score: got %d want 3, reasons=%v", scored.LongScore, scored.LongReasons)
	}
	if containsString(scored.LongReasons, "rsi-oversold") || containsString(scored.ShortReasons, "rsi-overbought") {
		t.Fatalf("RSI off mode added RSI scoring reasons: long=%v short=%v", scored.LongReasons, scored.ShortReasons)
	}

	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("RSI off mode should not reject signal: %+v", filtered.Signal.Debug)
	}
	if containsString(filtered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("RSI off mode should not apply hard filter: %+v", filtered.Signal.Debug)
	}
	if len(filtered.Signal.Debug.RejectedBy) != 0 {
		t.Fatalf("RSI off mode produced rejection: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.Reasons, "rsi mode=off value=40.00 impact=off") {
		t.Fatalf("RSI off mode missing debug reason: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSIScoreModeAddsScoreWithoutHardBlocking(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.RSIMode = config.RSIModeScore

	scored := tr.ScoreSignal(regressionLongSnapshot(40, ""), 1, regressionCloses())
	if scored.LongScore != 4 {
		t.Fatalf("RSI score mode long score got %d want 4, reasons=%v", scored.LongScore, scored.LongReasons)
	}
	if !containsString(scored.LongReasons, "rsi-oversold") {
		t.Fatalf("RSI score mode missing rsi-oversold reason: %v", scored.LongReasons)
	}

	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("RSI score mode should not hard-block signal: %+v", filtered.Signal.Debug)
	}
	if filtered.Signal.Debug.LongScore != 4 {
		t.Fatalf("debug long score got %d want 4", filtered.Signal.Debug.LongScore)
	}
	if containsString(filtered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("RSI score mode should not apply RSI hard filter: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.Reasons, "LONG:rsi-oversold") {
		t.Fatalf("RSI score mode missing score debug reason: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.Reasons, "rsi mode=score value=40.00 impact=score_long") {
		t.Fatalf("RSI score mode missing impact debug reason: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSIScoreModeDoesNotRejectLikeFilterMode(t *testing.T) {
	filterTrader := newSignalRegressionTrader()
	filterTrader.Config.RSIMode = config.RSIModeFilter
	filteredByFilterMode := filterTrader.FilterSignal(
		filterTrader.ScoreSignal(regressionLongSnapshot(60, ""), 1, regressionCloses()),
		regressionPassingOrderbook,
	)
	if filteredByFilterMode.Accepted {
		t.Fatalf("RSI filter mode should reject unsuitable LONG")
	}
	if !containsString(filteredByFilterMode.Signal.Debug.RejectedBy, filterRSIConfirmation) {
		t.Fatalf("filter mode missing RSI rejection: %+v", filteredByFilterMode.Signal.Debug)
	}

	scoreTrader := newSignalRegressionTrader()
	scoreTrader.Config.RSIMode = config.RSIModeScore
	scored := scoreTrader.ScoreSignal(regressionLongSnapshot(60, ""), 1, regressionCloses())
	if scored.LongScore != 3 || scored.ShortScore != 1 {
		t.Fatalf("RSI score mode scores got L/S=%d/%d want 3/1, reasons L=%v S=%v",
			scored.LongScore, scored.ShortScore, scored.LongReasons, scored.ShortReasons)
	}
	if !containsString(scored.ShortReasons, "rsi-overbought") {
		t.Fatalf("RSI score mode missing short RSI score reason: %v", scored.ShortReasons)
	}

	filtered := scoreTrader.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("RSI score mode should not reject like filter mode: %+v", filtered.Signal.Debug)
	}
	if filtered.Signal.Direction != "LONG" {
		t.Fatalf("direction got %q want LONG", filtered.Signal.Direction)
	}
	if containsString(filtered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("RSI score mode should not apply RSI hard filter: %+v", filtered.Signal.Debug)
	}
	if containsString(filtered.Signal.Debug.RejectedBy, filterRSIConfirmation) {
		t.Fatalf("RSI score mode should not reject by RSI filter: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.Reasons, "SHORT:rsi-overbought") {
		t.Fatalf("RSI score mode missing score-only short debug reason: %+v", filtered.Signal.Debug)
	}
	if !containsString(filtered.Signal.Debug.Reasons, "rsi mode=score value=60.00 impact=score_short") {
		t.Fatalf("RSI score mode missing score impact debug reason: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionRSIScoreVsOffModeDiffersOnlyByScoreAndReason(t *testing.T) {
	snap := regressionLongSnapshot(40, "")
	closes := regressionCloses()

	offTrader := newSignalRegressionTrader()
	offTrader.Config.RSIMode = config.RSIModeOff
	offFiltered := offTrader.FilterSignal(offTrader.ScoreSignal(snap, 1, closes), regressionPassingOrderbook)
	if !offFiltered.Accepted {
		t.Fatalf("RSI off mode should accept signal: %+v", offFiltered.Signal.Debug)
	}

	scoreTrader := newSignalRegressionTrader()
	scoreTrader.Config.RSIMode = config.RSIModeScore
	scoreFiltered := scoreTrader.FilterSignal(scoreTrader.ScoreSignal(snap, 1, closes), regressionPassingOrderbook)
	if !scoreFiltered.Accepted {
		t.Fatalf("RSI score mode should accept signal: %+v", scoreFiltered.Signal.Debug)
	}

	if offFiltered.Signal.Direction != scoreFiltered.Signal.Direction {
		t.Fatalf("RSI mode changed direction off=%s score=%s", offFiltered.Signal.Direction, scoreFiltered.Signal.Direction)
	}
	if len(offFiltered.Signal.Debug.RejectedBy) != 0 || len(scoreFiltered.Signal.Debug.RejectedBy) != 0 {
		t.Fatalf("unexpected rejection off=%+v score=%+v", offFiltered.Signal.Debug, scoreFiltered.Signal.Debug)
	}
	if containsString(offFiltered.Signal.Debug.AppliedFilters, filterRSIConfirmation) || containsString(scoreFiltered.Signal.Debug.AppliedFilters, filterRSIConfirmation) {
		t.Fatalf("RSI off/score should not apply RSI hard filter: off=%+v score=%+v", offFiltered.Signal.Debug, scoreFiltered.Signal.Debug)
	}
	if offFiltered.Signal.Debug.LongScore != 3 {
		t.Fatalf("off long score got %d want 3", offFiltered.Signal.Debug.LongScore)
	}
	if scoreFiltered.Signal.Debug.LongScore != offFiltered.Signal.Debug.LongScore+1 {
		t.Fatalf("score mode should add exactly one RSI score: off=%d score=%d",
			offFiltered.Signal.Debug.LongScore, scoreFiltered.Signal.Debug.LongScore)
	}
	if !containsString(scoreFiltered.Signal.Debug.Reasons, "LONG:rsi-oversold") {
		t.Fatalf("score mode missing RSI score reason: %+v", scoreFiltered.Signal.Debug)
	}
	if containsString(offFiltered.Signal.Debug.Reasons, "LONG:rsi-oversold") {
		t.Fatalf("off mode unexpectedly has RSI score reason: %+v", offFiltered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionHTFFilterBlocksOppositeSide(t *testing.T) {
	tr := newSignalRegressionTrader()

	scored := tr.ScoreSignal(regressionShortSnapshot(50, "LONG"), 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if filtered.Accepted {
		t.Fatalf("HTF LONG bias should block SHORT signal")
	}
	if !containsString(filtered.Signal.Debug.RejectedBy, filterHTFConfirmation) {
		t.Fatalf("missing HTF rejectedBy: %+v", filtered.Signal.Debug)
	}
}

func TestSignalGenerationRegressionMinimumStrengthRejectsBelowThreshold(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Now = func() time.Time { return regressionNow }
	tr.Config.SignalStrengthThreshold = 2
	tr.Config.GracePeriodSec = 0
	tr.Config.ReentryCooldownSec = 0

	runTraderWithSignal(tr, regressionRuntimeSignal("LONG", 1, 11))

	closeCount, openCount := ex.counts()
	if closeCount != 0 || openCount != 0 {
		t.Fatalf("threshold rejection should not place orders: close=%d open=%d", closeCount, openCount)
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterMinimumStrength) {
		t.Fatalf("missing minimum strength rejectedBy: %+v", debug)
	}
}

func TestSignalGenerationRegressionGracePeriodRejectsRuntimeSignal(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "LONG")
	defer ex.close()
	tr.Now = func() time.Time { return regressionNow }
	tr.Config.SignalStrengthThreshold = 1
	tr.Config.GracePeriodSec = 60
	tr.State.LastEntryAt = regressionNow.Add(-time.Second)

	runTraderWithSignal(tr, regressionRuntimeSignal("SHORT", 3, 12))

	closeCount, openCount := ex.counts()
	if closeCount != 0 || openCount != 0 {
		t.Fatalf("grace rejection should not place orders: close=%d open=%d", closeCount, openCount)
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterGracePeriod) {
		t.Fatalf("missing grace rejectedBy: %+v", debug)
	}
}

func TestSignalGenerationRegressionCooldownRejectsRuntimeSignal(t *testing.T) {
	tr, ex := newExecutionTestTrader(t, "")
	defer ex.close()
	tr.Now = func() time.Time { return regressionNow }
	tr.Config.SignalStrengthThreshold = 1
	tr.Config.ReentryCooldownSec = 60
	tr.State.LastExitAt = regressionNow.Add(-time.Second)
	tr.State.LastExitDir = "LONG"
	tr.State.LastEntryPrice = 1

	runTraderWithSignal(tr, regressionRuntimeSignal("LONG", 3, 13))

	closeCount, openCount := ex.counts()
	if closeCount != 0 || openCount != 0 {
		t.Fatalf("cooldown rejection should not place orders: close=%d open=%d", closeCount, openCount)
	}
	tr.State.StatusLock.RLock()
	debug := tr.State.LastSignal.Debug
	tr.State.StatusLock.RUnlock()
	if !containsString(debug.RejectedBy, filterCooldown) {
		t.Fatalf("missing cooldown rejectedBy: %+v", debug)
	}
}

func TestSignalGenerationRegressionWorkerNoSignalWithInsufficientCandles(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.SmaLen = 10
	setTestOhlcv(tr.State, []float64{100, 101, 102}, []float64{101, 102, 103}, []float64{99, 100, 101})
	lastSeq := uint64(0)

	if tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("worker cycle emitted signal with insufficient candles")
	}
	if len(tr.State.SigChan) != 0 {
		t.Fatalf("unexpected signal emitted with insufficient candles")
	}
}

func TestSignalGenerationRegressionWorkerDoesNotRepeatSameCandleSeq(t *testing.T) {
	tr := newSignalRegressionTrader()
	closes, highs, lows := regressionOhlcv()
	setTestOhlcv(tr.State, closes, highs, lows)
	lastSeq := uint64(0)

	if !tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("first worker cycle should emit signal")
	}
	first := <-tr.State.SigChan
	if first.CandleSeq == 0 {
		t.Fatalf("expected emitted signal to carry CandleSeq")
	}
	if tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("worker cycle emitted duplicate signal for same CandleSeq")
	}
	if len(tr.State.SigChan) != 0 {
		t.Fatalf("duplicate signal left in channel")
	}
}

func TestSignalGenerationRegressionWorkerEmitsOnlyAfterNewClosedCandle(t *testing.T) {
	tr := newSignalRegressionTrader()
	closes, highs, lows := regressionOhlcv()
	setTestOhlcv(tr.State, closes, highs, lows)
	lastSeq := uint64(0)

	if !tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("first worker cycle should emit signal")
	}
	first := <-tr.State.SigChan

	if tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("same CandleSeq should not emit before new closed candle")
	}

	tr.State.AppendClosedCandle(first.ClosePrice+1, first.ClosePrice+2, first.ClosePrice, 1, 1000)
	if !tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook) {
		t.Fatalf("worker should emit after new closed candle")
	}
	second := <-tr.State.SigChan
	if second.CandleSeq <= first.CandleSeq {
		t.Fatalf("new signal CandleSeq got %d want > %d", second.CandleSeq, first.CandleSeq)
	}
}

func TestMarketRegimeConcurrentUpdateAndSignalScoring(t *testing.T) {
	tr := newSignalRegressionTrader()
	tr.Config.SmaLen = 5
	tr.Config.HTFWindow = 5
	tr.Config.HTFMaLen = 3
	tr.Config.RegimePersistence = 1
	tr.State.SigChan = make(chan models.Signal, 1024)

	closes := make([]float64, 80)
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i := range closes {
		closes[i] = 100 + float64(i)*0.25
		highs[i] = closes[i] + 1
		lows[i] = closes[i] - 1
	}
	setTestOhlcv(tr.State, closes, highs, lows)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 300; i++ {
			tr.DetectMarketRegime()
		}
	}()

	go func() {
		defer wg.Done()
		lastSeq := uint64(0)
		for i := 0; i < 300; i++ {
			price := 120 + float64(i)*0.05
			tr.State.AppendClosedCandle(price, price+1, price-1, 1, 1000)
			tr.processSMASignalCycle(&lastSeq, regressionPassingOrderbook)
			for drained := false; !drained; {
				select {
				case <-tr.State.SigChan:
				default:
					drained = true
				}
			}
		}
	}()

	wg.Wait()

	regimeSnapshot := tr.State.GetMarketRegimeSnapshot()
	if regimeSnapshot.RegimeCandidate == "" {
		t.Fatalf("expected regime update loop to set candidate: %+v", regimeSnapshot)
	}
}
