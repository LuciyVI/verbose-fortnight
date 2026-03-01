package position

import (
	"bytes"
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

// helper builds a PositionManager with a test server that returns the provided payload for /v5/position/list.
func newTestPM(t *testing.T, listBody any, recordPostBody *bytes.Buffer) (*PositionManager, *httptest.Server) {
	t.Helper()

	getResp, err := json.Marshal(map[string]any{
		"retCode": 0,
		"retMsg":  "OK",
		"result": map[string]any{
			"list": listBody,
		},
	})
	if err != nil {
		t.Fatalf("marshal getResp: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v5/position/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(getResp)
	})
	mux.HandleFunc("/v5/position/trading-stop", func(w http.ResponseWriter, r *http.Request) {
		if recordPostBody != nil {
			body, _ := io.ReadAll(r.Body)
			recordPostBody.Write(body)
		}
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK"}`))
	})

	srv := httptest.NewServer(mux)

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"

	client := api.NewRESTClient(cfg, nopLogger{})
	pm := NewPositionManager(client, cfg, &models.State{}, nopLogger{})
	return pm, srv
}

func TestNormalizeSide(t *testing.T) {
	pm := NewPositionManager(nil, nil, nil, nopLogger{})
	if got := pm.NormalizeSide("buy"); got != "LONG" {
		t.Fatalf("NormalizeSide(buy)=%s want LONG", got)
	}
	if got := pm.NormalizeSide("SELL"); got != "SHORT" {
		t.Fatalf("NormalizeSide(SELL)=%s want SHORT", got)
	}
	if got := pm.NormalizeSide("xxx"); got != "" {
		t.Fatalf("NormalizeSide(invalid)=%s want empty", got)
	}
}

func TestHasOpenPositionReturnsValues(t *testing.T) {
	list := []map[string]string{
		{
			"side":       "Buy",
			"size":       "0.005",
			"takeProfit": "101.5",
			"stopLoss":   "99.9",
			"avgPrice":   "100.0",
		},
	}
	pm, srv := newTestPM(t, list, nil)
	defer srv.Close()

	ok, side, qty, tp, sl := pm.HasOpenPosition()
	if !ok {
		t.Fatalf("expected position")
	}
	if side != "LONG" || qty != 0.005 || tp != 101.5 || sl != 99.9 {
		t.Fatalf("unexpected values side=%s qty=%f tp=%f sl=%f", side, qty, tp, sl)
	}
}

func TestHasOpenPositionNone(t *testing.T) {
	list := []map[string]string{
		{"side": "Sell", "size": "0", "takeProfit": "", "stopLoss": "", "avgPrice": ""},
	}
	pm, srv := newTestPM(t, list, nil)
	defer srv.Close()

	ok, _, _, _, _ := pm.HasOpenPosition()
	if ok {
		t.Fatalf("expected no open position")
	}
}

func TestUpdatePositionTPSLUsesPositionIdxZero(t *testing.T) {
	list := []map[string]string{
		{
			"side":       "Sell",
			"size":       "1",
			"takeProfit": "0",
			"stopLoss":   "0",
			"avgPrice":   "95.0",
		},
	}
	buf := &bytes.Buffer{}
	pm, srv := newTestPM(t, list, buf)
	defer srv.Close()

	if err := pm.UpdatePositionTPSL("BTCUSDT", 90.0, 110.0); err != nil {
		t.Fatalf("UpdatePositionTPSL error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(buf.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if idx, ok := body["positionIdx"].(float64); !ok || idx != 0 {
		t.Fatalf("positionIdx=%v want 0", body["positionIdx"])
	}
	if body["takeProfit"] != "90.00" || body["stopLoss"] != "110.00" {
		t.Fatalf("takeProfit/stopLoss unexpected: %v", body)
	}
}

func TestUpdatePositionTPSLNotModified34040IsNoop(t *testing.T) {
	stopCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v5/position/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"side":"Buy","size":"0.01","takeProfit":"100.0","stopLoss":"99.0","avgPrice":"100.0"}]}}`))
	})
	mux.HandleFunc("/v5/position/trading-stop", func(w http.ResponseWriter, r *http.Request) {
		stopCalls++
		_, _ = w.Write([]byte(`{"retCode":34040,"retMsg":"not modified"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	client := api.NewRESTClient(cfg, nopLogger{})
	pm := NewPositionManager(client, cfg, &models.State{}, nopLogger{})

	if err := pm.UpdatePositionTPSL("BTCUSDT", 100, 99); err != nil {
		t.Fatalf("expected 34040 to be treated as noop, got err: %v", err)
	}
	if stopCalls != 1 {
		t.Fatalf("expected exactly one stop update call, got %d", stopCalls)
	}
}
