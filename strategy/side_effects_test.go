package strategy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

type marketCall struct {
	Side       string
	Qty        float64
	ReduceOnly bool
}

type spyOrderManager struct {
	mu          sync.Mutex
	marketCalls []marketCall
	nextID      int
}

func (s *spyOrderManager) PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketCalls = append(s.marketCalls, marketCall{Side: side, Qty: qty, ReduceOnly: reduceOnly})
	s.nextID++
	return fmt.Sprintf("oid-test-%d", s.nextID), nil
}

func (s *spyOrderManager) PlaceTakeProfitOrder(side string, qty, price float64) error {
	return nil
}

func (s *spyOrderManager) calls() []marketCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]marketCall, len(s.marketCalls))
	copy(out, s.marketCalls)
	return out
}

type spyPositionManager struct {
	mu          sync.Mutex
	exists      bool
	side        string
	qty         float64
	tp          float64
	sl          float64
	entry       float64
	bid         float64
	ask         float64
	updateCalls int
	hasOpenHits int
}

func (s *spyPositionManager) NormalizeSide(side string) string {
	switch strings.ToUpper(side) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

func (s *spyPositionManager) HasOpenPosition() (bool, string, float64, float64, float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasOpenHits++
	return s.exists, s.side, s.qty, s.tp, s.sl
}

func (s *spyPositionManager) GetLastEntryPrice() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entry
}

func (s *spyPositionManager) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateCalls++
	s.tp = tp
	s.sl = sl
	return nil
}

func (s *spyPositionManager) GetLastBidPrice() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bid
}

func (s *spyPositionManager) GetLastAskPrice() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ask
}

func (s *spyPositionManager) CancelAllOrders(symbol string) {}

func (s *spyPositionManager) setOpen(side string, qty, entry, tp, sl float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exists = qty > 0
	s.side = side
	s.qty = qty
	s.entry = entry
	s.tp = tp
	s.sl = sl
}

func (s *spyPositionManager) updateCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateCalls
}

func (s *spyPositionManager) hasOpenCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasOpenHits
}

func newSpyTrader(t *testing.T) (*Trader, *spyOrderManager, *spyPositionManager, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/account/wallet-balance":
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"totalAvailableBalance":"10000"}]}}`))
		default:
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[]}}`))
		}
	}))

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	cfg.Symbol = "BTCUSDT"
	cfg.OrderBalancePct = 0.001

	state := &models.State{
		PositionState:  models.PositionStateFlat,
		StopIntentChan: make(chan models.StopIntent, 32),
		OrderTradeIDs:  make(map[string]string),
		ExecTradeIDs:   make(map[string]string),
		ProcessedExec:  make(map[string]time.Time),
		ExecDedupMax:   200,
		ExecDedupTTL:   time.Hour,
		BidsMap:        map[string]float64{"99.9": 1},
		AsksMap:        map[string]float64{"100.1": 1},
	}
	state.Instr.MinQty = 0.001
	state.Instr.QtyStep = 0.001
	state.Instr.MinNotional = 1
	state.Instr.TickSize = 0.1
	state.MarketRegime = "range"

	orderSpy := &spyOrderManager{}
	posSpy := &spyPositionManager{
		bid:   99.9,
		ask:   100.1,
		entry: 100,
	}

	tr := &Trader{
		APIClient:       api.NewRESTClient(cfg, nopLogger{}),
		Config:          cfg,
		State:           state,
		OrderManager:    orderSpy,
		PositionManager: posSpy,
		Logger:          nopLogger{},
	}

	return tr, orderSpy, posSpy, srv.Close
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

func TestReverseOrderingNoOpenBeforeFlatConfirm(t *testing.T) {
	tr, orderSpy, posSpy, cleanup := newSpyTrader(t)
	defer cleanup()

	posSpy.setOpen("LONG", 1, 100, 101, 99)
	tr.State.PositionState = models.PositionStateOpen
	tr.State.OrderQty = 1
	tr.State.ActiveTradeID = "trade-long"

	tr.handleDirectionalSignal("SHORT", 100)

	calls := orderSpy.calls()
	if len(calls) != 1 {
		t.Fatalf("expected only close order before FLAT confirmation, got %d calls", len(calls))
	}
	if !calls[0].ReduceOnly {
		t.Fatalf("first order must be reduce-only close")
	}
	if tr.currentFSMState() != models.PositionStateClosing {
		t.Fatalf("expected FSM=CLOSING, got %s", tr.currentFSMState())
	}
	if _, _, _, ok := tr.popPendingReverseIfFlat(); ok {
		t.Fatalf("pending reverse must not be executable before FLAT confirmation")
	}
}

func TestReconnectGateBlocksOrdersUntilCleared(t *testing.T) {
	tr, orderSpy, _, cleanup := newSpyTrader(t)
	defer cleanup()

	tr.SetTradingBlocked(models.BlockedReasonReconnect, "ws_down")
	tr.handleDirectionalSignal("LONG", 100)
	if len(orderSpy.calls()) != 0 {
		t.Fatalf("orders must be blocked during reconnect")
	}

	if !tr.ClearTradingBlock(models.BlockedReasonReconnect, "resync_ok") {
		t.Fatalf("expected reconnect clear to succeed")
	}
	tr.handleDirectionalSignal("LONG", 100)
	calls := orderSpy.calls()
	if len(calls) != 1 {
		t.Fatalf("expected one open order after reconnect clear, got %d", len(calls))
	}
	if calls[0].ReduceOnly {
		t.Fatalf("expected non-reduce open order after unblock")
	}
}

