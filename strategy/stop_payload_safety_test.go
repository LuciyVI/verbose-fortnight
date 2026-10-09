package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
	"verbose-fortnight/position"
)

type stopWireVenue struct {
	mu               sync.Mutex
	reads            int
	first, final     api.Position
	reached, release chan struct{}
	journal          []map[string]any
	response         string
}

func (v *stopWireVenue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v5/position/list" {
		v.mu.Lock()
		v.reads++
		n := v.reads
		row := v.first
		v.mu.Unlock()
		if n == 2 && v.reached != nil {
			close(v.reached)
			select {
			case <-v.release:
			case <-r.Context().Done():
				return
			}
		}
		if n >= 2 {
			v.mu.Lock()
			row = v.final
			v.mu.Unlock()
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "result": map[string]any{"list": []api.Position{row}}})
		return
	}
	if r.URL.Path != "/v5/position/trading-stop" {
		http.Error(w, "unexpected path", 500)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	v.mu.Lock()
	v.journal = append(v.journal, body)
	reply := v.response
	v.mu.Unlock()
	if reply == "" {
		reply = `{"retCode":0}`
	}
	fmt.Fprint(w, reply)
}
func stopWireTrader(t *testing.T, v *stopWireVenue, side string, tick float64) *Trader {
	t.Helper()
	srv := httptest.NewServer(v)
	t.Cleanup(srv.Close)
	tr, _, log := independentTrader()
	tr.Config.DemoRESTHost = srv.URL
	tr.Config.RESTTimeoutMs = 1000
	tr.State.PosSide = side
	tr.State.Instr.TickSize = tick
	tr.State.StopCtrl = models.StopController{TradeID: "B", AppliedTradeID: "B", DesiredTP: 130, DesiredSL: 70, AppliedTP: 130, AppliedSL: 70}
	tr.APIClient = api.NewRESTClient(tr.Config, log)
	tr.PositionManager = position.NewPositionManager(tr.APIClient, tr.Config, tr.State, log)
	return tr
}
func wireRow(side string) api.Position {
	return api.Position{Symbol: "BTCUSDT", PositionIdx: 0, Side: side, Size: "10", AvgPrice: "100"}
}

func TestStopFinalObservationMatrix(t *testing.T) {
	for _, name := range []string{"LONG_to_SHORT", "SHORT_to_LONG", "closed", "wrong_symbol", "wrong_positionIdx", "entry_changed", "local_direction_change", "lifecycle_change", "unchanged_LONG", "unchanged_SHORT"} {
		t.Run(name, func(t *testing.T) {
			side, remote, tp, sl := "LONG", "Buy", 110.0, 90.0
			if name == "SHORT_to_LONG" || name == "unchanged_SHORT" {
				side, remote, tp, sl = "SHORT", "Sell", 90, 110
			}
			v := &stopWireVenue{first: wireRow(remote), final: wireRow(remote), reached: make(chan struct{}), release: make(chan struct{})}
			tr := stopWireTrader(t, v, side, 0.1)
			before := tr.State.StopCtrl
			tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: side, Entry: 100, TP: tp, SL: sl}
			close(tr.State.StopIntentChan)
			done := make(chan struct{})
			go func() { tr.StartStopController(); close(done) }()
			independentWait(t, v.reached, "final REST observation")
			// The global state mutex must stay available during the final GET.
			locked := make(chan struct{})
			go func() { tr.State.Lock(); tr.State.Unlock(); close(locked) }()
			independentWait(t, locked, "state lock during GET")
			tr.State.RLock()
			observed := tr.State.StopCtrl
			tr.State.RUnlock()
			if !reflect.DeepEqual(before, observed) {
				t.Errorf("unvalidated intent changed Desired/Applied: %+v", observed)
			}
			v.mu.Lock()
			switch name {
			case "LONG_to_SHORT":
				v.final.Side = "Sell"
			case "SHORT_to_LONG":
				v.final.Side = "Buy"
			case "closed":
				v.final.Size = "0"
			case "wrong_symbol":
				v.final.Symbol = "OTHERUSDT"
			case "wrong_positionIdx":
				v.final.PositionIdx = 1
			case "entry_changed":
				v.final.AvgPrice = "115"
			}
			v.mu.Unlock()
			if name == "local_direction_change" {
				tr.State.Lock()
				tr.State.PosSide = "SHORT"
				tr.State.Unlock()
			}
			if name == "lifecycle_change" {
				tr.setActiveTradeID("C")
				before = tr.State.StopCtrl
			}
			close(v.release)
			independentWait(t, done, "controller after final observation")
			want := 0
			if name == "unchanged_LONG" || name == "unchanged_SHORT" {
				want = 1
			}
			v.mu.Lock()
			calls := len(v.journal)
			v.mu.Unlock()
			if calls != want {
				t.Errorf("POST journal calls=%d want=%d", calls, want)
			}
			if want == 0 && !reflect.DeepEqual(before, tr.State.StopCtrl) {
				t.Errorf("rejected observation mutated controller: %+v", tr.State.StopCtrl)
			}
			if want == 1 && (tr.State.StopCtrl.AppliedTP != tp || tr.State.StopCtrl.AppliedSL != sl || tr.State.StopCtrl.AppliedTradeID != "B") {
				t.Errorf("valid current stop not applied: %+v", tr.State.StopCtrl)
			}
		})
	}
}

