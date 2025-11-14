package strategy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/indicators"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/order"
	"verbose-fortnight/position"
)

// Trader handles trading logic and signal processing
type Trader struct {
	APIClient       *api.RESTClient
	Config          *config.Config
	State           *models.State
	OrderManager    *order.OrderManager
	PositionManager *position.PositionManager
	Logger          logging.LoggerInterface
}

// NewTrader creates a new trader instance
func NewTrader(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface) *Trader {
	orderManager := order.NewOrderManager(apiClient, cfg, state, logger)
	positionManager := position.NewPositionManager(apiClient, cfg, state, logger)
	
	return &Trader{
		APIClient:       apiClient,
		Config:          cfg,
		State:           state,
		OrderManager:    orderManager,
		PositionManager: positionManager,
		Logger:          logger,
	}
}

// TPWorker processes take profit jobs
func (t *Trader) TPWorker() {
	t.Logger.Info("Starting TP worker to process take profit jobs...")
	
	for job := range t.State.TPChan {
		t.Logger.Info("Received TP job: Side=%s, Qty=%.4f, EntryPrice=%.2f", job.Side, job.Qty, job.EntryPrice)
		
		exists, side, qty, _, _ := t.PositionManager.HasOpenPosition()
		if !exists || t.PositionManager.NormalizeSide(job.Side) != t.PositionManager.NormalizeSide(side) || job.Qty != qty {
			t.Logger.Info("Position mismatch, skipping TP job")
			continue
		}
		
		// Calculate TP based on 15-minute price projection
		t.Logger.Debug("Calculating TP based on 15-minute price projection...")
		entryPrice := job.EntryPrice
		if entryPrice == 0 {
			entryPrice = t.PositionManager.GetLastEntryPrice()
		}
		if entryPrice == 0 {
			t.Logger.Error("Cannot find entry price - skipping TP")
			continue
		}
		
		// Use our new 15-minute projection method
		finalTP := t.calculateTPBasedOn15MinProjection(entryPrice, job.Side)
		t.Logger.Info("TP calculated using 15-minute projection: %.2f", finalTP)
		
		if finalTP == 0 || math.IsNaN(finalTP) {
			entryPrice := job.EntryPrice
			if entryPrice == 0 {
				entryPrice = t.PositionManager.GetLastEntryPrice()
			}
			if entryPrice == 0 {
				t.Logger.Error("Cannot find entry price - skipping TP")
				continue
			}
			// Ensure TickSize is valid before using it
			tickSize := t.State.Instr.TickSize
			if tickSize <= 0 {
				tickSize = 0.1 // Default fallback
			}
			if job.Side == "LONG" {
				finalTP = math.Round(entryPrice*(1+t.Config.TpOffset)/tickSize) * tickSize
			} else {
				finalTP = math.Round(entryPrice*(1-t.Config.TpOffset)/tickSize) * tickSize
			}
			t.Logger.Info("Using fallback TP calculation: %.2f", finalTP)
		}
		
		// Calculate TP/SL with 2:1 ratio
		entryPrice = job.EntryPrice
		if entryPrice == 0 {
			entryPrice = t.PositionManager.GetLastEntryPrice()
		}
		if entryPrice == 0 {
			entryPrice = finalTP // Fallback if we can't get entry price
		}
		
		// Apply 2:1 ratio between TP and SL based on 15-minute projection
		finalTP, _ = t.calculateTPSLWithRatio(entryPrice, job.Side)
		
		orderSide := "Sell"
		if job.Side == "SHORT" {
			orderSide = "Buy"
		}
		
		t.Logger.Info("Placing take profit order: %s, Qty=%.4f, Price=%.2f", orderSide, job.Qty, finalTP)
		if err := t.OrderManager.PlaceTakeProfitOrder(orderSide, job.Qty, finalTP); err != nil {
			t.Logger.Error("Error placing TP: %v", err)
		} else {
			t.Logger.Info("Successfully placed take profit order")
		}
	}
}

// CheckOrderbookStrength checks orderbook for potential signal confirmation
func (t *Trader) CheckOrderbookStrength(side string) bool {
	t.State.ObLock.Lock()
	defer t.State.ObLock.Unlock()

	var bidDepth, askDepth float64
	for _, size := range t.State.BidsMap {
		bidDepth += size
	}
	for _, size := range t.State.AsksMap {
		askDepth += size
	}

	// Log orderbook volumes
	if t.Config.Debug {
		t.Logger.Debug("Bid Depth: %.2f, Ask Depth: %.2f", bidDepth, askDepth)
	}

	// Calculate and log the strength ratio
	var ratio float64
	// For LONG signals: bid/ask ratio (want more buying than selling pressure)
	// For SHORT signals: ask/bid ratio (want more selling than buying pressure)
	if side == "LONG" && askDepth != 0 {
		ratio = bidDepth / askDepth
	} else if side == "SHORT" && bidDepth != 0 {
		ratio = askDepth / bidDepth
	}

	// Use dynamic threshold if enabled
	threshold := t.calculateDynamicOrderbookThreshold()

	// Log with dynamic threshold
	if t.Config.Debug {
		t.Logger.Debug("Orderbook strength for %s: ratio = %.2f, dynamic threshold = %.2f", side, ratio, threshold)
	}

	// Check if the basic orderbook condition is met
	orderbookConditionMet := false
	orderbookConditionMet = ratio > threshold

	if !orderbookConditionMet {
		if t.Config.Debug {
			t.Logger.Debug("Orderbook condition not met for side %s: ratio %.2f <= threshold %.2f", side, ratio, threshold)
		}
		return false
	}

	// If using dynamic filtering, also check volume confirmation
	if t.Config.UseDynamicOrderbookFilter {
		// Get the most recent volume data
		currentVolume := 0.0
		if len(t.State.RecentVolumes) > 0 {
			currentVolume = t.State.RecentVolumes[len(t.State.RecentVolumes)-1]
		}

		// Check if volume is sufficient (either a spike or above minimum threshold)
		if !t.detectVolumeSpike(currentVolume) {
			if t.Config.Debug {
				t.Logger.Debug("Volume confirmation failed for side %s: current volume %.2f not meeting spike criteria", side, currentVolume)
			}
			return false
		}

		if t.Config.Debug {
			t.Logger.Debug("Volume confirmation passed for side %s: current volume %.2f", side, currentVolume)
		}
	}

	// Adaptive thresholds based on market regime (additional check)
	if t.State.MarketRegime == "trend" {
		if side == "LONG" && bidDepth/askDepth > threshold {
			if t.Config.Debug {
				t.Logger.Debug("Trend: LONG signal confirmed")
			}
			return true
		} else if side == "SHORT" && askDepth/bidDepth > threshold {
			if t.Config.Debug {
				t.Logger.Debug("Trend: SHORT signal confirmed")
			}
			return true
		}
	} else if t.State.MarketRegime == "range" {
		if side == "LONG" && bidDepth/askDepth > threshold {
			if t.Config.Debug {
				t.Logger.Debug("Range: LONG signal confirmed")
			}
			return true
		} else if side == "SHORT" && askDepth/bidDepth > threshold {
			if t.Config.Debug {
				t.Logger.Debug("Range: SHORT signal confirmed")
			}
			return true
		}
	}

	// If not in regime-specific logic, just check the basic condition with dynamic threshold
	if orderbookConditionMet {
		if t.Config.Debug {
			t.Logger.Debug("%s signal confirmed with dynamic threshold: %.2f > %.2f", side, ratio, threshold)
		}
		return true
	}

	t.Logger.Debug("Orderbook strength check failed for side %s in regime %s", side, t.State.MarketRegime)
	return false
}

