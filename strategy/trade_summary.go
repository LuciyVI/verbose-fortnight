package strategy

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"verbose-fortnight/models"
	"verbose-fortnight/pnl"
)

func oppositeSide(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		return "SHORT"
	case "SHORT":
		return "LONG"
	default:
		return ""
	}
}

func inferCloseReason(evt models.ExecutionEvent) string {
	ct := strings.ToLower(strings.TrimSpace(evt.CreateType))
	switch {
	case strings.Contains(ct, "stoploss"):
		return "sl"
	case strings.Contains(ct, "takeprofit"):
		return "tp"
	case strings.Contains(ct, "trailing"):
		return "trail"
	}
	st := strings.ToLower(strings.TrimSpace(evt.StopOrderType))
	switch {
	case strings.Contains(st, "stoploss"):
		return "sl"
	case strings.Contains(st, "takeprofit"):
		return "tp"
	}
	if evt.ReduceOnly {
		return "manual"
	}
	return "unknown"
}

func hasGrossSignMismatch(execPnl, grossCalc float64) bool {
	execSign := pnl.Sign(execPnl)
	grossSign := pnl.Sign(grossCalc)
	if execSign == 0 || grossSign == 0 {
		return false
	}
	return execSign != grossSign
}

func (s *tradeSummaryState) entryVWAP(fallback float64) float64 {
	if s == nil {
		return fallback
	}
	if s.QtyOpened > 0 {
		return s.EntryNotional / s.QtyOpened
	}
	if len(s.Legs) > 0 && s.Legs[0].EntryPrice > 0 {
		return s.Legs[0].EntryPrice
	}
	return fallback
}

func (t *Trader) summaryLifecycleID(evt models.ExecutionEvent) string {
	if t.Config != nil && t.Config.EnableLifecycleID {
		return strings.TrimSpace(evt.TradeID)
	}
	return ""
}

func (t *Trader) summaryTraceKey(evt models.ExecutionEvent) string {
	return t.resolveTraceKey(
		t.summaryLifecycleID(evt),
		evt.TradeID,
		evt.OrderID,
		evt.OrderLinkID,
		evt.ExecID,
	)
}

func (t *Trader) ensureTradeSummaryState(evt models.ExecutionEvent) *tradeSummaryState {
	traceKey := t.summaryTraceKey(evt)
	if traceKey == "" {
		return nil
	}
	t.summaryMu.Lock()
	defer t.summaryMu.Unlock()
	if t.summaries == nil {
		t.summaries = make(map[string]*tradeSummaryState)
	}
	s, ok := t.summaries[traceKey]
	if !ok {
		now := time.Now().UTC()
		s = &tradeSummaryState{
			Symbol:       t.Config.Symbol,
			TraceKey:     traceKey,
			LifecycleID:  t.summaryLifecycleID(evt),
			TradeID:      evt.TradeID,
			Legs:         make([]pnl.Leg, 0, 4),
			StartedAt:    now,
			LastUpdateAt: now,
		}
		t.summaries[traceKey] = s
	}
	return s
}

