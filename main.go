// ============================================================================
//  bollinger_strategy_bybit.go
//  --------------------------------------------------------------------------
//  АГРЕССИВНАЯ демо-стратегия для Bybit v5 (линейные бессрочные контракты):
//
//    • Торгуем по пробою полос Боллинджера (20-SMA ± 2σ) на закрытии свечи
//      – пробой ВВЕРХ → открываем/поддерживаем LONG
//      – пробой ВНИЗ  → открываем/поддерживаем SHORT
//
//    • Переворот: если позиция уже открыта в противоположную сторону –
//      СНАЧАЛА закрываем reduceOnly-ордером, затем открываем новую.
//
//    • Объём рассчитывается без плеча (1×) и строго в границах доступного
//      баланса USDT. Ограничения (`minOrderQty`, `qtyStep`) читаем из
//      энд-пойнта  GET /v5/market/instruments-info.
//
//    • Код автоматически переподключается к публичному и приватному
//      WebSocket-потокам при любом разрыве (`i/o timeout`, network drop…) —
//      стратегию не нужно перезапускать вручную.
//
//    • При запуске с флагом  --debug  добавляется цветная подсветка логов,
//      печатаются все промежуточные вычисления (накопление буфера,
//      параметры полос, тела/ответы REST-ордеров и т. д.).
//
//  --------------------------------------------------------------------------
//  Запуск:
//
//      go run bollinger_strategy_bybit.go             # обычный режим
//      go run bollinger_strategy_bybit.go --debug     # подробные логи
//  --------------------------------------------------------------------------
//
//  Обновления (27-Apr-2025):
//      ▸ из instruments-info берём  minOrderQty  +  qtyStep;
//      ▸ qty форматируется с нужной точностью  →  ордера “0.001”, а не “0”;
//      ▸ вернули ВСЕ отладочные сообщения из оригинала демо-файла;
//      ▸ добавлен автоконнект приватного и публичного WS после таймаута.
//
//  --------------------------------------------------------------------------
//  ВНИМАНИЕ: Файл предназначен исключительно для образовательных целей.
//            КЛЮЧИ указаны тестовые (demo.bybit.com).  Для реальной торговли
//            используйте собственные Key / Secret и проводите обширное
//            тестирование на демо-среде перед запуском на живых средствах.
// ============================================================================

package main

// ---------------------------------------------------------------------------
// ========== 1. Imports ======================================================
// ---------------------------------------------------------------------------

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// ========== 2. Global Variables & Utilities =================================
// ---------------------------------------------------------------------------

// --- debug-флаг и указатели на активные WebSocket-соединения ----------------
var (
	debug bool

	privPtr atomic.Pointer[websocket.Conn] // приватный  (wallet)
	pubPtr  atomic.Pointer[websocket.Conn] // публичный  (k-line)
)

// dbg — короткий printf, который печатает только в debug-режиме.
func dbg(format string, v ...interface{}) {
	if debug {
		log.Printf(format, v...)
	}
}

// Цветные логи (ANSI) — выводятся только если запущено с --debug.
const (
	clrReset = "\033[0m"
	clrSig   = "\033[36m" // сигналы  (cyan)
	clrOrd   = "\033[32m" // ордера   (green)
	clrErr   = "\033[31m" // ошибки   (red)
)

func logSignalf(f string, v ...interface{}) {
	if debug {
		log.Printf(clrSig+f+clrReset, v...)
	} else {
		log.Printf(f, v...)
	}
}
func logOrderf(f string, v ...interface{}) {
	if debug {
		log.Printf(clrOrd+f+clrReset, v...)
	} else {
		log.Printf(f, v...)
	}
}
func logErrorf(f string, v ...interface{}) {
	if debug {
		log.Printf(clrErr+f+clrReset, v...)
	} else {
		log.Printf(f, v...)
	}
}

// ---------------------------------------------------------------------------
// ========== 3. Static Configuration ========================================
// ---------------------------------------------------------------------------