// DetectMarketRegime detects market regime (trend vs range)
func (t *Trader) DetectMarketRegime() {
	if len(t.State.Closes) < 50 {
		return
	}

	recent := t.State.Closes[len(t.State.Closes)-50:]
	t.Logger.Debug("Detecting market regime with %d recent closes", len(recent))

	// Calculate multiple metrics for better regime detection
	rangePerc := calculateRangePercentage(recent)
	t.Logger.Debug("Range percentage calculated: %.2f", rangePerc)

	volatilityRegime := detectVolatilityRegime(t.State.Closes)
	t.Logger.Debug("Volatility regime detected: %s", volatilityRegime)

	trendStrength := calculateTrendStrength(recent)
	t.Logger.Debug("Trend strength calculated: %.2f", trendStrength)

	t.Logger.Info("Market regime metrics - RangePerc: %.2f, VolatilityRegime: %s, TrendStrength: %.2f",
		rangePerc, volatilityRegime, trendStrength)

	// Combine multiple signals for final determination
	if rangePerc > 3.0 && trendStrength > t.Config.RegimeTrendThreshold {
		t.State.MarketRegime = "trend"
		t.Logger.Info("Market regime set to: trend")
	} else if volatilityRegime == "low" && rangePerc < t.Config.RegimeRangeThreshold {
		t.State.MarketRegime = "range"
		t.Logger.Info("Market regime set to: range")
	} else {
		// If mixed signals, default to trend detection
		if trendStrength > t.Config.RegimeTrendThreshold {
			t.State.MarketRegime = "trend"
			t.Logger.Info("Market regime set to: trend (based on trend strength)")
		} else {
			t.State.MarketRegime = "range"
			t.Logger.Info("Market regime set to: range (based on trend strength)")
		}
	}

	// Update state with higher-order trend if enabled
	if t.Config.UseHigherTrendFilter && len(t.State.LongTermCloses) >= t.Config.HigherTrendPeriod {
		t.State.HigherTrend = t.getHigherTrendDirection()
	}
}

// getHigherTrendDirection determines the direction of the higher-order trend
func (t *Trader) getHigherTrendDirection() string {
	if len(t.State.LongTermCloses) < t.Config.HigherTrendPeriod {
		return "unknown"
	}

	// Calculate the higher-order trend using the specified period
	higherSMA := indicators.SMAWithPeriod(t.State.LongTermCloses, t.Config.HigherTrendPeriod)
	currentPrice := t.State.LongTermCloses[len(t.State.LongTermCloses)-1]

	// Determine trend based on position relative to higher SMA
	if currentPrice > higherSMA {
		return "up"
	} else if currentPrice < higherSMA {
		return "down"
	} else {
		return "neutral"
	}
}

// calculateRangePercentage calculates the percentage range of price movement
func calculateRangePercentage(closes []float64) float64 {
	if len(closes) == 0 {
		return 0
	}
	
	maxHigh := indicators.MaxSlice(closes)
	minLow := indicators.MinSlice(closes)
	
	if minLow <= 0 {
		return 0
	}
	
	return (maxHigh - minLow) / minLow * 100
}

// detectVolatilityRegime determines if the market has high or low volatility
func detectVolatilityRegime(closes []float64) string {
	if len(closes) < 50 {
		return "unknown"
	}

	// Use the most recent 20 closes to calculate recent volatility
	recent := closes[len(closes)-20:]
	
	// Calculate standard deviation as a volatility measure
	mean := 0.0
	for _, c := range recent {
		mean += c
	}
	mean /= float64(len(recent))
	
	var variance float64
	for _, c := range recent {
		variance += (c - mean) * (c - mean)
	}
	variance /= float64(len(recent))
	stdDev := math.Sqrt(variance)
	
	// Calculate coefficient of variation
	if mean != 0 {
		cv := stdDev / math.Abs(mean) * 100
		
		if cv > 5.0 { // High threshold for high volatility in crypto
			return "high"
		} else {
			return "low"
		}
	}
	
	return "unknown"
}

// calculateTrendStrength measures the strength of the current trend
func calculateTrendStrength(closes []float64) float64 {
	if len(closes) < 2 {
		return 0
	}
	
	totalChange := math.Abs(closes[len(closes)-1] - closes[0])
	totalDistance := 0.0
	
	// Sum of absolute changes between each candle to see total movement vs net trend
	for i := 1; i < len(closes); i++ {
		totalDistance += math.Abs(closes[i] - closes[i-1])
	}
	
	if totalDistance == 0 {
		return 0
	}
	
	// A strong trend would have totalChange close to totalDistance
	// A ranging market would have totalDistance much larger than totalChange
	trendRatio := totalChange / totalDistance
	
	// Normalize to 0-1 scale
	return math.Min(trendRatio*2, 1.0) // Multiply by 2 to increase sensitivity
}

// HandleLongSignal processes a long signal
func (t *Trader) HandleLongSignal(closePrice float64) {
	exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
	side = t.PositionManager.NormalizeSide(side)
	newSide := t.PositionManager.NormalizeSide("LONG")

	if !exists {
		t.openPosition(newSide, closePrice)
	} else if side == newSide {
		t.adjustTPSL(closePrice)
	} else {
		// Close opposite position first
		t.closeOppositePosition(newSide)
		// Then open new position
		t.openPosition(newSide, closePrice)
	}
}

// HandleShortSignal processes a short signal
func (t *Trader) HandleShortSignal(closePrice float64) {
	exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
	side = t.PositionManager.NormalizeSide(side)
	newSide := t.PositionManager.NormalizeSide("SHORT")

	if !exists {
		t.openPosition(newSide, closePrice)
	} else if side == newSide {
		t.adjustTPSLForShort(closePrice)
	} else {
		// Close opposite position first
		t.closeOppositePosition(newSide)
		// Then open new position
		t.openPosition(newSide, closePrice)
	}
}

