package strategy

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"verbose-fortnight/config"
)

type ReversalReplayTimelineRow struct {
	Time               time.Time
	Close              float64
	Low                float64
	ATR                float64
	VolumeRatio        float64
	RSI                float64
	RSIDelta           float64
	MACDHist           float64
	MACDHistDelta      float64
	BBPosition         string
	WasBelowLowerBB    bool
	ReturnedInsideBB   bool
	BarsWithoutNewLow  int
	Score              float64
	State              string
	RejectReason       string
	HTFBias            string
	HTFAllowed         bool
	HTFOverrideAllowed bool
	OrderbookAvailable bool
	OrderbookAllowed   bool
	OrderbookHostile   bool
	CandidateReady     bool
	ScoreRSIOversold   float64
	ScoreVolumeSpike   float64
	ScoreBBReclaim     float64
	ScoreRSIRecovery   float64
	ScoreMACDRecovery  float64
	ScoreNoNewLow      float64
	OrderbookRatio     float64
	OrderbookImbalance float64
	SpreadBps          float64
	NoNewLowBars       int

	SetupDetectedAt           *time.Time
	TriggerCheckedAt          *time.Time
	SetupAgeBars              int
	SetupInvalidated          bool
	InvalidationReason        string
	MemoryReclaimUsed         bool
	SetupMemoryCandidateReady bool
	SetupMemoryScore          float64
}

type ReversalClosestCandidate struct {
	Row               ReversalReplayTimelineRow
	PassedConditions  []string
	FailedConditions  []string
	MissingComponents []string
}

type ReversalBlockerAnalysis struct {
	Summary                   ReversalReplaySummary
	Timeline                  []ReversalReplayTimelineRow
	MaxScore                  float64
	MaxScoreTime              time.Time
	ClosestCandidate          ReversalClosestCandidate
	MainBlocker               string
	MainBlockerDetail         string
	MainInvalidationReason    string
	FirstSetupDetectedAt      *time.Time
	FirstInvalidationAt       *time.Time
	InvalidationAfterBars     int
	ReclaimWasRemembered      bool
	SetupDetectedCount        int
	SetupInvalidatedCount     int
	SetupMemoryCandidateCount int
	FirstSetupMemoryCandidate *time.Time
}

type ReversalSensitivityOptions struct {
	MinRSIDeltaList                      []float64
	NoNewLowBarsList                     []int
	ScoreThresholdList                   []float64
	MinVolumeRatioList                   []float64
	SetupMemoryBarsList                  []int
	InvalidationATRMultList              []float64
	RequireOrderbookNotHostileList       []bool
	MinOrderbookRatioList                []float64
	MinBarsWithoutNewLowAfterReclaimList []int
	RequireCloseAboveMiddleBBList        []bool
	RequireHigherLowAfterReclaimList     []bool
	OutcomeTPBpsList                     []float64
	OutcomeSLBpsList                     []float64
	OutcomeFeeBps                        float64
	OutcomeSlippageBps                   float64
}

type ReversalSensitivityResult struct {
	MinRSIDelta                         float64                           `json:"min_rsi_delta"`
	NoNewLowBars                        int                               `json:"no_new_low_bars"`
	ScoreThreshold                      float64                           `json:"score_threshold"`
	MinVolumeRatio                      float64                           `json:"min_volume_ratio"`
	SetupMemoryBars                     int                               `json:"setup_memory_bars"`
	InvalidationATRMult                 float64                           `json:"invalidation_atr_mult"`
	RequireOrderbookNotHostile          bool                              `json:"require_orderbook_not_hostile"`
	MinOrderbookRatio                   float64                           `json:"min_orderbook_ratio"`
	MinBarsWithoutNewLowAfterReclaim    int                               `json:"min_bars_without_new_low_after_reclaim"`
	RequireCloseAboveMiddleBB           bool                              `json:"require_close_above_middle_bb"`
	RequireHigherLowAfterReclaim        bool                              `json:"require_higher_low_after_reclaim"`
	UnfilteredCandidateReadyCount       int                               `json:"unfiltered_candidate_ready_count"`
	CandidateReadyCount                 int                               `json:"candidate_ready_count"`
	OfflineFilterRejectedCandidateCount int                               `json:"offline_filter_rejected_candidate_count"`
	OfflineFilterRejectReasons          map[string]int                    `json:"offline_filter_reject_reasons,omitempty"`
	SetupDetectedCount                  int                               `json:"setup_detected_count"`
	SetupInvalidatedCount               int                               `json:"setup_invalidated_count"`
	FirstCandidateTime                  *time.Time                        `json:"first_candidate_time,omitempty"`
	MaxScore                            float64                           `json:"max_score"`
	MaxScoreTime                        *time.Time                        `json:"max_score_time,omitempty"`
	EmittedSignalDryRunCount            int                               `json:"emitted_signal_dry_run_count"`
	RuntimePassCount                    int                               `json:"runtime_pass_count"`
	RuntimeBlockCount                   int                               `json:"runtime_block_count"`
	MainBlocker                         string                            `json:"main_blocker"`
	OrdersSent                          int                               `json:"orders_sent"`
	Blockers                            map[string]int                    `json:"blockers,omitempty"`
	Summary                             ReversalReplaySummary             `json:"summary"`
	CandidateOutcomes                   []ReversalCandidateOutcome        `json:"candidate_outcomes,omitempty"`
	OutcomeSummaries                    []ReversalCandidateOutcomeSummary `json:"outcome_summaries,omitempty"`
}

type ReversalCandidateFilterOptions struct {
	RequireOrderbookNotHostile       bool
	MinOrderbookRatio                float64
	MinBarsWithoutNewLowAfterReclaim int
	RequireCloseAboveMiddleBB        bool
	RequireHigherLowAfterReclaim     bool
}

const ReversalCandidateOutcomeWindow = 30 * time.Minute

type ReversalCandidateOutcome struct {
	Time                 time.Time `json:"time"`
	EntryPrice           float64   `json:"entry_price"`
	TPBps                float64   `json:"tp_bps"`
	SLBps                float64   `json:"sl_bps"`
	FeeBps               float64   `json:"fee_bps"`
	SlippageBps          float64   `json:"slippage_bps"`
	ForwardReturn5mBps   float64   `json:"forward_return_5m_bps"`
	ForwardReturn15mBps  float64   `json:"forward_return_15m_bps"`
	ForwardReturn30mBps  float64   `json:"forward_return_30m_bps"`
	MaxFavorable30mBps   float64   `json:"max_favorable_30m_bps"`
	MaxAdverse30mBps     float64   `json:"max_adverse_30m_bps"`
	TimeToMFE            string    `json:"time_to_mfe"`
	TimeToMAE            string    `json:"time_to_mae"`
	WouldHitTP           bool      `json:"would_hit_tp"`
	WouldHitSL           bool      `json:"would_hit_sl"`
	HitOrder             string    `json:"hit_order"`
	NetReturnBps         float64   `json:"net_return_bps"`
	NetR                 float64   `json:"net_r"`
	ForwardWindowEndTime time.Time `json:"forward_window_end_time,omitempty"`
}

type ReversalCandidateOutcomeSummary struct {
	TPBps                 float64 `json:"tp_bps"`
	SLBps                 float64 `json:"sl_bps"`
	FeeBps                float64 `json:"fee_bps"`
	SlippageBps           float64 `json:"slippage_bps"`
	CandidateCount        int     `json:"candidate_count"`
	WinningCandidateCount int     `json:"winning_candidate_count"`
	WinRate               float64 `json:"win_rate"`
	AvgNetR               float64 `json:"avg_net_r"`
	MedianNetR            float64 `json:"median_net_r"`
	AvgMFEBps             float64 `json:"avg_mfe_bps"`
	AvgMAEBps             float64 `json:"avg_mae_bps"`
}

type ReversalSetupMemoryOptions struct {
	SetupMemoryBars     int
	InvalidationATRMult float64
}

