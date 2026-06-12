package models

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// InstrumentInfo holds instrument metadata
type InstrumentInfo struct {
	MinNotional float64
	MinQty      float64
	QtyStep     float64
	TickSize    float64
}

// TPJob represents a take profit job
type TPJob struct {
	Side       string
	Qty        float64
	EntryPrice float64
}

// SignalDetails represents details of a single signal
type SignalDetails struct {
	Kind   string  // "SMA_LONG", "MACD_LONG", etc.
	Weight int     // Weight of this signal
	Value  float64 // Additional value if needed e.g., indicator value
}

// SignalMode optionally tags the strategy mode that produced a signal.
type SignalMode string

const (
	ModeTrendLong    SignalMode = "trend_long"
	ModeTrendShort   SignalMode = "trend_short"
	ModeReversalLong SignalMode = "reversal_long"
)

const (
	SignalReasonReversalLong     = "reversal_long"
	SignalReasonReversalDisabled = "reversal_disabled"
)

// OrderbookResult captures the orderbook evaluation attached to a signal.
type OrderbookResult struct {
	Evaluated bool    `json:"evaluated"`
	Passed    bool    `json:"passed"`
	Ratio     float64 `json:"ratio"`
	Imbalance float64 `json:"imbalance"`
	Side      string  `json:"side,omitempty"`
	Threshold float64 `json:"threshold"`
	History   string  `json:"history,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// SignalDebug carries structured scoring and filtering context for a signal.
type SignalDebug struct {
	LongScore       int      `json:"longScore"`
	ShortScore      int      `json:"shortScore"`
	ChosenDirection string   `json:"chosenDirection,omitempty"`
	AppliedFilters  []string `json:"appliedFilters,omitempty"`
	RejectedBy      []string `json:"rejectedBy,omitempty"`
	Reasons         []string `json:"reasons,omitempty"`

	VolumeRatio            float64 `json:"volumeRatio"`
	VolumeRatioAvailable   bool    `json:"volumeRatioAvailable"`
	RSIDelta               float64 `json:"rsiDelta"`
	RSIDeltaAvailable      bool    `json:"rsiDeltaAvailable"`
	MACDHistDelta          float64 `json:"macdHistDelta"`
	MACDHistDeltaAvailable bool    `json:"macdHistDeltaAvailable"`
	BBPosition             string  `json:"bbPosition,omitempty"`
	WasBelowLowerBB        bool    `json:"wasBelowLowerBB"`
	ReturnedInsideBB       bool    `json:"returnedInsideBB"`
	BBReclaimAvailable     bool    `json:"bbReclaimAvailable"`

	ReversalScore                  float64 `json:"reversalScore,omitempty"`
	ReversalState                  string  `json:"reversalState,omitempty"`
	ReversalLowestLow              float64 `json:"reversalLowestLow,omitempty"`
	ReversalStopPrice              float64 `json:"reversalStopPrice,omitempty"`
	ReversalTimeoutBars            int     `json:"reversalTimeoutBars,omitempty"`
	ReversalPositionSizeMultiplier float64 `json:"reversalPositionSizeMultiplier,omitempty"`
	ReversalSoftOrderbookAllowed   bool    `json:"reversalSoftOrderbookAllowed,omitempty"`
	ReversalHTFOverrideAllowed     bool    `json:"reversalHTFOverrideAllowed,omitempty"`
}

// Signal represents a trading signal
type Signal struct {
	Kind       string           // "GC" или "SMA"
	Direction  string           // LONG or SHORT
	Mode       SignalMode       // optional strategy mode tag
	Strength   int              // aggregated indicator confirmations
	Contribs   []string         // names of indicators that fired
	HighConf   bool             // high-confidence, bypass normal confirmation rules
	ClosePrice float64          // цена закрытия свечи
	CandleSeq  uint64           // monotonic sequence of closed candles
	Time       time.Time        // signal time
	Orderbook  *OrderbookResult // precomputed orderbook confirmation, if available
	Debug      SignalDebug      // structured scoring/filtering context
}

// ConsolidatedSignal represents a consolidated trading signal with multiple confirmations
type ConsolidatedSignal struct {
	Kind          string  // Overall signal type: "LONG" or "SHORT"
	ClosePrice    float64 // Current close price
	Time          time.Time
	SignalDetails []SignalDetails // All contributing signals
	TotalWeight   int             // Total weight of all contributing signals
	SignalSource  string          // Source of the signal: "consolidated" or "original"
}

// SignalSnapshot holds the latest emitted signal details for status reporting.
type SignalSnapshot struct {
	Kind       string           `json:"kind"`
	Direction  string           `json:"direction"`
	Mode       SignalMode       `json:"mode,omitempty"`
	Strength   int              `json:"strength"`
	Contribs   []string         `json:"contribs,omitempty"`
	HighConf   bool             `json:"highConf"`
	ClosePrice float64          `json:"closePrice"`
	CandleSeq  uint64           `json:"candleSeq"`
	Time       time.Time        `json:"time"`
	Orderbook  *OrderbookResult `json:"orderbook,omitempty"`
	Debug      SignalDebug      `json:"debug"`
}

// IndicatorSnapshot holds the latest indicator values for status reporting.
type IndicatorSnapshot struct {
	Time                   time.Time `json:"time"`
	Close                  float64   `json:"close"`
	Low                    float64   `json:"low"`
	SMA                    float64   `json:"sma"`
	Volume                 float64   `json:"volume"`
	VolumeSMA              float64   `json:"volumeSMA"`
	VolumeRatio            float64   `json:"volumeRatio"`
	VolumeAvailable        bool      `json:"volumeAvailable"`
	RSI                    float64   `json:"rsi"`
	RSIPrev                float64   `json:"rsiPrev"`
	RSIDelta               float64   `json:"rsiDelta"`
	RSIDeltaAvailable      bool      `json:"rsiDeltaAvailable"`
	MACDLine               float64   `json:"macdLine"`
	MACDSignal             float64   `json:"macdSignal"`
	MACDHist               float64   `json:"macdHist"`
	MACDHistPrev           float64   `json:"macdHistPrev"`
	MACDHistDelta          float64   `json:"macdHistDelta"`
	MACDHistDeltaAvailable bool      `json:"macdHistDeltaAvailable"`
	ATR                    float64   `json:"atr"`
	BBUpper                float64   `json:"bbUpper"`
	BBMiddle               float64   `json:"bbMiddle"`
	BBLower                float64   `json:"bbLower"`
	BBPosition             string    `json:"bbPosition,omitempty"`
	WasBelowLowerBB        bool      `json:"wasBelowLowerBB"`
	ReturnedInsideBB       bool      `json:"returnedInsideBB"`
	BBReclaimAvailable     bool      `json:"bbReclaimAvailable"`
	HTFBias                string    `json:"htfBias,omitempty"`
}

// PositionSnapshot holds the last known position details for status reporting.
type PositionSnapshot struct {
	Side       string    `json:"side"`
	Size       float64   `json:"size"`
	EntryPrice float64   `json:"entryPrice"`
	TakeProfit float64   `json:"takeProfit"`
	StopLoss   float64   `json:"stopLoss"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// OrderbookSnapshotHealth captures diagnostic-only live orderbook recorder health.
type OrderbookSnapshotHealth struct {
	OrderbookSnapshotRecordedTotal    uint64    `json:"orderbook_snapshot_recorded_total"`
	OrderbookSnapshotAvailableTotal   uint64    `json:"orderbook_snapshot_available_total"`
	OrderbookSnapshotUnavailableTotal uint64    `json:"orderbook_snapshot_unavailable_total"`
	LastOrderbookSnapshotTime         time.Time `json:"last_orderbook_snapshot_time,omitempty"`
	LastOrderbookUnavailableReason    string    `json:"last_orderbook_unavailable_reason,omitempty"`
}

// OrderbookMsg represents orderbook message
type OrderbookMsg struct {
	Topic string        `json:"topic"`
	Type  string        `json:"type"` // snapshot | delta
	Data  OrderbookData `json:"data"`
}

// OrderbookData represents orderbook data
type OrderbookData struct {
	B [][]string `json:"b"` // [[price,size]]
	A [][]string `json:"a"`
}

// KlineMsg represents kline message
type KlineMsg struct {
	Topic string      `json:"topic"`
	Data  []KlineData `json:"data"`
}

// KlineData represents kline data
type KlineData struct {
	Open    string `json:"open"`
	High    string `json:"high"`
	Low     string `json:"low"`
	Close   string `json:"close"`
	Volume  string `json:"volume"`
	Confirm bool   `json:"confirm"`
}

// CloseFloat returns the close price as float64
func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
	return f
}

