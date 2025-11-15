package websocket

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"go-trader/config"
	"go-trader/types"
	"go-trader/logger"
	"go-trader/interfaces"
)

// WSHub manages WebSocket connections
type WSHub struct {
	config           *config.Config
	logger           *logger.Logger
	publicConn       *websocket.Conn
	privateConn      *websocket.Conn
	publicConnMutex  sync.Mutex
	privateConnMutex sync.Mutex
	obLock           sync.Mutex
	bidsMap          map[string]float64
	asksMap          map[string]float64
	orderbookReady   bool
	sigChan          chan types.Signal
}

var _ interfaces.WebSocketHub = (*WSHub)(nil)

// NewWSHub creates a new WebSocket hub
func NewWSHub(cfg *config.Config, log *logger.Logger) *WSHub {
	return &WSHub{
		config:    cfg,
		logger:    log,
		bidsMap:   make(map[string]float64),
		asksMap:   make(map[string]float64),
		sigChan:   make(chan types.Signal, 32),
	}
}

// ConnectPublic establishes a connection to the public WebSocket
func (h *WSHub) ConnectPublic() error {
	h.publicConnMutex.Lock()
	defer h.publicConnMutex.Unlock()
	
	conn, err := h.newWSConnection(h.config.WSPublicURL)
	if err != nil {
		return err
	}
	h.publicConn = conn
	
	// Subscribe to kline and orderbook
	klineTopic := h.getKlineTopic()
	obTopic := h.getOBTopic()
	
	if err := h.publicConn.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{klineTopic, obTopic},
	}); err != nil {
		h.publicConn.Close()
		return err
	}
	
	// Read subscription acknowledgment
	h.publicConn.ReadMessage()
	return nil
}

// ConnectPrivate establishes a connection to the private WebSocket
func (h *WSHub) ConnectPrivate() error {
	h.privateConnMutex.Lock()
	defer h.privateConnMutex.Unlock()
	
	conn, err := h.newWSConnection(h.config.WSPrivateURL)
	if err != nil {
		return err
	}
	h.privateConn = conn
	
	expires := time.Now().Add(5 * time.Second).UnixMilli()
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{h.config.APIKey, expires, h.signWS(h.config.APISecret, expires)},
	}
	if err := h.privateConn.WriteJSON(auth); err != nil {
		h.privateConn.Close()
		return err
	}
	h.privateConn.ReadMessage() // auth ack

	// Subscribe to wallet and position
	if err := h.privateConn.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position"},
	}); err != nil {
		h.privateConn.Close()
		return err
	}
	h.privateConn.ReadMessage() // sub ack
	return nil
}

// newWSConnection creates a new WebSocket connection
func (h *WSHub) newWSConnection(url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(time.Duration(h.config.PongWait) * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(time.Duration(h.config.PongWait) * time.Second))
		return nil
	})
	return conn, nil
}

// signWS signs a WebSocket request
func (h *WSHub) signWS(secret string, expires int64) string {
	// This is a simplified version - you might want to use the crypto functions from api package
	return ""
}

// getKlineTopic returns the kline topic
func (h *WSHub) getKlineTopic() string {
	return "kline." + h.config.Interval + "." + h.config.Symbol
}

// getOBTopic returns the orderbook topic
func (h *WSHub) getOBTopic() string {
	return "orderbook." + string(h.config.OBDepth) + "." + h.config.Symbol
}

// ApplySnapshot applies an orderbook snapshot
func (h *WSHub) ApplySnapshot(bids, asks [][]string) {
	h.obLock.Lock()
	defer h.obLock.Unlock()
	h.bidsMap = make(map[string]float64)
	h.asksMap = make(map[string]float64)

	for _, lv := range bids {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			h.bidsMap[lv[0]] = size
		}
	}
	for _, lv := range asks {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			h.asksMap[lv[0]] = size
		}
	}
	h.orderbookReady = true
}

// ApplyDelta applies an orderbook delta
func (h *WSHub) ApplyDelta(bids, asks [][]string) {
	h.obLock.Lock()
	defer h.obLock.Unlock()
	for _, lv := range bids {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(h.bidsMap, lv[0])
		} else {
			h.bidsMap[lv[0]] = size
		}
	}
	for _, lv := range asks {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(h.asksMap, lv[0])
		} else {
			h.asksMap[lv[0]] = size
		}
	}
}

// GetLastAskPrice gets the last ask price from orderbook
func (h *WSHub) GetLastAskPrice() float64 {
	h.obLock.Lock()
	defer h.obLock.Unlock()
	var max float64
	for ps := range h.asksMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p > max {
			max = p
		}
	}
	return max
}

