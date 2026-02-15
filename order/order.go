package order

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/indicators"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

// OrderManager handles order placement and management
type OrderManager struct {
	APIClient *api.RESTClient
	Config    *config.Config
	State     *models.State
	Logger    logging.LoggerInterface
}

// NewOrderManager creates a new order manager
func NewOrderManager(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface) *OrderManager {
	return &OrderManager{
		APIClient: apiClient,
		Config:    cfg,
		State:     state,
		Logger:    logger,
	}
}

// FormatQty formats quantity according to instrument step
func (om *OrderManager) FormatQty(qty, step float64) string {
	dec := 0
	tempStep := step
	for tempStep < 1 {
		tempStep *= 10
		dec++
	}
	return strconv.FormatFloat(qty, 'f', dec, 64)
}

// PlaceOrderMarket places a market order
func (om *OrderManager) PlaceOrderMarket(side string, qty float64, reduceOnly bool) error {
	if side == "" {
		return fmt.Errorf("invalid side: empty")
	}
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      om.Config.Symbol,
		"side":        side,
		"orderType":   "Market",
		"qty":         om.FormatQty(qty, om.State.Instr.QtyStep),
		"timeInForce": "IOC",
		"positionIdx": 0,
	}
	if reduceOnly {
		body["reduceOnly"] = true
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	om.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))

	req, _ := http.NewRequest("POST", om.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", om.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", om.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", om.APIClient.SignREST(om.Config.APISecret, ts, om.Config.APIKey, om.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		om.Logger.Error("Failed to send POST request to exchange: %v", err)
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	om.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			OrderID string `json:"orderId"`
		} `json:"result"`
	}
	if json.Unmarshal(reply, &r) != nil || r.RetCode != 0 {
		om.Logger.Error("Error in market order response: %d: %s", r.RetCode, r.RetMsg)
		return fmt.Errorf("error placing market order: %d: %s", r.RetCode, r.RetMsg)
	}
	if r.Result.OrderID != "" {
		om.State.Lock()
		om.State.LastOrderID = r.Result.OrderID
		om.State.Unlock()
		om.Logger.Info("Market order id: %s", r.Result.OrderID)
	}
	om.Logger.Info("Market %s %.4f OK", side, qty)
	return nil
}