// HighFloat returns the high price as float64
func (k KlineData) HighFloat() float64 {
	f, _ := strconv.ParseFloat(k.High, 64)
	return f
}

// LowFloat returns the low price as float64
func (k KlineData) LowFloat() float64 {
	f, _ := strconv.ParseFloat(k.Low, 64)
	return f
}

// OhlcvSnapshot is an immutable copy of candle state and its sequence.
type OhlcvSnapshot struct {
	Closes    []float64
	Highs     []float64
	Lows      []float64
	Volumes   []float64
	HTFCloses []float64
	CandleSeq uint64
}

// MarketRegimeSnapshot is a consistent copy of regime state.
type MarketRegimeSnapshot struct {
	MarketRegime    string
	RegimeCandidate string
	RegimeStreak    int
}

// MarketRegimeUpdate describes an atomic regime vote update.
type MarketRegimeUpdate struct {
	MarketRegimeSnapshot
	CandidateReset      bool
	MarketRegimeChanged bool
}

// State represents the application state
type State struct {
	// Global variables
	Debug     bool
	DynamicTP bool

	// Atomic pointers for WebSocket connections
	PrivPtr atomic.Pointer[websocket.Conn]
	PubPtr  atomic.Pointer[websocket.Conn]

	// Constants for trading
	SlPerc    float64 // Процент SL от TP (1%)
	TrailPerc float64 // Процент трейлинг-стопа (0.5%)
	SmaLen    int     // окно для SMA-воркера

	// Orderbook state
	OrderbookReady atomic.Bool
	PosSide        string
	OrderQty       float64

	// Strategy state
	OhlcvLock sync.RWMutex
	Closes    []float64
	Highs     []float64
	Lows      []float64
	Volumes   []float64
	HTFCloses []float64
	CandleSeq atomic.Uint64

	// Longer-term data for higher-order trend filter
	LongTermCloses []float64
	LongTermHighs  []float64
	LongTermLows   []float64

	// Instrument information
	Instr InstrumentInfo

	// Orderbook maps with mutex protection
	ObLock                  sync.Mutex
	BidsMap                 map[string]float64
	AsksMap                 map[string]float64
	ObImbalanceHistoryLong  []float64
	ObImbalanceHistoryShort []float64

	OrderbookSnapshotRecordedTotal    atomic.Uint64
	OrderbookSnapshotAvailableTotal   atomic.Uint64
	OrderbookSnapshotUnavailableTotal atomic.Uint64
	OrderbookSnapshotHealthLock       sync.RWMutex
	LastOrderbookSnapshotTime         time.Time
	LastOrderbookUnavailableReason    string

	// Signal statistics
	SignalStats struct {
		Total, Correct, FalsePositive int
		sync.Mutex
	}

	// Profit tracking
	RealizedPnL   float64
	UnrealizedPnL float64
	TotalProfit   float64
	TotalLoss     float64
	sync.RWMutex
	PartialTPDone  bool
	LastExitAt     time.Time
	LastExitDir    string
	LastEntryAt    time.Time
	LastEntryPrice float64
	LastEntryDir   string

	// Volume tracking
	RecentVolumes []float64 // Store recent volumes for calculating average/surge detection

	// Position tracking for partial profits
	PartialTPTriggered bool    // Flag to indicate if partial profit has been taken
	PartialTPPrice     float64 // Price at which partial profit was triggered

	// Re-entry tracking
	LastExitPrice float64   // Price at which the last position was closed
	LastExitTime  time.Time // Time at which the last position was closed
	LastExitSide  string    // Side of the last closed position ("LONG" or "SHORT")

	// Multi-timeframe analysis
	HigherTimeframeCloses []float64 // Closes for higher timeframe (e.g., 5-min candles aggregated from 1-min)
	HigherTimeframeHighs  []float64 // Highs for higher timeframe
	HigherTimeframeLows   []float64 // Lows for higher timeframe
	HigherTimeframeTrend  string    // Trend direction on higher timeframe ("up", "down", "neutral")
	LastHTFUpdateTime     time.Time // Time of last higher timeframe candle update

	// Divergence tracking
	PreviousPrices           []float64 // Previous prices for divergence detection
	PreviousRSIValues        []float64 // Previous RSI values for divergence detection
	PreviousMACDValues       []float64 // Previous MACD values for divergence detection
	PreviousMACDSignalValues []float64 // Previous MACD signal values for divergence detection

	// Partial profit and trailing tracking
	ActiveTrailingPositions  map[string]bool    // Track which positions are in trailing mode
	PartialProfitTriggers    map[string]bool    // Track if partial profit has been triggered
	TrailingStopLevels       map[string]float64 // Current trailing stop levels for positions
	TrailingTakeProfitLevels map[string]float64 // Current trailing TP levels for positions

	// Status snapshots for external reporting
	StatusLock     sync.RWMutex
	LastSignal     SignalSnapshot
	LastIndicators IndicatorSnapshot
	LastPosition   PositionSnapshot

	// Channels
	TPChan          chan TPJob
	SigChan         chan Signal
	MarketRegime    string
	RegimeCandidate string
	RegimeStreak    int
}

