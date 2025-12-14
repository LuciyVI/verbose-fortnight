package strategy

import (
	"testing"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type nopLogger struct{}

func (nopLogger) Debug(string, ...interface{})          {}
func (nopLogger) Info(string, ...interface{})           {}
func (nopLogger) Warning(string, ...interface{})        {}
func (nopLogger) Error(string, ...interface{})          {}
func (nopLogger) Fatal(string, ...interface{})          {}
func (nopLogger) Sync() error                           { return nil }
func (nopLogger) ChangeLogLevel(level logging.LogLevel) {}

func newTestTrader() *Trader {
	cfg := config.LoadConfig()
	state := &models.State{}
	state.Instr.TickSize = 0.1
	tr := NewTrader(api.NewRESTClient(cfg, nopLogger{}), cfg, state, nopLogger{})
	return tr
}

func TestFeeBufferUsesFeeMult(t *testing.T) {
	tr := newTestTrader()
	tr.Config.RoundTripFeePerc = 0.001
	tr.Config.FeeBufferMult = 2
	state := tr.State
	state.Instr.TickSize = 0.1

	buf := tr.feeBuffer(10000)
	if buf <= 0 {
		t.Fatalf("expected positive buffer")
	}
	if buf < 2 { // 0.1%*2 rounded up to tick
		t.Fatalf("buffer too small: %f", buf)
	}
}

func TestHigherTimeframeBiasLong(t *testing.T) {
	tr := newTestTrader()
	tr.Config.HTFWindow = 5
	tr.Config.HTFMaLen = 3
	tr.State.Closes = []float64{10, 10.2, 10.4, 10.6, 10.8}
	bias := tr.higherTimeframeBias(0.01)
	if bias != "LONG" {
		t.Fatalf("expected LONG bias, got %s", bias)
	}
}

func TestCalculateTPSLWithRatioRespectsRR(t *testing.T) {
	tr := newTestTrader()
	tr.State.Closes = []float64{100, 101, 102, 103, 104}
	tr.State.Highs = tr.State.Closes
	tr.State.Lows = tr.State.Closes
	tr.Config.AtrSLMult = 1.0
	tr.Config.AtrTPMult = 2.0
	tr.Config.MinProfitPerc = 0.001
	tr.State.Instr.TickSize = 0.1

	tp, sl := tr.calculateTPSLWithRatio(100, "LONG")
	if tp <= 100 || sl >= 100 {
		t.Fatalf("tp/sl not placed correctly: tp %f sl %f", tp, sl)
	}
	if (tp-100)/(100-sl) < 1.9 { // should be near 2:1
		t.Fatalf("RR too low: tp %f sl %f", tp, sl)
	}
}
