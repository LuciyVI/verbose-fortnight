package pnl

import (
	"math"
	"strings"
)

// GrossSource marks which semantic was used to compute gross PnL.
type GrossSource string

const (
	GrossSourcePositionSide GrossSource = "position_side"
	GrossSourceCloseSide    GrossSource = "close_side"
)

// NetSource marks which source was used to produce final net PnL.
type NetSource string

const (
	NetSourceExchange   NetSource = "exchange"
	NetSourceCalculated NetSource = "calculated"
)

// Leg is a single realized trading leg used for gross/fee/funding aggregation.
// For partial closes and flips, provide one leg per realized close.
type Leg struct {
	PositionSide  string
	EntryPrice    float64
	ExitPrice     float64
	Qty           float64
	Fee           float64
	FundingSigned float64
}

// Summary contains normalized PnL components.
type Summary struct {
	Gross         float64
	FeeTotal      float64
	FundingSigned float64
	NetCalculated float64
}

// NetBreakdown is an explicit net PnL component table used for forensic logs/reports.
type NetBreakdown struct {
	Gross            float64
	GrossSource      GrossSource
	FeeOpenAlloc     float64
	FeeClose         float64
	FundingSigned    float64
	RealisedDeltaCum float64
	NetCalculated    float64
	NetExchange      *float64
	NetSource        NetSource
}

// NetResult is a final net PnL decision with explicit source labeling.
type NetResult struct {
	Summary
	Net    float64
	Source NetSource
}

// NormalizePositionSide maps Buy/Sell and Long/Short variants to LONG/SHORT.
func NormalizePositionSide(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

// Gross calculates directional gross PnL from position side and prices.
func Gross(positionSide string, entry, exit, qty float64) float64 {
	gross, _, ok := GrossWithSource(positionSide, entry, exit, qty)
	if !ok {
		return 0
	}
	return gross
}

// GrossWithSource calculates directional gross PnL from position side and prices.
// Returns ok=false when position side is unknown.
func GrossWithSource(positionSide string, entry, exit, qty float64) (float64, GrossSource, bool) {
	if entry <= 0 || exit <= 0 || qty <= 0 {
		return 0, GrossSourcePositionSide, false
	}
	absQty := math.Abs(qty)
	switch NormalizePositionSide(positionSide) {
	case "SHORT":
		return (entry - exit) * absQty, GrossSourcePositionSide, true
	case "LONG":
		return (exit - entry) * absQty, GrossSourcePositionSide, true
	}
	return 0, GrossSourcePositionSide, false
}

// NetFromComponents calculates net PnL from normalized components.
// fundingSigned is signed cashflow: positive adds to PnL, negative reduces PnL.
func NetFromComponents(gross, feeTotal, fundingSigned float64) float64 {
	return gross - math.Abs(feeTotal) + fundingSigned
}

// Summarize aggregates realized legs into one PnL summary.
func Summarize(legs []Leg) Summary {
	s := Summary{}
	for _, leg := range legs {
		s.Gross += Gross(leg.PositionSide, leg.EntryPrice, leg.ExitPrice, leg.Qty)
		s.FeeTotal += math.Abs(leg.Fee)
		s.FundingSigned += leg.FundingSigned
	}
	s.NetCalculated = NetFromComponents(s.Gross, s.FeeTotal, s.FundingSigned)
	return s
}

// ResolveNet chooses final net source explicitly.
// If exchangeNet is present, it wins and Source=exchange.
func ResolveNet(exchangeNet *float64, summary Summary) NetResult {
	result := NetResult{
		Summary: summary,
		Net:     summary.NetCalculated,
		Source:  NetSourceCalculated,
	}
	if exchangeNet != nil {
		result.Net = *exchangeNet
		result.Source = NetSourceExchange
	}
	return result
}

// AllocateOpenFee prorates open-leg fee by closed quantity.
func AllocateOpenFee(feeOpenTotal, qtyOpened, qtyClosed float64) float64 {
	feeOpenTotal = math.Abs(feeOpenTotal)
	if feeOpenTotal == 0 {
		return 0
	}
	if qtyOpened <= 0 || qtyClosed <= 0 {
		return 0
	}
	ratio := math.Abs(qtyClosed) / math.Abs(qtyOpened)
	if ratio > 1 {
		ratio = 1
	}
	return feeOpenTotal * ratio
}

// BuildNetBreakdown returns an explicit net-component view and resolved net source.
func BuildNetBreakdown(gross, feeOpenAlloc, feeClose, fundingSigned, realisedDeltaCum float64, exchangeNet *float64) NetBreakdown {
	feeOpenAlloc = math.Abs(feeOpenAlloc)
	feeClose = math.Abs(feeClose)
	summary := Summary{
		Gross:         gross,
		FeeTotal:      feeOpenAlloc + feeClose,
		FundingSigned: fundingSigned,
	}
	summary.NetCalculated = NetFromComponents(summary.Gross, summary.FeeTotal, summary.FundingSigned)
	resolved := ResolveNet(exchangeNet, summary)
	return NetBreakdown{
		Gross:            gross,
		GrossSource:      GrossSourcePositionSide,
		FeeOpenAlloc:     feeOpenAlloc,
		FeeClose:         feeClose,
		FundingSigned:    fundingSigned,
		RealisedDeltaCum: realisedDeltaCum,
		NetCalculated:    summary.NetCalculated,
		NetExchange:      exchangeNet,
		NetSource:        resolved.Source,
	}
}

// Sign returns -1/0/1 with deadband epsilon to avoid float-noise mismatches.
func Sign(v float64) int {
	const eps = 1e-9
	switch {
	case v > eps:
		return 1
	case v < -eps:
		return -1
	default:
		return 0
	}
}