func (t *Trader) recordTradeSummaryFill(evt models.ExecutionEvent) {
	if t == nil || t.Config == nil {
		return
	}
	s := t.ensureTradeSummaryState(evt)
	if s == nil {
		return
	}

	t.summaryMu.Lock()
	defer t.summaryMu.Unlock()
	now := time.Now().UTC()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.LastUpdateAt = now

	if s.Symbol == "" && t.Config != nil {
		s.Symbol = t.Config.Symbol
	}
	if s.TradeID == "" {
		s.TradeID = evt.TradeID
	}
	if s.LifecycleID == "" {
		s.LifecycleID = t.summaryLifecycleID(evt)
	}
	if evt.OrderID != "" {
		s.LastOrderID = evt.OrderID
	}
	if evt.OrderLinkID != "" {
		s.LastOrderLinkID = evt.OrderLinkID
	}
	if evt.ExecID != "" {
		s.LastExecID = evt.ExecID
	}

	eventPosSide := t.PositionManager.NormalizeSide(evt.PositionSide)
	if eventPosSide == "" {
		eventPosSide = pnl.NormalizePositionSide(evt.PositionSide)
	}
	if s.PositionSide == "" && eventPosSide != "" {
		s.PositionSide = eventPosSide
	}

	if !evt.ReduceOnly && evt.Qty > 0 {
		s.QtyOpened += evt.Qty
		s.EntryNotional += evt.Price * evt.Qty
		s.FeeOpenTotal += math.Abs(evt.ExecFee)
		if s.PositionSide == "" {
			openPosSide := t.PositionManager.NormalizeSide(evt.ExecSide)
			if openPosSide == "" {
				openPosSide = pnl.NormalizePositionSide(evt.ExecSide)
			}
			s.PositionSide = openPosSide
		}
	}

	closedQty := evt.ClosedSize
	if closedQty <= 0 && evt.ReduceOnly && evt.Qty > 0 {
		closedQty = evt.Qty
	}
	if closedQty > 0 {
		if s.PositionSide == "" {
			s.PositionSide = oppositeSide(t.PositionManager.NormalizeSide(evt.ExecSide))
			if s.PositionSide == "" {
				s.PositionSide = oppositeSide(pnl.NormalizePositionSide(evt.ExecSide))
			}
		}
		entryVWAP := s.entryVWAP(evt.Price)
		legGross := pnl.Gross(s.PositionSide, entryVWAP, evt.Price, closedQty)
		if hasGrossSignMismatch(evt.ExecPnl, legGross) {
			t.Logger.Warning("gross_sign_mismatch trace_key=%s lifecycle_id=%s trade_id=%s exec_id=%s position_side=%s exec_pnl=%.8f gross_calc=%.8f entry=%.2f exit=%.2f qty=%.6f",
				s.TraceKey, s.LifecycleID, s.TradeID, evt.ExecID, s.PositionSide, evt.ExecPnl, legGross, entryVWAP, evt.Price, closedQty)
		}
		s.QtyClosed += closedQty
		s.ExitNotional += evt.Price * closedQty
		s.FeeCloseTotal += math.Abs(evt.ExecFee)
		s.CloseExecCount++
		s.Legs = append(s.Legs, pnl.Leg{
			PositionSide:  s.PositionSide,
			EntryPrice:    entryVWAP,
			ExitPrice:     evt.Price,
			Qty:           closedQty,
			Fee:           math.Abs(evt.ExecFee),
			FundingSigned: 0,
		})
	}
	s.FeeTotal = math.Abs(s.FeeOpenTotal) + math.Abs(s.FeeCloseTotal)
	if evt.HasExchangeNet {
		s.HasExchangeNet = true
		s.ExchangeNet += evt.ExchangeNet
		s.RealisedDelta = s.ExchangeNet
	}
	reason := inferCloseReason(evt)
	if reason != "" && reason != "unknown" {
		s.CloseReason = reason
	}
}