// openPosition opens a new position
func (t *Trader) openPosition(newSide string, price float64) {
	t.Logger.Info("Attempting to open new position: %s @ price %.2f", newSide, price)
	
	// Close opposite position if it exists and calculate profit
	if exists, side, qty, _, _ := t.PositionManager.HasOpenPosition(); exists {
		side = t.PositionManager.NormalizeSide(side)
		newSide = t.PositionManager.NormalizeSide(newSide)
		if side != "" && side != newSide && qty > 0 {
			t.Logger.Info("Closing opposite position: %s", side)
			
			// Get entry price for profit calculation
			entryPrice := t.PositionManager.GetLastEntryPrice()
			
			reduceSide := "Sell"
			if side == "SHORT" {
				reduceSide = "Buy"
			}
			if err := t.OrderManager.PlaceOrderMarket(reduceSide, qty, true); err != nil {
				t.Logger.Error("Error closing position %s: %v", side, err)
				return
			}
			
			t.Logger.Info("Successfully sent order to close position %s", side)
			
			// Calculate profit based on current price and entry price
			exitPrice := price  // Using the current price as the exit price
			if side == "LONG" {
				// For LONG positions, we sell to close, so we might get a slightly lower price
				exitPrice = t.PositionManager.GetLastBidPrice()
				if exitPrice <= 0 {
					exitPrice = price
				}
			} else if side == "SHORT" {
				// For SHORT positions, we buy to close, so we might get a slightly higher price
				exitPrice = t.PositionManager.GetLastAskPrice()
				if exitPrice <= 0 {
					exitPrice = price
				}
			}
			
			profit := t.PositionManager.CalculatePositionProfit(side, entryPrice, exitPrice, qty)
			signalType := "CLOSE_LONG"
			if side == "SHORT" {
				signalType = "CLOSE_SHORT"
			}
			
			t.PositionManager.UpdateSignalStats(signalType, profit)
		}
	}

	// Calculate quantity
	t.Logger.Info("Fetching balance from exchange...")
	bal, err := t.APIClient.GetBalance("USDT")
	if err != nil {
		t.Logger.Error("Error getting balance: %v", err)
		return
	}
	t.Logger.Info("Current balance: %.2f USDT", bal)

	step := t.State.Instr.QtyStep
	qty := math.Max(t.State.Instr.MinQty, step)
	if bal < price*qty {
		t.Logger.Error("Insufficient balance: %.2f USDT", bal)
		return
	}

	// Place market order
	orderSide := "Buy"
	if t.PositionManager.NormalizeSide(newSide) == "SHORT" {
		orderSide = "Sell"
	}
	t.Logger.Info("Placing market order: %s %.4f", orderSide, qty)
	if err := t.OrderManager.PlaceOrderMarket(orderSide, qty, false); err != nil {
		t.Logger.Error("Error opening position %s: %v", newSide, err)
		return
	}
	t.Logger.Info("Successfully placed market order to open position %s", newSide)

	// Set TP/SL with 2:1 ratio based on 15-minute projection
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		entry = price
	}

	// Calculate TP/SL with 2:1 ratio based on 15-minute projection
	tp, sl := t.calculateTPSLWithRatio(entry, newSide)

	t.Logger.Info("Setting TP/SL with 2:1 ratio: TP=%.2f, SL=%.2f", tp, sl)

	// Update position TP/SL
	body := map[string]interface{}{
		"category":    "linear",
		"symbol":      t.Config.Symbol,
		"takeProfit":  fmt.Sprintf("%.2f", tp),
		"stopLoss":    fmt.Sprintf("%.2f", sl),
		"positionIdx": 0,
		"tpslMode":    "Full",
	}

	raw, _ := json.Marshal(body)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	
	// Log outgoing request
	t.Logger.Info("Sending POST request to exchange: /v5/position/trading-stop, Body: %s", string(raw))
	
	req, _ := http.NewRequest("POST", t.Config.DemoRESTHost+"/v5/position/trading-stop", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-BAPI-API-KEY", t.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", t.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", t.APIClient.SignREST(t.Config.APISecret, ts, t.Config.APIKey, t.Config.RecvWindow, string(raw)))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logger.Error("Failed to send POST request to exchange: %v", err)
		return
	}
	defer resp.Body.Close()
	
	reply, _ := io.ReadAll(resp.Body)
	
	// Log incoming response
	t.Logger.Info("Received response from exchange for /v5/position/trading-stop: Status %d, Body: %s", resp.StatusCode, string(reply))

	t.Logger.Info("Position opened: %s %.4f @ %.2f | TP %.2f  SL %.2f", newSide, qty, entry, tp, sl)

	// Reset partial profit tracking for new position
	t.State.PartialTPTriggered = false
	t.State.PartialTPPrice = 0.0
}

// calculateTPSLWithRatio calculates TP/SL with a 2:1 ratio based on 15-minute price projection or ATR
// If UseDynamicRiskManagement is enabled, uses ATR-based levels; otherwise uses percentage-based
func (t *Trader) calculateTPSLWithRatio(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	if t.Config.UseDynamicRiskManagement {
		return t.calculateDynamicTPSL(entryPrice, positionSide)
	} else {
		// Fallback to original calculation
		return t.calculateFixedTPSL(entryPrice, positionSide)
	}
}

// calculateFixedTPSL calculates TP/SL with a 2:1 ratio based on 15-minute price projection (original method)
func (t *Trader) calculateFixedTPSL(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	// Calculate TP based on 15-minute price projection
	takeProfit = t.calculateTPBasedOn15MinProjection(entryPrice, positionSide)

	// Calculate the percentage distance from entry to TP
	var tpPercentDistance float64
	if positionSide == "LONG" {
		tpPercentDistance = math.Abs((takeProfit - entryPrice) / entryPrice * 100)
	} else {
		tpPercentDistance = math.Abs((entryPrice - takeProfit) / entryPrice * 100)
	}

	// Calculate SL distance as half of TP distance (2:1 ratio)
	slPercentDistance := tpPercentDistance / 2

	// Set SL based on position side to maintain 2:1 ratio
	if positionSide == "LONG" {
		// For LONG positions: SL below entry price, TP above entry price
		stopLoss = entryPrice * (1 - slPercentDistance/100)
	} else {
		// For SHORT positions: SL above entry price, TP below entry price
		stopLoss = entryPrice * (1 + slPercentDistance/100)
	}

	// Validate that we maintain the 2:1 ratio
	tpDistanceActual := math.Abs(takeProfit - entryPrice)
	slDistanceActual := math.Abs(entryPrice - stopLoss)

	ratio := 0.0
	if slDistanceActual > 0 {
		ratio = tpDistanceActual / slDistanceActual
	}

	t.Logger.Debug("Fixed TP/SL calculation with 2:1 ratio - Entry: %.2f, TP: %.2f, SL: %.2f, TP Distance: %.4f, SL Distance: %.4f, Actual Ratio: %.2f:1",
		entryPrice, takeProfit, stopLoss, tpDistanceActual, slDistanceActual, ratio)

	return takeProfit, stopLoss
}

// calculateDynamicTPSL calculates TP/SL based on ATR values
func (t *Trader) calculateDynamicTPSL(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	// Calculate ATR value
	if len(t.State.Closes) < t.Config.ATRPeriod {
		t.Logger.Warning("Not enough data for ATR calculation, using fallback")
		// Fallback to original method if not enough data for ATR
		return t.calculateFixedTPSL(entryPrice, positionSide)
	}

	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, t.Config.ATRPeriod)
	t.Logger.Debug("Calculated ATR(%d): %.4f for dynamic TP/SL calculation", t.Config.ATRPeriod, atr)

	// Calculate TP and SL based on ATR multipliers
	tpDistance := atr * t.Config.ATRMultiplierTP
	slDistance := atr * t.Config.ATRMultiplierSL

	// Set stop loss and take profit based on position side
	if positionSide == "LONG" {
		takeProfit = entryPrice + tpDistance
		stopLoss = entryPrice - slDistance
	} else {
		takeProfit = entryPrice - tpDistance
		stopLoss = entryPrice + slDistance
	}

	// Validate the ratio
	tpDistanceActual := math.Abs(takeProfit - entryPrice)
	slDistanceActual := math.Abs(entryPrice - stopLoss)

	ratio := 0.0
	if slDistanceActual > 0 {
		ratio = tpDistanceActual / slDistanceActual
	}

	t.Logger.Debug("Dynamic ATR-based TP/SL calculation - Entry: %.2f, TP: %.2f, SL: %.2f, ATR: %.4f, TP Dist: %.4f, SL Dist: %.4f, Ratio: %.2f:1",
		entryPrice, takeProfit, stopLoss, atr, tpDistanceActual, slDistanceActual, ratio)

	return takeProfit, stopLoss
}

// calculateTPBasedOn15MinProjection calculates TP based on expected price movement in next 15 minutes
func (t *Trader) calculateTPBasedOn15MinProjection(entryPrice float64, positionSide string) float64 {
	// Get recent price data for 15-minute projection
	if len(t.State.Closes) < 15 {
		t.Logger.Warning("Not enough data for 15-minute projection, using fallback")
		// Fallback to simple percentage-based TP
		const defaultTPPerc = 0.005 // 0.5%
		if positionSide == "LONG" {
			return entryPrice * (1 + defaultTPPerc)
		}
		return entryPrice * (1 - defaultTPPerc)
	}
	
	// Use the last 15 minutes of data (15 one-minute candles)
	recentCloses := t.State.Closes
	if len(recentCloses) > 15 {
		recentCloses = recentCloses[len(recentCloses)-15:]
	}
	
	// Calculate average price movement per minute
	priceChanges := make([]float64, len(recentCloses)-1)
	for i := 1; i < len(recentCloses); i++ {
		priceChanges[i-1] = recentCloses[i] - recentCloses[i-1]
	}
	
	// Calculate average movement per minute
	avgMovementPerMin := 0.0
	for _, change := range priceChanges {
		avgMovementPerMin += change
	}
	avgMovementPerMin /= float64(len(priceChanges))
	
	// Project movement for 15 minutes
	projectedMovement := avgMovementPerMin * 15
	
	// Apply momentum factor to make projections more realistic
	momentumFactor := 0.7 // Reduce projection to account for mean reversion
	projectedMovement *= momentumFactor
	
	// Calculate TP based on position side
	var tpPrice float64
	if positionSide == "LONG" {
		tpPrice = entryPrice + projectedMovement
	} else {
		tpPrice = entryPrice + projectedMovement // Note: for SHORT, negative movement means price goes down
	}
	
	// Ensure TP is reasonable (at least 0.1% away from entry)
	minTPDistance := entryPrice * 0.001
	actualTPDistance := math.Abs(tpPrice - entryPrice)
	
	if actualTPDistance < minTPDistance {
		if positionSide == "LONG" {
			tpPrice = entryPrice * (1 + 0.001) // 0.1% above entry
		} else {
			tpPrice = entryPrice * (1 - 0.001) // 0.1% below entry
		}
	}
	
	t.Logger.Debug("15-minute TP projection - Entry: %.2f, Avg movement/minute: %.4f, Projected movement: %.4f, TP: %.2f", 
		entryPrice, avgMovementPerMin, projectedMovement, tpPrice)
	
	return tpPrice
}

