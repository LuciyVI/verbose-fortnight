// bollinger_strategy_bybit.go
//
// Демо-стратегия «лонг-только по пробою верхней полосы Боллинджера»
// для Bybit v5 (линейные бессрочные).  Особенности:
//
//   - объём позиции рассчитывается без плеча (1×), строго в пределах
//     доступного баланса USDT;
//   - минимальный номинал и шаг количества берутся динамически через
//     /v5/market/instruments-info, поэтому код работает с любым тикером;
//   - в debug-режиме цветовая подсветка логов.
//
// Запуск:
//
//	go run bollinger_strategy_bybit.go            # обычный режим
//	go run bollinger_strategy_bybit.go --debug    # подробные логи
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// //////////////////////////////////////////////////////////////////////////////
// === Глобальные переменные и util ============================================
// //////////////////////////////////////////////////////////////////////////////
var (
	debug bool

	// активные WS-соединения (заменяются при реконнектах)
	privPtr atomic.Pointer[websocket.Conn]
	pubPtr  atomic.Pointer[websocket.Conn]
)

func dbg(format string, v ...interface{}) {
	if debug {
		log.Printf(format, v...)
	}
}

// === Цветные логи (работают только если debug == true) ======================
const (
	clrReset = "\033[0m"
	clrSig   = "\033[36m" // сигналы  – cyan
	clrOrd   = "\033[32m" // ордера   – green
	clrErr   = "\033[31m" // ошибки   – red
)

