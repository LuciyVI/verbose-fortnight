package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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
	"verbose-fortnight/strategy"
	"verbose-fortnight/web_interface"
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

// Initialize the application by setting up configuration, logging, and API client
func initializeApp() (*api.RESTClient, *models.State, *strategy.Trader, *web_interface.WebUI) {
	// Load configuration first to initialize cfg
	cfg = config.LoadConfig()

	// Parse command line flags first to check for daemon-related commands
	daemonStart := flag.Bool("start-daemon", false, "Start the application as a daemon")
	daemonStop := flag.Bool("stop-daemon", false, "Stop the daemon process")
	daemonRestart := flag.Bool("restart-daemon", false, "Restart the daemon process")

	// Keep the debug flag for compatibility
	// Use a temporary variable for the debug flag since cfg is initialized after flags are parsed
	debugFlag := flag.Bool("debug", false, "enable debug logs")
	flag.Parse()

	// Update the config with the debug flag value after parsing
	cfg.Debug = *debugFlag

	// Handle daemon commands
	if *daemonStart || *daemonStop || *daemonRestart {
		// Initialize logging for daemon operations
		if err := initLogging(); err != nil {
			log.Fatalf("Failed to initialize logging: %v", err)
			return nil, nil, nil, nil
		}

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
			return nil, nil, nil, nil
		} else if *daemonStop {
			logInfo("Stopping daemon...")
			if err := daemon.StopDaemon(); err != nil {
				logFatal("Failed to stop daemon: %v", err)
			}
			return nil, nil, nil, nil
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
			return nil, nil, nil, nil
		}
	}

	// Initialize logging
	if err := initLogging(); err != nil {
		log.Fatalf("Failed to initialize logging: %v", err)
		return nil, nil, nil, nil
	}

	logInfo("Application starting...")
	logInfo("Daemon mode: %t", cfg.DaemonMode)

	// Create API client
	apiClient := api.NewRESTClient(cfg, logger)

	// Test API connection to ensure credentials are valid
	if _, err := apiClient.GetBalance("USDT"); err != nil {
		logError("API authentication failed: %v", err)
		logFatal("Please check your API credentials")
	}

	logInfo("API connection established successfully")

	// Initialize state
	state := &models.State{
		Debug:     cfg.Debug,
		DynamicTP: cfg.DynamicTP,
		SlPerc:    cfg.SlPerc,
		TrailPerc: cfg.TrailPerc,
		SmaLen:    cfg.SmaLen,
		BidsMap:   make(map[string]float64),
		AsksMap:   make(map[string]float64),
		TPChan:    make(chan models.TPJob, 8),
		SigChan:   make(chan models.Signal, 32),
		MarketRegime: "range", // default
	}

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

	// Create WebUI
	webUI := web_interface.NewWebUI(cfg, state)

	// Create trader
	trader := strategy.NewTrader(apiClient, cfg, state, logger, webUI)

	return apiClient, state, trader, webUI
}

// logDebug logs debug messages
func logDebug(format string, v ...interface{}) {
	logger.Debug(format, v...)
}

// logInfo logs info messages
func logInfo(format string, v ...interface{}) {
	logger.Info(format, v...)
}

// dbg is an alias for logDebug
func dbg(format string, v ...interface{}) {
	if cfg.Debug {
		logger.Debug(format, v...)
	}
}

// logWarning logs warning messages
func logWarning(format string, v ...interface{}) {
	logger.Warning(format, v...)
}

// logError logs error messages
func logError(format string, v ...interface{}) {
	logger.Error(format, v...)
}

// logFatal logs fatal messages and exits
func logFatal(format string, v ...interface{}) {
	logger.Fatal(format, v...)
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

	// Subscribe to wallet and position
	subReq := map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position"},
	}
	dbg("Sending subscription request to exchange: %v", subReq)

	if err := ws.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position"},
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
func walletListener(ws *websocket.Conn, out chan<- []byte, done chan<- struct{}, state *models.State) {
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
			var posUpdate struct {
				Data struct {
					Side string `json:"side"`
					Size string `json:"size"`
				} `json:"data"`
			}
			if json.Unmarshal(msg, &posUpdate) == nil {
				size, _ := strconv.ParseFloat(posUpdate.Data.Size, 64)
				if size > 0 {
					state.PosSide = normalizeSide(posUpdate.Data.Side)
					state.OrderQty = size
					logInfo("Position updated: Side=%s, Size=%.4f", state.PosSide, state.OrderQty)
				} else {
					state.PosSide = ""
					state.OrderQty = 0
					logInfo("Position closed")
				}
			}
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

func main() {
	// Initialize application components
	apiClient, state, trader, _ := initializeApp() // webUI is declared but not used in the main function
	if apiClient == nil {
		// If app initialization handled daemon commands, exit early
		return
	}

	// Connect to private WebSocket
	privConn, err := connectPrivateWS(apiClient, state)
	if err != nil {
		logFatal("Private WS dial: %v", err)
	}

	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone, state)

	// Connect to public WebSocket
	logInfo("Connecting to public WebSocket: %s", cfg.DemoWSPublicURL)
	pubConn, err := newWSConn(cfg.DemoWSPublicURL)
	if err != nil {
		logError("Failed to connect to public WebSocket: %v", err)
		logFatal("Public WS dial: %v", err)
	}
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

	// Start indicator and trading goroutines
	trader.InitializeNewSignalSystem() // Initialize the new signal generation system
	go trader.Trader()
	go trader.SyncPositionRealTime()
	go trader.TPWorker() // take profit worker

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

	// Start signal processing from channel-based signals (SMA Worker)
	go func() {
		for {
			select {
			case sig := <-state.SigChan:
				switch sig.Kind {
				case "SMA_LONG", "GC":
					trader.HandleLongSignal(sig.ClosePrice)
				case "SMA_SHORT":
					trader.HandleShortSignal(sig.ClosePrice)
				}
			}
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
			if json.Unmarshal(raw, &km) == nil && len(km.Data) > 0 && km.Data[0].Confirm {
				logInfo("Received kline data update from exchange: %s", peek.Topic)
				trader.OnClosedCandle(km.Data[0].CloseFloat())
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
			for retry := 1; ; retry++ {
				time.Sleep(time.Duration(retry*2) * time.Second)
				logInfo("Attempting private WebSocket reconnect #%d", retry)
				if privConn, err = connectPrivateWS(apiClient, state); err == nil {
					logInfo("Successfully reconnected to private WebSocket")
					walletDone = make(chan struct{})
					go walletListener(privConn, walletChan, walletDone, state)
					break
				}
				logError("Private reconnect #%d: %v", retry, err)
			}
		case sig := <-signals:
			logInfo("Received signal %s, shutting down gracefully...", sig)
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
			if err := logger.Sync(); err != nil {
				logError("Error syncing logger: %v", err)
			}
			return
		default:
		}
	}
}