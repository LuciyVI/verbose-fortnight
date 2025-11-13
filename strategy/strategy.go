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
	"verbose-fortnight/strategy/signals"  // Add the signals package import
	"verbose-fortnight/web_interface"  // Add the web interface import
)

// Trader handles trading logic and signal processing
type Trader struct {
	APIClient       *api.RESTClient
	Config          *config.Config
	State           *models.State
	OrderManager    *order.OrderManager
	PositionManager *position.PositionManager
	Logger          logging.LoggerInterface
	WebUI           *web_interface.WebUI  // Add WebUI for broadcasting updates
	
	// New architecture components
	RegimeDetector        *RegimeDetector
	MultiTimeframeIndicators *MultiTimeframeIndicators
	SignalScorer          *SignalScorer
	SignalFilter          *SignalFilter
	SignalQueueManager    *SignalQueueManager
}

// NewTrader creates a new trader instance
func NewTrader(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface, webUI *web_interface.WebUI) *Trader {
	orderManager := order.NewOrderManager(apiClient, cfg, state, logger)
	positionManager := position.NewPositionManager(apiClient, cfg, state, logger)
	
	return &Trader{
		APIClient:       apiClient,
		Config:          cfg,
		State:           state,
		OrderManager:    orderManager,
		PositionManager: positionManager,
		Logger:          logger,
		WebUI:           webUI,
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
			// Calculate ATR-based TP as fallback instead of fixed percentage
			atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, t.Config.AtrPeriod)
			if atr <= 0 {
				// If ATR calculation fails, use a default fallback
				t.Logger.Warning("ATR calculation failed in fallback, using default multiplier")
				atr = 90 // Default value similar to the one in order/order.go
			}
			tpMultiplier := t.Config.TPAtrMultiplier
			tickSize = t.State.Instr.TickSize
			if tickSize <= 0 {
				tickSize = 0.1 // Default fallback
			}
			if job.Side == "LONG" {
				finalTP = math.Round((entryPrice + (atr * tpMultiplier))/tickSize) * tickSize
			} else {
				finalTP = math.Round((entryPrice - (atr * tpMultiplier))/tickSize) * tickSize
			}
			t.Logger.Info("Using ATR-based fallback TP calculation: %.2f", finalTP)
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
	if side == "LONG" && askDepth != 0 {
		ratio = bidDepth / askDepth
		t.Logger.Debug("Orderbook strength for LONG: bid/ask ratio = %.2f, threshold = %.2f", ratio, t.Config.OrderbookStrengthThreshold)
	} else if side == "SHORT" && bidDepth != 0 {
		ratio = askDepth / bidDepth
		t.Logger.Debug("Orderbook strength for SHORT: ask/bid ratio = %.2f, threshold = %.2f", ratio, t.Config.OrderbookStrengthThreshold)
	}

	// Adaptive thresholds based on market regime
	if t.State.MarketRegime == "trend" {
		if side == "LONG" && bidDepth/askDepth > t.Config.OrderbookStrengthThreshold {
			if t.Config.Debug {
				t.Logger.Debug("Trend: LONG signal confirmed")
			}
			return true
		} else if side == "SHORT" && askDepth/bidDepth > t.Config.OrderbookStrengthThreshold {
			if t.Config.Debug {
				t.Logger.Debug("Trend: SHORT signal confirmed")
			}
			return true
		}
	} else if t.State.MarketRegime == "range" {
		if side == "LONG" && bidDepth/askDepth > t.Config.OrderbookStrengthThreshold {
			if t.Config.Debug {
				t.Logger.Debug("Range: LONG signal confirmed")
			}
			return true
		} else if side == "SHORT" && askDepth/bidDepth > t.Config.OrderbookStrengthThreshold {
			if t.Config.Debug {
				t.Logger.Debug("Range: SHORT signal confirmed")
			}
			return true
		}
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
	if rangePerc > 3.0 && trendStrength > 0.5 {
		t.State.MarketRegime = "trend"
		t.Logger.Info("Market regime set to: trend")
	} else if volatilityRegime == "low" && rangePerc < 1.5 {
		t.State.MarketRegime = "range"
		t.Logger.Info("Market regime set to: range")
	} else {
		// If mixed signals, default to trend detection
		if trendStrength > 0.5 {
			t.State.MarketRegime = "trend"
			t.Logger.Info("Market regime set to: trend (based on trend strength)")
		} else {
			t.State.MarketRegime = "range"
			t.Logger.Info("Market regime set to: range (based on trend strength)")
		}
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
}

// calculateTPSLWithRatio calculates TP/SL with ATR-based multipliers
func (t *Trader) calculateTPSLWithRatio(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	// Calculate ATR using the configured period
	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, t.Config.AtrPeriod)
	if atr <= 0 {
		// Fallback to default behavior if ATR calculation fails
		t.Logger.Warning("ATR calculation failed, using fallback 15-minute projection")
		return t.calculateTPSLWithRatioFallback(entryPrice, positionSide)
	}

	t.Logger.Debug("ATR(%d) calculated for TP/SL: %.4f", t.Config.AtrPeriod, atr)

	// Calculate TP and SL based on ATR multipliers
	tpMultiplier := t.Config.TPAtrMultiplier
	slMultiplier := t.Config.SLAtrMultiplier

	if positionSide == "LONG" {
		// For LONG positions: TP above entry, SL below entry
		takeProfit = entryPrice + (atr * tpMultiplier)
		stopLoss = entryPrice - (atr * slMultiplier)
	} else if positionSide == "SHORT" {
		// For SHORT positions: TP below entry, SL above entry
		takeProfit = entryPrice - (atr * tpMultiplier)
		stopLoss = entryPrice + (atr * slMultiplier)
	}

	// Ensure TP and SL are properly rounded to tick size
	if t.State.Instr.TickSize > 0 {
		takeProfit = math.Round(takeProfit/t.State.Instr.TickSize) * t.State.Instr.TickSize
		stopLoss = math.Round(stopLoss/t.State.Instr.TickSize) * t.State.Instr.TickSize
	}

	// Validate that the calculated values make sense
	tpDistanceActual := math.Abs(takeProfit - entryPrice)
	slDistanceActual := math.Abs(entryPrice - stopLoss)

	// Calculate the effective ratio
	ratio := 0.0
	if slDistanceActual > 0 {
		ratio = tpDistanceActual / slDistanceActual
	}

	t.Logger.Debug("ATR-based TP/SL calculation - Entry: %.2f, TP: %.2f, SL: %.2f, TP Dist: %.4f, SL Dist: %.4f, Ratio: %.2f:1 (Target TP: %.2f, SL: %.2f)",
		entryPrice, takeProfit, stopLoss, tpDistanceActual, slDistanceActual, ratio, tpMultiplier, slMultiplier)

	return takeProfit, stopLoss
}

// calculateTPSLWithRatioFallback provides the previous 15-minute projection fallback for compatibility
func (t *Trader) calculateTPSLWithRatioFallback(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
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

	t.Logger.Debug("TP/SL calculation with 2:1 ratio - Entry: %.2f, TP: %.2f, SL: %.2f, TP Distance: %.4f, SL Distance: %.4f, Actual Ratio: %.2f:1",
		entryPrice, takeProfit, stopLoss, tpDistanceActual, slDistanceActual, ratio)

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
	
	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
	t.Logger.Debug("ATR(14) calculated for trailing stop: %.4f", atr)
	
	newTP := closePrice + atr*1.5
	if newTP <= curTP {
		t.Logger.Info("New TP %.2f is not better than current TP %.2f, skipping update", newTP, curTP)
		return
	}
	
	// Calculate TP/SL with 2:1 ratio based on 15-minute projection
	newTP, newSL := t.calculateTPSLWithRatio(entry, "LONG")
	
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
	
	newTP := closePrice * 0.998
	if newTP >= curTP*1.001 { // Protection against small changes
		t.Logger.Info("New TP %.2f is not better than current TP %.2f, skipping update", newTP, curTP)
		return
	}
	
	// Calculate TP/SL with 2:1 ratio based on 15-minute projection
	newTP, newSL := t.calculateTPSLWithRatio(entry, "SHORT")
	
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

		// Generate signals based on multiple indicators with priority (RSI temporarily disabled)
		// Primary: SMA, Secondary: MACD, Tertiary: Golden Cross, Quaternary: Bollinger Bands
		longSignal := false
		shortSignal := false
		
		// Primary signal: SMA crossing without RSI confirmation (RSI temporarily disabled) - INVERTED LOGIC
		if cls > smaVal*(1+hysteresis) {  // Inverted: was <, now >
			t.Logger.Debug("Primary SHORT signal (inverted): Close %.2f > SMA(%.2f) * (1+%.3f) = %.2f", 
				cls, smaVal, hysteresis, smaVal*(1+hysteresis))
			shortSignal = true
		} else if cls < smaVal*(1-hysteresis) {  // Inverted: was >, now <
			t.Logger.Debug("Primary LONG signal (inverted): Close %.2f < SMA(%.2f) * (1-%.3f) = %.2f", 
				cls, smaVal, hysteresis, smaVal*(1-hysteresis))
			longSignal = true
		}
		
		// Secondary signals: MACD confirmation - INVERTED LOGIC
		if !longSignal && !shortSignal {
			if macdHist < 0 && macdLine < signalLine { // Bearish MACD (inverted from bullish)
				t.Logger.Debug("Secondary SHORT signal (inverted from bullish MACD): Histogram(%.4f) < 0 and MACD(%.4f) < Signal(%.4f)", 
					macdHist, macdLine, signalLine)
				shortSignal = true
			} else if macdHist > 0 && macdLine > signalLine { // Bullish MACD (inverted from bearish)
				t.Logger.Debug("Secondary LONG signal (inverted from bearish MACD): Histogram(%.4f) > 0 and MACD(%.4f) > Signal(%.4f)", 
					macdHist, macdLine, signalLine)
				longSignal = true
			}
		}
		
		// Tertiary signals: Death cross (opposite of golden cross) without RSI
		if !longSignal && !shortSignal {
			if indicators.DeathCross(closesCopy) {
				t.Logger.Debug("Tertiary SHORT signal (inverted from golden cross): Death cross detected")
				shortSignal = true
			}
		}

		// Quaternary signals: Bollinger Bands (price touching bands for potential reversals) - INVERTED LOGIC
		if !longSignal && !shortSignal {
			if cls >= bbUpper { // Price touching or above upper band - potential SHORT signal (inverted from LONG)
				t.Logger.Debug("Quaternary SHORT signal (inverted from LONG): Close %.2f >= Bollinger Upper Band %.2f", cls, bbUpper)
				shortSignal = true
			} else if cls <= bbLower { // Price touching or below lower band - potential LONG signal (inverted from SHORT)
				t.Logger.Debug("Quaternary LONG signal (inverted from SHORT): Close %.2f <= Bollinger Lower Band %.2f", cls, bbLower)
				longSignal = true
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
}

// Trader processes signals and executes trades using the new architecture
func (t *Trader) Trader() {
	t.Logger.Info("Starting trader with new signal system...")
	
	for {
		// Check for signals in the queue
		signal, ok := t.SignalQueueManager.GetNextSignal()
		if !ok {
			// No signals in queue, wait a bit before checking again
			time.Sleep(100 * time.Millisecond)
			continue
		}

		// Only process high confidence signals
		if signal.Confidence < 0.5 {
			continue
		}

		// Process LONG signal
		if signal.Direction == "LONG" {
			t.Logger.Info("Processing LONG signal with confidence %.2f", signal.Confidence)

			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "SHORT" {
				t.Logger.Info("Closing existing SHORT position before opening LONG")
				t.closeOppositePosition("LONG")
			}

			t.HandleLongSignal(signal.ClosePrice)
		} else if signal.Direction == "SHORT" {
			t.Logger.Info("Processing SHORT signal with confidence %.2f", signal.Confidence)

			// Check if we have an opposite position that needs to be closed first
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "LONG" {
				t.Logger.Info("Closing existing LONG position before opening SHORT")
				t.closeOppositePosition("SHORT")
			}

			t.HandleShortSignal(signal.ClosePrice)
		}
	}
}

func (t *Trader) resetSignalStrength(m *map[string]int) {
	for k := range *m {
		delete(*m, k)
	}
}

// SyncPositionRealTime implements trailing stop logic
func (t *Trader) SyncPositionRealTime() {
	t.Logger.Info("Starting trailing stop logic...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Current position
		exists, side, _, tp, sl := t.PositionManager.HasOpenPosition()
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

		// Progress toward TP
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
		targetSL := entry + prog*dist*0.5
		needMove := false

		if side == "LONG" && targetSL > sl {
			needMove = true
		} else if side == "SHORT" && targetSL < sl {
			needMove = true
		}
		if !needMove {
			continue
		}

		t.Logger.Info("Trailing stop: updating SL from %.2f to %.2f (%.0f%% way to TP)", sl, targetSL, prog*100)
		
		// Update stop-loss
		if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tp, targetSL); err != nil {
			t.Logger.Error("Trailing SL update error: %v", err)
		} else {
			t.Logger.Info("SL → %.2f (%.0f%% way to TP)", targetSL, prog*100)
		}
	}
}

// OnClosedCandle processes a closed candle
func (t *Trader) OnClosedCandle(closePrice float64) {
	t.State.Closes = append(t.State.Closes, closePrice)
	t.State.Highs = append(t.State.Highs, closePrice)
	t.State.Lows = append(t.State.Lows, closePrice)

	if t.Config.Debug {
		t.Logger.Debug("Added price: %.2f, length closes: %d", closePrice, len(t.State.Closes))
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

// Initialize the new signal generation components
func (t *Trader) InitializeNewSignalSystem() {
	t.Logger.Info("Initializing new signal generation system...")
	
	// Initialize new components
	t.RegimeDetector = NewRegimeDetector()
	t.MultiTimeframeIndicators = NewMultiTimeframeIndicators()
	t.SignalScorer = NewSignalScorer()
	t.SignalFilter = NewSignalFilter()
	t.SignalQueueManager = NewSignalQueueManager()
	
	// Start the new signal generation worker
	go t.NewSignalGenerationWorker()
}

// NewSignalGenerationWorker generates signals based on the new architecture
func (t *Trader) NewSignalGenerationWorker() {
	t.Logger.Info("Starting New Signal Generation Worker with multi-timeframe analysis...")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Update market regime
		regime := t.RegimeDetector.DetectRegime(t.State.Closes)
		t.State.MarketRegime = string(regime)
		t.SignalQueueManager.UpdateRegime(regime)
		
		// Update multi-timeframe data
		t.MultiTimeframeIndicators.data.AddData(signals.Source1Sec, t.State.Closes)
		
		// Generate signals for current timeframe (1-second)
		if len(t.State.Closes) < t.Config.SmaLen {
			continue
		}

		closesCopy := append([]float64(nil), t.State.Closes...)
		cls := closesCopy[len(closesCopy)-1]

		// Calculate all indicators on current timeframe
		smaVal := indicators.SMA(closesCopy)
		macdResult := indicators.MACDResult{}
		macdLine, signalLine := indicators.MACD(closesCopy)
		macdResult.MACDLine = macdLine
		macdResult.SignalLine = signalLine
		macdResult.Histogram = macdLine - signalLine
		bbBands := indicators.BollingerBands{}
		bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(closesCopy, 20, 2.0)
		bbBands.Upper = bbUpper
		bbBands.Middle = bbMiddle
		bbBands.Lower = bbLower
		rsiValues := indicators.RSI(closesCopy, 14)
		var rsi float64
		if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
			rsi = rsiValues[len(rsiValues)-1]
		}

		// Score each signal type based on current regime
		smaSignal := t.SignalScorer.ScoreSMASignal(cls, smaVal, regime, signals.Source1Sec)
		macdSignal := t.SignalScorer.ScoreMACDSignal(macdResult, regime, signals.Source1Sec)
		bbSignal := t.SignalScorer.ScoreBollingerSignal(cls, bbBands, regime, signals.Source1Sec)
		rsiSignal := t.SignalScorer.ScoreRSISignal(rsi, regime, signals.Source1Sec)
		
		// Get orderbook signal
		t.State.ObLock.Lock()
		var bidDepth, askDepth float64
		for _, size := range t.State.BidsMap {
			bidDepth += size
		}
		for _, size := range t.State.AsksMap {
			askDepth += size
		}
		t.State.ObLock.Unlock()
		obSignal := t.SignalScorer.ScoreOrderbookSignal(bidDepth, askDepth, regime, signals.Source1Sec)
		
		// Collect all generated signals
		rawSignals := []signals.Signal{}
		if smaSignal.Direction != "" {
			smaSignal.ClosePrice = cls
			smaSignal.Time = time.Now()
			rawSignals = append(rawSignals, smaSignal)
		}
		if macdSignal.Direction != "" {
			macdSignal.ClosePrice = cls
			macdSignal.Time = time.Now()
			rawSignals = append(rawSignals, macdSignal)
		}
		if bbSignal.Direction != "" {
			bbSignal.ClosePrice = cls
			bbSignal.Time = time.Now()
			rawSignals = append(rawSignals, bbSignal)
		}
		if rsiSignal.Direction != "" {
			rsiSignal.ClosePrice = cls
			rsiSignal.Time = time.Now()
			rawSignals = append(rawSignals, rsiSignal)
		}
		if obSignal.Direction != "" {
			obSignal.ClosePrice = cls
			obSignal.Time = time.Now()
			rawSignals = append(rawSignals, obSignal)
		}

		// Filter each signal
		filteredSignals := []signals.Signal{}
		for _, signal := range rawSignals {
			filteredSignal, ok := t.SignalFilter.FilterSignal(signal, regime, cls)
			if ok {
				// Add to signal buffer for temporal confirmation
				t.SignalFilter.AddToBuffer(filteredSignal)
				
				// Check if signal is confirmed
				if t.SignalFilter.ConfirmSignal(filteredSignal) {
					filteredSignals = append(filteredSignals, filteredSignal)
				}
			}
		}

		// Process signal batch and get combined decision
		if len(filteredSignals) > 0 {
			combinedSignal, ok := t.SignalQueueManager.ProcessSignalBatch(filteredSignals, 0.3) // Minimum 0.3 confidence
			if ok {
				t.Logger.Info("Combined signal generated: %s with confidence %.2f", combinedSignal.Direction, combinedSignal.Confidence)
			}
		}
	}
}