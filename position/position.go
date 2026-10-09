package position

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
	"verbose-fortnight/internal/prices"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/pnl"
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
		pm.State.Lock()
		pm.State.TradingBlocked = true
		pm.State.BlockReason = models.BlockedReasonFatalAPI
		pm.State.BlockMessage = "position observation failed: " + err.Error()
		pm.State.BlockSetAt = time.Now()
		pm.State.Unlock()
		// A failed observation is not a confirmed flat position. Retain the last
		// snapshot and block new orders until an explicit resync/operator recovery.
		pm.State.StatusLock.RLock()
		last := pm.State.LastPosition
		pm.State.StatusLock.RUnlock()
		return last.Size > 0, last.Side, last.Size, last.TakeProfit, last.StopLoss
	}

	pm.Logger.Info("Received position list from exchange: %d positions", len(positions))
	for _, res := range positions {
		if res.Symbol != pm.Config.Symbol || res.PositionIdx != 0 {
			continue
		}
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
		if res.Symbol != pm.Config.Symbol || res.PositionIdx != 0 {
			continue
		}
		ep, _ := strconv.ParseFloat(res.AvgPrice, 64)
		if ep > 0 {
			return ep
		}
	}
	return 0
}

// UpdatePositionTPSL captures the local lifecycle before observing the exchange.
func (pm *PositionManager) UpdatePositionTPSL(symbol string, tp, sl float64) error {
	return pm.UpdatePositionTPSLContext(pm.APIClient.Context, symbol, tp, sl, "")
}
func (pm *PositionManager) UpdatePositionTPSLContext(ctx context.Context, symbol string, tp, sl float64, lifecycle string) error {
	pm.State.RLock()
	if lifecycle == "" {
		lifecycle = pm.State.ActiveTradeID
	}
	side := pm.State.PosSide
	pm.State.RUnlock()
	_, err := pm.ApplyStopIntent(ctx, symbol, models.StopIntent{TradeID: lifecycle, LifecycleID: lifecycle, Side: side, TP: tp, SL: sl})
	return err
}

// ApplyStopIntent performs the final observation and validates the actual JSON
// prices before POST. The state lock is never held during either request.
// Exchange close/reopen can still race the interval from this GET to the POST.
func (pm *PositionManager) ApplyStopIntent(ctx context.Context, symbol string, intent models.StopIntent) (prices.StopLevels, error) {
	var levels prices.StopLevels
	side := prices.NormalizeSide(intent.Side)
	if symbol != pm.Config.Symbol || side == "" || intent.TradeID == "" || (intent.LifecycleID != "" && intent.LifecycleID != intent.TradeID) {
		return levels, fmt.Errorf("invalid stop target or lifecycle")
	}
	row, err := pm.APIClient.ObservePosition(ctx, symbol, 0, side)
	if err != nil {
		return levels, err
	}
	entry, err := strconv.ParseFloat(row.AvgPrice, 64)
	if err != nil {
		return levels, fmt.Errorf("invalid observed entry: %w", err)
	}
	pm.State.RLock()
	tick := pm.State.Instr.TickSize
	current := !pm.State.TradingBlocked && pm.State.ActiveTradeID == intent.TradeID &&
		(pm.State.PosSide == "" || prices.NormalizeSide(pm.State.PosSide) == side) &&
		(pm.State.PositionState == models.PositionStateOpen || pm.State.PositionState == models.PositionStateOpening)
	pm.State.RUnlock()
	if !current {
		return levels, fmt.Errorf("stale stop lifecycle or direction")
	}
	levels, err = prices.Stops(side, entry, intent.TP, intent.SL, tick, intent.Breakeven)
	if err != nil {
		return levels, err
	}
	raw, err := json.Marshal(map[string]any{"category": "linear", "symbol": row.Symbol, "positionIdx": row.PositionIdx, "takeProfit": levels.TPText, "stopLoss": levels.SLText, "tpslMode": "Full"})
	if err != nil {
		return levels, err
	}
	ctx = operations.WithOwner(ctx, operations.Owner{Lifecycle: intent.TradeID, Side: side})
	_, err = pm.APIClient.DoJSON(ctx, "POST", "/v5/position/trading-stop", "", raw)
	return levels, err
}

func (pm *PositionManager) CancelAllOrders(symbol string) {
	raw, err := json.Marshal(map[string]any{"category": "linear", "symbol": symbol})
	if err == nil {
		_, err = pm.APIClient.DoJSON(pm.APIClient.Context, "POST", "/v5/order/cancel-all", "", raw)
	}
	if err != nil {
		pm.Logger.Error("Failed to cancel orders: %v", err)
		return
	}
	pm.Logger.Info("Cancel-all acknowledged for %s; terminal order state still requires reconciliation", symbol)
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

// CalculatePositionProfit calculates the gross profit of a position based on entry and exit prices.
func (pm *PositionManager) CalculatePositionProfit(side string, entryPrice, exitPrice, qty float64) float64 {
	if entryPrice <= 0 || exitPrice <= 0 || qty <= 0 {
		return 0 // Can't calculate profit without valid prices and quantity
	}

	profit := pnl.Gross(side, entryPrice, exitPrice, qty)

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

// CalculatePositionNetProfit calculates net profit including fee/funding estimates.
func (pm *PositionManager) CalculatePositionNetProfit(side string, entryPrice, exitPrice, qty, fee, funding float64) float64 {
	if entryPrice <= 0 || exitPrice <= 0 || qty <= 0 {
		return 0
	}
	gross := pnl.Gross(side, entryPrice, exitPrice, qty)
	// Keep legacy behavior here: funding arg is treated as cost magnitude.
	fundingSigned := -math.Abs(funding)
	net := pnl.NetFromComponents(gross, math.Abs(fee), fundingSigned)

	pm.State.Lock()
	pm.State.RealizedPnL += net
	if net > 0 {
		pm.State.TotalProfit += net
	} else {
		pm.State.TotalLoss += math.Abs(net)
	}
	pm.State.Unlock()

	return net
}

// GetMaxValue returns the maximum of three values
func (pm *PositionManager) GetMax(a, b, c float64) float64 {
	return math.Max(math.Max(a, b), c)
}

// GetMinValue returns the minimum of three values
func (pm *PositionManager) GetMin(a, b, c float64) float64 {
	return math.Min(math.Min(a, b), c)
}