const (
	APIKey    = "iAk6FbPXdSri6jFU1J" // demo keys!
	APISecret = "svqVf30XLzbaxmByb3qcMBBBUGN0NwXc2lSL"

	demoRESTHost     = "https://api-demo.bybit.com"
	demoWSPrivateURL = "wss://stream-demo.bybit.com/v5/private"
	demoWSPublicURL  = "wss://stream.bybit.com/v5/public/linear"

	pongWait   = 70 * time.Second // сервер шлёт ping раз в 30 с, ждём 70
	pingPeriod = 30 * time.Second // сами пингуем оба канала каждые 30 с

	recvWindow  = "5000"
	accountType = "UNIFIED"

	symbol       = "BTCUSDT" // тикер
	interval     = "1"       // k-line тайм-фрейм (минуты)
	windowSize   = 20        // SMA период
	bbMult       = 2.0       // σ-множитель
	contractSize = 0.001     // 1 контракт = 0.001 BTC
)

// ---------------------------------------------------------------------------
// ========== 4. Strategy State ==============================================
// ---------------------------------------------------------------------------

// Буфер последних N закрытий.
var closes []float64

// Текущая позиция: "", "LONG", "SHORT".
var posSide string

// Количество контрактов в текущей позиции.
var orderQty float64

// dynamic instrument limits
type instrumentInfo struct {
	MinNotional float64 // lotSizeFilter.minNotionalValue – для справки
	MinQty      float64 // lotSizeFilter.minOrderQty      – самый главный
	QtyStep     float64 // lotSizeFilter.qtyStep          – шаг количества
}

var instr instrumentInfo

var (
	obLock  sync.Mutex
	bidsMap = map[string]float64{} // price → size
	asksMap = map[string]float64{}
)

// настройки TP
const (
	obDepth        = 1     // orderbook.50
	tpThresholdQty = 500.0 // «стенка»: кумулятив ≥ 500 контрактов
)

// ---------------------------------------------------------------------------
// ========== 5. Indicators ==================================================
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// ========== 6. Signing Helpers =============================================
// ---------------------------------------------------------------------------

// REST sign V5 (HMAC-SHA256, concat as:  ts + apiKey + recvWindow + payload)
func signREST(secret, timestamp, apiKey, recvWindow, payload string) string {
	base := timestamp + apiKey + recvWindow + payload
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(base))
	return hex.EncodeToString(m.Sum(nil))
}

// WS sign V5 (HMAC-SHA256, concat "GET/realtime" + expires)
func signWS(secret string, expires int64) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(m.Sum(nil))
}

// ---------------------------------------------------------------------------
// ========== 7. REST Calls ===================================================
// ---------------------------------------------------------------------------

// 7.1  Instrument info  ------------------------------------------------------
func getInstrumentInfo(sym string) (instrumentInfo, error) {
	const path = "/v5/market/instruments-info"

	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", sym)

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("GET", demoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return instrumentInfo{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	dbg("Instrument raw: %s", body)

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				LotSizeFilter struct {
					MinNotionalValue string `json:"minNotionalValue"`
					MinOrderQty      string `json:"minOrderQty"`
					QtyStep          string `json:"qtyStep"`
				} `json:"lotSizeFilter"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		return instrumentInfo{}, fmt.Errorf("instrument info error %d: %s", r.RetCode, r.RetMsg)
	}

	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}
	it := r.Result.List[0].LotSizeFilter
	return instrumentInfo{
		MinNotional: parse(it.MinNotionalValue),
		MinQty:      parse(it.MinOrderQty),
		QtyStep:     parse(it.QtyStep),
	}, nil
}

// 7.2  Wallet balance (available)  ------------------------------------------
func getBalanceREST(coin string) (float64, error) {
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	q := "accountType=" + accountType + "&coin=" + coin

	req, _ := http.NewRequest("GET", demoRESTHost+"/v5/account/wallet-balance?"+q, nil)
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, q))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	dbg("REST wallet-balance raw: %s", body)

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
			} `json:"list"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		return 0, fmt.Errorf("wallet error %d: %s", r.RetCode, r.RetMsg)
	}
	return strconv.ParseFloat(r.Result.List[0].TotalAvailableBalance, 64)
}

// 7.3  qty formatter (exact decimals by qtyStep) -----------------------------
func formatQty(qty, step float64) string {
	dec := 0
	for step < 1 {
		step *= 10
		dec++
	}
	return strconv.FormatFloat(qty, 'f', dec, 64)
}

// 7.4  Market order (Buy / Sell)  -------------------------------------------
func placeOrderMarket(side string, qty float64, reduceOnly bool) error {
	const path = "/v5/order/create"

	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side, // Buy / Sell
		"orderType":   "Market",
		"qty":         formatQty(qty, instr.QtyStep),
		"timeInForce": "IOC",
		"positionIdx": 0, // one-way
	}
	if reduceOnly {
		body["reduceOnly"] = true
	}

	raw, _ := json.Marshal(body)
	dbg("REST order body: %s", raw)

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("POST", demoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)
	dbg("REST order response: %s", reply)

	var r struct {
		RetCode int
		RetMsg  string
	}
	if json.Unmarshal(reply, &r) == nil && r.RetCode != 0 {
		return fmt.Errorf("order error %d: %s", r.RetCode, r.RetMsg)
	}
	logOrderf("Market %s %s OK", side, body["qty"])
	return nil
}

