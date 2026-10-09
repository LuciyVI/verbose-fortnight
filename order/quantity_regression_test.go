package order

import (
	"math"
	"math/big"
	"strconv"
	"testing"
)

func TestFormatQtyDecimalSteps(t *testing.T) {
	for _, tc := range []struct {
		qty, step float64
		want      string
	}{
		{2.9, 1, "2"}, {0.29, 0.1, "0.2"}, {0.1234, 0.001, "0.123"}, {1.37, 0.25, "1.25"}, {7.8, 2.5, "7.5"}, {3.1e-20, 1e-20, "0.00000000000000000003"}, {math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64, strconv.FormatFloat(math.SmallestNonzeroFloat64, 'f', -1, 64)},
	} {
		if got := (&OrderManager{}).FormatQty(tc.qty, tc.step); got != tc.want {
			t.Errorf("qty=%g step=%g got=%s want=%s", tc.qty, tc.step, got, tc.want)
		}
	}
}
func TestFormatQtyInvalidInputs(t *testing.T) {
	for _, step := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := (&OrderManager{}).FormatQty(1, step); got != "" {
			t.Errorf("invalid step=%g got=%q", step, got)
		}
	}
	for _, qty := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := (&OrderManager{}).FormatQty(qty, 0.1); got != "" {
			t.Errorf("invalid qty=%g got=%q", qty, got)
		}
	}
}

func FuzzFormatQty(f *testing.F) {
	for _, seed := range [][2]float64{{1.37, 0.25}, {1, 0}, {math.NaN(), 1}, {1, math.Inf(1)}, {1e-300, 1e-310}, {math.MaxFloat64, math.SmallestNonzeroFloat64}, {0.3, 0.1}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, qty, step float64) {
		got := (&OrderManager{}).FormatQty(qty, step)
		valid := qty > 0 && step > 0 && !math.IsNaN(qty) && !math.IsNaN(step) && !math.IsInf(qty, 0) && !math.IsInf(step, 0)
		if !valid {
			if got != "" {
				t.Fatalf("invalid input returned %q", got)
			}
			return
		}
		if got == "" {
			return
		} // quantity below one step
		q, ok := new(big.Rat).SetString(got)
		if !ok || q.Sign() <= 0 {
			t.Fatalf("invalid decimal %q", got)
		}
		s, _ := new(big.Rat).SetString(strconv.FormatFloat(step, 'f', -1, 64))
		original, _ := new(big.Rat).SetString(strconv.FormatFloat(qty, 'f', -1, 64))
		if q.Cmp(original) > 0 || !new(big.Rat).Quo(q, s).IsInt() {
			t.Fatalf("quantity exceeds request or violates step: qty=%g step=%g got=%s", qty, step, got)
		}
	})
}
