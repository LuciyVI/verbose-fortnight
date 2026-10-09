package strategy

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/order"
)

type pendingReply struct {
	state api.OrderState
	err   error
}
type pendingQuery struct {
	id, link string
	reply    chan pendingReply
}
type pendingVenue struct{ queries chan pendingQuery }

func (v *pendingVenue) PlaceOrderMarketWithID(string, float64, bool) (string, error) {
	return "", fmt.Errorf("reconciliation attempted create")
}
func (v *pendingVenue) PlaceTakeProfitOrder(string, float64, float64) error {
	return fmt.Errorf("reconciliation attempted create")
}
func (v *pendingVenue) CancelOrder(context.Context, string, string) error {
	return fmt.Errorf("reconciliation attempted mutation")
}
func (v *pendingVenue) QueryOrder(ctx context.Context, id, link string) (api.OrderState, error) {
	q := pendingQuery{id, link, make(chan pendingReply, 1)}
	select {
	case v.queries <- q:
	case <-ctx.Done():
		return api.OrderState{}, ctx.Err()
	}
	select {
	case reply := <-q.reply:
		return reply.state, reply.err
	case <-ctx.Done():
		return api.OrderState{}, ctx.Err()
	}
}
func newPendingVenueTrader() (*Trader, *pendingVenue) {
	tr, _, _ := independentTrader()
	v := &pendingVenue{queries: make(chan pendingQuery, 8)}
	tr.OrderManager = v
	return tr, v
}
func registerPending(tr *Trader, id, link string) uint64 {
	tr.noteUncertainOrder(id, link, &order.UncertainOrderError{OrderID: id, OrderLinkID: link, Cause: fmt.Errorf("accepted response lost")})
	tr.State.RLock()
	defer tr.State.RUnlock()
	return tr.State.PendingOrderGeneration
}
func beginPendingQuery(tr *Trader, generation uint64) <-chan error {
	done := make(chan error, 1)
	go func() { _, err := tr.ReconcilePendingOrderGeneration(context.Background(), generation); done <- err }()
	return done
}
func nextPendingQuery(t *testing.T, v *pendingVenue) pendingQuery {
	t.Helper()
	select {
	case q := <-v.queries:
		return q
	case <-time.After(time.Second):
		t.Fatal("pending query not reached")
	}
	return pendingQuery{}
}
func pendingResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("pending query did not return")
	}
	return nil
}
func terminalPending(q pendingQuery) pendingReply {
	return pendingReply{state: api.OrderState{OrderID: q.id, OrderLinkID: q.link, Status: "Cancelled", Qty: 10}}
}
func requirePending(t *testing.T, tr *Trader, g uint64, id string, count int) {
	t.Helper()
	tr.State.RLock()
	pending, gen, current, blocked, n := tr.State.OrderReconciliationPending, tr.State.PendingOrderGeneration, tr.State.PendingOrderID, tr.State.TradingBlocked, len(tr.State.PendingOrders)
	tr.State.RUnlock()
	if !pending || gen != g || current != id || !blocked || n != count || tr.ClearTradingBlockManual("must-not-clear") {
		t.Fatalf("uncertainty lost: pending=%t generation=%d id=%s blocked=%t count=%d", pending, gen, current, blocked, n)
	}
}

func TestPendingGenerationReplacementRetainsAll(t *testing.T) {
	tr, v := newPendingVenueTrader()
	a := registerPending(tr, "A", "link-A")
	done := beginPendingQuery(tr, a)
	q := nextPendingQuery(t, v)
	b := registerPending(tr, "B", "link-B")
	requirePending(t, tr, b, "B", 2)
	rows := tr.State.PendingOrdersSnapshot()
	if len(rows) != 2 || rows[0].OrderID != "A" || rows[1].OrderID != "B" {
		t.Fatalf("A disappeared on registration of B: %v", rows)
	}
	q.reply <- terminalPending(q)
	if err := pendingResult(t, done); err != nil {
		t.Fatal(err)
	}
	requirePending(t, tr, b, "B", 1)
	if _, err := tr.ReconcilePendingOrderGeneration(context.Background(), a); !errors.Is(err, ErrStaleReconciliation) {
		t.Fatalf("repeated A result not stale: %v", err)
	}
	select {
	case extra := <-v.queries:
		t.Fatalf("stale generation queried another order: %v", extra)
	default:
	}
	done = beginPendingQuery(tr, b)
	q = nextPendingQuery(t, v)
	q.reply <- pendingReply{state: api.OrderState{OrderID: q.id, OrderLinkID: q.link, Status: "New", Qty: 10}}
	if err := pendingResult(t, done); !errors.Is(err, order.ErrOrderUncertain) {
		t.Fatalf("live B accepted as terminal: %v", err)
	}
	requirePending(t, tr, b, "B", 1)
}