// adjustTPSL adjusts TP/SL for long positions with 2:1 ratio
func (t *Trader) adjustTPSL(closePrice float64) {
	t.Logger.Info("Adjusting TP/SL for LONG position, current price: %.2f", closePrice)

	exists, side, _, curTP, _ := t.PositionManager.HasOpenPosition()
	if !exists || t.PositionManager.NormalizeSide(side) != "LONG" {
		t.Logger.Info("No LONG position found, skipping TP/SL adjustment")
		return
	}
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		t.Logger.Error("Could not get entry price, skipping TP/SL adjustment")
		return
	}

	// If not using dynamic risk management, we can implement trailing functionality here
	// Otherwise, let SyncPositionRealTime handle trailing based on dynamic risk management
	var newTP, newSL float64
	if !t.Config.UseDynamicRiskManagement {
		// Original logic with ATR-based trailing TP
		atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
		t.Logger.Debug("ATR(14) calculated for trailing stop: %.4f", atr)

		newTP = closePrice + atr*1.5
		if newTP <= curTP {
			t.Logger.Info("New TP %.2f is not better than current TP %.2f, skipping update", newTP, curTP)
			return
		}
		// Calculate new SL maintaining the 2:1 ratio with the new TP
		if entry != 0 {
			newSL = entry - (newTP - entry)/2 // 2:1 ratio
		} else {
			newTP, newSL = t.calculateTPSLWithRatio(entry, "LONG")
		}
	} else {
		// When using dynamic risk management, recalculate levels based on entry price only
		newTP, newSL = t.calculateTPSLWithRatio(entry, "LONG")
	}

	t.Logger.Info("Sending TP/SL update to exchange: TP %.2f, SL %.2f", newTP, newSL)
	if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, newTP, newSL); err != nil {
		t.Logger.Error("Error updating TP/SL: %v", err)
	} else {
		t.Logger.Info("TP/SL updated ▶ TP %.2f  SL %.2f", newTP, newSL)
	}
}

// adjustTPSLForShort adjusts TP/SL for short positions with 2:1 ratio
func (t *Trader) adjustTPSLForShort(closePrice float64) {
	t.Logger.Info("Adjusting TP/SL for SHORT position, current price: %.2f", closePrice)

	exists, side, _, curTP, _ := t.PositionManager.HasOpenPosition()
	if !exists || side != "SHORT" {
		t.Logger.Info("No SHORT position found, skipping TP/SL adjustment")
		return
	}
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		t.Logger.Error("Could not get entry price, skipping TP/SL adjustment")
		return
	}

	// If not using dynamic risk management, we can implement trailing functionality here
	// Otherwise, let SyncPositionRealTime handle trailing based on dynamic risk management
	var newTP, newSL float64
	if !t.Config.UseDynamicRiskManagement {
		newTP = closePrice * 0.998
		if newTP >= curTP*1.001 { // Protection against small changes
			t.Logger.Info("New TP %.2f is not better than current TP %.2f, skipping update", newTP, curTP)
			return
		}
		// Calculate new SL maintaining the 2:1 ratio with the new TP
		if entry != 0 {
			newSL = entry + (entry - newTP)/2 // 2:1 ratio for SHORT
		} else {
			newTP, newSL = t.calculateTPSLWithRatio(entry, "SHORT")
		}
	} else {
		// When using dynamic risk management, recalculate levels based on entry price only
		newTP, newSL = t.calculateTPSLWithRatio(entry, "SHORT")
	}

	t.Logger.Info("Sending TP/SL update to exchange: TP %.2f, SL %.2f", newTP, newSL)
	if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, newTP, newSL); err != nil {
		t.Logger.Error("adjustTPSLForShort error: %v", err)
	} else {
		t.Logger.Info("Recalculated TP/SL ▶ TP %.2f SL %.2f", newTP, newSL)
	}
}

// SMAMovingAverageWorker generates signals based on SMA, MACD and other indicators
func (t *Trader) SMAMovingAverageWorker() {
	t.Logger.Info("Starting SMA Moving Average Worker...")

	for range time.Tick(1 * time.Second) {
		if len(t.State.Closes) < t.Config.SmaLen {
			continue
		}

		closesCopy := append([]float64(nil), t.State.Closes...)
		cls := closesCopy[len(closesCopy)-1]

		// Calculate SMA
		smaVal := indicators.SMA(closesCopy)
		t.Logger.Debug("SMA(%d) calculated: %.2f, Current close: %.2f", t.Config.SmaLen, smaVal, cls)

		// Calculate RSI
		rsiValues := indicators.RSI(closesCopy, 14)
		var rsi float64
		if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
			rsi = rsiValues[len(rsiValues)-1]
		}
		t.Logger.Debug("RSI(14) calculated: %.2f", rsi)

		// Calculate MACD
		macdLine, signalLine := indicators.MACD(closesCopy)
		var macdHist float64
		if macdLine != 0 {
			macdHist = macdLine - signalLine
		}
		t.Logger.Debug("MACD calculated - Line: %.4f, Signal: %.4f, Histogram: %.4f", macdLine, signalLine, macdHist)

		// Calculate ATR
		atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
		t.Logger.Debug("ATR(14) calculated: %.4f", atr)

		// Calculate Bollinger Bands
		bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(closesCopy, 20, 2.0)
		t.Logger.Debug("Bollinger Bands calculated - Upper: %.2f, Middle: %.2f, Lower: %.2f", bbUpper, bbMiddle, bbLower)

		hysteresis := 0.005 // 0.5%
		if t.State.MarketRegime == "trend" {
			hysteresis = 0.01 // Wider hysteresis in trend
		}
		t.Logger.Debug("Current market regime: %s, hysteresis: %.3f", t.State.MarketRegime, hysteresis)

		// Generate consolidated signals with weights
		if t.Config.UseSignalConsolidation {
			t.generateConsolidatedSignals(closesCopy, cls, smaVal, macdHist, macdLine, signalLine, bbUpper, bbLower, hysteresis)
		} else {
			// Original logic for backward compatibility
			t.generateOriginalSignals(closesCopy, cls, smaVal, macdHist, macdLine, signalLine, bbUpper, bbLower, hysteresis)
		}
	}
}

