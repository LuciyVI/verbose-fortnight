package strategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func enabledReversalLongConfig() config.ReversalLongConfig {
	cfg := config.DefaultReversalLongConfig()
	cfg.Enabled = true
	cfg.MinVolumeRatio = 2
	cfg.OversoldRSI = 30
	cfg.MinRSIDelta = 5
	cfg.RequireMACDHistRecovery = true
	cfg.MinMACDHistDelta = 0.1
	cfg.MaxEntryRSI = 45
	cfg.NoNewLowBars = 2
	cfg.ScoreThreshold = 0.8
	cfg.TimeoutBars = 3
	return cfg
}

func TestPeriodicOrderbookSnapshotRecorderWritesAvailableSnapshot(t *testing.T) {
	tr := newTestTrader()
	tr.Config.Symbol = "BTCUSDT"
	tr.Config.OrderbookSnapshotJSONLPath = filepath.Join(t.TempDir(), "orderbook_snapshots.jsonl")
	tr.Config.OrderbookSnapshotIntervalMS = 10
	tr.State.BidsMap = map[string]float64{
		"100.00": 2,
		"99.90":  3,
	}
	tr.State.AsksMap = map[string]float64{
		"100.10": 1,
		"100.20": 1,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !tr.StartOrderbookSnapshotRecorder(ctx) {
		t.Fatalf("expected recorder to start when JSONL path is configured")
	}
	event := waitForOrderbookSnapshotEvent(t, tr.Config.OrderbookSnapshotJSONLPath)

	health := tr.State.GetOrderbookSnapshotHealth()
	if health.OrderbookSnapshotRecordedTotal == 0 || health.OrderbookSnapshotAvailableTotal == 0 || health.OrderbookSnapshotUnavailableTotal != 0 {
		t.Fatalf("unexpected orderbook health metrics: %+v", health)
	}
	if health.LastOrderbookSnapshotTime.IsZero() || health.LastOrderbookUnavailableReason != "" {
		t.Fatalf("unexpected last orderbook health fields: %+v", health)
	}
	if event["event"] != "orderbook_diagnostic_snapshot" || event["source"] != "live_orderbook_snapshot" {
		t.Fatalf("unexpected diagnostic event identity: %+v", event)
	}
	if event["orderbook_available"] != true || event["order_sent"] != false {
		t.Fatalf("diagnostic event should be available and order-free: %+v", event)
	}
	if event["best_bid"].(float64) != 100 || event["best_ask"].(float64) != 100.1 ||
		event["best_bid_size"].(float64) != 2 || event["best_ask_size"].(float64) != 1 ||
		event["depth_levels"].(float64) != float64(tr.Config.OrderbookLevels) {
		t.Fatalf("diagnostic event did not contain real top-book values: %+v", event)
	}
	if event["spread_bps"].(float64) <= 0 || event["bid_ask_imbalance"].(float64) <= 0 || event["orderbook_ratio"].(float64) <= 0 {
		t.Fatalf("diagnostic event missing computed orderbook features: %+v", event)
	}
}

func TestOrderbookSnapshotRecorderWritesUnavailableSnapshot(t *testing.T) {
	tr := newTestTrader()
	tr.Config.Symbol = "BTCUSDT"
	tr.Config.OrderbookSnapshotJSONLPath = filepath.Join(t.TempDir(), "orderbook_snapshots.jsonl")
	ts := time.Date(2026, 5, 22, 12, 1, 0, 0, time.UTC)

	if !tr.RecordLiveOrderbookDiagnosticSnapshot(ts) {
		t.Fatalf("expected one-shot recorder to write when JSONL path is configured")
	}
	event := readFirstOrderbookSnapshotEvent(t, tr.Config.OrderbookSnapshotJSONLPath)
	if event["orderbook_available"] != false {
		t.Fatalf("unavailable book should write unavailable snapshot: %+v", event)
	}
	if event["reason"] != ReversalReasonOrderbookNoData || event["unavailable_reason"] != ReversalReasonOrderbookNoData {
		t.Fatalf("unavailable reason missing: %+v", event)
	}
	health := tr.State.GetOrderbookSnapshotHealth()
	if health.OrderbookSnapshotRecordedTotal != 1 || health.OrderbookSnapshotAvailableTotal != 0 || health.OrderbookSnapshotUnavailableTotal != 1 {
		t.Fatalf("unexpected unavailable health metrics: %+v", health)
	}
	if !health.LastOrderbookSnapshotTime.Equal(ts) || health.LastOrderbookUnavailableReason != ReversalReasonOrderbookNoData {
		t.Fatalf("unexpected unavailable health fields: %+v", health)
	}
}

func TestOrderbookSnapshotJSONLEnvCreatesFileOnFirstWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshots", "orderbook.jsonl")
	t.Setenv("ORDERBOOK_SNAPSHOT_JSONL", path)
	tr := newTestTrader()

	if !tr.RecordLiveOrderbookDiagnosticSnapshot(time.Date(2026, 5, 22, 12, 2, 0, 0, time.UTC)) {
		t.Fatalf("expected ORDERBOOK_SNAPSHOT_JSONL to enable recorder")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected JSONL file to be created on first write: %v", err)
	}
}

