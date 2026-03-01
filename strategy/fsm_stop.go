package strategy

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"verbose-fortnight/models"
)

func newTradeID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return fmt.Sprintf("trade-%d", time.Now().UnixNano())
	}
	// UUID v4.
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexStr := hex.EncodeToString(raw)
	return fmt.Sprintf("%s-%s-%s-%s-%s", hexStr[0:8], hexStr[8:12], hexStr[12:16], hexStr[16:20], hexStr[20:32])
}

func allowedFSMTransition(from, to models.PositionFSMState) bool {
	if from == to {
		return true
	}
	switch from {
	case models.PositionStateFlat:
		return to == models.PositionStateOpening
	case models.PositionStateOpening:
		return to == models.PositionStateOpen
	case models.PositionStateOpen:
		return to == models.PositionStateClosing
	case models.PositionStateClosing:
		return to == models.PositionStateFlat
	default:
		return false
	}
}

func (t *Trader) currentFSMState() models.PositionFSMState {
	t.State.RLock()
	defer t.State.RUnlock()
	if t.State.PositionState == "" {
		return models.PositionStateFlat
	}
	return t.State.PositionState
}

func (t *Trader) transitionFSM(to models.PositionFSMState, trigger, tradeID string) bool {
	t.State.Lock()
	defer t.State.Unlock()

	from := t.State.PositionState
	if from == "" {
		from = models.PositionStateFlat
	}
	if tradeID == "" {
		tradeID = t.State.ActiveTradeID
	}
	if !allowedFSMTransition(from, to) {
		t.Logger.Error("FSM transition rejected: from=%s to=%s trade_id=%s trigger=%s", from, to, tradeID, trigger)
		return false
	}
	if from != to {
		t.Logger.Info("FSM transition: from=%s to=%s trade_id=%s trigger=%s", from, to, tradeID, trigger)
	}
	t.State.PositionState = to
	switch to {
	case models.PositionStateOpening:
		t.State.OpeningExecAck = false
		t.State.OpeningPosAck = false
	case models.PositionStateFlat:
		t.State.OpeningExecAck = false
		t.State.OpeningPosAck = false
		t.State.ActiveTradeID = ""
	}
	return true
}

func (t *Trader) forceFSM(to models.PositionFSMState, trigger, tradeID string) {
	t.State.Lock()
	defer t.State.Unlock()
	from := t.State.PositionState
	if from == "" {
		from = models.PositionStateFlat
	}
	if tradeID == "" {
		tradeID = t.State.ActiveTradeID
	}
	if from != to {
		t.Logger.Warning("FSM force sync: from=%s to=%s trade_id=%s trigger=%s", from, to, tradeID, trigger)
	}
	t.State.PositionState = to
	switch to {
	case models.PositionStateFlat:
		t.State.PosSide = ""
		t.State.OrderQty = 0
		t.State.OpeningExecAck = false
		t.State.OpeningPosAck = false
		t.State.ActiveTradeID = ""
	case models.PositionStateOpen:
		t.State.OpeningExecAck = true
		t.State.OpeningPosAck = true
	}
}

func (t *Trader) activeTradeID() string {
	t.State.RLock()
	defer t.State.RUnlock()
	return t.State.ActiveTradeID
}

// ActiveTradeID exposes current lifecycle id for external orchestration (resync/logging).
func (t *Trader) ActiveTradeID() string {
	return t.activeTradeID()
}

func (t *Trader) setActiveTradeID(tradeID string) {
	t.State.Lock()
	defer t.State.Unlock()
	t.State.ActiveTradeID = tradeID
}

func (t *Trader) bindOrderTradeID(orderID, tradeID string) {
	if orderID == "" || tradeID == "" {
		return
	}
	t.State.Lock()
	defer t.State.Unlock()
	if t.State.OrderTradeIDs == nil {
		t.State.OrderTradeIDs = make(map[string]string)
	}
	t.State.OrderTradeIDs[orderID] = tradeID
}

func (t *Trader) bindOrderLifecycle(orderID, orderLinkID, lifecycleID string) {
	if lifecycleID == "" {
		return
	}
	t.bindOrderTradeID(orderID, lifecycleID)
	if t.lifecycle != nil && t.Config != nil && t.Config.EnableLifecycleID {
		if orderID != "" {
			t.lifecycle.BindOrderID(orderID, lifecycleID)
		}
		if orderLinkID != "" {
			t.lifecycle.BindOrderLinkID(orderLinkID, lifecycleID)
		}
	}
}