// generateConsolidatedSignals generates signals using weighted consolidation approach
func (t *Trader) generateConsolidatedSignals(closesCopy []float64, cls, smaVal, macdHist, macdLine, signalLine, bbUpper, bbLower, hysteresis float64) {
	// Identify all possible signals with their weights
	signalDetails := []models.SignalDetails{}
	var totalWeight int

	// Primary signal: SMA crossing
	if cls < smaVal*(1-hysteresis) {
		t.Logger.Debug("Primary LONG signal: Close %.2f < SMA(%.2f) * (1-%.3f) = %.2f",
			cls, smaVal, hysteresis, smaVal*(1-hysteresis))
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "SMA_LONG",
			Weight: t.Config.PrimarySignalWeight,
			Value: cls,
		})
		totalWeight += t.Config.PrimarySignalWeight
	} else if cls > smaVal*(1+hysteresis) {
		t.Logger.Debug("Primary SHORT signal: Close %.2f > SMA(%.2f) * (1+%.3f) = %.2f",
			cls, smaVal, hysteresis, smaVal*(1+hysteresis))
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "SMA_SHORT",
			Weight: t.Config.PrimarySignalWeight,
			Value: cls,
		})
		totalWeight += t.Config.PrimarySignalWeight
	}

	// Secondary signals: MACD confirmation
	if macdHist > 0 && macdLine > signalLine { // Bullish MACD
		t.Logger.Debug("Secondary LONG signal: Histogram(%.4f) > 0 and MACD(%.4f) > Signal(%.4f)",
			macdHist, macdLine, signalLine)
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "MACD_LONG",
			Weight: t.Config.SecondarySignalWeight,
			Value: macdHist,
		})
		totalWeight += t.Config.SecondarySignalWeight
	} else if macdHist < 0 && macdLine < signalLine { // Bearish MACD
		t.Logger.Debug("Secondary SHORT signal: Histogram(%.4f) < 0 and MACD(%.4f) < Signal(%.4f)",
			macdHist, macdLine, signalLine)
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "MACD_SHORT",
			Weight: t.Config.SecondarySignalWeight,
			Value: macdHist,
		})
		totalWeight += t.Config.SecondarySignalWeight
	}

	// Tertiary signals: Golden cross (only for LONG)
	if indicators.GoldenCross(closesCopy) {
		t.Logger.Debug("Tertiary LONG signal: Golden cross detected")
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "GOLDEN_CROSS_LONG",
			Weight: t.Config.TertiarySignalWeight,
			Value: 1.0, // Binary indicator
		})
		totalWeight += t.Config.TertiarySignalWeight
	}

	// Quaternary signals: Bollinger Bands (price touching bands for potential reversals)
	// These will be filtered based on market regime
	if cls <= bbLower { // Price touching or below lower band - potential LONG signal
		t.Logger.Debug("Quaternary LONG signal: Close %.2f <= Bollinger Lower Band %.2f", cls, bbLower)
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "BB_LONG",
			Weight: t.Config.QuaternarySignalWeight,
			Value: cls,
		})
		totalWeight += t.Config.QuaternarySignalWeight
	} else if cls >= bbUpper { // Price touching or above upper band - potential SHORT signal
		t.Logger.Debug("Quaternary SHORT signal: Close %.2f >= Bollinger Upper Band %.2f", cls, bbUpper)
		signalDetails = append(signalDetails, models.SignalDetails{
			Kind: "BB_SHORT",
			Weight: t.Config.QuaternarySignalWeight,
			Value: cls,
		})
		totalWeight += t.Config.QuaternarySignalWeight
	}

	// Apply regime-based strategy if enabled
	if t.Config.UseRegimeBasedStrategy {
		signalDetails = t.filterSignalsByRegime(signalDetails)
	}

	// Apply higher-order trend filter if enabled
	if t.Config.UseHigherTrendFilter {
		signalDetails = t.filterSignalsByHigherTrend(signalDetails)
	}

	// Determine overall signal direction based on weights after filtering
	longWeight := 0
	shortWeight := 0

	for _, signal := range signalDetails {
		if signal.Kind == "SMA_LONG" || signal.Kind == "MACD_LONG" || signal.Kind == "GOLDEN_CROSS_LONG" || signal.Kind == "BB_LONG" {
			longWeight += signal.Weight
		} else if signal.Kind == "SMA_SHORT" || signal.Kind == "MACD_SHORT" || signal.Kind == "BB_SHORT" {
			shortWeight += signal.Weight
		}
	}

	// Recalculate total weight after filtering
	totalWeight = longWeight + shortWeight

	// Send consolidated signal based on weighted direction
	if totalWeight >= t.Config.SignalThreshold {
		var overallSignal string
		if longWeight > shortWeight {
			overallSignal = "LONG"
		} else if shortWeight > longWeight {
			overallSignal = "SHORT"
		} else {
			// If weights are equal, skip signal
			return
		}

		t.Logger.Info("Consolidated %s signal generated - Total Weight: %d, Long Weight: %d, Short Weight: %d, Close: %.2f",
			overallSignal, totalWeight, longWeight, shortWeight, cls)

		consolidatedSignal := models.ConsolidatedSignal{
			Kind:           overallSignal,
			ClosePrice:     cls,
			Time:           time.Now(),
			SignalDetails:  signalDetails,
			TotalWeight:    totalWeight,
			SignalSource:   "consolidated",
		}

		t.State.ConsolidatedSigChan <- consolidatedSignal
	}
}

// filterSignalsByRegime applies different weights or filters signals based on market regime
func (t *Trader) filterSignalsByRegime(signalDetails []models.SignalDetails) []models.SignalDetails {
	if t.State.MarketRegime == "trend" {
		// In trending regime, emphasize trend-following signals and de-emphasize counter-trend signals
		// Increase weight for SMA and MACD signals
		// Reduce weight for Bollinger Band signals (which may indicate counter-trend opportunities)
		filteredDetails := make([]models.SignalDetails, 0, len(signalDetails))

		for _, signal := range signalDetails {
			switch signal.Kind {
			case "BB_LONG", "BB_SHORT":
				// Reduce weight of Bollinger Band signals in trending markets
				adjustedSignal := signal
				adjustedSignal.Weight = max(1, signal.Weight/2) // Reduce to at least 1
				filteredDetails = append(filteredDetails, adjustedSignal)
			case "SMA_LONG", "SMA_SHORT", "MACD_LONG", "MACD_SHORT":
				// Keep trend-following signals as they are
				filteredDetails = append(filteredDetails, signal)
			case "GOLDEN_CROSS_LONG":
				// Golden cross is a trend-following signal, keep as is
				filteredDetails = append(filteredDetails, signal)
			default:
				filteredDetails = append(filteredDetails, signal)
			}
		}

		return filteredDetails
	} else if t.State.MarketRegime == "range" {
		// In ranging regime, emphasize counter-trend signals and de-emphasize trend-following signals
		// Increase weight for Bollinger Band signals (which indicate reversal opportunities)
		// Reduce weight for SMA and MACD signals which may generate false signals in ranges
		filteredDetails := make([]models.SignalDetails, 0, len(signalDetails))

		for _, signal := range signalDetails {
			switch signal.Kind {
			case "BB_LONG", "BB_SHORT":
				// Increase weight of Bollinger Band signals in ranging markets
				adjustedSignal := signal
				adjustedSignal.Weight = signal.Weight * 2
				filteredDetails = append(filteredDetails, adjustedSignal)
			case "SMA_LONG", "SMA_SHORT", "MACD_LONG", "MACD_SHORT":
				// Reduce weight of trend-following signals in ranging markets
				adjustedSignal := signal
				adjustedSignal.Weight = max(1, signal.Weight/2) // Reduce to at least 1
				filteredDetails = append(filteredDetails, adjustedSignal)
			case "GOLDEN_CROSS_LONG":
				// Reduce weight of golden cross in ranging markets
				adjustedSignal := signal
				adjustedSignal.Weight = max(1, signal.Weight/2) // Reduce to at least 1
				filteredDetails = append(filteredDetails, adjustedSignal)
			default:
				filteredDetails = append(filteredDetails, signal)
			}
		}

		return filteredDetails
	}

	// If regime is unknown, return original signals
	return signalDetails
}

