package order

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestProtectiveOrderRejectionNotSuccess(t *testing.T) {
	for _, response := range []string{`{"retCode":10001,"retMsg":"invalid qty"}`, `{broken`} {
		for _, method := range []string{"tp", "sl-limit", "sl-market"} {
			t.Run(method+"/"+response, func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v5/position/list" {
						fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","positionIdx":0,"side":"Buy","size":"1","avgPrice":"100"}]}}`)
						return
					}
					fmt.Fprint(w, response)
				}))
				defer srv.Close()
				cfg := config.LoadConfig()
				cfg.DemoRESTHost = srv.URL
				// Keep ambiguous-response reconciliation bounded during this regression test.
				setReconcileTimeout(cfg)
				state := &models.State{Instr: models.InstrumentInfo{QtyStep: 0.001, TickSize: 0.1}}
				om := NewOrderManager(api.NewRESTClient(cfg, nopLogger{}), cfg, state, nopLogger{})
				var err error
				switch method {
				case "tp":
					err = om.PlaceTakeProfitOrder("Sell", 1, 110)
				case "sl-limit":
					err = om.PlaceStopLossOrder("Sell", 1, 90)
				case "sl-market":
					err = om.PlaceStopLoss("Sell", 1, 90)
				}
				if err == nil {
					t.Fatal("rejected or malformed protective order reported success")
				}
			})
		}
	}
}
