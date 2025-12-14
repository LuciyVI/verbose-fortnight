package strategy

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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

type indicatorSnapshot struct {
	Close      float64
	SMA        float64
	RSI        float64
	MACDLine   float64
	MACDSignal float64
	MACDHist   float64
	ATR        float64
	BBUpper    float64
	BBMiddle   float64
	BBLower    float64
	HTFBias    string
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

func (t *Trader) adaptiveDeadband(atr, price, base float64) float64 {
	if base == 0 {
		base = price * 0.001
	}
	if atr > 0 {
		return math.Max(base, atr*t.Config.AtrDeadbandMult)
	}
	return base
}

// feeBuffer returns the minimum price move (in price units) needed to cover a round trip with fees and slippage padding
func (t *Trader) feeBuffer(entry float64) float64 {
	bufPerc := t.Config.RoundTripFeePerc
	if bufPerc <= 0 {
		bufPerc = 0.0012
	}
	mult := t.Config.FeeBufferMult
	if mult <= 0 {
		mult = 1.0
	}
	buf := entry * bufPerc * mult
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	if buf > 0 {
		buf = math.Ceil(buf/tick) * tick
	}
	return buf
}

func (t *Trader) higherTimeframeBias(atr float64) string {
	win := t.Config.HTFWindow
	if len(t.State.Closes) < win || win == 0 {
		return ""
	}
	window := t.State.Closes[len(t.State.Closes)-win:]
	short := window
	if len(short) > t.Config.HTFMaLen && t.Config.HTFMaLen > 0 {
		short = short[len(short)-t.Config.HTFMaLen:]
	}
	maLong := indicators.SMA(window)
	maShort := indicators.SMA(short)
	slope := window[len(window)-1] - window[0]
	bias := ""
	atrGate := t.adaptiveDeadband(atr, window[len(window)-1], 0)
	if maShort > maLong && slope > atrGate {
		bias = "LONG"
	} else if maShort < maLong && slope < -atrGate {
		bias = "SHORT"
	}
	return bias
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

// CheckOrderbookStrength inspects multi-level orderbook imbalance and stability
func (t *Trader) CheckOrderbookStrength(side string, lastPrice float64) (bool, string) {
	t.State.ObLock.Lock()
	bidsCopy := make(map[string]float64, len(t.State.BidsMap))
	asksCopy := make(map[string]float64, len(t.State.AsksMap))
	for k, v := range t.State.BidsMap {
		bidsCopy[k] = v
	}
	for k, v := range t.State.AsksMap {
		asksCopy[k] = v
	}
	t.State.ObLock.Unlock()

	if len(bidsCopy) == 0 || len(asksCopy) == 0 {
		return false, "orderbook empty"
	}

	type level struct {
		price float64
		size  float64
	}

	var bids, asks []level
	for ps, sz := range bidsCopy {
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil {
			continue
		}
		bids = append(bids, level{price: p, size: sz})
	}
	for ps, sz := range asksCopy {
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil {
			continue
		}
		asks = append(asks, level{price: p, size: sz})
	}

	sort.Slice(bids, func(i, j int) bool { return bids[i].price > bids[j].price })
	sort.Slice(asks, func(i, j int) bool { return asks[i].price < asks[j].price })

	levels := t.Config.OrderbookLevels
	if levels <= 0 {
		levels = 3
	}

	sumTop := func(src []level, take int) float64 {
		lim := take
		if len(src) < take {
			lim = len(src)
		}
		acc := 0.0
		for i := 0; i < lim; i++ {
			acc += src[i].size
		}
		return acc
	}

	sumAll := func(src []level) float64 {
		acc := 0.0
		for _, lv := range src {
			acc += lv.size
		}
		return acc
	}

	topBidDepth := sumTop(bids, levels)
	topAskDepth := sumTop(asks, levels)
	totalDepth := sumAll(bids) + sumAll(asks)

	if totalDepth < t.Config.OrderbookMinDepth {
		return false, fmt.Sprintf("depth low (%.2f < %.2f)", totalDepth, t.Config.OrderbookMinDepth)
	}

	var ratio float64
	if side == "LONG" && topAskDepth > 0 {
		ratio = topBidDepth / topAskDepth
	} else if side == "SHORT" && topBidDepth > 0 {
		ratio = topAskDepth / topBidDepth
	}

	// When no explicit strength threshold is set, skip advanced stability gating to let signals pass
	if t.Config.OrderbookStrengthThreshold == 0 {
		if ratio == 0 {
			return false, "orderbook ratio zero"
		}
		return true, fmt.Sprintf("ratio %.2f depth %.2f", ratio, totalDepth)
	}

	// Track stability of imbalance
	t.State.Lock()
	t.State.ObImbalanceHistory = append(t.State.ObImbalanceHistory, ratio)
	if len(t.State.ObImbalanceHistory) > t.Config.OrderbookStabilityLookback && t.Config.OrderbookStabilityLookback > 0 {
		t.State.ObImbalanceHistory = t.State.ObImbalanceHistory[len(t.State.ObImbalanceHistory)-t.Config.OrderbookStabilityLookback:]
	}
	historyCopy := append([]float64(nil), t.State.ObImbalanceHistory...)
	t.State.Unlock()

	stable := true
	if len(historyCopy) >= 3 {
		minR, maxR := historyCopy[0], historyCopy[0]
		for _, v := range historyCopy[1:] {
			if v < minR {
				minR = v
			}
			if v > maxR {
				maxR = v
			}
		}
		if maxR-minR > 0.6 {
			stable = false
		}
	}

	median := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		tmp := append([]float64(nil), arr...)
		sort.Float64s(tmp)
		return tmp[len(tmp)/2]
	}
	medianRatio := median(historyCopy)
	if medianRatio > 0 && ratio < medianRatio*0.8 {
		return false, fmt.Sprintf("ratio %.2f below median %.2f", ratio, medianRatio)
	}

	// Correlate imbalance with recent price drift
	priceDrift := 0.0
	if ln := len(t.State.Closes); ln >= 2 {
		priceDrift = t.State.Closes[ln-1] - t.State.Closes[ln-2]
	}

	// Volume confirmation: require last volume spike relative to median if configured
	if len(t.State.Volumes) > 5 && t.Config.VolumeSpikeMult > 0 {
		medVol := median(t.State.Volumes[len(t.State.Volumes)-5:])
		lastVol := t.State.Volumes[len(t.State.Volumes)-1]
		if medVol > 0 && lastVol < medVol*t.Config.VolumeSpikeMult {
			return false, fmt.Sprintf("volume %.2f below spike %.2f", lastVol, medVol*t.Config.VolumeSpikeMult)
		}
		if t.Config.MinVolume > 0 && lastVol < t.Config.MinVolume {
			return false, fmt.Sprintf("volume %.2f below minimum %.2f", lastVol, t.Config.MinVolume)
		}
	}

	threshold := t.Config.OrderbookStrengthThreshold
	if ratio == 0 {
		return false, "orderbook ratio zero"
	}
	if ratio < threshold {
		return false, fmt.Sprintf("ratio %.2f below %.2f", ratio, threshold)
	}
	if !stable {
		return false, fmt.Sprintf("ratio unstable %.2f", ratio)
	}
	if side == "LONG" && priceDrift < 0 && lastPrice != 0 {
		return false, "ratio bullish but price drifting down"
	}
	if side == "SHORT" && priceDrift > 0 && lastPrice != 0 {
		return false, "ratio bearish but price drifting up"
	}

	return true, fmt.Sprintf("ratio %.2f depth %.2f", ratio, totalDepth)
}

// DetectMarketRegime detects market regime (trend vs range)
func (t *Trader) DetectMarketRegime() {
	minLen := 50
	if t.Config.HTFWindow > minLen {
		minLen = t.Config.HTFWindow
	}
	if len(t.State.Closes) < minLen {
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

	candidate := "range"
	if trendStrength > 0.55 && rangePerc > 2.0 {
		candidate = "trend"
	} else if volatilityRegime == "low" && rangePerc < 1.5 {
		candidate = "range"
	}
	if candidate != t.State.RegimeCandidate {
		t.State.RegimeCandidate = candidate
		t.State.RegimeStreak = 1
		t.Logger.Info("Regime vote reset to %s", candidate)
	} else {
		t.State.RegimeStreak++
	}

	if t.State.RegimeStreak >= t.Config.RegimePersistence && t.State.MarketRegime != candidate {
		t.State.MarketRegime = candidate
		t.Logger.Info("Market regime persisted for %d votes → %s", t.State.RegimeStreak, candidate)
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
	t.State.Lock()
	t.State.PartialTPDone = false
	t.State.Unlock()

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
			exitPrice := price // Using the current price as the exit price
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

	t.State.Lock()
	t.State.LastEntryAt = time.Now()
	t.State.LastEntryPrice = entry
	t.State.LastEntryDir = t.PositionManager.NormalizeSide(newSide)
	t.State.Unlock()

	// Calculate TP/SL with 2:1 ratio based on 15-minute projection
	tp, sl := t.calculateTPSLWithRatio(entry, newSide)

	minPocket := entry*t.Config.SLPocketPerc + t.feeBuffer(entry)
	if t.PositionManager.NormalizeSide(newSide) == "LONG" {
		if entry-sl < minPocket {
			sl = entry - minPocket
		}
	} else {
		if sl-entry < minPocket {
			sl = entry + minPocket
		}
	}

	delay := time.Duration(t.Config.SLSetDelaySec) * time.Second
	t.Logger.Info("Position opened: %s %.4f @ %.2f | TP %.2f  SL %.2f (send after %s, minPocket %.4f)",
		newSide, qty, entry, tp, sl, delay.String(), minPocket)

	go func(tpVal, slVal float64) {
		if delay > 0 {
			time.Sleep(delay)
		}
		if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tpVal, slVal); err != nil {
			t.Logger.Error("Error setting TP/SL after delay: %v", err)
			return
		}
		t.Logger.Info("TP/SL placed ▶ TP %.2f  SL %.2f", tpVal, slVal)
	}(tp, sl)
}

// calculateTPSLWithRatio calculates TP/SL with a 2:1 ratio based on 15-minute price projection
// TP is calculated based on expected price movement in the next 15 minutes
func (t *Trader) calculateTPSLWithRatio(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
	regimeFactor := 1.0
	if t.State.MarketRegime == "trend" {
		regimeFactor = 1.1
	} else if t.State.MarketRegime == "range" {
		regimeFactor = 0.9
	}
	feeBuf := t.feeBuffer(entryPrice)
	minProfitDistance := math.Max(entryPrice*t.Config.MinProfitPerc, feeBuf*1.5)

	if atr > 0 {
		slDist := atr * t.Config.AtrSLMult * regimeFactor
		tpDist := atr * t.Config.AtrTPMult * regimeFactor
		// Enforce minimum SL distance to avoid noise whipsaw
		minSL := entryPrice * t.Config.SlPerc
		if slDist < minSL {
			slDist = minSL
		}
		if positionSide == "LONG" {
			stopLoss = entryPrice - slDist
			takeProfit = entryPrice + tpDist
		} else {
			stopLoss = entryPrice + slDist
			takeProfit = entryPrice - tpDist
		}
	} else {
		// Fallback to percentage distances
		takeProfit = t.calculateTPBasedOn15MinProjection(entryPrice, positionSide)
		var tpPercentDistance float64
		if positionSide == "LONG" {
			tpPercentDistance = math.Abs((takeProfit - entryPrice) / entryPrice * 100)
		} else {
			tpPercentDistance = math.Abs((entryPrice - takeProfit) / entryPrice * 100)
		}
		slPercentDistance := tpPercentDistance / 2
		if positionSide == "LONG" {
			stopLoss = entryPrice * (1 - slPercentDistance/100)
		} else {
			stopLoss = entryPrice * (1 + slPercentDistance/100)
		}
	}

	if positionSide == "LONG" && takeProfit-entryPrice < minProfitDistance {
		takeProfit = entryPrice + minProfitDistance
	}
	if positionSide == "SHORT" && entryPrice-takeProfit < minProfitDistance {
		takeProfit = entryPrice - minProfitDistance
	}

	tpDistanceActual := math.Abs(takeProfit - entryPrice)
	slDistanceActual := math.Abs(entryPrice - stopLoss)
	if slDistanceActual > 0 {
		ratio := tpDistanceActual / slDistanceActual
		targetRR := 2.0
		if ratio < targetRR && slDistanceActual > 0 {
			adj := slDistanceActual * targetRR
			if positionSide == "LONG" {
				takeProfit = entryPrice + adj
			} else {
				takeProfit = entryPrice - adj
			}
		}
	}

	// Round to tick size
	if t.State.Instr.TickSize > 0 {
		takeProfit = math.Round(takeProfit/t.State.Instr.TickSize) * t.State.Instr.TickSize
		stopLoss = math.Round(stopLoss/t.State.Instr.TickSize) * t.State.Instr.TickSize
	}

	t.Logger.Debug("TP/SL calc (ATR basis) Entry %.2f TP %.2f SL %.2f ATR %.4f RegimeFactor %.2f", entryPrice, takeProfit, stopLoss, atr, regimeFactor)
	return takeProfit, stopLoss
}

func (t *Trader) buildIndicatorSnapshot(closesCopy []float64) indicatorSnapshot {
	cls := closesCopy[len(closesCopy)-1]
	recent := closesCopy
	if len(recent) > t.Config.SmaLen && t.Config.SmaLen > 0 {
		recent = recent[len(recent)-t.Config.SmaLen:]
	}
	smaVal := indicators.SMA(recent)

	rsiValues := indicators.RSI(closesCopy, 14)
	var rsi float64
	if len(rsiValues) > 0 && !math.IsNaN(rsiValues[len(rsiValues)-1]) {
		rsi = rsiValues[len(rsiValues)-1]
	}

	macdLine, signalLine, macdHist := indicators.MACD(closesCopy)
	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
	bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(closesCopy, 20, 2.0)
	htfBias := t.higherTimeframeBias(atr)

	return indicatorSnapshot{
		Close:      cls,
		SMA:        smaVal,
		RSI:        rsi,
		MACDLine:   macdLine,
		MACDSignal: signalLine,
		MACDHist:   macdHist,
		ATR:        atr,
		BBUpper:    bbUpper,
		BBMiddle:   bbMiddle,
		BBLower:    bbLower,
		HTFBias:    htfBias,
	}
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

	move := projectedMovement
	if positionSide == "LONG" {
		move = math.Abs(projectedMovement)
	} else {
		move = -math.Abs(projectedMovement)
	}
	tpPrice := entryPrice + move

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

	newTP, newSL := t.calculateTPSLWithRatio(entry, "LONG")
	if math.Abs(newTP-curTP) < t.State.Instr.TickSize && newSL == 0 {
		return
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

	newTP, newSL := t.calculateTPSLWithRatio(entry, "SHORT")
	if math.Abs(newTP-curTP) < t.State.Instr.TickSize && newSL == 0 {
		return
	}

	t.Logger.Info("Sending TP/SL update to exchange: TP %.2f, SL %.2f", newTP, newSL)
	if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, newTP, newSL); err != nil {
		t.Logger.Error("adjustTPSLForShort error: %v", err)
	} else {
		t.Logger.Info("Recalculated TP/SL ▶ TP %.2f SL %.2f", newTP, newSL)
	}
}

// SMAMovingAverageWorker generates signals with regime-aware logic
func (t *Trader) SMAMovingAverageWorker() {
	t.Logger.Info("Starting SMA Moving Average Worker...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	hasTag := func(tags []string, needle string) bool {
		for _, tag := range tags {
			if strings.Contains(tag, needle) {
				return true
			}
		}
		return false
	}

	for range ticker.C {
		if len(t.State.Closes) < t.Config.SmaLen {
			t.Logger.Debug("SMA worker waiting: have %d candles, need %d", len(t.State.Closes), t.Config.SmaLen)
			continue
		}

		closesCopy := append([]float64(nil), t.State.Closes...)
		snap := t.buildIndicatorSnapshot(closesCopy)
		baseDeadband := 0.005
		if t.State.MarketRegime == "trend" {
			baseDeadband = 0.01
		}
		deadband := t.adaptiveDeadband(snap.ATR, snap.Close, baseDeadband)

		t.Logger.Debug("Indicators - Close %.2f SMA %.2f RSI %.2f MACD %.4f/%.4f hist %.4f BB %.2f/%.2f/%.2f ATR %.4f HTF %s Regime %s",
			snap.Close, snap.SMA, snap.RSI, snap.MACDLine, snap.MACDSignal, snap.MACDHist, snap.BBLower, snap.BBMiddle, snap.BBUpper, snap.ATR, snap.HTFBias, t.State.MarketRegime)

		allowLong := snap.RSI == 0 || snap.RSI <= t.Config.RSILow
		allowShort := snap.RSI == 0 || snap.RSI >= t.Config.RSIHigh
		if !allowLong {
			t.Logger.Debug("RSI filter blocks LONG: rsi=%.2f > %.2f", snap.RSI, t.Config.RSILow)
		}
		if !allowShort {
			t.Logger.Debug("RSI filter blocks SHORT: rsi=%.2f < %.2f", snap.RSI, t.Config.RSIHigh)
		}

		strength := map[string]int{"LONG": 0, "SHORT": 0}
		contrib := map[string][]string{
			"LONG":  {},
			"SHORT": {},
		}

		// Simple RSI/price divergence detection
		bullDiv := false
		bearDiv := false
		if len(closesCopy) >= 3 {
			cNow := closesCopy[len(closesCopy)-1]
			cPrev := closesCopy[len(closesCopy)-3]
			rsiValues := indicators.RSI(closesCopy, 14)
			if len(rsiValues) >= 3 {
				rNow := rsiValues[len(rsiValues)-1]
				rPrev := rsiValues[len(rsiValues)-3]
				if cNow < cPrev && rNow > rPrev {
					bullDiv = true
				}
				if cNow > cPrev && rNow < rPrev {
					bearDiv = true
				}
			}
		}

		regime := t.State.MarketRegime
		if regime == "" {
			regime = "trend"
		}

		// Regime-specific indicator alignment
		if regime == "trend" {
			if allowLong && snap.Close > snap.SMA+deadband {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "sma-breakout")
			}
			if allowShort && snap.Close < snap.SMA-deadband {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "sma-breakdown")
			}
			if allowLong && snap.MACDHist > 0 && snap.MACDLine > snap.MACDSignal {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "macd-trend")
			}
			if allowShort && snap.MACDHist < 0 && snap.MACDLine < snap.MACDSignal {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "macd-trend")
			}
			if allowLong && snap.Close >= snap.BBUpper+deadband {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "bb-breakout")
			}
			if allowShort && snap.Close <= snap.BBLower-deadband {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "bb-breakout")
			}
		} else { // range: contrarian
			if allowLong && snap.Close <= snap.BBLower-deadband {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "bb-revert")
			}
			if allowShort && snap.Close >= snap.BBUpper+deadband {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "bb-revert")
			}
			if allowLong && snap.Close < snap.SMA-deadband {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "sma-revert")
			}
			if allowShort && snap.Close > snap.SMA+deadband {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "sma-revert")
			}
			if allowLong && snap.MACDHist < 0 {
				strength["LONG"]++
				contrib["LONG"] = append(contrib["LONG"], "macd-mean")
			}
			if allowShort && snap.MACDHist > 0 {
				strength["SHORT"]++
				contrib["SHORT"] = append(contrib["SHORT"], "macd-mean")
			}
		}

		if bullDiv && allowLong {
			strength["LONG"]++
			contrib["LONG"] = append(contrib["LONG"], "rsi-div-bull")
		}
		if bearDiv && allowShort {
			strength["SHORT"]++
			contrib["SHORT"] = append(contrib["SHORT"], "rsi-div-bear")
		}

		// Higher timeframe alignment
		if snap.HTFBias == "LONG" && strength["SHORT"] > 0 {
			t.Logger.Debug("Dropping SHORT due to HTF LONG bias")
			strength["SHORT"] = 0
			contrib["SHORT"] = nil
		}
		if snap.HTFBias == "SHORT" && strength["LONG"] > 0 {
			t.Logger.Debug("Dropping LONG due to HTF SHORT bias")
			strength["LONG"] = 0
			contrib["LONG"] = nil
		}

		// Pick strongest direction
		dir := ""
		val := 0
		for d, v := range strength {
			if v > val {
				dir = d
				val = v
			}
		}
		if dir == "" || val == 0 {
			continue
		}

		// Orderbook confirmation contributes to strength and confidence
		obOK, obReason := t.CheckOrderbookStrength(dir, snap.Close)
		highConf := false
		if obOK {
			val++
			contrib[dir] = append(contrib[dir], "orderbook")
			if hasTag(contrib[dir], "sma") && hasTag(contrib[dir], "bb") {
				highConf = true
			}
		} else {
			t.Logger.Debug("Orderbook rejected %s: %s", dir, obReason)
		}

		sig := models.Signal{
			Kind:       dir,
			Direction:  dir,
			Strength:   val,
			Contribs:   contrib[dir],
			HighConf:   highConf,
			ClosePrice: snap.Close,
			Time:       time.Now(),
		}

		select {
		case t.State.SigChan <- sig:
			t.Logger.Info("%s signal generated (strength=%d, highConf=%t): %v", dir, val, highConf, contrib[dir])
		default:
			t.Logger.Warning("Signal channel full, dropping %s signal", dir)
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
		t.State.Lock()
		t.State.LastExitAt = time.Now()
		t.State.LastExitDir = side
		t.State.Unlock()
	}
}

