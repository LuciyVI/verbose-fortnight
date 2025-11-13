package strategy

import (
	"math"
	"verbose-fortnight/indicators"
	"verbose-fortnight/strategy/signals"
)

// RegimeDetector analyzes market conditions and determines the current regime
type RegimeDetector struct {
	historicalCloses []float64
}

// NewRegimeDetector creates a new regime detector instance
func NewRegimeDetector() *RegimeDetector {
	return &RegimeDetector{
		historicalCloses: make([]float64, 0),
	}
}

// DetectRegime determines whether the market is in trending or ranging mode
func (rd *RegimeDetector) DetectRegime(closes []float64) signals.MarketRegime {
	if len(closes) < 50 {
		// Default to trending if not enough data
		return signals.TrendRegime
	}

	rangePerc := rd.calculateRangePercentage(closes)
	volatilityRegime := rd.detectVolatilityRegime(closes)
	trendStrength := rd.calculateTrendStrength(closes)

	// Log regime metrics for debugging
	if len(closes) > 0 {
		lastPrice := closes[len(closes)-1]
		// Here we would log the metrics, but we don't have access to a logger in this struct
		_ = lastPrice
	}

	// If we have a strong trend and low range behavior, it's trending
	if trendStrength > 0.6 && rangePerc < 2.0 {
		return signals.TrendRegime
	} else if volatilityRegime == "low" && rangePerc > 1.5 {
		// If low volatility but price is moving in range, it's ranging
		return signals.RangeRegime
	} else if trendStrength < 0.4 && rangePerc > 2.5 {
		// Weak trend with high range suggests ranging
		return signals.RangeRegime
	}

	// Default to trend detection if mixed signals
	if trendStrength > 0.5 {
		return signals.TrendRegime
	} else {
		return signals.RangeRegime
	}
}

// calculateRangePercentage calculates the percentage range of price movement
func (rd *RegimeDetector) calculateRangePercentage(closes []float64) float64 {
	if len(closes) == 0 {
		return 0
	}

	maxHigh := indicators.MaxSlice(closes)
	minLow := indicators.MinSlice(closes)

	if minLow <= 0 {
		return 0
	}

	return (maxHigh - minLow) / minLow * 100
}

// detectVolatilityRegime determines if the market has high or low volatility
func (rd *RegimeDetector) detectVolatilityRegime(closes []float64) string {
	if len(closes) < 50 {
		return "unknown"
	}

	// Use the most recent 20 closes to calculate recent volatility
	recent := closes[len(closes)-20:]

	// Calculate standard deviation as a volatility measure
	mean := 0.0
	for _, c := range recent {
		mean += c
	}
	mean /= float64(len(recent))

	var variance float64
	for _, c := range recent {
		variance += (c - mean) * (c - mean)
	}
	variance /= float64(len(recent))
	stdDev := math.Sqrt(variance)

	// Calculate coefficient of variation
	if mean != 0 {
		cv := stdDev / math.Abs(mean) * 100

		if cv > 5.0 { // High threshold for high volatility in crypto
			return "high"
		} else {
			return "low"
		}
	}

	return "unknown"
}

// calculateTrendStrength measures the strength of the current trend
func (rd *RegimeDetector) calculateTrendStrength(closes []float64) float64 {
	if len(closes) < 2 {
		return 0
	}

	totalChange := math.Abs(closes[len(closes)-1] - closes[0])
	totalDistance := 0.0

	// Sum of absolute changes between each candle to see total movement vs net trend
	for i := 1; i < len(closes); i++ {
		totalDistance += math.Abs(closes[i] - closes[i-1])
	}

	if totalDistance == 0 {
		return 0
	}

	// A strong trend would have totalChange close to totalDistance
	// A ranging market would have totalDistance much larger than totalChange
	trendRatio := totalChange / totalDistance

	// Normalize to 0-1 scale
	return math.Min(trendRatio*2, 1.0) // Multiply by 2 to increase sensitivity
}