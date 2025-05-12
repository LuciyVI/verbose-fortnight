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
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"sort"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// ========== 1. Global Variables & Utilities ================================
// ---------------------------------------------------------------------------

var (
	debug     bool
	dynamicTP bool // совместимость

	privPtr atomic.Pointer[websocket.Conn]
	pubPtr  atomic.Pointer[websocket.Conn]
)

const (
	clrReset  = "\033[0m"
	clrSig    = "\033[36m" // cyan
	clrOrd    = "\033[32m" // green
	clrErr    = "\033[31m" // red
	slPerc    = 0.01       // Процент SL от TP (1%)
	trailPerc = 0.005      // Процент трейлинг-стопа (0.5%)
	smaLen    = 20         // окно для SMA-воркера
	 
)

func dbg(format string, v ...interface{}) {
	if debug {
		log.Printf(format, v...)
	}
}

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

var orderbookReady atomic.Bool
var posSide string
var orderQty float64

// ---------------------------------------------------------------------------
// ========== 2. Static Configuration ========================================
// ---------------------------------------------------------------------------

const (
	APIKey           = "iAk6FbPXdSri6jFU1J"
	APISecret        = "svqVf30XLzbaxmByb3qcMBBBUGN0NwXc2lSL"
	demoRESTHost     = "https://api-demo.bybit.com"
	demoWSPrivateURL = "wss://stream-demo.bybit.com/v5/private"
	demoWSPublicURL  = "wss://stream.bybit.com/v5/public/linear"
	pongWait         = 70 * time.Second
	pingPeriod       = 30 * time.Second
	recvWindow       = "5000"
	accountType      = "UNIFIED"
	symbol           = "BTCUSDT"
	interval         = "1"
	windowSize       = 20
	bbMult           = 2.0
	contractSize     = 0.001
	obDepth          = 50
	tpThresholdQty   = 500.0
	tpOffset         = 0.002 // фиксированный отступ 0.2%
	slThresholdQty   = 500.0 // стенка для SL
)

// ---------------------------------------------------------------------------
// ========== 3. Strategy State =============================================
// ---------------------------------------------------------------------------

var closes []float64
var highs []float64
var lows []float64

type instrumentInfo struct {
	MinNotional float64
	MinQty      float64
	QtyStep     float64
	TickSize    float64
}

var instr instrumentInfo

var (
	obLock  sync.Mutex
	bidsMap = map[string]float64{}
	asksMap = map[string]float64{}
)

type tpJob struct {
	side       string
	qty        float64
	entryPrice float64
}

var signalStats struct {
	total, correct, falsePositive int
	sync.Mutex
}

func updateSignalStats(signalType string, profit float64) {
	signalStats.Lock()
	defer signalStats.Unlock()

	signalStats.total++
	if profit > 0 {
		signalStats.correct++
	} else {
		signalStats.falsePositive++
	}
}

var tpChan = make(chan tpJob, 8)

func drainTPQueue() {
	for {
		select {
		case <-tpChan:
		default:
			return
		}
	}
}

// ---------------------------------------------------------------------------
// ========== 4. Indicators =================================================
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

func calcATR(period int) float64 {
	if len(highs) < period || len(lows) < period || len(closes) < period {
		return 0
	}
	var trSum float64
	for i := len(closes) - period; i < len(closes); i++ {
		high := highs[i] // Удалено деление на 100
		low := lows[i]
		closePrev := closes[i]
		tr := math.Max(
			high-low,
			math.Max(
				math.Abs(high-closePrev),
				math.Abs(low-closePrev),
			),
		)
		trSum += tr
	}
	return trSum / float64(period)
}

func updatePositionTradingStop(posSide string, takeProfit, stopLoss float64) error {
	const path = "/v5/position/trading-stop"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"positionIdx": 0,
	}
	// Уберите деление на 100
	if takeProfit > 0 {
		body["takeProfit"] = fmt.Sprintf("%.2f", takeProfit)
	}
	if stopLoss > 0 {
		body["stopLoss"] = fmt.Sprintf("%.2f", stopLoss)
	}
	raw, _ := json.Marshal(body)
	dbg("Update TP/SL body: %s", raw)
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
	dbg("TP/SL update response: %s", reply)
	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
		return fmt.Errorf("error updating TP/SL: %d: %s", r.RetCode, r.RetMsg)
	}
	logOrderf("TP/SL updated: TP=%.2f, SL=%.2f", takeProfit, stopLoss)
	return nil
}

// New function to update TP/SL via position trading-stop endpoint
func updatePositionTPSL(symbol string, tp, sl float64) error {
	exists, side, _, _, _ := hasOpenPosition()
	if !exists {
		return fmt.Errorf("нет открытой позиции для обновления TP/SL")
	}

	positionIdx := 0
	if normalizeSide(side) == "SHORT" {
		positionIdx = 1
	}

	const path = "/v5/position/trading-stop"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"takeProfit":  fmt.Sprintf("%.2f", tp),
		"stopLoss":    fmt.Sprintf("%.2f", sl),
		"positionIdx": positionIdx,
		"tpslMode":    "Full",
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

	reply, _ := io.ReadAll(resp.Body)
	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
		return fmt.Errorf("ошибка обновления TP/SL: %d: %s", r.RetCode, r.RetMsg)
	}

	logOrderf("TP/SL успешно обновлен: TP=%.2f, SL=%.2f", tp, sl)
	return nil
}