// Trader processes signals and executes trades
func (t *Trader) Trader() {
	signalStrength := map[string]int{"LONG": 0, "SHORT": 0}
	lastDirection := ""
	grace := time.Duration(t.Config.GracePeriodSec) * time.Second
	priceBufMult := t.Config.MinReentryFeeBufferMult
	if priceBufMult <= 0 {
		priceBufMult = 2.0
	}

	for sig := range t.State.SigChan {
		dir := t.PositionManager.NormalizeSide(sig.Direction)
		if dir == "" {
			continue
		}

		now := time.Now()
		exists, posSide, _, _, _ := t.PositionManager.HasOpenPosition()
		posSide = t.PositionManager.NormalizeSide(posSide)

		t.State.RLock()
		lastEntryAt := t.State.LastEntryAt
		lastEntryPrice := t.State.LastEntryPrice
		lastExitAt := t.State.LastExitAt
		lastExitDir := t.State.LastExitDir
		t.State.RUnlock()

		entryForDelta := lastEntryPrice
		if entryForDelta == 0 {
			entryForDelta = t.PositionManager.GetLastEntryPrice()
		}
		if entryForDelta == 0 {
			entryForDelta = sig.ClosePrice
		}
		feeBuf := t.feeBuffer(entryForDelta)
		minDelta := feeBuf * priceBufMult

		if exists && grace > 0 && !lastEntryAt.IsZero() && now.Sub(lastEntryAt) < grace {
			if t.Config.Debug {
				t.Logger.Debug("Skipping %s: within grace period (%s since entry)", dir, now.Sub(lastEntryAt).String())
			}
			continue
		}

		if minDelta > 0 {
			if !exists && !lastExitAt.IsZero() && dir == lastExitDir && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				if t.Config.Debug {
					t.Logger.Debug("Skipping %s: price delta %.2f < min %.2f since last exit", dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				continue
			}
			if exists && posSide != "" && dir != posSide && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				if t.Config.Debug {
					t.Logger.Debug("Skipping flip %s→%s: price delta %.2f < min %.2f", posSide, dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				continue
			}
		}

		signalStrength[dir] += sig.Strength

		// Re-entry cooldown to avoid noisy flips
		cooldown := time.Duration(t.Config.ReentryCooldownSec) * time.Second
		if cooldown > 0 && !sig.HighConf && !sig.Time.IsZero() && !lastExitAt.IsZero() && dir == lastExitDir {
			if sig.Time.Sub(lastExitAt) < cooldown {
				t.Logger.Debug("Skipping %s due to re-entry cooldown (last exit %s ago)", dir, time.Since(lastExitAt).String())
				continue
			}
		}

		if t.Config.Debug {
			t.Logger.Debug("Signal strength: %v", signalStrength)
		}

		// Allow repeated signals unless we already have an open position on that side
		if dir == lastDirection && !sig.HighConf {
			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if !exists || t.PositionManager.NormalizeSide(side) != dir {
				// let it pass to open/flip
			} else {
				t.Logger.Debug("Skipping duplicate direction %s while position active", dir)
				continue
			}
		}

		threshold := t.Config.SignalStrengthThreshold
		if sig.HighConf {
			threshold = 1
		}

		obOK, obReason := t.CheckOrderbookStrength(dir, sig.ClosePrice)
		if !obOK {
			t.Logger.Debug("Orderbook rejected %s at trader stage: %s", dir, obReason)
			continue
		}

		// Process LONG signal with proper conflict resolution
		if dir == "LONG" && signalStrength["LONG"] >= threshold {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed LONG signal: strength=%d (threshold=%d) highConf=%t", signalStrength["LONG"], threshold, sig.HighConf)
			}

			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "SHORT" {
				t.Logger.Info("Closing existing SHORT position before opening LONG")
				t.closeOppositePosition("LONG")
			}

			t.HandleLongSignal(sig.ClosePrice)
			lastDirection = "LONG"
			t.resetSignalStrength(&signalStrength)
		} else if dir == "SHORT" && signalStrength["SHORT"] >= threshold {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed SHORT signal: strength=%d (threshold=%d) highConf=%t", signalStrength["SHORT"], threshold, sig.HighConf)
			}

			exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
			if exists && t.PositionManager.NormalizeSide(side) == "LONG" {
				t.Logger.Info("Closing existing LONG position before opening SHORT")
				t.closeOppositePosition("SHORT")
			}

			t.HandleShortSignal(sig.ClosePrice)
			lastDirection = "SHORT"
			t.resetSignalStrength(&signalStrength)
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

		tick := t.State.Instr.TickSize
		if tick <= 0 {
			tick = 0.1
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
		if prog <= 0 || dist == 0 {
			continue
		}

		feeBuf := t.feeBuffer(entry)
		risk := math.Abs(entry - sl)
		if risk == 0 && dist != 0 {
			risk = math.Abs(dist) / 2 // fall back to 1R = half the TP distance
		}

		// Move SL to breakeven only when price has covered fees
		if prog >= t.Config.BreakevenProgress {
			lockStep := risk * 0.2 // lock 0.2R once in profit
			if side == "LONG" && price-entry >= feeBuf {
				target := math.Max(entry+feeBuf, entry+lockStep)
				if target > price-tick {
					target = price - tick
				}
				if target > sl {
					if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tp, target); err != nil {
						t.Logger.Error("Breakeven SL update error: %v", err)
					} else {
						t.Logger.Info("Moved SL to lock %.2f for LONG", target)
						sl = target
					}
				}
			}
			if side == "SHORT" && entry-price >= feeBuf {
				target := math.Min(entry-feeBuf, entry-lockStep)
				if target < price+tick {
					target = price + tick
				}
				if sl == 0 || target < sl {
					if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tp, target); err != nil {
						t.Logger.Error("Breakeven SL update error: %v", err)
					} else {
						t.Logger.Info("Moved SL to lock %.2f for SHORT", target)
						sl = target
					}
				}
			}
		}

		// Partial exit
		t.State.RLock()
		partialDone := t.State.PartialTPDone
		t.State.RUnlock()
		if !partialDone && prog >= t.Config.PartialTakeProfitProgress {
			partialQty := qty * t.Config.PartialTakeProfitRatio
			if partialQty > 0 {
				reduceSide := "Sell"
				if side == "SHORT" {
					reduceSide = "Buy"
				}
				if err := t.OrderManager.PlaceOrderMarket(reduceSide, partialQty, true); err != nil {
					t.Logger.Error("Partial TP order failed: %v", err)
				} else {
					t.State.Lock()
					t.State.PartialTPDone = true
					t.State.Unlock()
					t.Logger.Info("Partial TP executed: %.2f (%s), progress=%.0f%%", partialQty, reduceSide, prog*100)
				}
			}
		}

		if t.Config.DisableTrailing {
			continue
		}

		// Trail only after a defined amount of progress to avoid churn in chop
		trailStart := math.Max(t.Config.TrailActivation, t.Config.BreakevenProgress)
		var realizedR float64
		if risk > 0 {
			if side == "LONG" {
				realizedR = (price - entry) / risk
			} else {
				realizedR = (entry - price) / risk
			}
		}
		if prog < trailStart || realizedR < t.Config.MinTrailRR {
			continue
		}

		atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)

		baseBuffer := price * t.Config.TrailPerc
		if atr > 0 {
			baseBuffer = math.Max(baseBuffer, atr*0.5)
		}
		tightBuffer := price * t.Config.TrailTightPerc
		if atr > 0 {
			tightBuffer = math.Max(tightBuffer, atr*0.25)
		}

		var targetSL float64
		if side == "LONG" {
			targetSL = price - baseBuffer
			if risk > 0 {
				targetSL = math.Max(targetSL, entry+risk*0.25) // lock at least 0.25R when trailing kicks in
			}
			targetSL = math.Max(targetSL, entry+feeBuf)
			if prog >= 0.9 {
				targetSL = math.Max(targetSL, price-tightBuffer)
			}
			targetSL = math.Min(targetSL, price-tick)
			if targetSL <= sl {
				continue
			}
		} else {
			targetSL = price + baseBuffer
			if risk > 0 {
				targetSL = math.Min(targetSL, entry-risk*0.25) // move stop below entry to secure profit
			}
			targetSL = math.Min(targetSL, entry-feeBuf)
			if prog >= 0.9 {
				targetSL = math.Min(targetSL, price+tightBuffer)
			}
			targetSL = math.Max(targetSL, price+tick)
			if targetSL >= sl {
				continue
			}
		}

		t.Logger.Info("Trailing stop: updating SL from %.2f to %.2f (%.0f%% way to TP)", sl, targetSL, prog*100)
		if err := t.PositionManager.UpdatePositionTPSL(t.Config.Symbol, tp, targetSL); err != nil {
			t.Logger.Error("Trailing SL update error: %v", err)
		} else {
			t.Logger.Info("SL → %.2f (%.0f%% way to TP)", targetSL, prog*100)
		}
	}
}

