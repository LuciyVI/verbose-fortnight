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
	"strings"
	"sync"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/order"
)

type independentExchangeOrder struct {
	ID, Link, Kind, Side                string
	Qty                                 float64
	Reduce                              bool
	Accepted, CancelRequested, Terminal bool
	Status                              string
	Fills                               map[string]float64
}

func (o *independentExchangeOrder) filled() float64 {
	n := 0.0
	for _, v := range o.Fills {
		n += v
	}
	return n
}

type independentExchange struct {
	mu                                                sync.Mutex
	Orders                                            []*independentExchangeOrder
	Events                                            []string
	Initial, During, AfterAck                         float64
	RejectCancel, LostCancel, HoldCancel, Unconfirmed bool
	QueriesAfterCancel                                int
	Reordered                                         bool
	OldLiveAfterCancel                                bool
	position                                          *independentPosition
	release                                           chan struct{}
}

func (e *independentExchange) fill(o *independentExchangeOrder, id string, q float64) {
	if q <= 0 {
		return
	}
	if _, ok := o.Fills[id]; ok {
		e.Events = append(e.Events, "duplicate "+id)
		return
	}
	o.Fills[id] = q
	e.Events = append(e.Events, fmt.Sprintf("fill %s %g", id, q))
	if o.filled() == o.Qty {
		o.Terminal = true
		o.Status = "Filled"
	} else {
		o.Status = "PartiallyFilled"
	}
}
func (e *independentExchange) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	var response string
	switch r.URL.Path {
	case "/v5/order/create":
		var body map[string]any
		json.Unmarshal(raw, &body)
		q, _ := strconv.ParseFloat(fmt.Sprint(body["qty"]), 64)
		o := &independentExchangeOrder{ID: fmt.Sprintf("ind-%d", len(e.Orders)+1), Link: fmt.Sprint(body["orderLinkId"]), Kind: fmt.Sprint(body["orderType"]), Side: fmt.Sprint(body["side"]), Qty: q, Accepted: true, Status: "New", Fills: map[string]float64{}}
		o.Reduce, _ = body["reduceOnly"].(bool)
		e.Orders = append(e.Orders, o)
		e.Events = append(e.Events, fmt.Sprintf("accept %s %s %g", o.ID, o.Kind, o.Qty))
		if o.Kind == "Limit" {
			e.fill(o, "initial", e.Initial)
			e.fill(o, "initial", e.Initial)
			e.position.mu.Lock()
			if o.Reduce {
				e.position.qty = 99
			} else {
				e.position.qty = 101
			}
			e.position.mu.Unlock()
		} else {
			e.fill(o, "market", q)
		}
		response = fmt.Sprintf(`{"retCode":0,"result":{"orderId":%q}}`, o.ID)
	case "/v5/order/cancel", "/v5/order/cancel-all":
		o := e.Orders[0]
		o.CancelRequested = true
		e.Events = append(e.Events, "cancel request "+o.ID)
		if e.RejectCancel {
			response = `{"retCode":10001,"retMsg":"cancel refused"}`
			break
		}
		e.fill(o, "during-cancel", e.During)
		if !e.Unconfirmed && e.AfterAck == 0 && !o.Terminal {
			o.Terminal = true
			o.Status = "Cancelled"
			e.Events = append(e.Events, "terminal "+o.ID)
		}
		if e.HoldCancel {
			e.mu.Unlock()
			select {
			case <-r.Context().Done():
			case <-e.release:
			}
			return
		}
		if e.LostCancel {
			e.mu.Unlock()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		e.Events = append(e.Events, "cancel ACK "+o.ID)
		response = `{"retCode":0,"result":{}}`
	case "/v5/order/realtime", "/v5/order/history":
		var o *independentExchangeOrder
		for _, candidate := range e.Orders {
			if candidate.ID == r.URL.Query().Get("orderId") || candidate.Link == r.URL.Query().Get("orderLinkId") {
				o = candidate
				break
			}
		}
		if o == nil {
			response = `{"retCode":0,"result":{"list":[]}}`
			break
		}
		if o.CancelRequested && e.AfterAck > 0 {
			e.QueriesAfterCancel++
			if e.QueriesAfterCancel == 1 {
				e.fill(o, "after-ACK", e.AfterAck)
			} else if !o.Terminal {
				o.Terminal = true
				o.Status = "Cancelled"
				e.Events = append(e.Events, "terminal after ACK "+o.ID)
			}
		}
		cumulative := o.filled()
		status := o.Status
		if e.OldLiveAfterCancel && o.CancelRequested {
			cumulative, status = 0, "New"
			e.OldLiveAfterCancel = false
			e.Events = append(e.Events, "old live snapshot after cancel ACK")
		}
		// Return an older live snapshot once, without changing the exchange's own ledger.
		if e.Reordered && !o.CancelRequested && cumulative > 0 {
			cumulative = 0
			status = "New"
			e.Reordered = false
			e.Events = append(e.Events, "delayed old live snapshot")
		}
		response = fmt.Sprintf(`{"retCode":0,"result":{"list":[{"orderId":%q,"orderLinkId":%q,"orderStatus":%q,"qty":%q,"cumExecQty":%q}]}}`, o.ID, o.Link, status, strconv.FormatFloat(o.Qty, 'f', -1, 64), strconv.FormatFloat(cumulative, 'f', -1, 64))
	default:
		response = `{"retCode":0,"result":{"list":[]}}`
	}
	e.mu.Unlock()
	fmt.Fprint(w, response)
}
func (e *independentExchange) oracle() (filled, potential float64) {
	for _, o := range e.Orders {
		filled += o.filled()
		if o.Accepted && !o.Terminal {
			potential += o.Qty - o.filled()
		}
	}
	return
}