// Limit reduceOnly Take-Profit
func placeTakeProfitOrder(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        map[string]string{"LONG": "Sell", "SHORT": "Buy"}[side],
		"orderType":   "Limit",
		"price":       fmt.Sprintf("%.2f", price),
		"qty":         formatQty(qty, instr.QtyStep),
		"timeInForce": "GTC",
		"reduceOnly":  true,
		"positionIdx": 0,
	}
	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("POST", demoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	logOrderf("TP set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}

// ---------------------------------------------------------------------------
// ========== 8. WebSocket Helpers ===========================================
// ---------------------------------------------------------------------------

// Dial helper with ping/pong deadline.
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

// Connect + auth private WS (wallet stream)
func connectPrivateWS() (*websocket.Conn, error) {
	ws, err := newWSConn(demoWSPrivateURL)
	if err != nil {
		return nil, err
	}

	expires := time.Now().Add(5 * time.Second).UnixMilli()
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{APIKey, expires, signWS(APISecret, expires)},
	}
	if err = ws.WriteJSON(auth); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // auth resp (ignored)

	if err = ws.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}}); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // sub resp
	return ws, nil
}

// Reconnect public WS after any error (back-off with 2s * n).
func reconnectPublic(klineTopic, obTopic string) (*websocket.Conn, error) {
	for backoff := 1; ; backoff++ {
		time.Sleep(time.Duration(backoff*2) * time.Second)

		conn, err := newWSConn(demoWSPublicURL)
		if err != nil {
			logErrorf("Public reconnect dial: %v", err)
			continue
		}

		klineTopic := fmt.Sprintf("kline.%s.%s", interval, symbol)
		obTopic := fmt.Sprintf("orderbook.%d.%s", obDepth, symbol)
		if err = conn.WriteJSON(map[string]interface{}{
			"op": "subscribe", "args": []string{klineTopic, obTopic},
		}); err != nil {
			logErrorf("Public resub send: %v", err)
			conn.Close()
			continue
		}
		if _, msg, err := conn.ReadMessage(); err == nil {
			dbg("Public resub resp: %s", string(msg))
			return conn, nil
		}
		logErrorf("Public resub read: %v", err)
		conn.Close()
	}
}

// Wallet listener goroutine
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

// --- applySnapshot: полная перезапись локального стакана
func applySnapshot(bids, asks [][]string) {
	obLock.Lock()
	defer obLock.Unlock()
	bidsMap, asksMap = map[string]float64{}, map[string]float64{}
	for _, lv := range bids {
		if len(lv) < 2 {
			continue
		}
		sz, _ := strconv.ParseFloat(lv[1], 64)
		bidsMap[lv[0]] = sz
	}
	for _, lv := range asks {
		if len(lv) < 2 {
			continue
		}
		sz, _ := strconv.ParseFloat(lv[1], 64)
		asksMap[lv[0]] = sz
	}
}