// -------------------- Gaussian filter & channel ---------------------------

// Скользящий фильтр Гаусса с зеркальным «растягиванием» края,
// чтобы не выходить за пределы слайса и избежать panic.

// --------------------------- RSI & StochRSI -------------------------------
func rsi(src []float64, length int) []float64 {
	if len(src) < length+1 {
		return nil
	}
	out := make([]float64, len(src))
	var gain, loss float64
	for i := 1; i <= length; i++ {
		delta := src[i] - src[i-1]
		if delta >= 0 {
			gain += delta
		} else {
			loss -= delta
		}
	}
	avgGain := gain / float64(length)
	avgLoss := loss / float64(length)
	if avgLoss == 0 {
		out[length] = 100
	} else {
		rs := avgGain / avgLoss
		out[length] = 100 - 100/(1+rs)
	}
	for i := length + 1; i < len(src); i++ {
		delta := src[i] - src[i-1]
		if delta >= 0 {
			avgGain = (avgGain*(float64(length-1)) + delta) / float64(length)
			avgLoss = (avgLoss * (float64(length - 1))) / float64(length)
		} else {
			avgGain = (avgGain * (float64(length - 1))) / float64(length)
			avgLoss = (avgLoss*(float64(length-1)) - delta) / float64(length)
		}
		if avgLoss == 0 {
			out[i] = 100
		} else {
			rs := avgGain / avgLoss
			out[i] = 100 - 100/(1+rs)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// ========== 5. Signing Helpers =============================================
// ---------------------------------------------------------------------------

func signREST(secret, timestamp, apiKey, recvWindow, payload string) string {
	base := timestamp + apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

func signWS(secret string, expires int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// ---------------------------------------------------------------------------
// ========== 6. REST Calls =================================================
// ---------------------------------------------------------------------------

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
				PriceFilter struct {
					TickSize string `json:"tickSize"`
				} `json:"priceFilter"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		return instrumentInfo{}, fmt.Errorf("instrument info error %d: %s", r.RetCode, r.RetMsg)
	}

	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}

	it := r.Result.List[0]
	instr.TickSize = parse(it.PriceFilter.TickSize)
	if instr.TickSize <= 0 {
		instr.TickSize = 0.1
		logErrorf("TickSize == 0 → установлено резервное значение: %.2f", instr.TickSize)
	}

	return instrumentInfo{
		MinNotional: parse(it.LotSizeFilter.MinNotionalValue),
		MinQty:      parse(it.LotSizeFilter.MinOrderQty),
		QtyStep:     parse(it.LotSizeFilter.QtyStep),
		TickSize:    instr.TickSize,
	}, nil
}

func getBalanceREST(coin string) (float64, error) {
	q := "accountType=" + accountType + "&coin=" + coin
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
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

	var result struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &result) != nil || result.RetCode != 0 || len(result.Result.List) == 0 {
		return 0, fmt.Errorf("wallet error %d: %s", result.RetCode, result.RetMsg)
	}
	return strconv.ParseFloat(result.Result.List[0].TotalAvailableBalance, 64)
}

func formatQty(qty, step float64) string {
	dec := 0
	for step < 1 {
		step *= 10
		dec++
	}
	return strconv.FormatFloat(qty, 'f', dec, 64)
}

// ---------------------------------------------------------------------------
// ========== 7. Order Placement =============================================
// ---------------------------------------------------------------------------

func placeOrderMarket(side string, qty float64, reduceOnly bool) error {
	if side == "" {
		return fmt.Errorf("invalid side: empty")
	}
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
		"orderType":   "Market", // ← теперь рыночный ордер
		"qty":         formatQty(qty, instr.QtyStep),
		"timeInForce": "IOC",
		"positionIdx": 0,
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
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
		return fmt.Errorf("error placing market order: %d: %s", r.RetCode, r.RetMsg)
	}
	logOrderf("Market %s %.4f OK", side, qty)
	return nil
}

func placeStopLoss(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":      "linear",
		"symbol":        symbol,
		"side":          side,
		"orderType":     "Market", // или "Limit" – зависит от твоей стратегии
		"qty":           formatQty(qty, instr.QtyStep),
		"stopLossPrice": fmt.Sprintf("%.2f", price),
		"timeInForce":   "GTC",
		"reduceOnly":    true,
		"positionIdx":   0,
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

	logOrderf("SL set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}
func placeTakeProfitOrder(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
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

func placeStopLossOrder(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"side":        side,
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

	logOrderf("SL set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}

// ---------------------------------------------------------------------------
// ========== 8. Take Profit Methods =========================================
// ---------------------------------------------------------------------------

func max(a, b, c float64) float64 {
	return math.Max(math.Max(a, b), c)
}

func min(a, b, c float64) float64 {
	return math.Min(math.Min(a, b), c)
}



func getLastAskPrice() float64 {
	obLock.Lock()
	defer obLock.Unlock()
	var max float64
	for ps := range asksMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p > max {
			max = p
		}
	}
	return max
}

func getLastBidPrice() float64 {
	obLock.Lock()
	defer obLock.Unlock()
	var min float64
	for ps := range bidsMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p < min || min == 0 {
			min = p
		}
	}
	return min
}


func getLastEntryPriceFromREST() float64 {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("GET", demoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logErrorf("Failed to fetch position list: %v", err)
		return 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r struct {
		RetCode int `json:"retCode"`
		Result  []struct {
			Side     string `json:"side"`
			Size     string `json:"size"`
			AvgPrice string `json:"avgPrice"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result) == 0 {
		return 0
	}

	for _, res := range r.Result {
		ep, _ := strconv.ParseFloat(res.AvgPrice, 64)
		if ep > 0 {
			return ep
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// ========== 10. TP Worker =================================================
// ---------------------------------------------------------------------------

func tpWorker() {
	for job := range tpChan {
		exists, side, qty, _, _ := hasOpenPosition() // Игнорируем TP/SL
		if !exists || normalizeSide(job.side) != normalizeSide(side) || job.qty != qty {
			dbg("Несовпадение стороны или количества при установке TP: %s vs %s", job.side, side)
			continue
		}
		// Рассчитываем TP тремя способами:
		tpBB := calcTakeProfitBB(job.side)
		tpATR := calcTakeProfitATR(job.side)
		tpVol := calcTakeProfitVolume(job.side, tpThresholdQty)
		dbg("[TP Debug] BB=%.2f | ATR=%.2f | Volume=%.2f", tpBB, tpATR, tpVol)
		var finalTP float64
		switch job.side {
		case "LONG":
			finalTP = max(tpBB, tpATR, tpVol)
		case "SHORT":
			finalTP = min(tpBB, tpATR, tpVol)
		default:
			logErrorf("Неизвестная сторона: %s", job.side)
			continue
		}
		if finalTP == 0 || math.IsNaN(finalTP) {
			entryPrice := job.entryPrice
			if entryPrice == 0 {
				entryPrice = getLastEntryPriceFromREST()
			}
			if entryPrice == 0 {
				logErrorf("Не могу найти цену входа — пропуск TP")
				continue
			}
			if job.side == "LONG" {
				finalTP = math.Round(entryPrice*(1+tpOffset)/instr.TickSize) * instr.TickSize
			} else {
				finalTP = math.Round(entryPrice*(1-tpOffset)/instr.TickSize) * instr.TickSize
			}
			dbg("Fallback: использую фиксированный TP %.2f (entry %.2f)", finalTP, entryPrice)
		}
		dbg("[TP] BB=%.2f | ATR=%.2f | Volume=%.2f → Final TP=%.2f",
			tpBB, tpATR, tpVol, finalTP)
		orderSide := "Sell"
		if job.side == "SHORT" {
			orderSide = "Buy"
		}
		if err := placeTakeProfitOrder(orderSide, job.qty, finalTP); err != nil {
			logErrorf("Ошибка выставления TP: %v", err)
		}
	}
}

// ---------------------------------------------------------------------------
// ========== NEW: пересчёт TP/SL по повторному сигналу ======================
// ---------------------------------------------------------------------------

func adjustTPSL(closePrice float64) {
	exists, side, _, curTP, _ := hasOpenPosition()
	if !exists || normalizeSide(side) != "LONG" {
		return
	}
	entry := getLastEntryPriceFromREST()
	if entry == 0 {
		return
	}
	atr := calcATR(14)
	newTP := closePrice + atr*1.5
	if newTP <= curTP {
		return
	}
	newSL := entry + 0.5*(newTP-entry)
	if err := updatePositionTPSL(symbol, newTP, newSL); err != nil {
		logErrorf("Ошибка обновления TP/SL: %v", err)
	} else {
		logOrderf("TP/SL обновлен ▶ TP %.2f  SL %.2f", newTP, newSL)
	}
}

func adjustTPSLForShort(closePrice float64) {
	exists, side, _, curTP, _ := hasOpenPosition()
	if !exists || side != "SHORT" {
		return
	}
	entry := getLastEntryPriceFromREST()
	if entry == 0 {
		return
	}
	// TP = цена - 0.2%
	newTP := closePrice * 0.998
	if newTP >= curTP*1.001 { // Защита от небольших изменений
		return
	}
	// SL = 50% пути от entry к TP
	newSL := entry - 0.5*(entry-newTP)
	if err := updatePositionTPSL(symbol, newTP, newSL); err != nil {
		logErrorf("adjustTPSLForShort error: %v", err)
	} else {
		logOrderf("Пересчитан TP/SL ▶ TP %.2f SL %.2f", newTP, newSL)
	}
}

// ---------------------------------------------------------------------------
// ========== 11. Position Management ========================================
// ---------------------------------------------------------------------------
// Отменяет все активные ордера по символу (например, TP/SL)

func cancelCurrentOrders() {
	const path = "/v5/order/cancel-all"
	body := map[string]interface{}{
		"category": "linear",
		"symbol":   symbol,
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
		logErrorf("Failed to cancel orders: %v", err)
		return
	}
	defer resp.Body.Close()
	log.Printf("Отменены все активные ордера перед обновлением позиции")
}

// ---------------------------------------------------------------------------
// 3. Strategy State (добавьте/оставьте normalizeSide как есть)
// ---------------------------------------------------------------------------
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

// ---------------------------------------------------------------------------
// 11. Position Management → hasOpenPosition (полностью заменить)
// ---------------------------------------------------------------------------
func hasOpenPosition() (bool, string, float64, float64, float64) {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	req, _ := http.NewRequest("GET", demoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logErrorf("Failed to fetch position list: %v", err)
		return false, "", 0, 0, 0
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	// dbg("Raw position response: %s", body)

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Side       string `json:"side"`
				Size       string `json:"size"`
				TakeProfit string `json:"takeProfit"`
				StopLoss   string `json:"stopLoss"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		return false, "", 0, 0, 0
	}

	for _, res := range r.Result.List {
		size, _ := strconv.ParseFloat(res.Size, 64)
		if size > 0 {
			// пустые TP/SL → "0"
			if res.TakeProfit == "" {
				res.TakeProfit = "0"
			}
			if res.StopLoss == "" {
				res.StopLoss = "0"
			}
			tp, _ := strconv.ParseFloat(res.TakeProfit, 64)
			sl, _ := strconv.ParseFloat(res.StopLoss, 64)
			side := normalizeSide(res.Side)
			dbg("Найдена открытая позиция: %s %.4f | TP=%.2f | SL=%.2f",
				side, size, tp, sl)
			return true, side, size, tp, sl
		}
	}
	return false, "", 0, 0, 0
}

// ---------------------------------------------------------------------------
// 13. WebSocket Handlers → walletListener (полностью заменить)
// ---------------------------------------------------------------------------
func walletListener(ws *websocket.Conn, out chan<- []byte, done chan<- struct{}) {
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			ws.Close()
			done <- struct{}{}
			return
		}
		var peek struct {
			Topic string `json:"topic"`
		}
		if json.Unmarshal(msg, &peek) == nil && peek.Topic == "position" {
			dbg("Получено обновление позиции: %s", string(msg))
			var posUpdate struct {
				Data struct {
					Side string `json:"side"`
					Size string `json:"size"`
				} `json:"data"`
			}
			if json.Unmarshal(msg, &posUpdate) == nil {
				size, _ := strconv.ParseFloat(posUpdate.Data.Size, 64)
				if size > 0 {
					posSide = normalizeSide(posUpdate.Data.Side)
					orderQty = size
				} else {
					posSide = ""
					orderQty = 0
				}
			}
		}
		out <- msg
	}
}

// ---------------------------------------------------------------------------
// ========== openPosition (версия без apiClient, режим Full, фикс-SL) =======
// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// ========== openPosition (фикс-TP +0.20 %, SL −0.10 %) ======================
// ---------------------------------------------------------------------------
func openPosition(newSide string, price float64) {
	// 1. Закрываем противоположную позицию
	if exists, side, qty, _, _ := hasOpenPosition(); exists {
		side = normalizeSide(side)
		newSide = normalizeSide(newSide)
		if side != "" && side != newSide && qty > 0 {
			reduceSide := "Sell"
			if side == "SHORT" {
				reduceSide = "Buy"
			}
			if err := placeOrderMarket(reduceSide, qty, true); err != nil {
				logErrorf("Ошибка закрытия позиции %s: %v", side, err)
				return
			}
		}
	}

	// 2. Рассчитываем объем
	bal, err := getBalanceREST("USDT")
	if err != nil {
		logErrorf("Ошибка получения баланса: %v", err)
		return
	}

	step := instr.QtyStep
	qty := math.Max(instr.MinQty, step)
	if bal < price*qty {
		logErrorf("Недостаточный баланс: %.2f USDT", bal)
		return
	}

	// 3. Открываем позицию
	orderSide := "Buy"

	if normalizeSide(newSide) == "SHORT" {
		orderSide = "Sell"
	}
	if err := placeOrderMarket(orderSide, qty, false); err != nil {
		logErrorf("Ошибка открытия позиции %s: %v", newSide, err)
		return
	}

	// 4. Устанавливаем TP/SL
	entry := getLastEntryPriceFromREST()
	if entry == 0 {
		entry = price
	}

	const tpPerc = 0.005 // 0.5%
	const slPerc = 0.001 // 0.1%

	var tp, sl float64
	if newSide == "LONG" {
		tp = entry * (1 + tpPerc)
		sl = entry * (1 - slPerc)
	} else {
		tp = entry * (1 - tpPerc)
		sl = entry * (1 + slPerc)
	}

	// 5. Отправляем TP/SL
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"takeProfit":  fmt.Sprintf("%.2f", tp),
		"stopLoss":    fmt.Sprintf("%.2f", sl),
		"positionIdx": 0,
		"tpslMode":    "Full",
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("POST", demoRESTHost+"/v5/position/trading-stop", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", signREST(APISecret, ts, APIKey, recvWindow, string(raw)))

	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	} else {
		logErrorf("Ошибка установки TP/SL: %v", err)
		return
	}

	logOrderf("Позиция открыта: %s %.4f @ %.2f | TP %.2f  SL %.2f", newSide, qty, entry, tp, sl)
}

func isTPValid(entry, price float64, isLong bool) bool {
	var tp float64
	if isLong {
		tp = price * 1.005
		return tp/price-1 >= 0.005
	} else {
		tp = price * 0.995
		return 1-tp/price >= 0.005
	}
}

func syncPositionRealTime() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Актуальная позиция
		exists, side, _, tp, sl := hasOpenPosition()
		if !exists || tp == 0 {
			continue
		}
		entry := getLastEntryPriceFromREST()
		if entry == 0 {
			continue
		}

		// Текущая цена
		var price float64
		if side == "LONG" {
			price = getLastBidPrice()
		} else {
			price = getLastAskPrice()
		}
		if price == 0 {
			continue
		}

		// Прогресс в сторону TP
		var dist, prog float64
		if side == "LONG" {
			dist = tp - entry
			prog = (price - entry) / dist
		} else {
			dist = entry - tp
			prog = (entry - price) / dist
		}
		if prog <= 0 {
			continue
		}

		// Целевой SL: половина пройденного пути к TP
		targetSL := entry + prog*dist*0.5
		needMove := false

		if side == "LONG" && targetSL > sl {
			needMove = true
		} else if side == "SHORT" && targetSL < sl {
			needMove = true
		}
		if !needMove {
			continue
		}

		// Обновляем стоп-лосс
		if err := updatePositionTPSL(symbol, tp, targetSL); err != nil {
			logErrorf("Trailing SL update error: %v", err)
		} else {
			logOrderf("SL → %.2f (%.0f%% пути к TP)", targetSL, prog*100)
		}
	}
}

// ---------------------------------------------------------------------------
// ========== onClosedCandle (c выводом заполнения буфера) ===================
// ---------------------------------------------------------------------------
func onClosedCandle(closePrice float64) {
    closes = append(closes, closePrice)
    highs = append(highs, closePrice)
    lows = append(lows, closePrice)

    // Логирование для отладки
    log.Printf("Добавлена цена: %.2f, длина closes: %d", closePrice, len(closes))

    // Увеличьте maxLen, чтобы избежать обрезки данных
    maxLen := smaLen * 100 // Старое значение: smaLen * 10
    if len(closes) > maxLen {
        closes = closes[len(closes)-maxLen:]
        highs = highs[len(highs)-maxLen:]
        lows = lows[len(lows)-maxLen:]
    }
}
// ---------------------------------------------------------------------------
// ========== 13. WebSocket Handlers =========================================
// ---------------------------------------------------------------------------

func applySnapshot(bids, asks [][]string) {
	obLock.Lock()
	defer obLock.Unlock()
	bidsMap = map[string]float64{}
	asksMap = map[string]float64{}

	for _, lv := range bids {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			bidsMap[lv[0]] = size
		}
	}
	for _, lv := range asks {
		if len(lv) >= 2 {
			size, _ := strconv.ParseFloat(lv[1], 64)
			asksMap[lv[0]] = size
		}
	}
	orderbookReady.Store(true)
}

func applyDelta(bids, asks [][]string) {
	obLock.Lock()
	defer obLock.Unlock()
	for _, lv := range bids {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(bidsMap, lv[0])
		} else {
			bidsMap[lv[0]] = size
		}
	}
	for _, lv := range asks {
		if len(lv) < 2 {
			continue
		}
		size, _ := strconv.ParseFloat(lv[1], 64)
		if size == 0 {
			delete(asksMap, lv[0])
		} else {
			asksMap[lv[0]] = size
		}
	}
}

// ---------------------------------------------------------------------------
// ========== 14. Types for K-line and OB ===================================
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

type Signal struct {
	Kind       string  // "GC" или "SMA"
	ClosePrice float64 // цена закрытия свечи
	Time       time.Time
}

var (
	sigChan      = make(chan Signal, 32) // все индикаторы шлют сюда
	marketRegime string                  // "trend", "range"

)

type OrderbookMsg struct {
	Topic string        `json:"topic"`
	Type  string        `json:"type"` // snapshot | delta
	Data  OrderbookData `json:"data"`
}

type OrderbookData struct {
	B [][]string `json:"b"` // [[price,size]]
	A [][]string `json:"a"`
}

// ---------------------------------------------------------------------------
// ========== 15. WebSocket Helpers ===========================================
// ---------------------------------------------------------------------------

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
	if err := ws.WriteJSON(auth); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // auth ack

	// Подписываемся на позиции и кошелек
	if err := ws.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position"}, // ← Добавили "position"
	}); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // sub ack
	return ws, nil
}

func reconnectPublic(klineTopic, obTopic string) (*websocket.Conn, error) {
	for backoff := 1; ; backoff++ {
		time.Sleep(time.Duration(backoff*2) * time.Second)
		conn, err := newWSConn(demoWSPublicURL)
		if err != nil {
			logErrorf("Public reconnect dial: %v", err)
			continue
		}
		if err = conn.WriteJSON(map[string]interface{}{
			"op":   "subscribe",
			"args": []string{klineTopic, obTopic},
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

func checkOrderbookStrength(side string) bool {
    obLock.Lock()
    defer obLock.Unlock()

    var bidDepth, askDepth float64
    for _, size := range bidsMap {
        bidDepth += size
    }
    for _, size := range asksMap {
        askDepth += size
    }

    // Логируем объемы ордербука
    dbg("Bid Depth: %.2f, Ask Depth: %.2f", bidDepth, askDepth)

    // Адаптивные пороги в зависимости от рыночного режима
    if marketRegime == "trend" {
        if side == "LONG" && bidDepth/askDepth > 1.1 {
            dbg("Тренд: сигнал LONG подтверждён")
            return true
        } else if side == "SHORT" && askDepth/bidDepth > 1.1 {
            dbg("Тренд: сигнал SHORT подтверждён")
            return true
        }
    } else if marketRegime == "range" {
        if side == "LONG" && bidDepth/askDepth > 1.1 {
            dbg("Диапазон: сигнал LONG подтверждён")
            return true
        } else if side == "SHORT" && askDepth/bidDepth > 1.1 {
            dbg("Диапазон: сигнал SHORT подтверждён")
            return true
        }
    }
    return false
}

// Получает текущий блокрейт (награду за блок)
func getCurrentBlockReward() float64 {
	// Пример: после халвинга 2024 — 3.125 BTC
	return 3.125
}


func smaWorker() {
    const (
        smaLen     = 20
    )
	var hysteresis = 0.005 // 0.5%
    for range time.Tick(1 * time.Second) {
        closesCopy := append([]float64(nil), closes...)
        if len(closesCopy) < smaLen {
            log.Printf("Недостаточно данных для SMA (требуется %d, получено %d)", smaLen, len(closesCopy))
            continue
        }

        smaVal := sma(closesCopy)
        cls := closesCopy[len(closesCopy)-1]
        

        // Логируем ключевые значения
        dbg("SMA: %.2f, Close: %.2f, MarketRegime: %s", smaVal, cls,  marketRegime)

        // Адаптируем гистерезис в зависимости от рыночного режима
        if marketRegime == "trend" {
            hysteresis = 0.01 // Более широкий гистерезис в тренде
        } else {
            hysteresis = 0.005 // Стандартный гистерезис в диапазоне
        }
        dbg("Гистерезис: %.2f", hysteresis)

        // Условия с гистерезисом
        if cls < smaVal*(1-hysteresis) && isGoldenCross() {
            dbg("Сигнал LONG: Close < SMA*(1-hysteresis) и Golden Cross")
            sigChan <- Signal{"STRATEGY_LONG", cls, time.Now()}
        }
        if cls > smaVal*(1+hysteresis) && isGoldenCross() {
            dbg("Сигнал SHORT: Close > SMA*(1+hysteresis) и Golden Cross")
            sigChan <- Signal{"STRATEGY_SHORT", cls, time.Now()}
        }
    }
}
// Расчет TP на основе ATR
func calcTakeProfitATR(side string) float64 {
    atr := calcATR(14)
    if atr == 0 {
        atr = 90
    }

    var tp float64
    if side == "LONG" {
        tp = getLastAskPrice() + atr*1.5
    } else if side == "SHORT" {
        tp = getLastBidPrice() - atr*1.5
    }

    if instr.TickSize > 0 {
        tp = math.Round(tp/instr.TickSize) * instr.TickSize
    }
    return tp
}

func calcTakeProfitBB(side string) float64 {
    if len(closes) < windowSize {
        return 0
    }
    smaVal := sma(closes)
    stdVal := stddev(closes)
    var tp float64
    if side == "LONG" {
        tp = smaVal + bbMult*stdVal
    } else if side == "SHORT" {
        tp = smaVal - bbMult*stdVal
    }
    if instr.TickSize > 0 {
        tp = math.Round(tp/instr.TickSize) * instr.TickSize
    }
    return tp
}

// Расчет TP на основе объема
func calcTakeProfitVolume(side string, thresholdQty float64) float64 {
    obLock.Lock()
    defer obLock.Unlock()
    var arr []struct{ p, sz float64 }

    if side == "LONG" && len(asksMap) > 0 {
        for ps, sz := range asksMap {
            p, _ := strconv.ParseFloat(ps, 64)
            arr = append(arr, struct{ p, sz float64 }{p, sz})
        }
        sort.Slice(arr, func(i, j int) bool { return arr[i].p < arr[j].p })
        cum := 0.0
        for _, v := range arr {
            cum += v.sz
            if cum >= thresholdQty {
                tp := v.p * 0.9995
                if instr.TickSize > 0 {
                    tp = math.Round(tp/instr.TickSize) * instr.TickSize
                }
                return tp
            }
        }
        if len(arr) > 0 {
            tp := arr[len(arr)-1].p * 1.0005
            if instr.TickSize > 0 {
                tp = math.Round(tp/instr.TickSize) * instr.TickSize
            }
            return tp
        }
    } else if side == "SHORT" && len(bidsMap) > 0 {
        for ps, sz := range bidsMap {
            p, _ := strconv.ParseFloat(ps, 64)
            arr = append(arr, struct{ p, sz float64 }{p, sz})
        }
        sort.Slice(arr, func(i, j int) bool { return arr[i].p > arr[j].p })
        cum := 0.0
        for _, v := range arr {
            cum += v.sz
            if cum >= thresholdQty {
                tp := v.p * 0.9995
                if instr.TickSize > 0 {
                    tp = math.Round(tp/instr.TickSize) * instr.TickSize
                }
                return tp
            }
        }
        if len(arr) > 0 {
            tp := arr[0].p * 0.9995
            if instr.TickSize > 0 {
                tp = math.Round(tp/instr.TickSize) * instr.TickSize
            }
            return tp
        }
    }
    return 0
}

// Смешанный расчет TP
func calcTakeProfitVoting(side string) float64 {
    tpBB := calcTakeProfitBB(side)
    tpATR := calcTakeProfitATR(side)
    tpVol := calcTakeProfitVolume(side, tpThresholdQty)

    switch side {
    case "LONG":
        return math.Max(math.Max(tpBB, tpATR), tpVol)
    case "SHORT":
        return math.Min(math.Min(tpBB, tpATR), tpVol)
    default:
        return 0
    }
}

// ---------------------------------------------------------------------------
// 3w. Исполнитель сигналов (одна горутина), ответственная за ордера
// ---------------------------------------------------------------------------
func trader() {
    signalStrength := make(map[string]int)
    for sig := range sigChan {
        signalStrength[sig.Kind]++

        // Логируем силу сигнала
        dbg("Сила сигнала: %v", signalStrength)

        // Подтверждение от нескольких индикаторов
        if (signalStrength["SMA_LONG"] >= 2) && checkOrderbookStrength("LONG") {
            dbg("Подтверждён сигнал LONG: %d индикаторов", signalStrength["SMA_LONG"])
            handleLongSignal(sig.ClosePrice)
            resetSignalStrength(&signalStrength)
        } else if (signalStrength["SMA_SHORT"] >= 2) && checkOrderbookStrength("SHORT") {
            dbg("Подтверждён сигнал SHORT: %d индикаторов", signalStrength["SMA_SHORT"])
            handleShortSignal(sig.ClosePrice)
            resetSignalStrength(&signalStrength)
        }
    }
}

func resetSignalStrength(m *map[string]int) {
	for k := range *m {
		delete(*m, k)
	}
}

func maxSlice(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	max := arr[0]
	for _, v := range arr[1:] {
		if v > max {
			max = v
		}
	}
	return max
}

func minSlice(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	min := arr[0]
	for _, v := range arr[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

func logSignalStats() {
	signalStats.Lock()
	total := signalStats.total
	correct := signalStats.correct
	signalStats.Unlock()

	if total == 0 {
		log.Printf("Signal stats: Нет сигналов")
		return
	}

	accuracy := float64(correct) / float64(total) * 100
	log.Printf("Signal stats: %d/%d (%.1f%%)", correct, total, accuracy)
}


func calcEMA(src []float64, period int) float64 {
    if len(src) < period {
        dbg("Недостаточно данных для EMA (требуется %d, получено %d)", period, len(src))
        return 0
    }

    multiplier := 2.0 / float64(period+1)
    ema := src[0]
    for i := 1; i < len(src); i++ {
        ema = (src[i] * multiplier) + (ema * (1 - multiplier))
    }

    // Логируем EMA
    dbg("Рассчитанная EMA для %d периодов: %.2f", period, ema)
    return ema
}

// Расчёт MACD (Moving Average Convergence Divergence)
var macdHistory []float64

func calcMACD(src []float64) (macdLine, signalLine float64) {
    if len(src) < 26 {
        dbg("Недостаточно данных для EMA (требуется 26, получено %d)", len(src))
        return 0, 0
    }

    ema12 := calcEMA(src, 12)
    ema26 := calcEMA(src, 26)
    macdLine = ema12 - ema26

    // Логируем EMA12 и EMA26
    dbg("EMA12: %.2f, EMA26: %.2f", ema12, ema26)

    macdHistory = append(macdHistory, macdLine)
    if len(macdHistory) > 9 {
        macdHistory = macdHistory[1:]
    }

    // Рассчитываем сигнальную линию (EMA9)
    signalLine = calcEMA(macdHistory, 9)

    // Логируем MACD и сигнальную линию
    dbg("MACD: %.2f, Signal: %.2f", macdLine, signalLine)
    return macdLine, signalLine
}

var trendThreshold = 3.0 // 3% за 50 свечей

func detectMarketRegime() {
    // Лог: Проверка длины closes
    dbg("detectMarketRegime: Длина closes = %d", len(closes))

    // Проверка, достаточно ли данных для анализа
    if len(closes) < 50 {
        dbg("detectMarketRegime: Недостаточно данных для определения рыночного режима (требуется 50, получено %d)", len(closes))
        return
    }

    // Лог: Извлечение последних 50 свечей
    recent := closes[len(closes)-50:]
    dbg("detectMarketRegime: Последние 50 свечей: %v", recent)

    // Расчет максимальной и минимальной цены
    maxHigh := maxSlice(recent)
    minLow := minSlice(recent)
    dbg("detectMarketRegime: maxHigh = %.2f, minLow = %.2f", maxHigh, minLow)

    // Расчет диапазона в процентах
    rangePerc := (maxHigh - minLow) / minLow * 100
    dbg("detectMarketRegime: Рыночный диапазон: %.2f%% (max: %.2f, min: %.2f)", rangePerc, maxHigh, minLow)

    // Определение рыночного режима
    if rangePerc > trendThreshold {
        marketRegime = "trend"
        dbg("detectMarketRegime: Рыночный режим: Тренд (rangePerc = %.2f%% > %.1f%%)", rangePerc, trendThreshold)
    } else {
        marketRegime = "range"
        dbg("detectMarketRegime: Рыночный режим: Диапазон (rangePerc = %.2f%% <= %.1f%%)", rangePerc, trendThreshold)
    }
}

func handleLongSignal(closePrice float64) {
    exists, side, _, _, _ := hasOpenPosition()
    side = normalizeSide(side)
    newSide := normalizeSide("LONG")

    dbg("Проверка позиции: exists=%v, side=%s", exists, side)

    if !exists {
        dbg("Нет открытой позиции, открываем LONG")
        openPosition(newSide, closePrice)
    } else if side == newSide {
        dbg("Позиция уже LONG, обновляем TP/SL")
        adjustTPSL(closePrice)
    } else {
        dbg("Смена стороны с %s на %s", side, newSide)
        openPosition(newSide, closePrice)
    }
}

func handleShortSignal(closePrice float64) {
    exists, side, _, _, _ := hasOpenPosition()
    if !exists {
        dbg("Нет открытой позиции, открываем SHORT")
        openPosition("SHORT", closePrice)
    } else if side == "SHORT" {
        dbg("Позиция уже SHORT, обновляем TP/SL")
        adjustTPSLForShort(closePrice)
    } else {
        dbg("Смена стороны с %s на SHORT", side)
        openPosition("SHORT", closePrice)
    }
}
func isGoldenCross() bool {
    if len(closes) < 27 {
        dbg("Недостаточно данных для MACD (требуется 27, получено %d)", len(closes))
        return false
    }

    prevData := closes[len(closes)-27:]   // 27 элементов
    currData := closes[len(closes)-26:]   // 26 элементов

    prevMacd, prevSignal := calcMACD(prevData)
    currMacd, currSignal := calcMACD(currData)

    // Логируем значения MACD и сигнальной линии
    dbg("Предыдущий MACD: %.2f, Сигнал: %.2f", prevMacd, prevSignal)
    dbg("Текущий MACD: %.2f, Сигнал: %.2f", currMacd, currSignal)

    // Проверка на нулевые значения
    if prevMacd == 0 || prevSignal == 0 || currMacd == 0 || currSignal == 0 {
        dbg("Нулевые значения MACD, игнорируем сигнал")
        return false
    }

    // Логируем условие золотого кросса
    if prevMacd < prevSignal && currMacd > currSignal {
        dbg("Золотой кросс обнаружен: MACD пересек сигнал снизу вверх")
        return true
    } else {
        dbg("Золотой кросс не обнаружен")
        return false
    }
}
// ---------------------------------------------------------------------------
// ========== 16. MAIN (полная версия, запускает индикаторные горутины) ======
// ---------------------------------------------------------------------------
func main() {
	// ---------- 1. CLI ------------------------------------------------------
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.Parse()
	if debug {
		log.Printf("Debug mode ON")
	}
	debug = true
	// ---------- 2. REST-инициализация ---------------------------------------
	var err error
	instr, err = getInstrumentInfo(symbol)
	if err != nil {
		log.Fatalf("Instrument info: %v", err)
	}
	if instr.TickSize <= 0 {
		instr.TickSize = 0.1
		logErrorf("TickSize == 0 → fallback %.2f", instr.TickSize)
	}
	if bal, err := getBalanceREST("USDT"); err == nil {
		log.Printf("Init balance: %.2f USDT", bal)
	}
	log.Printf("Symbol %s — TickSize %.6f  MinQty %.6f  QtyStep %.6f",
		symbol, instr.TickSize, instr.MinQty, instr.QtyStep)

	// ---------- 3. WebSocket (private) --------------------------------------
	privConn, err := connectPrivateWS()
	if err != nil {
		log.Fatalf("Private WS dial: %v", err)
	}
	privPtr.Store(privConn)
	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone)

	// ---------- 4. WebSocket (public) ---------------------------------------
	pubConn, err := newWSConn(demoWSPublicURL)
	if err != nil {
		log.Fatalf("Public WS dial: %v", err)
	}
	pubPtr.Store(pubConn)
	klineTopic := fmt.Sprintf("kline.%s.%s", interval, symbol)
	obTopic := fmt.Sprintf("orderbook.%d.%s", obDepth, symbol)
	if err := pubConn.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{klineTopic, obTopic},
	}); err != nil {
		log.Fatalf("Public sub send: %v", err)
	}
	pubConn.ReadMessage() // sub ack

	// ---------- 5. Ping-ticker ---------------------------------------------
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

	// ---------- 6. Индикаторные горутины ------------------------------------
	
	go smaWorker() // сигнал по SMA
	go trader()    // исполняет вход / пересчёт TP-SL
	go syncPositionRealTime()



	// ---------- 8. Логирование статистики сигналов ------------------------
	go func() {
		for range time.Tick(5 * time.Minute) {
			signalStats.Lock()
			log.Printf("Signal stats: %d/%d (%.1f%%)",
				signalStats.correct, signalStats.total,
				float64(signalStats.correct)/float64(signalStats.total)*100)
			signalStats.Unlock()
		}
	}()
	go func() {
		for {
			detectMarketRegime()
			time.Sleep(5 * time.Minute) // Проверка каждые 5 минут
		}
	}()
	// ---------- 9. Главный цикл приёма данных ------------------------------
	for {
		// слить private-буфер
	drainWallet:
		for {
			select {
			case raw := <-walletChan:
				dbg("Private raw: %s", raw)
			default:
				break drainWallet
			}
		}
		// читаем public-сообщения
		_, raw, err := pubConn.ReadMessage()
		if err != nil {
			logErrorf("Public WS read: %v — reconnect…", err)
			pubConn.Close()
			if pubConn, err = reconnectPublic(klineTopic, obTopic); err != nil {
				log.Fatalf("Fatal: cannot restore public stream: %v", err)
			}
			pubPtr.Store(pubConn)
			continue
		}
		// роутер по топикам
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
		// ---------- 10. Авто-reconnect private WS --------------------------
		select {
		case <-walletDone:
			logErrorf("Private WS closed — reconnect…")
			for retry := 1; ; retry++ {
				time.Sleep(time.Duration(retry*2) * time.Second)
				if privConn, err = connectPrivateWS(); err == nil {
					privPtr.Store(privConn)
					walletDone = make(chan struct{})
					go walletListener(privConn, walletChan, walletDone)
					break
				}
				logErrorf("Private reconnect #%d: %v", retry, err)
			}
		default:
		}
	}
}
