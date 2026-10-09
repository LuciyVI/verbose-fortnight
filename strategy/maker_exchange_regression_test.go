package strategy

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
	"verbose-fortnight/config"

	"verbose-fortnight/api"
	"verbose-fortnight/order"
)

type journalOrder struct {
	id, link, side, kind, status string
	qty                          float64
	reduceOnly                   bool
	executions                   map[string]float64
}

func (o *journalOrder) filled() float64 {
	sum := 0.0
	for _, qty := range o.executions {
		sum += qty
	}
	return sum
}

type journalExchange struct {
	timeoutCancel                                         bool
	mu                                                    sync.Mutex
	orders                                                []*journalOrder
	initialFill, duringCancelFill, afterCancelFill        float64
	rejectCancel, loseCancel, holdCancel, regressSnapshot bool
	cancelCount                                           int
	queryCount                                            int
}

func (e *journalExchange) execute(o *journalOrder, id string, qty float64) {
	if qty <= 0 {
		return
	}
	if _, duplicate := o.executions[id]; duplicate {
		return
	}
	o.executions[id] = qty
	if math.Abs(o.filled()-o.qty) < 1e-12 {
		o.status = "Filled"
	} else {
		o.status = "PartiallyFilled"
	}
}
func (e *journalExchange) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch r.URL.Path {
	case "/v5/order/create":
		var body struct {
			Side   string `json:"side"`
			Kind   string `json:"orderType"`
			Qty    string `json:"qty"`
			Link   string `json:"orderLinkId"`
			Reduce bool   `json:"reduceOnly"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(400)
			return
		}
		qty, _ := strconv.ParseFloat(body.Qty, 64)
		o := &journalOrder{id: fmt.Sprintf("o%d", len(e.orders)+1), link: body.Link, side: body.Side, kind: body.Kind, qty: qty, reduceOnly: body.Reduce, status: "New", executions: map[string]float64{}}
		e.orders = append(e.orders, o)
		if body.Kind == "Limit" {
			e.execute(o, "fill-before-cancel", e.initialFill)
			e.execute(o, "fill-before-cancel", e.initialFill) // duplicate event: exchange cumulative total must not double
		} else {
			e.execute(o, "market-fill", qty)
		}
		fmt.Fprintf(w, `{"retCode":0,"result":{"orderId":%q}}`, o.id)
	case "/v5/order/cancel":
		io.Copy(io.Discard, r.Body)
		e.cancelCount++
		if e.timeoutCancel {
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
			return
		}
		o := e.orders[0]
		if e.rejectCancel {
			fmt.Fprint(w, `{"retCode":10001,"retMsg":"cancel rejected"}`)
			return
		}
		e.execute(o, "fill-during-cancel", e.duringCancelFill)
		if !e.holdCancel && o.status != "Filled" {
			o.status = "Cancelled"
		}
		if e.loseCancel {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		fmt.Fprint(w, `{"retCode":0,"result":{}}`)
	case "/v5/order/realtime", "/v5/order/history":
		e.queryCount++
		var o *journalOrder
		for _, candidate := range e.orders {
			if candidate.id == r.URL.Query().Get("orderId") || candidate.link == r.URL.Query().Get("orderLinkId") {
				o = candidate
				break
			}
		}
		if o == nil {
			fmt.Fprint(w, `{"retCode":0,"result":{"list":[]}}`)
			return
		}
		if e.cancelCount > 0 && e.afterCancelFill > 0 {
			// Fill matching was committed after cancel ACK but before terminality.
			e.execute(o, "fill-after-ack", e.afterCancelFill)
			e.afterCancelFill = 0
			if o.status != "Filled" {
				o.status = "Cancelled"
			}
		}
		cum := o.filled()
		status := o.status
		if e.regressSnapshot && e.cancelCount == 0 && e.queryCount%2 == 0 {
			cum = 0
			status = "New"
		}
		fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"orderId":%q,"orderLinkId":%q,"qty":%q,"cumExecQty":%q,"orderStatus":%q}]}}`, o.id, o.link, strconv.FormatFloat(o.qty, 'f', -1, 64), strconv.FormatFloat(cum, 'f', -1, 64), status)
	default:
		fmt.Fprint(w, `{"retCode":0,"result":{"list":[]}}`)
	}
}