func logSignalf(format string, v ...interface{}) {
	if debug {
		log.Printf(clrSig+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}
func logOrderf(format string, v ...interface{}) {
	if debug {
		log.Printf(clrOrd+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}
func logErrorf(format string, v ...interface{}) {
	if debug {
		log.Printf(clrErr+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}

// //////////////////////////////////////////////////////////////////////////////
// === Конфигурация ============================================================
// //////////////////////////////////////////////////////////////////////////////
const (
	APIKey           = ""
	APISecret        = ""
	demoRESTHost     = "https://api-demo.bybit.com"
	demoWSPrivateURL = "wss://stream-demo.bybit.com/v5/private"
	demoWSPublicURL  = "wss://stream.bybit.com/v5/public/linear"

	pongWait   = 70 * time.Second
	pingPeriod = 30 * time.Second

	recvWindow  = "5000"
	accountType = "UNIFIED"

	symbol     = "BTCUSDT" // тикер
	interval   = "1"       // k-line тайм-фрейм, минуты
	windowSize = 20        // SMA период
	bbMult     = 2.0       // σ-множитель

	contractSize = 0.001 // 1 контракт = 0.001 BTC
)

// //////////////////////////////////////////////////////////////////////////////
// === Переменные стратегии ====================================================
// //////////////////////////////////////////////////////////////////////////////
var (
	closes   []float64
	inLong   bool
	orderQty float64 // объём текущей позиции (контракты)
)

// динамические параметры инструмента
type instrumentInfo struct {
	MinNotional float64 // минимальный номинал ордера (USDT)
	QtyStep     float64 // шаг количества контрактов
}

var instr instrumentInfo

// //////////////////////////////////////////////////////////////////////////////
// === Индикаторы ==============================================================
// //////////////////////////////////////////////////////////////////////////////
func sma(data []float64) float64 {
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func stddev(data []float64) float64 {
	m := sma(data)
	var sum float64
	for _, v := range data {
		d := v - m
		sum += d * d
	}
	return math.Sqrt(sum / float64(len(data)))
}

////////////////////////////////////////////////////////////////////////////////
// === Подписи =================================================================
////////////////////////////////////////////////////////////////////////////////

// REST v5
func signREST(secret, timestamp, apiKey, recvWindow, payload string) string {
	s := timestamp + apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(s))
	return hex.EncodeToString(mac.Sum(nil))
}

// WebSocket v5
func signWS(secret string, expires int64) string {
	base := "GET/realtime" + strconv.FormatInt(expires, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

// //////////////////////////////////////////////////////////////////////////////
// === REST: информация о инструменте =========================================
// //////////////////////////////////////////////////////////////////////////////
func getInstrumentInfo(sym string) (instrumentInfo, error) {
	const path = "/v5/market/instruments-info"

	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", sym)

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := signREST(APISecret, ts, APIKey, recvWindow, q.Encode())

	req, err := http.NewRequest("GET", demoRESTHost+path+"?"+q.Encode(), nil)
	if err != nil {
		return instrumentInfo{}, err
	}
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return instrumentInfo{}, err
	}
	defer resp.Body.Close()

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				MinOrderAmt   string `json:"minOrderAmt"` // в USDT
				LotSizeFilter struct {
					QtyStep string `json:"qtyStep"`
				} `json:"lotSizeFilter"`
			} `json:"list"`
		} `json:"result"`
	}
	body, _ := io.ReadAll(resp.Body)
	dbg("Instrument raw: %s", string(body))
	if err := json.Unmarshal(body, &r); err != nil {
		return instrumentInfo{}, err
	}
	if r.RetCode != 0 || len(r.Result.List) == 0 {
		return instrumentInfo{}, fmt.Errorf("instrument info error %d: %s", r.RetCode, r.RetMsg)
	}
	minAmt, _ := strconv.ParseFloat(r.Result.List[0].MinOrderAmt, 64)
	qStep, _ := strconv.ParseFloat(r.Result.List[0].LotSizeFilter.QtyStep, 64)
	return instrumentInfo{MinNotional: minAmt, QtyStep: qStep}, nil
}

// //////////////////////////////////////////////////////////////////////////////
// === REST: баланс ============================================================
// //////////////////////////////////////////////////////////////////////////////
func getBalanceREST(coin string) (float64, error) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	path := "/v5/account/wallet-balance"
	q := url.Values{}
	q.Set("accountType", accountType)
	q.Set("coin", coin)
	query := q.Encode()

	sig := signREST(APISecret, ts, APIKey, recvWindow, query)

	req, err := http.NewRequest("GET", demoRESTHost+path+"?"+query, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	dbg("REST wallet-balance raw: %s", string(body))

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

// //////////////////////////////////////////////////////////////////////////////
// === REST: рыночный ордер ====================================================
// //////////////////////////////////////////////////////////////////////////////
func placeOrderMarket(side string, qty float64, reduceOnly bool) error {
	const path = "/v5/order/create"

	bodyMap := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
		"orderType":   "Market",
		"qty":         fmt.Sprintf("%.0f", qty), // Bybit принимает строку
		"timeInForce": "IOC",
		"positionIdx": 0, // one-way
	}
	if reduceOnly {
		bodyMap["reduceOnly"] = true
	}

	bodyBytes, _ := json.Marshal(bodyMap)
	dbg("REST order body: %s", string(bodyBytes))

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := signREST(APISecret, ts, APIKey, recvWindow, string(bodyBytes))

	req, err := http.NewRequest(http.MethodPost, demoRESTHost+path, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	dbg("REST order response: %s", string(respBody))

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(respBody, &r); err != nil {
		return err
	}
	if r.RetCode != 0 {
		return fmt.Errorf("order error %d: %s", r.RetCode, r.RetMsg)
	}

	logOrderf("Market order %s %.0f OK", side, qty)
	return nil
}

// //////////////////////////////////////////////////////////////////////////////
// === WebSocket helper ========================================================
// //////////////////////////////////////////////////////////////////////////////
func newWSConn(url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	return conn, nil
}

// //////////////////////////////////////////////////////////////////////////////
// === Приватный WS: подключение + подпись =====================================
// //////////////////////////////////////////////////////////////////////////////
func connectPrivateWS() (*websocket.Conn, error) {
	ws, err := newWSConn(demoWSPrivateURL)
	if err != nil {
		return nil, err
	}

	expires := time.Now().Add(5 * time.Second).UnixMilli()
	sig := signWS(APISecret, expires)
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{APIKey, expires, sig},
	}
	if err := ws.WriteJSON(auth); err != nil {
		ws.Close()
		return nil, fmt.Errorf("auth send: %w", err)
	}
	_, msg, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("auth resp: %w", err)
	}
	dbg("Private auth resp: %s", string(msg))

	sub := map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}}
	if err := ws.WriteJSON(sub); err != nil {
		ws.Close()
		return nil, fmt.Errorf("sub send: %w", err)
	}
	_, subMsg, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return nil, fmt.Errorf("sub resp: %w", err)
	}
	dbg("Private sub resp: %s", string(subMsg))
	return ws, nil
}

