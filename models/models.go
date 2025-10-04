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

// Signal represents a trading signal
type Signal struct {
	Kind       string    // "GC" или "SMA"
	ClosePrice float64   // цена закрытия свечи
	Time       time.Time
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

	// Channels
	TPChan      chan TPJob
	SigChan     chan Signal
	MarketRegime string
}