package api

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestTradingStopTickPayloadAndCollapse(t *testing.T) {
	for _, side := range []string{"Buy", "Sell"} {
		for _, tick := range []float64{1, 0.1, 0.001, 0.25} {
			t.Run(fmt.Sprintf("%s/tick=%g", side, tick), func(t *testing.T) {
				var journal []map[string]any
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v5/market/instruments-info":
						fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"lotSizeFilter":{"qtyStep":"0.1","minOrderQty":"0.1","minNotionalValue":"0"},"priceFilter":{"tickSize":%q}}]}}`, strconv.FormatFloat(tick, 'f', -1, 64))
					case "/v5/position/list":
						fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","positionIdx":0,"side":%q,"size":"10","avgPrice":"100"}]}}`, side)
					case "/v5/position/trading-stop":
						var body map[string]any
						json.NewDecoder(r.Body).Decode(&body)
						journal = append(journal, body)
						fmt.Fprint(w, `{"retCode":0}`)
					default:
						http.Error(w, "unexpected path", 500)
					}
				}))
				defer srv.Close()
				client := NewRESTClient(independentAPICfg(srv.URL), nil)
				tp, sl := 100+4.4*tick, 100-4.4*tick
				if side == "Sell" {
					tp, sl = sl, tp
				}
				if err := client.UpdatePositionTradingStop("BTCUSDT", side, tp, sl); err != nil {
					t.Fatal(err)
				}
				if len(journal) != 1 {
					t.Fatalf("journal=%v", journal)
				}
				step, _ := new(big.Rat).SetString(strconv.FormatFloat(tick, 'f', -1, 64))
				for _, key := range []string{"takeProfit", "stopLoss"} {
					wire, _ := new(big.Rat).SetString(fmt.Sprint(journal[0][key]))
					if wire == nil || !new(big.Rat).Quo(wire, step).IsInt() {
						t.Fatalf("unaligned actual JSON: %v", journal)
					}
				}
				collapsedTP, validSL := 100+tick/4, 100-2*tick
				if side == "Sell" {
					collapsedTP, validSL = 100-tick/4, 100+2*tick
				}
				if err := client.UpdatePositionTradingStop("BTCUSDT", side, collapsedTP, validSL); err == nil {
					t.Error("TP collapsing to entry accepted")
				}
				if len(journal) != 1 {
					t.Fatal("invalid quantized payload reached POST")
				}
			})
		}
	}
}
