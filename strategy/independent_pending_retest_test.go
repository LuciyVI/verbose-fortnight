package strategy

import (
	"context"
	"fmt"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/order"
)

type independentPendingExchange struct{ entered, release chan struct{} }

func (e *independentPendingExchange) PlaceOrderMarketWithID(string, float64, bool) (string, error) {
	return "", fmt.Errorf("unexpected create")
}
func (e *independentPendingExchange) PlaceTakeProfitOrder(string, float64, float64) error {
	return fmt.Errorf("unexpected create")
}
func (e *independentPendingExchange) CancelOrder(context.Context, string, string) error { return nil }
func (e *independentPendingExchange) QueryOrder(_ context.Context, id, link string) (api.OrderState, error) {
	close(e.entered)
	<-e.release
	return api.OrderState{OrderID: id, OrderLinkID: link, Qty: 10, Status: "Cancelled"}, nil
}

func TestIndependentPendingReconciliationIdentityBarrier(t *testing.T) {
	tr, _, _ := independentTrader()
	e := &independentPendingExchange{entered: make(chan struct{}), release: make(chan struct{})}
	tr.OrderManager = e
	tr.noteUncertainOrder("old", "old-link", &order.UncertainOrderError{OrderID: "old", OrderLinkID: "old-link", Cause: fmt.Errorf("response lost")})
	done := make(chan struct{})
	go func() { tr.ReconcilePendingOrder(context.Background()); close(done) }()
	independentWait(t, e.entered, "old reconciliation started")
	// A different previously admitted request finishes ambiguously while the old
	// reconciliation is in flight; it becomes the current pending identity.
	tr.noteUncertainOrder("new", "new-link", &order.UncertainOrderError{OrderID: "new", OrderLinkID: "new-link", Cause: fmt.Errorf("second response lost")})
	close(e.release)
	independentWait(t, done, "old terminal reply")
	cleared := tr.ClearTradingBlockManual("independent-test")
	tr.State.RLock()
	pending, id, blocked := tr.State.OrderReconciliationPending, tr.State.PendingOrderID, tr.State.TradingBlocked
	tr.State.RUnlock()
	if !pending || id != "new" || !blocked || cleared {
		t.Errorf("old reply released unresolved new order: pending=%t id=%s blocked=%t manualClear=%t", pending, id, blocked, cleared)
	}
}