func copyFloat64s(src []float64) []float64 {
	if len(src) == 0 {
		return nil
	}
	return append([]float64(nil), src...)
}

func (s *State) ohlcvSnapshotLocked(seq uint64) OhlcvSnapshot {
	return OhlcvSnapshot{
		Closes:    copyFloat64s(s.Closes),
		Highs:     copyFloat64s(s.Highs),
		Lows:      copyFloat64s(s.Lows),
		Volumes:   copyFloat64s(s.Volumes),
		HTFCloses: copyFloat64s(s.HTFCloses),
		CandleSeq: seq,
	}
}

// GetOhlcvSnapshot returns copied candle slices and CandleSeq from one read-locked view.
func (s *State) GetOhlcvSnapshot() OhlcvSnapshot {
	s.OhlcvLock.RLock()
	defer s.OhlcvLock.RUnlock()
	return s.ohlcvSnapshotLocked(s.CandleSeq.Load())
}

// AppendClosedCandle appends a closed candle, trims history, increments CandleSeq, and returns a copied snapshot.
func (s *State) AppendClosedCandle(closePrice, high, low, volume float64, maxLen int) OhlcvSnapshot {
	s.OhlcvLock.Lock()
	defer s.OhlcvLock.Unlock()

	s.Closes = append(s.Closes, closePrice)
	s.Highs = append(s.Highs, high)
	s.Lows = append(s.Lows, low)
	if volume > 0 {
		s.Volumes = append(s.Volumes, volume)
	}
	s.HTFCloses = append(s.HTFCloses, closePrice)

	if maxLen > 0 && len(s.Closes) > maxLen {
		s.Closes = s.Closes[len(s.Closes)-maxLen:]
		s.Highs = s.Highs[len(s.Highs)-maxLen:]
		s.Lows = s.Lows[len(s.Lows)-maxLen:]
		if len(s.Volumes) > maxLen {
			s.Volumes = s.Volumes[len(s.Volumes)-maxLen:]
		}
		if len(s.HTFCloses) > maxLen {
			s.HTFCloses = s.HTFCloses[len(s.HTFCloses)-maxLen:]
		}
	}

	seq := s.CandleSeq.Add(1)
	return s.ohlcvSnapshotLocked(seq)
}