func TestOrderbookSnapshotRecorderDoesNotEnableTrading(t *testing.T) {
	tr := newTestTrader()
	tr.Config.OrderbookSnapshotJSONLPath = filepath.Join(t.TempDir(), "orderbook.jsonl")
	tr.State.SigChan = make(chan models.Signal, 1)
	beforeEnabled := tr.Config.ReversalLong.Enabled
	beforeTradeEnabled := tr.Config.ReversalLong.TradeEnabled

	if !tr.RecordLiveOrderbookDiagnosticSnapshot(time.Date(2026, 5, 22, 12, 3, 0, 0, time.UTC)) {
		t.Fatalf("expected diagnostic recorder to write")
	}
	if tr.Config.ReversalLong.Enabled != beforeEnabled || tr.Config.ReversalLong.TradeEnabled != beforeTradeEnabled {
		t.Fatalf("recorder changed trading config: before enabled=%t trade=%t after=%+v", beforeEnabled, beforeTradeEnabled, tr.Config.ReversalLong)
	}
	if len(tr.State.SigChan) != 0 {
		t.Fatalf("recorder emitted a signal")
	}
}

func waitForOrderbookSnapshotEvent(t *testing.T, path string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(raw)) != "" {
			return parseFirstOrderbookSnapshotEvent(t, raw)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for orderbook snapshot JSONL at %s", path)
	return nil
}

func readFirstOrderbookSnapshotEvent(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read orderbook jsonl: %v", err)
	}
	return parseFirstOrderbookSnapshotEvent(t, raw)
}

func parseFirstOrderbookSnapshotEvent(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		t.Fatalf("orderbook jsonl is empty")
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal orderbook jsonl event: %v\n%s", err, raw)
	}
	return event
}

func reversalLongSnapshot() models.IndicatorSnapshot {
	return models.IndicatorSnapshot{
		Time:                   time.Unix(1700000000, 0),
		Close:                  100,
		Low:                    99,
		RSI:                    50,
		RSIPrev:                45,
		RSIDelta:               0,
		RSIDeltaAvailable:      true,
		MACDHist:               0,
		MACDHistPrev:           -0.1,
		MACDHistDelta:          0,
		MACDHistDeltaAvailable: true,
		Volume:                 100,
		VolumeSMA:              100,
		VolumeRatio:            1,
		VolumeAvailable:        true,
		BBLower:                95,
		BBMiddle:               100,
		BBUpper:                105,
		BBPosition:             bbPositionInsideAboveMiddle,
		BBReclaimAvailable:     true,
	}
}

func TestReversalLongDetectorDisabledDoesNotProgress(t *testing.T) {
	detector := NewReversalLongDetector(config.DefaultReversalLongConfig())
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 20
	snapshot.VolumeRatio = 5
	snapshot.BBPosition = bbPositionBelowLower
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDelta = 10
	snapshot.MACDHistDelta = 1

	evaluation := detector.Evaluate("BTCUSDT", snapshot, time.Unix(1700000001, 0))
	if evaluation.CandidateReady {
		t.Fatalf("disabled detector returned candidate_ready")
	}
	if evaluation.RejectReason != ReversalReasonDisabled {
		t.Fatalf("reject reason got %q want %q", evaluation.RejectReason, ReversalReasonDisabled)
	}
	if evaluation.State != RevIdle {
		t.Fatalf("state got %q want %q", evaluation.State, RevIdle)
	}
	if len(detector.state) != 0 {
		t.Fatalf("disabled detector created state: %+v", detector.state)
	}
}

func TestReversalLongDetectorIdleToCapitulatedByRSI(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 25

	evaluation := detector.Evaluate("BTCUSDT", snapshot, time.Unix(1700000001, 0))
	if evaluation.State != RevCapitulated {
		t.Fatalf("state got %q want %q", evaluation.State, RevCapitulated)
	}
	if evaluation.CandidateReady {
		t.Fatalf("capitulated state should not be candidate_ready")
	}
}

func TestReversalLongDetectorIdleToCapitulatedByVolumeRatio(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 50
	snapshot.VolumeAvailable = true
	snapshot.VolumeRatio = 2.5

	evaluation := detector.Evaluate("BTCUSDT", snapshot, time.Unix(1700000001, 0))
	if evaluation.State != RevCapitulated {
		t.Fatalf("state got %q want %q", evaluation.State, RevCapitulated)
	}
}

func TestReversalLongVolumeSpikeScoreRequiresCurrentThreshold(t *testing.T) {
	cfg := enabledReversalLongConfig()
	detector := NewReversalLongDetector(cfg)
	state := ReversalLongState{State: RevMomentumTurn, HadVolumeSpike: true}
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.RSIDelta = cfg.MinRSIDelta + 1
	snapshot.MACDHistDelta = cfg.MinMACDHistDelta + 0.1
	snapshot.VolumeAvailable = true
	snapshot.VolumeRatio = cfg.MinVolumeRatio - 0.1

	checks := detector.checks(snapshot, state, ReversalHTFInput{}, ReversalOrderbookInput{})
	if checks.ScoreBreakdown.VolumeSpike != 0 {
		t.Fatalf("volume spike score got %.2f want 0 below threshold", checks.ScoreBreakdown.VolumeSpike)
	}
	if checks.VolumeSpikePassed {
		t.Fatalf("volume_spike_passed got true below threshold")
	}
	if !checks.VolumeRatioAvailable {
		t.Fatalf("volume_ratio_available got false for available volume data")
	}
	debug := reversalLongDebug(detector, snapshot, state, checks)
	if floatFromDebug(debug, "min_volume_ratio") != cfg.MinVolumeRatio {
		t.Fatalf("min_volume_ratio diagnostic got %.2f want %.2f", floatFromDebug(debug, "min_volume_ratio"), cfg.MinVolumeRatio)
	}
	if boolFromDebug(debug, "volume_spike_passed") {
		t.Fatalf("volume_spike_passed diagnostic got true below threshold")
	}

	snapshot.VolumeRatio = cfg.MinVolumeRatio
	checks = detector.checks(snapshot, state, ReversalHTFInput{}, ReversalOrderbookInput{})
	if checks.ScoreBreakdown.VolumeSpike <= 0 {
		t.Fatalf("volume spike score got %.2f want > 0 at threshold", checks.ScoreBreakdown.VolumeSpike)
	}
	if !checks.VolumeSpikePassed {
		t.Fatalf("volume_spike_passed got false at threshold")
	}

	snapshot.VolumeAvailable = false
	snapshot.VolumeRatio = cfg.MinVolumeRatio * 2
	checks = detector.checks(snapshot, state, ReversalHTFInput{}, ReversalOrderbookInput{})
	if checks.ScoreBreakdown.VolumeSpike != 0 {
		t.Fatalf("volume spike score got %.2f want 0 when unavailable", checks.ScoreBreakdown.VolumeSpike)
	}
	if checks.VolumeRatioAvailable {
		t.Fatalf("volume_ratio_available got true when unavailable")
	}
}

