package strategy

import (
	"math"
	"testing"

	"verbose-fortnight/indicators"
	"verbose-fortnight/models"
)

func observabilityOhlcv(closes, volumes []float64) models.OhlcvSnapshot {
	highs := make([]float64, len(closes))
	lows := make([]float64, len(closes))
	for i, closePrice := range closes {
		highs[i] = closePrice + 1
		lows[i] = closePrice - 1
	}
	return models.OhlcvSnapshot{
		Closes:    append([]float64(nil), closes...),
		Highs:     highs,
		Lows:      lows,
		Volumes:   append([]float64(nil), volumes...),
		HTFCloses: append([]float64(nil), closes...),
		CandleSeq: uint64(len(closes)),
	}
}

func observabilityCloses(n int) []float64 {
	closes := make([]float64, n)
	for i := range closes {
		closes[i] = 100 + float64(i)*0.35 + math.Sin(float64(i))*1.5
	}
	return closes
}

func assertFloatNear(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s got %.12f want %.12f", name, got, want)
	}
}

func TestIndicatorSnapshotVolumeRatioCalculation(t *testing.T) {
	tr := newTestTrader()
	tr.Config.SmaLen = 5
	closes := observabilityCloses(30)
	volumes := make([]float64, len(closes))
	for i := range volumes {
		volumes[i] = 10 + float64(i)
	}

	snap := tr.buildIndicatorSnapshot(observabilityOhlcv(closes, volumes))

	wantVolumeSMA := indicators.SMA(volumes[len(volumes)-tr.Config.SmaLen:])
	if !snap.VolumeAvailable {
		t.Fatalf("expected volume diagnostics to be available")
	}
	assertFloatNear(t, "volume", snap.Volume, volumes[len(volumes)-1])
	assertFloatNear(t, "volumeSMA", snap.VolumeSMA, wantVolumeSMA)
	assertFloatNear(t, "volumeRatio", snap.VolumeRatio, volumes[len(volumes)-1]/wantVolumeSMA)
}

func TestIndicatorSnapshotRSIDeltaCalculation(t *testing.T) {
	tr := newTestTrader()
	closes := observabilityCloses(30)
	volumes := make([]float64, len(closes))
	for i := range volumes {
		volumes[i] = 1
	}

	snap := tr.buildIndicatorSnapshot(observabilityOhlcv(closes, volumes))
	rsiValues := indicators.RSI(closes, 14)
	wantPrev := rsiValues[len(rsiValues)-2]
	wantDelta := rsiValues[len(rsiValues)-1] - wantPrev

	if !snap.RSIDeltaAvailable {
		t.Fatalf("expected RSI delta diagnostics to be available")
	}
	assertFloatNear(t, "rsiPrev", snap.RSIPrev, wantPrev)
	assertFloatNear(t, "rsiDelta", snap.RSIDelta, wantDelta)
}

func TestIndicatorSnapshotMACDHistDeltaCalculation(t *testing.T) {
	tr := newTestTrader()
	closes := observabilityCloses(40)
	volumes := make([]float64, len(closes))
	for i := range volumes {
		volumes[i] = 1
	}

	snap := tr.buildIndicatorSnapshot(observabilityOhlcv(closes, volumes))
	_, _, wantPrev := indicators.MACD(closes[:len(closes)-1])
	_, _, wantCurrent := indicators.MACD(closes)

	if !snap.MACDHistDeltaAvailable {
		t.Fatalf("expected MACD histogram delta diagnostics to be available")
	}
	assertFloatNear(t, "macdHistPrev", snap.MACDHistPrev, wantPrev)
	assertFloatNear(t, "macdHistDelta", snap.MACDHistDelta, wantCurrent-wantPrev)
}

func TestBBPositionForClose(t *testing.T) {
	cases := []struct {
		name  string
		close float64
		want  string
	}{
		{name: "below lower", close: 89, want: bbPositionBelowLower},
		{name: "inside below middle", close: 95, want: bbPositionInsideBelowMiddle},
		{name: "inside above middle", close: 105, want: bbPositionInsideAboveMiddle},
		{name: "above upper", close: 111, want: bbPositionAboveUpper},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := bbPositionForClose(tc.close, 90, 100, 110)
			if got != tc.want {
				t.Fatalf("position got %q want %q", got, tc.want)
			}
		})
	}
}

func TestBBReclaimFromBands(t *testing.T) {
	wasBelow, returnedInside, available := bbReclaimFromBands(
		89,
		88,
		90,
		91,
		89.5,
		90,
		true,
	)

	if !available {
		t.Fatalf("expected BB reclaim diagnostics to be available")
	}
	if !wasBelow {
		t.Fatalf("expected wasBelowLowerBB=true")
	}
	if !returnedInside {
		t.Fatalf("expected returnedInsideBB=true")
	}
}

func TestIndicatorDiagnosticsDoNotChangeSignalDecision(t *testing.T) {
	tr := newSignalRegressionTrader()
	snap := regressionLongSnapshot(60, "")
	snap.VolumeRatio = 8
	snap.VolumeAvailable = true
	snap.RSIDelta = 12
	snap.RSIDeltaAvailable = true
	snap.MACDHistDelta = 0.5
	snap.MACDHistDeltaAvailable = true
	snap.BBPosition = bbPositionInsideBelowMiddle
	snap.WasBelowLowerBB = true
	snap.ReturnedInsideBB = true
	snap.BBReclaimAvailable = true

	scored := tr.ScoreSignal(snap, 1, regressionCloses())
	filtered := tr.FilterSignal(scored, regressionPassingOrderbook)
	if !filtered.Accepted {
		t.Fatalf("expected existing LONG signal to pass filters: %+v", filtered.Signal.Debug)
	}
	if filtered.Signal.Direction != "LONG" || filtered.Signal.Strength != 3 {
		t.Fatalf("decision changed: direction=%q strength=%d", filtered.Signal.Direction, filtered.Signal.Strength)
	}
	if filtered.Signal.Debug.VolumeRatio != snap.VolumeRatio || filtered.Signal.Debug.RSIDelta != snap.RSIDelta || filtered.Signal.Debug.MACDHistDelta != snap.MACDHistDelta {
		t.Fatalf("debug diagnostics not carried: %+v", filtered.Signal.Debug)
	}
}
