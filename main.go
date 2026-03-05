package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/daemon"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/status"
	"verbose-fortnight/strategy"
)

var (
	cfg    *config.Config
	logger *logging.Logger
)

// Initialize logging with the provided configuration
func initLogging() error {
	logLevel := logging.LogLevel(cfg.LogLevel)

	var err error
	logger, err = logging.NewLogger(
		cfg.LogFile,
		cfg.LogMaxSize,
		cfg.LogMaxBackups,
		cfg.LogMaxAge,
		cfg.LogCompress,
		logLevel,
	)

	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	return nil
}

// logDebug logs debug messages
func logDebug(format string, v ...interface{}) {
	if logger != nil {
		logger.Debug(format, v...)
	} else {
		log.Printf("[DEBUG] "+format, v...)
	}
}

// logInfo logs info messages
func logInfo(format string, v ...interface{}) {
	if logger != nil {
		logger.Info(format, v...)
	} else {
		log.Printf("[INFO] "+format, v...)
	}
}

// dbg is an alias for logDebug
func dbg(format string, v ...interface{}) {
	if cfg.Debug {
		logDebug(format, v...)
	}
}

// logWarning logs warning messages
func logWarning(format string, v ...interface{}) {
	if logger != nil {
		logger.Warning(format, v...)
	} else {
		log.Printf("[WARN] "+format, v...)
	}
}

// logError logs error messages
func logError(format string, v ...interface{}) {
	if logger != nil {
		logger.Error(format, v...)
	} else {
		log.Printf("[ERROR] "+format, v...)
	}
}

// logFatal logs fatal messages and exits
func logFatal(format string, v ...interface{}) {
	if logger != nil {
		logger.Fatal(format, v...)
	} else {
		log.Fatalf(format, v...)
	}
}

// newWSConn creates a new WebSocket connection
func newWSConn(url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(time.Duration(cfg.PongWait) * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(time.Duration(cfg.PongWait) * time.Second))
		return nil
	})
	return conn, nil
}

// connectPrivateWS connects to the private WebSocket
func connectPrivateWS(apiClient *api.RESTClient, state *models.State) (*websocket.Conn, error) {
	dbg("Attempting to connect to private WebSocket: %s", cfg.DemoWSPrivateURL)
	ws, err := newWSConn(cfg.DemoWSPrivateURL)
	if err != nil {
		logError("Failed to connect to private WebSocket: %v", err)
		return nil, err
	}
	logInfo("Successfully connected to private WebSocket: %s", cfg.DemoWSPrivateURL)

	expires := time.Now().Add(5 * time.Second).UnixMilli()

	// Log authentication request
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{cfg.APIKey, expires, signWS(cfg.APISecret, expires)},
	}
	dbg("Sending authentication request to exchange: %v", auth)

	if err := ws.WriteJSON(auth); err != nil {
		logError("Failed to send authentication to exchange: %v", err)
		ws.Close()
		return nil, err
	}

	// Read authentication response
	_, authResp, err := ws.ReadMessage()
	if err != nil {
		logError("Failed to read authentication response from exchange: %v", err)
		ws.Close()
		return nil, err
	}
	dbg("Received authentication response from exchange: %s", string(authResp))

	// Subscribe to wallet, position, execution
	subReq := map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position", "execution"},
	}
	dbg("Sending subscription request to exchange: %v", subReq)

	if err := ws.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position", "execution"},
	}); err != nil {
		logError("Failed to send subscription to exchange: %v", err)
		ws.Close()
		return nil, err
	}

	// Read subscription response
	_, subResp, err := ws.ReadMessage()
	if err != nil {
		logError("Failed to read subscription response from exchange: %v", err)
		ws.Close()
		return nil, err
	}
	dbg("Received subscription response from exchange: %s", string(subResp))

	state.PrivPtr.Store(ws)
	logInfo("Private WebSocket authentication and subscription completed successfully")
	return ws, nil
}

