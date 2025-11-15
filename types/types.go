package types

import (
	"strconv"
	"time"
)

// InstrumentInfo holds instrument-specific information
type InstrumentInfo struct {
	MinNotional float64
	MinQty      float64
	QtyStep     float64
	TickSize    float64
}

// KlineData represents a single candlestick data point
type KlineData struct {
	Close   string `json:"close"`
	Confirm bool   `json:"confirm"`
}

// CloseFloat converts the Close string to float64
func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
	return f
}

// KlineMsg represents a WebSocket message containing kline data
type KlineMsg struct {
	Topic string      `json:"topic"`
	Data  []KlineData `json:"data"`
}

// OrderbookData represents orderbook data (bids and asks)
type OrderbookData struct {
	B [][]string `json:"b"` // [[price,size]]
	A [][]string `json:"a"`
}

// OrderbookMsg represents a WebSocket message containing orderbook data
type OrderbookMsg struct {
	Topic string        `json:"topic"`
	Type  string        `json:"type"` // snapshot | delta
	Data  OrderbookData `json:"data"`
}

// Position holds position information
type Position struct {
	Exists     bool
	Side       string
	Size       float64
	TakeProfit float64
	StopLoss   float64
}

// Signal represents a trading signal
type Signal struct {
	Kind       string  // "GC" или "SMA"
	ClosePrice float64 // цена закрытия свечи
	Time       time.Time
}

// TPJob represents a take-profit job
type TPJob struct {
	Side       string
	Qty        float64
	EntryPrice float64
}

// TradingAccount represents trading account information
type TradingAccount struct {
	TotalAvailableBalance float64
}

// OrderParams represents parameters for placing an order
type OrderParams struct {
	Category    string
	Symbol      string
	Side        string
	OrderType   string
	Qty         string
	Price       string
	StopPrice   string
	TimeInForce string
	ReduceOnly  bool
	PositionIdx int
}

// PositionParams represents parameters for updating position
type PositionParams struct {
	Category    string
	Symbol      string
	TakeProfit  string
	StopLoss    string
	PositionIdx int
	TpslMode    string
}