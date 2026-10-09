package strategy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
	"verbose-fortnight/position"
)

func TestIndependentSecondPositionReadDirection(t *testing.T) {
	var mu sync.Mutex
	reads := 0
	var journal []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/v5/position/list" {
			reads++
			side := "Buy"
			if reads >= 2 {
				side = "Sell"
			}
			fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"side":%q,"size":"10","avgPrice":"100","takeProfit":"","stopLoss":""}]}}`, side)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		journal = append(journal, body)
		fmt.Fprint(w, `{"retCode":0}`)
	}))
	defer srv.Close()
	tr, _, log := independentTrader()
	tr.Config.DemoRESTHost = srv.URL
	tr.APIClient = api.NewRESTClient(tr.Config, log)
	tr.PositionManager = position.NewPositionManager(tr.APIClient, tr.Config, tr.State, log)
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: "LONG", Entry: 100, TP: 110, SL: 90}
	independentController(t, tr)
	if len(journal) != 0 {
		t.Errorf("LONG intent sent after final REST observation became SHORT: reads=%d journal=%v applied=%+v", reads, journal, tr.State.StopCtrl)
	}
}

func TestIndependentStopWirePriceInvariants(t *testing.T) {
	var wireTP, wireSL float64
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/position/list" {
			fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"side":"Buy","size":"10","avgPrice":"100","takeProfit":"","stopLoss":""}]}}`)
			return
		}
		var body map[string]string
		raw, _ := io.ReadAll(r.Body)
		var generic map[string]any
		json.Unmarshal(raw, &generic)
		body = map[string]string{"tp": fmt.Sprint(generic["takeProfit"]), "sl": fmt.Sprint(generic["stopLoss"])}
		mu.Lock()
		fmt.Sscan(body["tp"], &wireTP)
		fmt.Sscan(body["sl"], &wireSL)
		mu.Unlock()
		fmt.Fprint(w, `{"retCode":0}`)
	}))
	defer srv.Close()
	tr, _, log := independentTrader()
	tr.Config.DemoRESTHost = srv.URL
	tr.State.Instr.TickSize = 0.001
	tr.APIClient = api.NewRESTClient(tr.Config, log)
	tr.PositionManager = position.NewPositionManager(tr.APIClient, tr.Config, tr.State, log)
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "B", LifecycleID: "B", Side: "LONG", Entry: 100, TP: 100.004, SL: 99.996}
	independentController(t, tr)
	if wireTP <= 100 || wireSL >= 100 {
		t.Errorf("valid intent loses invariants on wire: entry=100 TP=%g SL=%g cache=%+v", wireTP, wireSL, tr.State.StopCtrl)
	}
}

func TestIndependentInFlightHTTPIsolation(t *testing.T) {
	reached, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/position/list" {
			fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"side":"Buy","size":"10","avgPrice":"100","takeProfit":"","stopLoss":""}]}}`)
			return
		}
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		calls++
		mu.Unlock()
		close(reached)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
			fmt.Fprint(w, `{"retCode":0}`)
		}
	}))
	defer srv.Close()
	defer close(release)
	tr, _, log := independentTrader()
	tr.State.ActiveTradeID = "A"
	tr.Config.DemoRESTHost = srv.URL
	tr.APIClient = api.NewRESTClient(tr.Config, log)
	tr.PositionManager = position.NewPositionManager(tr.APIClient, tr.Config, tr.State, log)
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: "LONG", Entry: 100, TP: 110, SL: 90}
	close(tr.State.StopIntentChan)
	done := make(chan struct{})
	go func() { tr.StartStopController(); close(done) }()
	independentWait(t, reached, "accepted HTTP")
	tr.setActiveTradeID("B")
	independentWait(t, canceled, "HTTP cancellation")
	independentWait(t, done, "controller return")
	if tr.State.StopCtrl.DesiredTP != 0 || tr.State.StopCtrl.AppliedTP != 0 {
		t.Errorf("old HTTP result affected B: %+v", tr.State.StopCtrl)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("unexpected old request journal count %d", calls)
	}
}
