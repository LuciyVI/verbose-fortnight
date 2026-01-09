package position

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

// PositionManager handles all position-related operations
type PositionManager struct {
	APIClient *api.RESTClient
	Config    *config.Config
	State     *models.State
	Logger    logging.LoggerInterface
}

// NewPositionManager creates a new position manager
func NewPositionManager(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface) *PositionManager {
	return &PositionManager{
		APIClient: apiClient,
		Config:    cfg,
		State:     state,
		Logger:    logger,
	}
}

// NormalizeSide normalizes position side to LONG/SHORT
func (pm *PositionManager) NormalizeSide(side string) string {
	switch strings.ToUpper(side) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

// HasOpenPosition checks if there's an open position and returns details
func (pm *PositionManager) HasOpenPosition() (bool, string, float64, float64, float64) {
	pm.Logger.Info("Checking for open positions with exchange...")
	positions, err := pm.APIClient.GetPositionList(pm.Config.Symbol)
	if err != nil {
		pm.Logger.Error("Failed to fetch position list: %v", err)
		return false, "", 0, 0, 0
	}

	pm.Logger.Info("Received position list from exchange: %d positions", len(positions))
	for _, res := range positions {
		size, _ := strconv.ParseFloat(res.Size, 64)
		if size > 0 {
			// Handle empty TP/SL values
			takeProfit := res.TakeProfit
			stopLoss := res.StopLoss
			if takeProfit == "" {
				takeProfit = "0"
			}
			if stopLoss == "" {
				stopLoss = "0"
			}

			tp, _ := strconv.ParseFloat(takeProfit, 64)
			sl, _ := strconv.ParseFloat(stopLoss, 64)
			side := pm.NormalizeSide(res.Side)
			entry, _ := strconv.ParseFloat(res.AvgPrice, 64)

			pm.Logger.Info("Found open position: Side=%s, Size=%.4f, TP=%.2f, SL=%.2f", side, size, tp, sl)
			pm.State.StatusLock.Lock()
			pm.State.LastPosition = models.PositionSnapshot{
				Side:       side,
				Size:       size,
				EntryPrice: entry,
				TakeProfit: tp,
				StopLoss:   sl,
				UpdatedAt:  time.Now(),
			}
			pm.State.StatusLock.Unlock()
			return true, side, size, tp, sl
		}
	}
	pm.Logger.Info("No open positions found")
	pm.State.StatusLock.Lock()
	pm.State.LastPosition = models.PositionSnapshot{
		UpdatedAt: time.Now(),
	}
	pm.State.StatusLock.Unlock()
	return false, "", 0, 0, 0
}

// GetLastEntryPrice fetches the last entry price from REST API
func (pm *PositionManager) GetLastEntryPrice() float64 {
	positions, err := pm.APIClient.GetPositionList(pm.Config.Symbol)
	if err != nil {
		pm.Logger.Error("Failed to fetch position list for entry price: %v", err)
		return 0
	}

	for _, res := range positions {
		ep, _ := strconv.ParseFloat(res.AvgPrice, 64)
		if ep > 0 {
			return ep
		}
	}
	return 0
}

// UpdatePositionTPSL updates the TP/SL for a position
func (pm *PositionManager) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	exists, _, _, _, _ := pm.HasOpenPosition()
	if !exists {
		return fmt.Errorf("no open position to update TP/SL")
	}

	const path = "/v5/position/trading-stop"
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

	// Log outgoing request
	pm.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))

	req, _ := http.NewRequest("POST", pm.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", pm.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", pm.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", pm.APIClient.SignREST(pm.Config.APISecret, ts, pm.Config.APIKey, pm.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		pm.Logger.Error("Failed to send POST request to exchange: %v", err)
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	pm.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
	}
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
		pm.Logger.Error("Error in TP/SL update response: %d: %s", r.RetCode, r.RetMsg)
		return fmt.Errorf("error updating TP/SL: %d: %s", r.RetCode, r.RetMsg)
	}

	return nil
}

// CancelAllOrders cancels all active orders for a symbol
func (pm *PositionManager) CancelAllOrders(symbol string) {
	const path = "/v5/order/cancel-all"
	body := map[string]interface{}{
		"category": "linear",
		"symbol":   symbol,
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	req, _ := http.NewRequest("POST", pm.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", pm.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", pm.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", pm.APIClient.SignREST(pm.Config.APISecret, ts, pm.Config.APIKey, pm.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		pm.Logger.Error("Failed to cancel orders: %v", err)
		return
	}
	defer resp.Body.Close()
	pm.Logger.Info("Cancelled all active orders for %s", symbol)
}

// GetLastBidPrice returns the last bid price from orderbook
func (pm *PositionManager) GetLastBidPrice() float64 {
	pm.State.ObLock.Lock()
	defer pm.State.ObLock.Unlock()

	var max float64
	for ps := range pm.State.BidsMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p > max {
			max = p
		}
	}
	return max
}

// GetLastAskPrice returns the last ask price from orderbook
func (pm *PositionManager) GetLastAskPrice() float64 {
	pm.State.ObLock.Lock()
	defer pm.State.ObLock.Unlock()

	var min float64
	for ps := range pm.State.AsksMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p < min || min == 0 {
			min = p
		}
	}
	return min
}

// UpdateSignalStats updates signal statistics
func (pm *PositionManager) UpdateSignalStats(signalType string, profit float64) {
	pm.State.SignalStats.Lock()
	defer pm.State.SignalStats.Unlock()

	pm.State.SignalStats.Total++
	if profit > 0 {
		pm.State.SignalStats.Correct++
	} else {
		pm.State.SignalStats.FalsePositive++
	}
}

// CalculatePositionProfit calculates the profit of a position based on entry and exit prices
func (pm *PositionManager) CalculatePositionProfit(side string, entryPrice, exitPrice, qty float64) float64 {
	if entryPrice <= 0 || exitPrice <= 0 || qty <= 0 {
		return 0 // Can't calculate profit without valid prices and quantity
	}

	var profit float64
	if side == "LONG" {
		profit = (exitPrice - entryPrice) * qty
	} else if side == "SHORT" {
		profit = (entryPrice - exitPrice) * qty
	}

	// Update state with profit information
	pm.State.Lock()
	pm.State.RealizedPnL += profit
	if profit > 0 {
		pm.State.TotalProfit += profit
	} else {
		pm.State.TotalLoss += math.Abs(profit)
	}
	pm.State.Unlock()

	return profit
}

// GetMaxValue returns the maximum of three values
func (pm *PositionManager) GetMax(a, b, c float64) float64 {
	return math.Max(math.Max(a, b), c)
}

// GetMinValue returns the minimum of three values
func (pm *PositionManager) GetMin(a, b, c float64) float64 {
	return math.Min(math.Min(a, b), c)
}
