package strategy

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

// This harness deliberately does not use the previous patch's spies or helpers.
type independentLog struct {
	mu    sync.Mutex
	lines []string
	hook  func(string)
}

func (l *independentLog) add(f string, a ...interface{}) {
	s := fmt.Sprintf(f, a...)
	l.mu.Lock()
	l.lines = append(l.lines, s)
	l.mu.Unlock()
	if l.hook != nil {
		l.hook(s)
	}
}
func (l *independentLog) Debug(f string, a ...interface{})   { l.add(f, a...) }
func (l *independentLog) Info(f string, a ...interface{})    { l.add(f, a...) }
func (l *independentLog) Warning(f string, a ...interface{}) { l.add(f, a...) }
func (l *independentLog) Error(f string, a ...interface{})   { l.add(f, a...) }
func (l *independentLog) Fatal(f string, a ...interface{})   { l.add(f, a...) }
func (l *independentLog) Sync() error                        { return nil }
func (l *independentLog) ChangeLogLevel(logging.LogLevel)    {}

type independentPosition struct {
	mu          sync.Mutex
	side        string
	qty, entry  float64
	reads       int
	writes      [][2]float64
	onRead      func(int)
	onWrite     func()
	onNormalize func()
}

func (p *independentPosition) NormalizeSide(s string) string {
	if p.onNormalize != nil {
		p.onNormalize()
	}
	switch strings.ToUpper(s) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	}
	return ""
}
func (p *independentPosition) HasOpenPosition() (bool, string, float64, float64, float64) {
	p.mu.Lock()
	p.reads++
	n := p.reads
	p.mu.Unlock()
	if p.onRead != nil {
		p.onRead(n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.qty > 0, p.side, p.qty, 0, 0
}
func (p *independentPosition) GetLastEntryPrice() float64 { return p.entry }
func (p *independentPosition) GetLastBidPrice() float64   { return 100 }
func (p *independentPosition) GetLastAskPrice() float64   { return 101 }
func (p *independentPosition) CancelAllOrders(string)     {}
func (p *independentPosition) UpdatePositionTPSL(_ string, tp, sl float64) error {
	if p.onWrite != nil {
		p.onWrite()
	}
	p.mu.Lock()
	p.writes = append(p.writes, [2]float64{tp, sl})
	p.mu.Unlock()
	return nil
}
func independentIntField(cfg *config.Config, name string, v int64) {
	field := reflect.ValueOf(cfg).Elem().FieldByName(name)
	if field.IsValid() {
		field.SetInt(v)
	}
}
func independentTrader() (*Trader, *independentPosition, *independentLog) {
	cfg := config.LoadConfig()
	cfg.APIKey = "offline-key"
	cfg.APISecret = "offline-secret"
	cfg.EnableLifecycleID = false
	cfg.EnableMakerFirst = true
	cfg.MakerTimeoutMs = 20
	independentIntField(cfg, "RESTTimeoutMs", 80)
	independentIntField(cfg, "OrderReconcileTimeoutMs", 100)
	state := &models.State{ActiveTradeID: "B", PositionState: models.PositionStateOpen, PosSide: "LONG", Instr: models.InstrumentInfo{MinQty: 0.1, QtyStep: 0.1, TickSize: 0.1}, StopIntentChan: make(chan models.StopIntent, 8), OrderTradeIDs: map[string]string{}, ExecTradeIDs: map[string]string{}, ProcessedExec: map[string]time.Time{}}
	pm := &independentPosition{side: "LONG", qty: 10, entry: 100}
	log := &independentLog{}
	return &Trader{Config: cfg, State: state, APIClient: api.NewRESTClient(cfg, log), PositionManager: pm, Logger: log}, pm, log
}
func independentWait(t *testing.T, ch <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("barrier timeout: " + why)
	}
}
func independentController(t *testing.T, tr *Trader) {
	t.Helper()
	close(tr.State.StopIntentChan)
	done := make(chan struct{})
	go func() { tr.StartStopController(); close(done) }()
	independentWait(t, done, "controller completion")
}

func TestIndependentLifecycleMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, active, position, intentID string
		qty                              float64
		want                             int
	}{
		{"late_A_same_side", "B", "LONG", "A", 10, 0},
		{"changed_direction", "B", "SHORT", "B", 10, 0},
		{"already_closed", "B", "LONG", "B", 0, 0},
		{"current_B", "B", "LONG", "B", 10, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr, pm, _ := independentTrader()
			pm.side = tc.position
			pm.qty = tc.qty
			tr.State.ActiveTradeID = tc.active
			tr.State.StopCtrl = models.StopController{TradeID: "B", DesiredTP: 120, DesiredSL: 80, AppliedTP: 120, AppliedSL: 80}
			before := tr.State.StopCtrl
			tr.State.StopIntentChan <- models.StopIntent{TradeID: tc.intentID, LifecycleID: tc.intentID, Side: "LONG", Entry: 100, TP: 110, SL: 90}
			independentController(t, tr)
			if len(pm.writes) != tc.want {
				t.Errorf("API journal=%v, expected writes=%d", pm.writes, tc.want)
			}
			if tc.want == 0 && !reflect.DeepEqual(before, tr.State.StopCtrl) {
				t.Errorf("foreign/unusable job modified controller: before=%+v after=%+v", before, tr.State.StopCtrl)
			}
			if tc.want == 1 && (tr.State.StopCtrl.DesiredTP != 110 || tr.State.StopCtrl.AppliedSL != 90) {
				t.Errorf("current job not applied: %+v", tr.State.StopCtrl)
			}
		})
	}
}

