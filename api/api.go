package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
	"verbose-fortnight/internal/prices"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

// RESTClient provides methods to interact with Bybit REST API
type RESTClient struct {
	Config         *config.Config
	Logger         logging.LoggerInterface
	HTTPClient     *http.Client
	Context        context.Context
	Operations     *operations.Store
	OperationOwner func() operations.Owner
}

// NewRESTClient creates a new REST API client
func NewRESTClient(cfg *config.Config, logger logging.LoggerInterface) *RESTClient {
	return &RESTClient{
		Config:     cfg,
		Logger:     logger,
		HTTPClient: &http.Client{Timeout: requestTimeout(cfg)},
		Context:    context.Background(),
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

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	body, err := c.DoJSON(c.Context, "GET", path, q.Encode(), nil)
	if err != nil {
		return models.InstrumentInfo{}, err
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

	it := r.Result.List[0]
	parse := func(name, raw string, positive bool) (float64, error) {
		v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || (positive && v == 0) {
			return 0, fmt.Errorf("invalid instrument %s: %q", name, raw)
		}
		return v, nil
	}
	var info models.InstrumentInfo
	// err comes from the request above.
	if info.QtyStep, err = parse("qtyStep", it.LotSizeFilter.QtyStep, true); err != nil {
		return info, err
	}
	if info.MinQty, err = parse("minOrderQty", it.LotSizeFilter.MinOrderQty, true); err != nil {
		return info, err
	}
	if info.MinNotional, err = parse("minNotionalValue", it.LotSizeFilter.MinNotionalValue, false); err != nil {
		return info, err
	}
	if info.TickSize, err = parse("tickSize", it.PriceFilter.TickSize, true); err != nil {
		return info, err
	}
	return info, nil
}

// GetBalance fetches wallet balance
func (c *RESTClient) GetBalance(coin string) (float64, error) {
	q := "accountType=" + c.Config.AccountType + "&coin=" + coin

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: /v5/account/wallet-balance?%s", q)
	}

	body, err := c.DoJSON(c.Context, "GET", "/v5/account/wallet-balance", q, nil)
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

	if json.Unmarshal(body, &result) != nil || result.RetCode != 0 || len(result.Result.List) == 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in wallet balance response: %d: %s", result.RetCode, result.RetMsg)
		}
		return 0, fmt.Errorf("wallet error %d: %s", result.RetCode, result.RetMsg)
	}
	balance, err := strconv.ParseFloat(result.Result.List[0].TotalAvailableBalance, 64)
	if err != nil || balance < 0 || math.IsNaN(balance) || math.IsInf(balance, 0) {
		return 0, fmt.Errorf("invalid wallet balance")
	}
	return balance, nil
}

