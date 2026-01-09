package strategy

import (
	"math"
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

func TestRoundTPSLDirectional(t *testing.T) {
	tr := newTestTrader()
	cases := []struct {
		name   string
		side   string
		tpIn   float64
		slIn   float64
		tick   float64
		wantTP float64
		wantSL float64
	}{
		{
			name:   "long_floor_tp_floor_sl",
			side:   "LONG",
			tpIn:   100.74,
			slIn:   99.26,
			tick:   0.5,
			wantTP: 100.5,
			wantSL: 99.0,
		},
		{
			name:   "short_ceil_tp_ceil_sl",
			side:   "SHORT",
			tpIn:   99.26,
			slIn:   100.74,
			tick:   0.5,
			wantTP: 99.5,
			wantSL: 101.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTP := tr.roundTP(100, tc.tpIn, tc.tick, tc.side)
			gotSL := tr.roundSL(100, tc.slIn, tc.tick, tc.side)
			if gotTP != tc.wantTP {
				t.Fatalf("tp rounding got %f want %f", gotTP, tc.wantTP)
			}
			if gotSL != tc.wantSL {
				t.Fatalf("sl rounding got %f want %f", gotSL, tc.wantSL)
			}
		})
	}
}

func TestRoundSLMovesAwayFromEntry(t *testing.T) {
	tr := newTestTrader()
	entry := 100.0
	tick := 0.5

	longSL := 99.26
	roundedLong := tr.roundSL(entry, longSL, tick, "LONG")
	if entry-roundedLong < entry-longSL {
		t.Fatalf("long SL rounding moved closer: before %.2f after %.2f", longSL, roundedLong)
	}

	shortSL := 100.74
	roundedShort := tr.roundSL(entry, shortSL, tick, "SHORT")
	if roundedShort-entry < shortSL-entry {
		t.Fatalf("short SL rounding moved closer: before %.2f after %.2f", shortSL, roundedShort)
	}
}

func TestMinPocketDistanceUsesMax(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.PocketFeeMult = 2.0

	cases := []struct {
		name       string
		entry      float64
		pocketPerc float64
		feePerc    float64
		want       float64
	}{
		{
			name:       "fee_buffer_wins",
			entry:      100,
			pocketPerc: 0.001,
			feePerc:    0.01,
			want:       2.0,
		},
		{
			name:       "pocket_perc_wins",
			entry:      100,
			pocketPerc: 0.01,
			feePerc:    0.0001,
			want:       1.0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr.Config.SLPocketPerc = tc.pocketPerc
			tr.Config.RoundTripFeePerc = tc.feePerc
			tr.Config.FeeBufferMult = 1.0
			got := tr.minPocketDistance(tc.entry)
			if got != tc.want {
				t.Fatalf("minPocket got %f want %f", got, tc.want)
			}
		})
	}
}

func TestFeeFloorsApplyToTPSL(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.AtrSLMult = 0.1
	tr.Config.AtrTPMult = 0.1
	tr.Config.SlPerc = 0.0001
	tr.Config.MinProfitPerc = 0.0
	tr.Config.RoundTripFeePerc = 0.001
	tr.Config.FeeBufferMult = 1.0
	tr.Config.SLFeeFloorMult = 4.0
	tr.Config.TPFeeFloorMult = 5.0

	closes := []float64{100, 100.02, 100.04, 100.03, 100.05, 100.04, 100.06, 100.05, 100.07, 100.06, 100.08, 100.07, 100.09, 100.08, 100.1}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 0.01
		lows[i] = v - 0.01
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	entry := 100.0
	feeBuf := tr.feeBuffer(entry)
	details := tr.calcTPSLDetails(entry, "LONG")
	if details.slDist < feeBuf*tr.Config.SLFeeFloorMult-1e-6 {
		t.Fatalf("slDist below fee floor: got %.4f want >= %.4f", details.slDist, feeBuf*tr.Config.SLFeeFloorMult)
	}
	if details.tpDist < feeBuf*tr.Config.TPFeeFloorMult-1e-6 {
		t.Fatalf("tpDist below fee floor: got %.4f want >= %.4f", details.tpDist, feeBuf*tr.Config.TPFeeFloorMult)
	}
}

