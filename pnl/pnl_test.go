package pnl

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestGrossByPositionSide(t *testing.T) {
	tests := []struct {
		name string
		side string
		in   float64
		out  float64
		qty  float64
		want float64
	}{
		{name: "long profit", side: "LONG", in: 100, out: 110, qty: 1, want: 10},
		{name: "long loss", side: "Buy", in: 100, out: 90, qty: 2, want: -20},
		{name: "short profit", side: "SHORT", in: 110, out: 100, qty: 1.5, want: 15},
		{name: "short loss", side: "Sell", in: 100, out: 103, qty: 2, want: -6},
	}
	for _, tc := range tests {
		got := Gross(tc.side, tc.in, tc.out, tc.qty)
		if !almostEqual(got, tc.want) {
			t.Fatalf("%s: got %.6f want %.6f", tc.name, got, tc.want)
		}
	}
}

func TestSummarizePartialCloses(t *testing.T) {
	legs := []Leg{
		{PositionSide: "LONG", EntryPrice: 100, ExitPrice: 110, Qty: 0.4, Fee: 0.08},
		{PositionSide: "LONG", EntryPrice: 100, ExitPrice: 90, Qty: 0.6, Fee: 0.12},
	}
	s := Summarize(legs)
	if !almostEqual(s.Gross, -2.0) {
		t.Fatalf("gross got %.6f want -2.0", s.Gross)
	}
	if !almostEqual(s.FeeTotal, 0.2) {
		t.Fatalf("fee got %.6f want 0.2", s.FeeTotal)
	}
	if !almostEqual(s.NetCalculated, -2.2) {
		t.Fatalf("net calc got %.6f want -2.2", s.NetCalculated)
	}
}

func TestSummarizeFlipSequence(t *testing.T) {
	legs := []Leg{
		{PositionSide: "LONG", EntryPrice: 100, ExitPrice: 102, Qty: 1.0, Fee: 0.10},
		{PositionSide: "SHORT", EntryPrice: 103, ExitPrice: 100, Qty: 0.5, Fee: 0.05},
	}
	s := Summarize(legs)
	if !almostEqual(s.Gross, 3.5) {
		t.Fatalf("gross got %.6f want 3.5", s.Gross)
	}
	if !almostEqual(s.FeeTotal, 0.15) {
		t.Fatalf("fee got %.6f want 0.15", s.FeeTotal)
	}
	if !almostEqual(s.NetCalculated, 3.35) {
		t.Fatalf("net calc got %.6f want 3.35", s.NetCalculated)
	}
}

func TestResolveNetSource(t *testing.T) {
	s := Summary{
		Gross:         2.0,
		FeeTotal:      0.3,
		FundingSigned: -0.1,
		NetCalculated: NetFromComponents(2.0, 0.3, -0.1),
	}
	res := ResolveNet(nil, s)
	if res.Source != NetSourceCalculated || !almostEqual(res.Net, 1.6) {
		t.Fatalf("calculated source mismatch: %+v", res)
	}

	exchangeNet := 1.2345
	res = ResolveNet(&exchangeNet, s)
	if res.Source != NetSourceExchange || !almostEqual(res.Net, 1.2345) {
		t.Fatalf("exchange source mismatch: %+v", res)
	}
}

func TestGrossSignInvariant(t *testing.T) {
	tests := []struct {
		name string
		side string
		in   float64
		out  float64
		qty  float64
		sign int
	}{
		{name: "long gain", side: "LONG", in: 100, out: 101, qty: 1, sign: 1},
		{name: "long loss", side: "LONG", in: 100, out: 99, qty: 1, sign: -1},
		{name: "short gain", side: "SHORT", in: 100, out: 99, qty: 1, sign: 1},
		{name: "short loss", side: "SHORT", in: 100, out: 101, qty: 1, sign: -1},
		{name: "unknown side", side: "CLOSE_BUY", in: 100, out: 101, qty: 1, sign: 0},
	}
	for _, tc := range tests {
		gross, source, ok := GrossWithSource(tc.side, tc.in, tc.out, tc.qty)
		if tc.sign == 0 {
			if ok || source != GrossSourcePositionSide || gross != 0 {
				t.Fatalf("%s: expected unknown-side zero gross, got gross=%.6f source=%s ok=%t", tc.name, gross, source, ok)
			}
			continue
		}
		if !ok {
			t.Fatalf("%s: expected gross to be computable", tc.name)
		}
		if source != GrossSourcePositionSide {
			t.Fatalf("%s: source mismatch got %s", tc.name, source)
		}
		if gotSign := Sign(gross); gotSign != tc.sign {
			t.Fatalf("%s: sign got %d want %d gross=%.6f", tc.name, gotSign, tc.sign, gross)
		}
	}
}

func TestFeeAllocationPartial(t *testing.T) {
	feeOpenAlloc := AllocateOpenFee(0.9, 3, 1)
	if !almostEqual(feeOpenAlloc, 0.3) {
		t.Fatalf("fee allocation mismatch got %.6f want 0.3", feeOpenAlloc)
	}

	fullAlloc := AllocateOpenFee(0.9, 3, 3)
	if !almostEqual(fullAlloc, 0.9) {
		t.Fatalf("full allocation mismatch got %.6f want 0.9", fullAlloc)
	}

	overAlloc := AllocateOpenFee(0.9, 3, 5)
	if !almostEqual(overAlloc, 0.9) {
		t.Fatalf("over-allocation must cap at full fee: got %.6f want 0.9", overAlloc)
	}
}

func TestBuildNetBreakdown(t *testing.T) {
	exchangeNet := 1.23
	b := BuildNetBreakdown(2.0, 0.2, 0.3, -0.1, 1.23, &exchangeNet)
	if b.GrossSource != GrossSourcePositionSide {
		t.Fatalf("gross source mismatch: %s", b.GrossSource)
	}
	if !almostEqual(b.FeeOpenAlloc, 0.2) || !almostEqual(b.FeeClose, 0.3) {
		t.Fatalf("unexpected fee split: %+v", b)
	}
	if !almostEqual(b.NetCalculated, 1.4) {
		t.Fatalf("unexpected net calculated: %.6f", b.NetCalculated)
	}
	if b.NetSource != NetSourceExchange || b.NetExchange == nil || !almostEqual(*b.NetExchange, 1.23) {
		t.Fatalf("unexpected net source/exchange: %+v", b)
	}
}