func TestReversalLongCandidateDoesNotUseRememberedFailedVolumeSpikeScore(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.ScoreThreshold = 0.8
	detector := NewReversalLongDetector(cfg)
	now := time.Unix(1700000001, 0)

	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 50
	capSnapshot.Low = 89
	capSnapshot.VolumeAvailable = true
	capSnapshot.VolumeRatio = cfg.MinVolumeRatio + 0.5
	capSnapshot.BBPosition = bbPositionBelowLower
	capSnapshot.WasBelowLowerBB = true
	capSnapshot.ReturnedInsideBB = false
	detector.Evaluate("BTCUSDT", capSnapshot, now)

	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.RSI = 38
	reclaimedSnapshot.Low = 90
	reclaimedSnapshot.VolumeAvailable = true
	reclaimedSnapshot.VolumeRatio = cfg.MinVolumeRatio - 0.1
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	detector.Evaluate("BTCUSDT", reclaimedSnapshot, now.Add(2*time.Minute))

	momentumSnapshot := reclaimedSnapshot
	momentumSnapshot.RSI = 42
	momentumSnapshot.Low = 91
	momentumSnapshot.RSIDeltaAvailable = true
	momentumSnapshot.RSIDelta = cfg.MinRSIDelta + 1
	momentumSnapshot.MACDHistDeltaAvailable = true
	momentumSnapshot.MACDHistDelta = cfg.MinMACDHistDelta + 0.1
	evaluation := detector.Evaluate("BTCUSDT", momentumSnapshot, now.Add(3*time.Minute))

	if evaluation.CandidateReady {
		t.Fatalf("unexpected candidate_ready with current volume below threshold")
	}
	if evaluation.RejectReason != ReversalReasonScoreBelowThreshold {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonScoreBelowThreshold)
	}
	if got := floatFromDebug(evaluation.Debug, "score_volume_spike"); got != 0 {
		t.Fatalf("score_volume_spike got %.2f want 0", got)
	}
	if evaluation.Score >= cfg.ScoreThreshold {
		t.Fatalf("score got %.2f want below threshold %.2f", evaluation.Score, cfg.ScoreThreshold)
	}
}

func TestReversalLongDetectorBelowLowerBB(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	detector.Evaluate("BTCUSDT", capSnapshot, now)

	belowSnapshot := reversalLongSnapshot()
	belowSnapshot.BBPosition = bbPositionBelowLower
	evaluation := detector.Evaluate("BTCUSDT", belowSnapshot, now.Add(time.Minute))

	if evaluation.State != RevBelowBB {
		t.Fatalf("state got %q want %q", evaluation.State, RevBelowBB)
	}
}

func TestReversalLongDetectorReclaimedBB(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	detector.Evaluate("BTCUSDT", capSnapshot, now)

	belowSnapshot := reversalLongSnapshot()
	belowSnapshot.BBPosition = bbPositionBelowLower
	detector.Evaluate("BTCUSDT", belowSnapshot, now.Add(time.Minute))

	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	evaluation := detector.Evaluate("BTCUSDT", reclaimedSnapshot, now.Add(2*time.Minute))

	if evaluation.State != RevReclaimedBB {
		t.Fatalf("state got %q want %q", evaluation.State, RevReclaimedBB)
	}
}

func advanceReversalToReclaimed(detector *ReversalLongDetector, now time.Time) {
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	capSnapshot.Low = 89
	capSnapshot.BBPosition = bbPositionBelowLower
	capSnapshot.WasBelowLowerBB = true
	detector.Evaluate("BTCUSDT", capSnapshot, now)

	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.RSI = 38
	reclaimedSnapshot.Low = 90
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	detector.Evaluate("BTCUSDT", reclaimedSnapshot, now.Add(time.Minute))
}

