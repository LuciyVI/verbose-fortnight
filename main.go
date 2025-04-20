package main

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "log"
    "time"

    "github.com/gorilla/websocket"
)

const (
    APIKey     = "YOU_KEY"
    APISecret  = "YOU_SECRET"
    demoWSUrl  = "wss://stream-demo.bybit.com/v5/private"

    // timing for ping/pong
    pongWait   = 70 * time.Second
    pingPeriod = 30 * time.Second
    writeWait  = 10 * time.Second
)

// signWS deja vu...
func signWS(secret string, expires int64) string {
    payload := fmt.Sprintf("GET/realtime%d", expires)
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(payload))
    return hex.EncodeToString(mac.Sum(nil))
}

func main() {
    // 1) Dial
    conn, _, err := websocket.DefaultDialer.Dial(demoWSUrl, nil)
    if err != nil {
        log.Fatalf("Dial error: %v", err)
    }
    defer conn.Close()

    // 2) Setup PongHandler to reset deadline
    conn.SetReadDeadline(time.Now().Add(pongWait))
    conn.SetPongHandler(func(appData string) error {
        conn.SetReadDeadline(time.Now().Add(pongWait))
        return nil
    })

    // 3) Auth
    expires := time.Now().Add(5 * time.Second).UnixMilli()
    auth := map[string]interface{}{
        "op":   "auth",
        "args": []interface{}{APIKey, expires, signWS(APISecret, expires)},
    }
    conn.SetWriteDeadline(time.Now().Add(writeWait))
    if err := conn.WriteJSON(auth); err != nil {
        log.Fatalf("Auth send error: %v", err)
    }
    var msg map[string]interface{}
    conn.ReadJSON(&msg)
    log.Printf("Auth response: %v", msg)

    // 4) Subscribe
    sub := map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}}
    conn.SetWriteDeadline(time.Now().Add(writeWait))
    if err := conn.WriteJSON(sub); err != nil {
        log.Fatalf("Subscribe error: %v", err)
    }
    conn.ReadJSON(&msg)
    log.Printf("Subscribe response: %v", msg)

    // 5) Start pinging in background
    ticker := time.NewTicker(pingPeriod)
    defer ticker.Stop()
    go func() {
        for range ticker.C {
            conn.SetWriteDeadline(time.Now().Add(writeWait))
            if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
                log.Printf("Ping error: %v", err)
                return
            }
        }
    }()

    // 6) Read loop
    for {
        if err := conn.ReadJSON(&msg); err != nil {
            log.Printf("Read error: %v", err)
            break
        }
        if topic, ok := msg["topic"].(string); ok && topic == "wallet" {
            log.Printf("Wallet update: %v", msg["data"])
        }
    }
}
