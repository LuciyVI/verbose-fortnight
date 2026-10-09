package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"verbose-fortnight/config"
)

func TestRESTHTTPFailureNotSuccess(t *testing.T) {
	for _, status := range []int{429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"totalAvailableBalance":"100"}]}}`)
			}))
			defer srv.Close()
			cfg := config.LoadConfig()
			cfg.DemoRESTHost = srv.URL
			if balance, err := NewRESTClient(cfg, nil).GetBalance("USDT"); err == nil {
				t.Fatalf("HTTP %d reported successful balance %v", status, balance)
			}
		})
	}
}

func TestInstrumentNumericValidation(t *testing.T) {
	for _, field := range []string{"qtyStep", "minOrderQty", "minNotionalValue", "tickSize"} {
		for _, raw := range []string{"0", "-1", "NaN", "+Inf", "garbage"} {
			if field == "minNotionalValue" && raw == "0" {
				continue
			}
			t.Run(field+"/"+raw, func(t *testing.T) {
				lot := map[string]string{"qtyStep": "0.001", "minOrderQty": "0.001", "minNotionalValue": "10"}
				tick := "0.1"
				if field == "tickSize" {
					tick = raw
				} else {
					lot[field] = raw
				}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"lotSizeFilter":{"qtyStep":%q,"minOrderQty":%q,"minNotionalValue":%q},"priceFilter":{"tickSize":%q}}]}}`, lot["qtyStep"], lot["minOrderQty"], lot["minNotionalValue"], tick)
				}))
				defer srv.Close()
				cfg := config.LoadConfig()
				cfg.DemoRESTHost = srv.URL
				if info, err := NewRESTClient(cfg, nil).GetInstrumentInfo("BTCUSDT"); err == nil {
					t.Fatalf("invalid %s=%s accepted: %+v", field, raw, info)
				}
			})
		}
	}
}
