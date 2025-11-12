package web_interface

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"text/template"
	"time"

	"github.com/gorilla/websocket"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

// WebUI handles the web interface
type WebUI struct {
	Config      *config.Config
	State       *models.State
	templates   *template.Template
	upgrader    websocket.Upgrader
	clients     map[*websocket.Conn]bool
	broadcast   chan Message
}

// Message represents a WebSocket message
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// DashboardData represents dashboard statistics
type DashboardData struct {
	TotalPnL        float64         `json:"totalPnL"`
	TotalTrades     int             `json:"totalTrades"`
	WinRate         float64         `json:"winRate"`
	ActivePositions int             `json:"activePositions"`
	CurrentPrice    float64         `json:"currentPrice"`
	DailyChange     float64         `json:"dailyChange"`
	SmaValue        float64         `json:"smaValue"`
	RsiValue        float64         `json:"rsiValue"`
	RecentTrades    []Trade         `json:"recentTrades"`
	OpenPositions   []Position      `json:"openPositions"`
}

// Trade represents a trading record
type Trade struct {
	Timestamp string  `json:"timestamp"`
	Side      string  `json:"side"`
	Price     float64 `json:"price"`
	PnL       float64 `json:"pnl"`
}

// Position represents an open position
type Position struct {
	Symbol      string  `json:"symbol"`
	Side        string  `json:"side"`
	Size        float64 `json:"size"`
	EntryPrice  float64 `json:"entryPrice"`
	MarkPrice   float64 `json:"markPrice"`
	TakeProfit  float64 `json:"takeProfit"`
	StopLoss    float64 `json:"stopLoss"`
	UnrealizedPnL float64 `json:"unrealizedPnL"`
}

// NewWebUI creates a new WebUI instance
func NewWebUI(cfg *config.Config, state *models.State) *WebUI {
	templates := template.Must(template.ParseGlob("web_interface/templates/*.html"))
	
	return &WebUI{
		Config: cfg,
		State:  state,
		templates: templates,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow connections from any origin for development
			},
		},
		clients: make(map[*websocket.Conn]bool),
		broadcast: make(chan Message),
	}
}

// Start initializes the WebUI server
func (w *WebUI) Start(port string) {
	// Serve static files
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("web_interface/static"))))
	
	// Setup routes
	http.HandleFunc("/", w.indexHandler)
	http.HandleFunc("/dashboard", w.dashboardHandler)
	http.HandleFunc("/ws", w.wsHandler)
	http.HandleFunc("/api/stats", w.statsHandler)
	http.HandleFunc("/api/positions", w.positionsHandler)
	http.HandleFunc("/api/trades", w.tradesHandler)
	http.HandleFunc("/control", w.controlHandler)
	
	// Start the broadcast handler for WebSocket updates
	go w.handleBroadcasts()
	
	// Start periodic updates for real-time data
	go w.startPeriodicUpdates()
	
	log.Printf("Starting WebUI server on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// indexHandler serves the main page
func (w *WebUI) indexHandler(rw http.ResponseWriter, r *http.Request) {
	err := w.templates.ExecuteTemplate(rw, "index.html", nil)
	if err != nil {
		log.Printf("Template execution error: %v", err)
		http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
	}
}

// dashboardHandler serves dashboard data
func (w *WebUI) dashboardHandler(rw http.ResponseWriter, r *http.Request) {
	dashboardData := w.getDashboardData()
	jsonData, err := json.Marshal(dashboardData)
	if err != nil {
		http.Error(rw, "JSON marshaling error", http.StatusInternalServerError)
		return
	}
	
	rw.Header().Set("Content-Type", "application/json")
	rw.Write(jsonData)
}

// wsHandler handles WebSocket connections
func (w *WebUI) wsHandler(rw http.ResponseWriter, r *http.Request) {
	conn, err := w.upgrader.Upgrade(rw, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()
	
	// Register client
	w.clients[conn] = true
	defer func() {
		delete(w.clients, conn)
	}()
	
	// Send initial data
	initialData := Message{
		Type: "dashboard_update",
		Data: w.getDashboardData(),
	}
	conn.WriteJSON(initialData)
	
	// Listen for messages from client
	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}
		
		// Process client messages
		switch msg.Type {
		case "subscribe":
			// Client wants to subscribe to updates
			log.Printf("Client subscribed to: %v", msg.Data)
		case "unsubscribe":
			// Client wants to unsubscribe
			log.Printf("Client unsubscribed from: %v", msg.Data)
		case "bot_control":
			// Bot control command (start/stop/etc)
			w.handleBotControl(msg.Data)
		case "close_position":
			// Close specific position
			w.handleClosePosition(msg.Data)
		case "update_settings":
			// Update bot settings
			w.handleUpdateSettings(msg.Data)
		}
	}
}