// PlaceTakeProfitOrder places a take profit order
func (om *OrderManager) PlaceTakeProfitOrder(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      om.Config.Symbol,
		"side":        side,
		"orderType":   "Limit",
		"price":       fmt.Sprintf("%.2f", price),
		"qty":         om.FormatQty(qty, om.State.Instr.QtyStep),
		"timeInForce": "GTC",
		"reduceOnly":  true,
		"positionIdx": 0,
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	om.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))

	req, _ := http.NewRequest("POST", om.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", om.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", om.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", om.APIClient.SignREST(om.Config.APISecret, ts, om.Config.APIKey, om.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		om.Logger.Error("Failed to send POST request to exchange: %v", err)
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	om.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))

	om.Logger.Info("TP set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}

// PlaceStopLossOrder places a stop loss order
func (om *OrderManager) PlaceStopLossOrder(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      om.Config.Symbol,
		"side":        side,
		"orderType":   "Limit",
		"price":       fmt.Sprintf("%.2f", price),
		"qty":         om.FormatQty(qty, om.State.Instr.QtyStep),
		"timeInForce": "GTC",
		"reduceOnly":  true,
		"positionIdx": 0,
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	om.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))

	req, _ := http.NewRequest("POST", om.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", om.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", om.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", om.APIClient.SignREST(om.Config.APISecret, ts, om.Config.APIKey, om.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		om.Logger.Error("Failed to send POST request to exchange: %v", err)
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	om.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))

	om.Logger.Info("SL set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}

// PlaceStopLoss places a stop loss using stopLossPrice parameter
func (om *OrderManager) PlaceStopLoss(side string, qty, price float64) error {
	const path = "/v5/order/create"
	body := map[string]interface{}{
		"category":      "linear",
		"symbol":        om.Config.Symbol,
		"side":          side,
		"orderType":     "Market", // or "Limit" - depends on strategy
		"qty":           om.FormatQty(qty, om.State.Instr.QtyStep),
		"stopLossPrice": fmt.Sprintf("%.2f", price),
		"timeInForce":   "GTC",
		"reduceOnly":    true,
		"positionIdx":   0,
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())

	// Log outgoing request
	om.Logger.Info("Sending POST request to exchange: %s, Body: %s", path, string(raw))

	req, _ := http.NewRequest("POST", om.Config.DemoRESTHost+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", om.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", om.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", om.APIClient.SignREST(om.Config.APISecret, ts, om.Config.APIKey, om.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		om.Logger.Error("Failed to send POST request to exchange: %v", err)
		return err
	}
	defer resp.Body.Close()

	reply, _ := io.ReadAll(resp.Body)

	// Log incoming response
	om.Logger.Info("Received response from exchange for %s: Status %d, Body: %s", path, resp.StatusCode, string(reply))

	om.Logger.Info("SL set @ %.2f (%s, qty %.4f)", price, side, qty)
	return nil
}

// CalculateTakeProfitATR calculates take profit based on ATR
func (om *OrderManager) CalculateTakeProfitATR(side string) float64 {
	atr := indicators.CalculateATR(om.State.Highs, om.State.Lows, om.State.Closes, 14)
	if atr == 0 {
		atr = 90 // Default value
	}
	om.Logger.Debug("ATR(14) calculated for TP: %.4f", atr)

	var tp float64
	if side == "LONG" {
		lastAsk := om.getLastAskPrice()
		tp = lastAsk + atr*1.5
		om.Logger.Debug("ATR TP calculation for LONG: %.2f + %.4f*1.5 = %.2f", lastAsk, atr, tp)
	} else if side == "SHORT" {
		lastBid := om.getLastBidPrice()
		tp = lastBid - atr*1.5
		om.Logger.Debug("ATR TP calculation for SHORT: %.2f - %.4f*1.5 = %.2f", lastBid, atr, tp)
	}

	if om.State.Instr.TickSize > 0 {
		tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
		om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
	}
	return tp
}

// CalculateTakeProfitBB calculates take profit based on Bollinger Bands
func (om *OrderManager) CalculateTakeProfitBB(side string) float64 {
	if len(om.State.Closes) < om.Config.WindowSize {
		om.Logger.Debug("Not enough closes for BB calculation: need %d, have %d", om.Config.WindowSize, len(om.State.Closes))
		return 0
	}

	smaVal := indicators.SMA(om.State.Closes)
	stdVal := indicators.StdDev(om.State.Closes)
	om.Logger.Debug("BB calculation - SMA: %.2f, StdDev: %.4f", smaVal, stdVal)

	var tp float64
	if side == "LONG" {
		tp = smaVal + om.Config.BbMult*stdVal
		om.Logger.Debug("BB TP calculation for LONG: %.2f + %.2f*%.4f = %.2f", smaVal, om.Config.BbMult, stdVal, tp)
	} else if side == "SHORT" {
		tp = smaVal - om.Config.BbMult*stdVal
		om.Logger.Debug("BB TP calculation for SHORT: %.2f - %.2f*%.4f = %.2f", smaVal, om.Config.BbMult, stdVal, tp)
	}

	if om.State.Instr.TickSize > 0 {
		tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
		om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
	}
	return tp
}

// CalculateTakeProfitVolume calculates take profit based on volume
func (om *OrderManager) CalculateTakeProfitVolume(side string, thresholdQty float64) float64 {
	om.State.ObLock.Lock()
	defer om.State.ObLock.Unlock()

	var arr []struct{ p, sz float64 }

	if side == "LONG" && len(om.State.AsksMap) > 0 {
		om.Logger.Debug("Calculating volume TP for LONG position with threshold %.2f", thresholdQty)
		for ps, sz := range om.State.AsksMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, struct{ p, sz float64 }{p, sz})
		}
		om.Logger.Debug("Found %d ask levels in orderbook", len(arr))
		// Sort by price ascending
		sort.Slice(arr, func(i, j int) bool { return arr[i].p < arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= thresholdQty {
				tp := v.p * 0.9995
				om.Logger.Debug("Found volume wall at price %.2f with cumulative size %.2f >= threshold %.2f", v.p, cum, thresholdQty)
				if om.State.Instr.TickSize > 0 {
					tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
					om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[len(arr)-1].p * 1.0005
			om.Logger.Debug("Using fallback TP at last ask level: %.2f", tp)
			if om.State.Instr.TickSize > 0 {
				tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
				om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
			}
			return tp
		}
	} else if side == "SHORT" && len(om.State.BidsMap) > 0 {
		om.Logger.Debug("Calculating volume TP for SHORT position with threshold %.2f", thresholdQty)
		for ps, sz := range om.State.BidsMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, struct{ p, sz float64 }{p, sz})
		}
		om.Logger.Debug("Found %d bid levels in orderbook", len(arr))
		// Sort by price descending
		sort.Slice(arr, func(i, j int) bool { return arr[i].p > arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= thresholdQty {
				tp := v.p * 0.9995
				om.Logger.Debug("Found volume wall at price %.2f with cumulative size %.2f >= threshold %.2f", v.p, cum, thresholdQty)
				if om.State.Instr.TickSize > 0 {
					tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
					om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[0].p * 0.9995
			om.Logger.Debug("Using fallback TP at first bid level: %.2f", tp)
			if om.State.Instr.TickSize > 0 {
				tp = math.Round(tp/om.State.Instr.TickSize) * om.State.Instr.TickSize
				om.Logger.Debug("Rounded TP to tick size: %.2f", tp)
			}
			return tp
		}
	} else {
		om.Logger.Debug("No orderbook data available for %s position volume calculation", side)
	}
	return 0
}

// CalculateTakeProfitVoting calculates take profit using a voting system
func (om *OrderManager) CalculateTakeProfitVoting(side string) float64 {
	tpBB := om.CalculateTakeProfitBB(side)
	tpATR := om.CalculateTakeProfitATR(side)
	tpVol := om.CalculateTakeProfitVolume(side, om.Config.TpThresholdQty)

	switch side {
	case "LONG":
		return math.Max(math.Max(tpBB, tpATR), tpVol)
	case "SHORT":
		return math.Min(math.Min(tpBB, tpATR), tpVol)
	default:
		return 0
	}
}

// getLastAskPrice returns the last ask price from orderbook
func (om *OrderManager) getLastAskPrice() float64 {
	om.State.ObLock.Lock()
	defer om.State.ObLock.Unlock()

	var max float64
	for ps := range om.State.AsksMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p > max {
			max = p
		}
	}
	return max
}

// getLastBidPrice returns the last bid price from orderbook
func (om *OrderManager) getLastBidPrice() float64 {
	om.State.ObLock.Lock()
	defer om.State.ObLock.Unlock()

	var min float64
	for ps := range om.State.BidsMap {
		p, _ := strconv.ParseFloat(ps, 64)
		if p < min || min == 0 {
			min = p
		}
	}
	return min
}