type reversalSetupMemoryStats struct {
	SetupDetectedCount    int
	SetupInvalidatedCount int
	CandidateReadyCount   int
	FirstCandidateTime    *time.Time
}

type replaySetupMemoryState struct {
	Active            bool
	SetupIndex        int
	SetupDetectedAt   time.Time
	SetupLow          float64
	LowestLow         float64
	BarsWithoutNewLow int
	HadRSIOversold    bool
}

func BuildReversalBlockerAnalysis(result ReversalReplayResult, cfg config.ReversalLongConfig, setupOpts ...ReversalSetupMemoryOptions) ReversalBlockerAnalysis {
	timeline := ReversalReplayTimeline(result)
	opts := ReversalSetupMemoryOptions{}
	if len(setupOpts) > 0 {
		opts = setupOpts[0]
	}
	stats := applyReplaySetupMemory(timeline, cfg, opts)
	analysis := ReversalBlockerAnalysis{
		Summary:                   result.Summary,
		Timeline:                  timeline,
		MaxScore:                  -1,
		SetupDetectedCount:        stats.SetupDetectedCount,
		SetupInvalidatedCount:     stats.SetupInvalidatedCount,
		SetupMemoryCandidateCount: stats.CandidateReadyCount,
		FirstSetupMemoryCandidate: stats.FirstCandidateTime,
	}
	for _, row := range timeline {
		if row.Score > analysis.MaxScore {
			analysis.MaxScore = row.Score
			analysis.MaxScoreTime = row.Time
			analysis.ClosestCandidate = ReversalClosestCandidate{Row: row}
		}
	}
	if analysis.MaxScore < 0 {
		analysis.MaxScore = 0
	}
	analysis.MainBlocker = mainBlocker(result.Summary.Blockers)
	applySetupMemoryBlockerSummary(&analysis, opts)
	analysis.ClosestCandidate.PassedConditions, analysis.ClosestCandidate.FailedConditions, analysis.ClosestCandidate.MissingComponents = classifyReplayRow(analysis.ClosestCandidate.Row, cfg)
	return analysis
}

func applySetupMemoryBlockerSummary(analysis *ReversalBlockerAnalysis, opts ReversalSetupMemoryOptions) {
	for _, row := range analysis.Timeline {
		if row.SetupDetectedAt != nil && analysis.FirstSetupDetectedAt == nil {
			analysis.FirstSetupDetectedAt = timePtr(*row.SetupDetectedAt)
		}
		if row.MemoryReclaimUsed {
			analysis.ReclaimWasRemembered = true
		}
		if row.SetupInvalidated && analysis.FirstInvalidationAt == nil {
			analysis.FirstInvalidationAt = timePtr(row.Time)
			analysis.InvalidationAfterBars = row.SetupAgeBars
			analysis.MainInvalidationReason = row.InvalidationReason
		}
	}
	if opts.SetupMemoryBars > 0 && analysis.SetupDetectedCount > 0 && analysis.SetupInvalidatedCount > 0 {
		analysis.MainBlocker = reversalMainBlockerSetupInvalidated
		analysis.MainBlockerDetail = analysis.MainInvalidationReason
		return
	}
	analysis.MainBlockerDetail = analysis.MainBlocker
}

func applyReplaySetupMemory(rows []ReversalReplayTimelineRow, cfg config.ReversalLongConfig, opts ReversalSetupMemoryOptions) reversalSetupMemoryStats {
	stats := reversalSetupMemoryStats{}
	if opts.SetupMemoryBars <= 0 {
		for i := range rows {
			row := &rows[i]
			if replaySetupDetected(*row) {
				row.SetupDetectedAt = timePtr(row.Time)
				row.SetupAgeBars = 0
				row.SetupMemoryScore = row.Score
				stats.SetupDetectedCount++
			}
		}
		return stats
	}
	state := replaySetupMemoryState{}
	for i := range rows {
		row := &rows[i]
		if state.Active {
			age := i - state.SetupIndex
			row.SetupDetectedAt = timePtr(state.SetupDetectedAt)
			row.TriggerCheckedAt = timePtr(row.Time)
			row.SetupAgeBars = age
			if reason := replaySetupInvalidationReason(*row, state, opts, cfg); reason != "" {
				row.MemoryReclaimUsed = replayMemoryReclaimUsed(*row, state)
				row.SetupInvalidated = true
				row.InvalidationReason = reason
				stats.SetupInvalidatedCount++
				state.Active = false
			} else {
				updateReplaySetupStability(row, &state, age)
				row.SetupAgeBars = age
				row.MemoryReclaimUsed = replayMemoryReclaimUsed(*row, state)
				row.SetupMemoryScore = replaySetupMemoryScore(*row, state, cfg)
				if replaySetupMemoryCandidateReady(*row, cfg, opts, state) {
					row.SetupMemoryCandidateReady = true
					stats.CandidateReadyCount++
					if stats.FirstCandidateTime == nil {
						stats.FirstCandidateTime = timePtr(row.Time)
					}
				}
			}
		}
		if replaySetupDetected(*row) {
			state = replaySetupMemoryState{
				Active:            true,
				SetupIndex:        i,
				SetupDetectedAt:   row.Time,
				SetupLow:          row.Low,
				LowestLow:         row.Low,
				BarsWithoutNewLow: 0,
				HadRSIOversold:    row.ScoreRSIOversold > 0,
			}
			row.SetupDetectedAt = timePtr(row.Time)
			row.SetupAgeBars = 0
			row.SetupMemoryScore = replaySetupMemoryScore(*row, state, cfg)
			stats.SetupDetectedCount++
		}
	}
	return stats
}

func replaySetupDetected(row ReversalReplayTimelineRow) bool {
	return row.ScoreBBReclaim > 0 || (row.WasBelowLowerBB && row.ReturnedInsideBB)
}

func replaySetupInvalidationReason(row ReversalReplayTimelineRow, state replaySetupMemoryState, opts ReversalSetupMemoryOptions, cfg config.ReversalLongConfig) string {
	age := row.SetupAgeBars
	if age > opts.SetupMemoryBars {
		return "setup_memory_expired"
	}
	if age > 0 && row.BBPosition == bbPositionBelowLower {
		return "closed_below_lower_bb_again"
	}
	threshold := replaySubstantialLowDistance(row, opts)
	if state.LowestLow > 0 && row.Low > 0 && row.Low < state.LowestLow-threshold {
		return "substantial_new_low"
	}
	momentumThreshold := cfg.MinRSIDelta
	if momentumThreshold < 5 {
		momentumThreshold = 5
	}
	if age > 0 && row.RSIDelta <= -momentumThreshold && row.MACDHistDelta < 0 {
		return "momentum_worsened"
	}
	return ""
}

func replaySubstantialLowDistance(row ReversalReplayTimelineRow, opts ReversalSetupMemoryOptions) float64 {
	if opts.InvalidationATRMult <= 0 || row.ATR <= 0 {
		return 0
	}
	return row.ATR * opts.InvalidationATRMult
}

func updateReplaySetupStability(row *ReversalReplayTimelineRow, state *replaySetupMemoryState, age int) {
	if row.Low <= 0 {
		row.BarsWithoutNewLow = state.BarsWithoutNewLow
		return
	}
	if state.LowestLow <= 0 || row.Low < state.LowestLow {
		state.LowestLow = row.Low
		state.BarsWithoutNewLow = 0
	} else if age > 0 {
		state.BarsWithoutNewLow++
	}
	row.BarsWithoutNewLow = state.BarsWithoutNewLow
}

func replayMemoryReclaimUsed(row ReversalReplayTimelineRow, state replaySetupMemoryState) bool {
	return state.Active && row.ScoreBBReclaim == 0 && !replaySetupDetected(row)
}

