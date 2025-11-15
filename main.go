package main

import (
	"flag"
	"log"
	"time"

	"go-trader/config"
	"go-trader/logger"
	"go-trader/api"
	"go-trader/position"
	"go-trader/websocket"
	"go-trader/strategy"
	"go-trader/types"
)

func main() {
	// Parse command line flags
	debug := flag.Bool("debug", false, "enable debug logs")
	configFile := flag.String("config", "", "configuration file path")
	flag.Parse()

	// Load configuration
	var cfg *config.Config
	var err error
	
	if *configFile != "" {
		cfg, err = config.LoadConfigFromFile(*configFile)
		if err != nil {
			log.Fatalf("Failed to load config file: %v", err)
		}
	} else {
		cfg = config.DefaultConfig()
	}
	
	// Override config with command line flag
	cfg.Debug = *debug

	// Initialize logger
	logInstance := logger.New(cfg.Debug)

	// Initialize API client
	apiClient := api.NewAPIClient(cfg, logInstance)

	// Initialize position manager
	positionManager := position.NewManager(cfg, logInstance, apiClient)

	// Initialize WebSocket hub
	wsHub := websocket.NewWSHub(cfg, logInstance)

	// Initialize strategy
	tradingStrategy := strategy.NewStrategy(cfg, logInstance, apiClient, positionManager, wsHub)

	// Initialize instrument info
	instr, err := apiClient.GetInstrumentInfo(cfg.Symbol)
	if err != nil {
		log.Fatalf("Instrument info: %v", err)
	}
	if instr.TickSize <= 0 {
		instr.TickSize = 0.1
		logInstance.Error("TickSize == 0 → fallback %.2f", instr.TickSize)
	}

	// Log initial balance
	if bal, err := apiClient.GetBalance("USDT"); err == nil {
		log.Printf("Init balance: %.2f USDT", bal)
	}

	log.Printf("Symbol %s — TickSize %.6f  MinQty %.6f  QtyStep %.6f",
		cfg.Symbol, instr.TickSize, instr.MinQty, instr.QtyStep)

	// Connect to WebSocket
	if err := wsHub.ConnectPrivate(); err != nil {
		log.Fatalf("Private WS dial: %v", err)
	}

	if err := wsHub.ConnectPublic(); err != nil {
		log.Fatalf("Public WS dial: %v", err)
	}

	// Start ping ticker
	wsHub.StartPingTicker()

	// Start strategy components
	go tradingStrategy.SMATradingLogic()
	go tradingStrategy.DetectMarketRegime()

	// Channels for handling WebSocket messages
	klineChan := make(chan types.KlineData, 10)
	obChan := make(chan types.OrderbookMsg, 10)

	// Start WebSocket message handlers
	go wsHub.HandlePublicMessages(klineChan, obChan)
	go wsHub.HandlePrivateMessages()

	// Main processing loop
	for {
		select {
		case klineData := <-klineChan:
			closePrice := klineData.CloseFloat()
			tradingStrategy.OnClosedCandle(closePrice)
		case obData := <-obChan:
			switch obData.Type {
			case "snapshot":
				wsHub.ApplySnapshot(obData.Data.B, obData.Data.A)
			case "delta":
				wsHub.ApplyDelta(obData.Data.B, obData.Data.A)
			}
		case <-time.After(5 * time.Minute):
			// Log signal stats periodically
			logInstance.Info("Signal stats updated")
		}
	}
}