package order

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestProtectivePricesMatchInstrumentTickOnWire(t *testing.T) {
	for _, tick := range []float64{1, 0.1, 0.001, 0.25} {
		for _, side := range []string{"Sell", "Buy"} {
			for _, kind := range []string{"tp", "sl_limit", "sl_trigger"} {
				t.Run(fmt.Sprintf("%s/%s/tick=%g", kind, side, tick), func(t *testing.T) {
					var journal []map[string]any
					positionSide := "Buy"
					long := side == "Sell"
					if !long {
						positionSide = "Sell"
					}
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/v5/position/list" {
							fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","positionIdx":0,"side":%q,"size":"10","avgPrice":"100"}]}}`, positionSide)
							return
						}
						if r.URL.Path != "/v5/order/create" {
							http.Error(w, "unexpected path", 500)
							return
						}
						var body map[string]any
						json.NewDecoder(r.Body).Decode(&body)
						journal = append(journal, body)
						fmt.Fprint(w, `{"retCode":0,"result":{"orderId":"protective"}}`)
					}))
					defer srv.Close()
					cfg := config.LoadConfig()
					cfg.DemoRESTHost = srv.URL
					cfg.APIKey = "offline"
					cfg.APISecret = "offline"
					om := NewOrderManager(api.NewRESTClient(cfg, nopLogger{}), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: 0.1, MinQty: 0.1, TickSize: tick}}, nopLogger{})
					price := 100 + 4.4*tick
					if (long && kind != "tp") || (!long && kind == "tp") {
						price = 100 - 4.4*tick
					}
					var err error
					switch kind {
					case "tp":
						err = om.PlaceTakeProfitOrder(side, 2, price)
					case "sl_limit":
						err = om.PlaceStopLossOrder(side, 2, price)
					case "sl_trigger":
						err = om.PlaceStopLoss(side, 2, price)
					}
					if err != nil || len(journal) != 1 {
						t.Fatalf("err=%v journal=%v", err, journal)
					}
					key := "price"
					if kind == "sl_trigger" {
						key = "stopLossPrice"
					}
					text, ok := journal[0][key].(string)
					if !ok {
						t.Fatalf("missing wire price: %v", journal)
					}
					wire, _ := new(big.Rat).SetString(text)
					step, _ := new(big.Rat).SetString(strconv.FormatFloat(tick, 'f', -1, 64))
					input, _ := new(big.Rat).SetString(strconv.FormatFloat(price, 'f', -1, 64))
					if wire == nil || !new(big.Rat).Quo(wire, step).IsInt() {
						t.Fatalf("price %s not tick aligned", text)
					}
					up := (long && kind != "tp") || (!long && kind == "tp")
					if (up && wire.Cmp(input) < 0) || (!up && wire.Cmp(input) > 0) {
						t.Fatalf("incorrect rounding on wire: %s input=%g", text, price)
					}
					if journal[0]["reduceOnly"] != true || journal[0]["side"] != side {
						t.Fatalf("protective policy lost: %v", journal)
					}
				})
			}
		}
	}
}

func TestProtectivePriceCollapseRejectedBeforeCreate(t *testing.T) {
	creates := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/position/list" {
			fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","positionIdx":0,"side":"Buy","size":"10","avgPrice":"100"}]}}`)
			return
		}
		creates++
		fmt.Fprint(w, `{"retCode":0,"result":{"orderId":"unexpected"}}`)
	}))
	defer srv.Close()
	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "offline"
	cfg.APISecret = "offline"
	om := NewOrderManager(api.NewRESTClient(cfg, nopLogger{}), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: 0.1, TickSize: 0.25}}, nopLogger{})
	if err := om.PlaceTakeProfitOrder("Sell", 2, 100.01); err == nil {
		t.Error("collapsed TP accepted")
	}
	if err := om.PlaceStopLoss("Sell", 2, 99.99); err == nil {
		t.Error("collapsed SL accepted")
	}
	if creates != 0 {
		t.Fatalf("collapsed price sent %d creates", creates)
	}
}