// filterSignalsByHigherTrend filters signals based on higher-order trend direction
func (t *Trader) filterSignalsByHigherTrend(signalDetails []models.SignalDetails) []models.SignalDetails {
	if t.State.HigherTrend == "up" {
		// Only allow LONG signals when higher trend is up
		filteredDetails := make([]models.SignalDetails, 0, len(signalDetails))

		for _, signal := range signalDetails {
			if signal.Kind == "SMA_LONG" || signal.Kind == "MACD_LONG" || signal.Kind == "GOLDEN_CROSS_LONG" || signal.Kind == "BB_LONG" {
				filteredDetails = append(filteredDetails, signal)
			} else {
				t.Logger.Debug("Filtered out SHORT signal %s due to higher trend being UP", signal.Kind)
			}
		}

		return filteredDetails
	} else if t.State.HigherTrend == "down" {
		// Only allow SHORT signals when higher trend is down
		filteredDetails := make([]models.SignalDetails, 0, len(signalDetails))

		for _, signal := range signalDetails {
			if signal.Kind == "SMA_SHORT" || signal.Kind == "MACD_SHORT" || signal.Kind == "BB_SHORT" {
				filteredDetails = append(filteredDetails, signal)
			} else {
				t.Logger.Debug("Filtered out LONG signal %s due to higher trend being DOWN", signal.Kind)
			}
		}

		return filteredDetails
	}

	// If higher trend is neutral or unknown, return original signals
	return signalDetails
}

// Helper function to return maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// calculateDynamicOrderbookThreshold calculates threshold based on current market volatility
func (t *Trader) calculateDynamicOrderbookThreshold() float64 {
	if !t.Config.UseDynamicOrderbookFilter {
		return t.Config.OrderbookStrengthThreshold
	}

	// Calculate current volatility based on recent price movements
	if len(t.State.Closes) < 20 {
		return t.Config.BaseOrderbookThreshold
	}

	recentCloses := t.State.Closes
	if len(recentCloses) > 100 {
		recentCloses = recentCloses[len(recentCloses)-100:]
	}

	// Calculate volatility as coefficient of variation
	mean := 0.0
	for _, c := range recentCloses {
		mean += c
	}
	mean /= float64(len(recentCloses))

	var variance float64
	for _, c := range recentCloses {
		variance += (c - mean) * (c - mean)
	}
	variance /= float64(len(recentCloses))
	stdDev := math.Sqrt(variance)

	// Calculate coefficient of variation (CV) as a percentage
	cv := 0.0
	if mean != 0 {
		cv = math.Abs(stdDev/mean) * 100
	}

	// Adjust threshold based on volatility level
	if cv >= t.Config.MaxVolatilityThreshold {
		// High volatility market - require stronger orderbook confirmation
		return t.Config.HighVolatilityThreshold
	} else if cv <= t.Config.MinVolatilityThreshold {
		// Low volatility market - allow lower threshold
		return t.Config.LowVolatilityThreshold
	} else {
		// Interpolate threshold between low and base based on volatility
		if cv < (t.Config.MinVolatilityThreshold+t.Config.MaxVolatilityThreshold)/2 {
			// Between low volatility and mid-point
			ratio := (cv - t.Config.MinVolatilityThreshold) /
			         ((t.Config.MinVolatilityThreshold+t.Config.MaxVolatilityThreshold)/2 - t.Config.MinVolatilityThreshold)
			return t.Config.LowVolatilityThreshold + ratio*(t.Config.BaseOrderbookThreshold-t.Config.LowVolatilityThreshold)
		} else {
			// Between mid-point and high volatility
			ratio := (cv - (t.Config.MinVolatilityThreshold+t.Config.MaxVolatilityThreshold)/2) /
			         (t.Config.MaxVolatilityThreshold - (t.Config.MinVolatilityThreshold+t.Config.MaxVolatilityThreshold)/2)
			return t.Config.BaseOrderbookThreshold + ratio*(t.Config.HighVolatilityThreshold-t.Config.BaseOrderbookThreshold)
		}
	}
}

// calculateAverageVolume calculates the average volume from recent periods
func (t *Trader) calculateAverageVolume() float64 {
	if len(t.State.RecentVolumes) == 0 {
		return 0
	}

	sum := 0.0
	for _, vol := range t.State.RecentVolumes {
		sum += vol
	}
	return sum / float64(len(t.State.RecentVolumes))
}

// detectVolumeSpike checks if current volume exceeds average by the required multiplier
func (t *Trader) detectVolumeSpike(currentVolume float64) bool {
	if len(t.State.RecentVolumes) == 0 {
		// If no historical data, assume no spike
		return currentVolume > t.Config.MinVolumeThreshold
	}

	avgVolume := t.calculateAverageVolume()
	if avgVolume == 0 {
		return currentVolume > t.Config.MinVolumeThreshold
	}

	return currentVolume > avgVolume*t.Config.VolumeSpikeMultiplier &&
		   currentVolume > t.Config.MinVolumeThreshold
}

// generateOriginalSignals maintains the original logic for backward compatibility
func (t *Trader) generateOriginalSignals(closesCopy []float64, cls, smaVal, macdHist, macdLine, signalLine, bbUpper, bbLower, hysteresis float64) {
	// Generate signals based on multiple indicators with priority (RSI temporarily disabled)
	// Primary: SMA, Secondary: MACD, Tertiary: Golden Cross, Quaternary: Bollinger Bands
	longSignal := false
	shortSignal := false

	// Primary signal: SMA crossing without RSI confirmation (RSI temporarily disabled)
	if cls < smaVal*(1-hysteresis) {
		t.Logger.Debug("Primary LONG signal: Close %.2f < SMA(%.2f) * (1-%.3f) = %.2f",
			cls, smaVal, hysteresis, smaVal*(1-hysteresis))
		longSignal = true
	} else if cls > smaVal*(1+hysteresis) {
		t.Logger.Debug("Primary SHORT signal: Close %.2f > SMA(%.2f) * (1+%.3f) = %.2f",
			cls, smaVal, hysteresis, smaVal*(1+hysteresis))
		shortSignal = true
	}

	// Secondary signals: MACD confirmation
	if !longSignal && !shortSignal {
		if macdHist > 0 && macdLine > signalLine { // Bullish MACD
			t.Logger.Debug("Secondary LONG signal: Histogram(%.4f) > 0 and MACD(%.4f) > Signal(%.4f)",
				macdHist, macdLine, signalLine)
			longSignal = true
		} else if macdHist < 0 && macdLine < signalLine { // Bearish MACD
			t.Logger.Debug("Secondary SHORT signal: Histogram(%.4f) < 0 and MACD(%.4f) < Signal(%.4f)",
				macdHist, macdLine, signalLine)
			shortSignal = true
		}
	}

	// Tertiary signals: Golden cross (only for LONG) without RSI
	if !longSignal && !shortSignal {
		if indicators.GoldenCross(closesCopy) {
			t.Logger.Debug("Tertiary LONG signal: Golden cross detected")
			longSignal = true
		}
	}

	// Quaternary signals: Bollinger Bands (price touching bands for potential reversals)
	if !longSignal && !shortSignal {
		if cls <= bbLower { // Price touching or below lower band - potential LONG signal
			t.Logger.Debug("Quaternary LONG signal: Close %.2f <= Bollinger Lower Band %.2f", cls, bbLower)
			longSignal = true
		} else if cls >= bbUpper { // Price touching or above upper band - potential SHORT signal
			t.Logger.Debug("Quaternary SHORT signal: Close %.2f >= Bollinger Upper Band %.2f", cls, bbUpper)
			shortSignal = true
		}
	}

	// Send only one signal per cycle to prevent conflicts
	if longSignal {
		t.Logger.Info("LONG signal generated - Close: %.2f", cls)
		t.State.SigChan <- models.Signal{
			Kind:       "SMA_LONG",
			ClosePrice: cls,
			Time:       time.Now(),
		}
	} else if shortSignal {
		t.Logger.Info("SHORT signal generated - Close: %.2f", cls)
		t.State.SigChan <- models.Signal{
			Kind:       "SMA_SHORT",
			ClosePrice: cls,
			Time:       time.Now(),
		}
	}
}