// statsHandler returns statistics
func (w *WebUI) statsHandler(rw http.ResponseWriter, r *http.Request) {
	stats := struct {
		TotalPnL    float64 `json:"totalPnL"`
		TotalTrades int     `json:"totalTrades"`
		WinRate     float64 `json:"winRate"`
		ActivePos   int     `json:"activePositions"`
	}{
		TotalPnL:    w.State.RealizedPnL + w.State.UnrealizedPnL,
		TotalTrades: w.getTotalTradesCount(),
		WinRate:     w.calculateWinRate(),
		ActivePos:   w.getState().ActivePositions,
	}
	
	jsonData, err := json.Marshal(stats)
	if err != nil {
		http.Error(rw, "JSON marshaling error", http.StatusInternalServerError)
		return
	}
	
	rw.Header().Set("Content-Type", "application/json")
	rw.Write(jsonData)
}

// positionsHandler returns open positions
func (w *WebUI) positionsHandler(rw http.ResponseWriter, r *http.Request) {
	positions := w.getOpenPositions()
	
	jsonData, err := json.Marshal(positions)
	if err != nil {
		http.Error(rw, "JSON marshaling error", http.StatusInternalServerError)
		return
	}
	
	rw.Header().Set("Content-Type", "application/json")
	rw.Write(jsonData)
}

// tradesHandler returns recent trades
func (w *WebUI) tradesHandler(rw http.ResponseWriter, r *http.Request) {
	trades := w.getRecentTrades()
	
	jsonData, err := json.Marshal(trades)
	if err != nil {
		http.Error(rw, "JSON marshaling error", http.StatusInternalServerError)
		return
	}
	
	rw.Header().Set("Content-Type", "application/json")
	rw.Write(jsonData)
}

// controlHandler handles bot control commands
func (w *WebUI) controlHandler(rw http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	
	switch action {
	case "start":
		w.startBot()
		fmt.Fprintf(rw, "Bot started")
	case "stop":
		w.stopBot()
		fmt.Fprintf(rw, "Bot stopped")
	case "get_status":
		status := w.getBotStatus()
		fmt.Fprintf(rw, status)
	default:
		http.Error(rw, "Invalid action", http.StatusBadRequest)
	}
}

// Helper methods

func (w *WebUI) getDashboardData() DashboardData {
	return DashboardData{
		TotalPnL:        w.State.RealizedPnL + w.State.UnrealizedPnL,
		TotalTrades:     w.getTotalTradesCount(),
		WinRate:         w.calculateWinRate(),
		ActivePositions: w.getState().ActivePositions,
		CurrentPrice:    w.getCurrentPrice(),
		DailyChange:     w.getDailyChange(),
		SmaValue:        w.getSMAValue(),
		RsiValue:        w.getRSIValue(),
		RecentTrades:    w.getRecentTrades(),
		OpenPositions:   w.getOpenPositions(),
	}
}

func (w *WebUI) getRecentTrades() []Trade {
	// For now returning mock data - in real implementation, this would come from a persistent store
	trades := make([]Trade, 0)
	
	// Add some example trades based on historical data
	for i := 0; i < 5; i++ {
		trades = append(trades, Trade{
			Timestamp: time.Now().Add(time.Duration(-i*10) * time.Minute).Format(time.RFC3339),
			Side:      "BUY",
			Price:     w.getCurrentPrice() - float64(i*10),
			PnL:       float64(i*5) / 1000, // Small positive P&L
		})
	}
	
	return trades
}