// SignWS signs a WebSocket request
func signWS(secret string, expires int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// reconnectPublic reconnects to the public WebSocket
func reconnectPublic(klineTopic, obTopic string, state *models.State) (*websocket.Conn, error) {
	logWarning("Attempting to reconnect to public WebSocket...")

	for backoff := 1; ; backoff++ {
		time.Sleep(time.Duration(backoff*2) * time.Second)
		logInfo("Attempting reconnect attempt #%d to public WebSocket: %s", backoff, cfg.DemoWSPublicURL)
		conn, err := newWSConn(cfg.DemoWSPublicURL)
		if err != nil {
			logError("Public reconnect dial: %v", err)
			continue
		}

		subReq := map[string]interface{}{
			"op":   "subscribe",
			"args": []string{klineTopic, obTopic},
		}
		dbg("Sending subscription request to exchange during reconnection: %v", subReq)

		if err = conn.WriteJSON(map[string]interface{}{
			"op":   "subscribe",
			"args": []string{klineTopic, obTopic},
		}); err != nil {
			logError("Public resub send: %v", err)
			conn.Close()
			continue
		}

		_, msg, err := conn.ReadMessage()
		if err == nil {
			dbg("Received subscription response from exchange during reconnection: %s", string(msg))
			state.PubPtr.Store(conn)
			logInfo("Successfully reconnected to public WebSocket: %s", cfg.DemoWSPublicURL)
			return conn, nil
		}

		logError("Public resub read: %v", err)
		conn.Close()
	}
}

// ApplySnapshot applies orderbook snapshot
func applySnapshot(state *models.State, bids, asks [][]string) {
	dbg("Received orderbook snapshot from exchange - bids: %d, asks: %d", len(bids), len(asks))

	state.ObLock.Lock()
	defer state.ObLock.Unlock()
	state.BidsMap = map[string]float64{}
	state.AsksMap = map[string]float64{}

	for _, lv := range bids {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			state.BidsMap[lv[0]] = size
		}
	}
	for _, lv := range asks {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			state.AsksMap[lv[0]] = size
		}
	}
	state.OrderbookReady.Store(true)

	dbg("Orderbook snapshot applied - bids: %d, asks: %d", len(state.BidsMap), len(state.AsksMap))
}

// ApplyDelta applies orderbook delta
func applyDelta(state *models.State, bids, asks [][]string) {
	dbg("Received orderbook delta from exchange - bid updates: %d, ask updates: %d", len(bids), len(asks))

	state.ObLock.Lock()
	defer state.ObLock.Unlock()
	for _, lv := range bids {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(state.BidsMap, lv[0])
			dbg("Removed bid level: %s", lv[0])
		} else {
			state.BidsMap[lv[0]] = size
			dbg("Updated bid level: %s = %f", lv[0], size)
		}
	}
	for _, lv := range asks {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(state.AsksMap, lv[0])
			dbg("Removed ask level: %s", lv[0])
		} else {
			state.AsksMap[lv[0]] = size
			dbg("Updated ask level: %s = %f", lv[0], size)
		}
	}
}

// WalletListener listens to wallet updates
func walletListener(ws *websocket.Conn, out chan<- []byte, done chan<- struct{}, state *models.State, symbol string) {
	logInfo("Starting wallet listener to receive messages from exchange")

	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			logError("Error reading message from private WebSocket: %v", err)
			ws.Close()
			done <- struct{}{}
			return
		}

		dbg("Received message from exchange: %s", string(msg))

		var peek struct {
			Topic string `json:"topic"`
		}
		if json.Unmarshal(msg, &peek) == nil && peek.Topic == "position" {
			logInfo("Received position update from exchange: %s", string(msg))
			updatePositionFromMessage(state, symbol, msg)
		}
		out <- msg
	}
}

// normalizeSide normalizes side values
func normalizeSide(side string) string {
	switch strings.ToUpper(side) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

type executionMsg struct {
	Topic string                   `json:"topic"`
	Data  []map[string]interface{} `json:"data"`
}

func getStringField(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case string:
				return val
			case json.Number:
				return val.String()
			case float64:
				return strconv.FormatFloat(val, 'f', -1, 64)
			}
		}
	}
	return ""
}

func getFloatField(m map[string]interface{}, keys ...string) float64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case float64:
				return val
			case string:
				f, _ := strconv.ParseFloat(val, 64)
				return f
			case json.Number:
				f, _ := val.Float64()
				return f
			}
		}
	}
	return 0
}

func getOptionalFloatField(m map[string]interface{}, keys ...string) (float64, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case float64:
				return val, true
			case string:
				if strings.TrimSpace(val) == "" {
					continue
				}
				f, err := strconv.ParseFloat(val, 64)
				if err == nil {
					return f, true
				}
			case json.Number:
				f, err := val.Float64()
				if err == nil {
					return f, true
				}
			}
		}
	}
	return 0, false
}

func getInt64Field(m map[string]interface{}, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case int64:
				return val
			case int:
				return int64(val)
			case float64:
				return int64(val)
			case json.Number:
				i, err := val.Int64()
				if err == nil {
					return i
				}
				f, ferr := val.Float64()
				if ferr == nil {
					return int64(f)
				}
			case string:
				s := strings.TrimSpace(val)
				if s == "" {
					continue
				}
				i, err := strconv.ParseInt(s, 10, 64)
				if err == nil {
					return i
				}
				f, ferr := strconv.ParseFloat(s, 64)
				if ferr == nil {
					return int64(f)
				}
			}
		}
	}
	return 0
}

func getBoolField(m map[string]interface{}, keys ...string) bool {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case bool:
				return val
			case string:
				switch strings.ToLower(strings.TrimSpace(val)) {
				case "1", "true", "yes":
					return true
				}
			case float64:
				return val != 0
			case json.Number:
				i, _ := val.Int64()
				return i != 0
			}
		}
	}
	return false
}

