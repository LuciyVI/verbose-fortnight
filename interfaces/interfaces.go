package interfaces

import (
	"github.com/gorilla/websocket"

	"go-trader/types"
)

// APIClient defines the interface for API operations
type APIClient interface {
	GetInstrumentInfo(symbol string) (*types.InstrumentInfo, error)
	GetBalance(coin string) (float64, error)
	PlaceOrderMarket(side string, qty float64, reduceOnly bool) error
	UpdatePositionTradingStop(posSide string, takeProfit, stopLoss float64) error
	GetPositionList() (*types.Position, error)
	NewWSConnection(url string) (*websocket.Conn, error)
	ConnectPrivateWS() (*websocket.Conn, error)
}

// PositionManager defines the interface for position management
type PositionManager interface {
	GetLastEntryPriceFromREST() (float64, error)
	OpenPosition(newSide string, price float64) error
}

// WebSocketHub defines the interface for WebSocket operations
type WebSocketHub interface {
	ConnectPublic() error
	ConnectPrivate() error
	ApplySnapshot(bids, asks [][]string)
	ApplyDelta(bids, asks [][]string)
	GetLastAskPrice() float64
	GetLastBidPrice() float64
	CheckOrderbookStrength(side string) bool
	StartPingTicker()
	HandlePublicMessages(klineChan chan types.KlineData, obChan chan types.OrderbookMsg)
	HandlePrivateMessages()
}

// TradingStrategy defines the interface for trading strategy operations
type TradingStrategy interface {
	OnClosedCandle(closePrice float64)
	SMATradingLogic()
	DetectMarketRegime()
	CalcTakeProfitATR(side string) float64
	CalcTakeProfitBB(side string) float64
	CalcTakeProfitVolume(side string, thresholdQty float64) float64
	CalcTakeProfitVoting(side string) float64
	AdjustTPSL(closePrice float64)
	AdjustTPSLForShort(closePrice float64)
	HandleSignal(signal types.Signal)
}