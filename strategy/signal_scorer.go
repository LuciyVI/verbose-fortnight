package strategy

import (
	"math"
	"time"
	"verbose-fortnight/indicators"
	"verbose-fortnight/strategy/signals"
)

// SignalScorer calculates weighted scores for trading signals
type SignalScorer struct {
	// Define weights for different indicators and market regimes
	weights map[signals.MarketRegime]map[signals.SignalType]float64
}

// NewSignalScorer creates a new signal scorer instance
func NewSignalScorer() *SignalScorer {
	return &SignalScorer{
		weights: map[signals.MarketRegime]map[signals.SignalType]float64{
			signals.TrendRegime: {
				signals.SMASignal:      0.7,
				signals.MACDSignal:     0.8,
				signals.BollingerSignal: 0.5,
				signals.RSISignal:      0.3,
				signals.OrderbookSignal: 0.5,
				signals.VolumeSignal:   0.4,
			},
			signals.RangeRegime: {
				signals.SMASignal:      0.5,
				signals.MACDSignal:     0.4,
				signals.BollingerSignal: 0.7,
				signals.RSISignal:      0.6,
				signals.OrderbookSignal: 0.5,
				signals.VolumeSignal:   0.4,
			},
		},
	}
}

// ScoreSMASignal calculates the score for SMA-based signals
func (ss *SignalScorer) ScoreSMASignal(closePrice float64, smaValue float64, regime signals.MarketRegime, source signals.SignalSource) signals.Signal {
	// Determine direction based on regime
	var direction string
	var confidence float64
	var weight float64
	
	if regime == signals.TrendRegime {
		// Trend following: LONG when price > SMA, SHORT when price < SMA
		if closePrice > smaValue {
			direction = "LONG"
			priceRatio := (closePrice - smaValue) / smaValue
			confidence = math.Min(priceRatio*5.0, 1.0) // Cap confidence at 1.0
		} else {
			direction = "SHORT"
			priceRatio := (smaValue - closePrice) / smaValue
			confidence = math.Min(priceRatio*5.0, 1.0) // Cap confidence at 1.0
		}
	} else {
		// Counter-trend: LONG when price < SMA (expecting reversion), SHORT when price > SMA
		if closePrice < smaValue {
			direction = "LONG"
			priceRatio := (smaValue - closePrice) / smaValue
			confidence = math.Min(priceRatio*5.0, 1.0) // Cap confidence at 1.0
		} else {
			direction = "SHORT"
			priceRatio := (closePrice - smaValue) / smaValue
			confidence = math.Min(priceRatio*5.0, 1.0) // Cap confidence at 1.0
		}
	}
	
	weight = ss.weights[regime][signals.SMASignal]
	
	return signals.Signal{
		Type:       signals.SMASignal,
		Source:     source,
		Direction:  direction,
		Confidence: confidence,
		ClosePrice: closePrice,
		Time:       sourceToTime(source), // Placeholder - in real implementation, use actual time
		Weight:     weight,
		Regime:     regime,
		Params: map[string]interface{}{
			"sma_value": smaValue,
		},
	}
}

// ScoreMACDSignal calculates the score for MACD-based signals
func (ss *SignalScorer) ScoreMACDSignal(macdResult indicators.MACDResult, regime signals.MarketRegime, source signals.SignalSource) signals.Signal {
	var direction string
	var confidence float64
	var weight float64
	
	if regime == signals.TrendRegime {
		// Trend following with MACD
		if macdResult.Histogram > 0 && macdResult.MACDLine > macdResult.SignalLine {
			direction = "LONG" // Golden cross pattern
			confidence = math.Min(math.Abs(macdResult.Histogram)*2.0, 1.0)
		} else if macdResult.Histogram < 0 && macdResult.MACDLine < macdResult.SignalLine {
			direction = "SHORT" // Death cross pattern
			confidence = math.Min(math.Abs(macdResult.Histogram)*2.0, 1.0)
		} else {
			// Weak signal if lines are close to each other
			direction = ""
			confidence = 0.0
		}
	} else {
		// Counter-trend with MACD (reversal signals)
		if macdResult.Histogram < 0 && macdResult.MACDLine < macdResult.SignalLine {
			direction = "LONG" // Expecting reversal from bearish
			confidence = math.Min(math.Abs(macdResult.Histogram)*2.0, 1.0)
		} else if macdResult.Histogram > 0 && macdResult.MACDLine > macdResult.SignalLine {
			direction = "SHORT" // Expecting reversal from bullish
			confidence = math.Min(math.Abs(macdResult.Histogram)*2.0, 1.0)
		} else {
			// Weak signal if lines are close to each other
			direction = ""
			confidence = 0.0
		}
	}
	
	weight = ss.weights[regime][signals.MACDSignal]
	
	return signals.Signal{
		Type:       signals.MACDSignal,
		Source:     source,
		Direction:  direction,
		Confidence: confidence,
		ClosePrice: 0, // Will be set based on context
		Time:       sourceToTime(source), // Placeholder - in real implementation, use actual time
		Weight:     weight,
		Regime:     regime,
		Params: map[string]interface{}{
			"macd_line":   macdResult.MACDLine,
			"signal_line": macdResult.SignalLine,
			"histogram":   macdResult.Histogram,
		},
	}
}

