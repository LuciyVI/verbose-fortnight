package strategy

import (
	"math"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

type makerPolicyOrderManagerStub struct {
	mu sync.Mutex

	marketCalls []makerOrderCall
	limitCalls  []makerOrderCall

	marketOrderID string
	limitOrderID  string
}

type makerOrderCall struct {
	side       string
	qty        float64
	reduceOnly bool
	linkID     string
	price      float64
}

func (s *makerPolicyOrderManagerStub) PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketCalls = append(s.marketCalls, makerOrderCall{side: side, qty: qty, reduceOnly: reduceOnly})
	if s.marketOrderID == "" {
		s.marketOrderID = "mkt-1"
	}
	return s.marketOrderID, nil
}

func (s *makerPolicyOrderManagerStub) PlaceOrderMarketWithLinkID(side string, qty float64, reduceOnly bool, orderLinkID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marketCalls = append(s.marketCalls, makerOrderCall{side: side, qty: qty, reduceOnly: reduceOnly, linkID: orderLinkID})
	if s.marketOrderID == "" {
		s.marketOrderID = "mkt-1"
	}
	return s.marketOrderID, nil
}

func (s *makerPolicyOrderManagerStub) PlaceOrderLimitPostOnlyWithLinkID(side string, qty, price float64, reduceOnly bool, orderLinkID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limitCalls = append(s.limitCalls, makerOrderCall{side: side, qty: qty, reduceOnly: reduceOnly, linkID: orderLinkID, price: price})
	if s.limitOrderID == "" {
		s.limitOrderID = "lim-1"
	}
	return s.limitOrderID, nil
}

func (s *makerPolicyOrderManagerStub) PlaceTakeProfitOrder(side string, qty, price float64) error {
	return nil
}

func (s *makerPolicyOrderManagerStub) marketCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marketCalls)
}

func (s *makerPolicyOrderManagerStub) limitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.limitCalls)
}

func (s *makerPolicyOrderManagerStub) lastMarketCall() makerOrderCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.marketCalls) == 0 {
		return makerOrderCall{}
	}
	return s.marketCalls[len(s.marketCalls)-1]
}

type makerPolicyPositionManagerStub struct {
	mu sync.Mutex

	snapshots []positionSnapshot
	idx       int
	cancelCnt int

	bid float64
	ask float64
}

type positionSnapshot struct {
	exists bool
	side   string
	qty    float64
	tp     float64
	sl     float64
}

func (s *makerPolicyPositionManagerStub) next() positionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshots) == 0 {
		return positionSnapshot{}
	}
	if s.idx >= len(s.snapshots) {
		return s.snapshots[len(s.snapshots)-1]
	}
	v := s.snapshots[s.idx]
	s.idx++
	return v
}

func (s *makerPolicyPositionManagerStub) HasOpenPosition() (bool, string, float64, float64, float64) {
	v := s.next()
	return v.exists, v.side, v.qty, v.tp, v.sl
}

func (s *makerPolicyPositionManagerStub) NormalizeSide(side string) string {
	switch side {
	case "BUY", "Buy", "LONG", "Long", "long":
		return "LONG"
	case "SELL", "Sell", "SHORT", "Short", "short":
		return "SHORT"
	default:
		return ""
	}
}

func (s *makerPolicyPositionManagerStub) GetLastEntryPrice() float64 { return 100 }
func (s *makerPolicyPositionManagerStub) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	return nil
}
func (s *makerPolicyPositionManagerStub) GetLastBidPrice() float64 { return s.bid }
func (s *makerPolicyPositionManagerStub) GetLastAskPrice() float64 { return s.ask }
func (s *makerPolicyPositionManagerStub) CancelAllOrders(symbol string) {
	s.mu.Lock()
	s.cancelCnt++
	s.mu.Unlock()
}
func (s *makerPolicyPositionManagerStub) cancelCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelCnt
}

