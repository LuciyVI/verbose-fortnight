// bollinger_strategy_bybit.go
//
// Реализация стратегии пробоя полос Боллинджера (лонг‑только) для демо‑сервера
// Bybit. Добавлена поддержка опции командной строки --debug, которая включает
// подробный вывод всех полученных/отправленных данных и промежуточных расчётов.
//
// Запуск:
//   go run bollinger_strategy_bybit.go           # обычный режим
//   go run bollinger_strategy_bybit.go --debug   # подробные логи
//
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
    "time"

    "github.com/gorilla/websocket"
)

////////////////////////////////////////////////////////////////////////////////
// === Глобальные переменные ===
////////////////////////////////////////////////////////////////////////////////
var debug bool // устанавливается флагом --debug

func dbg(format string, v ...interface{}) {
    if debug {
        log.Printf(format, v...)
    }
}

////////////////////////////////////////////////////////////////////////////////
// === Конфигурация ===
////////////////////////////////////////////////////////////////////////////////
const (
    APIKey    = "iAk6FbPXdSri6jFU1J"
    APISecret = "svqVf30XLzbaxmByb3qcMBBBUGN0NwXc2lSL"

    demoRESTHost       = "https://api-demo.bybit.com"
    demoWSPrivateURL   = "wss://stream-demo.bybit.com/v5/private"
    demoWSPublicURL    = "wss://stream.bybit.com/v5/public/linear" // публичный поток kline

    pongWait   = 70 * time.Second
    pingPeriod = 30 * time.Second
    writeWait  = 10 * time.Second

    recvWindow  = "5000"
    accountType = "UNIFIED"

    symbol     = "BTCUSDT"
    interval   = "1"   // 1‑минутные свечи
    windowSize = 20     // период SMA
    bbMult     = 2.0    // множитель σ
    orderQty   = 1.0    // количество контрактов
)

var (
    closes []float64
    inLong bool
)

////////////////////////////////////////////////////////////////////////////////
// === Индикаторы ===
////////////////////////////////////////////////////////////////////////////////
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
        diff := v - m
        sum += diff * diff
    }
    return math.Sqrt(sum / float64(len(data)))
}

////////////////////////////////////////////////////////////////////////////////
// === REST подпись (v5) ===
////////////////////////////////////////////////////////////////////////////////
func signV5(secret, timestamp, apiKey, recvWindow, payload string) string {
    s := timestamp + apiKey + recvWindow + payload
    mac := hmac.New(sha256.New, []byte(secret))
    mac.Write([]byte(s))
    return hex.EncodeToString(mac.Sum(nil))
}

////////////////////////////////////////////////////////////////////////////////
// === REST: баланс ===
////////////////////////////////////////////////////////////////////////////////
func getBalanceREST(coin string) (float64, error) {
    ts := fmt.Sprintf("%d", time.Now().UnixMilli())
    path := "/v5/account/wallet-balance"
    q := url.Values{}
    q.Set("accountType", accountType)
    q.Set("coin", coin)
    queryString := q.Encode()

    sig := signV5(APISecret, ts, APIKey, recvWindow, queryString)

    req, err := http.NewRequest("GET", demoRESTHost+path+"?"+queryString, nil)
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

////////////////////////////////////////////////////////////////////////////////
// === REST: рыночный ордер ===
////////////////////////////////////////////////////////////////////////////////
func placeOrderMarket(side string, qty float64, reduceOnly bool) error {
    path := "/v5/order/create"
    bodyMap := map[string]interface{}{
        "category":    "linear",
        "symbol":      symbol,
        "side":        side,
        "orderType":   "Market",
        "qty":         fmt.Sprintf("%.0f", qty),
        "reduceOnly":  reduceOnly,
        "timeInForce": "GTC",
    }
    bodyBytes, _ := json.Marshal(bodyMap)
    dbg("REST order body: %s", string(bodyBytes))

    ts := fmt.Sprintf("%d", time.Now().UnixMilli())
    sig := signV5(APISecret, ts, APIKey, recvWindow, string(bodyBytes))

    req, err := http.NewRequest("POST", demoRESTHost+path, bytes.NewReader(bodyBytes))
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
    body, _ := io.ReadAll(resp.Body)
    dbg("REST order response: %s", string(body))

    var r struct {
        RetCode int    `json:"retCode"`
        RetMsg  string `json:"retMsg"`
    }
    if err := json.Unmarshal(body, &r); err != nil {
        return err
    }
    if r.RetCode != 0 {
        return fmt.Errorf("order error %d: %s", r.RetCode, r.RetMsg)
    }
    log.Printf("Market order %s %.0f OK", side, qty)
    return nil
}

////////////////////////////////////////////////////////////////////////////////
// === Обработка закрытых свечей ===
////////////////////////////////////////////////////////////////////////////////
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

    dbg("Candle close %.2f | SMA %.2f Upper %.2f Lower %.2f inLong=%v", closePrice, smaVal, upper, lower, inLong)

    if !inLong && closePrice > upper {
        log.Printf("BUY signal @%.2f (upper %.2f)", closePrice, upper)
        if err := placeOrderMarket("Buy", orderQty, false); err == nil {
            inLong = true
        } else {
            log.Printf("Buy error: %v", err)
        }
    }

    if inLong && closePrice < lower {
        log.Printf("SELL signal @%.2f (lower %.2f)", closePrice, lower)
        if err := placeOrderMarket("Sell", orderQty, true); err == nil {
            inLong = false
        } else {
            log.Printf("Sell error: %v", err)
        }
    }
}