// --- applyDelta: частичные изменения
func applyDelta(bids, asks [][]string) {
	obLock.Lock()
	defer obLock.Unlock()
	for _, lv := range bids {
		if len(lv) < 2 {
			continue
		}
		sz, _ := strconv.ParseFloat(lv[1], 64)
		if sz == 0 {
			delete(bidsMap, lv[0])
		} else {
			bidsMap[lv[0]] = sz
		}
	}
	for _, lv := range asks {
		if len(lv) < 2 {
			continue
		}
		sz, _ := strconv.ParseFloat(lv[1], 64)
		if sz == 0 {
			delete(asksMap, lv[0])
		} else {
			asksMap[lv[0]] = sz
		}
	}
}

// --- calcTakeProfit: ищем первую «стенку» ликвидности
func calcTakeProfit(side string, threshold float64) float64 {
	type lvl struct{ p, sz float64 }
	obLock.Lock()
	defer obLock.Unlock()

	if side == "LONG" {
		var arr []lvl
		for ps, sz := range asksMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, lvl{p, sz})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].p < arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= threshold {
				return v.p * 0.9995
			} // чуть ниже
		}
	} else if side == "SHORT" {
		var arr []lvl
		for ps, sz := range bidsMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, lvl{p, sz})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].p > arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= threshold {
				return v.p * 1.0005
			} // чуть выше
		}
	}
	return 0 // стенка не найдена
}

// ---------------------------------------------------------------------------
// ========== 9. Position Management =========================================
// ---------------------------------------------------------------------------

func openPosition(newSide string, price float64) {
	// 1) Close opposite side first (reduceOnly).
	if posSide != "" && posSide != newSide && orderQty > 0 {
		reduce := map[string]string{"LONG": "Sell", "SHORT": "Buy"}[posSide]
		if err := placeOrderMarket(reduce, orderQty, true); err != nil {
			logErrorf("Close %s error: %v", posSide, err)
			return
		}
		posSide = ""
		orderQty = 0
	}

	// 2) If already in desired side – nothing to do.
	if posSide == newSide {
		return
	}

	// 3) Calculate qty respecting minQty & step.
	balUSDT, err := getBalanceREST("USDT")
	if err != nil {
		logErrorf("Balance error: %v", err)
		return
	}

	step := instr.QtyStep
	minQty := instr.MinQty
	if minQty < step {
		minQty = step
	}

	nominalPerCtt := price * contractSize
	maxQty := math.Floor(balUSDT/nominalPerCtt/step) * step
	if maxQty < minQty {
		logErrorf("Not enough balance %.2f USDT → maxQty %.4f < minQty %.4f (nominal %.2f)",
			balUSDT, maxQty, minQty, minQty*nominalPerCtt)
		return
	}
	qty := minQty // берём минимально разрешённый объём

	// 4) Send market order.
	side := map[string]string{"LONG": "Buy", "SHORT": "Sell"}[newSide]
	if err := placeOrderMarket(side, qty, false); err != nil {
		logErrorf("Open %s error: %v", newSide, err)
		return
	}
	posSide = newSide
	orderQty = qty
}

// ---------------------------------------------------------------------------
// ========== 10. Candle Handler =============================================
// ---------------------------------------------------------------------------

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

	dbg("Candle close %.2f | SMA %.2f Upper %.2f Lower %.2f pos=%s qty=%.4f",
		closePrice, smaVal, upper, lower, posSide, orderQty)

	if closePrice > upper {
		logSignalf("LONG signal @%.2f (upper %.2f)", closePrice, upper)
		openPosition("LONG", closePrice)
	} else if closePrice < lower {
		logSignalf("SHORT signal @%.2f (lower %.2f)", closePrice, lower)
		openPosition("SHORT", closePrice)
	}
}

// ---------------------------------------------------------------------------
// ========== 11. K-line structs =============================================
// ---------------------------------------------------------------------------

type KlineMsg struct {
	Topic string      `json:"topic"`
	Data  []KlineData `json:"data"`
}
type KlineData struct {
	Close   string `json:"close"`
	Confirm bool   `json:"confirm"`
}

func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
	return f
}

