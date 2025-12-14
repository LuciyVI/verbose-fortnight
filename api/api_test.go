package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"verbose-fortnight/config"
)

func TestSignREST(t *testing.T) {
	cfg := &config.Config{}
	client := NewRESTClient(cfg, nil)
	got := client.SignREST("secret", "1690000000000", "key", "5000", "param=1")

	mac := hmac.New(sha256.New, []byte("secret"))
	mac.Write([]byte("1690000000000" + "key" + "5000" + "param=1"))
	want := mac.Sum(nil)
	if got != "1c841861eb3bfcf8e5fe5ee1b44618f0c1be32c5002407acf77e64a5d80eb9c4" {
		t.Fatalf("SignREST mismatch: got %s want %x", got, want)
	}
}

func TestGetInstrumentInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/market/instruments-info" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"retMsg":"OK",
			"result":{"list":[{"lotSizeFilter":{"minNotionalValue":"10","minOrderQty":"0.001","qtyStep":"0.001"},"priceFilter":{"tickSize":"0.10"}}]}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)

	info, err := client.GetInstrumentInfo("BTCUSDT")
	if err != nil {
		t.Fatalf("GetInstrumentInfo error: %v", err)
	}
	if info.MinNotional != 10 || info.MinQty != 0.001 || info.QtyStep != 0.001 || info.TickSize != 0.1 {
		t.Fatalf("unexpected instrument info: %+v", info)
	}
}

func TestGetBalance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/account/wallet-balance" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"retMsg":"OK",
			"result":{"list":[{"totalAvailableBalance":"123.45"}]}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)

	bal, err := client.GetBalance("USDT")
	if err != nil {
		t.Fatalf("GetBalance error: %v", err)
	}
	if bal != 123.45 {
		t.Fatalf("balance mismatch: %f", bal)
	}
}

func TestGetPositionList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/position/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"result":{"list":[{"side":"Buy","size":"0.01","takeProfit":"100.1","stopLoss":"99.0","avgPrice":"100.0"}]}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)

	list, err := client.GetPositionList("BTCUSDT")
	if err != nil {
		t.Fatalf("GetPositionList error: %v", err)
	}
	if len(list) != 1 || list[0].Side != "Buy" || list[0].Size != "0.01" {
		t.Fatalf("unexpected position list: %+v", list)
	}
}

func TestUpdatePositionTradingStop(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/position/list" {
			_, _ = w.Write([]byte(`{"retCode":0,"result":{"list":[{"side":"Buy","size":"0.01","takeProfit":"","stopLoss":"","avgPrice":"100"}]}}`))
			return
		}
		if r.URL.Path != "/v5/position/trading-stop" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK"}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)

	if err := client.UpdatePositionTradingStop("BTCUSDT", "Buy", 105.0, 95.0); err != nil {
		t.Fatalf("UpdatePositionTradingStop error: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed["takeProfit"] != "105.00" || parsed["stopLoss"] != "95.00" {
		t.Fatalf("unexpected body: %v", parsed)
	}
}