// GetLastBidPrice gets the last bid price from orderbook
func (h *WSHub) GetLastBidPrice() float64 {
	h.obLock.Lock()
	defer h.obLock.Unlock()
	var min float64
	for ps := range h.bidsMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p < min || min == 0 {
			min = p
		}
	}
	return min
}

// CheckOrderbookStrength checks the strength of the orderbook for a given side
func (h *WSHub) CheckOrderbookStrength(side string) bool {
	h.obLock.Lock()
	defer h.obLock.Unlock()

	var bidDepth, askDepth float64
	for _, size := range h.bidsMap {
		bidDepth += size
	}
	for _, size := range h.asksMap {
		askDepth += size
	}

	h.logger.Debug("Bid Depth: %.2f, Ask Depth: %.2f", bidDepth, askDepth)

	// For now, return true if bid/ask ratio is favorable for LONG or ask/bid ratio is favorable for SHORT
	if side == "LONG" && bidDepth > askDepth {
		h.logger.Debug("Orderbook: signal LONG confirmed")
		return true
	} else if side == "SHORT" && askDepth > bidDepth {
		h.logger.Debug("Orderbook: signal SHORT confirmed")
		return true
	}
	return false
}

// GetBidsMap returns a copy of the bids map
func (h *WSHub) GetBidsMap() map[string]float64 {
	h.obLock.Lock()
	defer h.obLock.Unlock()

	// Create a copy of the map
	result := make(map[string]float64)
	for k, v := range h.bidsMap {
		result[k] = v
	}
	return result
}

// GetAsksMap returns a copy of the asks map
func (h *WSHub) GetAsksMap() map[string]float64 {
	h.obLock.Lock()
	defer h.obLock.Unlock()

	// Create a copy of the map
	result := make(map[string]float64)
	for k, v := range h.asksMap {
		result[k] = v
	}
	return result
}

// StartPingTicker starts the ping ticker to maintain connections
func (h *WSHub) StartPingTicker() {
	ticker := time.NewTicker(time.Duration(h.config.PingPeriod) * time.Second)
	go func() {
		for range ticker.C {
			h.publicConnMutex.Lock()
			if h.publicConn != nil {
				h.publicConn.WriteMessage(websocket.PingMessage, nil)
			}
			h.publicConnMutex.Unlock()
			
			h.privateConnMutex.Lock()
			if h.privateConn != nil {
				h.privateConn.WriteMessage(websocket.PingMessage, nil)
			}
			h.privateConnMutex.Unlock()
		}
	}()
}

// HandlePublicMessages handles messages from the public WebSocket
func (h *WSHub) HandlePublicMessages(klineChan chan types.KlineData, obChan chan types.OrderbookMsg) {
	for {
		_, raw, err := h.publicConn.ReadMessage()
		if err != nil {
			h.logger.Error("Public WS read: %v — reconnection needed", err)
			// We would handle reconnection here
			continue
		}

		var peek struct {
			Topic string `json:"topic"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			h.logger.Error("Failed to unmarshal message topic: %v", err)
			continue
		}

		switch {
		case strings.HasPrefix(peek.Topic, "kline."):
			var km types.KlineMsg
			if err := json.Unmarshal(raw, &km); err != nil {
				h.logger.Error("Failed to unmarshal kline message: %v", err)
				continue
			}
			if len(km.Data) > 0 && km.Data[0].Confirm {
				klineChan <- km.Data[0]
			}
		case strings.HasPrefix(peek.Topic, "orderbook."):
			var om types.OrderbookMsg
			if err := json.Unmarshal(raw, &om); err != nil {
				h.logger.Error("Failed to unmarshal orderbook message: %v", err)
				continue
			}
			obChan <- om
		}
	}
}

// HandlePrivateMessages handles messages from the private WebSocket
func (h *WSHub) HandlePrivateMessages() {
	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	
	go h.walletListener(walletChan, walletDone)
	
	for {
		_, raw, err := h.privateConn.ReadMessage()
		if err != nil {
			h.logger.Error("Private WS read: %v — reconnection needed", err)
			// We would handle reconnection here
			continue
		}
		
		var peek struct {
			Topic string `json:"topic"`
		}
		if json.Unmarshal(raw, &peek) == nil && peek.Topic == "position" {
			h.logger.Debug("Received position update: %s", string(raw))
			var posUpdate struct {
				Data struct {
					Side string `json:"side"`
					Size string `json:"size"`
				} `json:"data"`
			}
			if json.Unmarshal(raw, &posUpdate) == nil {
				// Handle position update
			}
		}
		
		walletChan <- raw
	}
}

// walletListener listens for wallet updates
func (h *WSHub) walletListener(out chan<- []byte, done chan<- struct{}) {
	for {
		_, msg, err := h.privateConn.ReadMessage()
		if err != nil {
			h.privateConn.Close()
			done <- struct{}{}
			return
		}
		out <- msg
	}
}