package status

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestStatusShowsAllPendingGenerations(t *testing.T) {
	state := &models.State{PendingOrders: map[uint64]models.PendingOrder{
		1: {OrderID: "old", OrderLinkID: "old-link", Generation: 1},
		2: {OrderID: "new", OrderLinkID: "new-link", Generation: 2},
	}, PendingOrderGeneration: 2, PendingOrderID: "new", OrderReconciliationPending: true, TradingBlocked: true}
	recorder := httptest.NewRecorder()
	buildStatusMux(config.LoadConfig(), state).ServeHTTP(recorder, httptest.NewRequest("GET", "/status", nil))
	var payload struct {
		PendingOrders []models.PendingOrder `json:"pendingOrders"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.PendingOrders) != 2 || payload.PendingOrders[0].Generation != 1 || payload.PendingOrders[0].OrderID != "old" || payload.PendingOrders[1].Generation != 2 || payload.PendingOrders[1].OrderID != "new" {
		t.Fatalf("pending records lost in diagnostics: %s", recorder.Body.String())
	}
}
