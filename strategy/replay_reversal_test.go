package strategy

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func replayReadyOrderbook() ReversalOrderbookInput {
	return ReversalOrderbookInput{
		Available:             true,
		SpreadBps:             1,
		Ratio:                 1.1,
		Imbalance:             0.05,
		BestBidSize:           1,
		BestAskSize:           0.9,
		AskWallRatio:          0.9,
		BidStabilityRatio:     1,
		BidStabilityAvailable: true,
	}
}

func replayHostileOrderbook() ReversalOrderbookInput {
	return ReversalOrderbookInput{
		Available:             true,
		SpreadBps:             1,
		Ratio:                 0.1,
		Imbalance:             -0.9,
		BestBidSize:           1,
		BestAskSize:           10,
		AskWallRatio:          10,
		BidStabilityRatio:     1,
		BidStabilityAvailable: true,
	}
}

func replayReversalFrames(t *testing.T, detectorOrderbook ReversalOrderbookInput) []ReversalReplayFrame {
	t.Helper()
	now := time.Unix(1700000200, 0).UTC()
	snap := func(ts time.Time) models.IndicatorSnapshot {
		s := reversalLongSnapshot()
		s.Time = ts
		s.Close = 100
		s.Low = 99
		s.HTFBias = "SHORT"
		return s
	}

	capSnapshot := snap(now)
	capSnapshot.RSI = 25
	capSnapshot.Low = 98

	belowSnapshot := snap(now.Add(time.Minute))
	belowSnapshot.BBPosition = bbPositionBelowLower
	belowSnapshot.Low = 97

	reclaimedSnapshot := snap(now.Add(2 * time.Minute))
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	reclaimedSnapshot.Low = 97.5

	momentumSnapshot := reclaimedSnapshot
	momentumSnapshot.Time = now.Add(3 * time.Minute)
	momentumSnapshot.RSI = 35
	momentumSnapshot.RSIPrev = 28
	momentumSnapshot.RSIDelta = 7
	momentumSnapshot.MACDHist = -0.1
	momentumSnapshot.MACDHistPrev = -0.3
	momentumSnapshot.MACDHistDelta = 0.2
	momentumSnapshot.Low = 98

	readySnapshot := momentumSnapshot
	readySnapshot.Time = now.Add(4 * time.Minute)
	readySnapshot.Low = 98.5

	snapshots := []models.IndicatorSnapshot{capSnapshot, belowSnapshot, reclaimedSnapshot, momentumSnapshot, readySnapshot}
	frames := make([]ReversalReplayFrame, 0, len(snapshots))
	for i := range snapshots {
		s := snapshots[i]
		frames = append(frames, ReversalReplayFrame{Time: s.Time, Snapshot: &s, Orderbook: detectorOrderbook})
	}
	return frames
}

func replayReversalFramesWithMomentumDelta(t *testing.T, detectorOrderbook ReversalOrderbookInput, delta float64) []ReversalReplayFrame {
	t.Helper()
	frames := replayReversalFrames(t, detectorOrderbook)
	for i := range frames {
		if frames[i].Snapshot != nil && !frames[i].Snapshot.Time.IsZero() && i >= 3 {
			frames[i].Snapshot.RSIDelta = delta
		}
	}
	return frames
}

func replayShortReversalFrames(t *testing.T, detectorOrderbook ReversalOrderbookInput) []ReversalReplayFrame {
	t.Helper()
	now := time.Unix(1700000400, 0).UTC()
	snap := func(ts time.Time) models.IndicatorSnapshot {
		s := reversalLongSnapshot()
		s.Time = ts
		s.Close = 100
		s.Low = 99
		s.HTFBias = "SHORT"
		s.RSI = 25
		return s
	}
	capSnapshot := snap(now)
	capSnapshot.Low = 98

	belowSnapshot := snap(now.Add(time.Minute))
	belowSnapshot.BBPosition = bbPositionBelowLower
	belowSnapshot.Low = 97

	reclaimedMomentumSnapshot := snap(now.Add(2 * time.Minute))
	reclaimedMomentumSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedMomentumSnapshot.WasBelowLowerBB = true
	reclaimedMomentumSnapshot.ReturnedInsideBB = true
	reclaimedMomentumSnapshot.RSI = 35
	reclaimedMomentumSnapshot.RSIPrev = 28
	reclaimedMomentumSnapshot.RSIDelta = 7
	reclaimedMomentumSnapshot.MACDHist = -0.1
	reclaimedMomentumSnapshot.MACDHistPrev = -0.3
	reclaimedMomentumSnapshot.MACDHistDelta = 0.2
	reclaimedMomentumSnapshot.Low = 98

	snapshots := []models.IndicatorSnapshot{capSnapshot, belowSnapshot, reclaimedMomentumSnapshot}
	frames := make([]ReversalReplayFrame, 0, len(snapshots))
	for i := range snapshots {
		s := snapshots[i]
		frames = append(frames, ReversalReplayFrame{Time: s.Time, Snapshot: &s, Orderbook: detectorOrderbook})
	}
	return frames
}