func TestReversalLongDetectorValidVShapeCandidate(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	capSnapshot.Low = 89
	capSnapshot.BBPosition = bbPositionBelowLower
	capSnapshot.WasBelowLowerBB = true
	capSnapshot.ReturnedInsideBB = false
	detector.Evaluate("BTCUSDT", capSnapshot, now)

	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.RSI = 38
	reclaimedSnapshot.Low = 90
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	detector.Evaluate("BTCUSDT", reclaimedSnapshot, now.Add(2*time.Minute))

	momentumSnapshot := reclaimedSnapshot
	momentumSnapshot.RSI = 42
	momentumSnapshot.Low = 91
	momentumSnapshot.RSIDeltaAvailable = true
	momentumSnapshot.RSIDelta = 6
	momentumSnapshot.MACDHistDeltaAvailable = true
	momentumSnapshot.MACDHistDelta = 0.2
	candidateEvaluation := detector.Evaluate("BTCUSDT", momentumSnapshot, now.Add(3*time.Minute))
	if candidateEvaluation.State != RevCandidate {
		t.Fatalf("state got %q want %q", candidateEvaluation.State, RevCandidate)
	}
	if !candidateEvaluation.CandidateReady {
		t.Fatalf("expected diagnostic candidate_ready")
	}
	if candidateEvaluation.RejectReason != ReversalReasonCandidateReady {
		t.Fatalf("reason got %q want %q", candidateEvaluation.RejectReason, ReversalReasonCandidateReady)
	}
	if candidateEvaluation.Score < detector.cfg.ScoreThreshold {
		t.Fatalf("score got %.2f below threshold %.2f", candidateEvaluation.Score, detector.cfg.ScoreThreshold)
	}
	if intFromDebug(candidateEvaluation.Debug, "bars_without_new_low") < detector.cfg.NoNewLowBars {
		t.Fatalf("bars_without_new_low got %d want >= %d", intFromDebug(candidateEvaluation.Debug, "bars_without_new_low"), detector.cfg.NoNewLowBars)
	}
}

func TestReversalLongDetectorNoCapitulation(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	evaluation := detector.Evaluate("BTCUSDT", reversalLongSnapshot(), time.Unix(1700000001, 0))
	if evaluation.CandidateReady {
		t.Fatalf("unexpected candidate_ready")
	}
	if evaluation.RejectReason != ReversalReasonNoCapitulation {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonNoCapitulation)
	}
}

func TestReversalLongDetectorBBDataUnavailable(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 25
	snapshot.BBReclaimAvailable = false
	snapshot.BBPosition = bbPositionNotAvailable

	evaluation := detector.Evaluate("BTCUSDT", snapshot, time.Unix(1700000001, 0))
	if evaluation.RejectReason != ReversalReasonBBDataUnavailable {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonBBDataUnavailable)
	}
}

func TestReversalLongDetectorNoLowerBBReclaim(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	snapshot := reversalLongSnapshot()
	snapshot.RSI = 25
	snapshot.BBReclaimAvailable = true
	snapshot.WasBelowLowerBB = false
	snapshot.ReturnedInsideBB = false
	snapshot.BBPosition = bbPositionInsideBelowMiddle

	evaluation := detector.Evaluate("BTCUSDT", snapshot, time.Unix(1700000001, 0))
	if evaluation.RejectReason != ReversalReasonNoLowerBBReclaim {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonNoLowerBBReclaim)
	}
}

func TestReversalLongDetectorRSITooHigh(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = detector.cfg.MaxEntryRSI + 1
	snapshot.Low = 91
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.BBPosition = bbPositionInsideAboveMiddle
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta + 1
	snapshot.MACDHistDeltaAvailable = true
	snapshot.MACDHistDelta = detector.cfg.MinMACDHistDelta + 0.1

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.RejectReason != ReversalReasonRSITooHigh {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonRSITooHigh)
	}
}

func TestReversalLongDetectorNoRSIRecovery(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.Low = 91
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta - 0.1
	snapshot.MACDHistDeltaAvailable = true
	snapshot.MACDHistDelta = detector.cfg.MinMACDHistDelta + 0.1

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.RejectReason != ReversalReasonNoRSIRecovery {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonNoRSIRecovery)
	}
}

func TestReversalLongDetectorMACDDataUnavailable(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.Low = 91
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta + 1
	snapshot.MACDHistDeltaAvailable = false

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.RejectReason != ReversalReasonMomentumUnavailable {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonMomentumUnavailable)
	}
}

func TestReversalLongDetectorNoMACDHistRecovery(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.Low = 91
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta + 1
	snapshot.MACDHistDeltaAvailable = true
	snapshot.MACDHistDelta = detector.cfg.MinMACDHistDelta - 0.01

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.RejectReason != ReversalReasonNoMACDRecovery {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonNoMACDRecovery)
	}
}

func TestReversalLongDetectorNewLowDetected(t *testing.T) {
	detector := NewReversalLongDetector(enabledReversalLongConfig())
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.Low = 88
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta + 1
	snapshot.MACDHistDeltaAvailable = true
	snapshot.MACDHistDelta = detector.cfg.MinMACDHistDelta + 0.1

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.RejectReason != ReversalReasonNewLowDetected {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonNewLowDetected)
	}
}

func TestReversalLongDetectorScoreBelowThreshold(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.ScoreThreshold = 0.95
	detector := NewReversalLongDetector(cfg)
	now := time.Unix(1700000001, 0)
	advanceReversalToReclaimed(detector, now)

	snapshot := reversalLongSnapshot()
	snapshot.RSI = 40
	snapshot.Low = 91
	snapshot.WasBelowLowerBB = true
	snapshot.ReturnedInsideBB = true
	snapshot.RSIDeltaAvailable = true
	snapshot.RSIDelta = detector.cfg.MinRSIDelta + 1
	snapshot.MACDHistDeltaAvailable = true
	snapshot.MACDHistDelta = detector.cfg.MinMACDHistDelta + 0.1

	evaluation := detector.Evaluate("BTCUSDT", snapshot, now.Add(2*time.Minute))
	if evaluation.CandidateReady {
		t.Fatalf("unexpected candidate_ready")
	}
	if evaluation.RejectReason != ReversalReasonScoreBelowThreshold {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonScoreBelowThreshold)
	}
}

