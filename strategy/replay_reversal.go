package strategy

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type ReversalReplayCandle struct {
	Time        time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	BestBid     float64
	BestAsk     float64
	BestBidSize float64
	BestAskSize float64
	Imbalance   float64
	SpreadBps   float64
	Ratio       float64
}

type ReversalReplayFrame struct {
	Time             time.Time
	Snapshot         *models.IndicatorSnapshot
	Candle           *ReversalReplayCandle
	Orderbook        ReversalOrderbookInput
	RuntimeOrderbook *ReversalOrderbookInput
}

type ReversalReplayOptions struct {
	Symbol                      string
	From                        time.Time
	To                          time.Time
	ReversalEnabled             bool
	ReversalTradeEnabled        bool
	OrdersEnabled               bool
	Config                      *config.Config
	Balance                     float64
	RequireSoftOrderbookRuntime *bool
}

type ReversalReplaySummary struct {
	Symbol                       string         `json:"symbol"`
	From                         time.Time      `json:"from"`
	To                           time.Time      `json:"to"`
	CandlesLoaded                int            `json:"candles_loaded"`
	WarmupCandles                int            `json:"warmup_candles"`
	TargetCandles                int            `json:"target_candles"`
	CandlesProcessed             int            `json:"candles_processed"`
	OrdinaryLongCandidates       int            `json:"ordinary_long_candidates"`
	OrdinaryShortCandidates      int            `json:"ordinary_short_candidates"`
	OrdinarySignalsEmitted       int            `json:"ordinary_signals_emitted"`
	ReversalEvaluations          int            `json:"reversal_evaluations"`
	ReversalCandidateReadyCount  int            `json:"reversal_candidate_ready_count"`
	ReversalPipelineAllowedCount int            `json:"reversal_pipeline_allowed_count"`
	ReversalSignalsEmittedDryRun int            `json:"reversal_signals_emitted_dry_run"`
	RuntimeReversalPassCount     int            `json:"runtime_reversal_pass_count"`
	RuntimeReversalBlockCount    int            `json:"runtime_reversal_block_count"`
	OrdersSent                   int            `json:"orders_sent"`
	Blockers                     map[string]int `json:"blockers,omitempty"`
	BBReclaimCount               int            `json:"bb_reclaim_count"`
	RSIRecoveryCount             int            `json:"rsi_recovery_count"`
	MACDHistRecoveryCount        int            `json:"macd_hist_recovery_count"`
	NoNewLowCount                int            `json:"no_new_low_count"`
	HTFOverrideAllowedCount      int            `json:"htf_override_allowed_count"`
	SoftOrderbookAllowedCount    int            `json:"soft_orderbook_allowed_count"`
	OrderbookDiagnosticCount     int            `json:"orderbook_diagnostic_count"`
	OrderbookAvailableCount      int            `json:"orderbook_available_count"`
}

type ReversalReplayResult struct {
	Summary ReversalReplaySummary `json:"summary"`
	Events  []map[string]any      `json:"events"`
}

type replayLogger struct{}

func (replayLogger) Debug(string, ...interface{})    {}
func (replayLogger) Info(string, ...interface{})     {}
func (replayLogger) Warning(string, ...interface{})  {}
func (replayLogger) Error(string, ...interface{})    {}
func (replayLogger) Fatal(string, ...interface{})    {}
func (replayLogger) Sync() error                     { return nil }
func (replayLogger) ChangeLogLevel(logging.LogLevel) {}

func LoadReversalReplayCandlesCSV(path string) ([]ReversalReplayCandle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadReversalReplayCandlesCSV(f)
}

