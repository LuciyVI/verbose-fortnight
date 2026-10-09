// Package prices implements decimal tick alignment for prices sent to the exchange.
package prices

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

func NormalizeSide(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	}
	return ""
}

// Quantize uses the decimal representations of the inputs, without float division.
// Padding to at least two decimals preserves existing wire representations, while
// the instrument tick (including 0.25 and smaller ticks) determines precision.
func Quantize(value, tick float64, up bool) (string, float64, error) {
	if value <= 0 || tick <= 0 || math.IsNaN(value) || math.IsNaN(tick) || math.IsInf(value, 0) || math.IsInf(tick, 0) {
		return "", 0, fmt.Errorf("price and tickSize must be finite and positive")
	}
	tickText := strconv.FormatFloat(tick, 'f', -1, 64)
	v, _ := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	t, _ := new(big.Rat).SetString(tickText)
	ratio := new(big.Rat).Quo(v, t)
	units, rem := new(big.Int), new(big.Int)
	units.QuoRem(ratio.Num(), ratio.Denom(), rem)
	if up && rem.Sign() != 0 {
		units.Add(units, big.NewInt(1))
	}
	if units.Sign() == 0 {
		return "", 0, fmt.Errorf("price rounds to zero at tickSize %g", tick)
	}
	aligned := new(big.Rat).Mul(new(big.Rat).SetInt(units), t)
	decimals := 2
	if i := strings.IndexByte(tickText, '.'); i >= 0 && len(tickText)-i-1 > decimals {
		decimals = len(tickText) - i - 1
	}
	text := aligned.FloatString(decimals)
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(parsed, 0) {
		return "", 0, fmt.Errorf("quantized price exceeds float range")
	}
	return text, parsed, nil
}

type StopLevels struct {
	TP, SL         float64
	TPText, SLText string
}

// OneStop validates and aligns a single TP/SL without inventing a missing level.
func OneStop(side string, entry, value, tick float64, tp, breakeven bool) (string, float64, error) {
	side = NormalizeSide(side)
	valid := func(price float64) bool {
		if side == "LONG" {
			if tp {
				return price > entry
			}
			return breakeven || price < entry
		}
		if side == "SHORT" {
			if tp {
				return price < entry
			}
			return breakeven || price > entry
		}
		return false
	}
	if entry <= 0 || math.IsNaN(entry) || math.IsInf(entry, 0) || !valid(value) {
		return "", 0, fmt.Errorf("invalid %s stop relative to entry %g", side, entry)
	}
	up := (side == "SHORT" && tp) || (side == "LONG" && !tp)
	text, price, err := Quantize(value, tick, up)
	if err != nil {
		return "", 0, err
	}
	if !valid(price) {
		return "", 0, fmt.Errorf("quantized stop collapses to/crosses entry: price=%s entry=%g", text, entry)
	}
	return text, price, nil
}

func ValidateStops(side string, entry, tp, sl float64, breakeven bool) error {
	if entry <= 0 || math.IsNaN(entry) || math.IsInf(entry, 0) {
		return fmt.Errorf("invalid entry %g", entry)
	}
	if tp <= 0 || sl <= 0 || math.IsNaN(tp) || math.IsNaN(sl) || math.IsInf(tp, 0) || math.IsInf(sl, 0) {
		return fmt.Errorf("TP/SL must be finite and positive")
	}
	switch NormalizeSide(side) {
	case "LONG":
		if tp <= entry || (!breakeven && sl >= entry) || sl >= tp {
			return fmt.Errorf("LONG TP/SL invariant violated: entry=%g TP=%g SL=%g breakeven=%t", entry, tp, sl, breakeven)
		}
	case "SHORT":
		if tp >= entry || (!breakeven && sl <= entry) || sl <= tp {
			return fmt.Errorf("SHORT TP/SL invariant violated: entry=%g TP=%g SL=%g breakeven=%t", entry, tp, sl, breakeven)
		}
	default:
		return fmt.Errorf("unknown position side %q", side)
	}
	return nil
}

// Stops rounds toward entry: LONG TP down/SL up; SHORT TP up/SL down.
// Explicit breakeven intents may retain SL == entry; ordinary stops may not.
// Validation uses values parsed from the exact strings that enter JSON.
func Stops(side string, entry, tp, sl, tick float64, breakeven bool) (StopLevels, error) {
	var out StopLevels
	if err := ValidateStops(side, entry, tp, sl, breakeven); err != nil {
		return out, err
	}
	long := NormalizeSide(side) == "LONG"
	var err error
	out.TPText, out.TP, err = Quantize(tp, tick, !long)
	if err != nil {
		return out, err
	}
	out.SLText, out.SL, err = Quantize(sl, tick, long)
	if err != nil {
		return out, err
	}
	if err = ValidateStops(side, entry, out.TP, out.SL, breakeven); err != nil {
		return out, fmt.Errorf("invalid quantized payload: %w", err)
	}
	return out, nil
}
