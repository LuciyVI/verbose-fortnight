package strategy

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
	"verbose-fortnight/order"
)

type orderReconciler interface {
	QueryOrder(context.Context, string, string) (api.OrderState, error)
	CancelOrder(context.Context, string, string) error
}

type fillProgress string

const (
	fillNone     fillProgress = "none"
	fillPartial  fillProgress = "partial"
	fillComplete fillProgress = "complete"
)

func makerFirstFillProgress(requested, executed float64) (fillProgress, float64) {
	remaining := math.Max(requested-executed, 0)
	if remaining == 0 {
		return fillComplete, 0
	}
	if executed > 0 {
		return fillPartial, remaining
	}
	return fillNone, remaining
}

func (t *Trader) requestContext() context.Context {
	if t.APIClient != nil && t.APIClient.Context != nil {
		return t.APIClient.Context
	}
	return context.Background()
}
func (t *Trader) reconcileTimeout() time.Duration {
	if t.Config.OrderReconcileTimeoutMs > 0 {
		return time.Duration(t.Config.OrderReconcileTimeoutMs) * time.Millisecond
	}
	return 3 * time.Second
}

func (t *Trader) noteUncertainOrder(id, link string, err error) {
	if !errors.Is(err, order.ErrOrderUncertain) {
		return
	}
	var unknown *order.UncertainOrderError
	if errors.As(err, &unknown) {
		if unknown.OrderID != "" {
			id = unknown.OrderID
		}
		if unknown.OrderLinkID != "" {
			link = unknown.OrderLinkID
		}
	}
	t.State.Lock()
	t.State.PendingOrderSequence++
	generation := t.State.PendingOrderSequence
	if t.State.PendingOrders == nil {
		t.State.PendingOrders = make(map[uint64]models.PendingOrder)
	}
	t.State.PendingOrders[generation] = models.PendingOrder{OrderID: id, OrderLinkID: link, Generation: generation}
	t.State.PendingOrderGeneration = generation
	t.State.PendingOrderID = id
	t.State.PendingOrderLinkID = link
	t.State.OrderReconciliationPending = true
	t.State.TradingBlocked = true
	t.State.BlockReason = models.BlockedReasonFatalAPI
	t.State.BlockMessage = fmt.Sprintf("awaiting order reconciliation: generation=%d orderId=%s orderLinkId=%s pending=%d", generation, id, link, len(t.State.PendingOrders))
	t.State.BlockSetAt = time.Now()
	message := t.State.BlockMessage
	t.State.Unlock()
	t.Logger.Error("Trading blocked: reason=%s message=%s", models.BlockedReasonFatalAPI, message)
}

var ErrStaleReconciliation = errors.New("pending reconciliation generation is no longer current")

// ReconcilePendingOrder observes the most recently registered uncertainty. All
// unresolved older generations remain retained and keep manual clear blocked.
func (t *Trader) ReconcilePendingOrder(ctx context.Context) (api.OrderState, error) {
	t.State.RLock()
	generation := t.State.PendingOrderGeneration
	t.State.RUnlock()
	return t.ReconcilePendingOrderGeneration(ctx, generation)
}

// ReconcilePendingOrderGeneration also allows an operator integration to resolve
// older entries. Terminal replies can only remove their own captured generation.
func (t *Trader) ReconcilePendingOrderGeneration(ctx context.Context, generation uint64) (api.OrderState, error) {
	t.State.RLock()
	pending, exists := t.State.PendingOrders[generation]
	t.State.RUnlock()
	if !exists {
		return api.OrderState{}, ErrStaleReconciliation
	}
	om, ok := t.OrderManager.(orderReconciler)
	if !ok {
		return api.OrderState{}, fmt.Errorf("order reconciliation unavailable")
	}
	snapshot, err := om.QueryOrder(ctx, pending.OrderID, pending.OrderLinkID)
	if err != nil {
		return snapshot, err
	}
	if (pending.OrderID != "" && snapshot.OrderID != pending.OrderID) ||
		(pending.OrderID == "" && (pending.OrderLinkID == "" || snapshot.OrderLinkID != pending.OrderLinkID)) ||
		(pending.OrderLinkID != "" && snapshot.OrderLinkID != "" && snapshot.OrderLinkID != pending.OrderLinkID) {
		return snapshot, fmt.Errorf("reconciliation result identity mismatch")
	}
	t.State.Lock()
	defer t.State.Unlock()
	current, exists := t.State.PendingOrders[generation]
	if !exists || current != pending {
		return snapshot, ErrStaleReconciliation
	}
	if !snapshot.Terminal() {
		return snapshot, order.ErrOrderUncertain
	}
	delete(t.State.PendingOrders, generation)
	t.State.OrderReconciliationPending = len(t.State.PendingOrders) != 0
	if t.State.PendingOrderGeneration == generation {
		t.State.PendingOrderGeneration = 0
		t.State.PendingOrderID = ""
		t.State.PendingOrderLinkID = ""
		for g, entry := range t.State.PendingOrders {
			if g > t.State.PendingOrderGeneration {
				t.State.PendingOrderGeneration = g
				t.State.PendingOrderID = entry.OrderID
				t.State.PendingOrderLinkID = entry.OrderLinkID
			}
		}
	}
	// TradingBlocked remains set until every uncertainty is resolved and the
	// operator has reconciled the position/stops and explicitly acknowledged it.
	return snapshot, nil
}