func replayOptions(tradeEnabled bool) ReversalReplayOptions {
	cfg := config.LoadConfig()
	cfg.ReversalLong = enabledReversalLongConfig()
	cfg.ReversalLong.TradeEnabled = tradeEnabled
	cfg.SignalStrengthThreshold = cfg.ReversalLong.MinRuntimeStrength
	return ReversalReplayOptions{
		Symbol:               "BTCUSDT",
		ReversalEnabled:      true,
		ReversalTradeEnabled: tradeEnabled,
		OrdersEnabled:        false,
		Config:               cfg,
	}
}

func TestReplayDiagnosticsModeDoesNotEmitSignal(t *testing.T) {
	result, err := RunReversalReplayFrames(replayReversalFrames(t, replayReadyOrderbook()), replayOptions(false))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.ReversalCandidateReadyCount == 0 {
		t.Fatalf("expected diagnostic candidate_ready, summary=%+v", result.Summary)
	}
	if result.Summary.ReversalSignalsEmittedDryRun != 0 || result.Summary.OrdersSent != 0 {
		t.Fatalf("diagnostics mode emitted signal/order: %+v", result.Summary)
	}
	if result.Summary.ReversalPipelineAllowedCount != 0 {
		t.Fatalf("TradeEnabled=false allowed pipeline: %+v", result.Summary)
	}
}

func TestReplaySignalDryRunEmitsModeReversalLongButNoOrder(t *testing.T) {
	result, err := RunReversalReplayFrames(replayReversalFrames(t, replayReadyOrderbook()), replayOptions(true))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.ReversalSignalsEmittedDryRun == 0 {
		t.Fatalf("expected dry-run reversal signal, summary=%+v", result.Summary)
	}
	if result.Summary.RuntimeReversalPassCount == 0 {
		t.Fatalf("expected runtime pass in dry-run, summary=%+v", result.Summary)
	}
	if result.Summary.OrdersSent != 0 {
		t.Fatalf("dry-run sent orders: %+v", result.Summary)
	}
	if !replayHasEvent(result, "dry_run_order_blocked") {
		t.Fatalf("missing dry_run_order_blocked event: %+v", result.Events)
	}
}

func TestReplayHostileRuntimeOrderbookBlocksReversal(t *testing.T) {
	frames := replayReversalFrames(t, replayReadyOrderbook())
	hostile := replayHostileOrderbook()
	for i := range frames {
		frames[i].RuntimeOrderbook = &hostile
	}
	result, err := RunReversalReplayFrames(frames, replayOptions(true))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.ReversalSignalsEmittedDryRun == 0 {
		t.Fatalf("expected dry-run signal before runtime block, summary=%+v", result.Summary)
	}
	if result.Summary.RuntimeReversalBlockCount == 0 {
		t.Fatalf("expected runtime block, summary=%+v", result.Summary)
	}
	if result.Summary.RuntimeReversalPassCount != 0 || result.Summary.OrdersSent != 0 {
		t.Fatalf("hostile orderbook passed unexpectedly: %+v", result.Summary)
	}
	if !replayHasRuntimeReason(result, ReversalReasonAskWallTooLarge) && !replayHasRuntimeReason(result, ReversalReasonOrderbookHostile) {
		t.Fatalf("missing hostile runtime reason: %+v", result.Events)
	}
}

