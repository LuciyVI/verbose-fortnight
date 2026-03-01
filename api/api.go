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

	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

// RESTClient provides methods to interact with Bybit REST API
type RESTClient struct {
	Config *config.Config
	Logger logging.LoggerInterface
}

// NewRESTClient creates a new REST API client
func NewRESTClient(cfg *config.Config, logger logging.LoggerInterface) *RESTClient {
	return &RESTClient{
		Config: cfg,
		Logger: logger,
	}
}

// SignREST signs a REST request
func (c *RESTClient) SignREST(secret, timestamp, apiKey, recvWindow, payload string) string {
	base := timestamp + apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

// GetInstrumentInfo fetches instrument information
func (c *RESTClient) GetInstrumentInfo(symbol string) (models.InstrumentInfo, error) {
	const path = "/v5/market/instruments-info"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	req, _ := http.NewRequest("GET", c.Config.DemoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.Error("Failed to send GET request to exchange: %v", err)
		}
		return models.InstrumentInfo{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Log incoming response
	if c.Logger != nil {
		c.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(body))
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

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in instrument info response: %d: %s", r.RetCode, r.RetMsg)
		}
		return models.InstrumentInfo{}, fmt.Errorf("instrument info error %d: %s", r.RetCode, r.RetMsg)
	}

	parse := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v
	}

	it := r.Result.List[0]
	tickSize := parse(it.PriceFilter.TickSize)
	if tickSize <= 0 {
		tickSize = 0.1
	}

	return models.InstrumentInfo{
		MinNotional: parse(it.LotSizeFilter.MinNotionalValue),
		MinQty:      parse(it.LotSizeFilter.MinOrderQty),
		QtyStep:     parse(it.LotSizeFilter.QtyStep),
		TickSize:    tickSize,
	}, nil
}

// GetBalance fetches wallet balance
func (c *RESTClient) GetBalance(coin string) (float64, error) {
	q := "accountType=" + c.Config.AccountType + "&coin=" + coin
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: /v5/account/wallet-balance?%s", q)
	}

	req, _ := http.NewRequest("GET", c.Config.DemoRESTHost+"/v5/account/wallet-balance?"+q, nil)
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, q))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.Error("Failed to send GET request to exchange: %v", err)
		}
		return 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Log incoming response
	if c.Logger != nil {
		c.Logger.Info("Received response from exchange for /v5/account/wallet-balance: Status %d, Body: %s", resp.StatusCode, string(body))
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

	if json.Unmarshal(body, &result) != nil || result.RetCode != 0 || len(result.Result.List) == 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in wallet balance response: %d: %s", result.RetCode, result.RetMsg)
		}
		return 0, fmt.Errorf("wallet error %d: %s", result.RetCode, result.RetMsg)
	}
	return strconv.ParseFloat(result.Result.List[0].TotalAvailableBalance, 64)
}

// UpdatePositionTradingStop updates take profit and stop loss
func (c *RESTClient) UpdatePositionTradingStop(symbol string, side string, takeProfit, stopLoss float64) error {
	const path = "/v5/position/trading-stop"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      symbol,
		"positionIdx": 0,
	}
	if takeProfit > 0 {
		body["takeProfit"] = fmt.Sprintf("%.2f", takeProfit)
	}
	if stopLoss > 0 {
		body["stopLoss"] = fmt.Sprintf("%.2f", stopLoss)
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))
	}

	req, _ := http.NewRequest("POST", c.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, string(raw)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.Error("Failed to send POST request to exchange: %v", err)
		}
		return err
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	if c.Logger != nil {
		c.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))
	}

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(reply, &r) != nil {
		if c.Logger != nil {
			c.Logger.Error("Error decoding TP/SL update response")
		}
		return fmt.Errorf("error updating TP/SL: decode failed")
	}
	if r.RetCode == 34040 {
		if c.Logger != nil {
			c.Logger.Info("TP/SL update noop (retCode=34040): %s", r.RetMsg)
		}
		return nil
	}
	if r.RetCode != 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in TP/SL update response: %d: %s", r.RetCode, r.RetMsg)
		}
		return fmt.Errorf("error updating TP/SL: %d: %s", r.RetCode, r.RetMsg)
	}
	return nil
}

