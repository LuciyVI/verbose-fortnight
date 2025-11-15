package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"go-trader/config"
	"go-trader/types"
	"go-trader/logger"
	"go-trader/interfaces"
)

// APIClient handles all API interactions
type APIClient struct {
	config *config.Config
	logger *logger.Logger
}

var _ interfaces.APIClient = (*APIClient)(nil)

// NewAPIClient creates a new API client
func NewAPIClient(cfg *config.Config, log *logger.Logger) *APIClient {
	return &APIClient{
		config: cfg,
		logger: log,
	}
}

// SignREST signs a REST request
func (c *APIClient) SignREST(secret, timestamp, apiKey, recvWindow, payload string) string {
	base := timestamp + apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignWS signs a WebSocket request
func (c *APIClient) SignWS(secret string, expires int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("GET/realtime" + strconv.FormatInt(expires, 10)))
	return hex.EncodeToString(mac.Sum(nil))
}

// GetInstrumentInfo gets instrument information
func (c *APIClient) GetInstrumentInfo(symbol string) (*types.InstrumentInfo, error) {
	const path = "/v5/market/instruments-info"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, err := http.NewRequest("GET", c.config.RESTHost+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-BAPI-API-KEY", c.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.config.APISecret, ts, c.config.APIKey, c.config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

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

	if err := json.Unmarshal(body, &r); err != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		return nil, fmt.Errorf("instrument info error %d: %s", r.RetCode, r.RetMsg)
	}

	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}

	it := r.Result.List[0]
	tickSize := parse(it.PriceFilter.TickSize)
	if tickSize <= 0 {
		tickSize = 0.1
		c.logger.Error("TickSize == 0 → установлено резервное значение: %.2f", tickSize)
	}

	return &types.InstrumentInfo{
		MinNotional: parse(it.LotSizeFilter.MinNotionalValue),
		MinQty:      parse(it.LotSizeFilter.MinOrderQty),
		QtyStep:     parse(it.LotSizeFilter.QtyStep),
		TickSize:    tickSize,
	}, nil
}