func TestDynamicRRByRegime(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.AtrSLMult = 1.0
	tr.Config.AtrTPMult = 0.8
	tr.Config.MinProfitPerc = 0.0
	tr.Config.RoundTripFeePerc = 0.0001
	tr.Config.FeeBufferMult = 1.0

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	cases := []struct {
		name   string
		regime string
		wantRR float64
	}{
		{
			name:   "range_rr",
			regime: "range",
			wantRR: tr.targetRR("range"),
		},
		{
			name:   "trend_rr",
			regime: "trend",
			wantRR: tr.targetRR("trend"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr.State.MarketRegime = tc.regime
			details := tr.calcTPSLDetails(100, "LONG")
			rr := details.tpDist / details.slDist
			if rr < tc.wantRR-0.0001 {
				t.Fatalf("rr too low: got %f want >= %f", rr, tc.wantRR)
			}
			if !details.dynamicRRApplied {
				t.Fatalf("expected dynamicRR to apply")
			}
		})
	}
}

func TestDynamicRRRespectsTarget(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.TargetRRRange = 1.3
	tr.Config.AtrSLMult = 1.0
	tr.Config.AtrTPMult = 0.5
	tr.Config.MinProfitPerc = 0.0
	tr.Config.RoundTripFeePerc = 0.0000001
	tr.Config.FeeBufferMult = 1.0
	tr.Config.AtrTPCapMultRange = 10.0

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	details := tr.calcTPSLDetails(100, "LONG")
	rr := details.tpDist / details.slDist
	if rr < tr.Config.TargetRRRange-0.001 {
		t.Fatalf("rr below target: got %.4f want >= %.4f", rr, tr.Config.TargetRRRange)
	}
	if rr >= 1.9 {
		t.Fatalf("rr unexpectedly close to 2: got %.4f", rr)
	}
	if details.tpDist < details.tpDistMinProfit-1e-6 {
		t.Fatalf("tpDist collapsed below minProfit: %.4f < %.4f", details.tpDist, details.tpDistMinProfit)
	}
}

func TestTPCapApplied(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.AtrSLMult = 1.0
	tr.Config.AtrTPMult = 10.0
	tr.Config.MinProfitPerc = 0.0
	tr.Config.RoundTripFeePerc = 0.0001
	tr.Config.FeeBufferMult = 1.0

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	details := tr.calcTPSLDetails(100, "LONG")
	cap := details.atr * tr.Config.AtrTPCapMultRange * tr.regimeFactor("range")
	if details.tpDistCapped > cap+0.0001 {
		t.Fatalf("tpDistCapped above cap: got %f cap %f", details.tpDistCapped, cap)
	}
	if !details.capApplied {
		t.Fatalf("expected capApplied")
	}
}

func TestTPSLInvariants(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 1.0
	tr.Config.MinProfitPerc = 0.005
	tr.Config.RoundTripFeePerc = 0.0005
	tr.Config.FeeBufferMult = 1.0

	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114}
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, v := range closes {
		highs[i] = v + 1
		lows[i] = v - 1
	}
	tr.State.Closes = closes
	tr.State.Highs = highs
	tr.State.Lows = lows

	cases := []struct {
		name string
		side string
	}{
		{name: "long", side: "LONG"},
		{name: "short", side: "SHORT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := 100.0
			tp, sl := tr.calculateInitialTPSL(entry, tc.side)
			minProfit := math.Max(entry*tr.Config.MinProfitPerc, tr.feeBuffer(entry)*1.5)
			minPocket := tr.minPocketDistance(entry)
			if tc.side == "LONG" {
				if tp <= entry+minProfit {
					t.Fatalf("tp invariant failed: tp %f min %f", tp, entry+minProfit)
				}
				if sl >= entry-minPocket {
					t.Fatalf("sl invariant failed: sl %f max %f", sl, entry-minPocket)
				}
			} else {
				if tp >= entry-minProfit {
					t.Fatalf("tp invariant failed: tp %f max %f", tp, entry-minProfit)
				}
				if sl <= entry+minPocket {
					t.Fatalf("sl invariant failed: sl %f min %f", sl, entry+minPocket)
				}
			}
		})
	}
}