func newMakerPolicyTrader() (*Trader, *makerPolicyOrderManagerStub, *makerPolicyPositionManagerStub) {
	cfg := config.LoadConfig()
	cfg.EnableLifecycleID = true
	cfg.EnableMakerFirst = true
	cfg.MakerTimeoutMs = 1
	cfg.EdgeGuardSpreadThreshold = 0
	cfg.MakerMaxSlippagePct = 0

	state := &models.State{
		Instr:         models.InstrumentInfo{TickSize: 0.1, QtyStep: 0.001},
		OrderTradeIDs: make(map[string]string),
		ExecTradeIDs:  make(map[string]string),
		ProcessedExec: make(map[string]time.Time),
	}
	om := &makerPolicyOrderManagerStub{}
	pm := &makerPolicyPositionManagerStub{bid: 100, ask: 101}
	tr := &Trader{
		Config:          cfg,
		State:           state,
		OrderManager:    om,
		PositionManager: pm,
		Logger:          nopLogger{},
	}
	return tr, om, pm
}

func TestMakerFirstDisabled_NoBehaviorChange(t *testing.T) {
	tr, om, pm := newMakerPolicyTrader()
	tr.Config.EnableMakerFirst = false
	pm.snapshots = []positionSnapshot{{exists: false, qty: 0}}

	if _, _, err := tr.placeOrderWithExecutionPolicy("Buy", 0.2, false, "lc-1", "entry"); err != nil {
		t.Fatalf("place order: %v", err)
	}
	if om.limitCount() != 0 {
		t.Fatalf("limit calls = %d want 0", om.limitCount())
	}
	if om.marketCount() != 1 {
		t.Fatalf("market calls = %d want 1", om.marketCount())
	}
}

func TestMakerFirstTimeoutFallback(t *testing.T) {
	tr, om, pm := newMakerPolicyTrader()
	pm.snapshots = []positionSnapshot{
		{exists: false, qty: 0}, // before
		{exists: false, qty: 0}, // immediate check
	}

	if _, _, err := tr.placeOrderWithExecutionPolicy("Buy", 0.2, false, "lc-1", "entry"); err != nil {
		t.Fatalf("place order: %v", err)
	}
	if om.limitCount() != 1 {
		t.Fatalf("limit calls = %d want 1", om.limitCount())
	}
	if om.marketCount() != 1 {
		t.Fatalf("market calls = %d want 1", om.marketCount())
	}
	if pm.cancelCount() == 0 {
		t.Fatalf("expected cancel-all on timeout")
	}
}

func TestMakerFirstPartialFill(t *testing.T) {
	tr, om, pm := newMakerPolicyTrader()
	pm.snapshots = []positionSnapshot{
		{exists: true, side: "LONG", qty: 1.0}, // before
		{exists: true, side: "LONG", qty: 0.6}, // immediate check => partial 0.4
	}

	if _, _, err := tr.placeOrderWithExecutionPolicy("Sell", 1.0, true, "lc-1", "clos"); err != nil {
		t.Fatalf("place reduce order: %v", err)
	}
	if om.marketCount() != 1 {
		t.Fatalf("market calls = %d want 1", om.marketCount())
	}
	gotQty := om.lastMarketCall().qty
	if math.Abs(gotQty-0.6) > 1e-9 {
		t.Fatalf("fallback market qty = %.6f want 0.6", gotQty)
	}
}

func TestMakerMetricsIncrement(t *testing.T) {
	tr, _, pm := newMakerPolicyTrader()
	pm.snapshots = []positionSnapshot{
		{exists: false, qty: 0},
		{exists: false, qty: 0},
	}

	if _, _, err := tr.placeOrderWithExecutionPolicy("Buy", 0.2, false, "lc-1", "entry"); err != nil {
		t.Fatalf("place order: %v", err)
	}
	counters, _ := tr.State.RuntimeSnapshot()
	if counters.MakerTimeoutCount != 1 {
		t.Fatalf("maker timeout count = %d want 1", counters.MakerTimeoutCount)
	}
	if counters.MakerFallbackCount != 1 {
		t.Fatalf("maker fallback count = %d want 1", counters.MakerFallbackCount)
	}
}

func TestMakerPolicyDoesNotAffectDefaults(t *testing.T) {
	cfg := config.LoadConfig()
	if cfg.EnableMakerFirst {
		t.Fatalf("default ENABLE_MAKER_FIRST must be false")
	}
}
