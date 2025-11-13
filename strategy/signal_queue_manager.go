package strategy

import (
	"verbose-fortnight/strategy/signals"
	"sync"
	"time"
)

// SignalQueueManager handles the queue of signals with priority and timing
type SignalQueueManager struct {
	queue       *signals.SignalQueue
	compositeScore *signals.CompositeSignalScore
	mutex       sync.Mutex
}

// NewSignalQueueManager creates a new signal queue manager instance
func NewSignalQueueManager() *SignalQueueManager {
	return &SignalQueueManager{
		queue:          &signals.SignalQueue{Queue: make([]signals.Signal, 0)},
		compositeScore: &signals.CompositeSignalScore{},
	}
}

// AddSignal adds a signal to the queue after validating and scoring it
func (sqm *SignalQueueManager) AddSignal(signal signals.Signal) {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	// Update the composite score with the new signal
	sqm.compositeScore.UpdateScore(signal)
	
	// Add to the queue
	sqm.queue.Enqueue(signal)
}

// GetNextSignal retrieves the next highest priority signal from the queue
func (sqm *SignalQueueManager) GetNextSignal() (signals.Signal, bool) {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	return sqm.queue.Dequeue()
}

// PeekSignal returns the highest priority signal without removing it
func (sqm *SignalQueueManager) PeekSignal() (signals.Signal, bool) {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	return sqm.queue.Peek()
}

// GetCompositeScore returns the current composite signal score
func (sqm *SignalQueueManager) GetCompositeScore() signals.CompositeSignalScore {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	return *sqm.compositeScore
}

// ProcessSignalBatch processes a batch of signals and combines them into a single decision
func (sqm *SignalQueueManager) ProcessSignalBatch(inputSignals []signals.Signal, minConfidence float64) (signals.Signal, bool) {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	if len(inputSignals) == 0 {
		return signals.Signal{}, false
	}
	
	// Filter signals by minimum confidence
	validSignals := make([]signals.Signal, 0)
	for _, signal := range inputSignals {
		if signal.Confidence >= minConfidence {
			validSignals = append(validSignals, signal)
			// Update composite score with each valid signal
			sqm.compositeScore.UpdateScore(signal)
		}
	}
	
	if len(validSignals) == 0 {
		return signals.Signal{}, false
	}
	
	// Calculate combined scores
	longScore := 0.0
	shortScore := 0.0
	
	for _, signal := range validSignals {
		weightedScore := signal.Confidence * signal.Weight
		if signal.Direction == "LONG" {
			longScore += weightedScore
		} else if signal.Direction == "SHORT" {
			shortScore += weightedScore
		}
	}
	
	// Determine the dominant direction
	var direction string
	if longScore > shortScore {
		direction = "LONG"
	} else if shortScore > longScore {
		direction = "SHORT"
	} else {
		// If equal, return no clear direction
		return signals.Signal{}, false
	}
	
	// Calculate combined confidence as the average of valid signals
	totalConfidence := 0.0
	for _, signal := range validSignals {
		totalConfidence += signal.Confidence
	}
	avgConfidence := totalConfidence / float64(len(validSignals))
	
	// Calculate combined weight
	totalWeight := 0.0
	for _, signal := range validSignals {
		totalWeight += signal.Weight
	}
	avgWeight := totalWeight / float64(len(validSignals))
	
	// Use the most recent time from valid signals
	var latestTime time.Time
	for _, signal := range validSignals {
		if signal.Time.After(latestTime) {
			latestTime = signal.Time
		}
	}
	
	// Create a combined signal representing the batch decision
	combinedSignal := signals.Signal{
		Type:       "CombinedBatch",
		Source:     validSignals[0].Source, // Use the source of the first signal as reference
		Direction:  direction,
		Confidence: avgConfidence,
		ClosePrice: validSignals[0].ClosePrice, // Use the price of the first signal
		Time:       latestTime,
		Weight:     avgWeight,
		Regime:     validSignals[0].Regime, // Use the regime of the first signal
		Params: map[string]interface{}{
			"valid_signals_count": len(validSignals),
			"total_long_score":    longScore,
			"total_short_score":   shortScore,
			"batch_signals":       validSignals,
		},
	}
	
	// Add the combined signal to the queue
	sqm.queue.Enqueue(combinedSignal)
	
	return combinedSignal, true
}

// ClearQueue clears all signals from the queue
func (sqm *SignalQueueManager) ClearQueue() {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	sqm.queue.Clear()
	sqm.compositeScore.ResetScore()
}

// GetQueueSize returns the current size of the queue
func (sqm *SignalQueueManager) GetQueueSize() int {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	return len(sqm.queue.Queue)
}

// DrainSignalsForTimeframe returns all signals for a specific timeframe and removes them from the queue
func (sqm *SignalQueueManager) DrainSignalsForTimeframe(source signals.SignalSource) []signals.Signal {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	var drainedSignals []signals.Signal
	var remainingSignals []signals.Signal
	
	for _, signal := range sqm.queue.Queue {
		if signal.Source == source {
			drainedSignals = append(drainedSignals, signal)
		} else {
			remainingSignals = append(remainingSignals, signal)
		}
	}
	
	// Update the queue with remaining signals
	sqm.queue.Queue = remainingSignals
	
	return drainedSignals
}

// UpdateRegime updates the composite score's regime
func (sqm *SignalQueueManager) UpdateRegime(regime signals.MarketRegime) {
	sqm.mutex.Lock()
	defer sqm.mutex.Unlock()
	
	sqm.compositeScore.Regime = regime
}