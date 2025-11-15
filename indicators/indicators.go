package indicators

import (
	"math"
)

// SMA calculates the Simple Moving Average
func SMA(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

// StdDev calculates the standard deviation
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

// CalcATR calculates the Average True Range
func CalcATR(highs, lows, closes []float64, period int) float64 {
	if len(highs) < period || len(lows) < period || len(closes) < period {
		return 0
	}
	
	var trSum float64
	for i := len(closes) - period; i < len(closes); i++ {
		high := highs[i]
		low := lows[i]
		closePrev := closes[i]
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

// RSI calculates the Relative Strength Index
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

// CalculateEMACalculates the Exponential Moving Average
func CalculateEMA(src []float64, period int) float64 {
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

// CalculateMACD calculates the MACD (Moving Average Convergence Divergence)
func CalculateMACD(src []float64) (macdLine, signalLine float64) {
	if len(src) < 26 {
		return 0, 0
	}

	ema12 := CalculateEMA(src, 12)
	ema26 := CalculateEMA(src, 26)
	macdLine = ema12 - ema26

	// Keep track of MACD history to calculate signal line
	macdHistory := []float64{macdLine}
	if len(macdHistory) > 9 {
		macdHistory = macdHistory[1:]
	}

	signalLine = CalculateEMA(macdHistory, 9)
	return macdLine, signalLine
}

// Max returns the maximum of three values
func Max(a, b, c float64) float64 {
	max := a
	if b > max {
		max = b
	}
	if c > max {
		max = c
	}
	return max
}

// Min returns the minimum of three values
func Min(a, b, c float64) float64 {
	min := a
	if b < min {
		min = b
	}
	if c < min {
		min = c
	}
	return min
}

// MaxSlice returns the maximum value from a slice
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

// MinSlice returns the minimum value from a slice
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