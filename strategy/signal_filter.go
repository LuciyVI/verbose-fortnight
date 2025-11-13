package strategy

import (
	"verbose-fortnight/strategy/signals"
)

// SignalFilter applies filters to ensure signal quality and confirmation
type SignalFilter struct {
	// Buffer for signal confirmation over time
	signalBuffer map[signals.SignalType][]signals.Signal
}

// NewSignalFilter creates a new signal filter instance
func NewSignalFilter() *SignalFilter {
	return &SignalFilter{
		signalBuffer: make(map[signals.SignalType][]signals.Signal),
	}
}

// FilterSignal applies various filters to validate a signal
func (sf *SignalFilter) FilterSignal(signal signals.Signal, regime signals.MarketRegime, currentPrice float64) (signals.Signal, bool) {
	// First, check if signal direction is appropriate for the current regime
	if !sf.isValidForRegime(signal, regime) {
		return signal, false
	}
	
	// Check if the signal is strong enough based on confidence level
	if signal.Confidence < 0.3 { // Minimum confidence threshold
		return signal, false
	}
	
	// Apply specific filters based on signal type
	switch signal.Type {
	case signals.SMASignal:
		return sf.filterSMASignal(signal, currentPrice)
	case signals.MACDSignal:
		return sf.filterMACDSignal(signal)
	case signals.BollingerSignal:
		return sf.filterBollingerSignal(signal, currentPrice)
	case signals.RSISignal:
		return sf.filterRSISignal(signal)
	case signals.OrderbookSignal:
		return sf.filterOrderbookSignal(signal)
	default:
		return signal, true // Allow other signal types by default
	}
}

// isValidForRegime checks if the signal direction is appropriate for the current regime
func (sf *SignalFilter) isValidForRegime(signal signals.Signal, regime signals.MarketRegime) bool {
	// For now, we'll allow all signals that match the regime logic,
	// but in the future we might want to implement additional checks
	return true
}

// filterSMASignal applies specific filters for SMA signals
func (sf *SignalFilter) filterSMASignal(signal signals.Signal, currentPrice float64) (signals.Signal, bool) {
	smaValue, ok := signal.Params["sma_value"].(float64)
	if !ok {
		return signal, false
	}
	
	// Verify the signal makes sense based on price position relative to SMA
	priceDiff := currentPrice - smaValue
	isLong := signal.Direction == "LONG"
	
	if isLong {
		// For LONG signals, price should be above SMA
		if priceDiff < 0 {
			return signal, false
		}
	} else {
		// For SHORT signals, price should be below SMA
		if priceDiff > 0 {
			return signal, false
		}
	}
	
	return signal, true
}

// filterMACDSignal applies specific filters for MACD signals
func (sf *SignalFilter) filterMACDSignal(signal signals.Signal) (signals.Signal, bool) {
	histogram, ok1 := signal.Params["histogram"].(float64)
	macdLine, ok2 := signal.Params["macd_line"].(float64)
	signalLine, ok3 := signal.Params["signal_line"].(float64)
	
	if !ok1 || !ok2 || !ok3 {
		return signal, false
	}
	
	isLong := signal.Direction == "LONG"
	
	if isLong {
		// For LONG signals, histogram should be positive and MACD above signal line
		if histogram <= 0 || macdLine <= signalLine {
			return signal, false
		}
	} else {
		// For SHORT signals, histogram should be negative and MACD below signal line
		if histogram >= 0 || macdLine >= signalLine {
			return signal, false
		}
	}
	
	return signal, true
}

// filterBollingerSignal applies specific filters for Bollinger Band signals
func (sf *SignalFilter) filterBollingerSignal(signal signals.Signal, currentPrice float64) (signals.Signal, bool) {
	upperBand, ok1 := signal.Params["upper_band"].(float64)
	_, ok2 := signal.Params["middle_band"].(float64)  // middleBand is used for additional validation if needed
	lowerBand, ok3 := signal.Params["lower_band"].(float64)
	
	if !ok1 || !ok2 || !ok3 {
		return signal, false
	}
	
	isLong := signal.Direction == "LONG"
	
	if isLong {
		// For LONG signals, price should be near or below lower band
		if currentPrice > lowerBand {
			return signal, false
		}
	} else {
		// For SHORT signals, price should be near or above upper band
		if currentPrice < upperBand {
			return signal, false
		}
	}
	
	return signal, true
}

