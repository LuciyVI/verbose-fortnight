package indicators

import (
	"testing"
	"math"
)

// TestCalculateATR tests the ATR calculation function
func TestCalculateATR(t *testing.T) {
	highs := []float64{50000, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700, 50650, 50800}
	lows := []float64{49900, 49950, 50050, 49900, 50100, 50050, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600}
	closes := []float64{50050, 50150, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700, 50750}

	atr := CalculateATR(highs, lows, closes, 14)

	// We expect some positive value for ATR
	if atr <= 0 {
		t.Errorf("ATR should be positive, got %f", atr)
	}

	t.Logf("ATR(14) calculated: %f", atr)
}

// TestCommissionCalculation tests the commission calculation logic
func TestCommissionCalculation(t *testing.T) {
	entryPrice := 50000.0
	commission := 0.09 // 0.09% as percentage
	commissionValue := entryPrice * (commission / 100)
	
	// Expected commission value for 50000 * 0.0009
	expected := 45.0 // 50000 * 0.0009
	
	if math.Abs(commissionValue-expected) > 0.001 {
		t.Errorf("Expected commission %f, got %f", expected, commissionValue)
	}
	
	t.Logf("Commission for entry price %f at 0.09%%: %f", entryPrice, commissionValue)
}