////////////////////////////////////////////////////////////////////////////////
// === Структуры для kline ===
////////////////////////////////////////////////////////////////////////////////

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

////////////////////////////////////////////////////////////////////////////////
// === MAIN ===
////////////////////////////////////////////////////////////////////////////////
func main() {
    flag.BoolVar(&debug, "debug", false, "enable debug logging")
    flag.Parse()

    if debug {
        log.Printf("Debug mode ON")
    }

    bal, err := getBalanceREST("USDT")
    if err != nil {
        log.Fatalf("Failed REST balance: %v", err)
    }
    log.Printf("Init balance (USDT): %.2f", bal)

    //------------------------ Приватный WS (баланс) ------------------------
    privConn, _, err := websocket.DefaultDialer.Dial(demoWSPrivateURL, nil)
    if err != nil {
        log.Fatalf("Private WS dial error: %v", err)
    }
    defer privConn.Close()
    privConn.SetReadDeadline(time.Now().Add(pongWait))
    privConn.SetPongHandler(func(string) error {
        privConn.SetReadDeadline(time.Now().Add(pongWait))
        return nil
    })

    expires := time.Now().Add(5 * time.Second).UnixMilli()
    auth := map[string]interface{}{"op": "auth", "args": []interface{}{APIKey, expires, signV5(APISecret, fmt.Sprintf("%d", expires), APIKey, "", "")}}
    privConn.WriteJSON(auth)
    _, msg, _ := privConn.ReadMessage()
    dbg("Private auth resp: %s", string(msg))

    privConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{"wallet"}})
    _, msg, _ = privConn.ReadMessage()
    dbg("Private sub resp: %s", string(msg))

    //------------------------ Публичный WS (kline) -------------------------
    pubConn, _, err := websocket.DefaultDialer.Dial(demoWSPublicURL, nil)
    if err != nil {
        log.Fatalf("Public WS dial error: %v", err)
    }
    defer pubConn.Close()
    pubConn.SetReadDeadline(time.Now().Add(pongWait))
    pubConn.SetPongHandler(func(string) error {
        pubConn.SetReadDeadline(time.Now().Add(pongWait))
        return nil
    })

    topic := fmt.Sprintf("kline.%s.%s", interval, symbol)
    pubConn.WriteJSON(map[string]interface{}{"op": "subscribe", "args": []string{topic}})
    _, msg, _ = pubConn.ReadMessage()
    dbg("Public sub resp: %s", string(msg))

    //------------------------ Пинг‑гороутины -------------------------------
    ticker := time.NewTicker(pingPeriod)
    defer ticker.Stop()
    go func() {
        for range ticker.C {
            privConn.WriteMessage(websocket.PingMessage, nil)
            pubConn.WriteMessage(websocket.PingMessage, nil)
        }
    }()

    //------------------------ Основной цикл --------------------------------
    for {
        _, raw, err := pubConn.ReadMessage()
        if err != nil {
            log.Printf("Public read error: %v", err)
            break
        }
        dbg("Public raw: %s", string(raw))
        var km KlineMsg
        if err := json.Unmarshal(raw, &km); err == nil && len(km.Data) > 0 {
            k := km.Data[0]
            if k.Confirm {
                onClosedCandle(k.CloseFloat())
            }
        }

        // читаем приватное WS non-blocking (только для debug/баланса)
        privConn.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
        if _, pRaw, err := privConn.ReadMessage(); err == nil {
            dbg("Private raw: %s", string(pRaw))
            var up struct {
                Topic string `json:"topic"`
                Data  []struct {
                    TotalAvailableBalance string `json:"totalAvailableBalance"`
                } `json:"data"`
            }
            if json.Unmarshal(pRaw, &up) == nil && up.Topic == "wallet" && len(up.Data) > 0 {
                bal, _ := strconv.ParseFloat(up.Data[0].TotalAvailableBalance, 64)
                log.Printf("Balance update: %.2f", bal)
            }
        }
    }
}
