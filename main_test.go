package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/strategy"
)

type noopMainLogger struct{}

func (noopMainLogger) Debug(string, ...interface{})          {}
func (noopMainLogger) Info(string, ...interface{})           {}
func (noopMainLogger) Warning(string, ...interface{})        {}
func (noopMainLogger) Error(string, ...interface{})          {}
func (noopMainLogger) Fatal(string, ...interface{})          {}
func (noopMainLogger) Sync() error                           { return nil }
func (noopMainLogger) ChangeLogLevel(level logging.LogLevel) {}

func TestResyncPrivateStateReconnectMidPosition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/position/list":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{"list":[{"side":"Buy","size":"0.50","takeProfit":"110.0","stopLoss":"95.0","avgPrice":"100.0"}]}
			}`))
		case "/v5/order/realtime":
			_, _ = w.Write([]byte(`{"retCode":0,"result":{"list":[]}}`))
		case "/v5/execution/list":
			_, _ = w.Write([]byte(`{
				"retCode":0,
				"result":{"list":[{"execId":"e-1","orderId":"o-1","side":"Buy","execQty":"0.1","execPrice":"100.1"}]}
			}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	cfg.Symbol = "BTCUSDT"

	state := &models.State{
		PositionState:  models.PositionStateFlat,
		StopIntentChan: make(chan models.StopIntent, 16),
		OrderTradeIDs:  make(map[string]string),
		ExecTradeIDs:   make(map[string]string),
		BidsMap:        make(map[string]float64),
		AsksMap:        make(map[string]float64),
	}
	state.Instr.TickSize = 0.1

	client := api.NewRESTClient(cfg, noopMainLogger{})
	trader := strategy.NewTrader(client, cfg, state, noopMainLogger{})
	trader.SetTradingBlocked(models.BlockedReasonReconnect, "test_reconnect")

	if err := resyncPrivateState(client, trader, state, cfg.Symbol); err != nil {
		t.Fatalf("resyncPrivateState error: %v", err)
	}

	if state.PositionState != models.PositionStateOpen {
		t.Fatalf("expected FSM OPEN after resync, got %s", state.PositionState)
	}
	if state.PosSide != "LONG" || state.OrderQty != 0.5 {
		t.Fatalf("unexpected resynced position: side=%s qty=%f", state.PosSide, state.OrderQty)
	}
	if state.StopCtrl.AppliedTP != 110.0 || state.StopCtrl.AppliedSL != 95.0 {
		t.Fatalf("unexpected applied stop state: %+v", state.StopCtrl)
	}
	if state.TradingBlocked {
		t.Fatalf("reconnect block must be cleared after successful resync")
	}
	if state.BlockReason != models.BlockedReasonNone {
		t.Fatalf("unexpected block reason after resync: %s", state.BlockReason)
	}
}

func TestRuntimeDoesNotStartLegacyTPWorker(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "main.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if strings.Contains(string(data), "go trader.TPWorker()") {
		t.Fatalf("legacy TPWorker runtime path must be removed")
	}
}