// filterRSISignal applies specific filters for RSI signals
func (sf *SignalFilter) filterRSISignal(signal signals.Signal) (signals.Signal, bool) {
	rsiValue, ok := signal.Params["rsi_value"].(float64)
	if !ok {
		return signal, false
	}
	
	isLong := signal.Direction == "LONG"
	
	if isLong {
		// For LONG signals in counter-trend mode, RSI should be oversold
		// For LONG signals in trend mode, RSI should be bullish but not overbought
		if rsiValue > 70 { // Too high for a LONG in any mode
			return signal, false
		}
	} else {
		// For SHORT signals in counter-trend mode, RSI should be overbought
		// For SHORT signals in trend mode, RSI should be bearish but not oversold
		if rsiValue < 30 { // Too low for a SHORT in any mode
			return signal, false
		}
	}
	
	return signal, true
}

// filterOrderbookSignal applies specific filters for orderbook signals
func (sf *SignalFilter) filterOrderbookSignal(signal signals.Signal) (signals.Signal, bool) {
	ratio, ok := signal.Params["ratio"].(float64)
	if !ok {
		return signal, false
	}
	
	// Ensure the ratio meets minimum threshold for a valid signal
	// (e.g., at least 10% more depth on one side than the other)
	if ratio < 1.1 && ratio > 0.9 {
		return signal, false
	}
	
	return signal, true
}

// AddToBuffer adds a signal to the buffer for temporal confirmation
func (sf *SignalFilter) AddToBuffer(signal signals.Signal) {
	if sf.signalBuffer[signal.Type] == nil {
		sf.signalBuffer[signal.Type] = make([]signals.Signal, 0)
	}
	sf.signalBuffer[signal.Type] = append(sf.signalBuffer[signal.Type], signal)
}

// ConfirmSignal checks if a signal is confirmed by recent signals of the same type
func (sf *SignalFilter) ConfirmSignal(signal signals.Signal) bool {
	signals, exists := sf.signalBuffer[signal.Type]
	if !exists || len(signals) == 0 {
		return false
	}
	
	// Count how many recent signals agree with the current signal
	confirmationCount := 0
	totalCount := len(signals)
	
	for _, bufferedSignal := range signals {
		if bufferedSignal.Direction == signal.Direction {
			confirmationCount++
		}
	}
	
	// Require at least 50% confirmation from recent signals of the same type
	return float64(confirmationCount)/float64(totalCount) >= 0.5
}

// ClearBuffer clears the signal buffer
func (sf *SignalFilter) ClearBuffer() {
	sf.signalBuffer = make(map[signals.SignalType][]signals.Signal)
}

// GetMultiTimeframeConfluence checks for signals across multiple timeframes
func (sf *SignalFilter) GetMultiTimeframeConfluence(inputSignals []signals.Signal) signals.Signal {
	if len(inputSignals) == 0 {
		return signals.Signal{}
	}
	
	// Count signals by direction
	longCount := 0
	shortCount := 0
	var avgConfidence float64
	
	for _, signal := range inputSignals {
		if signal.Direction == "LONG" {
			longCount++
		} else if signal.Direction == "SHORT" {
			shortCount++
		}
		avgConfidence += signal.Confidence
	}
	
	avgConfidence /= float64(len(inputSignals))
	
	// Determine the dominant direction
	var direction string
	if longCount > shortCount {
		direction = "LONG"
	} else if shortCount > longCount {
		direction = "SHORT"
	} else {
		// If equal, return empty signal (no clear direction)
		return signals.Signal{}
	}
	
	// Create a confluence signal with higher weight
	confluenceWeight := 0.9 // High weight for confluence
	if len(inputSignals) < 2 {
		confluenceWeight = 0.5 // Lower weight if only one timeframe confirms
	}
	
	return signals.Signal{
		Type:       "MultiTimeframe",
		Source:     signals.Source1Sec, // Placeholder - in real implementation, would be more specific
		Direction:  direction,
		Confidence: avgConfidence,
		ClosePrice: inputSignals[0].ClosePrice, // Use the first signal's price
		Time:       inputSignals[0].Time,       // Use the first signal's time
		Weight:     confluenceWeight,
		Regime:     inputSignals[0].Regime,
		Params: map[string]interface{}{
			"confluence_count": len(inputSignals),
			"long_signals":     longCount,
			"short_signals":    shortCount,
		},
	}
}