func (t *Trader) placeMakerFirstOrderWithFallback(side string, qty float64, reduceOnly bool, lifecycleID, leg string) (id, link string, err error) {
	om, hasPostOnly := t.OrderManager.(orderManagerWithPostOnlyLink)
	reconcile, hasReconcile := t.OrderManager.(orderReconciler)
	if !hasPostOnly || !hasReconcile {
		return t.placeMarketOrderWithLifecycle(side, qty, reduceOnly, lifecycleID, leg)
	}
	price := t.makerFirstLimitPrice(side)
	if price <= 0 {
		return t.placeMarketOrderWithLifecycle(side, qty, reduceOnly, lifecycleID, leg)
	}
	t.State.RLock()
	step := t.State.Instr.QtyStep
	t.State.RUnlock()
	formatted := (&order.OrderManager{}).FormatQty(qty, step)
	qty, err = strconv.ParseFloat(formatted, 64)
	if err != nil || qty <= 0 {
		return "", "", fmt.Errorf("invalid maker quantity or step")
	}
	if lifecycleID == "" {
		lifecycleID = newTradeID()
	}
	link = lifecycleOrderLinkID(lifecycleID, leg+"l")
	t.logMakerFirstEvent("maker_first_attempt", map[string]any{"side": side, "qty": qty, "reduceOnly": reduceOnly, "price": price, "lifecycle": lifecycleID})
	id, err = om.PlaceOrderLimitPostOnlyWithLinkID(side, qty, price, reduceOnly, link)
	if err != nil {
		var rejection *api.Rejection
		if errors.As(err, &rejection) && !rejection.OrderResultUnknown() {
			return t.placeMarketOrderWithLifecycle(side, qty, reduceOnly, lifecycleID, leg)
		}
		if errors.Is(err, order.ErrOrderUncertain) {
			return id, link, err
		}
		return id, link, err
	}
	if id == "" {
		return id, link, &order.UncertainOrderError{OrderLinkID: link, Cause: fmt.Errorf("missing maker order id")}
	}
	// Cumulative snapshots make duplicate/reordered execution deliveries irrelevant.
	// Never decrease observed execution; a regressing terminal snapshot is inconclusive.
	maxExecuted := 0.0
	observe := func(ctx context.Context) (api.OrderState, error) {
		s, e := reconcile.QueryOrder(ctx, id, link)
		if e != nil {
			return s, e
		}
		if s.OrderID != id || math.IsNaN(s.CumExecQty) || math.IsInf(s.CumExecQty, 0) || s.CumExecQty < maxExecuted || s.CumExecQty > qty || s.Qty != qty {
			return s, fmt.Errorf("inconsistent order snapshot")
		}
		maxExecuted = s.CumExecQty
		return s, nil
	}
	wait := func(ctx context.Context) bool {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	makerCtx, cancelMaker := context.WithTimeout(t.requestContext(), t.makerFirstTimeout())
	for {
		s, e := observe(makerCtx)
		progress, _ := makerFirstFillProgress(qty, maxExecuted)
		if e == nil && progress == fillComplete && s.Status == "Filled" {
			cancelMaker()
			return id, link, nil
		}
		if e == nil && s.Terminal() {
			break
		}
		if !wait(makerCtx) {
			break
		}
	}
	cancelMaker()
	t.State.RecordMakerTimeout()
	t.logMakerFirstEvent("maker_first_timeout", map[string]any{"order_id": id, "partial_fill_qty": maxExecuted, "remaining_qty": qty - maxExecuted})
	// A cancel acknowledgement only starts reconciliation; it is never fill evidence.
	ctx, cancel := context.WithTimeout(t.requestContext(), t.reconcileTimeout())
	defer cancel()
	cancelErr := reconcile.CancelOrder(ctx, id, link)
	for {
		s, e := observe(ctx)
		if e == nil && s.Terminal() {
			progress, remaining := makerFirstFillProgress(qty, maxExecuted)
			if progress == fillComplete {
				return id, link, nil
			}
			if s.Status == "Filled" {
				return id, link, &order.UncertainOrderError{OrderID: id, OrderLinkID: link, Cause: fmt.Errorf("Filled with incomplete cumulative qty")}
			}
			// Only a terminal snapshot fixes the final cumulative fill, even when cancel
			// failed or its response was lost. Nonterminal snapshots cannot authorize fallback.
			fallbackID, fallbackLink, fallbackErr := t.placeMarketOrderWithLifecycle(side, remaining, reduceOnly, lifecycleID, leg+"m")
			if fallbackErr == nil {
				t.State.RecordMakerFallback()
				slippage := t.calcMakerFallbackSlippagePct(side, price)
				if t.Config.MakerMaxSlippagePct > 0 && slippage > t.Config.MakerMaxSlippagePct {
					t.Logger.Warning("maker_first_slippage_warn side=%s slippage_pct=%.6f", side, slippage)
				}
				t.logMakerFirstEvent("maker_first_fallback_market", map[string]any{"order_id": fallbackID, "qty": remaining, "reduceOnly": reduceOnly, "partial_fill_qty": maxExecuted, "slippage_pct": slippage})
			}
			if fallbackErr != nil && maxExecuted > 0 && !errors.Is(fallbackErr, order.ErrOrderUncertain) {
				return id, link, &order.UncertainOrderError{OrderID: id, OrderLinkID: link, Cause: fmt.Errorf("partially filled maker; remainder placement failed: %w", fallbackErr)}
			}
			return fallbackID, fallbackLink, fallbackErr
		}
		if !wait(ctx) {
			cause := e
			if cause == nil {
				cause = ctx.Err()
			}
			if cancelErr != nil {
				cause = fmt.Errorf("cancel: %v; reconcile: %w", cancelErr, cause)
			}
			return id, link, &order.UncertainOrderError{OrderID: id, OrderLinkID: link, Cause: cause}
		}
	}
}
