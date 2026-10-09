package strategy

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/order"
)

func TestMakerCancellationUncertaintyBlocksFallback(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		for _, kind := range []string{"refused", "lost_response", "ack_without_terminal", "timeout"} {
			t.Run(fmt.Sprintf("reduce=%t/%s", reduce, kind), func(t *testing.T) {
				exchange := &journalExchange{timeoutCancel: kind == "timeout", initialFill: 0.2, holdCancel: true, rejectCancel: kind == "refused", loseCancel: kind == "lost_response"}
				srv := httptest.NewServer(exchange)
				defer srv.Close()
				tr, _, _ := newMakerPolicyTrader()
				tr.Config.DemoRESTHost = srv.URL
				tr.Config.MakerTimeoutMs = 10
				tr.Config.RESTTimeoutMs = 20
				tr.Config.OrderReconcileTimeoutMs = 50
				tr.APIClient = api.NewRESTClient(tr.Config, nopLogger{})
				tr.OrderManager = order.NewOrderManager(tr.APIClient, tr.Config, tr.State, nopLogger{})
				side := "Buy"
				if reduce {
					side = "Sell"
				}
				_, _, err := tr.placeOrderWithExecutionPolicy(side, 1, reduce, "A", "entry")
				if !errors.Is(err, order.ErrOrderUncertain) {
					t.Fatalf("expected reconciliation state: %v", err)
				}
				if !tr.State.OrderReconciliationPending {
					t.Fatal("missing explicit pending state")
				}
				if blocked, _, _ := tr.isTradingBlocked(); !blocked {
					t.Fatal("uncertain order did not block subsequent trading")
				}
				if _, _, err := tr.placeOrderWithExecutionPolicy("Buy", 1, false, "B", "entry"); err == nil {
					t.Fatal("extra order allowed while pending")
				}
				if _, err := tr.ReconcilePendingOrder(context.Background()); !errors.Is(err, order.ErrOrderUncertain) {
					t.Fatalf("live order must remain pending: %v", err)
				}
				exchange.mu.Lock()
				defer exchange.mu.Unlock()
				if len(exchange.orders) != 1 {
					t.Fatalf("extra market order: %d", len(exchange.orders))
				}
			})
		}
	}
}
