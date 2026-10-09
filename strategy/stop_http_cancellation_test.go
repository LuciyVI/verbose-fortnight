package strategy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
	"verbose-fortnight/position"
)

func TestStopHTTPContextCanceledOnLifecycleChange(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/position/list" {
			fmt.Fprint(w, `{"retCode":0,"result":{"list":[{"side":"Buy","size":"1","avgPrice":"100","takeProfit":"","stopLoss":""}]}}`)
			return
		}
		io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	tr, _, _, cleanup := newSpyTrader(t)
	defer cleanup()
	tr.Config.DemoRESTHost = srv.URL
	tr.Config.RESTTimeoutMs = 1000
	tr.APIClient = api.NewRESTClient(tr.Config, nopLogger{})
	tr.PositionManager = position.NewPositionManager(tr.APIClient, tr.Config, tr.State, nopLogger{})
	tr.State.ActiveTradeID = "A"
	tr.State.PositionState = models.PositionStateOpen
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: "LONG", Entry: 100, TP: 110, SL: 95}
	close(tr.State.StopIntentChan)
	done := make(chan struct{})
	go func() { tr.StartStopController(); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("stop request did not start")
	}
	tr.setActiveTradeID("B")
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("old lifecycle HTTP request not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop controller stuck on old response")
	}
	if tr.State.StopCtrl.DesiredTP != 0 || tr.State.StopCtrl.AppliedTP != 0 {
		t.Fatalf("old result polluted B cache: %+v", tr.State.StopCtrl)
	}
}