// //////////////////////////////////////////////////////////////////////////////
// === Обработка приватного потока ============================================
// //////////////////////////////////////////////////////////////////////////////
func walletListener(ws *websocket.Conn, out chan<- []byte, done chan<- struct{}) {
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			ws.Close()
			done <- struct{}{}
			return
		}
		out <- msg
	}
}

// //////////////////////////////////////////////////////////////////////////////
// === Обработка закрытия свечи ===============================================
// //////////////////////////////////////////////////////////////////////////////
func onClosedCandle(closePrice float64) {
	closes = append(closes, closePrice)
	if len(closes) < windowSize {
		dbg("Buffering close %.2f (%d/%d)", closePrice, len(closes), windowSize)
		return
	}
	if len(closes) > windowSize {
		closes = closes[1:]
	}

	smaVal := sma(closes)
	stdVal := stddev(closes)
	upper := smaVal + bbMult*stdVal
	lower := smaVal - bbMult*stdVal

	dbg("Candle close %.2f | SMA %.2f Upper %.2f Lower %.2f inLong=%v",
		closePrice, smaVal, upper, lower, inLong)

	// ------------------------------------------------------------------
	// BUY-сигнал — пробой верхней полосы
	// ------------------------------------------------------------------
	if !inLong && closePrice > upper {
		logSignalf("BUY signal @%.2f (upper %.2f)", closePrice, upper)

		// 1. Баланс
		balUSDT, err := getBalanceREST("USDT")
		if err != nil {
			logErrorf("Buy aborted: balance REST error: %v", err)
			return
		}

		// 2. Минимальное и максимальное количество
		nominalPerCtt := closePrice * contractSize
		step := instr.QtyStep
		if step <= 0 {
			step = 1
		}

		minQty := math.Ceil(instr.MinNotional/nominalPerCtt/step) * step
		if minQty < step {
			minQty = step
		}

		maxQty := math.Floor(balUSDT/nominalPerCtt/step) * step

		if maxQty < minQty {
			logErrorf("Buy aborted: баланс %.2f USDT → maxQty %.0f < minQty %.0f (номинал %.2f USDT)",
				balUSDT, maxQty, minQty, instr.MinNotional)
			return
		}

		// 3. Выбираем объём (минимально возможный)
		qty := minQty

		// 4. Отправляем ордер
		if err := placeOrderMarket("Buy", qty, false); err == nil {
			inLong = true
			orderQty = qty
		} else {
			logErrorf("Buy error: %v", err)
		}
	}

	// ------------------------------------------------------------------
	// SELL-сигнал — цена опустилась ниже нижней полосы
	// ------------------------------------------------------------------
	if inLong && closePrice < lower {
		logSignalf("SELL signal @%.2f (lower %.2f)", closePrice, lower)

		if orderQty < 1 {
			logErrorf("Sell aborted: recorded orderQty < 1 (%.0f)", orderQty)
			return
		}

		if err := placeOrderMarket("Sell", orderQty, true); err == nil {
			inLong = false
			orderQty = 0
		} else {
			logErrorf("Sell error: %v", err)
		}
	}
}

// //////////////////////////////////////////////////////////////////////////////
// === Структуры для k-line ====================================================
// //////////////////////////////////////////////////////////////////////////////
type KlineMsg struct {
	Topic string      `json:"topic"`
	Data  []KlineData `json:"data"`
}

type KlineData struct {
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Open    string `json:"open"`
	Close   string `json:"close"`
	Confirm bool   `json:"confirm"`
}

func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
	return f
}

