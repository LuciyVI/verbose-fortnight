package order

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...interface{})          {}
func (nopLogger) Info(string, ...interface{})           {}
func (nopLogger) Warning(string, ...interface{})        {}
func (nopLogger) Error(string, ...interface{})          {}
func (nopLogger) Fatal(string, ...interface{})          {}
func (nopLogger) Sync() error                           { return nil }
func (nopLogger) ChangeLogLevel(level logging.LogLevel) {}

func TestFormatQtyRespectsStep(t *testing.T) {
	om := &OrderManager{}
	if got := om.FormatQty(0.1234, 0.001); got != "0.123" {
		t.Fatalf("FormatQty wrong rounding: %s", got)
	}
	if got := om.FormatQty(2, 1); got != "2" {
		t.Fatalf("FormatQty whole number expected, got %s", got)
	}
}

func TestPlaceOrderMarketPayload(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK"}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.Symbol = "BTCUSDT"
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	state := &models.State{Instr: models.InstrumentInfo{QtyStep: 0.001}}
	client := api.NewRESTClient(cfg, nopLogger{})
	om := NewOrderManager(client, cfg, state, nopLogger{})

	if err := om.PlaceOrderMarket("Buy", 0.0054, true); err != nil {
		t.Fatalf("PlaceOrderMarket error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body["side"] != "Buy" || body["reduceOnly"] != true {
		t.Fatalf("unexpected body: %v", body)
	}
	if body["qty"] != "0.005" {
		t.Fatalf("qty formatted wrong: %v", body["qty"])
	}
}