func TestPendingSameIDAndRepeatedTerminalGenerations(t *testing.T) {
	tr, v := newPendingVenueTrader()
	a := registerPending(tr, "same", "same-link")
	done1, done2 := beginPendingQuery(tr, a), beginPendingQuery(tr, a)
	q1, q2 := nextPendingQuery(t, v), nextPendingQuery(t, v)
	b := registerPending(tr, "same", "same-link")
	if b <= a {
		t.Fatal("pending generation reused")
	}
	q1.reply <- terminalPending(q1)
	q2.reply <- terminalPending(q2)
	err1, err2 := pendingResult(t, done1), pendingResult(t, done2)
	if !((err1 == nil && errors.Is(err2, ErrStaleReconciliation)) || (err2 == nil && errors.Is(err1, ErrStaleReconciliation))) {
		t.Fatalf("duplicate replies: first=%v second=%v", err1, err2)
	}
	requirePending(t, tr, b, "same", 1)
	done := beginPendingQuery(tr, b)
	q := nextPendingQuery(t, v)
	q.reply <- pendingReply{state: api.OrderState{OrderID: q.id, OrderLinkID: q.link, Status: "New", Qty: 10}}
	if err := pendingResult(t, done); !errors.Is(err, order.ErrOrderUncertain) {
		t.Fatal(err)
	}
	requirePending(t, tr, b, "same", 1)
}

func TestPendingOlderUnresolvedSurvivesNewerTerminal(t *testing.T) {
	tr, v := newPendingVenueTrader()
	a := registerPending(tr, "A", "link-A")
	b := registerPending(tr, "B", "link-B")
	done := beginPendingQuery(tr, b)
	q := nextPendingQuery(t, v)
	q.reply <- terminalPending(q)
	if err := pendingResult(t, done); err != nil {
		t.Fatal(err)
	}
	requirePending(t, tr, a, "A", 1)
	done = beginPendingQuery(tr, a)
	q = nextPendingQuery(t, v)
	q.reply <- terminalPending(q)
	if err := pendingResult(t, done); err != nil {
		t.Fatal(err)
	}
	tr.State.RLock()
	pending, blocked, count := tr.State.OrderReconciliationPending, tr.State.TradingBlocked, len(tr.State.PendingOrders)
	tr.State.RUnlock()
	if pending || !blocked || count != 0 {
		t.Fatalf("terminal handling bypassed operator acknowledgement: pending=%t blocked=%t count=%d", pending, blocked, count)
	}
	if !tr.ClearTradingBlockManual("all orders terminal; position/stops reconciled") {
		t.Fatal("manual clear denied after all uncertainties resolved")
	}
	c := registerPending(tr, "A", "link-A")
	if c <= b {
		t.Fatal("completed identity reused its old generation")
	}
	requirePending(t, tr, c, "A", 1)
}

func TestPendingErrorsCancellationAndTimeoutRetainRecords(t *testing.T) {
	for _, kind := range []string{"error", "not_found", "cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			tr, v := newPendingVenueTrader()
			a := registerPending(tr, "A", "link-A")
			ctx, cancel := context.WithCancel(context.Background())
			if kind == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := tr.ReconcilePendingOrderGeneration(ctx, a); done <- err }()
			q := nextPendingQuery(t, v)
			b := registerPending(tr, "B", "link-B")
			before := tr.State.PendingOrdersSnapshot()
			switch kind {
			case "error":
				q.reply <- pendingReply{err: fmt.Errorf("query failed")}
			case "not_found":
				q.reply <- pendingReply{err: api.ErrOrderNotFound}
			case "cancel":
				cancel()
			}
			if err := pendingResult(t, done); err == nil {
				t.Fatal("query failure reported success")
			}
			requirePending(t, tr, b, "B", 2)
			if after := tr.State.PendingOrdersSnapshot(); !reflect.DeepEqual(before, after) {
				t.Fatalf("query error changed records: before=%v after=%v", before, after)
			}
		})
	}
}

func TestPendingRejectsWrongQueryIdentity(t *testing.T) {
	tr, v := newPendingVenueTrader()
	a := registerPending(tr, "A", "link-A")
	done := beginPendingQuery(tr, a)
	q := nextPendingQuery(t, v)
	q.reply <- pendingReply{state: api.OrderState{OrderID: "other", OrderLinkID: "other-link", Status: "Filled", Qty: 10, CumExecQty: 10}}
	if err := pendingResult(t, done); err == nil {
		t.Fatal("another order's terminal state accepted")
	}
	requirePending(t, tr, a, "A", 1)
}

func TestManualClearChecksRetainedUncertaintyRecords(t *testing.T) {
	tr, _ := newPendingVenueTrader()
	registerPending(tr, "A", "link-A")
	tr.State.Lock()
	tr.State.OrderReconciliationPending = false
	tr.State.Unlock()
	if tr.ClearTradingBlockManual("inconsistent compatibility flag") {
		t.Fatal("retained unresolved record bypassed manual-clear guard")
	}
	tr.State.RLock()
	blocked := tr.State.TradingBlocked
	tr.State.RUnlock()
	if !blocked {
		t.Fatal("manual clear unblocked retained uncertainty")
	}
}