// GetMarketRegimeSnapshot returns a consistent copy of market regime state.
func (s *State) GetMarketRegimeSnapshot() MarketRegimeSnapshot {
	s.RLock()
	defer s.RUnlock()
	return MarketRegimeSnapshot{
		MarketRegime:    s.MarketRegime,
		RegimeCandidate: s.RegimeCandidate,
		RegimeStreak:    s.RegimeStreak,
	}
}

// GetMarketRegime returns the current market regime under lock.
func (s *State) GetMarketRegime() string {
	s.RLock()
	defer s.RUnlock()
	return s.MarketRegime
}

// SetMarketRegime replaces regime state atomically.
func (s *State) SetMarketRegime(marketRegime, candidate string, streak int) {
	s.Lock()
	s.MarketRegime = marketRegime
	s.RegimeCandidate = candidate
	s.RegimeStreak = streak
	s.Unlock()
}

// RecordOrderbookSnapshotHealth updates diagnostic-only orderbook recorder counters.
func (s *State) RecordOrderbookSnapshotHealth(available bool, ts time.Time, unavailableReason string) {
	if s == nil {
		return
	}
	s.OrderbookSnapshotRecordedTotal.Add(1)
	if available {
		s.OrderbookSnapshotAvailableTotal.Add(1)
	} else {
		s.OrderbookSnapshotUnavailableTotal.Add(1)
	}
	s.OrderbookSnapshotHealthLock.Lock()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	s.LastOrderbookSnapshotTime = ts
	if available {
		s.LastOrderbookUnavailableReason = ""
	} else {
		s.LastOrderbookUnavailableReason = unavailableReason
	}
	s.OrderbookSnapshotHealthLock.Unlock()
}