func TestReversalHTFAllowsBullishOrNeutral(t *testing.T) {
	cfg := enabledReversalLongConfig()
	for _, bias := range []string{"LONG", ""} {
		result := CheckReversalHTF(cfg, ReversalHTFInput{Available: true, Bias: bias})
		if !result.Allowed {
			t.Fatalf("bias %q allowed got false reason=%q", bias, result.RejectReason)
		}
		if result.OverrideAllowed {
			t.Fatalf("bias %q override got true want false", bias)
		}
	}
}

func TestReversalHTFShortAllowedWithOverride(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.AllowHTFBearish = true
	result := CheckReversalHTF(cfg, ReversalHTFInput{Available: true, Bias: "SHORT"})
	if !result.Allowed || !result.OverrideAllowed {
		t.Fatalf("short override result %+v", result)
	}
}

func TestReversalHTFShortBlockedWhenOverrideDisabled(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.AllowHTFBearish = false
	result := CheckReversalHTF(cfg, ReversalHTFInput{Available: true, Bias: "SHORT"})
	if result.Allowed {
		t.Fatalf("expected bearish HTF block")
	}
	if result.RejectReason != ReversalReasonHTFBearishBlock {
		t.Fatalf("reason got %q want %q", result.RejectReason, ReversalReasonHTFBearishBlock)
	}
}

func TestReversalHTFExtremeDowntrendBlocks(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalHTF(cfg, ReversalHTFInput{
		Available:                 true,
		Bias:                      "LONG",
		ExtremeDowntrend:          true,
		ExtremeDowntrendAvailable: true,
	})
	if result.Allowed {
		t.Fatalf("expected extreme HTF downtrend block")
	}
	if result.RejectReason != ReversalReasonHTFExtremeBlock {
		t.Fatalf("reason got %q want %q", result.RejectReason, ReversalReasonHTFExtremeBlock)
	}
}

func TestReversalOrderbookUnavailableAllowsDiagnostic(t *testing.T) {
	result := CheckReversalOrderbookSoft(enabledReversalLongConfig(), ReversalOrderbookInput{})
	if !result.Allowed || result.Hostile {
		t.Fatalf("unavailable orderbook should not hard block diagnostic detector: %+v", result)
	}
	if boolFromDebug(result.Debug, "orderbook_available") {
		t.Fatalf("orderbook_available got true want false")
	}
}

func TestReversalOrderbookSpreadTooWide(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalOrderbookSoft(cfg, ReversalOrderbookInput{
		Available: true,
		SpreadBps: cfg.MaxSpreadBps + 0.1,
		Ratio:     1,
	})
	if result.Allowed || !result.Hostile || result.RejectReason != ReversalReasonSpreadTooWide {
		t.Fatalf("spread check result %+v", result)
	}
}

func TestReversalOrderbookAskWallTooLarge(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalOrderbookSoft(cfg, ReversalOrderbookInput{
		Available:    true,
		SpreadBps:    cfg.MaxSpreadBps - 1,
		Ratio:        1,
		AskWallRatio: cfg.MaxAskWallRatio + 0.1,
	})
	if result.Allowed || !result.Hostile || result.RejectReason != ReversalReasonAskWallTooLarge {
		t.Fatalf("ask wall check result %+v", result)
	}
}

func TestReversalOrderbookBidUnstable(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalOrderbookSoft(cfg, ReversalOrderbookInput{
		Available:             true,
		SpreadBps:             cfg.MaxSpreadBps - 1,
		Ratio:                 1,
		AskWallRatio:          1,
		BidStabilityAvailable: true,
		BidStabilityRatio:     cfg.MinBidStabilityRatio - 0.01,
	})
	if result.Allowed || !result.Hostile || result.RejectReason != ReversalReasonBidUnstable {
		t.Fatalf("bid stability check result %+v", result)
	}
}

func TestReversalOrderbookHostileImbalance(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalOrderbookSoft(cfg, ReversalOrderbookInput{
		Available:    true,
		SpreadBps:    cfg.MaxSpreadBps - 1,
		Ratio:        0.5,
		Imbalance:    -0.80,
		AskWallRatio: 1,
	})
	if result.Allowed || !result.Hostile || result.RejectReason != ReversalReasonOrderbookHostile {
		t.Fatalf("hostile imbalance result %+v", result)
	}
}

func TestReversalOrderbookNotHostileAllows(t *testing.T) {
	cfg := enabledReversalLongConfig()
	result := CheckReversalOrderbookSoft(cfg, ReversalOrderbookInput{
		Available:             true,
		SpreadBps:             cfg.MaxSpreadBps - 1,
		Ratio:                 0.8,
		Imbalance:             -0.1,
		AskWallRatio:          1.2,
		BidStabilityAvailable: true,
		BidStabilityRatio:     cfg.MinBidStabilityRatio + 0.1,
	})
	if !result.Allowed || result.Hostile {
		t.Fatalf("not hostile orderbook result %+v", result)
	}
}