func getOptionalBoolField(m map[string]interface{}, keys ...string) (bool, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch val := v.(type) {
			case bool:
				return val, true
			case string:
				switch strings.ToLower(strings.TrimSpace(val)) {
				case "1", "true", "yes":
					return true, true
				case "0", "false", "no":
					return false, true
				}
			case float64:
				return val != 0, true
			case json.Number:
				i, _ := val.Int64()
				return i != 0, true
			}
		}
	}
	return false, false
}

func resolveTraceKey(lifecycleID, tradeID, orderID, orderLinkID, execID string) string {
	lifecycleID = strings.TrimSpace(lifecycleID)
	if lifecycleID != "" {
		return lifecycleID
	}
	tradeID = strings.TrimSpace(tradeID)
	if tradeID != "" {
		return tradeID
	}
	orderID = strings.TrimSpace(orderID)
	if orderID != "" {
		return orderID
	}
	orderLinkID = strings.TrimSpace(orderLinkID)
	if orderLinkID != "" {
		return orderLinkID
	}
	return strings.TrimSpace(execID)
}

func buildExecutionFillLogPayload(item map[string]interface{}, symbol, source, lifecycleID string, lifecycleInferred bool) map[string]interface{} {
	fillSymbol := getStringField(item, "symbol")
	if fillSymbol == "" {
		fillSymbol = symbol
	}
	execSide := normalizeSide(getStringField(item, "execSide", "side"))
	positionSide := normalizeSide(getStringField(item, "positionSide", "side"))
	if positionSide == "" {
		positionSide = execSide
	}
	orderID := getStringField(item, "orderId", "orderID")
	orderLinkID := getStringField(item, "orderLinkId", "orderLinkID")
	execID := getStringField(item, "execId", "execID")
	rawTradeID := getStringField(item, "tradeId", "tradeID")
	lastLiquidityInd := getStringField(item, "lastLiquidityInd")
	isMaker, hasIsMaker := getOptionalBoolField(item, "isMaker")
	if !hasIsMaker {
		lq := strings.ToLower(strings.TrimSpace(lastLiquidityInd))
		switch {
		case strings.Contains(lq, "maker"), strings.Contains(lq, "add"):
			isMaker = true
			hasIsMaker = true
		case strings.Contains(lq, "taker"), strings.Contains(lq, "remove"):
			isMaker = false
			hasIsMaker = true
		}
	}
	traceKey := resolveTraceKey(lifecycleID, rawTradeID, orderID, orderLinkID, execID)
	var isMakerValue interface{}
	if hasIsMaker {
		isMakerValue = isMaker
	}
	return map[string]interface{}{
		"ts":                time.Now().UTC().Format(time.RFC3339Nano),
		"ts_epoch_ms":       time.Now().UTC().UnixMilli(),
		"symbol":            fillSymbol,
		"source":            source,
		"traceKey":          traceKey,
		"lifecycleId":       lifecycleID,
		"lifecycleInferred": lifecycleInferred,
		"orderId":           orderID,
		"orderLinkId":       orderLinkID,
		"execId":            execID,
		"tradeId":           rawTradeID,
		"side":              execSide,
		"positionSide":      positionSide,
		"execQty":           getFloatField(item, "execQty", "qty"),
		"execPrice":         getFloatField(item, "execPrice", "price"),
		"execFee":           getFloatField(item, "execFee"),
		"feeRate":           getFloatField(item, "feeRate"),
		"isMaker":           isMakerValue,
		"isMakerKnown":      hasIsMaker,
		"execPnl":           getFloatField(item, "execPnl", "closedPnl"),
		"execTime":          getInt64Field(item, "execTime", "execTimestamp", "createdTime"),
		"createType":        getStringField(item, "createType"),
		"stopOrderType":     getStringField(item, "stopOrderType"),
		"reduceOnly":        getBoolField(item, "reduceOnly"),
		"closeOnTrigger":    getBoolField(item, "closeOnTrigger"),
		"triggerPrice":      getFloatField(item, "triggerPrice"),
		"markPrice":         getFloatField(item, "markPrice"),
		"indexPrice":        getFloatField(item, "indexPrice"),
		"orderType":         getStringField(item, "orderType"),
		"timeInForce":       getStringField(item, "timeInForce"),
		"lastLiquidityInd":  lastLiquidityInd,
	}
}

func requiredExecutionFillFields(payload map[string]interface{}) []string {
	missing := make([]string, 0, 8)
	requiredString := []string{"execId", "orderId", "lifecycleId", "tradeId"}
	for _, key := range requiredString {
		if strings.TrimSpace(fmt.Sprint(payload[key])) == "" {
			missing = append(missing, key)
		}
	}
	requiredFloat := []string{"execQty", "execPrice"}
	for _, key := range requiredFloat {
		if math.Abs(getFloatField(payload, key)) <= 0 {
			missing = append(missing, key)
		}
	}
	if getInt64Field(payload, "execTime") <= 0 {
		missing = append(missing, "execTime")
	}
	if !getBoolField(payload, "isMakerKnown") {
		missing = append(missing, "isMaker")
	}
	return missing
}