// OnClosedCandle processes a closed candle
func (t *Trader) OnClosedCandle(kline models.KlineData) {
	closePrice := kline.CloseFloat()
	high := kline.HighFloat()
	low := kline.LowFloat()
	vol, _ := strconv.ParseFloat(kline.Volume, 64)
	t.Logger.Debug("OnClosedCandle received: o/h/l/c=%s/%s/%s/%s vol=%s", kline.Open, kline.High, kline.Low, kline.Close, kline.Volume)
	if high == 0 {
		high = closePrice
	}
	if low == 0 {
		low = closePrice
	}

	t.State.Closes = append(t.State.Closes, closePrice)
	t.State.Highs = append(t.State.Highs, high)
	t.State.Lows = append(t.State.Lows, low)
	if vol > 0 {
		t.State.Volumes = append(t.State.Volumes, vol)
	}
	t.State.HTFCloses = append(t.State.HTFCloses, closePrice)
	t.Logger.Debug("Candle appended: closes=%d highs=%d lows=%d volumes=%d", len(t.State.Closes), len(t.State.Highs), len(t.State.Lows), len(t.State.Volumes))

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
		macdLine, macdSig, _ := indicators.MACD(t.State.Closes)
		bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(t.State.Closes, 20, 2.0)

		t.Logger.Debug("Indicators updated - SMA: %.2f, RSI: %.2f, MACD: %.4f/%.4f, BB: %.2f/%.2f/%.2f",
			smaVal, rsi, macdLine, macdSig, bbLower, bbMiddle, bbUpper)
	}

	// Increase maxLen to avoid data truncation
	maxLen := t.Config.SmaLen * 100
	if len(t.State.Closes) > maxLen {
		t.State.Closes = t.State.Closes[len(t.State.Closes)-maxLen:]
		t.State.Highs = t.State.Highs[len(t.State.Highs)-maxLen:]
		t.State.Lows = t.State.Lows[len(t.State.Lows)-maxLen:]
		if len(t.State.Volumes) > maxLen {
			t.State.Volumes = t.State.Volumes[len(t.State.Volumes)-maxLen:]
		}
		t.State.HTFCloses = t.State.HTFCloses[len(t.State.HTFCloses)-maxLen:]
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
