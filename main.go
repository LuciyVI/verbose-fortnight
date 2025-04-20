// bybit_balance_ws_demo.go
//
// Пример получения баланса через REST‑V5 и дальнейших обновлений по WebSocket‑V5
// (demo‑сервер Bybit).  Полностью рабочий код с корректной подписью REST‑V5.
//
// go run bybit_balance_ws_demo.go
//
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

const (
	APIKey    = "iAk6FbPXdSri6jFU1J"
	APISecret = "svqVf30XLzbaxmByb3qcMBBBUGN0NwXc2lSL"

	// demo host / ws‑endpoint (используйте testnet или prod при необходимости)
	demoRESTHost = "https://api-demo.bybit.com"
	demoWSUrl    = "wss://stream-demo.bybit.com/v5/private"

	// WebSocket ping/pong timing
	pongWait   = 70 * time.Second
	pingPeriod = 30 * time.Second
	writeWait  = 10 * time.Second

	// для REST‑V5
	recvWindow  = "5000"   // мс
	accountType = "UNIFIED" // или CONTRACT, SPOT …
)

////////////////////////////////////////////////////////////////////////////////
// REST‑V5 подпись  —  HMAC_SHA256(timestamp + api_key + recvWindow + payload)
////////////////////////////////////////////////////////////////////////////////
func signV5(secret, timestamp, apiKey, recvWindow, payload string) string {
	s := timestamp + apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

////////////////////////////////////////////////////////////////////////////////
// Получить баланс через REST‑V5
////////////////////////////////////////////////////////////////////////////////
func getBalanceREST(coin string) (float64, error) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	path := "/v5/account/wallet-balance"

	// query‑string: ОБЯЗАТЕЛЬНО accountType
	q := url.Values{}
	q.Set("accountType", accountType)
	q.Set("coin", coin)
	queryString := q.Encode()

	// подпись
	sig := signV5(APISecret, ts, APIKey, recvWindow, queryString)

	req, err := http.NewRequest("GET", demoRESTHost+path+"?"+queryString, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2") // HMAC_SHA256
	req.Header.Set("X-BAPI-SIGN", sig)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	// разбор JSON‑ответа
	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
			} `json:"list"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return 0, err
	}
	if r.RetCode != 0 {
		return 0, fmt.Errorf("REST error %d: %s", r.RetCode, r.RetMsg)
	}
	if len(r.Result.List) == 0 {
		return 0, fmt.Errorf("no balance data for %s", coin)
	}

	return strconv.ParseFloat(r.Result.List[0].TotalAvailableBalance, 64)
}

////////////////////////////////////////////////////////////////////////////////
// Подпись для приватного WebSocket‑V5  —  HMAC_SHA256("GET/realtime" + expires)
////////////////////////////////////////////////////////////////////////////////
func signWS(secret string, expires int64) string {
	payload := fmt.Sprintf("GET/realtime%d", expires)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

////////////////////////////////////////////////////////////////////////////////
// MAIN
////////////////////////////////////////////////////////////////////////////////
func main() {
	//-----------------------------------------------------------------------
	// 0) REST‑запрос баланса при старте
	//-----------------------------------------------------------------------
	bal, err := getBalanceREST("USDT")
	if err != nil {
		log.Fatalf("Failed to get REST balance: %v", err)
	}
	log.Printf("Current totalAvailableBalance (REST): %.8f", bal)

	//-----------------------------------------------------------------------
	// 1) Подключаемся к WebSocket
	//-----------------------------------------------------------------------
	conn, _, err := websocket.DefaultDialer.Dial(demoWSUrl, nil)
	if err != nil {
		log.Fatalf("WebSocket dial error: %v", err)
	}
	defer conn.Close()

	//-----------------------------------------------------------------------
	// 2) Pong‑handler
	//-----------------------------------------------------------------------
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	//-----------------------------------------------------------------------
	// 3) Аутентификация
	//-----------------------------------------------------------------------
	expires := time.Now().Add(5 * time.Second).UnixMilli()
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{APIKey, expires, signWS(APISecret, expires)},
	}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(auth); err != nil {
		log.Fatalf("Auth send error: %v", err)
	}
	// consume auth response
	if _, _, err := conn.ReadMessage(); err != nil {
		log.Fatalf("Auth response error: %v", err)
	}

	//-----------------------------------------------------------------------
	// 4) Подписываемся на кошелёк
	//-----------------------------------------------------------------------
	sub := map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}}
	conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := conn.WriteJSON(sub); err != nil {
		log.Fatalf("Subscribe error: %v", err)
	}
	// consume subscribe response
	if _, _, err := conn.ReadMessage(); err != nil {
		log.Fatalf("Subscribe response error: %v", err)
	}

	//-----------------------------------------------------------------------
	// 5) Первое сообщение «snapshot» кошелька
	//-----------------------------------------------------------------------
	_, msg, err := conn.ReadMessage()
	if err != nil {
		log.Fatalf("Read initial wallet update error: %v", err)
	}
	var initial struct {
		Data []struct {
			TotalAvailableBalance string `json:"totalAvailableBalance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(msg, &initial); err != nil {
		log.Fatalf("Parse initial wallet JSON: %v", err)
	}
	if len(initial.Data) > 0 {
		bal2, _ := strconv.ParseFloat(initial.Data[0].TotalAvailableBalance, 64)
		log.Printf("Initial totalAvailableBalance (WS): %.8f", bal2)
	}

	//-----------------------------------------------------------------------
	// 6) Пинг‑гороутина для поддержания соединения
	//-----------------------------------------------------------------------
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

	//-----------------------------------------------------------------------
	// 7) Основной цикл чтения обновлений
	//-----------------------------------------------------------------------
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Read error: %v", err)
			break
		}
		var update struct {
			Data []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
			} `json:"data"`
		}
		if err := json.Unmarshal(msg, &update); err == nil && len(update.Data) > 0 {
			bal3, _ := strconv.ParseFloat(update.Data[0].TotalAvailableBalance, 64)
			log.Printf("Wallet update — totalAvailableBalance: %.8f", bal3)
		}
	}
}
