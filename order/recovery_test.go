package order

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestOrderAcceptedResponseLostRecovery(t *testing.T) {
	for _, recoverable := range []bool{true, false} {
		t.Run(fmt.Sprint(recoverable), func(t *testing.T) {
			var mu sync.Mutex
			createCount := 0
			acceptedLink := ""
			queryCount := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v5/order/create" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					createCount++
					acceptedLink, _ = body["orderLinkId"].(string)
					mu.Unlock()
					// Acceptance is committed. The response stalls until the client's deadline.
					<-r.Context().Done()
					return
				}
				mu.Lock()
				defer mu.Unlock()
				queryCount++
				if r.URL.Query().Get("orderLinkId") != acceptedLink {
					t.Error("reconciliation changed orderLinkId")
				}
				if recoverable {
					fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"orderId":"accepted","orderLinkId":%q,"qty":"1","cumExecQty":"0","orderStatus":"New"}]}}`, acceptedLink)
				} else {
					fmt.Fprint(w, `{"retCode":0,"result":{"list":[]}}`)
				}
			}))
			defer srv.Close()
			cfg := config.LoadConfig()
			cfg.DemoRESTHost = srv.URL
			cfg.RESTTimeoutMs = 25
			cfg.OrderReconcileTimeoutMs = 60
			state := &models.State{Instr: models.InstrumentInfo{QtyStep: 0.001}}
			om := NewOrderManager(api.NewRESTClient(cfg, nopLogger{}), cfg, state, nopLogger{})
			id, err := om.PlaceOrderMarketWithID("Buy", 1, false)
			if recoverable && (err != nil || id != "accepted") {
				t.Fatalf("failed to recover accepted order: id=%s err=%v", id, err)
			}
			if !recoverable {
				var unknown *UncertainOrderError
				if !errors.Is(err, ErrOrderUncertain) || !errors.As(err, &unknown) || unknown.OrderLinkID == "" {
					t.Fatalf("missing uncertain order identity: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if createCount != 1 || acceptedLink == "" || queryCount == 0 {
				t.Fatalf("create=%d link=%s queries=%d", createCount, acceptedLink, queryCount)
			}
		})
	}
}

func TestOrderValidationBeforeSend(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		fmt.Fprint(w, `{"retCode":0,"result":{"orderId":"unexpected"}}`)
	}))
	defer srv.Close()
	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	for _, tc := range []struct{ qty, step float64 }{{1, 0}, {1, -1}, {1, math.NaN()}, {math.NaN(), 0.1}, {math.Inf(1), 0.1}, {0.001, 0.1}, {-1, 0.1}} {
		om := NewOrderManager(api.NewRESTClient(cfg, nil), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: tc.step}}, nil)
		if _, err := om.PlaceOrderMarketWithID("Buy", tc.qty, false); err == nil {
			t.Fatalf("invalid qty/step sent: %+v", tc)
		}
	}
	if count != 0 {
		t.Fatalf("invalid orders sent: %d", count)
	}
}

func TestOrderServerTimeoutAndDuplicateIDRecovery(t *testing.T) {
	for _, code := range []int{10000, 10016, 10014, 110072} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			creates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					creates++
					fmt.Fprintf(w, `{"retCode":%d,"retMsg":"unknown result"}`, code)
					return
				}
				fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"orderId":"existing","orderLinkId":%q,"qty":"1","cumExecQty":"0.2","orderStatus":"PartiallyFilled"}]}}`, r.URL.Query().Get("orderLinkId"))
			}))
			defer srv.Close()
			cfg := config.LoadConfig()
			cfg.DemoRESTHost = srv.URL
			om := NewOrderManager(api.NewRESTClient(cfg, nil), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: 0.001}}, nil)
			id, err := om.PlaceOrderMarketWithID("Buy", 1, false)
			if err != nil || id != "existing" || creates != 1 {
				t.Fatalf("recovery failed or resubmitted: id=%s creates=%d err=%v", id, creates, err)
			}
		})
	}
}
