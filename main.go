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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// ========== 1. Global Variables & Utilities ================================
// ---------------------------------------------------------------------------

var (
	debug     bool
	dynamicTP bool              // совместимость
	tpMethod  string = "voting" // по умолчанию голосование
	slOffset         = 0.01     // ±1%
	privPtr   atomic.Pointer[websocket.Conn]
	pubPtr    atomic.Pointer[websocket.Conn]
)

const (
	clrReset = "\033[0m"
	clrSig   = "\033[36m" // cyan
	clrOrd   = "\033[32m" // green
	clrErr   = "\033[31m" // red
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
var posSide string
var orderQty float64

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
		high := highs[i]
		low := lows[i]
		closePrev := closes[i]

		// Переводим в "единицы контракта", если цена выражена в USD
		// Например, 95952 → делим на стоимость пункта (tick value), если известна
		// Либо просто делим на коэффициент контракта, например contractSize
		normalizedHigh := high * contractSize
		normalizedLow := low * contractSize
		normalizedClose := closePrev * contractSize

		tr := math.Max(
			normalizedHigh-normalizedLow,
			math.Max(
				math.Abs(normalizedHigh-normalizedClose),
				math.Abs(normalizedLow-normalizedClose),
			),
		)
		trSum += tr
	}
	return trSum / float64(period)
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

func calcTakeProfitVoting(side string) float64 {
	tpBB := calcTakeProfitBB(side)
	tpATR := calcTakeProfitATR(side)
	tpVol := calcTakeProfitVolume(side, tpThresholdQty)
	switch side {
	case "LONG":
		return max(tpBB, tpATR, tpVol)
	case "SHORT":
		return min(tpBB, tpATR, tpVol)
	default:
		return 0
	}
}

func calcStopLossVoting(side string) float64 {
	slBB := calcStopLossBB(side)
	slATR := calcStopLossATR(side)
	slVol := calcStopLossVolume(side, slThresholdQty)
	switch side {
	case "LONG":
		return min(slBB, slATR, slVol)
	case "SHORT":
		return max(slBB, slATR, slVol)
	default:
		return 0
	}
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
		tp = math.Round(tp/instr.TickSize*100) / 100
	}
	return tp
}