func logExecutionFillJSON(item map[string]interface{}, symbol, source, lifecycleID string, lifecycleInferred bool) {
	payload := buildExecutionFillLogPayload(item, symbol, source, lifecycleID, lifecycleInferred)
	if strings.TrimSpace(fmt.Sprint(payload["traceKey"])) == "" {
		logWarning("trace_key_missing source=%s lifecycleId=%v tradeId=%v orderId=%v orderLinkId=%v execId=%v",
			source, payload["lifecycleId"], payload["tradeId"], payload["orderId"], payload["orderLinkId"], payload["execId"])
	}
	if missing := requiredExecutionFillFields(payload); len(missing) > 0 {
		logWarning("execution_fill_anomaly type=missing_required_fields missing=%s traceKey=%v lifecycleId=%v orderId=%v execId=%v",
			strings.Join(missing, ","), payload["traceKey"], payload["lifecycleId"], payload["orderId"], payload["execId"])
	}
	b, err := json.Marshal(payload)
	if err != nil {
		logError("failed to marshal execution fill JSON log: %v", err)
		return
	}
	logInfo("execution_fill %s", string(b))
}

func handleExecutionMessage(raw []byte, trader *strategy.Trader, symbol, source string) {
	var msg executionMsg
	if json.Unmarshal(raw, &msg) != nil || len(msg.Data) == 0 {
		return
	}
	for _, item := range msg.Data {
		sym := getStringField(item, "symbol")
		if symbol != "" && sym != "" && sym != symbol {
			continue
		}
		execSideRaw := getStringField(item, "execSide", "side")
		positionSideRaw := getStringField(item, "positionSide", "side")
		execSide := normalizeSide(execSideRaw)
		positionSide := normalizeSide(positionSideRaw)
		if positionSide == "" {
			positionSide = execSide
		}
		execQty := getFloatField(item, "execQty", "qty")
		execPrice := getFloatField(item, "execPrice", "price")
		execFee := getFloatField(item, "execFee")
		execPnl := getFloatField(item, "execPnl")
		exchangeNet, hasExchangeNet := getOptionalFloatField(item, "closedPnl")
		createType := getStringField(item, "createType")
		stopOrderType := getStringField(item, "stopOrderType")
		execID := getStringField(item, "execId", "execID")
		orderID := getStringField(item, "orderId", "orderID")
		orderLinkID := getStringField(item, "orderLinkId", "orderLinkID")
		lastLiquidityInd := getStringField(item, "lastLiquidityInd")
		isMaker, hasIsMaker := getOptionalBoolField(item, "isMaker")
		if !hasIsMaker {
			lq := strings.ToLower(strings.TrimSpace(lastLiquidityInd))
			switch {
			case strings.Contains(lq, "maker"), strings.Contains(lq, "add"):
				isMaker = true
				hasIsMaker = true
			case strings.Contains(lq, "taker"), strings.Contains(lq, "remove"):
				isMaker = false
				hasIsMaker = true
			}
		}
		if source == "ws" {
			markExecutionSeenFromWS(execID)
			if trader != nil && trader.State != nil {
				trader.State.RecordWSExecution(time.Now().UTC())
			}
		}
		reduceOnly := getBoolField(item, "reduceOnly")
		closedSize := getFloatField(item, "closedSize")
		leavesQty := getFloatField(item, "leavesQty")
		positionSizeAfter := getFloatField(item, "positionSizeAfter", "positionSize")
		tradeID := ""
		lifecycleInferred := false
		if cfg != nil && cfg.EnableLifecycleID {
			tradeID, lifecycleInferred = trader.ResolveExecutionLifecycleID(orderID, execID, orderLinkID)
		} else {
			tradeID = trader.ResolveExecutionTradeID(orderID, execID)
		}
		if lifecycleInferred {
			logWarning("Lifecycle inferred for execution: source=%s orderId=%s orderLinkId=%s execId=%s lifecycleId=%s",
				source, orderID, orderLinkID, execID, tradeID)
		}
		if cfg != nil && cfg.EnableFillJSONLog {
			logExecutionFillJSON(item, symbol, source, tradeID, lifecycleInferred)
		} else {
			traceKey := resolveTraceKey(tradeID, "", orderID, orderLinkID, execID)
			logInfo("Execution fill: traceKey=%s tradeID=%s execSide=%s positionSide=%s reduceOnly=%t closedSize=%.4f leavesQty=%.4f positionSizeAfter=%.4f execQty=%.4f execPrice=%.2f execFee=%.6f execPnl=%.6f createType=%s stopOrderType=%s execId=%s orderId=%s orderLinkId=%s",
				traceKey, tradeID, execSide, positionSide, reduceOnly, closedSize, leavesQty, positionSizeAfter, execQty, execPrice, execFee, execPnl, createType, stopOrderType, execID, orderID, orderLinkID)
		}
		trader.ProcessExecutionEvent(models.ExecutionEvent{
			TradeID:           tradeID,
			ExecID:            execID,
			OrderID:           orderID,
			OrderLinkID:       orderLinkID,
			ExecSide:          execSide,
			PositionSide:      positionSide,
			HasIsMaker:        hasIsMaker,
			IsMaker:           isMaker,
			LastLiquidityInd:  lastLiquidityInd,
			ReduceOnly:        reduceOnly,
			Qty:               execQty,
			Price:             execPrice,
			ExecFee:           execFee,
			ExecPnl:           execPnl,
			HasExchangeNet:    hasExchangeNet,
			ExchangeNet:       exchangeNet,
			CreateType:        createType,
			StopOrderType:     stopOrderType,
			ClosedSize:        closedSize,
			LeavesQty:         leavesQty,
			PositionSizeAfter: positionSizeAfter,
		}, source)
	}
}