func replaySetupMemoryScore(row ReversalReplayTimelineRow, state replaySetupMemoryState, cfg config.ReversalLongConfig) float64 {
	score := row.Score
	if state.HadRSIOversold && row.ScoreRSIOversold == 0 {
		score += reversalScoreRSIOversold
	}
	if state.Active && row.ScoreBBReclaim == 0 {
		score += reversalScoreBBReclaim
	}
	if (cfg.NoNewLowBars <= 0 || state.BarsWithoutNewLow >= cfg.NoNewLowBars) && row.ScoreNoNewLow == 0 {
		score += reversalScoreNoNewLow
	}
	return score
}

func replaySetupMemoryCandidateReady(row ReversalReplayTimelineRow, cfg config.ReversalLongConfig, opts ReversalSetupMemoryOptions, state replaySetupMemoryState) bool {
	if !state.Active || opts.SetupMemoryBars <= 0 || row.SetupAgeBars <= 0 || row.SetupAgeBars > opts.SetupMemoryBars {
		return false
	}
	if row.RSIDelta < cfg.MinRSIDelta {
		return false
	}
	if cfg.MaxEntryRSI > 0 && row.RSI > cfg.MaxEntryRSI {
		return false
	}
	if cfg.RequireMACDHistRecovery && row.MACDHistDelta < cfg.MinMACDHistDelta {
		return false
	}
	if cfg.NoNewLowBars > 0 && state.BarsWithoutNewLow < cfg.NoNewLowBars {
		return false
	}
	if !row.HTFAllowed || !row.OrderbookAllowed {
		return false
	}
	return replaySetupMemoryScore(row, state, cfg) >= cfg.ScoreThreshold
}