func validReversalEvaluationWithSafety(t *testing.T, cfg config.ReversalLongConfig, htf ReversalHTFInput, orderbook ReversalOrderbookInput) ReversalLongEvaluation {
	t.Helper()
	detector := NewReversalLongDetector(cfg)
	now := time.Unix(1700000001, 0)
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	capSnapshot.Low = 89
	capSnapshot.BBPosition = bbPositionBelowLower
	capSnapshot.WasBelowLowerBB = true
	detector.EvaluateWithSafety("BTCUSDT", capSnapshot, now, htf, orderbook)

	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.RSI = 38
	reclaimedSnapshot.Low = 90
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	detector.EvaluateWithSafety("BTCUSDT", reclaimedSnapshot, now.Add(time.Minute), htf, orderbook)

	momentumSnapshot := reclaimedSnapshot
	momentumSnapshot.RSI = 42
	momentumSnapshot.Low = 91
	momentumSnapshot.RSIDelta = cfg.MinRSIDelta + 1
	momentumSnapshot.MACDHistDelta = cfg.MinMACDHistDelta + 0.2
	return detector.EvaluateWithSafety("BTCUSDT", momentumSnapshot, now.Add(2*time.Minute), htf, orderbook)
}

func TestReversalDetectorCandidateWithHTFShortOverrideAndNotHostileOrderbook(t *testing.T) {
	cfg := enabledReversalLongConfig()
	evaluation := validReversalEvaluationWithSafety(t, cfg,
		ReversalHTFInput{Available: true, Bias: "SHORT"},
		ReversalOrderbookInput{
			Available:             true,
			SpreadBps:             cfg.MaxSpreadBps - 1,
			Ratio:                 0.9,
			Imbalance:             -0.05,
			AskWallRatio:          1.1,
			BidStabilityAvailable: true,
			BidStabilityRatio:     cfg.MinBidStabilityRatio + 0.2,
		},
	)
	if !evaluation.CandidateReady {
		t.Fatalf("expected diagnostic candidate, got %+v", evaluation)
	}
	if !boolFromDebug(evaluation.Debug, "htf_override_allowed") {
		t.Fatalf("expected htf_override_allowed in debug: %+v", evaluation.Debug)
	}
	if !boolFromDebug(evaluation.Debug, "orderbook_allowed") {
		t.Fatalf("expected orderbook_allowed in debug: %+v", evaluation.Debug)
	}
}

func TestReversalDetectorExtremeHTFBlocksValidPattern(t *testing.T) {
	cfg := enabledReversalLongConfig()
	evaluation := validReversalEvaluationWithSafety(t, cfg,
		ReversalHTFInput{Available: true, Bias: "SHORT", ExtremeDowntrend: true, ExtremeDowntrendAvailable: true},
		ReversalOrderbookInput{},
	)
	if evaluation.CandidateReady {
		t.Fatalf("unexpected candidate_ready")
	}
	if evaluation.RejectReason != ReversalReasonHTFExtremeBlock {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonHTFExtremeBlock)
	}
}

func TestReversalDetectorHostileOrderbookBlocksValidPattern(t *testing.T) {
	cfg := enabledReversalLongConfig()
	evaluation := validReversalEvaluationWithSafety(t, cfg,
		ReversalHTFInput{Available: true, Bias: "LONG"},
		ReversalOrderbookInput{
			Available:    true,
			SpreadBps:    cfg.MaxSpreadBps - 1,
			Ratio:        0.5,
			Imbalance:    -0.85,
			AskWallRatio: 1.1,
		},
	)
	if evaluation.CandidateReady {
		t.Fatalf("unexpected candidate_ready")
	}
	if evaluation.RejectReason != ReversalReasonOrderbookHostile {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonOrderbookHostile)
	}
}

func TestReversalLongDetectorCandidateReadyIsDiagnosticOnly(t *testing.T) {
	tr := newTestTrader()
	cfg := enabledReversalLongConfig()
	tr.Config.ReversalLong = cfg
	tr.Config.Symbol = "BTCUSDT"
	tr.ReversalLongDetector = NewReversalLongDetector(cfg)
	tr.State.SigChan = make(chan models.Signal, 1)

	now := time.Unix(1700000001, 0)
	tr.Now = func() time.Time { return now }
	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	tr.evaluateReversalLongLogOnly(capSnapshot)

	tr.Now = func() time.Time { return now.Add(time.Minute) }
	belowSnapshot := reversalLongSnapshot()
	belowSnapshot.BBPosition = bbPositionBelowLower
	tr.evaluateReversalLongLogOnly(belowSnapshot)

	tr.Now = func() time.Time { return now.Add(2 * time.Minute) }
	reclaimedSnapshot := reversalLongSnapshot()
	reclaimedSnapshot.BBPosition = bbPositionInsideBelowMiddle
	reclaimedSnapshot.WasBelowLowerBB = true
	reclaimedSnapshot.ReturnedInsideBB = true
	tr.evaluateReversalLongLogOnly(reclaimedSnapshot)

	tr.Now = func() time.Time { return now.Add(3 * time.Minute) }
	momentumSnapshot := reclaimedSnapshot
	momentumSnapshot.RSIDelta = 6
	momentumSnapshot.MACDHistDelta = 0.2
	tr.evaluateReversalLongLogOnly(momentumSnapshot)

	tr.Now = func() time.Time { return now.Add(4 * time.Minute) }
	tr.evaluateReversalLongLogOnly(momentumSnapshot)

	if len(tr.State.SigChan) != 0 {
		t.Fatalf("diagnostic reversal candidate emitted a signal")
	}
	tr.State.StatusLock.RLock()
	lastSignal := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if lastSignal.Direction != "" || lastSignal.Mode != "" {
		t.Fatalf("diagnostic reversal candidate updated LastSignal: %+v", lastSignal)
	}

	filtered := tr.FilterSignal(tr.ScoreSignal(regressionLongSnapshot(40, ""), 1, regressionCloses()), regressionPassingOrderbook)
	if !filtered.Accepted || filtered.Signal.Direction != "LONG" {
		t.Fatalf("ordinary signal generation changed: accepted=%t signal=%+v", filtered.Accepted, filtered.Signal)
	}
	if filtered.Signal.Mode != "" {
		t.Fatalf("ordinary signal got reversal mode: %q", filtered.Signal.Mode)
	}
}