// //////////////////////////////////////////////////////////////////////////////
// === MAIN ====================================================================
// //////////////////////////////////////////////////////////////////////////////
func main() {
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.Parse()
	if debug {
		log.Printf("Debug mode ON")
	}

	// ------------------------------------------------------------
	// Инициализация: баланс + параметры инструмента
	// ------------------------------------------------------------
	bal, err := getBalanceREST("USDT")
	if err != nil {
		log.Fatalf("Failed REST balance: %v", err)
	}
	log.Printf("Init balance (USDT): %.2f", bal)

	instr, err = getInstrumentInfo(symbol)
	if err != nil {
		log.Fatalf("Instrument info error: %v", err)
	}
	log.Printf("Symbol %s — minNotional %.2f, qtyStep %.4f",
		symbol, instr.MinNotional, instr.QtyStep)

	//----------------------------------------------------------------------
	// Приватный WS (wallet)
	//----------------------------------------------------------------------
	privConn, err := connectPrivateWS()
	if err != nil {
		log.Fatalf("Private WS dial error: %v", err)
	}
	privPtr.Store(privConn)

	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone)

	//----------------------------------------------------------------------
	// Публичный WS (k-line)
	//----------------------------------------------------------------------
	pubConn, err := newWSConn(demoWSPublicURL)
	if err != nil {
		log.Fatalf("Public WS dial error: %v", err)
	}
	pubPtr.Store(pubConn)

	topic := fmt.Sprintf("kline.%s.%s", interval, symbol)
	if err := pubConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{topic}}); err != nil {
		log.Fatalf("Public subscribe error: %v", err)
	}
	if _, msg, err := pubConn.ReadMessage(); err == nil {
		dbg("Public sub resp: %s", string(msg))
	}

	//----------------------------------------------------------------------
	// Ping goroutine
	//----------------------------------------------------------------------
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	go func() {
		for range ticker.C {
			if c := privPtr.Load(); c != nil {
				c.WriteMessage(websocket.PingMessage, nil)
			}
			if c := pubPtr.Load(); c != nil {
				c.WriteMessage(websocket.PingMessage, nil)
			}
		}
	}()

	//----------------------------------------------------------------------
	// Основной цикл
	//----------------------------------------------------------------------
	for {
		// 1 — drain кошелёк (non-blocking)
	drainWallet:
		for {
			select {
			case raw := <-walletChan:
				dbg("Private raw: %s", string(raw))
				var up struct {
					Topic string `json:"topic"`
					Data  []struct {
						TotalAvailableBalance string `json:"totalAvailableBalance"`
					} `json:"data"`
				}
				if json.Unmarshal(raw, &up) == nil && up.Topic == "wallet" && len(up.Data) > 0 {
					bal, _ := strconv.ParseFloat(up.Data[0].TotalAvailableBalance, 64)
					log.Printf("Balance update: %.2f", bal)
				}
			default:
				break drainWallet
			}
		}

		// 2 — читаем k-line (blocking)
		_, raw, err := pubConn.ReadMessage()
		if err != nil {
			logErrorf("Public WS read error: %v – reconnecting", err)
			pubConn.Close()
			// попытка реконнекта
			for i := 1; ; i++ {
				time.Sleep(time.Duration(i*2) * time.Second)
				if pubConn, err = newWSConn(demoWSPublicURL); err == nil {
					pubPtr.Store(pubConn)
					topic := fmt.Sprintf("kline.%s.%s", interval, symbol)
					if err = pubConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{topic}}); err == nil {
						if _, msg, err := pubConn.ReadMessage(); err == nil {
							dbg("Public re-sub resp: %s", string(msg))
							break
						}
					}
					pubConn.Close()
				}
				logErrorf("Public reconnect failed: %v", err)
			}
			continue
		}
		dbg("Public raw: %s", string(raw))

		var km KlineMsg
		if json.Unmarshal(raw, &km) == nil && len(km.Data) > 0 && km.Data[0].Confirm {
			onClosedCandle(km.Data[0].CloseFloat())
		}

		// 3 — следим, не умер ли приватный поток
		select {
		case <-walletDone:
			logErrorf("Private WS closed – reconnecting")
			for i := 1; ; i++ {
				time.Sleep(time.Duration(i*2) * time.Second)
				if privConn, err = connectPrivateWS(); err == nil {
					privPtr.Store(privConn)
					go walletListener(privConn, walletChan, walletDone)
					break
				}
				logErrorf("Private reconnect failed: %v", err)
			}
		default:
		}
	}
}