func TestMakerOrderJournalRegressions(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		for _, tc := range []struct {
			name                   string
			initial, during, after float64
			wantOrders             int
			hold, regress          bool
		}{
			{"full", 1, 0, 0, 1, false, false},
			{"partial", 0.4, 0, 0, 2, false, false},
			{"no_fill", 0, 0, 0, 2, false, false},
			{"during_cancel", 0.2, 0.3, 0, 2, false, false},
			{"full_during_cancel", 0.2, 0.8, 0, 1, false, false},
			{"after_cancel_ack", 0.2, 0, 0.3, 2, true, false},
			{"duplicate_reordered", 0.4, 0, 0, 2, false, true},
		} {
			t.Run(fmt.Sprintf("reduce=%t/%s", reduce, tc.name), func(t *testing.T) {
				exchange := &journalExchange{initialFill: tc.initial, duringCancelFill: tc.during, afterCancelFill: tc.after, holdCancel: tc.hold, regressSnapshot: tc.regress}
				srv := httptest.NewServer(exchange)
				defer srv.Close()
				tr, _, pm := newMakerPolicyTrader()
				tr.Config.DemoRESTHost = srv.URL
				tr.Config.MakerTimeoutMs = 30
				setMakerRegressionTimeouts(tr.Config)
				tr.APIClient = api.NewRESTClient(tr.Config, nopLogger{})
				tr.OrderManager = order.NewOrderManager(tr.APIClient, tr.Config, tr.State, nopLogger{})
				// Deliberately misleading total-position changes. They cannot identify this order's fills.
				if reduce {
					pm.snapshots = []positionSnapshot{{exists: true, qty: 1}, {exists: true, qty: 0.6}}
				} else {
					pm.snapshots = []positionSnapshot{{exists: false}, {exists: true, qty: 0.4}}
				}
				side := "Buy"
				if reduce {
					side = "Sell"
				}
				if _, _, err := tr.placeOrderWithExecutionPolicy(side, 1, reduce, "lc-regression", "entry"); err != nil {
					t.Fatal(err)
				}
				exchange.mu.Lock()
				defer exchange.mu.Unlock()
				if len(exchange.orders) != tc.wantOrders {
					t.Fatalf("orders=%d want=%d; maker filled=%g", len(exchange.orders), tc.wantOrders, exchange.orders[0].filled())
				}
				total := 0.0
				for _, o := range exchange.orders {
					total += o.filled()
					if o.reduceOnly != reduce {
						t.Fatal("reduceOnly lost")
					}
				}
				if math.Abs(total-1) > 1e-9 {
					t.Fatalf("journal execution total=%g want 1", total)
				}
				if tc.wantOrders == 2 && (exchange.orders[0].status != "Cancelled" || exchange.cancelCount != 1) {
					t.Fatalf("fallback before terminal cancellation: %+v cancels=%d", exchange.orders[0], exchange.cancelCount)
				}
				if tc.wantOrders == 2 && math.Abs(exchange.orders[1].qty-(1-exchange.orders[0].filled())) > 1e-9 {
					t.Fatal("fallback exceeded order remainder")
				}
			})
		}
	}
}

// The same behavioral test runs on the baseline, whose config lacks the new fields.
func setMakerRegressionTimeouts(cfg *config.Config) {
	for name, value := range map[string]int64{"RESTTimeoutMs": 50, "OrderReconcileTimeoutMs": 100} {
		field := reflect.ValueOf(cfg).Elem().FieldByName(name)
		if field.IsValid() {
			field.SetInt(value)
		}
	}
}