// closeOppositePosition closes an existing position before opening a new one in the opposite direction
func (t *Trader) closeOppositePosition(newSide string) {
	exists, side, qty, _, _ := t.PositionManager.HasOpenPosition()
	if !exists {
		return
	}
	
	side = t.PositionManager.NormalizeSide(side)
	newSide = t.PositionManager.NormalizeSide(newSide)
	
	// Only close if it's truly the opposite side
	if side != "" && side != newSide {
		t.Logger.Info("Closing opposite position: %s", side)
		
		// Get entry price for profit calculation
		entryPrice := t.PositionManager.GetLastEntryPrice()
		
		reduceSide := "Sell"
		if side == "SHORT" {
			reduceSide = "Buy"
		}
		
		if err := t.OrderManager.PlaceOrderMarket(reduceSide, qty, true); err != nil {
			t.Logger.Error("Error closing position %s: %v", side, err)
			return
		}
		
		t.Logger.Info("Successfully sent order to close position %s", side)
		
		// Calculate profit based on current price and entry price
		var exitPrice float64
		if side == "LONG" {
			// For LONG positions, we sell to close
			exitPrice = t.PositionManager.GetLastBidPrice()
		} else if side == "SHORT" {
			// For SHORT positions, we buy to close
			exitPrice = t.PositionManager.GetLastAskPrice()
		}
		
		if exitPrice <= 0 {
			exitPrice = entryPrice // Fallback if we can't get current price
		}
		
		profit := t.PositionManager.CalculatePositionProfit(side, entryPrice, exitPrice, qty)
		signalType := "CLOSE_LONG"
		if side == "SHORT" {
			signalType = "CLOSE_SHORT"
		}
		
		t.PositionManager.UpdateSignalStats(signalType, profit)
	}

	// Reset partial profit tracking when closing any position
	t.State.PartialTPTriggered = false
	t.State.PartialTPPrice = 0.0
}

// Trader processes signals and executes trades
func (t *Trader) Trader() {
	if t.Config.UseSignalConsolidation {
		t.processConsolidatedSignals()
	} else {
		t.processOriginalSignals()
	}
}

// processConsolidatedSignals handles the new weighted/consolidated signals
func (t *Trader) processConsolidatedSignals() {
	lastSignal := ""

	for consolidatedSig := range t.State.ConsolidatedSigChan {
		// Prevent consecutive same signals
		if string(consolidatedSig.Kind) == lastSignal {
			if t.Config.Debug {
				t.Logger.Debug("Skipping duplicate consolidated signal: %s", consolidatedSig.Kind)
			}
			continue
		}

		// Check orderbook strength as additional confirmation
		if !t.CheckOrderbookStrength(consolidatedSig.Kind) {
			if t.Config.Debug {
				t.Logger.Debug("Orderbook strength check failed for %s signal, skipping", consolidatedSig.Kind)
			}
			continue
		}

		t.Logger.Info("Processing consolidated %s signal with total weight: %d, component signals: %d",
			consolidatedSig.Kind, consolidatedSig.TotalWeight, len(consolidatedSig.SignalDetails))

		// Log details of each contributing signal
		for _, signalDetail := range consolidatedSig.SignalDetails {
			t.Logger.Debug("  - %s (weight: %d, value: %.4f)", signalDetail.Kind, signalDetail.Weight, signalDetail.Value)
		}

		// Process the consolidated signal
		if consolidatedSig.Kind == "LONG" {
			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "SHORT" {
				t.Logger.Info("Closing existing SHORT position before opening LONG")
				t.closeOppositePosition("LONG")
			}

			t.HandleLongSignal(consolidatedSig.ClosePrice)
			lastSignal = "LONG"
		} else if consolidatedSig.Kind == "SHORT" {
			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "LONG" {
				t.Logger.Info("Closing existing LONG position before opening SHORT")
				t.closeOppositePosition("SHORT")
			}

			t.HandleShortSignal(consolidatedSig.ClosePrice)
			lastSignal = "SHORT"
		}
	}
}

// processOriginalSignals handles the original signal processing for backward compatibility
func (t *Trader) processOriginalSignals() {
	signalStrength := make(map[string]int)
	lastSignal := ""

	for sig := range t.State.SigChan {
		signalStrength[sig.Kind]++

		// Prevent consecutive same signals
		if sig.Kind == lastSignal {
			if t.Config.Debug {
				t.Logger.Debug("Skipping duplicate signal: %s", sig.Kind)
			}
			continue
		}

		if t.Config.Debug {
			t.Logger.Debug("Signal strength: %v", signalStrength)
		}

		// Process LONG signal with proper conflict resolution
		if sig.Kind == "SMA_LONG" && signalStrength["SMA_LONG"] >= t.Config.SignalStrengthThreshold && t.CheckOrderbookStrength("LONG") {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed LONG signal: %d indicators", signalStrength["SMA_LONG"])
			}

			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "SHORT" {
				t.Logger.Info("Closing existing SHORT position before opening LONG")
				t.closeOppositePosition("LONG")
			}

			t.HandleLongSignal(sig.ClosePrice)
			lastSignal = "SMA_LONG"
			t.resetSignalStrength(&signalStrength)
		} else if sig.Kind == "SMA_SHORT" && signalStrength["SMA_SHORT"] >= t.Config.SignalStrengthThreshold && t.CheckOrderbookStrength("SHORT") {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed SHORT signal: %d indicators", signalStrength["SMA_SHORT"])
			}

			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "LONG" {
				t.Logger.Info("Closing existing LONG position before opening SHORT")
				t.closeOppositePosition("SHORT")
			}

			t.HandleShortSignal(sig.ClosePrice)
			lastSignal = "SMA_SHORT"
			t.resetSignalStrength(&signalStrength)
		}
	}
}

func (t *Trader) resetSignalStrength(m *map[string]int) {
	for k := range *m {
		delete(*m, k)
	}
}