// UpdatePositionTradingStop updates take profit and stop loss
func (c *RESTClient) UpdatePositionTradingStop(symbol string, side string, takeProfit, stopLoss float64) error {
	if prices.NormalizeSide(side) == "" || takeProfit < 0 || stopLoss < 0 || (takeProfit == 0 && stopLoss == 0) {
		return fmt.Errorf("invalid stop side or levels")
	}
	instr, err := c.GetInstrumentInfo(symbol)
	if err != nil {
		return err
	}
	row, err := c.ObservePosition(c.Context, symbol, 0, side)
	if err != nil {
		return err
	}
	entry, err := strconv.ParseFloat(row.AvgPrice, 64)
	if err != nil {
		return err
	}
	body := map[string]any{"category": "linear", "symbol": row.Symbol, "positionIdx": row.PositionIdx, "tpslMode": "Full"}
	if takeProfit != 0 && stopLoss != 0 {
		levels, err := prices.Stops(side, entry, takeProfit, stopLoss, instr.TickSize, false)
		if err != nil {
			return err
		}
		body["takeProfit"], body["stopLoss"] = levels.TPText, levels.SLText
	} else {
		value, tp := stopLoss, false
		if takeProfit != 0 {
			value, tp = takeProfit, true
		}
		text, _, err := prices.OneStop(side, entry, value, instr.TickSize, tp, false)
		if err != nil {
			return err
		}
		if tp {
			body["takeProfit"] = text
		} else {
			body["stopLoss"] = text
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.DoJSON(c.Context, "POST", "/v5/position/trading-stop", "", raw)
	return err
}

type Position struct {
	Symbol      string `json:"symbol"`
	PositionIdx int    `json:"positionIdx"`
	Side        string `json:"side"`
	Size        string `json:"size"`
	TakeProfit  string `json:"takeProfit"`
	StopLoss    string `json:"stopLoss"`
	AvgPrice    string `json:"avgPrice"`
}

func (c *RESTClient) GetPositionList(symbol string) ([]Position, error) {
	return c.GetPositionListContext(c.Context, symbol)
}

func (c *RESTClient) GetPositionListContext(ctx context.Context, symbol string) ([]Position, error) {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)

	// Log outgoing request
	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	body, err := c.DoJSON(ctx, "GET", path, q.Encode(), nil)
	if err != nil {
		return nil, err
	}

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			List []Position `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || r.Result.List == nil {
		if c.Logger != nil {
			c.Logger.Error("Error in position list response: %d", r.RetCode)
		}
		return nil, fmt.Errorf("error fetching position list: %d", r.RetCode)
	}

	for i := range r.Result.List {
		p := &r.Result.List[i]
		// The endpoint is queried with an exact symbol filter. Older adapters
		// omit symbol and positionIdx=0; explicit mismatches must never be used.
		if p.Symbol == "" {
			p.Symbol = symbol
		}
		if p.Symbol != symbol || p.PositionIdx < 0 || p.PositionIdx > 2 {
			return nil, fmt.Errorf("unexpected position target %s/%d", p.Symbol, p.PositionIdx)
		}
		size, err := strconv.ParseFloat(p.Size, 64)
		if err != nil || math.IsNaN(size) || math.IsInf(size, 0) || size < 0 {
			return nil, fmt.Errorf("invalid position size")
		}
		for name, raw := range map[string]string{"takeProfit": p.TakeProfit, "stopLoss": p.StopLoss, "avgPrice": p.AvgPrice} {
			if raw == "" {
				continue
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("invalid position %s", name)
			}
		}
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
	ExecID        string
	OrderID       string
	OrderLinkID   string
	TradeID       string
	Side          string
	ExecQty       float64
	ExecPrice     float64
	ExecFee       float64
	ExecPnl       float64
	IsMaker       bool
	HasIsMaker    bool
	LastLiquidity string
	ClosedSize    float64
	ReduceOnly    bool
	CreateType    string
	StopOrderType string
	ExecTime      int64
	CreatedTime   int64
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

func (c *RESTClient) logExecutionListResponse(retCode int, page []ExecutionRecord, nextCursor string) {
	if c == nil || c.Logger == nil || c.Config == nil || !c.Config.EnableExecutionResponseLog {
		return
	}
	firstExecID := ""
	lastExecID := ""
	if len(page) > 0 {
		firstExecID = strings.TrimSpace(page[0].ExecID)
		lastExecID = strings.TrimSpace(page[len(page)-1].ExecID)
	}
	payload := map[string]any{
		"retCode":     retCode,
		"list_len":    len(page),
		"firstExecId": firstExecID,
		"lastExecId":  lastExecID,
		"nextCursor":  strings.TrimSpace(nextCursor),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		c.Logger.Warning("execution_list_response marshal_failed: %v", err)
		return
	}
	c.Logger.Info("execution_list_response %s", string(raw))
}

// GetOpenOrders fetches open orders for resync.
func (c *RESTClient) GetOpenOrders(symbol string) ([]OpenOrder, error) {
	const path = "/v5/order/realtime"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)

	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	body, err := c.DoJSON(c.Context, "GET", path, q.Encode(), nil)
	if err != nil {
		return nil, err
	}

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

	if c.Logger != nil {
		c.Logger.Info("Sending GET request to exchange: %s?%s", path, q.Encode())
	}

	body, err := c.DoJSON(c.Context, "GET", path, q.Encode(), nil)
	if err != nil {
		return nil, "", false, err
	}

	var r struct {
		RetCode int `json:"retCode"`
		Result  struct {
			NextPageCursor string `json:"nextPageCursor"`
			List           []struct {
				ExecID         string `json:"execId"`
				OrderID        string `json:"orderId"`
				OrderLinkID    string `json:"orderLinkId"`
				OrderLinkIDAlt string `json:"orderLinkID"`
				TradeID        string `json:"tradeId"`
				TradeIDAlt     string `json:"tradeID"`
				Side           string `json:"side"`
				ExecQty        string `json:"execQty"`
				ExecPrice      string `json:"execPrice"`
				ExecFee        string `json:"execFee"`
				ExecPnl        string `json:"execPnl"`
				IsMaker        *bool  `json:"isMaker"`
				LastLiquidity  string `json:"lastLiquidityInd"`
				ClosedSize     string `json:"closedSize"`
				ReduceOnly     bool   `json:"reduceOnly"`
				CreateType     string `json:"createType"`
				StopOrderType  string `json:"stopOrderType"`
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
		execFee, _ := strconv.ParseFloat(it.ExecFee, 64)
		execPnl, _ := strconv.ParseFloat(it.ExecPnl, 64)
		closedSize, _ := strconv.ParseFloat(it.ClosedSize, 64)
		orderLinkID := strings.TrimSpace(it.OrderLinkID)
		if orderLinkID == "" {
			orderLinkID = strings.TrimSpace(it.OrderLinkIDAlt)
		}
		tradeID := strings.TrimSpace(it.TradeID)
		if tradeID == "" {
			tradeID = strings.TrimSpace(it.TradeIDAlt)
		}
		hasIsMaker := it.IsMaker != nil
		isMaker := false
		if it.IsMaker != nil {
			isMaker = *it.IsMaker
		}
		out = append(out, ExecutionRecord{
			ExecID:        it.ExecID,
			OrderID:       it.OrderID,
			OrderLinkID:   orderLinkID,
			TradeID:       tradeID,
			Side:          it.Side,
			ExecQty:       qty,
			ExecPrice:     price,
			ExecFee:       execFee,
			ExecPnl:       execPnl,
			IsMaker:       isMaker,
			HasIsMaker:    hasIsMaker,
			LastLiquidity: strings.TrimSpace(it.LastLiquidity),
			ClosedSize:    closedSize,
			ReduceOnly:    it.ReduceOnly,
			CreateType:    strings.TrimSpace(it.CreateType),
			StopOrderType: strings.TrimSpace(it.StopOrderType),
			ExecTime:      parseExecutionTimestamp(it.ExecTime),
			CreatedTime:   parseExecutionTimestamp(it.CreatedTime),
		})
	}
	nextCursor := strings.TrimSpace(r.Result.NextPageCursor)
	hasMore := nextCursor != ""
	c.logExecutionListResponse(r.RetCode, out, nextCursor)
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