func TestReplayRuntimeOrderbookUnavailableBlocksWhenRequired(t *testing.T) {
	frames := replayReversalFrames(t, replayReadyOrderbook())
	unavailable := ReversalOrderbookInput{}
	for i := range frames {
		frames[i].RuntimeOrderbook = &unavailable
	}
	opts := replayOptions(true)
	required := true
	opts.RequireSoftOrderbookRuntime = &required
	result, err := RunReversalReplayFrames(frames, opts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.RuntimeReversalBlockCount == 0 {
		t.Fatalf("expected unavailable runtime orderbook block, summary=%+v", result.Summary)
	}
	if !replayHasRuntimeReason(result, ReversalReasonRuntimeOBUnavailable) {
		t.Fatalf("missing unavailable orderbook reason: %+v", result.Events)
	}
}

func TestReplayRuntimeOrderbookUnavailableAllowedWhenNotRequired(t *testing.T) {
	frames := replayReversalFrames(t, replayReadyOrderbook())
	unavailable := ReversalOrderbookInput{}
	for i := range frames {
		frames[i].RuntimeOrderbook = &unavailable
	}
	opts := replayOptions(true)
	required := false
	opts.RequireSoftOrderbookRuntime = &required
	result, err := RunReversalReplayFrames(frames, opts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.RuntimeReversalPassCount == 0 || result.Summary.OrdersSent != 0 {
		t.Fatalf("unavailable orderbook should be allowed in dry-run when not required: %+v", result.Summary)
	}
}

func TestReadReversalReplayCandlesCSV(t *testing.T) {
	raw := `time,open,high,low,close,volume,best_bid,best_ask,best_bid_size,best_ask_size
2026-05-20T13:39:00Z,100,101,99,100.5,10,100.4,100.6,2,1
`
	candles, err := ReadReversalReplayCandlesCSV(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	if len(candles) != 1 {
		t.Fatalf("candles len got %d want 1", len(candles))
	}
	if !candles[0].Time.Equal(time.Date(2026, 5, 20, 13, 39, 0, 0, time.UTC)) {
		t.Fatalf("time got %s", candles[0].Time)
	}
	if candles[0].BestBidSize != 2 || candles[0].BestAskSize != 1 {
		t.Fatalf("optional orderbook fields not parsed: %+v", candles[0])
	}
}

func TestReadReversalReplayCandlesMissingOrderbookIsUnavailable(t *testing.T) {
	raw := `time,open,high,low,close,volume
2026-05-20T13:39:00Z,100,101,99,100.5,10
`
	candles, err := ReadReversalReplayCandlesCSV(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	input := replayOrderbookFromCandle(candles[0])
	if input.Available {
		t.Fatalf("missing optional orderbook fields should be unavailable: %+v", input)
	}
}

func TestReadReversalReplayCandlesOrderbookDiagnosticsAreRealInputs(t *testing.T) {
	raw := `time,open,high,low,close,volume,bid_ask_imbalance,spread_bps,orderbook_ratio
2026-05-20T13:39:00Z,100,101,99,100.5,10,-0.8,2.5,0.2
`
	candles, err := ReadReversalReplayCandlesCSV(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	input := replayOrderbookFromCandle(candles[0])
	if !input.Available {
		t.Fatalf("real diagnostic orderbook fields should mark input available")
	}
	if input.Imbalance != -0.8 || input.SpreadBps != 2.5 || input.Ratio != 0.2 {
		t.Fatalf("diagnostic fields not parsed: %+v", input)
	}
}

func TestOrderbookDiagnosticSnapshotSerialization(t *testing.T) {
	ts := time.Unix(1700000300, 0).UTC()
	snap := ReversalOrderbookDiagnosticFromInput("BTCUSDT", ts, ReversalOrderbookInput{
		Available:   true,
		BestBid:     100,
		BestAsk:     100.1,
		SpreadBps:   10,
		BestBidSize: 2,
		BestAskSize: 1,
		Imbalance:   0.3333,
		Ratio:       2,
		DepthLevels: 5,
	}, orderbookDiagnosticLiveSource)
	raw, err := json.Marshal(snap.Event())
	if err != nil {
		t.Fatalf("marshal diagnostic snapshot: %v", err)
	}
	text := string(raw)
	for _, want := range []string{`"event":"orderbook_diagnostic_snapshot"`, `"source":"live_orderbook_snapshot"`, `"best_bid":100`, `"best_ask":100.1`, `"best_bid_size":2`, `"best_ask_size":1`, `"order_sent":false`} {
		if !strings.Contains(text, want) {
			t.Fatalf("serialized orderbook diagnostic missing %s: %s", want, text)
		}
	}
}

func TestOrderbookUnavailableDiagnosticExplicit(t *testing.T) {
	ts := time.Unix(1700000310, 0).UTC()
	snap := ReversalOrderbookDiagnosticFromInput("BTCUSDT", ts, ReversalOrderbookInput{
		UnavailableReason: "orderbook websocket not ready",
	}, orderbookDiagnosticReplay)
	event := snap.Event()
	if event["orderbook_available"] != false {
		t.Fatalf("orderbook availability should be false: %+v", event)
	}
	if event["reason"] != "orderbook websocket not ready" {
		t.Fatalf("missing unavailable reason: %+v", event)
	}
}

func TestReplayWarmupCandlesLoadedButTargetFiltered(t *testing.T) {
	from := time.Date(2026, 5, 20, 13, 39, 0, 0, time.UTC)
	frames := []ReversalReplayFrame{
		{Time: from.Add(-2 * time.Minute), Snapshot: &models.IndicatorSnapshot{Time: from.Add(-2 * time.Minute), Close: 100}},
		{Time: from.Add(-time.Minute), Snapshot: &models.IndicatorSnapshot{Time: from.Add(-time.Minute), Close: 101}},
		{Time: from, Snapshot: &models.IndicatorSnapshot{Time: from, Close: 102}},
	}
	opts := replayOptions(false)
	opts.From = from
	opts.To = from.Add(time.Minute)
	result, err := RunReversalReplayFrames(frames, opts)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if result.Summary.CandlesLoaded != 3 || result.Summary.WarmupCandles != 2 || result.Summary.TargetCandles != 1 {
		t.Fatalf("unexpected warmup/target counts: %+v", result.Summary)
	}
}

func TestReversalBlockerReportContainsTimelineAndRejectReasons(t *testing.T) {
	result, err := RunReversalReplayFrames(replayReversalFramesWithMomentumDelta(t, replayReadyOrderbook(), 1), replayOptions(false))
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var buf bytes.Buffer
	if err := WriteReversalBlockerMarkdown(&buf, result, enabledReversalLongConfig()); err != nil {
		t.Fatalf("write blocker report: %v", err)
	}
	text := buf.String()
	for _, want := range []string{"## Timeline", "reject_reason", "no_rsi_recovery"} {
		if !strings.Contains(text, want) {
			t.Fatalf("blocker report missing %q:\n%s", want, text)
		}
	}
}

func TestReversalClosestCandidateReportClassifiesFailures(t *testing.T) {
	ts := time.Unix(1700000500, 0).UTC()
	result := ReversalReplayResult{
		Summary: ReversalReplaySummary{Symbol: "BTCUSDT", From: ts, To: ts, Blockers: map[string]int{ReversalReasonNoRSIRecovery: 1}},
		Events: []map[string]any{{
			"event":                "reversal_long_evaluation",
			"time":                 ts,
			"score":                0.55,
			"score_rsi_oversold":   0.20,
			"score_volume_spike":   0.0,
			"score_bb_reclaim":     0.20,
			"score_rsi_recovery":   0.0,
			"score_macd_recovery":  0.15,
			"score_no_new_low":     0.0,
			"rsi_delta":            2.0,
			"volume_ratio":         0.8,
			"bars_without_new_low": 0,
			"state":                string(RevReclaimedBB),
			"reject_reason":        ReversalReasonNoRSIRecovery,
			"htf_allowed":          true,
			"htf_override_allowed": true,
			"orderbook_available":  false,
			"orderbook_allowed":    true,
			"candidate_ready":      false,
		}},
	}
	analysis := BuildReversalBlockerAnalysis(result, enabledReversalLongConfig())
	if analysis.MaxScore != 0.55 || !analysis.MaxScoreTime.Equal(ts) {
		t.Fatalf("unexpected closest score/time: %+v", analysis)
	}
	failed := strings.Join(analysis.ClosestCandidate.FailedConditions, " ")
	if !strings.Contains(failed, "rsi_recovery") || !strings.Contains(failed, "no_new_low") || !strings.Contains(failed, "orderbook: unavailable") {
		t.Fatalf("missing failed conditions: %+v", analysis.ClosestCandidate.FailedConditions)
	}
}

func TestEpisodeClassificationFalseReversalInvalidated(t *testing.T) {
	result := replayInvalidatedSetupResult()
	cfg := enabledReversalLongConfig()
	episodes := BuildReversalEpisodeSummaries(result, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 3, InvalidationATRMult: 0.1})
	if len(episodes) != 1 {
		t.Fatalf("episodes len got %d want 1: %+v", len(episodes), episodes)
	}
	episode := episodes[0]
	if episode.Classification != EpisodeFalseReversalInvalidated {
		t.Fatalf("classification got %q want %q: %+v", episode.Classification, EpisodeFalseReversalInvalidated, episode)
	}
	if episode.MainInvalidationReason != "substantial_new_low" || episode.InvalidationAfterBars != 1 {
		t.Fatalf("unexpected invalidation details: %+v", episode)
	}
}

func TestBlockerMainBecomesSetupInvalidatedWhenMemoryReclaimInvalidates(t *testing.T) {
	result := replayInvalidatedSetupResult()
	analysis := BuildReversalBlockerAnalysis(result, enabledReversalLongConfig(), ReversalSetupMemoryOptions{SetupMemoryBars: 3, InvalidationATRMult: 0.1})
	if analysis.MainBlocker != reversalMainBlockerSetupInvalidated {
		t.Fatalf("main blocker got %q want setup_invalidated: %+v", analysis.MainBlocker, analysis)
	}
	if analysis.MainBlockerDetail != "substantial_new_low" {
		t.Fatalf("main blocker detail got %q", analysis.MainBlockerDetail)
	}
	if analysis.FirstSetupDetectedAt == nil || analysis.FirstInvalidationAt == nil || analysis.InvalidationAfterBars != 1 {
		t.Fatalf("missing setup/invalidation metadata: %+v", analysis)
	}
	if !analysis.ReclaimWasRemembered {
		t.Fatalf("expected reclaim_was_remembered=true")
	}
}

func TestMultiEpisodeSummaryAggregatesCounts(t *testing.T) {
	episodes := []ReversalEpisodeSummary{
		{Classification: EpisodeTrueReversalCandidate, MaxScore: 0.9, OrderbookCoveragePct: 100},
		{Classification: EpisodeFalseReversalInvalidated, MaxScore: 0.5, OrderbookCoveragePct: 50},
	}
	agg := AggregateReversalMultiEpisodeSensitivity(episodes)
	if agg.TotalEpisodes != 2 || agg.CandidateReadyEpisodes != 1 || agg.FalseInvalidatedEpisodes != 1 {
		t.Fatalf("unexpected aggregate counts: %+v", agg)
	}
	if agg.CandidateRate != 0.5 || agg.InvalidationRate != 0.5 {
		t.Fatalf("unexpected rates: %+v", agg)
	}
	if agg.AvgMaxScore != 0.7 || agg.OrderbookAvailablePct != 75 {
		t.Fatalf("unexpected averages: %+v", agg)
	}
}

func TestReversalSensitivityMinRSIDeltaRelaxationDoesNotChangeDefaults(t *testing.T) {
	defaultCfg := config.DefaultReversalLongConfig()
	opts := replayOptions(true)
	results, err := RunReversalReplaySensitivityFrames(replayReversalFramesWithMomentumDelta(t, replayReadyOrderbook(), 2), opts, ReversalSensitivityOptions{
		MinRSIDeltaList:    []float64{1, 5},
		NoNewLowBarsList:   []int{2},
		ScoreThresholdList: []float64{0.8},
		MinVolumeRatioList: []float64{2},
	})
	if err != nil {
		t.Fatalf("sensitivity: %v", err)
	}
	if config.DefaultReversalLongConfig().MinRSIDelta != defaultCfg.MinRSIDelta {
		t.Fatalf("sensitivity changed default config")
	}
	if len(results) != 2 {
		t.Fatalf("results len got %d want 2", len(results))
	}
	if results[0].CandidateReadyCount == 0 {
		t.Fatalf("expected relaxed MinRSIDelta to produce candidate: %+v", results)
	}
	if results[1].CandidateReadyCount != 0 {
		t.Fatalf("expected default MinRSIDelta to keep candidate blocked: %+v", results)
	}
	for _, result := range results {
		if result.OrdersSent != 0 {
			t.Fatalf("sensitivity sent order: %+v", result)
		}
	}
}

func TestReversalSensitivityNoNewLowRelaxation(t *testing.T) {
	opts := replayOptions(true)
	results, err := RunReversalReplaySensitivityFrames(replayShortReversalFrames(t, replayReadyOrderbook()), opts, ReversalSensitivityOptions{
		MinRSIDeltaList:    []float64{5},
		NoNewLowBarsList:   []int{1, 3},
		ScoreThresholdList: []float64{0.8},
		MinVolumeRatioList: []float64{2},
	})
	if err != nil {
		t.Fatalf("sensitivity: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results len got %d want 2", len(results))
	}
	if results[0].CandidateReadyCount == 0 {
		t.Fatalf("expected NoNewLowBars=1 to produce candidate: %+v", results)
	}
	if results[1].CandidateReadyCount != 0 {
		t.Fatalf("expected NoNewLowBars=3 to block candidate: %+v", results)
	}
}

func TestReversalCandidateOutcomeComputesForwardAndTPSL(t *testing.T) {
	start := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	candles := []ReversalReplayCandle{
		{Time: start, Open: 100, High: 100, Low: 100, Close: 100},
		{Time: start.Add(time.Minute), Open: 100, High: 100.6, Low: 99.8, Close: 100.5},
		{Time: start.Add(2 * time.Minute), Open: 100.5, High: 100.8, Low: 99.5, Close: 100},
		{Time: start.Add(5 * time.Minute), Open: 100, High: 101, Low: 100, Close: 101},
		{Time: start.Add(15 * time.Minute), Open: 101, High: 101.5, Low: 100.8, Close: 101.5},
		{Time: start.Add(30 * time.Minute), Open: 101.5, High: 102, Low: 101, Close: 102},
	}
	rows := []ReversalReplayTimelineRow{{
		Time:           start,
		Close:          100,
		CandidateReady: true,
	}}
	outcomes, summaries := evaluateReversalCandidateOutcomes(candles, rows, 0, ReversalSensitivityOptions{
		OutcomeTPBpsList:   []float64{50},
		OutcomeSLBpsList:   []float64{30},
		OutcomeFeeBps:      1,
		OutcomeSlippageBps: 1,
	})
	if len(outcomes) != 1 {
		t.Fatalf("outcomes len got %d want 1: %+v", len(outcomes), outcomes)
	}
	outcome := outcomes[0]
	if math.Abs(outcome.ForwardReturn5mBps-100) > 1e-9 || math.Abs(outcome.ForwardReturn15mBps-150) > 1e-9 || math.Abs(outcome.ForwardReturn30mBps-200) > 1e-9 {
		t.Fatalf("unexpected forward returns: %+v", outcome)
	}
	if math.Abs(outcome.MaxFavorable30mBps-200) > 1e-9 || math.Abs(outcome.MaxAdverse30mBps+50) > 1e-9 {
		t.Fatalf("unexpected mfe/mae: %+v", outcome)
	}
	if outcome.TimeToMFE != "30m0s" || outcome.TimeToMAE != "2m0s" {
		t.Fatalf("unexpected mfe/mae timing: %+v", outcome)
	}
	if !outcome.WouldHitTP || !outcome.WouldHitSL || outcome.HitOrder != "tp_first" {
		t.Fatalf("unexpected tp/sl hit diagnostics: %+v", outcome)
	}
	if math.Abs(outcome.NetReturnBps-48) > 1e-9 || math.Abs(outcome.NetR-1.6) > 1e-9 {
		t.Fatalf("unexpected net result: %+v", outcome)
	}
	if len(summaries) != 1 || summaries[0].CandidateCount != 1 || summaries[0].WinRate != 1 || math.Abs(summaries[0].AvgNetR-1.6) > 1e-9 {
		t.Fatalf("unexpected outcome summary: %+v", summaries)
	}
}

func TestReversalCandidateOfflineFiltersRejectReasons(t *testing.T) {
	setupTime := time.Date(2026, 5, 20, 13, 0, 0, 0, time.UTC)
	candidateBase := ReversalReplayTimelineRow{
		Time:                      setupTime.Add(time.Minute),
		Close:                     101,
		Low:                       101,
		BBPosition:                bbPositionInsideAboveMiddle,
		BarsWithoutNewLow:         2,
		OrderbookRatio:            1.4,
		SetupDetectedAt:           &setupTime,
		CandidateReady:            true,
		SetupMemoryCandidateReady: true,
	}
	rows := []ReversalReplayTimelineRow{
		{
			Time:            setupTime,
			Close:           100,
			Low:             100,
			BBPosition:      bbPositionInsideBelowMiddle,
			SetupDetectedAt: &setupTime,
		},
		candidateBase,
	}
	opts := ReversalCandidateFilterOptions{
		RequireOrderbookNotHostile:       true,
		MinOrderbookRatio:                1.2,
		MinBarsWithoutNewLowAfterReclaim: 2,
		RequireCloseAboveMiddleBB:        true,
		RequireHigherLowAfterReclaim:     true,
	}
	filtered, rejects := filterReversalCandidateRows(rows, []ReversalReplayTimelineRow{candidateBase}, opts)
	if len(filtered) != 1 || len(rejects) != 0 {
		t.Fatalf("expected baseline candidate to pass filters, filtered=%+v rejects=%+v", filtered, rejects)
	}

	hostile := candidateBase
	hostile.Time = hostile.Time.Add(time.Minute)
	hostile.OrderbookHostile = true
	ratio := candidateBase
	ratio.Time = ratio.Time.Add(2 * time.Minute)
	ratio.OrderbookRatio = 1.1
	bars := candidateBase
	bars.Time = bars.Time.Add(3 * time.Minute)
	bars.BarsWithoutNewLow = 1
	middle := candidateBase
	middle.Time = middle.Time.Add(4 * time.Minute)
	middle.BBPosition = bbPositionInsideBelowMiddle
	higherLow := candidateBase
	higherLow.Time = higherLow.Time.Add(5 * time.Minute)
	higherLow.Low = 100

	filtered, rejects = filterReversalCandidateRows(rows, []ReversalReplayTimelineRow{hostile, ratio, bars, middle, higherLow}, opts)
	if len(filtered) != 0 {
		t.Fatalf("expected all candidates to be rejected, got %+v", filtered)
	}
	for reason, want := range map[string]int{
		"orderbook_hostile":                            1,
		"orderbook_ratio_below_min":                    1,
		"bars_without_new_low_after_reclaim_below_min": 1,
		"close_not_above_middle_bb":                    1,
		"no_higher_low_after_reclaim":                  1,
	} {
		if rejects[reason] != want {
			t.Fatalf("reject reason %s got %d want %d: %+v", reason, rejects[reason], want, rejects)
		}
	}
}

func TestReplaySetupMemoryReclaimAndNewLowSameCandleDoesNotTrigger(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.NoNewLowBars = 1
	cfg.ScoreThreshold = 0.7
	rows := []ReversalReplayTimelineRow{replaySetupRow(time.Unix(1700000600, 0).UTC())}
	stats := applyReplaySetupMemory(rows, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 3, InvalidationATRMult: 0.2})
	if stats.SetupDetectedCount != 1 {
		t.Fatalf("setup count got %d want 1", stats.SetupDetectedCount)
	}
	if stats.CandidateReadyCount != 0 || rows[0].SetupMemoryCandidateReady {
		t.Fatalf("same-candle setup should not trigger candidate: stats=%+v row=%+v", stats, rows[0])
	}
}

func TestReplaySetupMemoryReclaimRememberedForNBars(t *testing.T) {
	cfg := enabledReversalLongConfig()
	rows := []ReversalReplayTimelineRow{
		replaySetupRow(time.Unix(1700000700, 0).UTC()),
		replayTriggerRow(time.Unix(1700000760, 0).UTC(), 101),
	}
	stats := applyReplaySetupMemory(rows, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 2, InvalidationATRMult: 0.2})
	if stats.SetupDetectedCount != 1 {
		t.Fatalf("setup count got %d want 1", stats.SetupDetectedCount)
	}
	if !rows[1].MemoryReclaimUsed || rows[1].SetupDetectedAt == nil || rows[1].SetupAgeBars != 1 {
		t.Fatalf("expected remembered reclaim on trigger row: %+v", rows[1])
	}
}

func TestReplaySetupMemoryTriggerAfterNoNewLowStabilization(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.NoNewLowBars = 1
	cfg.ScoreThreshold = 0.7
	rows := []ReversalReplayTimelineRow{
		replaySetupRow(time.Unix(1700000800, 0).UTC()),
		replayTriggerRow(time.Unix(1700000860, 0).UTC(), 101),
	}
	stats := applyReplaySetupMemory(rows, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 2, InvalidationATRMult: 0.2})
	if stats.CandidateReadyCount != 1 {
		t.Fatalf("expected trigger after stabilization: stats=%+v rows=%+v", stats, rows)
	}
	if !rows[1].SetupMemoryCandidateReady {
		t.Fatalf("trigger row not marked candidate: %+v", rows[1])
	}
}

func TestReplaySetupMemoryInvalidatedByLowerLow(t *testing.T) {
	cfg := enabledReversalLongConfig()
	rows := []ReversalReplayTimelineRow{
		replaySetupRow(time.Unix(1700000900, 0).UTC()),
		replayTriggerRow(time.Unix(1700000960, 0).UTC(), 98),
	}
	stats := applyReplaySetupMemory(rows, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 3, InvalidationATRMult: 0.1})
	if stats.SetupInvalidatedCount != 1 {
		t.Fatalf("expected invalidation by lower low: stats=%+v rows=%+v", stats, rows)
	}
	if !rows[1].SetupInvalidated || rows[1].InvalidationReason != "substantial_new_low" {
		t.Fatalf("unexpected invalidation row: %+v", rows[1])
	}
}

