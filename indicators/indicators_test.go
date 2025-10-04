package indicators

import (
	"math"
	"testing"
)

// Тип для удобного создания тестовых данных
type orderbook struct {
	bids [][]string // [[price, size], ...]
	asks [][]string
}

func TestVolumeWeightedPrice(t *testing.T) {
	// Test data setup
	orderbook := map[string]float64{
		"50000": 100,
		"50100": 400,
		"50200": 800,
	}

	// Test LONG side (asks) - looking for lowest price that meets threshold
	price := VolumeWeightedPrice(orderbook, false, 900) // false = sell/ask (looking for lowest price first)
	expected := 50000.0  // The first price level is 50000 with size 100, cumulative = 100 < 900
	// Then 50100 with size 400, cumulative = 500 < 900
	// Then 50200 with size 800, cumulative = 1300 >= 900, so it should return 50200
	expected = 50200.0
	if math.Abs(price-expected) > 1 {
		t.Errorf("Expected ~%.0f, got %.2f", expected, price)
	}

	// Test SHORT side (bids) - looking for highest price that meets threshold
	price = VolumeWeightedPrice(orderbook, true, 900) // true = buy/bid
	expected = 50100.0
	if math.Abs(price-expected) > 1 {
		t.Errorf("Expected ~%.0f, got %.2f", expected, price)
	}

	// Test case with not enough liquidity
	orderbookLow := map[string]float64{
		"50000": 10,
	}
	price = VolumeWeightedPrice(orderbookLow, false, 900)
	if price != 50000 {
		t.Errorf("Expected 50000 for not enough liquidity, got %.2f", price)
	}
}

func TestSMA(t *testing.T) {
	data := []float64{10, 20, 30, 40, 50}
	result := SMA(data)
	expected := 30.0
	if result != expected {
		t.Errorf("Expected %.1f, got %.2f", expected, result)
	}

	// Test empty slice
	emptyData := []float64{}
	result = SMA(emptyData)
	if result != 0 {
		t.Errorf("Expected 0 for empty slice, got %.2f", result)
	}
}

func TestStdDev(t *testing.T) {
	data := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	result := StdDev(data)
	expected := 2.0
	if math.Abs(result-expected) > 0.1 {
		t.Errorf("Expected ~%.1f, got %.2f", expected, result)
	}
}

func TestMaxMinSlice(t *testing.T) {
	data := []float64{1, 5, 3, 9, 2}
	max := MaxSlice(data)
	if max != 9 {
		t.Errorf("Expected max 9, got %.2f", max)
	}

	min := MinSlice(data)
	if min != 1 {
		t.Errorf("Expected min 1, got %.2f", min)
	}

	// Test empty slice
	empty := []float64{}
	max = MaxSlice(empty)
	if max != 0 {
		t.Errorf("Expected 0 for empty max slice, got %.2f", max)
	}

	min = MinSlice(empty)
	if min != 0 {
		t.Errorf("Expected 0 for empty min slice, got %.2f", min)
	}
}