type positionItem struct {
	Symbol     string `json:"symbol"`
	Side       string `json:"side"`
	Size       string `json:"size"`
	TakeProfit string `json:"takeProfit"`
	StopLoss   string `json:"stopLoss"`
	AvgPrice   string `json:"avgPrice"`
}

func updatePositionFromMessage(state *models.State, symbol string, raw []byte) {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) == 0 {
		return
	}

	var items []positionItem
	if json.Unmarshal(envelope.Data, &items) == nil {
		for _, item := range items {
			if symbol != "" && item.Symbol != "" && item.Symbol != symbol {
				continue
			}
			applyPositionItem(state, item)
		}
		return
	}

	var item positionItem
	if json.Unmarshal(envelope.Data, &item) != nil {
		return
	}
	if symbol != "" && item.Symbol != "" && item.Symbol != symbol {
		return
	}
	applyPositionItem(state, item)
}

func applyPositionItem(state *models.State, item positionItem) {
	size := parseFloat(item.Size)
	side := normalizeSide(item.Side)
	tp := parseFloat(item.TakeProfit)
	sl := parseFloat(item.StopLoss)
	entry := parseFloat(item.AvgPrice)
	now := time.Now()

	if size <= 0 {
		side = ""
		size = 0
		entry = 0
		tp = 0
		sl = 0
	}

	state.StatusLock.Lock()
	state.LastPosition = models.PositionSnapshot{
		Side:       side,
		Size:       size,
		EntryPrice: entry,
		TakeProfit: tp,
		StopLoss:   sl,
		UpdatedAt:  now,
	}
	state.StatusLock.Unlock()

	if size > 0 {
		state.Lock()
		state.PosSide = side
		state.OrderQty = size
		if state.PositionState == "" {
			state.PositionState = models.PositionStateFlat
		}
		if state.PositionState == models.PositionStateOpening {
			state.OpeningPosAck = true
			if state.OpeningExecAck {
				state.PositionState = models.PositionStateOpen
			}
		}
		if state.PositionState == models.PositionStateFlat {
			state.PositionState = models.PositionStateOpen
			state.OpeningExecAck = true
			state.OpeningPosAck = true
		}
		state.Unlock()
		logInfo("Position updated: Side=%s, Size=%.4f TP=%.2f SL=%.2f", side, size, tp, sl)
	} else {
		state.Lock()
		state.PosSide = ""
		state.OrderQty = 0
		if state.PositionState == "" {
			state.PositionState = models.PositionStateFlat
		}
		if state.PositionState == models.PositionStateClosing || state.PositionState == models.PositionStateOpen || state.PositionState == models.PositionStateOpening {
			state.PositionState = models.PositionStateFlat
			state.OpeningExecAck = false
			state.OpeningPosAck = false
			state.ActiveTradeID = ""
		}
		state.Unlock()
		logInfo("Position closed")
	}
}