func TestReplaySetupMemoryDoesNotChangeProductionDefaults(t *testing.T) {
	before := config.DefaultReversalLongConfig()
	cfg := enabledReversalLongConfig()
	rows := []ReversalReplayTimelineRow{
		replaySetupRow(time.Unix(1700001000, 0).UTC()),
		replayTriggerRow(time.Unix(1700001060, 0).UTC(), 101),
	}
	applyReplaySetupMemory(rows, cfg, ReversalSetupMemoryOptions{SetupMemoryBars: 2, InvalidationATRMult: 0.2})
	after := config.DefaultReversalLongConfig()
	if before != after {
		t.Fatalf("setup memory changed production defaults: before=%+v after=%+v", before, after)
	}
}

func replaySetupRow(ts time.Time) ReversalReplayTimelineRow {
	return ReversalReplayTimelineRow{
		Time:              ts,
		Close:             100,
		Low:               100,
		ATR:               10,
		RSI:               25,
		RSIDelta:          2,
		MACDHistDelta:     0.1,
		BBPosition:        bbPositionInsideBelowMiddle,
		WasBelowLowerBB:   true,
		ReturnedInsideBB:  true,
		Score:             reversalScoreRSIOversold + reversalScoreBBReclaim + reversalScoreMACDRecovery,
		ScoreRSIOversold:  reversalScoreRSIOversold,
		ScoreBBReclaim:    reversalScoreBBReclaim,
		ScoreMACDRecovery: reversalScoreMACDRecovery,
		HTFAllowed:        true,
		OrderbookAllowed:  true,
	}
}

