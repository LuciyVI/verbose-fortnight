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

// Signal represents a trading signal
type Signal struct {
	Kind       string   // "GC" или "SMA"
	Direction  string   // LONG or SHORT
	Strength   int      // aggregated indicator confirmations
	Contribs   []string // names of indicators that fired
	HighConf   bool     // high-confidence, bypass normal confirmation rules
	ClosePrice float64  // цена закрытия свечи
	CandleSeq  uint64   // monotonic sequence of closed candles
	Time       time.Time
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
	Kind       string    `json:"kind"`
	Direction  string    `json:"direction"`
	Strength   int       `json:"strength"`
	Contribs   []string  `json:"contribs,omitempty"`
	HighConf   bool      `json:"highConf"`
	ClosePrice float64   `json:"closePrice"`
	CandleSeq  uint64    `json:"candleSeq"`
	Time       time.Time `json:"time"`
}

// IndicatorSnapshot holds the latest indicator values for status reporting.
type IndicatorSnapshot struct {
	Time       time.Time `json:"time"`
	Close      float64   `json:"close"`
	SMA        float64   `json:"sma"`
	RSI        float64   `json:"rsi"`
	MACDLine   float64   `json:"macdLine"`
	MACDSignal float64   `json:"macdSignal"`
	MACDHist   float64   `json:"macdHist"`
	ATR        float64   `json:"atr"`
	BBUpper    float64   `json:"bbUpper"`
	BBMiddle   float64   `json:"bbMiddle"`
	BBLower    float64   `json:"bbLower"`
	HTFBias    string    `json:"htfBias,omitempty"`
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
	ObLock             sync.Mutex
	BidsMap            map[string]float64
	AsksMap            map[string]float64
	ObImbalanceHistory []float64

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
	LastOrderID    string
	LastExecID     string

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