func ReadReversalReplayCandlesCSV(r io.Reader) ([]ReversalReplayCandle, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("csv must include a header and at least one data row")
	}
	header := make(map[string]int, len(rows[0]))
	for i, col := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(col))] = i
	}
	required := []string{"time", "open", "high", "low", "close", "volume"}
	for _, col := range required {
		if _, ok := header[col]; !ok {
			return nil, fmt.Errorf("csv missing required column %q", col)
		}
	}

	var out []ReversalReplayCandle
	for rowIdx, row := range rows[1:] {
		c, err := parseReplayCandle(row, header)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowIdx+2, err)
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func parseReplayCandle(row []string, header map[string]int) (ReversalReplayCandle, error) {
	ts, err := parseReplayTime(valueAt(row, header, "time"))
	if err != nil {
		return ReversalReplayCandle{}, err
	}
	floatCol := func(name string) (float64, error) {
		return strconv.ParseFloat(valueAt(row, header, name), 64)
	}
	open, err := floatCol("open")
	if err != nil {
		return ReversalReplayCandle{}, fmt.Errorf("open: %w", err)
	}
	high, err := floatCol("high")
	if err != nil {
		return ReversalReplayCandle{}, fmt.Errorf("high: %w", err)
	}
	low, err := floatCol("low")
	if err != nil {
		return ReversalReplayCandle{}, fmt.Errorf("low: %w", err)
	}
	closePrice, err := floatCol("close")
	if err != nil {
		return ReversalReplayCandle{}, fmt.Errorf("close: %w", err)
	}
	volume, err := floatCol("volume")
	if err != nil {
		return ReversalReplayCandle{}, fmt.Errorf("volume: %w", err)
	}
	c := ReversalReplayCandle{Time: ts, Open: open, High: high, Low: low, Close: closePrice, Volume: volume}
	c.BestBid = optionalFloat(row, header, "best_bid")
	c.BestAsk = optionalFloat(row, header, "best_ask")
	c.BestBidSize = optionalFloat(row, header, "best_bid_size")
	c.BestAskSize = optionalFloat(row, header, "best_ask_size")
	c.Imbalance = optionalFloat(row, header, "bid_ask_imbalance")
	c.SpreadBps = optionalFloat(row, header, "spread_bps")
	c.Ratio = optionalFloat(row, header, "orderbook_ratio")
	return c, nil
}

func parseReplayTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("time must be RFC3339 or unix milliseconds: %w", err)
	}
	if n > 10_000_000_000 {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Unix(n, 0).UTC(), nil
}

