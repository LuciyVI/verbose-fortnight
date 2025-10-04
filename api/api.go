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
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
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
				Side     string `json:"side"`
				Size     string `json:"size"`
				TakeProfit string `json:"takeProfit"`
				StopLoss   string `json:"stopLoss"`
				AvgPrice   string `json:"avgPrice"`
			} `json:"list"`
		} `json:"result"`
	}

	if json.Unmarshal(body, &r) != nil || r.RetCode != 0 || len(r.Result.List) == 0 {
		if c.Logger != nil {
			c.Logger.Error("Error in position list response: %d", r.RetCode)
		}
		return nil, fmt.Errorf("error fetching position list: %d", r.RetCode)
	}

	return r.Result.List, nil
}