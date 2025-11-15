package position

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go-trader/config"
	"go-trader/types"
	"go-trader/logger"
	"go-trader/api"
	"go-trader/interfaces"
)

// Manager handles position management
type Manager struct {
	config   *config.Config
	logger   *logger.Logger
	api      *api.APIClient
	tpChan   chan types.TPJob
}

var _ interfaces.PositionManager = (*Manager)(nil)

// NewManager creates a new position manager
func NewManager(cfg *config.Config, log *logger.Logger, api *api.APIClient) *Manager {
	return &Manager{
		config: cfg,
		logger: log,
		api:    api,
		tpChan: make(chan types.TPJob, 8),
	}
}

// GetLastEntryPriceFromREST gets the last entry price from REST API
func (m *Manager) GetLastEntryPriceFromREST() (float64, error) {
	const path = "/v5/position/list"
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", m.config.Symbol)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	req, err := http.NewRequest("GET", m.config.RESTHost+path+"?"+q.Encode(), nil)
	if err != nil {
		m.logger.Error("Error creating request for position list: %v", err)
		return 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-BAPI-API-KEY", m.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", m.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", m.api.SignREST(m.config.APISecret, ts, m.config.APIKey, m.config.RecvWindow, q.Encode()))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.logger.Error("Failed to fetch position list: %v", err)
		return 0, fmt.Errorf("failed to fetch position list: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		m.logger.Error("Failed to read response body: %v", err)
		return 0, fmt.Errorf("failed to read response: %w", err)
	}

	var r struct {
		RetCode int `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  []struct {
			Side     string `json:"side"`
			Size     string `json:"size"`
			AvgPrice string `json:"avgPrice"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &r); err != nil {
		m.logger.Error("Failed to unmarshal response: %v", err)
		return 0, fmt.Errorf("failed to parse response: %w", err)
	}

	if r.RetCode != 0 {
		m.logger.Error("API error getting position: %d - %s", r.RetCode, r.RetMsg)
		return 0, fmt.Errorf("API error: %d - %s", r.RetCode, r.RetMsg)
	}

	if len(r.Result) == 0 {
		m.logger.Debug("No position found in response")
		return 0, fmt.Errorf("no position found")
	}

	for _, res := range r.Result {
		ep, err := strconv.ParseFloat(res.AvgPrice, 64)
		if err != nil {
			m.logger.Error("Failed to parse AvgPrice: %v", err)
			continue
		}
		if ep > 0 {
			return ep, nil
		}
	}

	return 0, fmt.Errorf("no valid entry price found")
}

// OpenPosition opens a new position
func (m *Manager) OpenPosition(newSide string, price float64) error {
	// Close opposite position
	currentPos, err := m.api.GetPositionList()
	if err != nil {
		return fmt.Errorf("failed to get position list: %w", err)
	}

	if currentPos.Exists {
		side := normalizeSide(currentPos.Side)
		newSide = normalizeSide(newSide)
		if side != "" && side != newSide && currentPos.Size > 0 {
			reduceSide := "Sell"
			if side == "SHORT" {
				reduceSide = "Buy"
			}
			if err := m.api.PlaceOrderMarket(reduceSide, currentPos.Size, true); err != nil {
				m.logger.Error("Ошибка закрытия позиции %s: %v", side, err)
				return fmt.Errorf("failed to close opposite position: %w", err)
			}
		}
	}

	// Calculate quantity
	bal, err := m.api.GetBalance("USDT")
	if err != nil {
		m.logger.Error("Ошибка получения баланса: %v", err)
		return fmt.Errorf("failed to get balance: %w", err)
	}

	instr, err := m.api.GetInstrumentInfo(m.config.Symbol)
	if err != nil {
		m.logger.Error("Ошибка получения информации об инструменте: %v", err)
		return fmt.Errorf("failed to get instrument info: %w", err)
	}

	step := instr.QtyStep
	qty := math.Max(instr.MinQty, step)
	if bal < price*qty {
		m.logger.Error("Недостаточный баланс: %.2f USDT", bal)
		return fmt.Errorf("insufficient balance: %.2f USDT < %.2f*%.4f", bal, price, qty)
	}

	// Place market order
	orderSide := "Buy"
	if normalizeSide(newSide) == "SHORT" {
		orderSide = "Sell"
	}
	if err := m.api.PlaceOrderMarket(orderSide, qty, false); err != nil {
		m.logger.Error("Ошибка открытия позиции %s: %v", newSide, err)
		return fmt.Errorf("failed to place market order: %w", err)
	}

	// Set TP/SL
	entry, err := m.GetLastEntryPriceFromREST()
	if err != nil || entry == 0 {
		m.logger.Debug("Using price as entry price: %.2f", price)
		entry = price
	}

	tp := entry * (1 + m.config.TPPercent)
	sl := entry * (1 - m.config.SlPercent)
	if newSide == "SHORT" {
		tp = entry * (1 - m.config.TPPercent)
		sl = entry * (1 + m.config.SlPercent)
	}

	// Update position with TP/SL
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      m.config.Symbol,
		"takeProfit":  fmt.Sprintf("%.2f", tp),
		"stopLoss":    fmt.Sprintf("%.2f", sl),
		"positionIdx": 0,
		"tpslMode":    "Full",
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal position params: %w", err)
	}

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, err := http.NewRequest("POST", m.config.RESTHost+"/v5/position/trading-stop", bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("failed to create TP/SL request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", m.config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", m.config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", m.api.SignREST(m.config.APISecret, ts, m.config.APIKey, m.config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.logger.Error("Ошибка установки TP/SL: %v", err)
		return fmt.Errorf("failed to update TP/SL: %w", err)
	}
	defer resp.Body.Close()

	// Check API response for errors
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read TP/SL response: %w", err)
	}

	var apiResp struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if err := json.Unmarshal(responseBody, &apiResp); err != nil {
		return fmt.Errorf("failed to parse TP/SL response: %w", err)
	}

	if apiResp.RetCode != 0 {
		return fmt.Errorf("API error setting TP/SL: %d - %s", apiResp.RetCode, apiResp.RetMsg)
	}

	m.logger.Order("Позиция открыта: %s %.4f @ %.2f | TP %.2f  SL %.2f", newSide, qty, entry, tp, sl)
	return nil
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