func ReversalReplayTimeline(result ReversalReplayResult) []ReversalReplayTimelineRow {
	rows := make([]ReversalReplayTimelineRow, 0, len(result.Events))
	for _, event := range result.Events {
		if stringValue(event["event"]) != "reversal_long_evaluation" {
			continue
		}
		rows = append(rows, ReversalReplayTimelineRow{
			Time:               timeValue(event["time"]),
			Close:              floatValue(event["close"]),
			Low:                floatValue(event["low"]),
			ATR:                floatValue(event["atr"]),
			VolumeRatio:        floatValue(event["volume_ratio"]),
			RSI:                floatValue(event["rsi"]),
			RSIDelta:           floatValue(event["rsi_delta"]),
			MACDHist:           floatValue(event["macd_hist"]),
			MACDHistDelta:      floatValue(event["macd_hist_delta"]),
			BBPosition:         stringValue(event["bb_position"]),
			WasBelowLowerBB:    boolValue(event["was_below_lower_bb"]),
			ReturnedInsideBB:   boolValue(event["returned_inside_bb"]),
			BarsWithoutNewLow:  intValue(event["bars_without_new_low"]),
			Score:              floatValue(event["score"]),
			State:              stringValue(event["state"]),
			RejectReason:       stringValue(event["reject_reason"]),
			HTFBias:            stringValue(event["htf_bias"]),
			HTFAllowed:         boolValue(event["htf_allowed"]),
			HTFOverrideAllowed: boolValue(event["htf_override_allowed"]),
			OrderbookAvailable: boolValue(event["orderbook_available"]),
			OrderbookAllowed:   boolValue(event["orderbook_allowed"]),
			OrderbookHostile:   boolValue(event["orderbook_hostile"]),
			CandidateReady:     boolValue(event["candidate_ready"]),
			ScoreRSIOversold:   floatValue(event["score_rsi_oversold"]),
			ScoreVolumeSpike:   floatValue(event["score_volume_spike"]),
			ScoreBBReclaim:     floatValue(event["score_bb_reclaim"]),
			ScoreRSIRecovery:   floatValue(event["score_rsi_recovery"]),
			ScoreMACDRecovery:  floatValue(event["score_macd_recovery"]),
			ScoreNoNewLow:      floatValue(event["score_no_new_low"]),
			OrderbookRatio:     floatValue(event["orderbook_ratio"]),
			OrderbookImbalance: floatValue(event["orderbook_imbalance"]),
			SpreadBps:          floatValue(event["spread_bps"]),
			NoNewLowBars:       intValue(event["no_new_low_bars"]),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Time.Before(rows[j].Time) })
	return rows
}

func WriteReversalBlockerMarkdown(w io.Writer, result ReversalReplayResult, cfg config.ReversalLongConfig, setupOpts ...ReversalSetupMemoryOptions) error {
	analysis := BuildReversalBlockerAnalysis(result, cfg, setupOpts...)
	_, err := fmt.Fprintf(w, "# Reversal Long Blocker Analysis\n\n")
	if err != nil {
		return err
	}
	s := analysis.Summary
	fmt.Fprintf(w, "symbol: `%s`\n\n", s.Symbol)
	fmt.Fprintf(w, "window: `%s` - `%s`\n\n", s.From.Format(time.RFC3339), s.To.Format(time.RFC3339))
	fmt.Fprintf(w, "summary: candles_loaded=%d warmup_candles=%d target_candles=%d reversal_evaluations=%d candidate_ready=%d setup_detected=%d setup_invalidated=%d setup_memory_candidate_ready=%d pipeline_allowed=%d dry_run_signals=%d runtime_pass=%d runtime_block=%d orders_sent=%d\n\n",
		s.CandlesLoaded, s.WarmupCandles, s.TargetCandles, s.ReversalEvaluations, s.ReversalCandidateReadyCount, analysis.SetupDetectedCount, analysis.SetupInvalidatedCount, analysis.SetupMemoryCandidateCount, s.ReversalPipelineAllowedCount, s.ReversalSignalsEmittedDryRun, s.RuntimeReversalPassCount, s.RuntimeReversalBlockCount, s.OrdersSent)
	fmt.Fprintf(w, "blockers: `%s`\n\n", formatBlockers(s.Blockers))
	fmt.Fprintf(w, "main_blocker: `%s`\n\n", analysis.MainBlocker)
	fmt.Fprintf(w, "main_blocker_detail: `%s`\n\n", analysis.MainBlockerDetail)
	fmt.Fprintf(w, "first_setup_detected_at: `%s`\n\n", formatOptionalTime(analysis.FirstSetupDetectedAt))
	fmt.Fprintf(w, "first_invalidation_at: `%s`\n\n", formatOptionalTime(analysis.FirstInvalidationAt))
	fmt.Fprintf(w, "invalidation_after_bars: `%d`\n\n", analysis.InvalidationAfterBars)
	fmt.Fprintf(w, "reclaim_was_remembered: `%t`\n\n", analysis.ReclaimWasRemembered)

	closest := analysis.ClosestCandidate
	fmt.Fprintf(w, "## Closest reversal candidate\n\n")
	if closest.Row.Time.IsZero() {
		fmt.Fprintln(w, "No reversal evaluations were produced.")
	} else {
		row := closest.Row
		fmt.Fprintf(w, "time=%s score=%.2f state=%s reject_reason=%s\n\n", row.Time.Format(time.RFC3339), row.Score, row.State, row.RejectReason)
		fmt.Fprintf(w, "setup_detected_at=%s trigger_checked_at=%s setup_age_bars=%d setup_invalidated=%t invalidation_reason=%s memory_reclaim_used=%t setup_memory_score=%.2f setup_memory_candidate_ready=%t\n\n",
			formatOptionalTime(row.SetupDetectedAt), formatOptionalTime(row.TriggerCheckedAt), row.SetupAgeBars, row.SetupInvalidated, row.InvalidationReason, row.MemoryReclaimUsed, row.SetupMemoryScore, row.SetupMemoryCandidateReady)
		fmt.Fprintf(w, "passed: %s\n\n", strings.Join(closest.PassedConditions, ", "))
		fmt.Fprintf(w, "failed: %s\n\n", strings.Join(closest.FailedConditions, ", "))
		fmt.Fprintf(w, "missing_score_components: %s\n\n", strings.Join(closest.MissingComponents, ", "))
	}

	fmt.Fprintln(w, "## Timeline")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| time | close | low | volume_ratio | rsi | rsi_delta | macd_hist | macd_hist_delta | bb_position | was_below_lower_bb | returned_inside_bb | bars_without_new_low | score | setup_memory_score | state | reject_reason | setup_detected_at | trigger_checked_at | setup_age_bars | setup_invalidated | invalidation_reason | memory_reclaim_used | htf_bias | htf_allowed | orderbook_available | orderbook_allowed | candidate_ready | setup_memory_candidate_ready |")
	fmt.Fprintln(w, "| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- | ---: | ---: | ---: | --- | --- | --- | --- | ---: | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, row := range analysis.Timeline {
		fmt.Fprintf(w, "| %s | %.2f | %.2f | %.3f | %.2f | %.3f | %.4f | %.4f | %s | %t | %t | %d | %.2f | %.2f | %s | %s | %s | %s | %d | %t | %s | %t | %s | %t | %t | %t | %t | %t |\n",
			row.Time.Format(time.RFC3339), row.Close, row.Low, row.VolumeRatio, row.RSI, row.RSIDelta, row.MACDHist, row.MACDHistDelta, row.BBPosition, row.WasBelowLowerBB, row.ReturnedInsideBB, row.BarsWithoutNewLow, row.Score, row.SetupMemoryScore, row.State, row.RejectReason, formatOptionalTime(row.SetupDetectedAt), formatOptionalTime(row.TriggerCheckedAt), row.SetupAgeBars, row.SetupInvalidated, row.InvalidationReason, row.MemoryReclaimUsed, row.HTFBias, row.HTFAllowed, row.OrderbookAvailable, row.OrderbookAllowed, row.CandidateReady, row.SetupMemoryCandidateReady)
	}
	return nil
}

func RunReversalReplaySensitivity(candles []ReversalReplayCandle, opts ReversalReplayOptions, spec ReversalSensitivityOptions) ([]ReversalSensitivityResult, error) {
	return runReversalReplaySensitivity(opts, spec, candles, func(runOpts ReversalReplayOptions) (ReversalReplayResult, error) {
		return RunReversalReplayCandles(candles, runOpts)
	})
}

func RunReversalReplaySensitivityFrames(frames []ReversalReplayFrame, opts ReversalReplayOptions, spec ReversalSensitivityOptions) ([]ReversalSensitivityResult, error) {
	return runReversalReplaySensitivity(opts, spec, replayOutcomeCandlesFromFrames(frames), func(runOpts ReversalReplayOptions) (ReversalReplayResult, error) {
		return RunReversalReplayFrames(frames, runOpts)
	})
}

func runReversalReplaySensitivity(opts ReversalReplayOptions, spec ReversalSensitivityOptions, outcomeCandles []ReversalReplayCandle, run func(ReversalReplayOptions) (ReversalReplayResult, error)) ([]ReversalSensitivityResult, error) {
	baseCfg := config.LoadConfig()
	if opts.Config != nil {
		cfgCopy := *opts.Config
		baseCfg = &cfgCopy
	}
	spec = normalizeSensitivityOptions(spec, baseCfg.ReversalLong)
	filterGrid := reversalCandidateFilterGrid(spec)
	results := make([]ReversalSensitivityResult, 0, len(spec.MinRSIDeltaList)*len(spec.NoNewLowBarsList)*len(spec.ScoreThresholdList)*len(spec.MinVolumeRatioList)*len(spec.SetupMemoryBarsList)*len(spec.InvalidationATRMultList)*len(filterGrid))
	for _, minRSIDelta := range spec.MinRSIDeltaList {
		for _, noNewLowBars := range spec.NoNewLowBarsList {
			for _, scoreThreshold := range spec.ScoreThresholdList {
				for _, minVolumeRatio := range spec.MinVolumeRatioList {
					for _, setupMemoryBars := range spec.SetupMemoryBarsList {
						for _, invalidationATRMult := range spec.InvalidationATRMultList {
							cfgCopy := *baseCfg
							cfgCopy.ReversalLong.MinRSIDelta = minRSIDelta
							cfgCopy.ReversalLong.NoNewLowBars = noNewLowBars
							cfgCopy.ReversalLong.ScoreThreshold = scoreThreshold
							cfgCopy.ReversalLong.MinVolumeRatio = minVolumeRatio
							runOpts := opts
							runOpts.Config = &cfgCopy
							runOpts.OrdersEnabled = false
							result, err := run(runOpts)
							if err != nil {
								return nil, err
							}
							setupOpts := ReversalSetupMemoryOptions{SetupMemoryBars: setupMemoryBars, InvalidationATRMult: invalidationATRMult}
							analysis := BuildReversalBlockerAnalysis(result, cfgCopy.ReversalLong, setupOpts)
							baseCandidates := reversalCandidateRows(analysis.Timeline, setupMemoryBars)
							for _, filterOpts := range filterGrid {
								filteredCandidates, filterRejects := filterReversalCandidateRows(analysis.Timeline, baseCandidates, filterOpts)
								outcomes, outcomeSummaries := evaluateReversalCandidateRowsOutcomes(outcomeCandles, filteredCandidates, spec)
								sens := ReversalSensitivityResult{
									MinRSIDelta:                         minRSIDelta,
									NoNewLowBars:                        noNewLowBars,
									ScoreThreshold:                      scoreThreshold,
									MinVolumeRatio:                      minVolumeRatio,
									SetupMemoryBars:                     setupMemoryBars,
									InvalidationATRMult:                 invalidationATRMult,
									RequireOrderbookNotHostile:          filterOpts.RequireOrderbookNotHostile,
									MinOrderbookRatio:                   filterOpts.MinOrderbookRatio,
									MinBarsWithoutNewLowAfterReclaim:    filterOpts.MinBarsWithoutNewLowAfterReclaim,
									RequireCloseAboveMiddleBB:           filterOpts.RequireCloseAboveMiddleBB,
									RequireHigherLowAfterReclaim:        filterOpts.RequireHigherLowAfterReclaim,
									UnfilteredCandidateReadyCount:       len(baseCandidates),
									CandidateReadyCount:                 len(filteredCandidates),
									OfflineFilterRejectedCandidateCount: sumIntMap(filterRejects),
									OfflineFilterRejectReasons:          filterRejects,
									SetupDetectedCount:                  analysis.SetupDetectedCount,
									SetupInvalidatedCount:               analysis.SetupInvalidatedCount,
									FirstCandidateTime:                  firstFilteredCandidateTime(filteredCandidates),
									MaxScore:                            analysis.MaxScore,
									EmittedSignalDryRunCount:            result.Summary.ReversalSignalsEmittedDryRun,
									RuntimePassCount:                    result.Summary.RuntimeReversalPassCount,
									RuntimeBlockCount:                   result.Summary.RuntimeReversalBlockCount,
									MainBlocker:                         analysis.MainBlocker,
									OrdersSent:                          result.Summary.OrdersSent,
									Blockers:                            result.Summary.Blockers,
									Summary:                             result.Summary,
									CandidateOutcomes:                   outcomes,
									OutcomeSummaries:                    outcomeSummaries,
								}
								if !analysis.MaxScoreTime.IsZero() {
									t := analysis.MaxScoreTime
									sens.MaxScoreTime = &t
								}
								results = append(results, sens)
							}
						}
					}
				}
			}
		}
	}
	return results, nil
}

func replayOutcomeCandlesFromFrames(frames []ReversalReplayFrame) []ReversalReplayCandle {
	candles := make([]ReversalReplayCandle, 0, len(frames))
	for _, frame := range frames {
		ts := frameTime(frame)
		if ts.IsZero() {
			continue
		}
		if frame.Candle != nil {
			c := *frame.Candle
			if c.Time.IsZero() {
				c.Time = ts
			}
			candles = append(candles, c)
			continue
		}
		if frame.Snapshot == nil || frame.Snapshot.Close <= 0 {
			continue
		}
		low := frame.Snapshot.Low
		if low <= 0 {
			low = frame.Snapshot.Close
		}
		high := frame.Snapshot.Close
		if low > high {
			high = low
		}
		candles = append(candles, ReversalReplayCandle{
			Time:   ts,
			Open:   frame.Snapshot.Close,
			High:   high,
			Low:    low,
			Close:  frame.Snapshot.Close,
			Volume: frame.Snapshot.Volume,
		})
	}
	sort.Slice(candles, func(i, j int) bool { return candles[i].Time.Before(candles[j].Time) })
	return candles
}

func evaluateReversalCandidateOutcomes(candles []ReversalReplayCandle, rows []ReversalReplayTimelineRow, setupMemoryBars int, spec ReversalSensitivityOptions) ([]ReversalCandidateOutcome, []ReversalCandidateOutcomeSummary) {
	filterOpts := ReversalCandidateFilterOptions{}
	if len(spec.RequireOrderbookNotHostileList) == 1 {
		filterOpts.RequireOrderbookNotHostile = spec.RequireOrderbookNotHostileList[0]
	}
	if len(spec.MinOrderbookRatioList) == 1 {
		filterOpts.MinOrderbookRatio = spec.MinOrderbookRatioList[0]
	}
	if len(spec.MinBarsWithoutNewLowAfterReclaimList) == 1 {
		filterOpts.MinBarsWithoutNewLowAfterReclaim = spec.MinBarsWithoutNewLowAfterReclaimList[0]
	}
	if len(spec.RequireCloseAboveMiddleBBList) == 1 {
		filterOpts.RequireCloseAboveMiddleBB = spec.RequireCloseAboveMiddleBBList[0]
	}
	if len(spec.RequireHigherLowAfterReclaimList) == 1 {
		filterOpts.RequireHigherLowAfterReclaim = spec.RequireHigherLowAfterReclaimList[0]
	}
	candidates := reversalCandidateRows(rows, setupMemoryBars)
	if filterOpts != (ReversalCandidateFilterOptions{}) {
		candidates, _ = filterReversalCandidateRows(rows, candidates, filterOpts)
	}
	return evaluateReversalCandidateRowsOutcomes(candles, candidates, spec)
}

func evaluateReversalCandidateRowsOutcomes(candles []ReversalReplayCandle, candidates []ReversalReplayTimelineRow, spec ReversalSensitivityOptions) ([]ReversalCandidateOutcome, []ReversalCandidateOutcomeSummary) {
	candles = sortedReplayCandles(candles)
	if len(candles) == 0 {
		return nil, nil
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	outcomes := make([]ReversalCandidateOutcome, 0, len(candidates)*len(spec.OutcomeTPBpsList)*len(spec.OutcomeSLBpsList))
	for _, row := range candidates {
		base, ok := candidateForwardBase(candles, row)
		if !ok {
			continue
		}
		metrics, ok := computeForwardMetrics(candles, base, row)
		if !ok {
			continue
		}
		for _, tpBps := range spec.OutcomeTPBpsList {
			for _, slBps := range spec.OutcomeSLBpsList {
				if tpBps <= 0 || slBps <= 0 {
					continue
				}
				outcome := simulateCandidateTPSL(candles, base, metrics, tpBps, slBps, spec.OutcomeFeeBps, spec.OutcomeSlippageBps)
				outcomes = append(outcomes, outcome)
			}
		}
	}
	return outcomes, summarizeCandidateOutcomes(outcomes)
}

func reversalCandidateFilterGrid(spec ReversalSensitivityOptions) []ReversalCandidateFilterOptions {
	out := make([]ReversalCandidateFilterOptions, 0, len(spec.RequireOrderbookNotHostileList)*len(spec.MinOrderbookRatioList)*len(spec.MinBarsWithoutNewLowAfterReclaimList)*len(spec.RequireCloseAboveMiddleBBList)*len(spec.RequireHigherLowAfterReclaimList))
	for _, requireOrderbookNotHostile := range spec.RequireOrderbookNotHostileList {
		for _, minOrderbookRatio := range spec.MinOrderbookRatioList {
			for _, minBarsWithoutNewLowAfterReclaim := range spec.MinBarsWithoutNewLowAfterReclaimList {
				for _, requireCloseAboveMiddleBB := range spec.RequireCloseAboveMiddleBBList {
					for _, requireHigherLowAfterReclaim := range spec.RequireHigherLowAfterReclaimList {
						out = append(out, ReversalCandidateFilterOptions{
							RequireOrderbookNotHostile:       requireOrderbookNotHostile,
							MinOrderbookRatio:                minOrderbookRatio,
							MinBarsWithoutNewLowAfterReclaim: minBarsWithoutNewLowAfterReclaim,
							RequireCloseAboveMiddleBB:        requireCloseAboveMiddleBB,
							RequireHigherLowAfterReclaim:     requireHigherLowAfterReclaim,
						})
					}
				}
			}
		}
	}
	return out
}

func filterReversalCandidateRows(rows, candidates []ReversalReplayTimelineRow, opts ReversalCandidateFilterOptions) ([]ReversalReplayTimelineRow, map[string]int) {
	if opts == (ReversalCandidateFilterOptions{}) {
		return candidates, nil
	}
	setupLowByTime := setupLowByTime(rows)
	filtered := make([]ReversalReplayTimelineRow, 0, len(candidates))
	rejects := map[string]int{}
	for _, row := range candidates {
		if reason := reversalCandidateFilterRejectReason(row, setupLowByTime, opts); reason != "" {
			rejects[reason]++
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered, rejects
}

func reversalCandidateFilterRejectReason(row ReversalReplayTimelineRow, setupLowByTime map[string]float64, opts ReversalCandidateFilterOptions) string {
	if opts.RequireOrderbookNotHostile && row.OrderbookHostile {
		return "orderbook_hostile"
	}
	if opts.MinOrderbookRatio > 0 && row.OrderbookRatio < opts.MinOrderbookRatio {
		return "orderbook_ratio_below_min"
	}
	if opts.MinBarsWithoutNewLowAfterReclaim > 0 && row.BarsWithoutNewLow < opts.MinBarsWithoutNewLowAfterReclaim {
		return "bars_without_new_low_after_reclaim_below_min"
	}
	if opts.RequireCloseAboveMiddleBB && !replayCloseAboveMiddleBB(row) {
		return "close_not_above_middle_bb"
	}
	if opts.RequireHigherLowAfterReclaim && !replayHigherLowAfterReclaim(row, setupLowByTime) {
		return "no_higher_low_after_reclaim"
	}
	return ""
}

func setupLowByTime(rows []ReversalReplayTimelineRow) map[string]float64 {
	out := map[string]float64{}
	for _, row := range rows {
		if row.Time.IsZero() || row.Low <= 0 {
			continue
		}
		if row.SetupDetectedAt != nil && row.SetupDetectedAt.Equal(row.Time) {
			out[replayEventTimeKey(row.Time)] = row.Low
		}
	}
	return out
}

func replayCloseAboveMiddleBB(row ReversalReplayTimelineRow) bool {
	return row.BBPosition == bbPositionInsideAboveMiddle || row.BBPosition == bbPositionAboveUpper
}

func replayHigherLowAfterReclaim(row ReversalReplayTimelineRow, setupLowByTime map[string]float64) bool {
	if row.SetupDetectedAt == nil || row.Low <= 0 {
		return false
	}
	setupLow := setupLowByTime[replayEventTimeKey(*row.SetupDetectedAt)]
	return setupLow > 0 && row.Low > setupLow
}

func replayEventTimeKey(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func firstFilteredCandidateTime(candidates []ReversalReplayTimelineRow) *time.Time {
	if len(candidates) == 0 {
		return nil
	}
	first := candidates[0].Time
	for _, row := range candidates[1:] {
		if row.Time.Before(first) {
			first = row.Time
		}
	}
	if first.IsZero() {
		return nil
	}
	return timePtr(first)
}

func sumIntMap(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func sortedReplayCandles(candles []ReversalReplayCandle) []ReversalReplayCandle {
	out := make([]ReversalReplayCandle, 0, len(candles))
	for _, candle := range candles {
		if candle.Time.IsZero() || candle.Close <= 0 {
			continue
		}
		if candle.High <= 0 {
			candle.High = candle.Close
		}
		if candle.Low <= 0 {
			candle.Low = candle.Close
		}
		out = append(out, candle)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func reversalCandidateRows(rows []ReversalReplayTimelineRow, setupMemoryBars int) []ReversalReplayTimelineRow {
	out := make([]ReversalReplayTimelineRow, 0, len(rows))
	for _, row := range rows {
		if row.Time.IsZero() {
			continue
		}
		if setupMemoryBars > 0 {
			if row.SetupMemoryCandidateReady {
				out = append(out, row)
			}
			continue
		}
		if row.CandidateReady {
			out = append(out, row)
		}
	}
	return out
}

func candidateForwardBase(candles []ReversalReplayCandle, row ReversalReplayTimelineRow) (int, bool) {
	for i, candle := range candles {
		if candle.Time.Equal(row.Time) {
			return i, true
		}
		if candle.Time.After(row.Time) {
			if i == 0 {
				return i, true
			}
			return i - 1, true
		}
	}
	return len(candles) - 1, len(candles) > 0
}

type candidateForwardMetrics struct {
	Time                time.Time
	EntryPrice          float64
	ForwardReturn5mBps  float64
	ForwardReturn15mBps float64
	ForwardReturn30mBps float64
	MaxFavorable30mBps  float64
	MaxAdverse30mBps    float64
	TimeToMFE           string
	TimeToMAE           string
	WindowEndTime       time.Time
}

func computeForwardMetrics(candles []ReversalReplayCandle, base int, row ReversalReplayTimelineRow) (candidateForwardMetrics, bool) {
	if base < 0 || base >= len(candles) {
		return candidateForwardMetrics{}, false
	}
	entry := row.Close
	if entry <= 0 {
		entry = candles[base].Close
	}
	if entry <= 0 {
		return candidateForwardMetrics{}, false
	}
	entryTime := row.Time
	if entryTime.IsZero() {
		entryTime = candles[base].Time
	}
	windowEnd := entryTime.Add(ReversalCandidateOutcomeWindow)
	metrics := candidateForwardMetrics{
		Time:          entryTime,
		EntryPrice:    entry,
		WindowEndTime: windowEnd,
	}
	if closePrice, ok := forwardClose(candles, base, entryTime, 5*time.Minute, windowEnd); ok {
		metrics.ForwardReturn5mBps = bpsReturn(closePrice, entry)
	}
	if closePrice, ok := forwardClose(candles, base, entryTime, 15*time.Minute, windowEnd); ok {
		metrics.ForwardReturn15mBps = bpsReturn(closePrice, entry)
	}
	if closePrice, ok := forwardClose(candles, base, entryTime, ReversalCandidateOutcomeWindow, windowEnd); ok {
		metrics.ForwardReturn30mBps = bpsReturn(closePrice, entry)
	}
	maxFav := math.Inf(-1)
	maxAdv := math.Inf(1)
	var mfeTime, maeTime time.Time
	futureSeen := false
	for i := base + 1; i < len(candles); i++ {
		candle := candles[i]
		if candle.Time.After(windowEnd) {
			break
		}
		futureSeen = true
		fav := bpsReturn(candle.High, entry)
		adv := bpsReturn(candle.Low, entry)
		if fav > maxFav {
			maxFav = fav
			mfeTime = candle.Time
		}
		if adv < maxAdv {
			maxAdv = adv
			maeTime = candle.Time
		}
	}
	if !futureSeen {
		return candidateForwardMetrics{}, false
	}
	if !math.IsInf(maxFav, -1) {
		metrics.MaxFavorable30mBps = maxFav
		metrics.TimeToMFE = mfeTime.Sub(entryTime).String()
	}
	if !math.IsInf(maxAdv, 1) {
		metrics.MaxAdverse30mBps = maxAdv
		metrics.TimeToMAE = maeTime.Sub(entryTime).String()
	}
	return metrics, true
}

func forwardClose(candles []ReversalReplayCandle, base int, entryTime time.Time, horizon time.Duration, windowEnd time.Time) (float64, bool) {
	target := entryTime.Add(horizon)
	var fallback float64
	found := false
	for i := base + 1; i < len(candles); i++ {
		candle := candles[i]
		if candle.Time.After(windowEnd) {
			break
		}
		fallback = candle.Close
		found = true
		if !candle.Time.Before(target) {
			return candle.Close, true
		}
	}
	return fallback, found
}

func simulateCandidateTPSL(candles []ReversalReplayCandle, base int, metrics candidateForwardMetrics, tpBps, slBps, feeBps, slippageBps float64) ReversalCandidateOutcome {
	outcome := ReversalCandidateOutcome{
		Time:                 metrics.Time,
		EntryPrice:           metrics.EntryPrice,
		TPBps:                tpBps,
		SLBps:                slBps,
		FeeBps:               feeBps,
		SlippageBps:          slippageBps,
		ForwardReturn5mBps:   metrics.ForwardReturn5mBps,
		ForwardReturn15mBps:  metrics.ForwardReturn15mBps,
		ForwardReturn30mBps:  metrics.ForwardReturn30mBps,
		MaxFavorable30mBps:   metrics.MaxFavorable30mBps,
		MaxAdverse30mBps:     metrics.MaxAdverse30mBps,
		TimeToMFE:            metrics.TimeToMFE,
		TimeToMAE:            metrics.TimeToMAE,
		HitOrder:             "neither",
		ForwardWindowEndTime: metrics.WindowEndTime,
	}
	tpPrice := metrics.EntryPrice * (1 + tpBps/10000)
	slPrice := metrics.EntryPrice * (1 - slBps/10000)
	for i := base + 1; i < len(candles); i++ {
		candle := candles[i]
		if candle.Time.After(metrics.WindowEndTime) {
			break
		}
		if candle.High >= tpPrice {
			outcome.WouldHitTP = true
		}
		if candle.Low <= slPrice {
			outcome.WouldHitSL = true
		}
	}
	grossReturn := metrics.ForwardReturn30mBps
	for i := base + 1; i < len(candles); i++ {
		candle := candles[i]
		if candle.Time.After(metrics.WindowEndTime) {
			break
		}
		hitTP := candle.High >= tpPrice
		hitSL := candle.Low <= slPrice
		switch {
		case hitTP && hitSL:
			outcome.HitOrder = "same_candle_sl_first"
			grossReturn = -slBps
			i = len(candles)
		case hitSL:
			outcome.HitOrder = "sl_first"
			grossReturn = -slBps
			i = len(candles)
		case hitTP:
			outcome.HitOrder = "tp_first"
			grossReturn = tpBps
			i = len(candles)
		}
	}
	outcome.NetReturnBps = grossReturn - feeBps - slippageBps
	if slBps > 0 {
		outcome.NetR = outcome.NetReturnBps / slBps
	}
	return outcome
}

func summarizeCandidateOutcomes(outcomes []ReversalCandidateOutcome) []ReversalCandidateOutcomeSummary {
	type key struct {
		tpBps       float64
		slBps       float64
		feeBps      float64
		slippageBps float64
	}
	type acc struct {
		key     key
		wins    int
		netRs   []float64
		mfeSum  float64
		maeSum  float64
		netRSum float64
		candles int
	}
	accs := map[key]*acc{}
	for _, outcome := range outcomes {
		k := key{tpBps: outcome.TPBps, slBps: outcome.SLBps, feeBps: outcome.FeeBps, slippageBps: outcome.SlippageBps}
		item := accs[k]
		if item == nil {
			item = &acc{key: k}
			accs[k] = item
		}
		item.candles++
		if outcome.NetReturnBps > 0 {
			item.wins++
		}
		item.netRSum += outcome.NetR
		item.netRs = append(item.netRs, outcome.NetR)
		item.mfeSum += outcome.MaxFavorable30mBps
		item.maeSum += outcome.MaxAdverse30mBps
	}
	summaries := make([]ReversalCandidateOutcomeSummary, 0, len(accs))
	for _, item := range accs {
		summary := ReversalCandidateOutcomeSummary{
			TPBps:                 item.key.tpBps,
			SLBps:                 item.key.slBps,
			FeeBps:                item.key.feeBps,
			SlippageBps:           item.key.slippageBps,
			CandidateCount:        item.candles,
			WinningCandidateCount: item.wins,
		}
		if item.candles > 0 {
			summary.WinRate = float64(item.wins) / float64(item.candles)
			summary.AvgNetR = item.netRSum / float64(item.candles)
			summary.MedianNetR = medianOutcomeFloat64(item.netRs)
			summary.AvgMFEBps = item.mfeSum / float64(item.candles)
			summary.AvgMAEBps = item.maeSum / float64(item.candles)
		}
		summaries = append(summaries, summary)
	}
	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].TPBps != summaries[j].TPBps {
			return summaries[i].TPBps < summaries[j].TPBps
		}
		if summaries[i].SLBps != summaries[j].SLBps {
			return summaries[i].SLBps < summaries[j].SLBps
		}
		if summaries[i].FeeBps != summaries[j].FeeBps {
			return summaries[i].FeeBps < summaries[j].FeeBps
		}
		return summaries[i].SlippageBps < summaries[j].SlippageBps
	})
	return summaries
}

func medianOutcomeFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func bpsReturn(price, entry float64) float64 {
	if entry <= 0 || price <= 0 {
		return 0
	}
	return (price/entry - 1) * 10000
}

func WriteReversalSensitivityJSONL(w io.Writer, results []ReversalSensitivityResult) error {
	enc := json.NewEncoder(w)
	for _, result := range results {
		event := map[string]any{
			"event":                                   "reversal_long_sensitivity_result",
			"min_rsi_delta":                           result.MinRSIDelta,
			"no_new_low_bars":                         result.NoNewLowBars,
			"score_threshold":                         result.ScoreThreshold,
			"min_volume_ratio":                        result.MinVolumeRatio,
			"setup_memory_bars":                       result.SetupMemoryBars,
			"invalidation_atr_mult":                   result.InvalidationATRMult,
			"require_orderbook_not_hostile":           result.RequireOrderbookNotHostile,
			"min_orderbook_ratio":                     result.MinOrderbookRatio,
			"min_bars_without_new_low_after_reclaim":  result.MinBarsWithoutNewLowAfterReclaim,
			"require_close_above_middle_bb":           result.RequireCloseAboveMiddleBB,
			"require_higher_low_after_reclaim":        result.RequireHigherLowAfterReclaim,
			"unfiltered_candidate_ready_count":        result.UnfilteredCandidateReadyCount,
			"candidate_ready_count":                   result.CandidateReadyCount,
			"offline_filter_rejected_candidate_count": result.OfflineFilterRejectedCandidateCount,
			"offline_filter_reject_reasons":           result.OfflineFilterRejectReasons,
			"setup_detected_count":                    result.SetupDetectedCount,
			"setup_invalidated_count":                 result.SetupInvalidatedCount,
			"first_candidate_time":                    result.FirstCandidateTime,
			"max_score":                               result.MaxScore,
			"max_score_time":                          result.MaxScoreTime,
			"emitted_signal_dry_run_count":            result.EmittedSignalDryRunCount,
			"runtime_pass_count":                      result.RuntimePassCount,
			"runtime_block_count":                     result.RuntimeBlockCount,
			"main_blocker":                            result.MainBlocker,
			"orders_sent":                             result.OrdersSent,
			"blockers":                                result.Blockers,
			"candidate_outcomes":                      result.CandidateOutcomes,
			"outcome_summaries":                       result.OutcomeSummaries,
		}
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

func WriteReversalSensitivityMarkdown(w io.Writer, results []ReversalSensitivityResult) error {
	fmt.Fprintln(w, "# Reversal Long Sensitivity")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "This report is offline-only. It does not modify production defaults and keeps `orders_sent=0`.")
	fmt.Fprintln(w)
	first := firstSensitivityCandidate(results)
	if first == nil {
		fmt.Fprintln(w, "No candidate appeared for the tested parameter grid.")
	} else {
		fmt.Fprintf(w, "First candidate in the tested grid: min_rsi_delta=%.3f no_new_low_bars=%d score_threshold=%.3f min_volume_ratio=%.3f setup_memory_bars=%d invalidation_atr_mult=%.3f first_candidate_time=%s\n",
			first.MinRSIDelta, first.NoNewLowBars, first.ScoreThreshold, first.MinVolumeRatio, first.SetupMemoryBars, first.InvalidationATRMult, first.FirstCandidateTime.Format(time.RFC3339))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | raw_candidates | filtered_candidates | rejected_candidates | setup_detected | setup_invalidated | first_candidate_time | max_score | dry_run_signals | runtime_pass | runtime_block | main_blocker | orders_sent |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | ---: | ---: | --- | ---: |")
	for _, r := range results {
		firstTime := ""
		if r.FirstCandidateTime != nil {
			firstTime = r.FirstCandidateTime.Format(time.RFC3339)
		}
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %d | %d | %d | %d | %d | %s | %.2f | %d | %d | %d | %s | %d |\n",
			r.MinRSIDelta, r.NoNewLowBars, r.ScoreThreshold, r.MinVolumeRatio, r.SetupMemoryBars, r.InvalidationATRMult,
			r.RequireOrderbookNotHostile, r.MinOrderbookRatio, r.MinBarsWithoutNewLowAfterReclaim, r.RequireCloseAboveMiddleBB, r.RequireHigherLowAfterReclaim,
			r.UnfilteredCandidateReadyCount, r.CandidateReadyCount, r.OfflineFilterRejectedCandidateCount, r.SetupDetectedCount, r.SetupInvalidatedCount, firstTime, r.MaxScore,
			r.EmittedSignalDryRunCount, r.RuntimePassCount, r.RuntimeBlockCount, r.MainBlocker, r.OrdersSent)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Candidate Outcome Summary")
	fmt.Fprintln(w)
	writeReversalOutcomeSummaryTable(w, results)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Offline Filter Rejected Candidate Reasons")
	fmt.Fprintln(w)
	writeReversalFilterRejectReasonTable(w, results)
	return nil
}

func normalizeSensitivityOptions(spec ReversalSensitivityOptions, cfg config.ReversalLongConfig) ReversalSensitivityOptions {
	if len(spec.MinRSIDeltaList) == 0 {
		spec.MinRSIDeltaList = []float64{cfg.MinRSIDelta}
	}
	if len(spec.NoNewLowBarsList) == 0 {
		spec.NoNewLowBarsList = []int{cfg.NoNewLowBars}
	}
	if len(spec.ScoreThresholdList) == 0 {
		spec.ScoreThresholdList = []float64{cfg.ScoreThreshold}
	}
	if len(spec.MinVolumeRatioList) == 0 {
		spec.MinVolumeRatioList = []float64{cfg.MinVolumeRatio}
	}
	if len(spec.SetupMemoryBarsList) == 0 {
		spec.SetupMemoryBarsList = []int{0}
	}
	if len(spec.InvalidationATRMultList) == 0 {
		spec.InvalidationATRMultList = []float64{0}
	}
	if len(spec.RequireOrderbookNotHostileList) == 0 {
		spec.RequireOrderbookNotHostileList = []bool{false}
	}
	if len(spec.MinOrderbookRatioList) == 0 {
		spec.MinOrderbookRatioList = []float64{0}
	}
	if len(spec.MinBarsWithoutNewLowAfterReclaimList) == 0 {
		spec.MinBarsWithoutNewLowAfterReclaimList = []int{0}
	}
	if len(spec.RequireCloseAboveMiddleBBList) == 0 {
		spec.RequireCloseAboveMiddleBBList = []bool{false}
	}
	if len(spec.RequireHigherLowAfterReclaimList) == 0 {
		spec.RequireHigherLowAfterReclaimList = []bool{false}
	}
	if len(spec.OutcomeTPBpsList) == 0 {
		spec.OutcomeTPBpsList = []float64{50}
	}
	if len(spec.OutcomeSLBpsList) == 0 {
		spec.OutcomeSLBpsList = []float64{30}
	}
	sort.Float64s(spec.MinRSIDeltaList)
	sort.Ints(spec.NoNewLowBarsList)
	sort.Float64s(spec.ScoreThresholdList)
	sort.Float64s(spec.MinVolumeRatioList)
	sort.Ints(spec.SetupMemoryBarsList)
	sort.Float64s(spec.InvalidationATRMultList)
	sortBoolList(spec.RequireOrderbookNotHostileList)
	sort.Float64s(spec.MinOrderbookRatioList)
	sort.Ints(spec.MinBarsWithoutNewLowAfterReclaimList)
	sortBoolList(spec.RequireCloseAboveMiddleBBList)
	sortBoolList(spec.RequireHigherLowAfterReclaimList)
	sort.Float64s(spec.OutcomeTPBpsList)
	sort.Float64s(spec.OutcomeSLBpsList)
	return spec
}

func sortBoolList(values []bool) {
	sort.Slice(values, func(i, j int) bool {
		return !values[i] && values[j]
	})
}

func writeReversalOutcomeSummaryTable(w io.Writer, results []ReversalSensitivityResult) {
	type row struct {
		result  ReversalSensitivityResult
		summary ReversalCandidateOutcomeSummary
	}
	var rows []row
	for _, result := range results {
		for _, summary := range result.OutcomeSummaries {
			if summary.CandidateCount == 0 {
				continue
			}
			rows = append(rows, row{result: result, summary: summary})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "No candidate outcomes were available for the tested parameter grid.")
		return
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].summary.AvgNetR != rows[j].summary.AvgNetR {
			return rows[i].summary.AvgNetR > rows[j].summary.AvgNetR
		}
		return rows[i].summary.CandidateCount > rows[j].summary.CandidateCount
	})
	if len(rows) > 20 {
		rows = rows[:20]
	}
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | tp_bps | sl_bps | fee_bps | slippage_bps | candidate_count | win_rate | avg_net_r | median_net_r | avg_mfe_bps | avg_mae_bps |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, item := range rows {
		r := item.result
		s := item.summary
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %.2f | %.2f | %.2f | %.2f | %d | %.4f | %.4f | %.4f | %.2f | %.2f |\n",
			r.MinRSIDelta, r.NoNewLowBars, r.ScoreThreshold, r.MinVolumeRatio, r.SetupMemoryBars, r.InvalidationATRMult,
			r.RequireOrderbookNotHostile, r.MinOrderbookRatio, r.MinBarsWithoutNewLowAfterReclaim, r.RequireCloseAboveMiddleBB, r.RequireHigherLowAfterReclaim,
			s.TPBps, s.SLBps, s.FeeBps, s.SlippageBps, s.CandidateCount, s.WinRate, s.AvgNetR, s.MedianNetR, s.AvgMFEBps, s.AvgMAEBps)
	}
}

func writeReversalFilterRejectReasonTable(w io.Writer, results []ReversalSensitivityResult) {
	counts := map[string]int{}
	for _, result := range results {
		for reason, count := range result.OfflineFilterRejectReasons {
			counts[reason] += count
		}
	}
	if len(counts) == 0 {
		fmt.Fprintln(w, "No offline filter rejections.")
		return
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if counts[reasons[i]] != counts[reasons[j]] {
			return counts[reasons[i]] > counts[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	fmt.Fprintln(w, "| reason | count |")
	fmt.Fprintln(w, "| --- | ---: |")
	for _, reason := range reasons {
		fmt.Fprintf(w, "| %s | %d |\n", reason, counts[reason])
	}
}

func classifyReplayRow(row ReversalReplayTimelineRow, cfg config.ReversalLongConfig) ([]string, []string, []string) {
	var passed, failed, missing []string
	addScore := func(name string, score float64) {
		if score > 0 {
			passed = append(passed, name)
		} else {
			missing = append(missing, name)
		}
	}
	addScore("rsi_oversold", row.ScoreRSIOversold)
	addScore("volume_spike", row.ScoreVolumeSpike)
	addScore("bb_reclaim", row.ScoreBBReclaim)
	addScore("rsi_recovery", row.ScoreRSIRecovery)
	addScore("macd_recovery", row.ScoreMACDRecovery)
	addScore("no_new_low", row.ScoreNoNewLow)
	if row.ScoreRSIRecovery == 0 {
		failed = append(failed, fmt.Sprintf("rsi_recovery: rsi_delta=%.3f < min=%.3f", row.RSIDelta, cfg.MinRSIDelta))
	}
	if row.ScoreVolumeSpike == 0 {
		failed = append(failed, fmt.Sprintf("volume_spike: volume_ratio=%.3f < min=%.3f", row.VolumeRatio, cfg.MinVolumeRatio))
	}
	if row.ScoreNoNewLow == 0 {
		failed = append(failed, fmt.Sprintf("no_new_low: bars_without_new_low=%d < required=%d", row.BarsWithoutNewLow, cfg.NoNewLowBars))
	}
	if row.Score < cfg.ScoreThreshold {
		failed = append(failed, fmt.Sprintf("score_threshold: score=%.2f < threshold=%.2f", row.Score, cfg.ScoreThreshold))
	} else {
		passed = append(passed, "score_threshold")
	}
	if row.HTFAllowed {
		if row.HTFOverrideAllowed {
			passed = append(passed, "htf_override_allowed")
		} else {
			passed = append(passed, "htf_allowed")
		}
	} else {
		failed = append(failed, "htf_blocked")
	}
	if !row.OrderbookAvailable {
		failed = append(failed, "orderbook: unavailable")
	} else if row.OrderbookAllowed {
		passed = append(passed, "orderbook_allowed")
	} else {
		failed = append(failed, "orderbook_hostile")
	}
	return passed, failed, missing
}

func firstCandidateTime(result ReversalReplayResult) (time.Time, bool) {
	for _, row := range ReversalReplayTimeline(result) {
		if row.CandidateReady {
			return row.Time, true
		}
	}
	return time.Time{}, false
}

func firstSensitivityCandidate(results []ReversalSensitivityResult) *ReversalSensitivityResult {
	for i := range results {
		if results[i].CandidateReadyCount > 0 && results[i].FirstCandidateTime != nil {
			return &results[i]
		}
	}
	return nil
}

func mainBlocker(blockers map[string]int) string {
	if len(blockers) == 0 {
		return ""
	}
	keys := make([]string, 0, len(blockers))
	for key := range blockers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if blockers[keys[i]] == blockers[keys[j]] {
			return keys[i] < keys[j]
		}
		return blockers[keys[i]] > blockers[keys[j]]
	})
	return keys[0]
}

func formatBlockers(blockers map[string]int) string {
	if len(blockers) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(blockers))
	for key := range blockers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, blockers[key]))
	}
	return strings.Join(parts, ", ")
}

func formatOptionalTime(ts *time.Time) string {
	if ts == nil || ts.IsZero() {
		return ""
	}
	return ts.Format(time.RFC3339)
}

func timePtr(ts time.Time) *time.Time {
	if ts.IsZero() {
		return nil
	}
	return &ts
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func intValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func timeValue(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		ts, _ := time.Parse(time.RFC3339Nano, v)
		return ts
	default:
		return time.Time{}
	}
}
