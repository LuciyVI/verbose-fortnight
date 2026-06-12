package strategy

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

type ReversalState string

const (
	RevIdle         ReversalState = "idle"
	RevCapitulated  ReversalState = "capitulated"
	RevBelowBB      ReversalState = "below_lower_bb"
	RevReclaimedBB  ReversalState = "reclaimed_bb"
	RevMomentumTurn ReversalState = "momentum_turn"
	RevCandidate    ReversalState = "candidate_ready"
	RevExpired      ReversalState = "expired"
)

const (
	ReversalReasonDisabled             = models.SignalReasonReversalDisabled
	ReversalReasonNoCapitulation       = "no_capitulation"
	ReversalReasonBBDataUnavailable    = "bb_data_not_available"
	ReversalReasonNoLowerBBReclaim     = "no_lower_bb_reclaim"
	ReversalReasonMomentumUnavailable  = "momentum_data_not_available"
	ReversalReasonVolumeUnavailable    = "volume_ratio_not_available"
	ReversalReasonNoRSIRecovery        = "no_rsi_recovery"
	ReversalReasonRSITooHigh           = "rsi_too_high_for_reversal_entry"
	ReversalReasonNoMACDRecovery       = "no_macd_hist_recovery"
	ReversalReasonNewLowDetected       = "new_low_detected"
	ReversalReasonScoreBelowThreshold  = "score_below_threshold"
	ReversalReasonWindowExpired        = "reversal_window_expired"
	ReversalReasonCandidateReady       = "reversal_candidate_ready"
	ReversalReasonHTFBearishBlock      = "htf_bearish_block"
	ReversalReasonHTFExtremeBlock      = "htf_extreme_downtrend_block"
	ReversalReasonOrderbookHostile     = "orderbook_hostile"
	ReversalReasonSpreadTooWide        = "spread_too_wide"
	ReversalReasonAskWallTooLarge      = "ask_wall_too_large"
	ReversalReasonBidUnstable          = "bid_unstable"
	ReversalReasonOrderbookNoData      = "orderbook_data_not_available"
	ReversalReasonTradeDisabled        = "reversal_trade_disabled"
	ReversalReasonPipelineSelected     = "reversal_pipeline_selected"
	ReversalReasonConflictShort        = "reversal_conflict_short"
	ReversalReasonConflictExisting     = "reversal_conflict_existing_signal"
	ReversalReasonPipelineSkipped      = "reversal_pipeline_skipped"
	ReversalReasonLocalLowNoStop       = "local_low_not_available_for_stop"
	ReversalReasonStopInvalid          = "reversal_stop_invalid"
	ReversalReasonRiskProfileApplied   = "reversal_risk_profile_applied"
	ReversalReasonRuntimeOBUnavailable = "reversal_runtime_orderbook_unavailable"
	ReversalReasonRuntimeOBHostile     = "reversal_runtime_orderbook_hostile"
	ReversalReasonRuntimeOBPassed      = "reversal_runtime_orderbook_passed"
)

const (
	reversalScoreRSIOversold  = 0.20
	reversalScoreVolumeSpike  = 0.15
	reversalScoreBBReclaim    = 0.20
	reversalScoreRSIRecovery  = 0.15
	reversalScoreMACDRecovery = 0.15
	reversalScoreNoNewLow     = 0.15
	cfgLargeRatio             = 1_000_000.0
)

type ReversalLongState struct {
	State             ReversalState
	PreviousState     ReversalState
	StartedAt         time.Time
	UpdatedAt         time.Time
	LowestLow         float64
	LastCandleSeq     int64
	BarsSinceStart    int
	BarsWithoutNewLow int
	HadRSIOversold    bool
	HadVolumeSpike    bool
}

type ReversalLongEvaluation struct {
	Symbol        string
	Mode          models.SignalMode
	State         ReversalState
	PreviousState ReversalState
	Enabled       bool
	Time          time.Time

	CandidateReady bool
	RejectReason   string
	Score          float64

	Debug map[string]any
}

// CanEnterSignalPipeline is the explicit safety gate between diagnostics and real signals.
func (e ReversalLongEvaluation) CanEnterSignalPipeline(cfg config.ReversalLongConfig) bool {
	return cfg.Enabled &&
		cfg.TradeEnabled &&
		e.CandidateReady &&
		e.RejectReason == ReversalReasonCandidateReady
}

type ReversalLongSignalCandidate struct {
	Enabled                bool
	Mode                   models.SignalMode
	Side                   string
	Score                  float64
	Reason                 string
	Evaluation             ReversalLongEvaluation
	LocalLow               float64
	StopPrice              float64
	PositionSizeMultiplier float64
	TimeoutBars            int
	RuntimeStrength        int
}

type reversalLongPipelineDecision struct {
	Active             bool
	Enabled            bool
	TradeEnabled       bool
	CandidateReady     bool
	CandidateScore     float64
	State              ReversalState
	RejectReason       string
	PipelineAllowed    bool
	PipelineSelected   bool
	PipelineSkipReason string
	SelectedMode       models.SignalMode
	SelectedSide       string
	ExistingSignalSide string
	ConflictShort      bool
	PositionOpen       bool
	PendingOrder       bool
}

type reversalLongRuntimeRisk struct {
	IsReversal             bool
	LocalLow               float64
	StopPrice              float64
	StopValid              bool
	RejectReason           string
	PositionSizeMultiplier float64
	TimeoutBars            int
}

type reversalLongRuntimeGateDecision struct {
	Strength               int
	RequiredStrength       int
	PassedStrength         bool
	PositionOpen           bool
	PendingOrder           bool
	OrderbookRequired      bool
	OrderbookAvailable     bool
	OrderbookAllowed       bool
	OrderbookRejectReason  string
	LocalLow               float64
	StopBelowLocalLowBps   float64
	StopPrice              float64
	StopValid              bool
	RiskProfileApplied     bool
	PositionSizeMultiplier float64
	NormalSize             float64
	FinalSize              float64
	FinalDecision          string
}

type ReversalHTFInput struct {
	Bias                      string
	Reason                    string
	Available                 bool
	ExtremeDowntrend          bool
	ExtremeReason             string
	ExtremeDowntrendAvailable bool
}

type ReversalHTFResult struct {
	Allowed          bool
	OverrideAllowed  bool
	ExtremeDowntrend bool
	RejectReason     string
	Debug            map[string]any
}

type ReversalOrderbookInput struct {
	Available             bool
	UnavailableReason     string
	BestBid               float64
	BestAsk               float64
	Spread                float64
	SpreadBps             float64
	Imbalance             float64
	Ratio                 float64
	BestBidSize           float64
	BestAskSize           float64
	DepthLevels           int
	AskWallRatio          float64
	BidStabilityRatio     float64
	BidStabilityAvailable bool
}