func valueAt(row []string, header map[string]int, name string) string {
	idx, ok := header[name]
	if !ok || idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func optionalFloat(row []string, header map[string]int, name string) float64 {
	raw := valueAt(row, header, name)
	if raw == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(raw, 64)
	return v
}

func RunReversalReplayCandles(candles []ReversalReplayCandle, opts ReversalReplayOptions) (ReversalReplayResult, error) {
	frames := make([]ReversalReplayFrame, 0, len(candles))
	for i := range candles {
		c := candles[i]
		frames = append(frames, ReversalReplayFrame{
			Time:      c.Time,
			Candle:    &c,
			Orderbook: replayOrderbookFromCandle(c),
		})
	}
	return RunReversalReplayFrames(frames, opts)
}

func RunReversalReplayFrames(frames []ReversalReplayFrame, opts ReversalReplayOptions) (ReversalReplayResult, error) {
	cfg := config.LoadConfig()
	if opts.Config != nil {
		cfgCopy := *opts.Config
		cfg = &cfgCopy
	}
	if opts.Symbol != "" {
		cfg.Symbol = opts.Symbol
	}
	cfg.ReversalLong.Enabled = opts.ReversalEnabled
	cfg.ReversalLong.TradeEnabled = opts.ReversalTradeEnabled
	if opts.RequireSoftOrderbookRuntime != nil {
		cfg.ReversalLong.RequireSoftOrderbookAtRuntime = *opts.RequireSoftOrderbookRuntime
	}
	cfg.SLSetDelaySec = 0

	state := &models.State{
		SigChan: make(chan models.Signal, len(frames)+16),
	}
	state.Instr = models.InstrumentInfo{MinQty: 0.001, QtyStep: 0.001, TickSize: 0.1}
	logger := replayLogger{}
	tr := NewTrader(api.NewRESTClient(cfg, logger), cfg, state, logger)
	tr.ReversalLongDetector = NewReversalLongDetector(cfg.ReversalLong)

	result := ReversalReplayResult{
		Summary: ReversalReplaySummary{
			Symbol:        cfg.Symbol,
			From:          opts.From,
			To:            opts.To,
			CandlesLoaded: len(frames),
			OrdersSent:    0,
			Blockers:      make(map[string]int),
		},
	}
	signalStrength := map[string]int{"LONG": 0, "SHORT": 0}
	balance := opts.Balance
	if balance <= 0 {
		balance = 100000
	}

	sort.Slice(frames, func(i, j int) bool { return frameTime(frames[i]).Before(frameTime(frames[j])) })
	for _, frame := range frames {
		now := frameTime(frame)
		if now.IsZero() {
			continue
		}
		tr.Now = func() time.Time { return now }
		applyReplayOrderbookToState(state, frame)

		var snap models.IndicatorSnapshot
		if frame.Snapshot != nil {
			snap = *frame.Snapshot
			if snap.Time.IsZero() {
				snap.Time = now
			}
		} else if frame.Candle != nil {
			snapshot := state.AppendClosedCandle(frame.Candle.Close, frame.Candle.High, frame.Candle.Low, frame.Candle.Volume, cfg.SmaLen*100)
			if len(snapshot.Closes) == 0 {
				continue
			}
			snap = tr.buildIndicatorSnapshot(snapshot)
		} else {
			continue
		}
		if !withinReplayWindow(now, opts.From, opts.To) {
			if !opts.From.IsZero() && now.Before(opts.From) {
				result.Summary.WarmupCandles++
			}
			continue
		}

		result.Summary.CandlesProcessed++
		result.Summary.TargetCandles++
		addReplayOrderbookDiagnostic(&result, cfg.Symbol, now, frame.Orderbook)
		reversalEval, hasReversalEval := ReversalLongEvaluation{}, false
		if cfg.ReversalLong.Enabled {
			reversalEval = tr.ReversalLongDetector.EvaluateWithSafety(cfg.Symbol, snap, now, ReversalHTFInput{
				Available:                 snap.HTFBias != "",
				Bias:                      snap.HTFBias,
				ExtremeDowntrendAvailable: false,
			}, frame.Orderbook)
			hasReversalEval = true
			addReplayEvaluation(&result, reversalEval, snap, cfg.Symbol)
		}

		scored := tr.scoreSignalWithRegime(snap, uint64(result.Summary.CandlesProcessed), state.GetOhlcvSnapshot().Closes, state.GetMarketRegime())
		if scored.LongScore > 0 {
			result.Summary.OrdinaryLongCandidates++
		}
		if scored.ShortScore > 0 {
			result.Summary.OrdinaryShortCandidates++
		}
		filtered := tr.FilterSignal(scored, replayOrderbookEvaluator(frame.Orderbook))
		filtered, decision := tr.applyReversalLongPipelineBridge(filtered, scored, reversalEval, hasReversalEval)
		if decision.Active {
			addReplayPipelineDecision(&result, decision, cfg.Symbol, now)
		}

		emitted := tr.EmitSignal(filtered)
		if emitted {
			sig := <-state.SigChan
			if sig.Mode == models.ModeReversalLong {
				result.Summary.ReversalSignalsEmittedDryRun++
				runtimeOrderbook := frame.Orderbook
				if frame.RuntimeOrderbook != nil {
					runtimeOrderbook = *frame.RuntimeOrderbook
				}
				allowed := addReplayRuntime(&result, tr, sig, runtimeOrderbook, signalStrength, balance, opts.OrdersEnabled, now)
				if allowed {
					result.Summary.RuntimeReversalPassCount++
				} else {
					result.Summary.RuntimeReversalBlockCount++
				}
			} else {
				result.Summary.OrdinarySignalsEmitted++
			}
		}
	}
	return result, nil
}

func frameTime(frame ReversalReplayFrame) time.Time {
	if !frame.Time.IsZero() {
		return frame.Time
	}
	if frame.Snapshot != nil {
		return frame.Snapshot.Time
	}
	if frame.Candle != nil {
		return frame.Candle.Time
	}
	return time.Time{}
}

func withinReplayWindow(ts, from, to time.Time) bool {
	if !from.IsZero() && ts.Before(from) {
		return false
	}
	if !to.IsZero() && ts.After(to) {
		return false
	}
	return true
}

func replayOrderbookFromCandle(c ReversalReplayCandle) ReversalOrderbookInput {
	if c.Ratio > 0 || c.Imbalance != 0 || c.SpreadBps > 0 {
		return ReversalOrderbookInput{
			Available:   true,
			BestBid:     c.BestBid,
			BestAsk:     c.BestAsk,
			SpreadBps:   c.SpreadBps,
			Ratio:       c.Ratio,
			Imbalance:   c.Imbalance,
			BestBidSize: c.BestBidSize,
			BestAskSize: c.BestAskSize,
			DepthLevels: 1,
			AskWallRatio: func() float64 {
				if c.Ratio > 0 {
					return 1 / c.Ratio
				}
				return 0
			}(),
		}
	}
	if c.BestBid <= 0 || c.BestAsk <= 0 || c.BestBidSize <= 0 || c.BestAskSize <= 0 {
		return ReversalOrderbookInput{UnavailableReason: ReversalReasonOrderbookNoData}
	}
	input := ReversalOrderbookInput{
		Available:   true,
		BestBid:     c.BestBid,
		BestAsk:     c.BestAsk,
		Spread:      c.BestAsk - c.BestBid,
		BestBidSize: c.BestBidSize,
		BestAskSize: c.BestAskSize,
		DepthLevels: 1,
	}
	mid := (c.BestBid + c.BestAsk) / 2
	if mid > 0 {
		input.SpreadBps = input.Spread / mid * 10000
	}
	if c.BestAskSize > 0 {
		input.Ratio = c.BestBidSize / c.BestAskSize
	}
	if c.BestBidSize+c.BestAskSize > 0 {
		input.Imbalance = (c.BestBidSize - c.BestAskSize) / (c.BestBidSize + c.BestAskSize)
	}
	if c.BestBidSize > 0 {
		input.AskWallRatio = c.BestAskSize / c.BestBidSize
	}
	input.BidStabilityRatio = 1
	input.BidStabilityAvailable = true
	return input
}

func addReplayOrderbookDiagnostic(result *ReversalReplayResult, symbol string, ts time.Time, input ReversalOrderbookInput) {
	result.Summary.OrderbookDiagnosticCount++
	if input.Available {
		result.Summary.OrderbookAvailableCount++
	}
	snap := ReversalOrderbookDiagnosticFromInput(symbol, ts, input, orderbookDiagnosticReplay)
	result.Events = append(result.Events, snap.Event())
}

func applyReplayOrderbookToState(state *models.State, frame ReversalReplayFrame) {
	state.ObLock.Lock()
	defer state.ObLock.Unlock()
	state.BidsMap = nil
	state.AsksMap = nil
	if frame.Candle == nil || frame.Candle.BestBid <= 0 || frame.Candle.BestAsk <= 0 || frame.Candle.BestBidSize <= 0 || frame.Candle.BestAskSize <= 0 {
		return
	}
	state.BidsMap = map[string]float64{fmt.Sprintf("%.8f", frame.Candle.BestBid): frame.Candle.BestBidSize}
	state.AsksMap = map[string]float64{fmt.Sprintf("%.8f", frame.Candle.BestAsk): frame.Candle.BestAskSize}
}

func replayOrderbookEvaluator(input ReversalOrderbookInput) orderbookEvaluator {
	return func(side string, _ float64) models.OrderbookResult {
		if !input.Available {
			return models.OrderbookResult{Evaluated: true, Passed: false, Side: side, Reason: ReversalReasonOrderbookNoData}
		}
		return models.OrderbookResult{
			Evaluated: true,
			Passed:    true,
			Ratio:     input.Ratio,
			Imbalance: input.Imbalance,
			Side:      side,
			Reason:    "replay_orderbook_available",
		}
	}
}

func addReplayEvaluation(result *ReversalReplayResult, evaluation ReversalLongEvaluation, snap models.IndicatorSnapshot, symbol string) {
	result.Summary.ReversalEvaluations++
	if evaluation.CandidateReady {
		result.Summary.ReversalCandidateReadyCount++
	}
	if evaluation.RejectReason != "" && evaluation.RejectReason != ReversalReasonCandidateReady {
		result.Summary.Blockers[evaluation.RejectReason]++
	}
	if boolFromDebug(evaluation.Debug, "bb_reclaim_available") && boolFromDebug(evaluation.Debug, "returned_inside_bb") {
		result.Summary.BBReclaimCount++
	}
	if boolFromDebug(evaluation.Debug, "rsi_delta_available") && floatFromDebug(evaluation.Debug, "rsi_delta") > 0 {
		result.Summary.RSIRecoveryCount++
	}
	if boolFromDebug(evaluation.Debug, "macd_hist_delta_available") && floatFromDebug(evaluation.Debug, "macd_hist_delta") >= 0 {
		result.Summary.MACDHistRecoveryCount++
	}
	if intFromDebug(evaluation.Debug, "bars_without_new_low") > 0 {
		result.Summary.NoNewLowCount++
	}
	if boolFromDebug(evaluation.Debug, "htf_override_allowed") {
		result.Summary.HTFOverrideAllowedCount++
	}
	if boolFromDebug(evaluation.Debug, "orderbook_allowed") {
		result.Summary.SoftOrderbookAllowedCount++
	}
	result.Events = append(result.Events, map[string]any{
		"event":                        "reversal_long_evaluation",
		"time":                         evaluation.Time,
		"symbol":                       symbol,
		"mode":                         evaluation.Mode,
		"state":                        evaluation.State,
		"candidate_ready":              evaluation.CandidateReady,
		"score":                        evaluation.Score,
		"score_rsi_oversold":           floatFromDebug(evaluation.Debug, "score_rsi_oversold"),
		"score_volume_spike":           floatFromDebug(evaluation.Debug, "score_volume_spike"),
		"score_bb_reclaim":             floatFromDebug(evaluation.Debug, "score_bb_reclaim"),
		"score_rsi_recovery":           floatFromDebug(evaluation.Debug, "score_rsi_recovery"),
		"score_macd_recovery":          floatFromDebug(evaluation.Debug, "score_macd_recovery"),
		"score_no_new_low":             floatFromDebug(evaluation.Debug, "score_no_new_low"),
		"reject_reason":                evaluation.RejectReason,
		"close":                        snap.Close,
		"low":                          snap.Low,
		"atr":                          snap.ATR,
		"volume":                       snap.Volume,
		"rsi":                          snap.RSI,
		"rsi_delta":                    snap.RSIDelta,
		"rsi_delta_available":          snap.RSIDeltaAvailable,
		"macd_hist":                    snap.MACDHist,
		"macd_hist_delta":              snap.MACDHistDelta,
		"macd_hist_delta_available":    snap.MACDHistDeltaAvailable,
		"volume_ratio":                 snap.VolumeRatio,
		"min_volume_ratio":             floatFromDebug(evaluation.Debug, "min_volume_ratio"),
		"volume_ratio_available":       boolFromDebug(evaluation.Debug, "volume_ratio_available"),
		"volume_spike_passed":          boolFromDebug(evaluation.Debug, "volume_spike_passed"),
		"bb_position":                  snap.BBPosition,
		"bb_reclaim_available":         snap.BBReclaimAvailable,
		"was_below_lower_bb":           snap.WasBelowLowerBB,
		"returned_inside_bb":           snap.ReturnedInsideBB,
		"htf_bias":                     snap.HTFBias,
		"htf_allowed":                  boolFromDebug(evaluation.Debug, "htf_allowed"),
		"htf_override_allowed":         boolFromDebug(evaluation.Debug, "htf_override_allowed"),
		"orderbook_available":          boolFromDebug(evaluation.Debug, "orderbook_available"),
		"orderbook_allowed":            boolFromDebug(evaluation.Debug, "orderbook_allowed"),
		"orderbook_hostile":            boolFromDebug(evaluation.Debug, "orderbook_hostile"),
		"orderbook_reject_reason":      stringFromDebug(evaluation.Debug, "orderbook_reject_reason"),
		"orderbook_unavailable_reason": stringFromDebug(evaluation.Debug, "orderbook_unavailable_reason"),
		"orderbook_ratio":              floatFromDebug(evaluation.Debug, "orderbook_ratio"),
		"orderbook_imbalance":          floatFromDebug(evaluation.Debug, "orderbook_imbalance"),
		"best_bid":                     floatFromDebug(evaluation.Debug, "best_bid"),
		"best_ask":                     floatFromDebug(evaluation.Debug, "best_ask"),
		"best_bid_size":                floatFromDebug(evaluation.Debug, "best_bid_size"),
		"best_ask_size":                floatFromDebug(evaluation.Debug, "best_ask_size"),
		"depth_levels":                 intFromDebug(evaluation.Debug, "depth_levels"),
		"spread_bps":                   floatFromDebug(evaluation.Debug, "spread_bps"),
		"bars_without_new_low":         intFromDebug(evaluation.Debug, "bars_without_new_low"),
		"no_new_low_bars":              intFromDebug(evaluation.Debug, "no_new_low_bars"),
		"dry_run":                      true,
		"orders_enabled":               false,
		"order_sent":                   false,
	})
}

func addReplayPipelineDecision(result *ReversalReplayResult, decision reversalLongPipelineDecision, symbol string, ts time.Time) {
	if decision.PipelineAllowed {
		result.Summary.ReversalPipelineAllowedCount++
	}
	result.Events = append(result.Events, map[string]any{
		"event":                "reversal_long_pipeline_decision",
		"time":                 ts,
		"symbol":               symbol,
		"mode":                 models.ModeReversalLong,
		"candidate_ready":      decision.CandidateReady,
		"score":                decision.CandidateScore,
		"reject_reason":        decision.RejectReason,
		"pipeline_allowed":     decision.PipelineAllowed,
		"pipeline_selected":    decision.PipelineSelected,
		"pipeline_skip_reason": decision.PipelineSkipReason,
		"dry_run":              true,
		"orders_enabled":       false,
		"order_sent":           false,
	})
}

func addReplayRuntime(result *ReversalReplayResult, tr *Trader, sig models.Signal, input ReversalOrderbookInput, signalStrength map[string]int, balance float64, ordersEnabled bool, ts time.Time) bool {
	dir := tr.PositionManager.NormalizeSide(sig.Direction)
	risk := tr.reversalLongRuntimeRisk(sig, sig.ClosePrice)
	required := tr.runtimeStrengthThreshold(sig)
	obResult := replayRuntimeOrderbookResult(tr.Config.ReversalLong, input)
	allowed := true
	reason := ReversalReasonRiskProfileApplied
	if !tr.Config.ReversalLong.TradeEnabled {
		allowed = false
		reason = ReversalReasonTradeDisabled
	} else if tr.Config.ReversalLong.RequireStop && !risk.StopValid {
		allowed = false
		reason = risk.RejectReason
	} else if !obResult.Passed {
		allowed = false
		reason = obResult.Reason
	}
	if allowed {
		signalStrength[dir] += sig.Strength
		if signalStrength[dir] < required {
			allowed = false
			reason = filterMinimumStrength
		}
	}
	size, _ := tr.calculatePositionSize(sig.ClosePrice, balance, sig)
	result.Events = append(result.Events, map[string]any{
		"event":                       "reversal_long_runtime_gate",
		"time":                        ts,
		"symbol":                      tr.Config.Symbol,
		"mode":                        sig.Mode,
		"strength":                    signalStrength[dir],
		"required_strength":           required,
		"passed_strength":             signalStrength[dir] >= required,
		"runtime_allowed":             allowed,
		"runtime_reject_reason":       reason,
		"orderbook_runtime_required":  tr.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
		"orderbook_runtime_available": input.Available,
		"orderbook_runtime_allowed":   obResult.Passed,
		"local_low":                   risk.LocalLow,
		"stop_price":                  risk.StopPrice,
		"stop_valid":                  risk.StopValid,
		"position_size_multiplier":    size.Multiplier,
		"final_size":                  size.FinalQty,
		"dry_run":                     true,
		"orders_enabled":              ordersEnabled,
		"order_sent":                  false,
	})
	result.Events = append(result.Events, map[string]any{
		"event":                    "reversal_long_risk",
		"time":                     ts,
		"symbol":                   tr.Config.Symbol,
		"mode":                     sig.Mode,
		"normal_size":              size.NormalQty,
		"position_size_multiplier": size.Multiplier,
		"final_size":               size.FinalQty,
		"local_low":                risk.LocalLow,
		"stop_price":               risk.StopPrice,
		"stop_valid":               risk.StopValid,
		"dry_run":                  true,
		"orders_enabled":           ordersEnabled,
		"order_sent":               false,
	})
	if risk.StopValid {
		result.Events = append(result.Events, map[string]any{
			"event":                    "reversal_long_tpsl",
			"time":                     ts,
			"symbol":                   tr.Config.Symbol,
			"mode":                     sig.Mode,
			"reversal_stop_source":     "local_low",
			"local_low":                risk.LocalLow,
			"stop_price":               risk.StopPrice,
			"stop_valid":               risk.StopValid,
			"position_size_multiplier": size.Multiplier,
			"final_size":               size.FinalQty,
			"dry_run":                  true,
			"orders_enabled":           ordersEnabled,
			"order_sent":               false,
		})
	}
	if allowed {
		result.Events = append(result.Events, map[string]any{
			"event":          "dry_run_order_blocked",
			"time":           ts,
			"symbol":         tr.Config.Symbol,
			"mode":           sig.Mode,
			"side":           sig.Direction,
			"reason":         "orders_disabled",
			"dry_run":        true,
			"orders_enabled": ordersEnabled,
			"order_sent":     false,
		})
	}
	return allowed
}

func replayRuntimeOrderbookResult(cfg config.ReversalLongConfig, input ReversalOrderbookInput) models.OrderbookResult {
	result := CheckReversalOrderbookSoft(cfg, input)
	obResult := models.OrderbookResult{
		Evaluated: true,
		Passed:    result.Allowed,
		Ratio:     input.Ratio,
		Imbalance: input.Imbalance,
		Side:      "LONG",
		Reason:    ReversalReasonRuntimeOBPassed,
		History:   string(models.ModeReversalLong),
	}
	if !input.Available && cfg.RequireSoftOrderbookAtRuntime {
		obResult.Passed = false
		obResult.Reason = ReversalReasonRuntimeOBUnavailable
		return obResult
	}
	if !result.Allowed {
		obResult.Passed = false
		obResult.Reason = result.RejectReason
		if obResult.Reason == "" {
			obResult.Reason = ReversalReasonRuntimeOBHostile
		}
	}
	return obResult
}

func WriteReversalReplayJSONL(w io.Writer, result ReversalReplayResult) error {
	enc := json.NewEncoder(w)
	for _, event := range result.Events {
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	return nil
}