func reversalPipelineScoredSignal() scoredSignal {
	snapshot := reversalLongSnapshot()
	snapshot.Close = 100
	return scoredSignal{
		Snapshot:  snapshot,
		CandleSeq: 42,
		Time:      time.Unix(1700000100, 0),
		Regime:    "trend",
		RSIDecision: rsiPolicyDecision{
			Mode:       config.RSIModeLog,
			AllowLong:  true,
			AllowShort: true,
			Impact:     "log_only",
		},
	}
}

func readyReversalPipelineEvaluation(t *testing.T, cfg config.ReversalLongConfig) ReversalLongEvaluation {
	t.Helper()
	return validReversalEvaluationWithSafety(t, cfg,
		ReversalHTFInput{Available: true, Bias: "SHORT"},
		ReversalOrderbookInput{
			Available:             true,
			SpreadBps:             cfg.MaxSpreadBps - 1,
			Ratio:                 1.1,
			Imbalance:             0.05,
			AskWallRatio:          1.1,
			BidStabilityRatio:     cfg.MinBidStabilityRatio + 0.1,
			BidStabilityAvailable: true,
		},
	)
}

func TestReversalLongEvaluationCanEnterPipelineRequiresTradeEnabled(t *testing.T) {
	cfg := enabledReversalLongConfig()
	evaluation := readyReversalPipelineEvaluation(t, cfg)
	if evaluation.CanEnterSignalPipeline(cfg) {
		t.Fatalf("TradeEnabled=false allowed reversal candidate into pipeline")
	}
	cfg.TradeEnabled = true
	if !evaluation.CanEnterSignalPipeline(cfg) {
		t.Fatalf("TradeEnabled=true did not allow ready reversal candidate into pipeline")
	}
}

func TestReversalLongPipelineTradeDisabledDoesNotEmit(t *testing.T) {
	tr := newTestTrader()
	cfg := enabledReversalLongConfig()
	cfg.TradeEnabled = false
	tr.Config.ReversalLong = cfg
	tr.State.SigChan = make(chan models.Signal, 1)

	scored := reversalPipelineScoredSignal()
	evaluation := readyReversalPipelineEvaluation(t, cfg)
	rejected := filteredSignal{
		Accepted: false,
		Signal:   signalFromScore(scored, "", 0, nil, signalDebugFromScore(scored)),
		Scored:   scored,
	}

	filtered, decision := tr.applyReversalLongPipelineBridge(rejected, scored, evaluation, true)
	if filtered.Accepted {
		t.Fatalf("TradeEnabled=false produced accepted reversal signal")
	}
	if decision.PipelineAllowed || decision.PipelineSelected {
		t.Fatalf("TradeEnabled=false allowed pipeline decision: %+v", decision)
	}
	if decision.PipelineSkipReason != ReversalReasonTradeDisabled {
		t.Fatalf("skip reason got %q want %q", decision.PipelineSkipReason, ReversalReasonTradeDisabled)
	}
	if tr.EmitSignal(filtered) {
		t.Fatalf("TradeEnabled=false emitted reversal signal")
	}
	if len(tr.State.SigChan) != 0 {
		t.Fatalf("TradeEnabled=false wrote to SigChan")
	}
	tr.State.StatusLock.RLock()
	lastSignal := tr.State.LastSignal
	tr.State.StatusLock.RUnlock()
	if lastSignal.Mode == models.ModeReversalLong {
		t.Fatalf("TradeEnabled=false tagged LastSignal as reversal_long: %+v", lastSignal)
	}
}