func (t *Trader) emitTradeCloseSummaryIfClosed(evt models.ExecutionEvent, positionExists bool) {
	if t == nil || t.Config == nil {
		return
	}
	if positionExists {
		return
	}
	traceKey := t.summaryTraceKey(evt)
	if traceKey == "" {
		return
	}

	t.summaryMu.Lock()
	s, ok := t.summaries[traceKey]
	if !ok || s == nil {
		t.summaryMu.Unlock()
		return
	}
	if s.QtyClosed <= 0 && len(s.Legs) == 0 {
		delete(t.summaries, traceKey)
		t.summaryMu.Unlock()
		return
	}
	delete(t.summaries, traceKey)
	t.summaryMu.Unlock()

	entryVWAP := s.entryVWAP(evt.Price)
	exitVWAP := 0.0
	if s.QtyClosed > 0 {
		exitVWAP = s.ExitNotional / s.QtyClosed
	}
	if exitVWAP <= 0 {
		exitVWAP = evt.Price
	}
	if len(s.Legs) == 0 && s.QtyClosed > 0 {
		s.Legs = append(s.Legs, pnl.Leg{
			PositionSide:  s.PositionSide,
			EntryPrice:    entryVWAP,
			ExitPrice:     exitVWAP,
			Qty:           s.QtyClosed,
			Fee:           s.FeeCloseTotal,
			FundingSigned: s.FundingSigned,
		})
	}
	summary := pnl.Summarize(s.Legs)
	if s.QtyOpened <= 0 {
		s.QtyOpened = s.QtyClosed
	}
	feeOpenAlloc := pnl.AllocateOpenFee(s.FeeOpenTotal, s.QtyOpened, s.QtyClosed)
	if feeOpenAlloc == 0 && s.QtyClosed > 0 && s.FeeOpenTotal > 0 && s.QtyOpened <= 0 {
		feeOpenAlloc = math.Abs(s.FeeOpenTotal)
	}
	feeClose := math.Abs(s.FeeCloseTotal)
	if feeClose == 0 && summary.FeeTotal > 0 {
		feeClose = math.Abs(summary.FeeTotal)
	}
	netExchange := (*float64)(nil)
	if s.HasExchangeNet {
		v := s.ExchangeNet
		netExchange = &v
	}
	breakdown := pnl.BuildNetBreakdown(
		summary.Gross,
		feeOpenAlloc,
		feeClose,
		s.FundingSigned,
		s.RealisedDelta,
		netExchange,
	)
	closeReason := s.CloseReason
	if closeReason == "" {
		closeReason = inferCloseReason(evt)
	}
	if closeReason == "" {
		closeReason = "unknown"
	}
	if s.CloseExecCount == 0 || strings.TrimSpace(s.LastExecID) == "" {
		t.Logger.Warning("missing_exec_for_close trace_key=%s lifecycle_id=%s trade_id=%s qty_closed=%.6f legs=%d", s.TraceKey, s.LifecycleID, s.TradeID, s.QtyClosed, len(s.Legs))
	}
	feeTotal := breakdown.FeeOpenAlloc + breakdown.FeeClose
	if math.Abs(breakdown.Gross) > 1e-9 && feeTotal > math.Abs(breakdown.Gross)*0.7 {
		t.Logger.Warning("fee_outlier trace_key=%s lifecycle_id=%s trade_id=%s gross=%.8f fee_total=%.8f fee_open_alloc=%.8f fee_close=%.8f",
			s.TraceKey, s.LifecycleID, s.TradeID, breakdown.Gross, feeTotal, breakdown.FeeOpenAlloc, breakdown.FeeClose)
	}
	endedAt := time.Now().UTC()
	durationSec := 0.0
	if !s.StartedAt.IsZero() {
		durationSec = endedAt.Sub(s.StartedAt).Seconds()
	}
	netFinal := breakdown.NetCalculated
	if breakdown.NetExchange != nil {
		netFinal = *breakdown.NetExchange
	}
	if t.State != nil {
		t.State.RecordTradeOutcome(durationSec, netFinal)
	}
	if netFinal > 0 {
		if movePct, ok := calcWinMovePct(s.PositionSide, entryVWAP, exitVWAP); ok {
			source := "fill"
			if t.State != nil {
				t.State.RecordLastWinMove(s.PositionSide, movePct, netFinal, entryVWAP, exitVWAP, source, endedAt)
			}
			t.logTradeEvent("last_win_updated", tradeEventLog{
				TradeID:        s.TradeID,
				LifecycleID:    s.LifecycleID,
				OrderID:        s.LastOrderID,
				OrderLinkID:    s.LastOrderLinkID,
				ExecID:         s.LastExecID,
				Source:         source,
				Side:           s.PositionSide,
				Entry:          entryVWAP,
				Exit:           exitVWAP,
				Qty:            s.QtyClosed,
				NetPnL:         netFinal,
				MovePct:        movePct,
				SLPolicy:       "half_last_win",
				LastWinMovePct: movePct,
			})
		}
	}
	if !t.Config.EnableTradeSummaryLog {
		return
	}

	payload := map[string]any{
		"ts":                 endedAt.Format(time.RFC3339Nano),
		"ts_epoch_ms":        endedAt.UnixMilli(),
		"symbol":             s.Symbol,
		"traceKey":           s.TraceKey,
		"lifecycleId":        s.LifecycleID,
		"tradeId":            s.TradeID,
		"orderId":            s.LastOrderID,
		"orderLinkId":        s.LastOrderLinkID,
		"execId":             s.LastExecID,
		"side":               s.PositionSide,
		"positionSide":       s.PositionSide,
		"qty_opened":         s.QtyOpened,
		"qty_total":          s.QtyOpened,
		"qty_closed_total":   s.QtyClosed,
		"entry_vwap":         entryVWAP,
		"exit_vwap":          exitVWAP,
		"gross_calc":         breakdown.Gross,
		"gross_source":       string(breakdown.GrossSource),
		"fee_open_alloc":     breakdown.FeeOpenAlloc,
		"fee_close":          breakdown.FeeClose,
		"fee_total":          feeTotal,
		"funding_signed":     breakdown.FundingSigned,
		"realised_delta_cum": breakdown.RealisedDeltaCum,
		"net_calc":           breakdown.NetCalculated,
		"net_exchange":       netExchange,
		"net_source":         string(breakdown.NetSource),
		"close_reason":       closeReason,
		"legs_count":         len(s.Legs),
		"duration_sec":       durationSec,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Logger.Error("failed to marshal trade_close_summary: %v", err)
		return
	}
	t.Logger.Info("trade_close_summary %s", string(raw))
}