// This oracle uses only the JSON received by the venue, decimal rational math,
// and the stated contract. It does not call the production price formatter.
func assertStopWireOracle(t *testing.T, body map[string]any, side string, entry, tp, sl, tick float64) (float64, float64) {
	t.Helper()
	if body["symbol"] != "BTCUSDT" || body["positionIdx"] != float64(0) {
		t.Fatalf("wrong target: %v", body)
	}
	step, _ := new(big.Rat).SetString(strconv.FormatFloat(tick, 'f', -1, 64))
	values := make([]float64, 2)
	for i, key := range []string{"takeProfit", "stopLoss"} {
		text, ok := body[key].(string)
		if !ok {
			t.Fatalf("non-string wire price: %v", body)
		}
		wire, ok := new(big.Rat).SetString(text)
		if !ok || !new(big.Rat).Quo(wire, step).IsInt() {
			t.Fatalf("wire price %s not multiple of %g", text, tick)
		}
		requested := []float64{tp, sl}[i]
		input, _ := new(big.Rat).SetString(strconv.FormatFloat(requested, 'f', -1, 64))
		up := (side == "LONG" && key == "stopLoss") || (side == "SHORT" && key == "takeProfit")
		comparison := wire.Cmp(input)
		if (up && comparison < 0) || (!up && comparison > 0) {
			t.Fatalf("rounding violates stated policy: %s request=%g", text, requested)
		}
		distance := new(big.Rat).Sub(wire, input)
		distance.Abs(distance)
		if distance.Cmp(step) >= 0 {
			t.Fatalf("rounding lost a whole tick: %s request=%g", text, requested)
		}
		values[i], _ = strconv.ParseFloat(text, 64)
	}
	if (side == "LONG" && !(values[0] > entry && values[1] < entry)) || (side == "SHORT" && !(values[0] < entry && values[1] > entry)) {
		t.Fatalf("wire invariants lost: entry=%g payload=%v", entry, body)
	}
	return values[0], values[1]
}