func (t *Trader) bindExecutionLifecycle(execID, lifecycleID string) {
	if execID == "" || lifecycleID == "" {
		return
	}
	t.State.Lock()
	if t.State.ExecTradeIDs == nil {
		t.State.ExecTradeIDs = make(map[string]string)
	}
	t.State.ExecTradeIDs[execID] = lifecycleID
	t.State.Unlock()
	if t.lifecycle != nil && t.Config != nil && t.Config.EnableLifecycleID {
		t.lifecycle.BindExecID(execID, lifecycleID)
	}
}

// ResolveExecutionTradeID maps execution/order ids to deterministic lifecycle trade id.
func (t *Trader) ResolveExecutionTradeID(orderID, fallback string) string {
	if t.Config != nil && t.Config.EnableLifecycleID {
		lifecycleID, _ := t.ResolveExecutionLifecycleID(orderID, fallback, "")
		return lifecycleID
	}
	return t.resolveExecutionTradeIDLegacy(orderID, fallback)
}

func (t *Trader) resolveExecutionTradeIDLegacy(orderID, fallback string) string {
	t.State.Lock()
	defer t.State.Unlock()

	if orderID != "" && t.State.OrderTradeIDs != nil {
		if tradeID, ok := t.State.OrderTradeIDs[orderID]; ok && tradeID != "" {
			if fallback != "" {
				if t.State.ExecTradeIDs == nil {
					t.State.ExecTradeIDs = make(map[string]string)
				}
				t.State.ExecTradeIDs[fallback] = tradeID
			}
			return tradeID
		}
	}

	if fallback != "" && t.State.ExecTradeIDs != nil {
		if tradeID, ok := t.State.ExecTradeIDs[fallback]; ok && tradeID != "" {
			return tradeID
		}
	}

	if t.State.ActiveTradeID != "" {
		if fallback != "" {
			if t.State.ExecTradeIDs == nil {
				t.State.ExecTradeIDs = make(map[string]string)
			}
			t.State.ExecTradeIDs[fallback] = t.State.ActiveTradeID
		}
		return t.State.ActiveTradeID
	}

	tradeID := newTradeID()
	t.State.ActiveTradeID = tradeID
	if fallback != "" {
		if t.State.ExecTradeIDs == nil {
			t.State.ExecTradeIDs = make(map[string]string)
		}
		t.State.ExecTradeIDs[fallback] = tradeID
	}
	return tradeID
}

func (t *Trader) ResolveExecutionLifecycleID(orderID, execID, orderLinkID string) (string, bool) {
	if t.Config == nil || !t.Config.EnableLifecycleID || t.lifecycle == nil {
		return t.resolveExecutionTradeIDLegacy(orderID, execID), false
	}

	activeLifecycleID := t.activeTradeID()
	lifecycleID, inferred := t.lifecycle.Resolve(orderID, execID, orderLinkID, activeLifecycleID)
	if lifecycleID == "" {
		lifecycleID = t.resolveExecutionTradeIDLegacy(orderID, execID)
		inferred = true
	}

	t.bindOrderLifecycle(orderID, orderLinkID, lifecycleID)
	t.bindExecutionLifecycle(execID, lifecycleID)
	if activeLifecycleID == "" && lifecycleID != "" {
		t.setActiveTradeID(lifecycleID)
	}

	return lifecycleID, inferred
}

func (t *Trader) SetTradingBlocked(reason models.TradingBlockReason, message string) {
	t.State.Lock()
	defer t.State.Unlock()
	if reason == models.BlockedReasonNone {
		reason = models.BlockedReasonManual
	}
	if t.State.TradingBlocked && t.State.BlockReason == reason && t.State.BlockMessage == message {
		return
	}
	t.State.TradingBlocked = true
	t.State.BlockReason = reason
	t.State.BlockMessage = message
	t.State.BlockSetAt = time.Now()
	t.Logger.Error("Trading blocked: reason=%s message=%s", reason, message)
}