func calcTakeProfitVolume(side string, threshold float64) float64 {
	type lvl struct{ p, sz float64 }

	obLock.Lock()
	defer obLock.Unlock()

	if side == "LONG" && len(asksMap) > 0 {
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
				tp := v.p * 0.9995
				if instr.TickSize > 0 {
					tp = math.Round(tp/instr.TickSize*100) / 100
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[0].p * 0.9995
			if instr.TickSize > 0 {
				tp = math.Round(tp/instr.TickSize*100) / 100
			}
			return tp
		}
	} else if side == "SHORT" && len(bidsMap) > 0 {
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
				tp := v.p * 1.0005
				if instr.TickSize > 0 {
					tp = math.Round(tp/instr.TickSize*100) / 100
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[0].p * 1.0005
			if instr.TickSize > 0 {
				tp = math.Round(tp/instr.TickSize*100) / 100
			}
			return tp
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// ========== 9. Stop Loss Functions =========================================
// ---------------------------------------------------------------------------

func calcStopLossBB(side string) float64 {
	if len(closes) < windowSize {
		return 0
	}
	smaVal := sma(closes)
	stdVal := stddev(closes)
	var sl float64
	if side == "LONG" {
		sl = smaVal - bbMult*stdVal
	} else if side == "SHORT" {
		sl = smaVal + bbMult*stdVal
	}
	if instr.TickSize > 0 {
		sl = math.Round(sl/instr.TickSize*100) / 100
	}
	return sl
}

func calcStopLossATR(side string) float64 {
	atr := calcATR(14)
	if atr == 0 {
		atr = 90
	}
	var sl float64
	if side == "LONG" {
		sl = getLastBidPrice() - atr*1.5
	} else if side == "SHORT" {
		sl = getLastAskPrice() + atr*1.5
	}
	if instr.TickSize > 0 {
		sl = math.Round(sl/instr.TickSize*100) / 100
	}
	return sl
}

func calcStopLossVolume(side string, threshold float64) float64 {
	type lvl struct{ p, sz float64 }

	obLock.Lock()
	defer obLock.Unlock()

	if side == "LONG" && len(bidsMap) > 0 {
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
				sl := v.p * 0.99
				if instr.TickSize > 0 {
					sl = math.Round(sl/instr.TickSize*100) / 100
				}
				return sl
			}
		}
		if len(arr) > 0 {
			sl := arr[len(arr)-1].p * 0.99
			if instr.TickSize > 0 {
				sl = math.Round(sl/instr.TickSize*100) / 100
			}
			return sl
		}
	} else if side == "SHORT" && len(asksMap) > 0 {
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
				sl := v.p * 1.01
				if instr.TickSize > 0 {
					sl = math.Round(sl/instr.TickSize*100) / 100
				}
				return sl
			}
		}
		if len(arr) > 0 {
			sl := arr[0].p * 1.01
			if instr.TickSize > 0 {
				sl = math.Round(sl/instr.TickSize*100) / 100
			}
			return sl
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// ========== 10. getLastEntryPriceFromREST — получает цену входа из позиции =======
// ---------------------------------------------------------------------------

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
			Side       string `json:"side"`
			Size       string `json:"size"`
			EntryPrice string `json:"entryPrice"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result) == 0 {
		return 0
	}

	for _, res := range r.Result {
		ep, _ := strconv.ParseFloat(res.EntryPrice, 64)
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
		if job.side != posSide {
			dbg("Несовпадение стороны при установке TP: %s vs %s", job.side, posSide)
			continue
		}

		// Считаем все три варианта TP
		tpBB := calcTakeProfitBB(job.side)
		tpATR := calcTakeProfitATR(job.side)
		tpVol := calcTakeProfitVolume(job.side, tpThresholdQty)

		// Выбираем лучший вариант
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

		// Если всё равно не определён → fallback к фиксированному отступу
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

		if err := placeTakeProfitOrder(job.side, job.qty, finalTP); err != nil {
			logErrorf("error placing TP: %v", err)
		}
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

func openPosition(newSide string, price float64) {
	if posSide != "" && posSide != newSide && orderQty > 0 {
		var reduce string
		switch posSide {
		case "LONG":
			reduce = "Sell"
		case "SHORT":
			reduce = "Buy"
		default:
			logErrorf("Unknown position side for closing: %s", posSide)
			return
		}
		if err := placeOrderMarket(reduce, orderQty, true); err != nil {
			logErrorf("Close %s error: %v", posSide, err)
			return
		}
		posSide = ""
		orderQty = 0
	}
	if posSide == newSide {
		dbg("Обновление TP/SL для существующей позиции")
		cancelCurrentOrders()
		drainTPQueue()
		tpChan <- tpJob{
			side:       newSide,
			qty:        orderQty,
			entryPrice: price,
		}
		placeStopLoss(newSide, orderQty, price)
		return
	}
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
	maxQty := math.Floor(balUSDT/nominalPerCtt/step*100) / 100
	if maxQty < minQty {
		logErrorf("Not enough balance %.2f USDT → maxQty %.4f < minQty %.4f (nominal %.2f)",
			balUSDT, maxQty, minQty, minQty*nominalPerCtt)
		return
	}
	qty := minQty
	var side string
	switch newSide {
	case "LONG":
		side = "Buy"
	case "SHORT":
		side = "Sell"
	default:
		logErrorf("Invalid newSide value: %s", newSide)
		return
	}
	if err := placeOrderMarket(side, qty, false); err != nil {
		logErrorf("Open %s error: %v", newSide, err)
		return
	}
	for i := 0; i < 50 && !orderbookReady.Load(); i++ {
		dbg("Жду загрузки стакана (%d/50)", i+1)
		time.Sleep(100 * time.Millisecond)
	}
	posSide = newSide
	orderQty = qty
	drainTPQueue()
	job := tpJob{
		side:       newSide,
		qty:        qty,
		entryPrice: price,
	}
	tpChan <- job
	go func() {
		tp := calcTakeProfitVoting(newSide)
		sl := calcStopLossVoting(newSide)
		if tp > 0 && !math.IsNaN(tp) {
			placeTakeProfitOrder(newSide, qty, tp)
		}
		if sl > 0 && !math.IsNaN(sl) {
			placeStopLossOrder(newSide, qty, sl)
		}
	}()
}

// ---------------------------------------------------------------------------
// ========== 12. K-Line Handler =============================================
// ---------------------------------------------------------------------------

func onClosedCandle(closePrice float64) {
	closes = append(closes, closePrice)
	highs = append(highs, closePrice)
	lows = append(lows, closePrice)
	if len(closes) > windowSize {
		closes = closes[1:]
		highs = highs[1:]
		lows = lows[1:]
	}
	if len(closes) < windowSize {
		dbg("Buffering close %.2f (%d/%d)", closePrice, len(closes), windowSize)
		return
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
	if err := ws.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}}); err != nil {
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

// ---------------------------------------------------------------------------
// ========== 16. MAIN ======================================================
// ---------------------------------------------------------------------------

func main() {
	flag.BoolVar(&debug, "debug", false, "enable debug logs")
	flag.StringVar(&tpMethod, "tp-method", "voting", "метод TP: volume / atr / bb / voting")
	flag.BoolVar(&dynamicTP, "dynamic-tp", false, "устаревший флаг – поддерживается для совместимости")
	flag.Parse()

	if debug {
		log.Printf("Debug mode ON")
	}

	var err error
	instr, err = getInstrumentInfo(symbol)
	if err != nil {
		log.Fatalf("Instrument info: %v", err)
	}

	// Защита от нулевого TickSize
	if instr.TickSize <= 0 {
		instr.TickSize = 0.1
		logErrorf("TickSize == 0 → установлено резервное значение: %.2f", instr.TickSize)
	}

	log.Printf("Symbol %s — TickSize %.6f, MinQty %.6f, QtyStep %.6f", symbol, instr.TickSize, instr.MinQty, instr.QtyStep)

	if bal, err := getBalanceREST("USDT"); err == nil {
		log.Printf("Init balance: %.2f USDT", bal)
	}

	privConn, err := connectPrivateWS()
	if err != nil {
		log.Fatalf("Private WS dial: %v", err)
	}
	privPtr.Store(privConn)
	walletChan := make(chan []byte, 16)
	walletDone := make(chan struct{})
	go walletListener(privConn, walletChan, walletDone)

	pubConn, err := newWSConn(demoWSPublicURL)
	if err != nil {
		log.Fatalf("Public WS dial: %v", err)
	}
	pubPtr.Store(pubConn)
	klineTopic := fmt.Sprintf("kline.%s.%s", interval, symbol)
	obTopic := fmt.Sprintf("orderbook.%d.%s", obDepth, symbol)
	if err := pubConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{klineTopic, obTopic}}); err != nil {
		log.Fatalf("Public sub send: %v", err)
	}
	pubConn.ReadMessage() // sub ack

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

	go tpWorker()

	for {
	drainWallet:
		for {
			select {
			case raw := <-walletChan:
				dbg("Private raw: %s", raw)
			default:
				break drainWallet
			}
		}

		_, raw, err := pubConn.ReadMessage()
		if err != nil {
			logErrorf("Public WS read error: %v – reconnecting", err)
			pubConn.Close()
			pubConn, err = reconnectPublic(klineTopic, obTopic)
			if err != nil {
				log.Fatalf("Fatal: cannot restore public stream: %v", err)
			}
			pubPtr.Store(pubConn)
			continue
		}

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

		// Переподключаем private WS при потере соединения
		select {
		case <-walletDone:
			logErrorf("Private WS closed – reconnecting...")
			for retry := 1; ; retry++ {
				time.Sleep(time.Duration(retry*2) * time.Second)
				if privConn, err = connectPrivateWS(); err == nil {
					privPtr.Store(privConn)
					walletDone = make(chan struct{})
					go walletListener(privConn, walletChan, walletDone)
					break
				}
				logErrorf("Private reconnect attempt #%d: %v", retry, err)
			}
		default:
		}
	}
}