func TestStopPayloadTickOracleAndCache(t *testing.T) {
	for _, side := range []string{"LONG", "SHORT"} {
		for _, tick := range []float64{1, 0.1, 0.001, 0.25} {
			for _, boundary := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/tick=%g/boundary=%t", side, tick, boundary), func(t *testing.T) {
					remote, tp, sl := "Buy", 100+4.4*tick, 100-4.4*tick
					if side == "SHORT" {
						remote, tp, sl = "Sell", sl, tp
					}
					if boundary {
						if side == "LONG" {
							tp = math.Nextafter(100+4*tick, math.Inf(1))
							sl = math.Nextafter(100-4*tick, 0)
						} else {
							tp = math.Nextafter(100-4*tick, 0)
							sl = math.Nextafter(100+4*tick, math.Inf(1))
						}
					}
					v := &stopWireVenue{first: wireRow(remote), final: wireRow(remote)}
					tr := stopWireTrader(t, v, side, tick)
					tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: side, Entry: 100, TP: tp, SL: sl}
					independentController(t, tr)
					if len(v.journal) != 1 {
						t.Fatalf("wire journal=%v", v.journal)
					}
					wireTP, wireSL := assertStopWireOracle(t, v.journal[0], side, 100, tp, sl, tick)
					ctrl := tr.State.StopCtrl
					if ctrl.AppliedTP != wireTP || ctrl.AppliedSL != wireSL || ctrl.DesiredTP != wireTP || ctrl.DesiredSL != wireSL || ctrl.AppliedTradeID != "B" {
						t.Fatalf("cache differs from acknowledged payload: %+v wire=%v", ctrl, v.journal)
					}
				})
			}
		}
	}
}

func TestStopRejectedPayloadAndResponsePreserveCache(t *testing.T) {
	for _, tc := range []struct {
		name, side, response string
		tp, sl, tick         float64
		wantPosts            int
	}{
		{"LONG_TP_collapses", "LONG", "", 100.01, 99.8, 0.1, 0},
		{"LONG_SL_collapses", "LONG", "", 101, 99.999, 0.1, 0},
		{"SHORT_TP_collapses", "SHORT", "", 99.99, 100.2, 0.1, 0},
		{"SHORT_SL_collapses", "SHORT", "", 99, 100.001, 0.1, 0},
		{"TP_equals_entry", "LONG", "", 100, 99, 0.1, 0},
		{"tick_zero", "LONG", "", 110, 90, 0, 0},
		{"rejection", "LONG", `{"retCode":10001,"retMsg":"rejected"}`, 100.49, 99.51, 0.25, 1},
		{"malformed_reply", "LONG", `{broken`, 100.49, 99.51, 0.25, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remote := "Buy"
			if tc.side == "SHORT" {
				remote = "Sell"
			}
			v := &stopWireVenue{first: wireRow(remote), final: wireRow(remote), response: tc.response}
			tr := stopWireTrader(t, v, tc.side, tc.tick)
			before := tr.State.StopCtrl
			tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: tc.side, Entry: 100, TP: tc.tp, SL: tc.sl}
			independentController(t, tr)
			if len(v.journal) != tc.wantPosts || !reflect.DeepEqual(before, tr.State.StopCtrl) {
				t.Fatalf("rejection changed state/journal: before=%+v after=%+v journal=%v", before, tr.State.StopCtrl, v.journal)
			}
		})
	}
}

func TestStopFinalReadCancelsOnLifecycleChange(t *testing.T) {
	v := &stopWireVenue{first: wireRow("Buy"), final: wireRow("Buy"), reached: make(chan struct{}), release: make(chan struct{})}
	tr := stopWireTrader(t, v, "LONG", 0.001)
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: "LONG", Entry: 100, TP: 100.004, SL: 99.996}
	close(tr.State.StopIntentChan)
	done := make(chan struct{})
	go func() { tr.StartStopController(); close(done) }()
	independentWait(t, v.reached, "final GET")
	tr.setActiveTradeID("C")
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("final GET ignores stop context cancellation")
	}
	close(v.release)
	if len(v.journal) != 0 || tr.State.StopCtrl.AppliedTP != 0 {
		t.Fatalf("stale final read applied: %v %+v", v.journal, tr.State.StopCtrl)
	}
}
