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
	Kind  string  // "SMA_LONG", "MACD_LONG", etc.
	Weight int    // Weight of this signal
	Value float64 // Additional value if needed e.g., indicator value
}

// Signal represents a trading signal
type Signal struct {
	Kind       string    // "GC" или "SMA"
	ClosePrice float64   // цена закрытия свечи
	Time       time.Time
}

// ConsolidatedSignal represents a consolidated trading signal with multiple confirmations
type ConsolidatedSignal struct {
	Kind           string          // Overall signal type: "LONG" or "SHORT"
	ClosePrice     float64         // Current close price
	Time           time.Time
	SignalDetails  []SignalDetails // All contributing signals
	TotalWeight    int             // Total weight of all contributing signals
	SignalSource   string          // Source of the signal: "consolidated" or "original"
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
	Close   string `json:"close"`
	Confirm bool   `json:"confirm"`
}

// CloseFloat returns the close price as float64
func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
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
	Closes []float64
	Highs  []float64
	Lows   []float64

	// Longer-term data for higher-order trend filter
	LongTermCloses []float64
	LongTermHighs  []float64
	LongTermLows   []float64

	// Instrument information
	Instr InstrumentInfo

	// Orderbook maps with mutex protection
	ObLock  sync.Mutex
	BidsMap map[string]float64
	AsksMap map[string]float64

	// Signal statistics
	SignalStats struct {
		Total, Correct, FalsePositive int
		sync.Mutex
	}
	
	// Profit tracking
	RealizedPnL    float64
	UnrealizedPnL  float64
	TotalProfit    float64
	TotalLoss      float64
	sync.RWMutex

	// Volume tracking
	RecentVolumes  []float64 // Store recent volumes for calculating average/surge detection

	// Position tracking for partial profits
	PartialTPTriggered bool  // Flag to indicate if partial profit has been taken
	PartialTPPrice     float64  // Price at which partial profit was triggered

	// Re-entry tracking
	LastExitPrice      float64  // Price at which the last position was closed
	LastExitTime       time.Time // Time at which the last position was closed
	LastExitSide       string    // Side of the last closed position ("LONG" or "SHORT")

	// Multi-timeframe analysis
	HigherTimeframeCloses []float64 // Closes for higher timeframe (e.g., 5-min candles aggregated from 1-min)
	HigherTimeframeHighs  []float64 // Highs for higher timeframe
	HigherTimeframeLows   []float64 // Lows for higher timeframe
	HigherTimeframeTrend  string    // Trend direction on higher timeframe ("up", "down", "neutral")
	LastHTFUpdateTime     time.Time // Time of last higher timeframe candle update

	// Divergence tracking
	PreviousPrices         []float64  // Previous prices for divergence detection
	PreviousRSIValues      []float64  // Previous RSI values for divergence detection
	PreviousMACDValues     []float64  // Previous MACD values for divergence detection
	PreviousMACDSignalValues []float64  // Previous MACD signal values for divergence detection

	// Channels
	TPChan      chan TPJob
	SigChan     chan Signal
	ConsolidatedSigChan chan ConsolidatedSignal
	MarketRegime string
	HigherTrend  string  // Direction of higher-order trend (up, down, neutral)
}