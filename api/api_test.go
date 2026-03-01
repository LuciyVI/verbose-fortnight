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

func TestUpdatePositionTradingStopNotModified34040(t *testing.T) {
	stopCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/position/trading-stop" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		stopCalls++
		_, _ = w.Write([]byte(`{"retCode":34040,"retMsg":"not modified"}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	if err := client.UpdatePositionTradingStop("BTCUSDT", "Buy", 105.0, 95.0); err != nil {
		t.Fatalf("34040 must be treated as noop: %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("expected exactly one call without retry, got %d", stopCalls)
	}
}

func TestGetOpenOrders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/order/realtime" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"result":{"list":[{"orderId":"oid-1","side":"Buy","qty":"1.5","leavesQty":"1.0","reduceOnly":true}]}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	orders, err := client.GetOpenOrders("BTCUSDT")
	if err != nil {
		t.Fatalf("GetOpenOrders error: %v", err)
	}
	if len(orders) != 1 || orders[0].OrderID != "oid-1" || !orders[0].ReduceOnly {
		t.Fatalf("unexpected open orders: %+v", orders)
	}
}

func TestGetTradingStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/position/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"result":{"list":[{"side":"Buy","size":"0.10","takeProfit":"110.5","stopLoss":"95.2","avgPrice":"100.0"}]}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	tp, sl, err := client.GetTradingStop("BTCUSDT")
	if err != nil {
		t.Fatalf("GetTradingStop error: %v", err)
	}
	if tp != 110.5 || sl != 95.2 {
		t.Fatalf("unexpected trading stop: tp=%f sl=%f", tp, sl)
	}
}

func TestGetExecutionsSince(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{"list":[
					{"execId":"e3","orderId":"o3","orderLinkId":"lc-e3","side":"Sell","execQty":"0.3","execPrice":"103"},
					{"execId":"e2","orderId":"o2","side":"Buy","execQty":"0.2","execPrice":"102"},
					{"execId":"e1","orderId":"o1","side":"Buy","execQty":"0.1","execPrice":"101"}
				]}
			}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	execs, err := client.GetExecutionsSince("BTCUSDT", "e2", 10)
	if err != nil {
		t.Fatalf("GetExecutionsSince error: %v", err)
	}
	if len(execs) != 1 || execs[0].ExecID != "e3" {
		t.Fatalf("unexpected executions: %+v", execs)
	}
	if execs[0].OrderLinkID != "lc-e3" {
		t.Fatalf("unexpected orderLinkId: %+v", execs[0])
	}
}

func TestGetExecutionsPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("cursor"); got != "cursor-1" {
			t.Fatalf("unexpected cursor: %s", got)
		}
		_, _ = w.Write([]byte(`{
			"retCode":0,
			"result":{
				"nextPageCursor":"cursor-2",
				"list":[
					{"execId":"e1","orderId":"o1","orderLinkID":"lc-alt-1","side":"Buy","execQty":"0.1","execPrice":"101","execTime":"1700000001000","createdTime":"1700000000000"}
				]
			}
		}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	page, nextCursor, hasMore, err := client.GetExecutionsPage("BTCUSDT", "cursor-1", 50)
	if err != nil {
		t.Fatalf("GetExecutionsPage error: %v", err)
	}
	if !hasMore || nextCursor != "cursor-2" {
		t.Fatalf("unexpected pagination metadata hasMore=%t next=%s", hasMore, nextCursor)
	}
	if len(page) != 1 {
		t.Fatalf("unexpected page length: %d", len(page))
	}
	if page[0].OrderLinkID != "lc-alt-1" {
		t.Fatalf("expected fallback orderLinkID field, got: %+v", page[0])
	}
	if page[0].ExecTime != 1700000001000 || page[0].CreatedTime != 1700000000000 {
		t.Fatalf("unexpected execution timestamps: %+v", page[0])
	}
}

func TestGetExecutionsSincePaginatesUntilBoundary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		cursor := r.URL.Query().Get("cursor")
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{
					"nextPageCursor":"p2",
					"list":[
						{"execId":"e9","orderId":"o9","side":"Buy","execQty":"0.9","execPrice":"109"},
						{"execId":"e8","orderId":"o8","side":"Buy","execQty":"0.8","execPrice":"108"}
					]
				}
			}`))
		case "p2":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{
					"nextPageCursor":"p3",
					"list":[
						{"execId":"e7","orderId":"o7","side":"Buy","execQty":"0.7","execPrice":"107"},
						{"execId":"e6","orderId":"o6","side":"Buy","execQty":"0.6","execPrice":"106"}
					]
				}
			}`))
		case "p3":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{
					"nextPageCursor":"",
					"list":[
						{"execId":"e5","orderId":"o5","side":"Buy","execQty":"0.5","execPrice":"105"},
						{"execId":"e4","orderId":"o4","side":"Buy","execQty":"0.4","execPrice":"104"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected cursor: %s", cursor)
		}
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	execs, err := client.GetExecutionsSince("BTCUSDT", "e5", 20)
	if err != nil {
		t.Fatalf("GetExecutionsSince error: %v", err)
	}
	if len(execs) != 4 {
		t.Fatalf("expected 4 executions newer than e5, got %d: %+v", len(execs), execs)
	}
	if execs[0].ExecID != "e9" || execs[3].ExecID != "e6" {
		t.Fatalf("unexpected order/content: %+v", execs)
	}
}

func TestGetExecutionsSinceFallbackWhenBoundaryMissingDedup(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v5/execution/list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		call++
		cursor := r.URL.Query().Get("cursor")
		switch cursor {
		case "":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{
					"nextPageCursor":"p2",
					"list":[
						{"execId":"e3","orderId":"o3","side":"Buy","execQty":"0.3","execPrice":"103"},
						{"execId":"e2","orderId":"o2","side":"Buy","execQty":"0.2","execPrice":"102"}
					]
				}
			}`))
		case "p2":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{
					"nextPageCursor":"",
					"list":[
						{"execId":"e2","orderId":"o2","side":"Buy","execQty":"0.2","execPrice":"102"},
						{"execId":"e1","orderId":"o1","side":"Buy","execQty":"0.1","execPrice":"101"}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected cursor: %s", cursor)
		}
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	client := NewRESTClient(cfg, nil)
	execs, err := client.GetExecutionsSince("BTCUSDT", "missing", 20)
	if err != nil {
		t.Fatalf("GetExecutionsSince error: %v", err)
	}
	if call < 2 {
		t.Fatalf("expected pagination fallback calls, got %d", call)
	}
	if len(execs) != 3 {
		t.Fatalf("expected deduped fallback executions, got %d: %+v", len(execs), execs)
	}
}