// ScoreBollingerSignal calculates the score for Bollinger Band-based signals
func (ss *SignalScorer) ScoreBollingerSignal(closePrice float64, bbBands indicators.BollingerBands, regime signals.MarketRegime, source signals.SignalSource) signals.Signal {
	var direction string
	var confidence float64
	var weight float64
	
	if regime == signals.TrendRegime {
		// Trend following with Bollinger Bands
		if closePrice > bbBands.Upper {
			direction = "LONG" // Trend breakout above upper band
			priceRatio := (closePrice - bbBands.Upper) / bbBands.Middle
			confidence = math.Min(priceRatio*3.0, 1.0)
		} else if closePrice < bbBands.Lower {
			direction = "SHORT" // Trend breakdown below lower band
			priceRatio := (bbBands.Lower - closePrice) / bbBands.Middle
			confidence = math.Min(priceRatio*3.0, 1.0)
		} else {
			confidence = 0.0
			direction = ""
		}
	} else {
		// Counter-trend with Bollinger Bands (mean reversion)
		if closePrice > bbBands.Upper {
			direction = "SHORT" // Expecting reversion from upper band
			priceRatio := (closePrice - bbBands.Upper) / bbBands.Middle
			confidence = math.Min(priceRatio*3.0, 1.0)
		} else if closePrice < bbBands.Lower {
			direction = "LONG" // Expecting reversion from lower band
			priceRatio := (bbBands.Lower - closePrice) / bbBands.Middle
			confidence = math.Min(priceRatio*3.0, 1.0)
		} else {
			confidence = 0.0
			direction = ""
		}
	}
	
	weight = ss.weights[regime][signals.BollingerSignal]
	
	return signals.Signal{
		Type:       signals.BollingerSignal,
		Source:     source,
		Direction:  direction,
		Confidence: confidence,
		ClosePrice: closePrice,
		Time:       sourceToTime(source), // Placeholder - in real implementation, use actual time
		Weight:     weight,
		Regime:     regime,
		Params: map[string]interface{}{
			"upper_band": bbBands.Upper,
			"middle_band": bbBands.Middle,
			"lower_band": bbBands.Lower,
		},
	}
}

// ScoreRSISignal calculates the score for RSI-based signals
func (ss *SignalScorer) ScoreRSISignal(rsiValue float64, regime signals.MarketRegime, source signals.SignalSource) signals.Signal {
	var direction string
	var confidence float64
	var weight float64
	
	if regime == signals.TrendRegime {
		// In trend regime, use RSI for trend confirmation rather than reversal
		if rsiValue > 50 {
			direction = "LONG"
			confidence = math.Min((rsiValue-50)/50.0, 1.0) // Normalize to 0-1
		} else {
			direction = "SHORT"
			confidence = math.Min((50-rsiValue)/50.0, 1.0) // Normalize to 0-1
		}
	} else {
		// In range regime, use RSI for reversal signals (counter-trend)
		if rsiValue > 70 { // Overbought
			direction = "SHORT"
			confidence = math.Min((rsiValue-70)/30.0, 1.0) // Normalize to 0-1
		} else if rsiValue < 30 { // Oversold
			direction = "LONG"
			confidence = math.Min((30-rsiValue)/30.0, 1.0) // Normalize to 0-1
		} else {
			confidence = 0.0
			direction = ""
		}
	}
	
	weight = ss.weights[regime][signals.RSISignal]
	
	return signals.Signal{
		Type:       signals.RSISignal,
		Source:     source,
		Direction:  direction,
		Confidence: confidence,
		ClosePrice: 0, // Will be set based on context
		Time:       sourceToTime(source), // Placeholder - in real implementation, use actual time
		Weight:     weight,
		Regime:     regime,
		Params: map[string]interface{}{
			"rsi_value": rsiValue,
		},
	}
}

// ScoreOrderbookSignal calculates the score for orderbook-based signals
func (ss *SignalScorer) ScoreOrderbookSignal(bidDepth, askDepth float64, regime signals.MarketRegime, source signals.SignalSource) signals.Signal {
	var direction string
	var confidence float64
	var weight float64
	
	ratio := 0.0
	if askDepth != 0 {
		ratio = bidDepth / askDepth
	}
	
	if ratio > 1.1 {
		// Significant bid depth, potential upward pressure
		direction = "LONG"
		confidence = math.Min((ratio-1.0), 1.0)
	} else if ratio < 0.9 {
		// Significant ask depth, potential downward pressure
		direction = "SHORT"
		confidence = math.Min((1.0-(1.0/ratio)), 1.0)
	} else {
		confidence = 0.0
		direction = ""
	}
	
	weight = ss.weights[regime][signals.OrderbookSignal]
	
	return signals.Signal{
		Type:       signals.OrderbookSignal,
		Source:     source,
		Direction:  direction,
		Confidence: confidence,
		ClosePrice: 0, // Will be set based on context
		Time:       sourceToTime(source), // Placeholder - in real implementation, use actual time
		Weight:     weight,
		Regime:     regime,
		Params: map[string]interface{}{
			"bid_depth": bidDepth,
			"ask_depth": askDepth,
			"ratio":     ratio,
		},
	}
}

// Helper function to convert source to time - this is a placeholder
func sourceToTime(source signals.SignalSource) time.Time {
	// In a real implementation, you'd have the actual time for the signal
	// For now, return a placeholder
	return time.Time{}
}