func (t *Trader) ClearTradingBlock(reason models.TradingBlockReason, trigger string) bool {
	t.State.Lock()
	defer t.State.Unlock()
	if !t.State.TradingBlocked {
		return true
	}
	if reason != models.BlockedReasonNone && t.State.BlockReason != reason {
		return false
	}
	t.Logger.Info("Trading block cleared: prev_reason=%s trigger=%s", t.State.BlockReason, trigger)
	t.State.TradingBlocked = false
	t.State.BlockReason = models.BlockedReasonNone
	t.State.BlockMessage = ""
	t.State.BlockSetAt = time.Time{}
	return true
}

func (t *Trader) ClearTradingBlockManual(trigger string) bool {
	return t.ClearTradingBlock(models.BlockedReasonNone, "manual:"+trigger)
}

func (t *Trader) isTradingBlocked() (bool, models.TradingBlockReason, string) {
	t.State.RLock()
	defer t.State.RUnlock()
	return t.State.TradingBlocked, t.State.BlockReason, t.State.BlockMessage
}

func (t *Trader) queuePendingReverse(side string, price float64, tradeID string) {
	t.State.Lock()
	defer t.State.Unlock()
	t.State.PendingSide = side
	t.State.PendingPrice = price
	t.State.PendingTradeID = tradeID
}

func (t *Trader) popPendingReverseIfFlat() (string, float64, string, bool) {
	t.State.Lock()
	defer t.State.Unlock()
	if t.State.PositionState == "" {
		t.State.PositionState = models.PositionStateFlat
	}
	if t.State.PositionState != models.PositionStateFlat || t.State.PendingSide == "" {
		return "", 0, "", false
	}
	side := t.State.PendingSide
	price := t.State.PendingPrice
	tradeID := t.State.PendingTradeID
	t.State.PendingSide = ""
	t.State.PendingPrice = 0
	t.State.PendingTradeID = ""
	return side, price, tradeID, true
}

func (t *Trader) syncFSMFromPositionSnapshot(exists bool, side string, size float64) {
	t.State.Lock()
	defer t.State.Unlock()

	if t.State.PositionState == "" {
		t.State.PositionState = models.PositionStateFlat
	}
	prev := t.State.PositionState

	if exists && size > 0 {
		t.State.PosSide = side
		t.State.OrderQty = size
		switch t.State.PositionState {
		case models.PositionStateOpening:
			t.State.OpeningPosAck = true
			if t.State.OpeningExecAck {
				t.State.PositionState = models.PositionStateOpen
			}
		case models.PositionStateFlat:
			// Resync safety if snapshot indicates live position.
			t.State.PositionState = models.PositionStateOpen
			t.State.OpeningExecAck = true
			t.State.OpeningPosAck = true
		}
	} else {
		t.State.PosSide = ""
		t.State.OrderQty = 0
		switch t.State.PositionState {
		case models.PositionStateClosing, models.PositionStateOpen, models.PositionStateOpening:
			t.State.PositionState = models.PositionStateFlat
			t.State.OpeningExecAck = false
			t.State.OpeningPosAck = false
			t.State.ActiveTradeID = ""
		}
	}

	if prev != t.State.PositionState {
		t.Logger.Info("FSM transition: from=%s to=%s trade_id=%s trigger=position_snapshot", prev, t.State.PositionState, t.State.ActiveTradeID)
	}
}

func validateStopInvariants(side string, entry, tp, sl float64, breakeven bool) error {
	if entry <= 0 {
		return fmt.Errorf("invalid entry %.8f", entry)
	}
	if tp <= 0 || sl <= 0 {
		return fmt.Errorf("tp/sl must be positive: tp=%.8f sl=%.8f", tp, sl)
	}
	switch side {
	case "LONG":
		if tp <= entry {
			return fmt.Errorf("LONG invariant violated: tp(%.8f) <= entry(%.8f)", tp, entry)
		}
		if sl > entry && !breakeven {
			return fmt.Errorf("LONG invariant violated: sl(%.8f) > entry(%.8f)", sl, entry)
		}
		if sl == entry && !breakeven {
			return fmt.Errorf("LONG invariant violated: sl equals entry without breakeven")
		}
	case "SHORT":
		if tp >= entry {
			return fmt.Errorf("SHORT invariant violated: tp(%.8f) >= entry(%.8f)", tp, entry)
		}
		if sl < entry && !breakeven {
			return fmt.Errorf("SHORT invariant violated: sl(%.8f) < entry(%.8f)", sl, entry)
		}
		if sl == entry && !breakeven {
			return fmt.Errorf("SHORT invariant violated: sl equals entry without breakeven")
		}
	default:
		return fmt.Errorf("unknown side %q", side)
	}
	return nil
}