type ReversalOrderbookResult struct {
	Allowed      bool
	Hostile      bool
	RejectReason string
	Debug        map[string]any
}

type reversalLongScoreBreakdown struct {
	RSIOversold  float64
	VolumeSpike  float64
	BBReclaim    float64
	RSIRecovery  float64
	MACDRecovery float64
	NoNewLow     float64
}

func (b reversalLongScoreBreakdown) total() float64 {
	return b.RSIOversold + b.VolumeSpike + b.BBReclaim + b.RSIRecovery + b.MACDRecovery + b.NoNewLow
}

type reversalLongChecks struct {
	RSIOversold          bool
	VolumeSpike          bool
	VolumeSpikePassed    bool
	VolumeRatioAvailable bool
	Capitulated          bool
	BBDataAvailable      bool
	BelowLowerBB         bool
	BBReclaimed          bool
	RSIRecovery          bool
	MACDRecovery         bool
	NoNewLow             bool
	CurrentLow           float64
	ScoreBreakdown       reversalLongScoreBreakdown
	HTF                  ReversalHTFResult
	Orderbook            ReversalOrderbookResult
}

type ReversalLongDetector struct {
	mu    sync.Mutex
	cfg   config.ReversalLongConfig
	state map[string]*ReversalLongState
}

func NewReversalLongDetector(cfg config.ReversalLongConfig) *ReversalLongDetector {
	return &ReversalLongDetector{
		cfg:   cfg,
		state: make(map[string]*ReversalLongState),
	}
}

func (d *ReversalLongDetector) Evaluate(symbol string, snapshot models.IndicatorSnapshot, now time.Time) ReversalLongEvaluation {
	return d.EvaluateWithSafety(symbol, snapshot, now, ReversalHTFInput{
		Available:                 snapshot.HTFBias != "",
		Bias:                      snapshot.HTFBias,
		ExtremeDowntrendAvailable: false,
	}, ReversalOrderbookInput{})
}