func TestReversalLongPipelineTradeEnabledEmitsModeTaggedSignal(t *testing.T) {
	tr := newTestTrader()
	cfg := enabledReversalLongConfig()
	cfg.TradeEnabled = true
	tr.Config.ReversalLong = cfg
	tr.State.SigChan = make(chan models.Signal, 1)

	scored := reversalPipelineScoredSignal()
	evaluation := readyReversalPipelineEvaluation(t, cfg)
	rejected := filteredSignal{
		Accepted: false,
		Signal:   signalFromScore(scored, "", 0, nil, signalDebugFromScore(scored)),
		Scored:   scored,
	}

	filtered, decision := tr.applyReversalLongPipelineBridge(rejected, scored, evaluation, true)
	if !decision.PipelineAllowed || !decision.PipelineSelected {
		t.Fatalf("TradeEnabled=true did not select ready reversal candidate: %+v", decision)
	}
	if !filtered.Accepted {
		t.Fatalf("selected reversal candidate returned rejected filtered signal")
	}
	if filtered.Signal.Direction != "LONG" || filtered.Signal.Mode != models.ModeReversalLong {
		t.Fatalf("signal got direction=%q mode=%q", filtered.Signal.Direction, filtered.Signal.Mode)
	}
	if filtered.Signal.Strength != cfg.MinRuntimeStrength {
		t.Fatalf("reversal signal strength got %d want %d", filtered.Signal.Strength, cfg.MinRuntimeStrength)
	}
	if filtered.Signal.Debug.ReversalScore <= 0 || filtered.Signal.Debug.ReversalLowestLow <= 0 || filtered.Signal.Debug.ReversalStopPrice <= 0 {
		t.Fatalf("reversal signal missing risk metadata: %+v", filtered.Signal.Debug)
	}
	if filtered.Signal.Debug.ReversalPositionSizeMultiplier != safeReversalPositionSizeMultiplier(cfg) {
		t.Fatalf("reversal multiplier metadata got %.4f want %.4f", filtered.Signal.Debug.ReversalPositionSizeMultiplier, safeReversalPositionSizeMultiplier(cfg))
	}
	if filtered.Signal.Orderbook == nil || !filtered.Signal.Orderbook.Evaluated || !filtered.Signal.Orderbook.Passed {
		t.Fatalf("reversal signal missing precomputed soft orderbook pass: %+v", filtered.Signal.Orderbook)
	}
	if !containsString(filtered.Signal.Debug.Reasons, ReversalReasonPipelineSelected) {
		t.Fatalf("reversal signal missing pipeline reason: %+v", filtered.Signal.Debug)
	}
	if !tr.EmitSignal(filtered) {
		t.Fatalf("TradeEnabled=true selected signal should emit through common EmitSignal")
	}
	sig := <-tr.State.SigChan
	if sig.Mode != models.ModeReversalLong || sig.Direction != "LONG" {
		t.Fatalf("emitted signal got direction=%q mode=%q", sig.Direction, sig.Mode)
	}
}

func TestReversalLongPipelineOrdinaryLongWins(t *testing.T) {
	tr := newSignalRegressionTrader()
	cfg := enabledReversalLongConfig()
	cfg.TradeEnabled = true
	tr.Config.ReversalLong = cfg

	scored := tr.ScoreSignal(regressionLongSnapshot(60, ""), 1, regressionCloses())
	ordinary := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !ordinary.Accepted || ordinary.Signal.Direction != "LONG" {
		t.Fatalf("expected ordinary LONG acceptance: %+v", ordinary.Signal.Debug)
	}

	evaluation := readyReversalPipelineEvaluation(t, cfg)
	filtered, decision := tr.applyReversalLongPipelineBridge(ordinary, scored, evaluation, true)
	if decision.PipelineSelected {
		t.Fatalf("reversal replaced accepted ordinary LONG: %+v", decision)
	}
	if decision.PipelineSkipReason != ReversalReasonConflictExisting {
		t.Fatalf("skip reason got %q want %q", decision.PipelineSkipReason, ReversalReasonConflictExisting)
	}
	if filtered.Signal.Mode != "" || containsString(filtered.Signal.Debug.Reasons, models.SignalReasonReversalLong) {
		t.Fatalf("ordinary LONG was tagged as reversal: %+v", filtered.Signal)
	}
}

func TestReversalLongPipelineOrdinaryShortConflictBlocksReversal(t *testing.T) {
	tr := newSignalRegressionTrader()
	cfg := enabledReversalLongConfig()
	cfg.TradeEnabled = true
	tr.Config.ReversalLong = cfg

	scored := tr.ScoreSignal(regressionShortSnapshot(40, ""), 1, regressionCloses())
	ordinary := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !ordinary.Accepted || ordinary.Signal.Direction != "SHORT" {
		t.Fatalf("expected ordinary SHORT acceptance: %+v", ordinary.Signal.Debug)
	}

	evaluation := readyReversalPipelineEvaluation(t, cfg)
	filtered, decision := tr.applyReversalLongPipelineBridge(ordinary, scored, evaluation, true)
	if decision.PipelineSelected {
		t.Fatalf("reversal emitted alongside ordinary SHORT: %+v", decision)
	}
	if !decision.ConflictShort || decision.PipelineSkipReason != ReversalReasonConflictShort {
		t.Fatalf("missing SHORT conflict decision: %+v", decision)
	}
	if filtered.Signal.Direction != "SHORT" || filtered.Signal.Mode != "" {
		t.Fatalf("ordinary SHORT changed by reversal bridge: %+v", filtered.Signal)
	}
}

func TestReversalLongDetectorExpired(t *testing.T) {
	cfg := enabledReversalLongConfig()
	cfg.TimeoutBars = 1
	detector := NewReversalLongDetector(cfg)
	now := time.Unix(1700000001, 0)

	capSnapshot := reversalLongSnapshot()
	capSnapshot.RSI = 25
	detector.Evaluate("BTCUSDT", capSnapshot, now)
	detector.Evaluate("BTCUSDT", reversalLongSnapshot(), now.Add(time.Minute))
	evaluation := detector.Evaluate("BTCUSDT", reversalLongSnapshot(), now.Add(2*time.Minute))

	if evaluation.State != RevExpired {
		t.Fatalf("state got %q want %q", evaluation.State, RevExpired)
	}
	if evaluation.RejectReason != ReversalReasonWindowExpired {
		t.Fatalf("reason got %q want %q", evaluation.RejectReason, ReversalReasonWindowExpired)
	}
}