// GetOrderbookSnapshotHealth returns a consistent copy of orderbook recorder counters.
func (s *State) GetOrderbookSnapshotHealth() OrderbookSnapshotHealth {
	if s == nil {
		return OrderbookSnapshotHealth{}
	}
	health := OrderbookSnapshotHealth{
		OrderbookSnapshotRecordedTotal:    s.OrderbookSnapshotRecordedTotal.Load(),
		OrderbookSnapshotAvailableTotal:   s.OrderbookSnapshotAvailableTotal.Load(),
		OrderbookSnapshotUnavailableTotal: s.OrderbookSnapshotUnavailableTotal.Load(),
	}
	s.OrderbookSnapshotHealthLock.RLock()
	health.LastOrderbookSnapshotTime = s.LastOrderbookSnapshotTime
	health.LastOrderbookUnavailableReason = s.LastOrderbookUnavailableReason
	s.OrderbookSnapshotHealthLock.RUnlock()
	return health
}

// UpdateMarketRegime applies a regime vote and updates MarketRegime atomically.
func (s *State) UpdateMarketRegime(candidate string, persistence int) MarketRegimeUpdate {
	s.Lock()
	defer s.Unlock()

	update := MarketRegimeUpdate{}
	if candidate != s.RegimeCandidate {
		s.RegimeCandidate = candidate
		s.RegimeStreak = 1
		update.CandidateReset = true
	} else {
		s.RegimeStreak++
	}

	if s.RegimeStreak >= persistence && s.MarketRegime != candidate {
		s.MarketRegime = candidate
		update.MarketRegimeChanged = true
	}

	update.MarketRegimeSnapshot = MarketRegimeSnapshot{
		MarketRegime:    s.MarketRegime,
		RegimeCandidate: s.RegimeCandidate,
		RegimeStreak:    s.RegimeStreak,
	}
	return update
}