func (t *Trader) enqueueStopIntent(intent models.StopIntent) {
	if t.currentFSMState() == models.PositionStateClosing {
		t.Logger.Warning("Stop intent ignored while CLOSING: trade_id=%s reason=%s", intent.TradeID, intent.Reason)
		return
	}
	if intent.Side != "" {
		intent.Side = t.PositionManager.NormalizeSide(intent.Side)
	}
	if intent.TradeID == "" {
		intent.TradeID = t.activeTradeID()
	}
	if intent.TradeID == "" {
		intent.TradeID = newTradeID()
	}
	select {
	case t.State.StopIntentChan <- intent:
	default:
		t.Logger.Error("Stop intent channel overflow: trade_id=%s reason=%s", intent.TradeID, intent.Reason)
		t.SetTradingBlocked(models.BlockedReasonFatalAPI, "stop intent channel overflow")
	}
}

func (t *Trader) enqueueDelayedStopIntent(intent models.StopIntent, delay time.Duration) {
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		t.enqueueStopIntent(intent)
	}()
}

func (t *Trader) stopsEqual(aTP, aSL, bTP, bSL float64) bool {
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	eps := math.Max(tick/2, 1e-9)
	return math.Abs(aTP-bTP) <= eps && math.Abs(aSL-bSL) <= eps
}

func (t *Trader) StartStopController() {
	t.Logger.Info("Starting StopController (single TP/SL writer)")
	for intent := range t.State.StopIntentChan {
		blocked, reason, message := t.isTradingBlocked()
		if blocked {
			t.Logger.Warning("StopController skipped intent while blocked: reason=%s message=%s intent_reason=%s", reason, message, intent.Reason)
			continue
		}
		if t.currentFSMState() == models.PositionStateClosing {
			t.Logger.Warning("StopController dropped intent in CLOSING: trade_id=%s reason=%s", intent.TradeID, intent.Reason)
			continue
		}
		if strings.EqualFold(intent.Kind, "MoveSLToBE") {
			t.Logger.Info("stop_intent move_sl_to_be trade_id=%s lifecycle_id=%s side=%s entry=%.2f sl=%.2f qty_left=%.4f fee_est=%.6f reason=%s",
				intent.TradeID, intent.LifecycleID, intent.Side, intent.Entry, intent.SL, intent.QtyLeft, intent.FeeEstimate, intent.Reason)
		}

		side := t.PositionManager.NormalizeSide(intent.Side)
		entry := intent.Entry
		if entry <= 0 {
			entry = t.PositionManager.GetLastEntryPrice()
		}
		if err := validateStopInvariants(side, entry, intent.TP, intent.SL, intent.Breakeven); err != nil {
			t.Logger.Error("TP/SL invariant violation: trade_id=%s side=%s entry=%.8f tp=%.8f sl=%.8f reason=%s err=%v",
				intent.TradeID, side, entry, intent.TP, intent.SL, intent.Reason, err)
			t.SetTradingBlocked(models.BlockedReasonInvariant, fmt.Sprintf("tp/sl invariant violation (%s): %v", intent.Reason, err))
			continue
		}

		t.State.Lock()
		ctrl := t.State.StopCtrl
		ctrl.DesiredTP = intent.TP
		ctrl.DesiredSL = intent.SL
		ctrl.TradeID = intent.TradeID
		ctrl.Reason = intent.Reason
		ctrl.UpdatedAt = time.Now()
		t.State.StopCtrl = ctrl
		t.State.Unlock()

		t.logTradeEvent("tpsl_intent", tradeEventLog{
			TradeID: intent.TradeID,
			Side:    side,
			Entry:   entry,
			TP:      intent.TP,
			SL:      intent.SL,
		})

		t.State.RLock()
		appliedTP := t.State.StopCtrl.AppliedTP
		appliedSL := t.State.StopCtrl.AppliedSL
		t.State.RUnlock()

		if t.stopsEqual(appliedTP, appliedSL, intent.TP, intent.SL) {
			continue
		}

		exists, posSide, _, _, _ := t.PositionManager.HasOpenPosition()
		posSide = t.PositionManager.NormalizeSide(posSide)
		if !exists || posSide == "" {
			t.Logger.Warning("StopController deferred update: no open position (trade_id=%s)", intent.TradeID)
			continue
		}
		if posSide != side {
			t.Logger.Warning("StopController deferred update: side mismatch intent=%s pos=%s trade_id=%s", side, posSide, intent.TradeID)
			continue
		}

		if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, intent.TP, intent.SL); err != nil {
			t.Logger.Error("StopController apply failed: trade_id=%s tp=%.8f sl=%.8f err=%v", intent.TradeID, intent.TP, intent.SL, err)
			continue
		}

		t.State.Lock()
		ctrl = t.State.StopCtrl
		ctrl.AppliedTP = intent.TP
		ctrl.AppliedSL = intent.SL
		ctrl.UpdatedAt = time.Now()
		t.State.StopCtrl = ctrl
		t.State.Unlock()

		t.logTradeEvent("tpsl_applied", tradeEventLog{
			TradeID: intent.TradeID,
			Side:    side,
			Entry:   entry,
			TP:      intent.TP,
			SL:      intent.SL,
		})
	}
}