func (d *ReversalLongDetector) EvaluateWithSafety(symbol string, snapshot models.IndicatorSnapshot, now time.Time, htfInput ReversalHTFInput, orderbookInput ReversalOrderbookInput) ReversalLongEvaluation {
	if now.IsZero() {
		now = time.Now()
	}
	if d == nil {
		return ReversalLongEvaluation{
			Symbol:        symbol,
			Mode:          models.ModeReversalLong,
			State:         RevIdle,
			PreviousState: RevIdle,
			Time:          now,
			RejectReason:  ReversalReasonDisabled,
			Debug:         reversalLongDebug(d, snapshot, ReversalLongState{}, reversalLongChecks{CurrentLow: snapshotLow(snapshot), HTF: CheckReversalHTF(config.ReversalLongConfig{}, htfInput), Orderbook: CheckReversalOrderbookSoft(config.ReversalLongConfig{}, orderbookInput)}),
		}
	}

	if !d.cfg.Enabled {
		checks := d.checks(snapshot, ReversalLongState{}, htfInput, orderbookInput)
		return ReversalLongEvaluation{
			Symbol:        symbol,
			Mode:          models.ModeReversalLong,
			State:         RevIdle,
			PreviousState: RevIdle,
			Enabled:       false,
			Time:          now,
			RejectReason:  ReversalReasonDisabled,
			Debug:         reversalLongDebug(d, snapshot, ReversalLongState{}, checks),
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	st := d.stateFor(symbol)
	previousState := st.State
	if previousState == "" {
		previousState = RevIdle
		st.State = RevIdle
	}

	wasActiveAtStart := isActiveReversalState(st.State)
	if wasActiveAtStart {
		advanceReversalWindow(st, snapshot)
		if d.cfg.TimeoutBars > 0 && st.BarsSinceStart > d.cfg.TimeoutBars {
			d.transition(st, RevExpired, now, snapshot)
			st.UpdatedAt = now
			checks := d.checks(snapshot, *st, htfInput, orderbookInput)
			return d.evaluation(symbol, snapshot, now, previousState, st, false, ReversalReasonWindowExpired, checks)
		}
	}

	rejectReason := ""
	candidateReady := false
	checks := d.checks(snapshot, *st, htfInput, orderbookInput)

	switch st.State {
	case RevIdle, RevExpired:
		if st.State == RevExpired {
			d.reset(st)
			checks = d.checks(snapshot, *st, htfInput, orderbookInput)
		}
		if checks.Capitulated {
			d.start(st, RevCapitulated, now, snapshot)
			checks = d.checks(snapshot, *st, htfInput, orderbookInput)
		} else {
			rejectReason = ReversalReasonNoCapitulation
			break
		}

		if checks.BelowLowerBB {
			d.transition(st, RevBelowBB, now, snapshot)
			checks = d.checks(snapshot, *st, htfInput, orderbookInput)
		} else if d.cfg.RequireLowerBBReclaim {
			rejectReason = reversalBBRejectReason(checks)
			break
		}
		fallthrough
	case RevCapitulated:
		if st.State == RevCapitulated {
			if checks.BelowLowerBB {
				d.transition(st, RevBelowBB, now, snapshot)
				checks = d.checks(snapshot, *st, htfInput, orderbookInput)
			} else if d.cfg.RequireLowerBBReclaim {
				rejectReason = reversalBBRejectReason(checks)
				break
			}
		}
		fallthrough
	case RevBelowBB:
		if st.State == RevBelowBB {
			if checks.BBReclaimed {
				d.transition(st, RevReclaimedBB, now, snapshot)
				checks = d.checks(snapshot, *st, htfInput, orderbookInput)
			} else if d.cfg.RequireLowerBBReclaim {
				rejectReason = reversalBBRejectReason(checks)
				break
			}
		}
		fallthrough
	case RevReclaimedBB:
		if st.State == RevReclaimedBB {
			reason := reversalMomentumRejectReason(d.cfg, snapshot)
			if reason == "" {
				d.transition(st, RevMomentumTurn, now, snapshot)
				checks = d.checks(snapshot, *st, htfInput, orderbookInput)
			} else {
				rejectReason = reason
				break
			}
		}
		fallthrough
	case RevMomentumTurn:
		if st.State == RevMomentumTurn {
			reason := reversalCandidateRejectReason(d.cfg, snapshot, checks)
			if reason == "" {
				d.transition(st, RevCandidate, now, snapshot)
				checks = d.checks(snapshot, *st, htfInput, orderbookInput)
				candidateReady = true
				rejectReason = ReversalReasonCandidateReady
			} else {
				rejectReason = reason
			}
		}
	case RevCandidate:
		checks = d.checks(snapshot, *st, htfInput, orderbookInput)
		reason := reversalCandidateRejectReason(d.cfg, snapshot, checks)
		if reason == "" {
			candidateReady = true
			rejectReason = ReversalReasonCandidateReady
		} else {
			candidateReady = false
			rejectReason = reason
		}
	}

	st.UpdatedAt = now
	return d.evaluation(symbol, snapshot, now, previousState, st, candidateReady, rejectReason, checks)
}

func (d *ReversalLongDetector) stateFor(symbol string) *ReversalLongState {
	st := d.state[symbol]
	if st == nil {
		st = &ReversalLongState{State: RevIdle, PreviousState: RevIdle}
		d.state[symbol] = st
	}
	return st
}

func (d *ReversalLongDetector) start(st *ReversalLongState, next ReversalState, now time.Time, snapshot models.IndicatorSnapshot) {
	st.State = next
	st.PreviousState = RevIdle
	st.StartedAt = now
	st.UpdatedAt = now
	st.BarsSinceStart = 0
	st.BarsWithoutNewLow = 0
	st.LowestLow = snapshotLow(snapshot)
	st.HadRSIOversold = snapshot.RSI > 0 && snapshot.RSI <= d.cfg.OversoldRSI
	st.HadVolumeSpike = d.cfg.MinVolumeRatio > 0 && snapshot.VolumeAvailable && snapshot.VolumeRatio >= d.cfg.MinVolumeRatio
}

func (d *ReversalLongDetector) transition(st *ReversalLongState, next ReversalState, now time.Time, snapshot models.IndicatorSnapshot) {
	st.PreviousState = st.State
	st.State = next
	st.UpdatedAt = now
}

func (d *ReversalLongDetector) reset(st *ReversalLongState) {
	*st = ReversalLongState{State: RevIdle, PreviousState: st.State}
}

func (d *ReversalLongDetector) evaluation(symbol string, snapshot models.IndicatorSnapshot, now time.Time, previousState ReversalState, st *ReversalLongState, candidateReady bool, rejectReason string, checks reversalLongChecks) ReversalLongEvaluation {
	return ReversalLongEvaluation{
		Symbol:         symbol,
		Mode:           models.ModeReversalLong,
		State:          st.State,
		PreviousState:  previousState,
		Enabled:        d.cfg.Enabled,
		Time:           now,
		CandidateReady: candidateReady,
		RejectReason:   rejectReason,
		Score:          checks.ScoreBreakdown.total(),
		Debug:          reversalLongDebug(d, snapshot, *st, checks),
	}
}

func (d *ReversalLongDetector) checks(snapshot models.IndicatorSnapshot, st ReversalLongState, htfInput ReversalHTFInput, orderbookInput ReversalOrderbookInput) reversalLongChecks {
	rsiOversoldNow := snapshot.RSI > 0 && snapshot.RSI <= d.cfg.OversoldRSI
	volumeRatioAvailable := snapshot.VolumeAvailable
	volumeSpikeNow := d.cfg.MinVolumeRatio > 0 && volumeRatioAvailable && snapshot.VolumeRatio >= d.cfg.MinVolumeRatio
	rsiOversold := rsiOversoldNow || st.HadRSIOversold
	volumeSpikeMemory := volumeSpikeNow || st.HadVolumeSpike
	bbDataAvailable := snapshot.BBReclaimAvailable || (snapshot.BBPosition != "" && snapshot.BBPosition != bbPositionNotAvailable)
	belowLowerBB := snapshot.BBPosition == bbPositionBelowLower || (snapshot.BBReclaimAvailable && snapshot.WasBelowLowerBB)
	bbReclaimed := true
	if d.cfg.RequireLowerBBReclaim {
		bbReclaimed = snapshot.BBReclaimAvailable &&
			snapshot.WasBelowLowerBB &&
			snapshot.ReturnedInsideBB &&
			snapshot.BBPosition != bbPositionBelowLower
	}
	rsiRecovery := snapshot.RSIDeltaAvailable &&
		snapshot.RSIDelta >= d.cfg.MinRSIDelta &&
		(d.cfg.MaxEntryRSI <= 0 || snapshot.RSI <= d.cfg.MaxEntryRSI)
	macdRecovery := true
	if d.cfg.RequireMACDHistRecovery {
		macdRecovery = snapshot.MACDHistDeltaAvailable && snapshot.MACDHistDelta >= d.cfg.MinMACDHistDelta
	}
	noNewLow := d.cfg.NoNewLowBars <= 0 || st.BarsWithoutNewLow >= d.cfg.NoNewLowBars

	checks := reversalLongChecks{
		RSIOversold:          rsiOversold,
		VolumeSpike:          volumeSpikeNow,
		VolumeSpikePassed:    volumeSpikeNow,
		VolumeRatioAvailable: volumeRatioAvailable,
		Capitulated:          rsiOversold || volumeSpikeMemory,
		BBDataAvailable:      bbDataAvailable,
		BelowLowerBB:         belowLowerBB,
		BBReclaimed:          bbReclaimed,
		RSIRecovery:          rsiRecovery,
		MACDRecovery:         macdRecovery,
		NoNewLow:             noNewLow,
		CurrentLow:           snapshotLow(snapshot),
		HTF:                  CheckReversalHTF(d.cfg, htfInput),
		Orderbook:            CheckReversalOrderbookSoft(d.cfg, orderbookInput),
	}
	checks.ScoreBreakdown = reversalScoreBreakdown(d.cfg, checks)
	return checks
}

func reversalScoreBreakdown(cfg config.ReversalLongConfig, checks reversalLongChecks) reversalLongScoreBreakdown {
	breakdown := reversalLongScoreBreakdown{}
	if checks.RSIOversold {
		breakdown.RSIOversold = reversalScoreRSIOversold
	}
	if checks.VolumeSpike {
		breakdown.VolumeSpike = reversalScoreVolumeSpike
	}
	if checks.BBReclaimed && cfg.RequireLowerBBReclaim {
		breakdown.BBReclaim = reversalScoreBBReclaim
	}
	if checks.RSIRecovery {
		breakdown.RSIRecovery = reversalScoreRSIRecovery
	}
	if checks.MACDRecovery && cfg.RequireMACDHistRecovery {
		breakdown.MACDRecovery = reversalScoreMACDRecovery
	}
	if checks.NoNewLow {
		breakdown.NoNewLow = reversalScoreNoNewLow
	}
	return breakdown
}

func reversalBBRejectReason(checks reversalLongChecks) string {
	if !checks.BBDataAvailable {
		return ReversalReasonBBDataUnavailable
	}
	return ReversalReasonNoLowerBBReclaim
}

func reversalMomentumRejectReason(cfg config.ReversalLongConfig, snapshot models.IndicatorSnapshot) string {
	if !snapshot.RSIDeltaAvailable {
		return ReversalReasonMomentumUnavailable
	}
	if cfg.MaxEntryRSI > 0 && snapshot.RSI > cfg.MaxEntryRSI {
		return ReversalReasonRSITooHigh
	}
	if snapshot.RSIDelta < cfg.MinRSIDelta {
		return ReversalReasonNoRSIRecovery
	}
	if cfg.RequireMACDHistRecovery {
		if !snapshot.MACDHistDeltaAvailable {
			return ReversalReasonMomentumUnavailable
		}
		if snapshot.MACDHistDelta < cfg.MinMACDHistDelta {
			return ReversalReasonNoMACDRecovery
		}
	}
	return ""
}

func reversalCandidateRejectReason(cfg config.ReversalLongConfig, snapshot models.IndicatorSnapshot, checks reversalLongChecks) string {
	if cfg.RequireLowerBBReclaim {
		if !checks.BBDataAvailable {
			return ReversalReasonBBDataUnavailable
		}
		if !checks.BBReclaimed {
			return ReversalReasonNoLowerBBReclaim
		}
	}
	if reason := reversalMomentumRejectReason(cfg, snapshot); reason != "" {
		return reason
	}
	if !checks.NoNewLow {
		return ReversalReasonNewLowDetected
	}
	if checks.ScoreBreakdown.total() < cfg.ScoreThreshold {
		return ReversalReasonScoreBelowThreshold
	}
	if !checks.HTF.Allowed {
		return checks.HTF.RejectReason
	}
	if !checks.Orderbook.Allowed {
		return checks.Orderbook.RejectReason
	}
	return ""
}

func CheckReversalHTF(cfg config.ReversalLongConfig, input ReversalHTFInput) ReversalHTFResult {
	bias := strings.ToUpper(strings.TrimSpace(input.Bias))
	result := ReversalHTFResult{
		Allowed:          true,
		ExtremeDowntrend: input.ExtremeDowntrend,
		Debug: map[string]any{
			"htf_available":                   input.Available,
			"htf_bias":                        bias,
			"htf_reason":                      input.Reason,
			"htf_extreme_downtrend":           input.ExtremeDowntrend,
			"htf_extreme_reason":              input.ExtremeReason,
			"htf_extreme_downtrend_available": input.ExtremeDowntrendAvailable,
		},
	}
	if !input.Available {
		return result
	}
	if cfg.BlockExtremeHTFDowntrend && input.ExtremeDowntrend {
		result.Allowed = false
		result.RejectReason = ReversalReasonHTFExtremeBlock
		return result
	}
	if bias != "SHORT" {
		return result
	}
	if cfg.AllowHTFBearish {
		result.OverrideAllowed = true
		return result
	}
	result.Allowed = false
	result.RejectReason = ReversalReasonHTFBearishBlock
	return result
}

func CheckReversalOrderbookSoft(cfg config.ReversalLongConfig, input ReversalOrderbookInput) ReversalOrderbookResult {
	result := ReversalOrderbookResult{
		Allowed: true,
		Debug: map[string]any{
			"orderbook_available":          input.Available,
			"orderbook_unavailable_reason": input.UnavailableReason,
			"orderbook_soft_check_enabled": cfg.UseSoftOrderbookCheck,
			"orderbook_ratio":              input.Ratio,
			"orderbook_imbalance":          input.Imbalance,
			"best_bid":                     input.BestBid,
			"best_ask":                     input.BestAsk,
			"best_bid_size":                input.BestBidSize,
			"best_ask_size":                input.BestAskSize,
			"depth_levels":                 input.DepthLevels,
			"spread":                       input.Spread,
			"spread_bps":                   input.SpreadBps,
			"max_spread_bps":               cfg.MaxSpreadBps,
			"ask_wall_ratio":               input.AskWallRatio,
			"max_ask_wall_ratio":           cfg.MaxAskWallRatio,
			"bid_stability_ratio":          input.BidStabilityRatio,
			"bid_stability_available":      input.BidStabilityAvailable,
			"min_bid_stability_ratio":      cfg.MinBidStabilityRatio,
		},
	}
	if !cfg.UseSoftOrderbookCheck {
		return result
	}
	if !input.Available {
		return result
	}
	if cfg.MaxSpreadBps > 0 && input.SpreadBps > cfg.MaxSpreadBps {
		result.Allowed = false
		result.Hostile = true
		result.RejectReason = ReversalReasonSpreadTooWide
		return result
	}
	if cfg.MaxAskWallRatio > 0 && input.AskWallRatio > cfg.MaxAskWallRatio {
		result.Allowed = false
		result.Hostile = true
		result.RejectReason = ReversalReasonAskWallTooLarge
		return result
	}
	if cfg.MinBidStabilityRatio > 0 && input.BidStabilityAvailable && input.BidStabilityRatio < cfg.MinBidStabilityRatio {
		result.Allowed = false
		result.Hostile = true
		result.RejectReason = ReversalReasonBidUnstable
		return result
	}
	if input.Imbalance <= -0.75 || (input.Ratio > 0 && input.Ratio <= 0.25) {
		result.Allowed = false
		result.Hostile = true
		result.RejectReason = ReversalReasonOrderbookHostile
		return result
	}
	return result
}

func isActiveReversalState(state ReversalState) bool {
	switch state {
	case RevCapitulated, RevBelowBB, RevReclaimedBB, RevMomentumTurn:
		return true
	default:
		return false
	}
}

func snapshotLow(snapshot models.IndicatorSnapshot) float64 {
	if snapshot.Low > 0 {
		return snapshot.Low
	}
	return snapshot.Close
}

func safeReversalPositionSizeMultiplier(cfg config.ReversalLongConfig) float64 {
	if cfg.PositionSizeMultiplier > 0 && cfg.PositionSizeMultiplier <= 1 {
		return cfg.PositionSizeMultiplier
	}
	return 0.3
}

func reversalLongStopPrice(localLow, stopBelowLocalLowBps float64) (float64, bool) {
	if localLow <= 0 || stopBelowLocalLowBps <= 0 {
		return 0, false
	}
	stopPrice := localLow * (1 - stopBelowLocalLowBps/10000)
	return stopPrice, stopPrice > 0
}

func advanceReversalWindow(st *ReversalLongState, snapshot models.IndicatorSnapshot) {
	st.BarsSinceStart++
	low := snapshotLow(snapshot)
	if low == 0 {
		return
	}
	if st.LowestLow == 0 || low < st.LowestLow {
		st.LowestLow = low
		st.BarsWithoutNewLow = 0
		return
	}
	st.BarsWithoutNewLow++
}

func reversalLongDebug(d *ReversalLongDetector, snapshot models.IndicatorSnapshot, st ReversalLongState, checks reversalLongChecks) map[string]any {
	enabled := false
	cfg := config.ReversalLongConfig{}
	if d != nil {
		enabled = d.cfg.Enabled
		cfg = d.cfg
	}
	debug := map[string]any{
		"enabled":                   enabled,
		"trade_enabled":             cfg.TradeEnabled,
		"oversold_rsi":              cfg.OversoldRSI,
		"max_entry_rsi":             cfg.MaxEntryRSI,
		"min_volume_ratio":          cfg.MinVolumeRatio,
		"rsi":                       snapshot.RSI,
		"rsi_prev":                  snapshot.RSIPrev,
		"rsi_delta":                 snapshot.RSIDelta,
		"rsi_delta_available":       snapshot.RSIDeltaAvailable,
		"macd_hist":                 snapshot.MACDHist,
		"macd_hist_prev":            snapshot.MACDHistPrev,
		"macd_hist_delta":           snapshot.MACDHistDelta,
		"macd_hist_delta_available": snapshot.MACDHistDeltaAvailable,
		"volume":                    snapshot.Volume,
		"volume_sma":                snapshot.VolumeSMA,
		"volume_ratio":              snapshot.VolumeRatio,
		"volume_available":          snapshot.VolumeAvailable,
		"volume_ratio_available":    checks.VolumeRatioAvailable,
		"volume_spike_passed":       checks.VolumeSpikePassed,
		"bb_position":               snapshot.BBPosition,
		"was_below_lower_bb":        snapshot.WasBelowLowerBB,
		"returned_inside_bb":        snapshot.ReturnedInsideBB,
		"bb_reclaim_available":      snapshot.BBReclaimAvailable,
		"current_low":               checks.CurrentLow,
		"lowest_low":                st.LowestLow,
		"bars_since_start":          st.BarsSinceStart,
		"bars_without_new_low":      st.BarsWithoutNewLow,
		"timeout_bars":              cfg.TimeoutBars,
		"no_new_low_bars":           cfg.NoNewLowBars,
		"score":                     checks.ScoreBreakdown.total(),
		"score_rsi_oversold":        checks.ScoreBreakdown.RSIOversold,
		"score_volume_spike":        checks.ScoreBreakdown.VolumeSpike,
		"score_bb_reclaim":          checks.ScoreBreakdown.BBReclaim,
		"score_rsi_recovery":        checks.ScoreBreakdown.RSIRecovery,
		"score_macd_recovery":       checks.ScoreBreakdown.MACDRecovery,
		"score_no_new_low":          checks.ScoreBreakdown.NoNewLow,
		"state":                     st.State,
		"previous_state":            st.PreviousState,
	}
	for key, value := range checks.HTF.Debug {
		debug[key] = value
	}
	debug["htf_allowed"] = checks.HTF.Allowed
	debug["htf_override_allowed"] = checks.HTF.OverrideAllowed
	debug["htf_reject_reason"] = checks.HTF.RejectReason
	for key, value := range checks.Orderbook.Debug {
		debug[key] = value
	}
	debug["orderbook_allowed"] = checks.Orderbook.Allowed
	debug["orderbook_hostile"] = checks.Orderbook.Hostile
	debug["orderbook_reject_reason"] = checks.Orderbook.RejectReason
	return debug
}

func (t *Trader) evaluateReversalLongLogOnly(snapshot models.IndicatorSnapshot) (ReversalLongEvaluation, bool) {
	if t == nil || t.ReversalLongDetector == nil || t.Config == nil || !t.Config.ReversalLong.Enabled {
		return ReversalLongEvaluation{}, false
	}
	now := t.now()
	t.logReversalOrderbookDiagnosticSnapshot(now)
	evaluation := t.ReversalLongDetector.EvaluateWithSafety(t.Config.Symbol, snapshot, now, ReversalHTFInput{
		Available:                 snapshot.HTFBias != "",
		Bias:                      snapshot.HTFBias,
		ExtremeDowntrend:          false,
		ExtremeDowntrendAvailable: false, // TODO: derive this from HTF slope/ATR once an extreme-trend metric exists.
	}, t.reversalOrderbookInput())
	t.logReversalLongEvaluation(evaluation, snapshot)
	return evaluation, true
}

func newReversalLongSignalCandidate(cfg config.ReversalLongConfig, evaluation ReversalLongEvaluation) ReversalLongSignalCandidate {
	localLow := floatFromDebug(evaluation.Debug, "lowest_low")
	stopPrice, _ := reversalLongStopPrice(localLow, cfg.StopBelowLocalLowBps)
	runtimeStrength := cfg.MinRuntimeStrength
	if runtimeStrength <= 0 {
		runtimeStrength = 1
	}
	return ReversalLongSignalCandidate{
		Enabled:                cfg.Enabled && cfg.TradeEnabled,
		Mode:                   models.ModeReversalLong,
		Side:                   "LONG",
		Score:                  evaluation.Score,
		Reason:                 evaluation.RejectReason,
		Evaluation:             evaluation,
		LocalLow:               localLow,
		StopPrice:              stopPrice,
		PositionSizeMultiplier: safeReversalPositionSizeMultiplier(cfg),
		TimeoutBars:            cfg.TimeoutBars,
		RuntimeStrength:        runtimeStrength,
	}
}

func (t *Trader) applyReversalLongPipelineBridge(filtered filteredSignal, scored scoredSignal, evaluation ReversalLongEvaluation, hasEvaluation bool) (filteredSignal, reversalLongPipelineDecision) {
	if t == nil || t.Config == nil || !hasEvaluation || !evaluation.Enabled {
		return filtered, reversalLongPipelineDecision{}
	}

	cfg := t.Config.ReversalLong
	decision := reversalLongPipelineDecision{
		Active:             true,
		Enabled:            cfg.Enabled,
		TradeEnabled:       cfg.TradeEnabled,
		CandidateReady:     evaluation.CandidateReady,
		CandidateScore:     evaluation.Score,
		State:              evaluation.State,
		RejectReason:       evaluation.RejectReason,
		PipelineAllowed:    evaluation.CanEnterSignalPipeline(cfg),
		ExistingSignalSide: filtered.Signal.Direction,
		PositionOpen:       false,
		PendingOrder:       false,
	}

	if !evaluation.CandidateReady {
		decision.PipelineSkipReason = evaluation.RejectReason
		if decision.PipelineSkipReason == "" {
			decision.PipelineSkipReason = ReversalReasonPipelineSkipped
		}
		return filtered, decision
	}
	if !cfg.TradeEnabled {
		decision.PipelineSkipReason = ReversalReasonTradeDisabled
		return filtered, decision
	}
	if !decision.PipelineAllowed {
		decision.PipelineSkipReason = ReversalReasonPipelineSkipped
		return filtered, decision
	}
	if filtered.Accepted && filtered.Signal.Direction == "LONG" {
		decision.PipelineSkipReason = ReversalReasonConflictExisting
		return filtered, decision
	}
	if filtered.Signal.Direction == "SHORT" {
		decision.ConflictShort = true
		decision.PipelineSkipReason = ReversalReasonConflictShort
		return filtered, decision
	}
	if filtered.Accepted {
		decision.PipelineSkipReason = ReversalReasonConflictExisting
		return filtered, decision
	}

	candidate := newReversalLongSignalCandidate(cfg, evaluation)
	decision.PipelineSelected = true
	decision.PipelineSkipReason = ""
	decision.SelectedMode = candidate.Mode
	decision.SelectedSide = candidate.Side
	return reversalLongFilteredSignal(scored, candidate), decision
}

func reversalLongFilteredSignal(scored scoredSignal, candidate ReversalLongSignalCandidate) filteredSignal {
	strength := candidate.RuntimeStrength
	if strength <= 0 {
		strength = 1
	}
	signalTime := scored.Time
	if signalTime.IsZero() {
		signalTime = time.Now()
	}
	debug := reversalLongSignalDebug(scored.Snapshot, candidate)
	orderbook := reversalLongOrderbookResult(candidate.Evaluation)
	return filteredSignal{
		Accepted: true,
		Signal: models.Signal{
			Kind:       candidate.Side,
			Direction:  candidate.Side,
			Mode:       candidate.Mode,
			Strength:   strength,
			Contribs:   []string{models.SignalReasonReversalLong, ReversalReasonCandidateReady},
			HighConf:   false,
			ClosePrice: scored.Snapshot.Close,
			CandleSeq:  scored.CandleSeq,
			Time:       signalTime,
			Orderbook:  &orderbook,
			Debug:      debug,
		},
		Scored: scored,
	}
}

func reversalLongSignalDebug(snapshot models.IndicatorSnapshot, candidate ReversalLongSignalCandidate) models.SignalDebug {
	debug := models.SignalDebug{
		LongScore:                      1,
		ChosenDirection:                candidate.Side,
		Reasons:                        []string{models.SignalReasonReversalLong, ReversalReasonPipelineSelected},
		VolumeRatio:                    snapshot.VolumeRatio,
		VolumeRatioAvailable:           snapshot.VolumeAvailable,
		RSIDelta:                       snapshot.RSIDelta,
		RSIDeltaAvailable:              snapshot.RSIDeltaAvailable,
		MACDHistDelta:                  snapshot.MACDHistDelta,
		MACDHistDeltaAvailable:         snapshot.MACDHistDeltaAvailable,
		BBPosition:                     snapshot.BBPosition,
		WasBelowLowerBB:                snapshot.WasBelowLowerBB,
		ReturnedInsideBB:               snapshot.ReturnedInsideBB,
		BBReclaimAvailable:             snapshot.BBReclaimAvailable,
		ReversalScore:                  candidate.Score,
		ReversalState:                  string(candidate.Evaluation.State),
		ReversalLowestLow:              candidate.LocalLow,
		ReversalStopPrice:              candidate.StopPrice,
		ReversalTimeoutBars:            candidate.TimeoutBars,
		ReversalPositionSizeMultiplier: candidate.PositionSizeMultiplier,
		ReversalSoftOrderbookAllowed:   boolFromDebug(candidate.Evaluation.Debug, "orderbook_allowed"),
		ReversalHTFOverrideAllowed:     boolFromDebug(candidate.Evaluation.Debug, "htf_override_allowed"),
	}
	debug.Reasons = append(debug.Reasons,
		fmt.Sprintf("reversal_score=%.2f", candidate.Score),
		fmt.Sprintf("reversal_state=%s", candidate.Evaluation.State),
		fmt.Sprintf("reversal_lowest_low=%.8f", candidate.LocalLow),
		fmt.Sprintf("reversal_stop_hint=%.8f", candidate.StopPrice),
		fmt.Sprintf("reversal_timeout_bars=%d", candidate.TimeoutBars),
		fmt.Sprintf("reversal_position_size_multiplier=%.4f", candidate.PositionSizeMultiplier),
		fmt.Sprintf("htf_override_allowed=%t", boolFromDebug(candidate.Evaluation.Debug, "htf_override_allowed")),
		fmt.Sprintf("orderbook_allowed=%t", boolFromDebug(candidate.Evaluation.Debug, "orderbook_allowed")),
	)
	return debug
}

func reversalLongOrderbookResult(evaluation ReversalLongEvaluation) models.OrderbookResult {
	reason := "reversal_soft_orderbook_allowed"
	if !boolFromDebug(evaluation.Debug, "orderbook_soft_check_enabled") {
		reason = "reversal_soft_orderbook_disabled"
	} else if !boolFromDebug(evaluation.Debug, "orderbook_available") {
		reason = "reversal_soft_orderbook_unavailable_allowed"
	}
	return models.OrderbookResult{
		Evaluated: true,
		Passed:    true,
		Ratio:     floatFromDebug(evaluation.Debug, "orderbook_ratio"),
		Imbalance: floatFromDebug(evaluation.Debug, "orderbook_imbalance"),
		Side:      "LONG",
		Reason:    reason,
		History:   string(models.ModeReversalLong),
	}
}

func isReversalLongSignal(sig models.Signal) bool {
	return sig.Mode == models.ModeReversalLong
}

func (t *Trader) runtimeStrengthThreshold(sig models.Signal) int {
	threshold := t.Config.SignalStrengthThreshold
	if isReversalLongSignal(sig) {
		reversalThreshold := t.Config.ReversalLong.MinRuntimeStrength
		if reversalThreshold > threshold {
			threshold = reversalThreshold
		}
		return threshold
	}
	if sig.HighConf {
		return 1
	}
	return threshold
}

func (t *Trader) reversalLongRuntimeRisk(sig models.Signal, entry float64) reversalLongRuntimeRisk {
	risk := reversalLongRuntimeRisk{
		IsReversal:             isReversalLongSignal(sig),
		LocalLow:               sig.Debug.ReversalLowestLow,
		PositionSizeMultiplier: safeReversalPositionSizeMultiplier(t.Config.ReversalLong),
		TimeoutBars:            t.Config.ReversalLong.TimeoutBars,
	}
	if !risk.IsReversal {
		return risk
	}
	stopPrice := sig.Debug.ReversalStopPrice
	if stopPrice <= 0 {
		var ok bool
		stopPrice, ok = reversalLongStopPrice(risk.LocalLow, t.Config.ReversalLong.StopBelowLocalLowBps)
		if !ok {
			risk.RejectReason = ReversalReasonLocalLowNoStop
			return risk
		}
	}
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	stopPrice = t.roundSL(entry, stopPrice, tick, "LONG")
	if risk.LocalLow > 0 && stopPrice >= risk.LocalLow {
		stopPrice = risk.LocalLow - tick
	}
	risk.StopPrice = stopPrice
	switch {
	case risk.LocalLow <= 0:
		risk.RejectReason = ReversalReasonLocalLowNoStop
	case stopPrice <= 0:
		risk.RejectReason = ReversalReasonStopInvalid
	case entry > 0 && stopPrice >= entry:
		risk.RejectReason = ReversalReasonStopInvalid
	default:
		risk.StopValid = true
	}
	return risk
}

func (t *Trader) reversalLongRuntimeOrderbookResult() models.OrderbookResult {
	input := t.reversalOrderbookInput()
	result := CheckReversalOrderbookSoft(t.Config.ReversalLong, input)
	obResult := models.OrderbookResult{
		Evaluated: true,
		Passed:    result.Allowed,
		Ratio:     input.Ratio,
		Imbalance: input.Imbalance,
		Side:      "LONG",
		Threshold: t.Config.ReversalLong.MaxAskWallRatio,
		Reason:    ReversalReasonRuntimeOBPassed,
		History:   string(models.ModeReversalLong),
	}
	if !input.Available && t.Config.ReversalLong.RequireSoftOrderbookAtRuntime {
		obResult.Passed = false
		obResult.Reason = ReversalReasonRuntimeOBUnavailable
		return obResult
	}
	if !result.Allowed {
		obResult.Passed = false
		if result.RejectReason != "" {
			obResult.Reason = result.RejectReason
		} else {
			obResult.Reason = ReversalReasonRuntimeOBHostile
		}
		return obResult
	}
	return obResult
}

func (t *Trader) logReversalLongRuntimeGate(sig models.Signal, gate reversalLongRuntimeGateDecision) {
	if t == nil || t.Logger == nil || !isReversalLongSignal(sig) {
		return
	}
	t.Logger.Info("event=reversal_long_runtime_gate symbol=%s mode=%s side=%s trade_enabled=%t strength=%d required_strength=%d passed_strength=%t position_open=%t pending_order=%t orderbook_runtime_required=%t orderbook_runtime_available=%t orderbook_runtime_allowed=%t orderbook_runtime_reject_reason=%s local_low=%.8f stop_below_local_low_bps=%.4f stop_price=%.8f stop_valid=%t risk_profile_applied=%t position_size_multiplier=%.4f normal_size=%.8f final_size=%.8f final_decision=%s",
		t.Config.Symbol,
		sig.Mode,
		sig.Direction,
		t.Config.ReversalLong.TradeEnabled,
		gate.Strength,
		gate.RequiredStrength,
		gate.PassedStrength,
		gate.PositionOpen,
		gate.PendingOrder,
		gate.OrderbookRequired,
		gate.OrderbookAvailable,
		gate.OrderbookAllowed,
		gate.OrderbookRejectReason,
		gate.LocalLow,
		gate.StopBelowLocalLowBps,
		gate.StopPrice,
		gate.StopValid,
		gate.RiskProfileApplied,
		gate.PositionSizeMultiplier,
		gate.NormalSize,
		gate.FinalSize,
		gate.FinalDecision,
	)
}

func (t *Trader) logReversalLongRisk(sig models.Signal, size positionSizeDecision, risk reversalLongRuntimeRisk) {
	if t == nil || t.Logger == nil || !isReversalLongSignal(sig) {
		return
	}
	t.Logger.Info("event=reversal_long_risk mode=%s normal_size=%.8f position_size_multiplier=%.4f final_size=%.8f risk_profile_applied=%t local_low=%.8f stop_price=%.8f stop_valid=%t timeout_bars=%d",
		sig.Mode,
		size.NormalQty,
		size.Multiplier,
		size.FinalQty,
		size.RiskProfileApplied,
		risk.LocalLow,
		risk.StopPrice,
		risk.StopValid,
		risk.TimeoutBars,
	)
}

func (t *Trader) reversalOrderbookInput() ReversalOrderbookInput {
	if t == nil || t.State == nil {
		return ReversalOrderbookInput{UnavailableReason: ReversalReasonOrderbookNoData}
	}
	snapshot := t.readOrderbookSnapshot()
	if len(snapshot.bids) == 0 || len(snapshot.asks) == 0 {
		return ReversalOrderbookInput{UnavailableReason: ReversalReasonOrderbookNoData}
	}
	levels := t.Config.OrderbookLevels
	if levels <= 0 {
		levels = 3
	}
	topBidDepth := sumOrderbookTop(snapshot.bids, levels)
	topAskDepth := sumOrderbookTop(snapshot.asks, levels)
	bestBid := snapshot.bids[0]
	bestAsk := snapshot.asks[0]
	input := ReversalOrderbookInput{
		Available:   true,
		BestBid:     bestBid.price,
		BestAsk:     bestAsk.price,
		Spread:      bestAsk.price - bestBid.price,
		BestBidSize: bestBid.size,
		BestAskSize: bestAsk.size,
		DepthLevels: levels,
	}
	mid := (bestBid.price + bestAsk.price) / 2
	if mid > 0 {
		input.SpreadBps = input.Spread / mid * 10000
	}
	if topAskDepth > 0 {
		input.Ratio = topBidDepth / topAskDepth
	}
	if topBidDepth+topAskDepth > 0 {
		input.Imbalance = (topBidDepth - topAskDepth) / (topBidDepth + topAskDepth)
	}
	if topBidDepth > 0 {
		input.AskWallRatio = topAskDepth / topBidDepth
	} else if topAskDepth > 0 {
		input.AskWallRatio = cfgLargeRatio
	}
	history := t.orderbookHistoryCopy("LONG")
	medianRatio := medianFloat64(history)
	if medianRatio > 0 && input.Ratio > 0 {
		input.BidStabilityRatio = input.Ratio / medianRatio
		input.BidStabilityAvailable = true
	}
	return input
}

func (t *Trader) logReversalLongEvaluation(evaluation ReversalLongEvaluation, snapshot models.IndicatorSnapshot) {
	if t == nil || t.Logger == nil || !evaluation.Enabled {
		return
	}
	t.Logger.Debug("event=reversal_long_evaluation symbol=%s time=%s mode=%s enabled=%t state=%s previous_state=%s candidate_ready=%t reject_reason=%s score=%.2f score_rsi_oversold=%.2f score_volume_spike=%.2f score_bb_reclaim=%.2f score_rsi_recovery=%.2f score_macd_recovery=%.2f score_no_new_low=%.2f rsi=%.2f oversold_rsi=%.2f max_entry_rsi=%.2f rsi_prev=%s rsi_delta=%s rsi_delta_available=%t macd_hist=%.4f macd_hist_prev=%s macd_hist_delta=%s macd_hist_delta_available=%t volume=%s volume_sma=%s volume_ratio=%s min_volume_ratio=%.4f volume_available=%t volume_ratio_available=%t volume_spike_passed=%t bb_position=%s was_below_lower_bb=%s returned_inside_bb=%s bb_reclaim_available=%t lowest_low=%.8f current_low=%.8f bars_since_start=%d bars_without_new_low=%d timeout_bars=%d no_new_low_bars=%d htf_available=%t htf_bias=%s htf_reason=%s htf_allowed=%t htf_override_allowed=%t htf_extreme_downtrend=%t htf_extreme_downtrend_available=%t htf_reject_reason=%s orderbook_available=%t orderbook_soft_check_enabled=%t orderbook_allowed=%t orderbook_hostile=%t orderbook_reject_reason=%s orderbook_ratio=%.4f orderbook_imbalance=%.4f best_bid_size=%.4f best_ask_size=%.4f spread=%.8f spread_bps=%.4f max_spread_bps=%.4f ask_wall_ratio=%.4f max_ask_wall_ratio=%.4f bid_stability_ratio=%.4f min_bid_stability_ratio=%.4f",
		evaluation.Symbol,
		evaluation.Time.Format(time.RFC3339Nano),
		evaluation.Mode,
		evaluation.Enabled,
		evaluation.State,
		evaluation.PreviousState,
		evaluation.CandidateReady,
		evaluation.RejectReason,
		evaluation.Score,
		floatFromDebug(evaluation.Debug, "score_rsi_oversold"),
		floatFromDebug(evaluation.Debug, "score_volume_spike"),
		floatFromDebug(evaluation.Debug, "score_bb_reclaim"),
		floatFromDebug(evaluation.Debug, "score_rsi_recovery"),
		floatFromDebug(evaluation.Debug, "score_macd_recovery"),
		floatFromDebug(evaluation.Debug, "score_no_new_low"),
		snapshot.RSI,
		floatFromDebug(evaluation.Debug, "oversold_rsi"),
		floatFromDebug(evaluation.Debug, "max_entry_rsi"),
		formatDiagnosticFloat(snapshot.RSIPrev, snapshot.RSIDeltaAvailable, "%.2f"),
		formatDiagnosticFloat(snapshot.RSIDelta, snapshot.RSIDeltaAvailable, "%.2f"),
		snapshot.RSIDeltaAvailable,
		snapshot.MACDHist,
		formatDiagnosticFloat(snapshot.MACDHistPrev, snapshot.MACDHistDeltaAvailable, "%.4f"),
		formatDiagnosticFloat(snapshot.MACDHistDelta, snapshot.MACDHistDeltaAvailable, "%.4f"),
		snapshot.MACDHistDeltaAvailable,
		formatDiagnosticFloat(snapshot.Volume, snapshot.VolumeAvailable, "%.4f"),
		formatDiagnosticFloat(snapshot.VolumeSMA, snapshot.VolumeAvailable, "%.4f"),
		formatDiagnosticFloat(snapshot.VolumeRatio, snapshot.VolumeAvailable, "%.4f"),
		floatFromDebug(evaluation.Debug, "min_volume_ratio"),
		snapshot.VolumeAvailable,
		boolFromDebug(evaluation.Debug, "volume_ratio_available"),
		boolFromDebug(evaluation.Debug, "volume_spike_passed"),
		snapshot.BBPosition,
		formatDiagnosticBool(snapshot.WasBelowLowerBB, snapshot.BBReclaimAvailable),
		formatDiagnosticBool(snapshot.ReturnedInsideBB, snapshot.BBReclaimAvailable),
		snapshot.BBReclaimAvailable,
		floatFromDebug(evaluation.Debug, "lowest_low"),
		floatFromDebug(evaluation.Debug, "current_low"),
		intFromDebug(evaluation.Debug, "bars_since_start"),
		intFromDebug(evaluation.Debug, "bars_without_new_low"),
		intFromDebug(evaluation.Debug, "timeout_bars"),
		intFromDebug(evaluation.Debug, "no_new_low_bars"),
		boolFromDebug(evaluation.Debug, "htf_available"),
		stringFromDebug(evaluation.Debug, "htf_bias"),
		stringFromDebug(evaluation.Debug, "htf_reason"),
		boolFromDebug(evaluation.Debug, "htf_allowed"),
		boolFromDebug(evaluation.Debug, "htf_override_allowed"),
		boolFromDebug(evaluation.Debug, "htf_extreme_downtrend"),
		boolFromDebug(evaluation.Debug, "htf_extreme_downtrend_available"),
		stringFromDebug(evaluation.Debug, "htf_reject_reason"),
		boolFromDebug(evaluation.Debug, "orderbook_available"),
		boolFromDebug(evaluation.Debug, "orderbook_soft_check_enabled"),
		boolFromDebug(evaluation.Debug, "orderbook_allowed"),
		boolFromDebug(evaluation.Debug, "orderbook_hostile"),
		stringFromDebug(evaluation.Debug, "orderbook_reject_reason"),
		floatFromDebug(evaluation.Debug, "orderbook_ratio"),
		floatFromDebug(evaluation.Debug, "orderbook_imbalance"),
		floatFromDebug(evaluation.Debug, "best_bid_size"),
		floatFromDebug(evaluation.Debug, "best_ask_size"),
		floatFromDebug(evaluation.Debug, "spread"),
		floatFromDebug(evaluation.Debug, "spread_bps"),
		floatFromDebug(evaluation.Debug, "max_spread_bps"),
		floatFromDebug(evaluation.Debug, "ask_wall_ratio"),
		floatFromDebug(evaluation.Debug, "max_ask_wall_ratio"),
		floatFromDebug(evaluation.Debug, "bid_stability_ratio"),
		floatFromDebug(evaluation.Debug, "min_bid_stability_ratio"),
	)
}

func (t *Trader) logReversalLongPipelineDecision(decision reversalLongPipelineDecision, signalEmitted bool) {
	if t == nil || t.Logger == nil || !decision.Active {
		return
	}
	t.Logger.Debug("event=reversal_long_pipeline_decision symbol=%s time=%s enabled=%t trade_enabled=%t candidate_ready=%t candidate_score=%.2f state=%s reject_reason=%s pipeline_allowed=%t pipeline_selected=%t pipeline_skip_reason=%s selected_mode=%s selected_side=%s existing_signal_side=%s conflict_short=%t position_open=%t pending_order=%t signal_emitted=%t order_sent=false",
		t.Config.Symbol,
		t.now().Format(time.RFC3339Nano),
		decision.Enabled,
		decision.TradeEnabled,
		decision.CandidateReady,
		decision.CandidateScore,
		decision.State,
		decision.RejectReason,
		decision.PipelineAllowed,
		decision.PipelineSelected,
		decision.PipelineSkipReason,
		decision.SelectedMode,
		decision.SelectedSide,
		decision.ExistingSignalSide,
		decision.ConflictShort,
		decision.PositionOpen,
		decision.PendingOrder,
		signalEmitted && decision.PipelineSelected,
	)
}

func floatFromDebug(debug map[string]any, key string) float64 {
	value, _ := debug[key].(float64)
	return value
}

func intFromDebug(debug map[string]any, key string) int {
	value, _ := debug[key].(int)
	return value
}

func boolFromDebug(debug map[string]any, key string) bool {
	value, _ := debug[key].(bool)
	return value
}

func stringFromDebug(debug map[string]any, key string) string {
	value, _ := debug[key].(string)
	return value
}
