// File: bollinger_strategy_bybit_test.go

package main

import (
	"math"
	"testing"
)

// Тип для удобного создания тестовых данных
type orderbook struct {
	bids [][]string // [[price, size], ...]
	asks [][]string
}

func TestCalcTakeProfit(t *testing.T) {
	tests := []struct {
		name          string
		ob            orderbook
		side          string  // "LONG" или "SHORT"
		thresholdQty  float64 // порог ликвидности (tpThresholdQty)
		expectedPrice float64 // ожидаемая цена TP
		expectZero    bool    // должен вернуться 0?
	}{
		{
			name: "LONG — первая стенка на ASK",
			ob: orderbook{
				asks: [][]string{
					{"50000", "100"},
					{"50100", "400"},
					{"50200", "800"}, // сумма = 1300 > threshold=900
				},
			},
			side:          "LONG",
			thresholdQty:  900,
			expectedPrice: 50100 * 0.9995,
		},
		{
			name: "SHORT — первая стенка на BID",
			ob: orderbook{
				bids: [][]string{
					{"50000", "100"},
					{"49900", "400"},
					{"49800", "800"}, // сумма = 1300 > threshold=900
				},
			},
			side:          "SHORT",
			thresholdQty:  900,
			expectedPrice: 49900 * 1.0005,
		},
		{
			name: "LONG — недостаточно ликвидности",
			ob: orderbook{
				asks: [][]string{{"50000", "10"}}, // сумма = 10 < threshold=900
			},
			side:         "LONG",
			thresholdQty: 900,
			expectZero:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applySnapshot(tt.ob.bids, tt.ob.asks)

			tp := calcTakeProfit(tt.side, tt.thresholdQty)
			if tt.expectZero {
				if tp != 0 {
					t.Errorf("ожидалось 0 (недостаточно ликвидности), получено %.2f", tp)
				}
				return
			}

			diff := math.Abs(tp - tt.expectedPrice)
			if diff > 0.1 { // допуск ±0.1 из-за округления
				t.Errorf("ожидано ~%.2f, получено %.2f (разница %.2f)", tt.expectedPrice, tp, diff)
			}
		})
	}
}