func (w *WebUI) getOpenPositions() []Position {
	// Placeholder implementation - would connect to position manager in real app
	positions := make([]Position, 0)
	
	// Check if there's an active position from state
	if w.State.PosSide != "" {
		positions = append(positions, Position{
			Symbol:        "BTCUSDT",
			Side:          w.State.PosSide,
			Size:          w.State.OrderQty,
			EntryPrice:    w.getCurrentPrice(), // Use current price for demo purposes
			MarkPrice:     w.getCurrentPrice(),
			TakeProfit:    0, // No direct field in state for TP
			StopLoss:      0, // No direct field in state for SL
			UnrealizedPnL: w.State.UnrealizedPnL,
		})
	}
	
	return positions
}

func (w *WebUI) calculateWinRate() float64 {
	// Use actual SignalStats from state
	w.State.SignalStats.Lock()
	defer w.State.SignalStats.Unlock()
	
	if w.State.SignalStats.Total == 0 {
		return 0
	}
	return float64(w.State.SignalStats.Correct) / float64(w.State.SignalStats.Total) * 100
}

func (w *WebUI) getTotalTradesCount() int {
	// Access the SignalStats counter which tracks trades
	w.State.SignalStats.Lock()
	defer w.State.SignalStats.Unlock()
	
	return w.State.SignalStats.Total
}



func (w *WebUI) getCurrentPrice() float64 {
	if len(w.State.Closes) > 0 {
		return w.State.Closes[len(w.State.Closes)-1]
	}
	return 0
}

func (w *WebUI) getDailyChange() float64 {
	if len(w.State.Closes) < 2 {
		return 0
	}
	
	current := w.State.Closes[len(w.State.Closes)-1]
	// For simplicity, comparing with the oldest value in history
	oldest := w.State.Closes[0]
	
	if oldest == 0 {
		return 0
	}
	
	return ((current - oldest) / oldest) * 100
}

func (w *WebUI) getSMAValue() float64 {
	// Would calculate actual SMA value - using a default length of 20 for now
	smaLen := 20
	if w.State.SmaLen > 0 {
		smaLen = w.State.SmaLen
	}
	
	if len(w.State.Closes) < smaLen {
		return w.getCurrentPrice()
	}
	
	// Calculate simple moving average
	sum := 0.0
	for i := len(w.State.Closes) - smaLen; i < len(w.State.Closes); i++ {
		sum += w.State.Closes[i]
	}
	
	return sum / float64(smaLen)
}

func (w *WebUI) getRSIValue() float64 {
	// Would calculate actual RSI value - returning mock value for now
	return 45.6 // Placeholder
}

func (w *WebUI) getState() struct{ ActivePositions int } {
	// Placeholder - would interface with actual position manager
	active := 0
	if w.State.PosSide != "" {
		active = 1
	}
	return struct{ ActivePositions int }{ActivePositions: active}
}

func (w *WebUI) startBot() {
	// In real implementation, would interact with bot controller
	log.Println("Bot started via WebUI")
}

func (w *WebUI) stopBot() {
	// In real implementation, would interact with bot controller
	log.Println("Bot stopped via WebUI")
}

func (w *WebUI) getBotStatus() string {
	// In real implementation, would get actual status
	return "running"
}

func (w *WebUI) handleBotControl(data interface{}) {
	// Handle bot control commands from the UI
	log.Printf("Bot control command received: %v", data)
}

func (w *WebUI) handleClosePosition(data interface{}) {
	// Handle close position commands from the UI
}


func (w *WebUI) handleUpdateSettings(data interface{}) {
	// Handle settings update commands from the UI
	log.Printf("Settings update received: %v", data)
}

// BroadcastUpdate sends an update to all connected WebSocket clients
func (w *WebUI) BroadcastUpdate(msgType string, data interface{}) {
	msg := Message{
		Type: msgType,
		Data: data,
	}

	// Send to broadcast channel
	select {
	case w.broadcast <- msg:
	default:
		// Channel is full, skip this update
		log.Println("Broadcast channel is full, skipping update")
	}
}

// GetDashboardData returns current dashboard data - can be called from external components
func (w *WebUI) GetDashboardData() DashboardData {
	return w.getDashboardData()
}