// GetPositionList fetches the current position list
func (c *RESTClient) GetPositionList(symbol string) ([]struct {
	Side       string `json:"side"`
	Size       string `json:"size"`
	TakeProfit string `json:"takeProfit"`
	StopLoss   string `json:"stopLoss"`
	AvgPrice   string `json:"avgPrice"`
}, error) {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	req, _ := http.NewRequest("GET", c.Config.DemoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.Error("Failed to send GET request to exchange: %v", err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	// Log incoming response
	if c.Logger != nil {
		c.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(body))
	}

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				Side       string `json:"side"`
				Size       string `json:"size"`
				TakeProfit string `json:"takeProfit"`
				StopLoss   string `json:"stopLoss"`
				AvgPrice   string `json:"avgPrice"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in position list response: %d", r.RetCode)
		}
		return nil, fmt.Errorf("error fetching position list: %d", r.RetCode)
	}

	return r.Result.List, nil
}

// OpenOrder represents an active order returned by REST.
type OpenOrder struct {
	OrderID    string
	Side       string
	Qty        float64
	LeavesQty  float64
	ReduceOnly bool
}

// ExecutionRecord represents a single execution row from REST.
type ExecutionRecord struct {
	ExecID      string
	OrderID     string
	OrderLinkID string
	Side        string
	ExecQty     float64
	ExecPrice   float64
	ExecTime    int64
	CreatedTime int64
}

func parseExecutionTimestamp(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// GetOpenOrders fetches open orders for resync.
func (c *RESTClient) GetOpenOrders(symbol string) ([]OpenOrder, error) {
	const path = "/v5/order/realtime"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	req, _ := http.NewRequest("GET", c.Config.DemoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []struct {
				OrderID    string `json:"orderId"`
				Side       string `json:"side"`
				Qty        string `json:"qty"`
				LeavesQty  string `json:"leavesQty"`
				ReduceOnly bool   `json:"reduceOnly"`
			} `json:"list"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 {
		return nil, fmt.Errorf("error fetching open orders: %d", r.RetCode)
	}

	out := make([]OpenOrder, 0, len(r.Result.List))
	for _, it := range r.Result.List {
		qty, _ := strconv.ParseFloat(it.Qty, 64)
		leaves, _ := strconv.ParseFloat(it.LeavesQty, 64)
		out = append(out, OpenOrder{
			OrderID:    it.OrderID,
			Side:       it.Side,
			Qty:        qty,
			LeavesQty:  leaves,
			ReduceOnly: it.ReduceOnly,
		})
	}
	return out, nil
}

// GetTradingStop fetches current TP/SL snapshot for the symbol.
func (c *RESTClient) GetTradingStop(symbol string) (float64, float64, error) {
	positions, err := c.GetPositionList(symbol)
	if err != nil {
		return 0, 0, err
	}
	for _, p := range positions {
		size, _ := strconv.ParseFloat(strings.TrimSpace(p.Size), 64)
		if size <= 0 {
			continue
		}
		tp, _ := strconv.ParseFloat(strings.TrimSpace(p.TakeProfit), 64)
		sl, _ := strconv.ParseFloat(strings.TrimSpace(p.StopLoss), 64)
		return tp, sl, nil
	}
	return 0, 0, nil
}

// GetExecutionsPage fetches one execution page with API cursor semantics.
func (c *RESTClient) GetExecutionsPage(symbol, cursor string, limit int) ([]ExecutionRecord, string, bool, error) {
	const path = "/v5/execution/list"
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	q.Set("limit", strconv.Itoa(limit))
	if strings.TrimSpace(cursor) != "" {
		q.Set("cursor", strings.TrimSpace(cursor))
	}
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	req, _ := http.NewRequest("GET", c.Config.DemoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			NextPageCursor string `json:"nextPageCursor"`
			List           []struct {
				ExecID         string `json:"execId"`
				OrderID        string `json:"orderId"`
				OrderLinkID    string `json:"orderLinkId"`
				OrderLinkIDAlt string `json:"orderLinkID"`
				Side           string `json:"side"`
				ExecQty        string `json:"execQty"`
				ExecPrice      string `json:"execPrice"`
				ExecTime       string `json:"execTime"`
				CreatedTime    string `json:"createdTime"`
			} `json:"list"`
		} `json:"result"`
	}
	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 {
		return nil, "", false, fmt.Errorf("error fetching executions: %d", r.RetCode)
	}

	out := make([]ExecutionRecord, 0, len(r.Result.List))
	for _, it := range r.Result.List {
		if strings.TrimSpace(it.ExecID) == "" {
			continue
		}
		qty, _ := strconv.ParseFloat(it.ExecQty, 64)
		price, _ := strconv.ParseFloat(it.ExecPrice, 64)
		orderLinkID := strings.TrimSpace(it.OrderLinkID)
		if orderLinkID == "" {
			orderLinkID = strings.TrimSpace(it.OrderLinkIDAlt)
		}
		out = append(out, ExecutionRecord{
			ExecID:      it.ExecID,
			OrderID:     it.OrderID,
			OrderLinkID: orderLinkID,
			Side:        it.Side,
			ExecQty:     qty,
			ExecPrice:   price,
			ExecTime:    parseExecutionTimestamp(it.ExecTime),
			CreatedTime: parseExecutionTimestamp(it.CreatedTime),
		})
	}
	nextCursor := strings.TrimSpace(r.Result.NextPageCursor)
	hasMore := nextCursor != ""
	return out, nextCursor, hasMore, nil
}

// GetExecutionsSince fetches recent executions from REST and optionally filters by exec id.
func (c *RESTClient) GetExecutionsSince(symbol, sinceExecID string, limit int) ([]ExecutionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	pageLimit := limit
	if pageLimit > 200 {
		pageLimit = 200
	}
	const maxPages = 20

	out := make([]ExecutionRecord, 0, limit)
	seen := make(map[string]struct{}, limit)
	cursor := ""
	foundSince := sinceExecID == ""
	pages := 0

	for pages < maxPages {
		pages++
		page, nextCursor, hasMore, err := c.GetExecutionsPage(symbol, cursor, pageLimit)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}

		stopPagination := false
		for _, rec := range page {
			if _, ok := seen[rec.ExecID]; ok {
				continue
			}
			// API list is expected newest -> oldest.
			if !foundSince && rec.ExecID == sinceExecID {
				foundSince = true
				stopPagination = true
				break
			}
			out = append(out, rec)
			seen[rec.ExecID] = struct{}{}
			if limit > 0 && len(out) >= limit {
				stopPagination = true
				break
			}
		}

		if stopPagination {
			break
		}
		if !hasMore || nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	if !foundSince && sinceExecID != "" && c.Logger != nil {
		c.Logger.Warning("Execution boundary not found (sinceExecID=%s); returning fallback deduped set size=%d", sinceExecID, len(out))
	}
	return out, nil
}