func (t *Trader) markExecutionProcessed(execID string, now time.Time) bool {
	if execID == "" {
		return true
	}

	t.State.Lock()
	defer t.State.Unlock()

	maxItems := t.State.ExecDedupMax
	if maxItems <= 0 {
		maxItems = 5000
		t.State.ExecDedupMax = maxItems
	}
	ttl := t.State.ExecDedupTTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
		t.State.ExecDedupTTL = ttl
	}
	if t.State.ProcessedExec == nil {
		t.State.ProcessedExec = make(map[string]time.Time)
	}

	// Compact old ids from the queue head.
	for len(t.State.ExecSeenOrder) > 0 {
		head := t.State.ExecSeenOrder[0]
		seenAt, ok := t.State.ProcessedExec[head]
		if !ok {
			t.State.ExecSeenOrder = t.State.ExecSeenOrder[1:]
			continue
		}
		if now.Sub(seenAt) > ttl || len(t.State.ProcessedExec) > maxItems {
			delete(t.State.ProcessedExec, head)
			t.State.ExecSeenOrder = t.State.ExecSeenOrder[1:]
			continue
		}
		break
	}

	if seenAt, ok := t.State.ProcessedExec[execID]; ok && now.Sub(seenAt) <= ttl {
		return false
	}

	t.State.ProcessedExec[execID] = now
	t.State.ExecSeenOrder = append(t.State.ExecSeenOrder, execID)

	for len(t.State.ProcessedExec) > maxItems && len(t.State.ExecSeenOrder) > 0 {
		head := t.State.ExecSeenOrder[0]
		t.State.ExecSeenOrder = t.State.ExecSeenOrder[1:]
		delete(t.State.ProcessedExec, head)
	}

	return true
}

// ProcessExecutionEvent deduplicates and applies execution events exactly-once.
func (t *Trader) ProcessExecutionEvent(evt models.ExecutionEvent, source string) bool {
	now := time.Now()
	if !t.markExecutionProcessed(evt.ExecID, now) {
		t.Logger.Warning("Duplicate execution ignored: source=%s exec_id=%s order_id=%s trade_id=%s", source, evt.ExecID, evt.OrderID, evt.TradeID)
		return false
	}
	t.bindOrderLifecycle(evt.OrderID, evt.OrderLinkID, evt.TradeID)
	t.bindExecutionLifecycle(evt.ExecID, evt.TradeID)
	if evt.ExecID != "" {
		t.State.Lock()
		t.State.LastExecID = evt.ExecID
		if evt.OrderID != "" {
			t.State.LastOrderID = evt.OrderID
		}
		t.State.Unlock()
	}
	t.HandleExecutionFill(evt)
	return true
}

func partialClosePlan(totalQty, ratio float64) (float64, float64) {
	if totalQty <= 0 {
		return 0, 0
	}
	if ratio <= 0 {
		return 0, totalQty
	}
	if ratio >= 1 {
		return totalQty, 0
	}
	closeQty := totalQty * ratio
	if closeQty > totalQty {
		closeQty = totalQty
	}
	remaining := totalQty - closeQty
	if remaining < 0 {
		remaining = 0
	}
	return closeQty, remaining
}