type OrderbookMsg struct {
	Topic string        `json:"topic"`
	Type  string        `json:"type"` // snapshot | delta
	Data  OrderbookData `json:"data"`
}
type OrderbookData struct {
	B [][]string `json:"b"` // bids [price,size]
	A [][]string `json:"a"` // asks
}

// ---------------------------------------------------------------------------
// ========== 12. MAIN ========================================================
// ---------------------------------------------------------------------------

func main() {
	// --- flags --------------------------------------------------------------
	flag.BoolVar(&debug, "debug", false, "enable debug logging")
	flag.Parse()
	if debug {
		log.Printf("Debug mode ON")
	}

	// --- init instrument limits --------------------------------------------
	var err error
	instr, err = getInstrumentInfo(symbol)
	if err != nil {
		log.Fatalf("Instrument info: %v", err)
	}
	log.Printf("Symbol %s — minQty %.6f, qtyStep %.6f", symbol, instr.MinQty, instr.QtyStep)

	// --- init balance -------------------------------------------------------
	if bal, err := getBalanceREST("USDT"); err == nil {
		log.Printf("Init balance: %.2f USDT", bal)
	}

	// --- private WS ---------------------------------------------------------
	privConn, err := connectPrivateWS()
	if err != nil {
		log.Fatalf("Private WS dial: %v", err)
	}
	privPtr.Store(privConn)
	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone)

	// --- public WS ----------------------------------------------------------
	pubConn, err := newWSConn(demoWSPublicURL)
	if err != nil {
		log.Fatalf("Public WS dial: %v", err)
	}
	pubPtr.Store(pubConn)

	// два топика: kline и orderbook
	klineTopic := fmt.Sprintf("kline.%s.%s", interval, symbol)
	obTopic := fmt.Sprintf("orderbook.%d.%s", obDepth, symbol)

	if err := pubConn.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{klineTopic, obTopic},
	}); err != nil {
		log.Fatalf("Public sub send: %v", err)
	}
	pubConn.ReadMessage() // sub resp

	// --- ping goroutine -----------------------------------------------------
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

	// -----------------------------------------------------------------------
	// === MAIN LOOP =========================================================
	// -----------------------------------------------------------------------
	for {
		// 1) drain wallet updates (non-blocking)
	drainWallet:
		for {
			select {
			case raw := <-walletChan:
				dbg("Private raw: %s", string(raw))
			default:
				break drainWallet
			}
		}

		// 2) read k-line (blocking)
		_, raw, err := pubConn.ReadMessage()
		if err != nil {
			logErrorf("Public WS read error: %v – reconnecting", err)
			pubConn.Close()
			if pubConn, err = reconnectPublic(klineTopic, obTopic); err == nil {
				pubPtr.Store(pubConn)
				continue
			}
			log.Fatalf("Fatal: cannot restore public WS: %v", err)
		}

		// сначала узнаём канал
		var peek struct {
			Topic string `json:"topic"`
		}
		if json.Unmarshal(raw, &peek) != nil {
			continue
		}

		switch {
		case strings.HasPrefix(peek.Topic, "kline."):
			var km KlineMsg
			if json.Unmarshal(raw, &km) == nil && len(km.Data) > 0 && km.Data[0].Confirm {
				onClosedCandle(km.Data[0].CloseFloat())
			}

		case strings.HasPrefix(peek.Topic, "orderbook."):
			var om OrderbookMsg
			if json.Unmarshal(raw, &om) != nil {
				break
			}

			switch strings.ToLower(om.Type) {
			case "snapshot":
				applySnapshot(om.Data.B, om.Data.A)
			case "delta":
				applyDelta(om.Data.B, om.Data.A)
			}

		}

		// 3) monitor private channel closure
		select {
		case <-walletDone:
			logErrorf("Private WS closed – reconnecting")
			for retry := 1; ; retry++ {
				time.Sleep(time.Duration(retry*2) * time.Second)
				if privConn, err = connectPrivateWS(); err == nil {
					privPtr.Store(privConn)
					walletDone = make(chan struct{})
					go walletListener(privConn, walletChan, walletDone)
					break
				}
				logErrorf("Private reconnect dial: %v", err)
			}
		default:
		}
	}
}