// SyncPositionRealTime implements trailing stop logic and partial profit taking
func (t *Trader) SyncPositionRealTime() {
	t.Logger.Info("Starting trailing stop logic and partial profit management...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Current position
		exists, side, qty, tp, sl := t.PositionManager.HasOpenPosition()
		if !exists || tp == 0 {
			continue
		}
		entry := t.PositionManager.GetLastEntryPrice()
		if entry == 0 {
			continue
		}

		// Current price
		var price float64
		if side == "LONG" {
			price = t.PositionManager.GetLastBidPrice()
		} else {
			price = t.PositionManager.GetLastAskPrice()
		}
		if price == 0 {
			continue
		}

		// Check if we should take partial profits if enabled
		if t.Config.UsePartialProfitTaking && !t.State.PartialTPTriggered {
			shouldTakePartial := false
			partialPrice := 0.0

			if side == "LONG" && price >= tp {
				shouldTakePartial = true
				partialPrice = price
			} else if side == "SHORT" && price <= tp {
				shouldTakePartial = true
				partialPrice = price
			}

			if shouldTakePartial {
				t.takePartialProfit(side, qty, partialPrice)
				// Mark that partial profit has been taken
				t.State.PartialTPTriggered = true
				t.State.PartialTPPrice = partialPrice
				t.Logger.Info("Partial profit taken: %s position at price %.2f", side, partialPrice)
			}
		}

		// Calculate ATR for trailing stop if dynamic risk management is enabled
		currentATR := 0.0
		if t.Config.UseDynamicRiskManagement && len(t.State.Closes) >= t.Config.ATRPeriod {
			currentATR = indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, t.Config.ATRPeriod)
		}

		// Enhanced trailing stop logic
		var newStopLoss float64
		if t.Config.UseDynamicRiskManagement && currentATR > 0 {
			// Use ATR-based trailing stop
			atrDistance := currentATR * t.Config.TrailingStopATRMultiplier

			if side == "LONG" {
				// For LONG positions, trailing stop is below current price but above entry
				newStopLoss = price - atrDistance
				// Ensure the trailing stop doesn't go below the original stop loss or entry price
				if newStopLoss < sl {
					newStopLoss = sl
				}
				if newStopLoss < entry {
					newStopLoss = entry - (sl - entry) // Maintain original risk distance from entry
				}
			} else if side == "SHORT" {
				// For SHORT positions, trailing stop is above current price but below entry
				newStopLoss = price + atrDistance
				// Ensure the trailing stop doesn't go above the original stop loss or entry price
				if newStopLoss > sl {
					newStopLoss = sl
				}
				if newStopLoss > entry {
					newStopLoss = entry + (entry - sl) // Maintain original risk distance from entry
				}
			}
		} else {
			// Original trailing stop logic (maintaining 2:1 ratio concept but adjusted as price moves)
			var dist, prog float64
			if side == "LONG" {
				dist = tp - entry
				prog = (price - entry) / dist
			} else {
				dist = entry - tp
				prog = (entry - price) / dist
			}

			if prog <= 0 {
				continue
			}

			// Target SL: half of the way from entry to TP
			newStopLoss = entry + prog*dist*0.5

			// Ensure the new stop loss is in the correct position relative to current price and original SL
			if side == "LONG" && (newStopLoss > sl || newStopLoss > price) {
				continue // Don't move stop loss if it's not an improvement
			} else if side == "SHORT" && (newStopLoss < sl || newStopLoss < price) {
				continue // Don't move stop loss if it's not an improvement
			}
		}

		// Update stop-loss if improved
		needUpdate := false
		if side == "LONG" && newStopLoss > sl {
			needUpdate = true
		} else if side == "SHORT" && newStopLoss < sl {
			needUpdate = true
		}

		if needUpdate {
			t.Logger.Info("Trailing stop: updating SL from %.2f to %.2f", sl, newStopLoss)

			// Update stop-loss only, keep TP unchanged
			if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tp, newStopLoss); err != nil {
				t.Logger.Error("Trailing SL update error: %v", err)
			} else {
				t.Logger.Info("SL → %.2f (trailing)", newStopLoss)
			}
		}
	}
}

// takePartialProfit handles partial profit taking by reducing position size
func (t *Trader) takePartialProfit(positionSide string, totalQty float64, currentPrice float64) {
	if !t.Config.UsePartialProfitTaking {
		return
	}

	partialQty := totalQty * t.Config.PartialProfitPercentage

	if partialQty <= 0 {
		return
	}

	// Calculate how much of the position remains after partial closure
	remainingQty := totalQty - partialQty

	orderSide := "Sell"
	if positionSide == "SHORT" {
		orderSide = "Buy"
	}

	t.Logger.Info("Taking partial profit: closing %.4f of %.4f total quantity", partialQty, totalQty)

	// Place order for partial quantity
	if err := t.OrderManager.PlaceOrderMarket(orderSide, partialQty, true); err != nil {
		t.Logger.Error("Error taking partial profit: %v", err)
		return
	}

	// Get the actual entry price for this part of the position
	entryPrice := t.PositionManager.GetLastEntryPrice()
	if entryPrice == 0 {
		entryPrice = currentPrice // fallback
	}

	// Calculate profit for the partial closure
	realExitPrice := currentPrice
	if positionSide == "LONG" {
		realExitPrice = t.PositionManager.GetLastBidPrice()
		if realExitPrice <= 0 {
			realExitPrice = currentPrice
		}
	} else if positionSide == "SHORT" {
		realExitPrice = t.PositionManager.GetLastAskPrice()
		if realExitPrice <= 0 {
			realExitPrice = currentPrice
		}
	}

	partialProfit := t.PositionManager.CalculatePositionProfit(positionSide, entryPrice, realExitPrice, partialQty)
	t.PositionManager.UpdateSignalStats("PARTIAL_"+positionSide, partialProfit)

	t.Logger.Info("Partial profit taken: %.4f units at %.2f, estimated profit: %.2f",
		partialQty, realExitPrice, partialProfit)

	// If remaining quantity is small, consider closing the entire position
	if remainingQty < t.State.Instr.MinQty {
		t.Logger.Info("Remaining position (%.4f) is below minimum quantity, considering full closure", remainingQty)
	}
}

// OnClosedCandle processes a closed candle
func (t *Trader) OnClosedCandle(closePrice float64) {
	t.State.Closes = append(t.State.Closes, closePrice)
	t.State.Highs = append(t.State.Highs, closePrice)
	t.State.Lows = append(t.State.Lows, closePrice)

	// Add to long-term data as well
	t.State.LongTermCloses = append(t.State.LongTermCloses, closePrice)
	t.State.LongTermHighs = append(t.State.LongTermHighs, closePrice)
	t.State.LongTermLows = append(t.State.LongTermLows, closePrice)

	// For volume tracking, we'll calculate a simple proxy using ATR * price as an approximation
	// In a real implementation, we would have access to actual volume data
	currentATR := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
	approximateVolume := currentATR * closePrice // This is a proxy for volume, not actual volume

	// Add to volume tracking with a reasonable cap
	if approximateVolume > 0 {
		t.State.RecentVolumes = append(t.State.RecentVolumes, approximateVolume)
	}

	if t.Config.Debug {
		t.Logger.Debug("Added price: %.2f, length closes: %d, approximate volume: %.2f", closePrice, len(t.State.Closes), approximateVolume)
	}

	// Log indicator values after adding new candle
	if len(t.State.Closes) >= t.Config.SmaLen {
		smaVal := indicators.SMA(t.State.Closes)
		rsiValues := indicators.RSI(t.State.Closes, 14)
		var rsi float64
		if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
			rsi = rsiValues[len(rsiValues)-1]
		}
		macdLine, _ := indicators.MACD(t.State.Closes)
		bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(t.State.Closes, 20, 2.0)

		t.Logger.Debug("Indicators updated - SMA: %.2f, RSI: %.2f, MACD: %.4f, BB: %.2f/%.2f/%.2f",
			smaVal, rsi, macdLine, bbLower, bbMiddle, bbUpper)
	}

	// Increase maxLen to avoid data truncation
	maxLen := t.Config.SmaLen * 100
	if len(t.State.Closes) > maxLen {
		t.State.Closes = t.State.Closes[len(t.State.Closes)-maxLen:]
		t.State.Highs = t.State.Highs[len(t.State.Highs)-maxLen:]
		t.State.Lows = t.State.Lows[len(t.State.Lows)-maxLen:]
	}

	// Maintain long-term data with appropriate length
	longTermMaxLen := t.Config.HigherTrendPeriod * 10  // Keep 10x the higher trend period
	if len(t.State.LongTermCloses) > longTermMaxLen {
		t.State.LongTermCloses = t.State.LongTermCloses[len(t.State.LongTermCloses)-longTermMaxLen:]
		t.State.LongTermHighs = t.State.LongTermHighs[len(t.State.LongTermHighs)-longTermMaxLen:]
		t.State.LongTermLows = t.State.LongTermLows[len(t.State.LongTermLows)-longTermMaxLen:]
	}

	// Maintain volume data with appropriate length
	volumeMaxLen := 50  // Keep 50 periods of volume data for average calculation
	if len(t.State.RecentVolumes) > volumeMaxLen {
		t.State.RecentVolumes = t.State.RecentVolumes[len(t.State.RecentVolumes)-volumeMaxLen:]
	}
}

// LogSignalStats logs signal statistics
func (t *Trader) LogSignalStats() {
	t.State.SignalStats.Lock()
	total := t.State.SignalStats.Total
	correct := t.State.SignalStats.Correct
	t.State.SignalStats.Unlock()

	if total == 0 {
		t.Logger.Info("Signal stats: No signals")
		return
	}

	accuracy := float64(correct) / float64(total) * 100
	t.Logger.Info("Signal stats: %d/%d (%.1f%%)", correct, total, accuracy)
}