func TestIndependentMakerExposureOracle(t *testing.T) {
	for _, reduce := range []bool{false, true} {
		for _, tc := range []struct {
			name                                                       string
			initial, during, after                                     float64
			reject, lost, hold, unconfirmed, reorder, before, oldAfter bool
		}{
			{name: "no_fill"}, {name: "fill_1", initial: 1}, {name: "fill_4", initial: 4}, {name: "fill_10", initial: 10},
			{name: "fill_before_cancel", initial: 1, before: true},
			{name: "fill_during_cancel", initial: 1, during: 3},
			{name: "full_during_cancel", initial: 1, during: 9},
			{name: "fill_after_ACK", initial: 1, after: 3},
			{name: "full_after_ACK", initial: 1, after: 9},
			{name: "duplicates_and_old_snapshot", initial: 4, reorder: true},
			{name: "old_live_after_cancel", initial: 4, oldAfter: true},
			{name: "cancel_refused", initial: 1, reject: true, unconfirmed: true},
			{name: "cancel_response_lost_terminal", initial: 4, lost: true},
			{name: "cancel_response_lost_live", initial: 1, lost: true, unconfirmed: true},
			{name: "cancel_timeout", initial: 1, hold: true, unconfirmed: true},
		} {
			t.Run(fmt.Sprintf("reduce=%t/%s", reduce, tc.name), func(t *testing.T) {
				tr, pm, log := independentTrader()
				pm.qty = 100
				exchange := &independentExchange{Initial: tc.initial, During: tc.during, AfterAck: tc.after, RejectCancel: tc.reject, LostCancel: tc.lost, HoldCancel: tc.hold, Unconfirmed: tc.unconfirmed, Reordered: tc.reorder, OldLiveAfterCancel: tc.oldAfter, position: pm, release: make(chan struct{})}
				srv := httptest.NewServer(exchange)
				defer srv.Close()
				defer close(exchange.release)
				tr.Config.DemoRESTHost = srv.URL
				tr.APIClient = api.NewRESTClient(tr.Config, log)
				tr.OrderManager = order.NewOrderManager(tr.APIClient, tr.Config, tr.State, log)
				if tc.before {
					log.hook = func(line string) {
						if strings.HasPrefix(line, "maker_first_timeout ") {
							exchange.mu.Lock()
							exchange.fill(exchange.Orders[0], "just-before-cancel", 3)
							exchange.mu.Unlock()
						}
					}
				}
				side := "Buy"
				if reduce {
					side = "Sell"
				}
				_, _, err := tr.placeOrderWithExecutionPolicy(side, 10, reduce, "B", "entry")
				exchange.mu.Lock()
				defer exchange.mu.Unlock()
				filled, potential := exchange.oracle()
				t.Logf("filled=%g potential=%g orders=%d events=%v err=%v", filled, potential, len(exchange.Orders), exchange.Events, err)
				if filled+potential > 10+1e-12 {
					t.Errorf("volume contract breached: executed %g + executable remainder %g > requested 10", filled, potential)
				}
				for _, o := range exchange.Orders {
					if o.Reduce != reduce {
						t.Error("reduceOnly lost")
					}
				}
				uncertain := tc.unconfirmed
				if uncertain {
					if err == nil || len(exchange.Orders) != 1 {
						t.Errorf("unknown cancellation must reconcile without fallback: err=%v orders=%d", err, len(exchange.Orders))
					}
					flag := reflect.ValueOf(tr.State).Elem().FieldByName("OrderReconciliationPending")
					if flag.IsValid() && !flag.Bool() {
						t.Error("missing explicit reconciliation state")
					}
				} else {
					if err != nil || math.Abs(filled-10) > 1e-12 {
						t.Errorf("confirmed remainder not filled: qty=%g err=%v", filled, err)
					}
					if len(exchange.Orders) > 1 {
						maker, market := exchange.Orders[0], exchange.Orders[1]
						if !maker.Terminal || math.Abs(market.Qty-(10-maker.filled())) > 1e-12 {
							t.Errorf("fallback did not use confirmed final remainder: maker=%+v market=%+v", maker, market)
						}
					}
				}
			})
		}
	}
}