func parseFloat(raw string) float64 {
	if raw == "" {
		return 0
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return val
}

func resyncPrivateState(apiClient *api.RESTClient, trader *strategy.Trader, state *models.State, symbol string) error {
	logInfo("Private WS resync started")

	positions, err := apiClient.GetPositionList(symbol)
	if err != nil {
		return fmt.Errorf("resync getPosition: %w", err)
	}
	openOrders, err := apiClient.GetOpenOrders(symbol)
	if err != nil {
		return fmt.Errorf("resync getOpenOrders: %w", err)
	}
	tp, sl, err := apiClient.GetTradingStop(symbol)
	if err != nil {
		return fmt.Errorf("resync getTradingStop: %w", err)
	}

	side := ""
	size := 0.0
	entry := 0.0
	for _, p := range positions {
		psize := parseFloat(p.Size)
		if psize <= 0 {
			continue
		}
		side = normalizeSide(p.Side)
		size = psize
		entry = parseFloat(p.AvgPrice)
		if p.TakeProfit != "" {
			tp = parseFloat(p.TakeProfit)
		}
		if p.StopLoss != "" {
			sl = parseFloat(p.StopLoss)
		}
		break
	}

	now := time.Now()
	state.StatusLock.Lock()
	state.LastPosition = models.PositionSnapshot{
		Side:       side,
		Size:       size,
		EntryPrice: entry,
		TakeProfit: tp,
		StopLoss:   sl,
		UpdatedAt:  now,
	}
	state.StatusLock.Unlock()

	if size > 0 {
		state.Lock()
		state.PosSide = side
		state.OrderQty = size
		state.PositionState = models.PositionStateOpen
		state.OpeningExecAck = true
		state.OpeningPosAck = true
		state.Unlock()
		if trader.ActiveTradeID() == "" {
			trader.ResolveExecutionTradeID("", "resync-"+strconv.FormatInt(now.UnixNano(), 10))
		}
	} else {
		state.Lock()
		state.PosSide = ""
		state.OrderQty = 0
		state.PositionState = models.PositionStateFlat
		state.OpeningExecAck = false
		state.OpeningPosAck = false
		state.ActiveTradeID = ""
		state.Unlock()
	}

	state.Lock()
	state.StopCtrl.AppliedTP = tp
	state.StopCtrl.AppliedSL = sl
	state.StopCtrl.DesiredTP = tp
	state.StopCtrl.DesiredSL = sl
	state.StopCtrl.UpdatedAt = now
	state.Unlock()

	execStats, err := runExecutionBackfillCycle(apiClient, trader, state, symbol, "resync", 200, 3, 600)
	if err != nil {
		return fmt.Errorf("resync execution backfill: %w", err)
	}
	_ = trader.ClearTradingBlock(models.BlockedReasonReconnect, "resync_complete")

	logInfo("Private WS resync complete: positionSize=%.4f side=%s openOrders=%d executions=%d processed=%d deduped=%d pages=%d cursor_before=%s cursor_after=%s tp=%.2f sl=%.2f",
		size, side, len(openOrders), execStats.Fetched, execStats.Processed, execStats.Deduped, execStats.Pages, execStats.CursorBefore, execStats.CursorAfter, tp, sl)
	return nil
}

func main() {
	// Load configuration first to initialize cfg
	cfg = config.LoadConfig()

	// Parse command line flags first to check for daemon-related commands
	daemonStart := flag.Bool("start-daemon", false, "Start the application as a daemon")
	daemonStop := flag.Bool("stop-daemon", false, "Stop the daemon process")
	daemonRestart := flag.Bool("restart-daemon", false, "Restart the daemon process")
	versionFlag := flag.Bool("version", false, "Print build/version info and exit")

	debugLevel := flag.Int("debug", cfg.LogLevel, "set log level (0=DEBUG, 1=INFO, 2=WARNING, 3=ERROR)")
	flag.Parse()

	// Update logging settings based on flag
	cfg.LogLevel = *debugLevel
	cfg.Debug = cfg.LogLevel == 0

	if *versionFlag {
		printVersion(os.Stdout)
		return
	}

	// Handle daemon commands
	if *daemonStart || *daemonStop || *daemonRestart {
		if *daemonStart {
			logInfo("Starting daemon...")
			args := []string{}
			for _, arg := range os.Args[1:] {
				if arg != "-start-daemon" {
					args = append(args, arg)
				}
			}
			if err := daemon.StartDaemon(args); err != nil {
				logFatal("Failed to start daemon: %v", err)
			}
			return
		} else if *daemonStop {
			logInfo("Stopping daemon...")
			if err := daemon.StopDaemon(); err != nil {
				logFatal("Failed to stop daemon: %v", err)
			}
			return
		} else if *daemonRestart {
			logInfo("Restarting daemon...")
			args := []string{}
			for _, arg := range os.Args[1:] {
				if arg != "-restart-daemon" {
					args = append(args, arg)
				}
			}
			if err := daemon.RestartDaemon(args); err != nil {
				logFatal("Failed to restart daemon: %v", err)
			}
			return
		}
	}

	// Initialize logging
	if err := initLogging(); err != nil {
		log.Fatalf("Failed to initialize logging: %v", err)
		return
	}

	logInfo("Application starting...")
	version := currentVersionInfo()
	status.SetBuildInfo(status.BuildInfo{Commit: version.GitCommit, BuildTime: version.BuildTime})
	logInfo("%s", formatVersionInfo(version))
	logInfo("Daemon mode: %t", cfg.DaemonMode)

	// Initialize state early so status endpoint is available even when startup network checks fail.
	state := &models.State{
		Debug:           cfg.Debug,
		DynamicTP:       cfg.DynamicTP,
		SlPerc:          cfg.SlPerc,
		TrailPerc:       cfg.TrailPerc,
		SmaLen:          cfg.SmaLen,
		PositionState:   models.PositionStateFlat,
		BidsMap:         make(map[string]float64),
		AsksMap:         make(map[string]float64),
		TPChan:          make(chan models.TPJob, 8),
		StopIntentChan:  make(chan models.StopIntent, 128),
		SigChan:         make(chan models.Signal, 128),
		MarketRegime:    "range", // default
		RegimeCandidate: "range",
		OrderTradeIDs:   make(map[string]string),
		ExecTradeIDs:    make(map[string]string),
		ProcessedExec:   make(map[string]time.Time),
		ExecDedupMax:    5000,
		ExecDedupTTL:    24 * time.Hour,
	}

	applyRuntimeFeatures(state, cfg)
	if err := config.Validate(cfg); err != nil {
		logFatal("Invalid configuration: %v", err)
	}

	statusServer := status.StartServer(cfg, state, logger)
	if cfg.EnableDryRun {
		runDryRunMode(state, statusServer)
		return
	}

	// Create API client
	apiClient := api.NewRESTClient(cfg, logger)

	// Test API connection to ensure credentials are valid
	if _, err := apiClient.GetBalance("USDT"); err != nil {
		logError("API authentication failed: %v", err)
		logFatal("Please check your API credentials")
	}

	logInfo("API connection established successfully")

	// Initialize instrument info
	var err error
	state.Instr, err = apiClient.GetInstrumentInfo(cfg.Symbol)
	if err != nil {
		logFatal("Instrument info: %v", err)
	}
	if state.Instr.TickSize <= 0 {
		state.Instr.TickSize = 0.1
		logWarning("TickSize == 0 → fallback %.2f", state.Instr.TickSize)
	}

	if bal, err := apiClient.GetBalance("USDT"); err == nil {
		logInfo("Init balance: %.2f USDT", bal)
	}
	logInfo("Symbol %s — TickSize %.6f  MinQty %.6f  QtyStep %.6f",
		cfg.Symbol, state.Instr.TickSize, state.Instr.MinQty, state.Instr.QtyStep)

	// Connect to private WebSocket
	privConn, err := connectPrivateWS(apiClient, state)
	if err != nil {
		logFatal("Private WS dial: %v", err)
	}

	// Connect to public WebSocket
	pubWSURL := cfg.DemoWSPublicURL
	logInfo("Connecting to public WebSocket: %s", pubWSURL)
	pubConn, err := newWSConn(pubWSURL)
	if err != nil && strings.Contains(pubWSURL, "stream-demo.bybit.com") {
		// Fallback to mainnet public stream if demo WS rejects handshake
		mainnetWS := "wss://stream.bybit.com/v5/public/linear"
		logWarning("Demo public WS failed (%v); falling back to %s", err, mainnetWS)
		pubConn, err = newWSConn(mainnetWS)
		if err == nil {
			pubWSURL = mainnetWS
		}
	}
	if err != nil {
		logError("Failed to connect to public WebSocket: %v", err)
		logFatal("Public WS dial: %v", err)
	}
	cfg.DemoWSPublicURL = pubWSURL
	logInfo("Successfully connected to public WebSocket: %s", cfg.DemoWSPublicURL)
	state.PubPtr.Store(pubConn)

	klineTopic := fmt.Sprintf("kline.%s.%s", cfg.Interval, cfg.Symbol)
	obTopic := fmt.Sprintf("orderbook.%d.%s", cfg.ObDepth, cfg.Symbol)

	subReq := map[string]interface{}{
		"op":   "subscribe",
		"args": []string{klineTopic, obTopic},
	}
	dbg("Sending subscription request to exchange: %v", subReq)

	if err := pubConn.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{klineTopic, obTopic},
	}); err != nil {
		logError("Failed to send subscription to exchange: %v", err)
		logFatal("Public sub send: %v", err)
	}

	// Read subscription response
	_, subResp, err := pubConn.ReadMessage()
	if err != nil {
		logError("Failed to read subscription response from exchange: %v", err)
		logFatal("Public sub read: %v", err)
	}
	dbg("Received subscription response from exchange: %s", string(subResp))

	// Start ping ticker
	ticker := time.NewTicker(time.Duration(cfg.PingPeriod) * time.Second)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			if c := state.PrivPtr.Load(); c != nil {
				c.WriteMessage(websocket.PingMessage, nil)
			}
			if c := state.PubPtr.Load(); c != nil {
				c.WriteMessage(websocket.PingMessage, nil)
			}
		}
	}()

	// Create trader
	trader := strategy.NewTrader(apiClient, cfg, state, logger)
	trader.SetTradingBlocked(models.BlockedReasonReconnect, "startup_resync")
	if err := resyncPrivateState(apiClient, trader, state, cfg.Symbol); err != nil {
		logFatal("Private WS initial resync failed: %v", err)
	}
	_ = trader.ClearTradingBlock(models.BlockedReasonReconnect, "startup_resync_ok")

	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone, state, cfg.Symbol)
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	defer runtimeCancel()
	startKPIMonitorWorker(runtimeCtx, state, cfg)

	// Start indicator and trading goroutines
	go trader.StartStopController()
	go trader.SMAMovingAverageWorker()
	go trader.Trader()
	go trader.SyncPositionRealTime()
	startExecutionBackfillWorker(runtimeCtx, apiClient, trader, state, cfg.Symbol)

	// Start signal stats logging
	go func() {
		for range time.Tick(5 * time.Minute) {
			trader.LogSignalStats()
		}
	}()

	// Start profit stats logging
	go func() {
		for range time.Tick(10 * time.Minute) {
			logInfo("Profit stats - Realized P&L: %.2f, Total Profit: %.2f, Total Loss: %.2f",
				state.RealizedPnL, state.TotalProfit, state.TotalLoss)
		}
	}()

	// Start market regime detection
	go func() {
		for {
			trader.DetectMarketRegime()
			time.Sleep(5 * time.Minute) // Check every 5 minutes
		}
	}()

	// Setup signal handling for graceful shutdown
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	// Main event loop
	for {
		// Drain private buffer
	drainWallet:
		for {
			select {
			case raw := <-walletChan:
				dbg("Received private message from exchange: %s", string(raw))
				var peek struct {
					Topic string `json:"topic"`
				}
				if json.Unmarshal(raw, &peek) == nil {
					switch peek.Topic {
					case "execution":
						handleExecutionMessage(raw, trader, cfg.Symbol, "ws")
					}
				}
			default:
				break drainWallet
			}
		}

		// Read public messages
		_, raw, err := pubConn.ReadMessage()
		if err != nil {
			logError("Public WS read: %v — reconnect…", err)
			pubConn.Close()
			if pubConn, err = reconnectPublic(klineTopic, obTopic, state); err != nil {
				logFatal("Fatal: cannot restore public stream: %v", err)
			}
			continue
		}

		dbg("Received public message from exchange: %s", string(raw))

		// Route by topic
		var peek struct {
			Topic string `json:"topic"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}

		switch {
		case strings.HasPrefix(peek.Topic, "kline."):
			var km models.KlineMsg
			if json.Unmarshal(raw, &km) == nil && len(km.Data) > 0 {
				if !km.Data[0].Confirm {
					dbg("Ignoring kline update (not closed): %s close=%s", peek.Topic, km.Data[0].Close)
					break
				}
				logInfo("Processing closed kline from exchange: %s o/h/l/c=%s/%s/%s/%s vol=%s", peek.Topic, km.Data[0].Open, km.Data[0].High, km.Data[0].Low, km.Data[0].Close, km.Data[0].Volume)
				trader.OnClosedCandle(km.Data[0])
			}
		case strings.HasPrefix(peek.Topic, "orderbook."):
			var om models.OrderbookMsg
			if json.Unmarshal(raw, &om) != nil {
				break
			}
			logInfo("Received orderbook %s update from exchange: %s", strings.ToLower(om.Type), peek.Topic)
			switch strings.ToLower(om.Type) {
			case "snapshot":
				applySnapshot(state, om.Data.B, om.Data.A)
			case "delta":
				applyDelta(state, om.Data.B, om.Data.A)
			}
		}

		// Auto-reconnect private WS
		select {
		case <-walletDone:
			logError("Private WS closed — reconnect…")
			trader.SetTradingBlocked(models.BlockedReasonReconnect, "private_ws_disconnected")
			for retry := 1; ; retry++ {
				time.Sleep(time.Duration(retry*2) * time.Second)
				logInfo("Attempting private WebSocket reconnect #%d", retry)
				if privConn, err = connectPrivateWS(apiClient, state); err == nil {
					if err := resyncPrivateState(apiClient, trader, state, cfg.Symbol); err != nil {
						logError("Private reconnect resync failed: %v", err)
						privConn.Close()
						continue
					}
					_ = trader.ClearTradingBlock(models.BlockedReasonReconnect, "private_ws_resync_ok")
					logInfo("Successfully reconnected to private WebSocket and resynced state")
					walletDone = make(chan struct{})
					go walletListener(privConn, walletChan, walletDone, state, cfg.Symbol)
					break
				}
				logError("Private reconnect #%d: %v", retry, err)
			}
		case sig := <-signals:
			logInfo("Received signal %s, shutting down gracefully...", sig)
			runtimeCancel()
			// Cancel all active orders to avoid unwanted positions
			if trader != nil {
				logInfo("Cancelling all active orders before shutdown...")
				go trader.PositionManager.CancelAllOrders(cfg.Symbol)
			}

			// Close WebSocket connections
			logInfo("Closing WebSocket connections...")
			if pubConn := state.PubPtr.Load(); pubConn != nil {
				pubConn.Close()
			}

			if privConn := state.PrivPtr.Load(); privConn != nil {
				privConn.Close()
			}

			// Perform cleanup operations
			if statusServer != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = statusServer.Shutdown(ctx)
				cancel()
			}
			trader.Shutdown()
			if err := logger.Sync(); err != nil {
				logError("Error syncing logger: %v", err)
			}
			return
		default:
		}
	}
}
