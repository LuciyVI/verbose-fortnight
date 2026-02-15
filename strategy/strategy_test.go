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
			name:   "long_floor_tp_ceil_sl",
			side:   "LONG",
			tpIn:   100.74,
			slIn:   99.26,
			tick:   0.5,
			wantTP: 100.5,
			wantSL: 99.0,
		},
		{
			name:   "short_ceil_tp_floor_sl",
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

func TestCalculateInitialTPSLDerivesSLFromTP(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.EnableTPSLStage1 = true

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

	entry := 100.03
	tp, sl := tr.calculateInitialTPSL(entry, "LONG")

	tpDist := math.Abs(tp - entry)
	wantSLRaw := entry - tpDist/2
	wantSL := tr.roundSL(entry, wantSLRaw, tr.State.Instr.TickSize, "LONG")
	if sl != wantSL {
		t.Fatalf("sl not derived from tp: got %.4f want %.4f (tp %.4f entry %.4f)", sl, wantSL, tp, entry)
	}
}

func TestCalculateInitialTPSLIgnoresSlPercWhenDerivingSL(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.State.MarketRegime = "range"
	tr.Config.EnableTPSLStage1 = true

	// Make fee floors/pocket small and deterministic.
	tr.Config.RoundTripFeePerc = 0.0000001
	tr.Config.FeeBufferMult = 1.0
	tr.Config.MinProfitPerc = 0.0
	tr.Config.TPFeeFloorMult = 1.0
	tr.Config.SLPocketPerc = 0.0
	tr.Config.PocketFeeMult = 2.0

	// ATR should be available.
	closes := []float64{100, 100.01, 100.02, 100.01, 100.03, 100.02, 100.01, 100.02, 100.03, 100.02, 100.01, 100.02, 100.03, 100.02, 100.01}
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

	// SlPerc can be wide, but should not force a huge TP when SL is derived from TP.
	tr.Config.SlPerc = 0.2
	tp, _ := tr.calculateInitialTPSL(entry, "LONG")
	tpDist := tp - entry
	if tpDist >= 1.5 {
		t.Fatalf("tpDist too large despite small fee floors: got %.4f", tpDist)
	}
}

func TestCalculateInitialTPSLRespectsFeeFloors(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.TPFeeFloorMult = 3.0
	tr.Config.SLFeeFloorMult = 2.5
	entry := 100.0

	tp, sl := tr.calculateInitialTPSL(entry, "LONG")
	feeBuf := tr.feeBuffer(entry)

	tpDist := math.Abs(tp - entry)
	slDist := math.Abs(entry - sl)

	if tpDist < feeBuf*tr.Config.TPFeeFloorMult {
		t.Fatalf("tpDist %.4f below fee floor %.4f", tpDist, feeBuf*tr.Config.TPFeeFloorMult)
	}
	if slDist < feeBuf*tr.Config.SLFeeFloorMult {
		t.Fatalf("slDist %.4f below SL fee floor %.4f", slDist, feeBuf*tr.Config.SLFeeFloorMult)
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

func TestDynamicTPClamps(t *testing.T) {
	tr := newTestTrader()
	tr.Config.DynamicTPK = 1.0
	tr.Config.DynamicTPVolatilityFactor = 1.0
	tr.Config.DynamicTPMinPerc = 0.4
	tr.Config.DynamicTPMaxPerc = 1.8

	low := tr.dynamicTP(100, 0.01, "range") // ATR% = 0.01%
	if math.Abs(low.ClampedPercent-0.4) > 1e-9 {
		t.Fatalf("expected clamp to min: got %.6f", low.ClampedPercent)
	}

	high := tr.dynamicTP(100, 5.0, "range") // ATR% = 5%
	if math.Abs(high.ClampedPercent-1.8) > 1e-9 {
		t.Fatalf("expected clamp to max: got %.6f", high.ClampedPercent)
	}

	rangeMid := tr.dynamicTP(100, 0.5, "range") // ATR% = 0.5%
	trendMid := tr.dynamicTP(100, 0.5, "trend")
	if trendMid.RawPercent <= rangeMid.RawPercent {
		t.Fatalf("expected trend raw TP > range raw TP: trend %.6f range %.6f", trendMid.RawPercent, rangeMid.RawPercent)
	}
}

func TestTPSLInvariants(t *testing.T) {
	tr := newTestTrader()
	tr.State.Instr.TickSize = 0.1
	tr.Config.DynamicTPK = 1.0
	tr.Config.DynamicTPVolatilityFactor = 1.0
	tr.Config.DynamicTPMinPerc = 0.4
	tr.Config.DynamicTPMaxPerc = 1.8
	tr.Config.RoundTripFeePerc = 0.001
	tr.Config.FeeBufferMult = 1.0
	// No ATR available -> TP is clamped to min (then adjusted for pocket + rounding).
	tr.State.Closes = nil
	tr.State.Highs = nil
	tr.State.Lows = nil

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
			minPocket := tr.minPocketDistance(entry)
			tpDist := math.Abs(tp - entry)
			slDist := math.Abs(entry - sl)
			if slDist <= minPocket {
				t.Fatalf("sl pocket invariant failed: slDist %.4f <= minPocket %.4f", slDist, minPocket)
			}
			if tpDist <= 2*minPocket {
				t.Fatalf("tp pocket invariant failed: tpDist %.4f <= 2*minPocket %.4f", tpDist, 2*minPocket)
			}
			if slDist > 0 && tpDist/slDist < 2.0-1e-9 {
				t.Fatalf("rr invariant failed: got %.6f want >= 2.0", tpDist/slDist)
			}
			if tc.side == "LONG" {
				if sl >= entry-minPocket {
					t.Fatalf("sl invariant failed: sl %f max %f", sl, entry-minPocket)
				}
			} else {
				if sl <= entry+minPocket {
					t.Fatalf("sl invariant failed: sl %f min %f", sl, entry+minPocket)
				}
			}
		})
	}
}