func replayInvalidatedSetupResult() ReversalReplayResult {
	ts := time.Unix(1700000550, 0).UTC()
	return ReversalReplayResult{
		Summary: ReversalReplaySummary{
			Symbol:   "BTCUSDT",
			From:     ts,
			To:       ts.Add(time.Minute),
			Blockers: map[string]int{ReversalReasonNoLowerBBReclaim: 1},
		},
		Events: []map[string]any{
			{
				"event":               "reversal_long_evaluation",
				"time":                ts,
				"score":               0.55,
				"score_rsi_oversold":  reversalScoreRSIOversold,
				"score_bb_reclaim":    reversalScoreBBReclaim,
				"score_macd_recovery": reversalScoreMACDRecovery,
				"low":                 100.0,
				"atr":                 10.0,
				"rsi_delta":           2.0,
				"macd_hist_delta":     0.1,
				"bb_position":         bbPositionInsideBelowMiddle,
				"was_below_lower_bb":  true,
				"returned_inside_bb":  true,
				"state":               string(RevReclaimedBB),
				"reject_reason":       ReversalReasonNoRSIRecovery,
				"htf_allowed":         true,
				"orderbook_available": true,
				"orderbook_allowed":   true,
				"candidate_ready":     false,
			},
			{
				"event":               "reversal_long_evaluation",
				"time":                ts.Add(time.Minute),
				"score":               0.35,
				"low":                 98.0,
				"atr":                 10.0,
				"rsi_delta":           6.0,
				"macd_hist_delta":     0.2,
				"bb_position":         bbPositionInsideBelowMiddle,
				"was_below_lower_bb":  false,
				"returned_inside_bb":  false,
				"state":               string(RevReclaimedBB),
				"reject_reason":       ReversalReasonNoLowerBBReclaim,
				"htf_allowed":         true,
				"orderbook_available": true,
				"orderbook_allowed":   true,
				"candidate_ready":     false,
			},
		},
	}
}

func replayTriggerRow(ts time.Time, low float64) ReversalReplayTimelineRow {
	return ReversalReplayTimelineRow{
		Time:              ts,
		Close:             101,
		Low:               low,
		ATR:               10,
		RSI:               35,
		RSIDelta:          6,
		MACDHistDelta:     0.2,
		BBPosition:        bbPositionInsideBelowMiddle,
		Score:             reversalScoreRSIRecovery + reversalScoreMACDRecovery,
		ScoreRSIRecovery:  reversalScoreRSIRecovery,
		ScoreMACDRecovery: reversalScoreMACDRecovery,
		HTFAllowed:        true,
		OrderbookAllowed:  true,
	}
}

func replayHasEvent(result ReversalReplayResult, event string) bool {
	for _, item := range result.Events {
		if item["event"] == event {
			return true
		}
	}
	return false
}

func replayHasRuntimeReason(result ReversalReplayResult, reason string) bool {
	for _, item := range result.Events {
		if item["event"] == "reversal_long_runtime_gate" && item["runtime_reject_reason"] == reason {
			return true
		}
	}
	return false
}
