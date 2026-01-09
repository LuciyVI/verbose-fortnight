package indicators

import (
	"math"
	"sort"
	"strconv"
)

// SMA calculates Simple Moving Average
func SMA(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	result := sum / float64(len(data))
	return result
}

// SMAWithPeriod calculates Simple Moving Average with a specific period
func SMAWithPeriod(data []float64, period int) float64 {
	if len(data) < period || period <= 0 {
		return 0
	}

	// Use the last 'period' number of elements
	startIndex := len(data) - period
	sum := 0.0
	for i := startIndex; i < len(data); i++ {
		sum += data[i]
	}
	result := sum / float64(period)
	return result
}

// StdDev calculates standard deviation
func StdDev(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	m := SMA(data)
	var sum float64
	for _, v := range data {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(data)))
}

// CalculateATR calculates Average True Range
func CalculateATR(highs, lows, closes []float64, period int) float64 {
	if len(highs) < period || len(lows) < period || len(closes) < period {
		return 0
	}
	var trSum float64
	for i := len(closes) - period; i < len(closes); i++ {
		high := highs[i]
		low := lows[i]
		var closePrev float64
		if i > 0 {
			closePrev = closes[i-1]
		} else {
			closePrev = closes[i]
		}
		tr := math.Max(
			high-low,
			math.Max(
				math.Abs(high-closePrev),
				math.Abs(low-closePrev),
			),
		)
		trSum += tr
	}
	return trSum / float64(period)
}

// RSI calculates Relative Strength Index
func RSI(src []float64, length int) []float64 {
	if len(src) < length+1 {
		return nil
	}
	out := make([]float64, len(src))
	var gain, loss float64
	for i := 1; i <= length; i++ {
		delta := src[i] - src[i-1]
		if delta >= 0 {
			gain += delta
		} else {
			loss -= delta
		}
	}
	avgGain := gain / float64(length)
	avgLoss := loss / float64(length)
	if avgLoss == 0 {
		out[length] = 100
	} else {
		rs := avgGain / avgLoss
		out[length] = 100 - 100/(1+rs)
	}
	for i := length + 1; i < len(src); i++ {
		delta := src[i] - src[i-1]
		if delta >= 0 {
			avgGain = (avgGain*(float64(length-1)) + delta) / float64(length)
			avgLoss = (avgLoss * (float64(length - 1))) / float64(length)
		} else {
			avgGain = (avgGain * (float64(length - 1))) / float64(length)
			avgLoss = (avgLoss*(float64(length-1)) - delta) / float64(length)
		}
		if avgLoss == 0 {
			out[i] = 100
		} else {
			rs := avgGain / avgLoss
			out[i] = 100 - 100/(1+rs)
		}
	}
	return out
}

// EMA calculates Exponential Moving Average
func EMA(src []float64, period int) float64 {
	if len(src) < period {
		return 0
	}

	multiplier := 2.0 / float64(period+1)
	ema := src[0]
	for i := 1; i < len(src); i++ {
		ema = (src[i] * multiplier) + (ema * (1 - multiplier))
	}
	return ema
}

// MACD calculates Moving Average Convergence Divergence
func MACD(src []float64) (macdLine, signalLine, histogram float64) {
	if len(src) < 26 {
		return 0, 0, 0
	}

	macdSeries := make([]float64, len(src))
	for i := range src {
		if i+1 < 26 {
			continue
		}
		short := EMA(src[:i+1], 12)
		long := EMA(src[:i+1], 26)
		macdSeries[i] = short - long
	}

	macdLine = macdSeries[len(macdSeries)-1]

	// Signal line: EMA of MACD series (9)
	nonZero := make([]float64, 0, len(macdSeries))
	for _, v := range macdSeries {
		if v != 0 {
			nonZero = append(nonZero, v)
		}
	}
	if len(nonZero) >= 9 {
		signalLine = EMA(nonZero, 9)
	}
	histogram = macdLine - signalLine
	return macdLine, signalLine, histogram
}

// CalculateBollingerBands calculates upper and lower bands
func CalculateBollingerBands(closes []float64, windowSize int, bbMult float64) (upper, middle, lower float64) {
	if len(closes) < windowSize {
		return 0, 0, 0
	}

	recent := closes[len(closes)-windowSize:]
	middle = SMA(recent)
	stdDev := StdDev(recent)

	upper = middle + bbMult*stdDev
	lower = middle - bbMult*stdDev

	return upper, middle, lower
}

// GoldenCross checks for golden cross pattern
func GoldenCross(closes []float64) bool {
	if len(closes) < 27 {
		return false
	}

	// Calculate MACD on previous and current data points
	prevData := closes[len(closes)-27 : len(closes)-1] // 26 elements
	currData := closes[len(closes)-26:]                // 26 elements

	prevMacd, _, _ := MACD(prevData)
	currMacd, _, _ := MACD(currData)

	// This is simplified - a real golden cross would compare MACD line to signal line
	return prevMacd < 0 && currMacd > 0
}

// MaxSlice returns the maximum value in a slice
func MaxSlice(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	max := arr[0]
	for _, v := range arr[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

// MinSlice returns the minimum value in a slice
func MinSlice(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	min := arr[0]
	for _, v := range arr[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

// VolumeWeightedPrice finds price level with significant volume
func VolumeWeightedPrice(orderbook map[string]float64, isBuy bool, thresholdQty float64) float64 {
	if len(orderbook) == 0 {
		return 0
	}

	// Create a slice of price-size pairs
	var pairs []struct {
		Price float64
		Size  float64
	}

	for priceStr, size := range orderbook {
		price, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			continue
		}
		pairs = append(pairs, struct {
			Price float64
			Size  float64
		}{price, size})
	}

	// Sort based on whether we're looking for buy (bids - descending) or sell (asks - ascending) orders
	if isBuy {
		// For buy orders, we want the highest prices first (descending)
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].Price > pairs[j].Price
		})
	} else {
		// For sell orders, we want the lowest prices first (ascending)
		sort.Slice(pairs, func(i, j int) bool {
			return pairs[i].Price < pairs[j].Price
		})
	}

	// Accumulate volume until we hit the threshold
	cumulativeQty := 0.0
	for _, pair := range pairs {
		cumulativeQty += pair.Size
		if cumulativeQty >= thresholdQty {
			return pair.Price
		}
	}

	// If we didn't reach the threshold, return the last price we saw
	if len(pairs) > 0 {
		return pairs[len(pairs)-1].Price
	}

	return 0
}