func TestStopControllerSingleWriterAppliesUpdates(t *testing.T) {
	tr, _, posSpy, cleanup := newSpyTrader(t)
	defer cleanup()

	posSpy.setOpen("LONG", 1, 100, 0, 0)
	tr.State.PositionState = models.PositionStateOpen
	intent := models.StopIntent{TradeID: "trade-1", Side: "LONG", Entry: 100, TP: 101, SL: 99, Reason: "test"}
	tr.enqueueStopIntent(intent)

	if got := posSpy.updateCount(); got != 0 {
		t.Fatalf("stop update must not be called directly by enqueue, got %d", got)
	}

	done := make(chan struct{})
	go func() {
		tr.StartStopController()
		close(done)
	}()
	waitFor(t, 500*time.Millisecond, func() bool { return posSpy.updateCount() == 1 }, "single writer apply")

	close(tr.State.StopIntentChan)
	<-done
}

func TestClosingStateDropsStopIntents(t *testing.T) {
	tr, _, posSpy, cleanup := newSpyTrader(t)
	defer cleanup()

	posSpy.setOpen("LONG", 1, 100, 101, 99)
	tr.State.PositionState = models.PositionStateClosing

	done := make(chan struct{})
	go func() {
		tr.StartStopController()
		close(done)
	}()
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "trade-1", Side: "LONG", Entry: 100, TP: 101, SL: 99, Reason: "closing_test"}
	time.Sleep(120 * time.Millisecond)
	if got := posSpy.updateCount(); got != 0 {
		t.Fatalf("no stop updates allowed during CLOSING, got %d", got)
	}

	close(tr.State.StopIntentChan)
	<-done
}

func TestProcessExecutionEventExactlyOnce(t *testing.T) {
	tr, _, posSpy, cleanup := newSpyTrader(t)
	defer cleanup()

	evt := models.ExecutionEvent{
		TradeID:      "trade-1",
		ExecID:       "exec-dup",
		OrderID:      "order-1",
		ExecSide:     "LONG",
		PositionSide: "LONG",
		Qty:          0.1,
		Price:        100,
	}

	if !tr.ProcessExecutionEvent(evt, "ws") {
		t.Fatalf("first execution must be processed")
	}
	if tr.ProcessExecutionEvent(evt, "resync") {
		t.Fatalf("duplicate execution must be ignored")
	}
	if hits := posSpy.hasOpenCount(); hits != 1 {
		t.Fatalf("HandleExecutionFill side effect expected once, HasOpenPosition calls=%d", hits)
	}
}

func TestInvariantBlockPersistsUntilManualClear(t *testing.T) {
	tr, _, posSpy, cleanup := newSpyTrader(t)
	defer cleanup()

	posSpy.setOpen("LONG", 1, 100, 0, 0)
	tr.State.PositionState = models.PositionStateOpen

	done := make(chan struct{})
	go func() {
		tr.StartStopController()
		close(done)
	}()
	tr.State.StopIntentChan <- models.StopIntent{
		TradeID: "trade-1",
		Side:    "LONG",
		Entry:   100,
		TP:      99,  // invalid
		SL:      101, // invalid
		Reason:  "invalid_invariant",
	}
	waitFor(t, 500*time.Millisecond, func() bool {
		blocked, reason, _ := tr.isTradingBlocked()
		return blocked && reason == models.BlockedReasonInvariant
	}, "invariant block set")

	if tr.ClearTradingBlock(models.BlockedReasonReconnect, "resync_complete") {
		t.Fatalf("invariant block must not be auto-cleared by reconnect clear")
	}
	blocked, reason, _ := tr.isTradingBlocked()
	if !blocked || reason != models.BlockedReasonInvariant {
		t.Fatalf("expected invariant block to persist, blocked=%t reason=%s", blocked, reason)
	}
	if !tr.ClearTradingBlockManual("operator_ack") {
		t.Fatalf("manual clear must release invariant block")
	}
	blocked, _, _ = tr.isTradingBlocked()
	if blocked {
		t.Fatalf("block must be cleared after manual ack")
	}

	close(tr.State.StopIntentChan)
	<-done
}

func TestSingleWriterCallSiteOnlyInStopController(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	re := regexp.MustCompile(`\.UpdatePositionTPSL\s*\(`)
	count := 0
	var files []string

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", path, err)
		}
		matches := re.FindAll(data, -1)
		if len(matches) > 0 {
			count += len(matches)
			files = append(files, e.Name())
		}
	}

	if count != 1 {
		t.Fatalf("expected exactly one UpdatePositionTPSL callsite in strategy package, got %d files=%v", count, files)
	}
	if len(files) != 1 || files[0] != "fsm_stop.go" {
		t.Fatalf("single writer callsite must be in fsm_stop.go, got files=%v", files)
	}
}