// GetBalance gets the account balance
func (c *APIClient) GetBalance(coin string) (float64, error) {
	q := "accountType=" + c.config.AccountType + "&coin=" + coin
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, err := http.NewRequest("GET", c.config.RESTHost+"/v5/account/wallet-balance?"+q, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-BAPI-API-KEY", c.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.config.APISecret, ts, c.config.APIKey, c.config.RecvWindow, q))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var result struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List []struct {
				TotalAvailableBalance string `json:"totalAvailableBalance"`
			} `json:"list"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &result); err != nil || result.RetCode != 0 || len(result.Result.List) == 0 {
		return 0, fmt.Errorf("wallet error %d: %s", result.RetCode, result.RetMsg)
	}
	return strconv.ParseFloat(result.Result.List[0].TotalAvailableBalance, 64)
}

// FormatQty formats quantity according to step size
func (c *APIClient) FormatQty(qty, step float64) string {
	dec := 0
	for step < 1 {
		step *= 10
		dec++
	}
	return strconv.FormatFloat(qty, 'f', dec, 64)
}

// PlaceOrderMarket places a market order
func (c *APIClient) PlaceOrderMarket(side string, qty float64, reduceOnly bool) error {
	if side == "" {
		return fmt.Errorf("invalid side: empty")
	}

	fields := map[string]interface{}{
		"side":        side,
		"qty":         qty,
		"reduceOnly":  reduceOnly,
		"symbol":      c.config.Symbol,
	}

	const path = "/v5/order/create"

	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      c.config.Symbol,
		"side":        side,
		"orderType":   "Market",
		"qty":         c.FormatQty(qty, c.config.ContractSize),
		"timeInForce": "IOC",
		"positionIdx": 0,
	}
	if reduceOnly {
		body["reduceOnly"] = true
	}

	raw, err := json.Marshal(body)
	if err != nil {
		c.logger.LogWithFields(logger.Error, fields, "Failed to marshal order request: %v", err)
		return fmt.Errorf("failed to marshal order request: %w", err)
	}

	c.logger.LogWithFields(logger.Debug, fields, "REST order body: %s", raw)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, err := http.NewRequest("POST", c.config.RESTHost+path, bytes.NewReader(raw))
	if err != nil {
		c.logger.LogWithFields(logger.Error, fields, "Failed to create request: %v", err)
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", c.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.config.APISecret, ts, c.config.APIKey, c.config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.LogWithFields(logger.Error, fields, "Failed to place market order: %v", err)
		return fmt.Errorf("failed to place market order: %w", err)
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.LogWithFields(logger.Error, fields, "Failed to read response: %v", err)
		return fmt.Errorf("failed to read response: %w", err)
	}

	c.logger.LogWithFields(logger.Debug, fields, "REST order response: %s", reply)
	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(reply, &r); err != nil {
		c.logger.LogWithFields(logger.Error, fields, "Failed to unmarshal response: %v", err)
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if r.RetCode != 0 {
		c.logger.LogWithFields(logger.Error, fields, "API error placing order: %d - %s", r.RetCode, r.RetMsg)
		return fmt.Errorf("API error placing order: %d - %s", r.RetCode, r.RetMsg)
	}

	c.logger.OrderWithFields(fields, "Market %s %.4f OK", side, qty)
	return nil
}

// UpdatePositionTradingStop updates position with take profit and stop loss
func (c *APIClient) UpdatePositionTradingStop(posSide string, takeProfit, stopLoss float64) error {
	const path = "/v5/position/trading-stop"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      c.config.Symbol,
		"positionIdx": 0,
	}
	
	if takeProfit > 0 {
		body["takeProfit"] = fmt.Sprintf("%.2f", takeProfit)
	}
	if stopLoss > 0 {
		body["stopLoss"] = fmt.Sprintf("%.2f", stopLoss)
	}
	
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	
	c.logger.Debug("Update TP/SL body: %s", raw)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, err := http.NewRequest("POST", c.config.RESTHost+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", c.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.config.APISecret, ts, c.config.APIKey, c.config.RecvWindow, string(raw)))
	
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	reply, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	
	c.logger.Debug("TP/SL update response: %s", reply)
	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(reply, &r); err != nil || r.RetCode != 0 {
		return fmt.Errorf("error updating TP/SL: %d: %s", r.RetCode, r.RetMsg)
	}
	c.logger.Order("TP/SL updated: TP=%.2f, SL=%.2f", takeProfit, stopLoss)
	return nil
}

// GetPositionList gets the current position
func (c *APIClient) GetPositionList() (*types.Position, error) {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", c.config.Symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	req, err := http.NewRequest("GET", c.config.RESTHost+path+"?"+q.Encode(), nil)
	if err != nil {
		c.logger.Error("Error creating request for position list: %v", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("X-BAPI-API-KEY", c.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.config.APISecret, ts, c.config.APIKey, c.config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.Error("Failed to fetch position list: %v", err)
		return nil, fmt.Errorf("failed to fetch position list: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Error("Failed to read response body: %v", err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var r struct {
		RetCode int `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  []struct {
			Side       string `json:"side"`
			Size       string `json:"size"`
			TakeProfit string `json:"takeProfit"`
			StopLoss   string `json:"stopLoss"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &r); err != nil {
		c.logger.Error("Failed to unmarshal response: %v", err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if r.RetCode != 0 {
		c.logger.Error("API error getting position: %d - %s", r.RetCode, r.RetMsg)
		return nil, fmt.Errorf("API error: %d - %s", r.RetCode, r.RetMsg)
	}

	if len(r.Result) == 0 {
		c.logger.Debug("No position found in response")
		return &types.Position{Exists: false}, nil
	}

	for _, res := range r.Result {
		size, err := strconv.ParseFloat(res.Size, 64)
		if err != nil {
			c.logger.Error("Failed to parse position size: %v", err)
			continue
		}
		if size > 0 {
			// пустые TP/SL → "0"
			if res.TakeProfit == "" {
				res.TakeProfit = "0"
			}
			if res.StopLoss == "" {
				res.StopLoss = "0"
			}
			tp, err := strconv.ParseFloat(res.TakeProfit, 64)
			if err != nil {
				c.logger.Error("Failed to parse take profit: %v", err)
				tp = 0
			}
			sl, err := strconv.ParseFloat(res.StopLoss, 64)
			if err != nil {
				c.logger.Error("Failed to parse stop loss: %v", err)
				sl = 0
			}
			side := normalizeSide(res.Side)
			c.logger.Debug("Найдена открытая позиция: %s %.4f | TP=%.2f | SL=%.2f", side, size, tp, sl)
			return &types.Position{
				Exists:     true,
				Side:       side,
				Size:       size,
				TakeProfit: tp,
				StopLoss:   sl,
			}, nil
		}
	}

	return &types.Position{Exists: false}, nil
}

// normalizeSide normalizes the side string to standard format
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

// NewWSConnection creates a new WebSocket connection
func (c *APIClient) NewWSConnection(url string) (*websocket.Conn, error) {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(time.Duration(c.config.PongWait) * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(time.Duration(c.config.PongWait) * time.Second))
		return nil
	})
	return conn, nil
}

// ConnectPrivateWS connects to the private WebSocket
func (c *APIClient) ConnectPrivateWS() (*websocket.Conn, error) {
	ws, err := c.NewWSConnection(c.config.WSPrivateURL)
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(5 * time.Second).UnixMilli()
	auth := map[string]interface{}{
		"op":   "auth",
		"args": []interface{}{c.config.APIKey, expires, c.SignWS(c.config.APISecret, expires)},
	}
	if err := ws.WriteJSON(auth); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // auth ack

	// Subscribe to wallet and position
	if err := ws.WriteJSON(map[string]interface{}{
		"op":   "subscribe",
		"args": []string{"wallet", "position"},
	}); err != nil {
		ws.Close()
		return nil, err
	}
	ws.ReadMessage() // sub ack
	return ws, nil
}