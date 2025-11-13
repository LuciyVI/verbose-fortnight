package signals

import (
	"sync"
	"time"
)

// SignalType defines the type of trading signal
type SignalType string

const (
	SMASignal     SignalType = "SMA"
	MACDSignal    SignalType = "MACD"
	BollingerSignal SignalType = "BOLLINGER"
	RSISignal     SignalType = "RSI"
	OrderbookSignal SignalType = "ORDERBOOK"
	VolumeSignal  SignalType = "VOLUME"
)

// SignalSource defines the timeframe or source of a signal
type SignalSource string

const (
	Source1Sec  SignalSource = "1s"
	Source1Min  SignalSource = "1m"
	Source5Min  SignalSource = "5m"
	Source15Min SignalSource = "15m"
)

// MarketRegime represents the current market condition
type MarketRegime string

const (
	TrendRegime MarketRegime = "trend"
	RangeRegime MarketRegime = "range"
)

// Signal represents a trading signal with enhanced metadata
type Signal struct {
	Type       SignalType   `json:"type"`
	Source     SignalSource `json:"source"`
	Direction  string       `json:"direction"` // "LONG" or "SHORT"
	Confidence float64      `json:"confidence"` // 0.0 to 1.0
	ClosePrice float64      `json:"close_price"`
	Time       time.Time    `json:"time"`
	Weight     float64      `json:"weight"`
	Regime     MarketRegime `json:"regime"`
	Params     map[string]interface{} `json:"params"` // Additional parameters specific to the signal
}

// SignalQueue represents a queue of signals with priority
type SignalQueue struct {
	Queue []Signal
	Mutex sync.Mutex
}

// Enqueue adds a signal to the queue
func (sq *SignalQueue) Enqueue(signal Signal) {
	sq.Mutex.Lock()
	defer sq.Mutex.Unlock()
	
	// Insert by priority (highest confidence first)
	inserted := false
	for i, s := range sq.Queue {
		if signal.Confidence > s.Confidence {
			sq.Queue = append(sq.Queue[:i], append([]Signal{signal}, sq.Queue[i:]...)...)
			inserted = true
			break
		}
	}
	
	if !inserted {
		sq.Queue = append(sq.Queue, signal)
	}
}

// Dequeue gets the highest priority signal
func (sq *SignalQueue) Dequeue() (Signal, bool) {
	sq.Mutex.Lock()
	defer sq.Mutex.Unlock()
	
	if len(sq.Queue) == 0 {
		return Signal{}, false
	}
	
	signal := sq.Queue[0]
	sq.Queue = sq.Queue[1:]
	return signal, true
}

// Peek returns the highest priority signal without removing it
func (sq *SignalQueue) Peek() (Signal, bool) {
	sq.Mutex.Lock()
	defer sq.Mutex.Unlock()
	
	if len(sq.Queue) == 0 {
		return Signal{}, false
	}
	
	return sq.Queue[0], true
}

// Clear removes all signals from the queue
func (sq *SignalQueue) Clear() {
	sq.Mutex.Lock()
	defer sq.Mutex.Unlock()
	sq.Queue = nil
}

// CompositeSignalScore represents the combined score of all active signals
type CompositeSignalScore struct {
	LongScore  float64
	ShortScore float64
	TotalScore float64 // LongScore - ShortScore
	Regime     MarketRegime
	LastUpdate time.Time
	Mutex      sync.Mutex
}

// UpdateScore updates the composite score with a new signal
func (css *CompositeSignalScore) UpdateScore(signal Signal) {
	css.Mutex.Lock()
	defer css.Mutex.Unlock()
	
	if signal.Direction == "LONG" {
		css.LongScore += signal.Confidence * signal.Weight
	} else if signal.Direction == "SHORT" {
		css.ShortScore += signal.Confidence * signal.Weight
	}
	
	css.TotalScore = css.LongScore - css.ShortScore
	css.LastUpdate = time.Now()
}

// ResetScore resets the composite score
func (css *CompositeSignalScore) ResetScore() {
	css.Mutex.Lock()
	defer css.Mutex.Unlock()
	
	css.LongScore = 0
	css.ShortScore = 0
	css.TotalScore = 0
	css.LastUpdate = time.Now()
}

// GetDirection returns the dominant signal direction
func (css *CompositeSignalScore) GetDirection() string {
	css.Mutex.Lock()
	defer css.Mutex.Unlock()
	
	if css.TotalScore > 0.1 { // Threshold to avoid trading on weak signals
		return "LONG"
	} else if css.TotalScore < -0.1 {
		return "SHORT"
	}
	return ""
}

// MultiTimeframeData holds data across multiple timeframes
type MultiTimeframeData struct {
	Data map[SignalSource][]float64 // Map of closes for different timeframes
	Mutex sync.Mutex
}

// AddData adds data for a specific timeframe
func (mtd *MultiTimeframeData) AddData(source SignalSource, data []float64) {
	mtd.Mutex.Lock()
	defer mtd.Mutex.Unlock()
	
	if mtd.Data == nil {
		mtd.Data = make(map[SignalSource][]float64)
	}
	mtd.Data[source] = data
}

// GetData returns data for a specific timeframe
func (mtd *MultiTimeframeData) GetData(source SignalSource) ([]float64, bool) {
	mtd.Mutex.Lock()
	defer mtd.Mutex.Unlock()
	
	data, exists := mtd.Data[source]
	return data, exists
}

// GetAllSources returns all available timeframes
func (mtd *MultiTimeframeData) GetAllSources() []SignalSource {
	mtd.Mutex.Lock()
	defer mtd.Mutex.Unlock()
	
	sources := make([]SignalSource, 0, len(mtd.Data))
	for source := range mtd.Data {
		sources = append(sources, source)
	}
	return sources
}