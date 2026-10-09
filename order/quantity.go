package order

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

func positiveFinite(v float64) bool { return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0) }

func formatQty(qty, step float64) string {
	if !positiveFinite(qty) || !positiveFinite(step) {
		return ""
	}
	stepText := strconv.FormatFloat(step, 'f', -1, 64)
	q, _ := new(big.Rat).SetString(strconv.FormatFloat(qty, 'f', -1, 64))
	s, _ := new(big.Rat).SetString(stepText)
	ratio := new(big.Rat).Quo(q, s)
	units := new(big.Int).Quo(ratio.Num(), ratio.Denom())
	if units.Sign() == 0 {
		return ""
	}
	aligned := new(big.Rat).Mul(new(big.Rat).SetInt(units), s)
	dec := 0
	if i := strings.IndexByte(stepText, '.'); i >= 0 {
		dec = len(stepText) - i - 1
	}
	return aligned.FloatString(dec)
}