func TestIndependentLifecycleBarriers(t *testing.T) {
	for _, stage := range []string{"position_read", "before_dispatch", "response"} {
		t.Run(stage, func(t *testing.T) {
			tr, pm, log := independentTrader()
			tr.State.ActiveTradeID = "A"
			reached, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			gate := func() { once.Do(func() { close(reached); <-release }) }
			if stage == "position_read" {
				pm.onRead = func(int) { gate() }
			}
			if stage == "before_dispatch" {
				log.hook = func(s string) {
					if strings.Contains(s, `"event":"tpsl_intent"`) {
						gate()
					}
				}
			}
			if stage == "response" {
				pm.onWrite = gate
			}
			tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: "LONG", Entry: 100, TP: 110, SL: 90}
			close(tr.State.StopIntentChan)
			done := make(chan struct{})
			go func() { tr.StartStopController(); close(done) }()
			independentWait(t, reached, stage)
			// setActiveTradeID must acquire the shared state lock while network work is pending.
			switched := make(chan struct{})
			go func() { tr.setActiveTradeID("B"); close(switched) }()
			independentWait(t, switched, "unlocked state")
			tr.State.Lock()
			tr.State.StopCtrl = models.StopController{TradeID: "B", DesiredTP: 130, DesiredSL: 70, AppliedTP: 130, AppliedSL: 70}
			tr.State.Unlock()
			close(release)
			independentWait(t, done, "old job completion")
			if stage != "response" && len(pm.writes) != 0 {
				t.Errorf("stale request reached API: %v", pm.writes)
			}
			if tr.State.StopCtrl.DesiredTP != 130 || tr.State.StopCtrl.AppliedTP != 130 {
				t.Errorf("old job changed B controller: %+v", tr.State.StopCtrl)
			}
		})
	}
}

func TestIndependentLifecycleCacheOwnership(t *testing.T) {
	tr, pm, _ := independentTrader()
	tr.setActiveTradeID("A")
	tr.State.StopIntentChan <- models.StopIntent{TradeID: "A", LifecycleID: "A", Side: "LONG", Entry: 100, TP: 110, SL: 90}
	independentController(t, tr)
	tr.setActiveTradeID("B")
	tr.State.StopIntentChan = make(chan models.StopIntent, 8)
	job := models.StopIntent{TradeID: "B", LifecycleID: "B", Side: "LONG", Entry: 100, TP: 110, SL: 90}
	tr.State.StopIntentChan <- job
	tr.State.StopIntentChan <- job
	independentController(t, tr)
	if len(pm.writes) != 2 {
		t.Fatalf("A and B each require one write even with identical levels; journal=%v", pm.writes)
	}
}

func TestIndependentDelayedSourceIdentity(t *testing.T) {
	tr, pm, _ := independentTrader()
	tr.State.ActiveTradeID = "A"
	reached, release, scheduled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	pm.onNormalize = func() { once.Do(func() { close(reached); <-release }) }
	go func() {
		tr.enqueueDelayedStopIntent(models.StopIntent{Side: "LONG", Entry: 100, TP: 110, SL: 90}, 0)
		close(scheduled)
	}()
	independentWait(t, reached, "source capture/normalization")
	tr.setActiveTradeID("B")
	close(release)
	independentWait(t, scheduled, "scheduling returned")
	var job models.StopIntent
	select {
	case job = <-tr.State.StopIntentChan:
	case <-time.After(2 * time.Second):
		t.Fatal("delayed delivery missing")
	}
	if job.TradeID != "A" || job.LifecycleID != "A" {
		t.Errorf("source identity rebound during delivery: %+v", job)
	}
	tr.State.StopIntentChan <- job
	independentController(t, tr)
	if len(pm.writes) != 0 || tr.State.StopCtrl.DesiredTP != 0 || tr.State.StopCtrl.AppliedTP != 0 {
		t.Errorf("late anonymous A changed B: journal=%v controller=%+v", pm.writes, tr.State.StopCtrl)
	}
}
