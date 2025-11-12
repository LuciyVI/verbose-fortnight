package web_interface

import (
	"log"
	"time"

	_ "github.com/gorilla/websocket"
)

// handleBroadcasts handles broadcasting messages to all connected WebSocket clients
func (w *WebUI) handleBroadcasts() {
	for {
		msg := <-w.broadcast

		// Send the message to all connected clients
		for client := range w.clients {
			err := client.WriteJSON(msg)
			if err != nil {
				log.Printf("WebSocket write error: %v", err)

				// Remove the client that caused the error
				delete(w.clients, client)
				client.Close()
			}
		}
	}
}

// startPeriodicUpdates sends periodic dashboard updates to connected clients
func (w *WebUI) startPeriodicUpdates() {
	ticker := time.NewTicker(5 * time.Second) // Send updates every 5 seconds
	defer ticker.Stop()

	for range ticker.C {
		// Prepare dashboard data
		dashboardData := w.getDashboardData()

		// Create message
		msg := Message{
			Type: "dashboard_update",
			Data: dashboardData,
		}

		// Send to broadcast channel
		select {
		case w.broadcast <- msg:
		default:
			// Channel is full, skip this update
			log.Println("Broadcast channel is full, skipping update")
		}
	}
}

// StartPeriodicUpdates is a public method to start periodic updates
func (w *WebUI) StartPeriodicUpdates() {
	w.startPeriodicUpdates()
}