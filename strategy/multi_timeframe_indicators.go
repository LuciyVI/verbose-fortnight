package strategy

import (
	"math"
	"verbose-fortnight/indicators"
	"verbose-fortnight/strategy/signals"
)

// MultiTimeframeIndicators calculates indicators across multiple timeframes
type MultiTimeframeIndicators struct {
	// We'll store the data for different timeframes
	data signals.MultiTimeframeData
}

// NewMultiTimeframeIndicators creates a new multi-timeframe indicators instance
func NewMultiTimeframeIndicators() *MultiTimeframeIndicators {
	return &MultiTimeframeIndicators{
		data: signals.MultiTimeframeData{
			Data: make(map[signals.SignalSource][]float64),
		},
	}
}

// CalculateSMA calculates SMA across multiple timeframes
func (mti *MultiTimeframeIndicators) CalculateSMA(closes []float64, period int) map[signals.SignalSource]float64 {
	results := make(map[signals.SignalSource]float64)
	
	// Calculate SMA for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < period {
			continue
		}
		
		smaResult := indicators.SMA(data[len(data)-period:])
		results[source] = smaResult
	}
	
	// Calculate SMA for the current timeframe as well
	if len(closes) >= period {
		smaResult := indicators.SMA(closes[len(closes)-period:])
		results[signals.Source1Sec] = smaResult
	}
	
	return results
}

// CalculateMACD calculates MACD across multiple timeframes
func (mti *MultiTimeframeIndicators) CalculateMACD(closes []float64) map[signals.SignalSource]indicators.MACDResult {
	results := make(map[signals.SignalSource]indicators.MACDResult)
	
	// Calculate MACD for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < 26 {
			continue
		}
		
		macdLine, signalLine := indicators.MACD(data)
		results[source] = indicators.MACDResult{
			MACDLine:   macdLine,
			SignalLine: signalLine,
			Histogram:  macdLine - signalLine,
		}
	}
	
	// Calculate MACD for the current timeframe as well
	if len(closes) >= 26 {
		macdLine, signalLine := indicators.MACD(closes)
		results[signals.Source1Sec] = indicators.MACDResult{
			MACDLine:   macdLine,
			SignalLine: signalLine,
			Histogram:  macdLine - signalLine,
		}
	}
	
	return results
}

// CalculateBollingerBands calculates Bollinger Bands across multiple timeframes
func (mti *MultiTimeframeIndicators) CalculateBollingerBands(closes []float64, windowSize int, bbMult float64) map[signals.SignalSource]indicators.BollingerBands {
	results := make(map[signals.SignalSource]indicators.BollingerBands)
	
	// Calculate Bollinger Bands for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < windowSize {
			continue
		}
		
		upper, middle, lower := indicators.CalculateBollingerBands(data, windowSize, bbMult)
		results[source] = indicators.BollingerBands{
			Upper:  upper,
			Middle: middle,
			Lower:  lower,
		}
	}
	
	// Calculate Bollinger Bands for the current timeframe as well
	if len(closes) >= windowSize {
		upper, middle, lower := indicators.CalculateBollingerBands(closes, windowSize, bbMult)
		results[signals.Source1Sec] = indicators.BollingerBands{
			Upper:  upper,
			Middle: middle,
			Lower:  lower,
		}
	}
	
	return results
}

// CalculateRSI calculates RSI across multiple timeframes
func (mti *MultiTimeframeIndicators) CalculateRSI(closes []float64, period int) map[signals.SignalSource]float64 {
	results := make(map[signals.SignalSource]float64)
	
	// Calculate RSI for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < period+1 {
			continue
		}
		
		rsiValues := indicators.RSI(data, period)
		if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
			results[source] = rsiValues[len(rsiValues)-1]
		}
	}
	
	// Calculate RSI for the current timeframe as well
	if len(closes) >= period+1 {
		rsiValues := indicators.RSI(closes, period)
		if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
			results[signals.Source1Sec] = rsiValues[len(rsiValues)-1]
		}
	}
	
	return results
}

// IsGoldenCross checks for golden cross across multiple timeframes
func (mti *MultiTimeframeIndicators) IsGoldenCross(closes []float64) map[signals.SignalSource]bool {
	results := make(map[signals.SignalSource]bool)
	
	// Check for golden cross for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < 27 {
			continue
		}
		
		results[source] = indicators.GoldenCross(data)
	}
	
	// Check for golden cross for the current timeframe as well
	if len(closes) >= 27 {
		results[signals.Source1Sec] = indicators.GoldenCross(closes)
	}
	
	return results
}

// IsDeathCross checks for death cross across multiple timeframes
func (mti *MultiTimeframeIndicators) IsDeathCross(closes []float64) map[signals.SignalSource]bool {
	results := make(map[signals.SignalSource]bool)
	
	// Check for death cross for each available timeframe
	for _, source := range mti.data.GetAllSources() {
		data, exists := mti.data.GetData(source)
		if !exists || len(data) < 27 {
			continue
		}
		
		results[source] = indicators.DeathCross(data)
	}
	
	// Check for death cross for the current timeframe as well
	if len(closes) >= 27 {
		results[signals.Source1Sec] = indicators.DeathCross(closes)
	}
	
	return results
}

// GetLatestClose gets the latest close price for a timeframe
func (mti *MultiTimeframeIndicators) GetLatestClose(source signals.SignalSource) (float64, bool) {
	data, exists := mti.data.GetData(source)
	if !exists || len(data) == 0 {
		return 0, false
	}
	return data[len(data)-1], true
}