package strategy

import (
	"reflect"
	"testing"
	"time"
	"verbose-fortnight/models"
)

func runStopQueue(t *testing.T, tr *Trader) {
	t.Helper()
	close(tr.State.StopIntentChan)
	done := make(chan struct{})
	go func() { tr.StartStopController(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stop controller did not finish")
	}
}

func TestStopIntentLifecycleIsolation(t *testing.T) {
	for _, tc := range []struct {
		name, active, posSide, intentSide string
		state                             models.PositionFSMState
		exists                            bool
	}{
		{"same_direction_A_to_B", "B", "LONG", "LONG", models.PositionStateOpen, true},
		{"reverse_A_to_B", "B", "SHORT", "LONG", models.PositionStateOpen, true},
		{"closed", "", "LONG", "LONG", models.PositionStateFlat, false},
		{"current_wrong_side", "A", "SHORT", "LONG", models.PositionStateOpen, true},
		{"closing", "A", "LONG", "LONG", models.PositionStateClosing, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, _, pm, cleanup := newSpyTrader(t)
			defer cleanup()
			pm.setOpen(tc.posSide, 1, 100, 0, 0)
			pm.exists = tc.exists
			tr.State.ActiveTradeID = tc.active
			tr.State.PositionState = tc.state
			tr.State.StopCtrl = models.StopController{DesiredTP: 140, DesiredSL: 80, AppliedTP: 130, AppliedSL: 85, TradeID: tc.active}
			before := tr.State.StopCtrl
			tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: tc.intentSide, Entry: 100, TP: 110, SL: 95}
			runStopQueue(t, tr)
			if pm.updateCount() != 0 || !reflect.DeepEqual(before, tr.State.StopCtrl) {
				t.Fatalf("stale intent changed state/API: before=%+v after=%+v calls=%d", before, tr.State.StopCtrl, pm.updateCount())
			}
		})
	}
}

func TestDelayedStopCapturesLifecycleAtCreation(t *testing.T) {
	tr, _, pm, cleanup := newSpyTrader(t)
	defer cleanup()
	tr.State.ActiveTradeID = "A"
	tr.State.PositionState = models.PositionStateOpen
	pm.setOpen("LONG", 1, 100, 0, 0)
	tr.enqueueDelayedStopIntent(models.StopIntent{Side: "LONG", Entry: 100, TP: 110, SL: 95}, 20*time.Millisecond)
	tr.setActiveTradeID("B")
	select {
	case intent := <-tr.State.StopIntentChan:
		if intent.TradeID != "A" || intent.LifecycleID != "A" {
			t.Fatalf("intent rebound to new trade: %+v", intent)
		}
		tr.State.StopIntentChan <- intent
	case <-time.After(time.Second):
		t.Fatal("delayed delivery did not finish")
	}
	runStopQueue(t, tr)
	if pm.updateCount() != 0 {
		t.Fatal("old delayed intent called API")
	}
}

type stopBarrierPosition struct {
	*spyPositionManager
	readStarted, readRelease, writeStarted, writeRelease chan struct{}
}

func (p *stopBarrierPosition) HasOpenPosition() (bool, string, float64, float64, float64) {
	if p.readStarted != nil {
		close(p.readStarted)
		<-p.readRelease
	}
	return p.spyPositionManager.HasOpenPosition()
}
func (p *stopBarrierPosition) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	if p.writeStarted != nil {
		close(p.writeStarted)
		<-p.writeRelease
	}
	return p.spyPositionManager.UpdatePositionTPSL(symbol, tp, sl)
}

func TestStopLifecycleChangesDuringProcessing(t *testing.T) {
	for _, stage := range []string{"position_read", "network_response"} {
		t.Run(stage, func(t *testing.T) {
			tr, _, pm, cleanup := newSpyTrader(t)
			defer cleanup()
			pm.setOpen("LONG", 1, 100, 0, 0)
			tr.State.PositionState = models.PositionStateOpen
			tr.State.ActiveTradeID = "A"
			p := &stopBarrierPosition{spyPositionManager: pm}
			started, release := make(chan struct{}), make(chan struct{})
			if stage == "position_read" {
				p.readStarted, p.readRelease = started, release
			} else {
				p.writeStarted, p.writeRelease = started, release
			}
			tr.PositionManager = p
			tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: "LONG", Entry: 100, TP: 110, SL: 95}
			close(tr.State.StopIntentChan)
			done := make(chan struct{})
			go func() { tr.StartStopController(); close(done) }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("barrier not reached")
			}
			// This lock must remain available while the position read / network response stalls.
			tr.setActiveTradeID("B")
			tr.State.Lock()
			tr.State.StopCtrl = models.StopController{TradeID: "B", DesiredTP: 120, DesiredSL: 90, AppliedTP: 120, AppliedSL: 90}
			tr.State.Unlock()
			close(release)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("controller did not finish")
			}
			if tr.State.StopCtrl.TradeID != "B" || tr.State.StopCtrl.DesiredTP != 120 || tr.State.StopCtrl.AppliedTP != 120 {
				t.Fatalf("old job overwrote new lifecycle: %+v", tr.State.StopCtrl)
			}
			if stage == "position_read" && pm.updateCount() != 0 {
				t.Fatal("stale read called API")
			}
		})
	}
}

func TestStopCurrentLifecycleAndCache(t *testing.T) {
	tr, _, pm, cleanup := newSpyTrader(t)
	defer cleanup()
	tr.State.ActiveTradeID = "B"
	tr.State.PositionState = models.PositionStateOpen
	pm.setOpen("LONG", 1, 100, 0, 0)
	// Matching levels inherited from A must not suppress B's write.
	tr.State.StopCtrl = models.StopController{TradeID: "A", AppliedTP: 110, AppliedSL: 95}
	intent := models.StopIntent{TradeID: "B", LifecycleID: "B", Side: "LONG", Entry: 100, TP: 110, SL: 95}
	tr.enqueueStopIntent(intent)
	tr.enqueueStopIntent(intent)
	runStopQueue(t, tr)
	if pm.updateCount() != 1 || tr.State.StopCtrl.DesiredTP != 110 || tr.State.StopCtrl.AppliedTP != 110 {
		t.Fatalf("current trade not applied exactly once: %+v calls=%d", tr.State.StopCtrl, pm.updateCount())
	}
}
