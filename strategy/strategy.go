package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	OrderManager    OrderManager
	PositionManager PositionManager
	Logger          logging.LoggerInterface
	lifecycle       *lifecycleMapper
	edgeObs         edgeObservability
}

type edgeObservability struct {
	mu sync.Mutex

	summaryWindowStart   time.Time
	summaryPass          int
	summaryReject        int
	summaryRejectMin     int
	summaryRejectSpread  int
	summaryRejectImb     int
	summaryDecisionCount int
	summaryScoreSum      float64

	rejectBurstCount  int
	rejectBurstStart  time.Time
	lastRejectWarnLog time.Time
}

const (
	edgeRejectBurstThreshold = 50
	edgeRejectBurstWindow    = 5 * time.Minute
	edgeRejectWarnCooldown   = 1 * time.Minute
	edgeSummaryWindow        = 60 * time.Second
)

// OrderManager is the strategy-facing order interface.
type OrderManager interface {
	PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error)
	PlaceTakeProfitOrder(side string, qty, price float64) error
}

type orderManagerWithLink interface {
	PlaceOrderMarketWithLinkID(side string, qty float64, reduceOnly bool, orderLinkID string) (string, error)
}

type orderManagerWithTPSLLink interface {
	PlaceTakeProfitOrderWithLink(side string, qty, price float64, orderLinkID string) error
}

// PositionManager is the strategy-facing position interface.
type PositionManager interface {
	HasOpenPosition() (bool, string, float64, float64, float64)
	NormalizeSide(side string) string
	GetLastEntryPrice() float64
	UpdatePositionTPSL(symbol string, tp, sl float64) error
	GetLastBidPrice() float64
	GetLastAskPrice() float64
	CancelAllOrders(symbol string)
}

type tradeEventLog struct {
	Event             string  `json:"event"`
	TradeID           string  `json:"trade_id"`
	Side              string  `json:"side"`
	Qty               float64 `json:"qty"`
	Entry             float64 `json:"entry"`
	TP                float64 `json:"tp"`
	SL                float64 `json:"sl"`
	TPDist            float64 `json:"tp_dist"`
	SLDist            float64 `json:"sl_dist"`
	FeeOpenEst        float64 `json:"fee_open_est"`
	FeeCloseEst       float64 `json:"fee_close_est"`
	FundingEst        float64 `json:"funding_est"`
	BreakevenMove     float64 `json:"breakeven_move"`
	TickSize          float64 `json:"tick_size"`
	StepSize          float64 `json:"step_size"`
	TPOrderType       string  `json:"tp_order_type"`
	SLOrderType       string  `json:"sl_order_type"`
	ReduceOnlyTP      bool    `json:"reduce_only_tp"`
	ReduceOnlySL      bool    `json:"reduce_only_sl"`
	TrailActivation   float64 `json:"trail_activation"`
	BreakevenProgress float64 `json:"breakeven_progress"`
}

func (t *Trader) logTradeEvent(event string, payload tradeEventLog) {
	payload.Event = event
	if payload.TradeID == "" {
		payload.TradeID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if payload.TickSize == 0 {
		payload.TickSize = t.State.Instr.TickSize
	}
	if payload.StepSize == 0 {
		payload.StepSize = t.State.Instr.QtyStep
	}
	if payload.TPOrderType == "" {
		payload.TPOrderType = "trading-stop"
	}
	if payload.SLOrderType == "" {
		payload.SLOrderType = "trading-stop"
	}
	if payload.TrailActivation == 0 {
		payload.TrailActivation = t.Config.TrailActivation
	}
	if payload.BreakevenProgress == 0 {
		payload.BreakevenProgress = t.Config.BreakevenProgress
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		t.Logger.Error("failed to marshal trade log: %v", err)
		return
	}
	t.Logger.Info("trade_event %s", string(bytes))
}

type tpslCalcDetails struct {
	entry            float64
	side             string
	regime           string
	atr              float64
	atrPercent       float64
	tpPercRaw        float64
	tpPercClamped    float64
	tpPercMin        float64
	tpPercMax        float64
	k                float64
	volatilityFactor float64
	regimeFactor     float64

	tpDistBase float64
	tpDist     float64
}

type dynamicTPDetails struct {
	ATRPercent       float64
	K                float64
	VolatilityFactor float64
	RegimeFactor     float64
	RawPercent       float64
	ClampedPercent   float64
	MinPercent       float64
	MaxPercent       float64
}

const fixedStopLossFraction = 0.0025 // 0.25%

func (t *Trader) dynamicTP(entry, atr float64, regime string) dynamicTPDetails {
	k := t.Config.DynamicTPK
	if k <= 0 {
		k = 1.0
	}
	volFactor := t.Config.DynamicTPVolatilityFactor
	if volFactor <= 0 {
		volFactor = 6.0
	}
	minPerc := t.Config.DynamicTPMinPerc
	if minPerc <= 0 {
		minPerc = 0.4
	}
	maxPerc := t.Config.DynamicTPMaxPerc
	if maxPerc <= 0 {
		maxPerc = 1.8
	}
	if maxPerc < minPerc {
		maxPerc = minPerc
	}

	regimeFactor := t.regimeFactor(regime)

	atrPerc := 0.0
	if entry > 0 && atr > 0 {
		atrPerc = atr / entry * 100
	}
	raw := k * atrPerc * volFactor * regimeFactor
	clamped := clamp(raw, minPerc, maxPerc)
	if math.IsNaN(clamped) || math.IsInf(clamped, 0) {
		clamped = minPerc
	}

	return dynamicTPDetails{
		ATRPercent:       atrPerc,
		K:                k,
		VolatilityFactor: volFactor,
		RegimeFactor:     regimeFactor,
		RawPercent:       raw,
		ClampedPercent:   clamped,
		MinPercent:       minPerc,
		MaxPercent:       maxPerc,
	}
}

// NewTrader creates a new trader instance
func NewTrader(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface) *Trader {
	orderManager := order.NewOrderManager(apiClient, cfg, state, logger)
	positionManager := position.NewPositionManager(apiClient, cfg, state, logger)

	tr := &Trader{
		APIClient:       apiClient,
		Config:          cfg,
		State:           state,
		OrderManager:    orderManager,
		PositionManager: positionManager,
		Logger:          logger,
	}
	if cfg != nil && cfg.EnableLifecycleID {
		tr.lifecycle = newLifecycleMapper(defaultLifecycleMapPath(), logger)
	}
	return tr
}

func (t *Trader) Shutdown() {
	if t == nil {
		return
	}
	if t.lifecycle != nil {
		t.lifecycle.Close()
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

func (t *Trader) estimateRoundTripFee(entry, exit, qty float64) float64 {
	feePerc := t.Config.RoundTripFeePerc
	if feePerc <= 0 {
		feePerc = 0.0012
	}
	price := entry
	if exit > 0 {
		price = (entry + exit) / 2
	}
	return math.Abs(price*qty) * feePerc
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
		if err := t.placeTakeProfitOrderWithLifecycle(orderSide, job.Qty, finalTP, t.activeTradeID()); err != nil {
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
	stabilityRange := t.Config.OrderbookStabilityRange
	if stabilityRange <= 0 {
		stabilityRange = 0.6
	}
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
		if maxR-minR > stabilityRange {
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
	medianMult := t.Config.OrderbookMedianRatioMult
	if medianMult <= 0 {
		medianMult = 0.5
	}
	if medianRatio > 0 && ratio < medianRatio*medianMult {
		return false, fmt.Sprintf("ratio %.2f below median %.2f", ratio, medianRatio)
	}

	// Correlate imbalance with recent price drift
	priceDrift := 0.0
	if ln := len(t.State.Closes); ln >= 2 {
		priceDrift = t.State.Closes[ln-1] - t.State.Closes[ln-2]
	}

	// Volume confirmation: require last volume spike relative to median if configured
	volOk := true
	volReason := ""
	lastVol := 0.0
	medVol := 0.0
	if len(t.State.Volumes) > 5 && t.Config.VolumeSpikeMult > 0 {
		medVol = median(t.State.Volumes[len(t.State.Volumes)-5:])
		lastVol = t.State.Volumes[len(t.State.Volumes)-1]
		if medVol > 0 && lastVol < medVol*t.Config.VolumeSpikeMult {
			volOk = false
			volReason = fmt.Sprintf("volume %.2f below spike %.2f", lastVol, medVol*t.Config.VolumeSpikeMult)
		} else if t.Config.MinVolume > 0 && lastVol < t.Config.MinVolume {
			volOk = false
			volReason = fmt.Sprintf("volume %.2f below minimum %.2f", lastVol, t.Config.MinVolume)
		}
	}
	if !volOk {
		if t.Config.Debug {
			t.Logger.Debug("Orderbook volume check: ok=false reason=%s lastVol=%.2f medVol=%.2f min=%.2f", volReason, lastVol, medVol, t.Config.MinVolume)
		}
		return false, volReason
	}
	if t.Config.Debug && t.Config.VolumeSpikeMult > 0 && len(t.State.Volumes) > 5 {
		t.Logger.Debug("Orderbook volume check: ok=true lastVol=%.2f medVol=%.2f spike=%.2f min=%.2f",
			lastVol, medVol, medVol*t.Config.VolumeSpikeMult, t.Config.MinVolume)
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

type edgeScoreResult struct {
	Score      float64
	OK         bool
	Reason     string
	Spread     float64
	SumBid     float64
	SumAsk     float64
	LevelsUsed int
}

func calcEdgeScore(side string, bidsMap, asksMap map[string]float64, levels int, minDepth float64) (score float64, ok bool, reason string) {
	res := calcEdgeScoreDetailed(side, bidsMap, asksMap, levels, minDepth)
	return res.Score, res.OK, res.Reason
}

func calcEdgeScoreDetailed(side string, bidsMap, asksMap map[string]float64, levels int, minDepth float64) edgeScoreResult {
	type level struct {
		price float64
		size  float64
	}
	if levels <= 0 {
		levels = 1
	}

	bids := make([]level, 0, len(bidsMap))
	for ps, sz := range bidsMap {
		if sz <= 0 {
			continue
		}
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil || p <= 0 {
			continue
		}
		bids = append(bids, level{price: p, size: sz})
	}
	asks := make([]level, 0, len(asksMap))
	for ps, sz := range asksMap {
		if sz <= 0 {
			continue
		}
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil || p <= 0 {
			continue
		}
		asks = append(asks, level{price: p, size: sz})
	}
	sort.Slice(bids, func(i, j int) bool { return bids[i].price > bids[j].price })
	sort.Slice(asks, func(i, j int) bool { return asks[i].price < asks[j].price })
	levelsUsed := levels
	if len(bids) < levelsUsed {
		levelsUsed = len(bids)
	}
	if len(asks) < levelsUsed {
		levelsUsed = len(asks)
	}
	if levelsUsed <= 0 {
		return edgeScoreResult{Reason: "min_depth"}
	}

	sumBid := 0.0
	sumAsk := 0.0
	for i := 0; i < levelsUsed; i++ {
		sumBid += bids[i].size
		sumAsk += asks[i].size
	}
	totalTopDepth := sumBid + sumAsk
	if minDepth > 0 && totalTopDepth < minDepth {
		return edgeScoreResult{
			Reason:     "min_depth",
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}
	if sumBid <= 0 || sumAsk <= 0 {
		return edgeScoreResult{
			Reason:     "min_depth",
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}

	bestBid := bids[0].price
	bestAsk := asks[0].price
	if bestBid <= 0 || bestAsk <= 0 || bestAsk <= bestBid {
		return edgeScoreResult{
			Reason:     "wide_spread",
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}
	spread := bestAsk - bestBid
	mid := (bestAsk + bestBid) / 2
	spreadPct := 0.0
	if mid > 0 {
		spreadPct = spread / mid
	}
	// Conservative hard gate: do not enter on abnormally wide spread.
	if spreadPct > 0.003 {
		return edgeScoreResult{
			Reason:     "wide_spread",
			Spread:     spread,
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}

	side = strings.ToUpper(strings.TrimSpace(side))
	directional := 0.0
	switch side {
	case "LONG":
		directional = sumBid / sumAsk
	case "SHORT":
		directional = sumAsk / sumBid
	default:
		return edgeScoreResult{
			Reason:     "low_imbalance",
			Spread:     spread,
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}

	// Penalize score for wider spread while preserving existing threshold scale.
	score := directional - spreadPct*100
	if score < 0 {
		score = 0
	}
	if directional <= 1 {
		return edgeScoreResult{
			Score:      score,
			OK:         false,
			Reason:     "low_imbalance",
			Spread:     spread,
			SumBid:     sumBid,
			SumAsk:     sumAsk,
			LevelsUsed: levelsUsed,
		}
	}
	return edgeScoreResult{
		Score:      score,
		OK:         true,
		Reason:     "ok",
		Spread:     spread,
		SumBid:     sumBid,
		SumAsk:     sumAsk,
		LevelsUsed: levelsUsed,
	}
}

func (t *Trader) edgeFilterCheck(side string) (pass bool, score float64, reason string, res edgeScoreResult) {
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

	res = calcEdgeScoreDetailed(side, bidsCopy, asksCopy, t.Config.OrderbookLevels, t.Config.OrderbookMinDepth)
	threshold := t.Config.OrderbookStrengthThreshold
	pass = res.OK && res.Score >= threshold
	score = res.Score
	reason = res.Reason
	if res.OK && res.Score < threshold {
		reason = "low_imbalance"
	}
	return pass, score, reason, res
}

func (t *Trader) logEdgeFilterDecision(pass bool, side string, score, threshold float64, reason string, res edgeScoreResult) {
	now := time.Now().UTC()
	status := "reject"
	if pass {
		status = "pass"
	}
	symbol := ""
	if t.Config != nil {
		symbol = t.Config.Symbol
	}
	payload := map[string]interface{}{
		"ts":         now.Format(time.RFC3339Nano),
		"symbol":     symbol,
		"side":       side,
		"score":      score,
		"threshold":  threshold,
		"reason":     reason,
		"spread":     res.Spread,
		"sumBid":     res.SumBid,
		"sumAsk":     res.SumAsk,
		"levelsUsed": res.LevelsUsed,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Logger.Info("edge_filter_%s side=%s score=%.4f threshold=%.4f reason=%s spread=%.6f sumBid=%.4f sumAsk=%.4f levelsUsed=%d",
			status, side, score, threshold, reason, res.Spread, res.SumBid, res.SumAsk, res.LevelsUsed)
		t.recordEdgeDecisionObservability(pass, score, reason, now)
		return
	}
	t.Logger.Info("edge_filter_%s %s", status, string(raw))
	t.recordEdgeDecisionObservability(pass, score, reason, now)
}

func (t *Trader) recordEdgeDecisionObservability(pass bool, score float64, reason string, now time.Time) {
	if t.State != nil {
		t.State.RecordEdgeDecision(pass, reason, now)
	}
	obs := &t.edgeObs
	obs.mu.Lock()

	if obs.summaryWindowStart.IsZero() {
		obs.summaryWindowStart = now
	}
	obs.summaryDecisionCount++
	obs.summaryScoreSum += score
	if pass {
		obs.summaryPass++
		obs.rejectBurstCount = 0
		obs.rejectBurstStart = time.Time{}
	} else {
		obs.summaryReject++
		switch reason {
		case "min_depth":
			obs.summaryRejectMin++
		case "wide_spread":
			obs.summaryRejectSpread++
		case "low_imbalance":
			obs.summaryRejectImb++
		}
		if obs.rejectBurstCount == 0 || obs.rejectBurstStart.IsZero() || now.Sub(obs.rejectBurstStart) > edgeRejectBurstWindow {
			obs.rejectBurstCount = 1
			obs.rejectBurstStart = now
		} else {
			obs.rejectBurstCount++
		}
	}

	shouldWarn := false
	rejectCount := obs.rejectBurstCount
	rejectWindowSec := int(now.Sub(obs.rejectBurstStart).Seconds())
	if !pass && obs.rejectBurstCount > edgeRejectBurstThreshold && now.Sub(obs.rejectBurstStart) <= edgeRejectBurstWindow {
		if obs.lastRejectWarnLog.IsZero() || now.Sub(obs.lastRejectWarnLog) >= edgeRejectWarnCooldown {
			obs.lastRejectWarnLog = now
			shouldWarn = true
		}
	}

	shouldSummary := obs.summaryDecisionCount > 0 && now.Sub(obs.summaryWindowStart) >= edgeSummaryWindow
	summaryPass := obs.summaryPass
	summaryReject := obs.summaryReject
	summaryMin := obs.summaryRejectMin
	summarySpread := obs.summaryRejectSpread
	summaryImb := obs.summaryRejectImb
	summaryDecisions := obs.summaryDecisionCount
	summaryAvg := 0.0
	if summaryDecisions > 0 {
		summaryAvg = obs.summaryScoreSum / float64(summaryDecisions)
	}
	windowSec := int(now.Sub(obs.summaryWindowStart).Seconds())
	if shouldSummary {
		obs.summaryWindowStart = now
		obs.summaryPass = 0
		obs.summaryReject = 0
		obs.summaryRejectMin = 0
		obs.summaryRejectSpread = 0
		obs.summaryRejectImb = 0
		obs.summaryDecisionCount = 0
		obs.summaryScoreSum = 0
	}
	obs.mu.Unlock()

	if shouldWarn {
		t.Logger.Warning("edge_filter_reject_burst count=%d window_sec=%d threshold=%d reason=%s",
			rejectCount, rejectWindowSec, edgeRejectBurstThreshold, reason)
	}
	if shouldSummary {
		t.Logger.Info("edge_filter_summary pass=%d reject=%d reasons={min_depth:%d wide_spread:%d low_imbalance:%d} avg_score=%.4f window_sec=%d",
			summaryPass, summaryReject, summaryMin, summarySpread, summaryImb, summaryAvg, windowSec)
	}
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
	t.handleDirectionalSignal("LONG", closePrice)
}

// HandleShortSignal processes a short signal
func (t *Trader) HandleShortSignal(closePrice float64) {
	t.handleDirectionalSignal("SHORT", closePrice)
}

func (t *Trader) handleDirectionalSignal(targetSide string, closePrice float64) {
	blocked, reason, message := t.isTradingBlocked()
	if blocked {
		t.Logger.Error("Signal rejected while trading blocked: side=%s reason=%s message=%s", targetSide, reason, message)
		return
	}

	targetSide = t.PositionManager.NormalizeSide(targetSide)
	exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
	side = t.PositionManager.NormalizeSide(side)
	fsm := t.currentFSMState()

	if !exists && fsm == models.PositionStateOpen {
		t.forceFSM(models.PositionStateFlat, "signal_sync_no_position", t.activeTradeID())
		fsm = models.PositionStateFlat
	}
	if exists && fsm == models.PositionStateFlat {
		t.forceFSM(models.PositionStateOpen, "signal_sync_existing_position", t.activeTradeID())
		fsm = models.PositionStateOpen
	}
	if t.Config != nil && t.Config.EnableEdgeFilter {
		needsEdgeCheck := false
		switch fsm {
		case models.PositionStateFlat:
			needsEdgeCheck = true
		case models.PositionStateOpen:
			needsEdgeCheck = exists && side != "" && side != targetSide
		}
		if needsEdgeCheck {
			threshold := t.Config.OrderbookStrengthThreshold
			pass, score, reason, res := t.edgeFilterCheck(targetSide)
			t.logEdgeFilterDecision(pass, targetSide, score, threshold, reason, res)
			if !pass {
				return
			}
		}
	}

	switch fsm {
	case models.PositionStateFlat:
		tradeID := newTradeID()
		if !t.transitionFSM(models.PositionStateOpening, "signal_accepted", tradeID) {
			return
		}
		t.setActiveTradeID(tradeID)
		t.logTradeEvent("signal_accepted", tradeEventLog{
			TradeID: tradeID,
			Side:    targetSide,
			Entry:   closePrice,
		})
		if err := t.openPosition(targetSide, closePrice, tradeID); err != nil {
			t.Logger.Error("Open position failed after accepted signal: %v", err)
			t.forceFSM(models.PositionStateFlat, "open_failed", tradeID)
		}
	case models.PositionStateOpen:
		if !exists || side == "" {
			t.forceFSM(models.PositionStateFlat, "open_state_without_live_position", t.activeTradeID())
			t.handleDirectionalSignal(targetSide, closePrice)
			return
		}
		if side == targetSide {
			if targetSide == "LONG" {
				t.adjustTPSL(closePrice)
			} else {
				t.adjustTPSLForShort(closePrice)
			}
			return
		}

		currentTradeID := t.activeTradeID()
		if currentTradeID == "" {
			currentTradeID = newTradeID()
			t.setActiveTradeID(currentTradeID)
		}
		nextTradeID := newTradeID()
		t.queuePendingReverse(targetSide, closePrice, nextTradeID)
		if !t.transitionFSM(models.PositionStateClosing, "reverse_close_intent", currentTradeID) {
			return
		}
		t.State.Lock()
		t.State.StopCtrl.DesiredTP = 0
		t.State.StopCtrl.DesiredSL = 0
		t.State.StopCtrl.Reason = "closing_disable_stops"
		t.State.StopCtrl.UpdatedAt = time.Now()
		t.State.Unlock()
		t.logTradeEvent("close_intent", tradeEventLog{
			TradeID: currentTradeID,
			Side:    side,
			Qty:     t.State.OrderQty,
			Entry:   closePrice,
		})
		t.closeCurrentPosition(side, t.State.OrderQty, currentTradeID, "reverse_to_"+targetSide)
	case models.PositionStateOpening, models.PositionStateClosing:
		t.Logger.Warning("Signal ignored while FSM=%s side=%s", fsm, targetSide)
	default:
		t.Logger.Warning("Signal ignored with unknown FSM=%s side=%s", fsm, targetSide)
	}
}

// roundDownToStep avoids exceeding the target notional while keeping step alignment.
func roundDownToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Floor(qty/step+1e-9) * step
}

// roundUpToStep enforces exchange minimums while keeping step alignment.
func roundUpToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Ceil(qty/step-1e-9) * step
}

func (t *Trader) placeMarketOrderWithLifecycle(side string, qty float64, reduceOnly bool, lifecycleID, leg string) (orderID, orderLinkID string, err error) {
	if t.Config != nil && t.Config.EnableLifecycleID && lifecycleID != "" {
		orderLinkID = lifecycleOrderLinkID(lifecycleID, leg)
		if om, ok := t.OrderManager.(orderManagerWithLink); ok {
			orderID, err = om.PlaceOrderMarketWithLinkID(side, qty, reduceOnly, orderLinkID)
			return orderID, orderLinkID, err
		}
		t.Logger.Warning("Lifecycle enabled but order manager does not support orderLinkId; fallback to plain order/create")
	}
	orderID, err = t.OrderManager.PlaceOrderMarketWithID(side, qty, reduceOnly)
	return orderID, "", err
}

func (t *Trader) placeTakeProfitOrderWithLifecycle(side string, qty, price float64, lifecycleID string) error {
	if t.Config != nil && t.Config.EnableLifecycleID && lifecycleID != "" {
		orderLinkID := lifecycleOrderLinkID(lifecycleID, "tp")
		if om, ok := t.OrderManager.(orderManagerWithTPSLLink); ok {
			return om.PlaceTakeProfitOrderWithLink(side, qty, price, orderLinkID)
		}
		t.Logger.Warning("Lifecycle enabled but order manager does not support TP orderLinkId; fallback to plain order/create")
	}
	return t.OrderManager.PlaceTakeProfitOrder(side, qty, price)
}

// openPosition opens a new position from FLAT->OPENING lifecycle only.
func (t *Trader) openPosition(newSide string, price float64, tradeID string) error {
	t.Logger.Info("Attempting to open new position: %s @ price %.2f", newSide, price)
	if blocked, reason, message := t.isTradingBlocked(); blocked {
		return fmt.Errorf("trading blocked: reason=%s message=%s", reason, message)
	}
	if t.currentFSMState() != models.PositionStateOpening {
		return fmt.Errorf("cannot open: fsm=%s", t.currentFSMState())
	}
	t.State.Lock()
	t.State.PartialTPDone = false
	t.State.Unlock()

	// Calculate quantity
	t.Logger.Info("Fetching balance from exchange...")
	bal, err := t.APIClient.GetBalance("USDT")
	if err != nil {
		t.Logger.Error("Error getting balance: %v", err)
		return err
	}
	t.Logger.Info("Current balance: %.2f USDT", bal)

	step := t.State.Instr.QtyStep
	if step <= 0 {
		step = t.State.Instr.MinQty
	}
	if step <= 0 {
		t.Logger.Error("Invalid instrument step size: %.8f", step)
		return fmt.Errorf("invalid instrument step size")
	}

	minQty := math.Max(t.State.Instr.MinQty, step)
	minRequired := minQty
	if price > 0 && t.State.Instr.MinNotional > 0 {
		minNotionalQty := t.State.Instr.MinNotional / price
		if minNotionalQty > minRequired {
			minRequired = minNotionalQty
		}
	}
	minRequired = roundUpToStep(minRequired, step)

	qty := minRequired
	if t.Config.OrderBalancePct > 0 && price > 0 {
		targetQty := (bal * t.Config.OrderBalancePct) / price
		qty = roundDownToStep(targetQty, step)
		if qty < minRequired {
			qty = minRequired
		}
	}
	if bal < price*qty {
		t.Logger.Error("Insufficient balance: %.2f USDT", bal)
		return fmt.Errorf("insufficient balance")
	}

	// Place market order
	orderSide := "Buy"
	if t.PositionManager.NormalizeSide(newSide) == "SHORT" {
		orderSide = "Sell"
	}
	t.logTradeEvent("entry_intent", tradeEventLog{
		TradeID:           tradeID,
		Side:              newSide,
		Qty:               qty,
		Entry:             price,
		TPOrderType:       "market",
		SLOrderType:       "market",
		TrailActivation:   t.Config.TrailActivation,
		BreakevenProgress: t.Config.BreakevenProgress,
	})
	t.Logger.Info("Placing market order: %s %.4f", orderSide, qty)
	orderID, orderLinkID, err := t.placeMarketOrderWithLifecycle(orderSide, qty, false, tradeID, "entry")
	if err != nil {
		t.Logger.Error("Error opening position %s: %v", newSide, err)
		return err
	}
	t.bindOrderLifecycle(orderID, orderLinkID, tradeID)
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
	tp, sl := t.calculateInitialTPSL(entry, newSide)
	feeBuf := t.feeBuffer(entry)
	tpDist := math.Abs(tp - entry)
	slDist := math.Abs(entry - sl)
	t.logTradeEvent("order_setup", tradeEventLog{
		TradeID:           tradeID,
		Side:              newSide,
		Qty:               qty,
		Entry:             entry,
		TP:                tp,
		SL:                sl,
		TPDist:            tpDist,
		SLDist:            slDist,
		FeeOpenEst:        feeBuf / 2,
		FeeCloseEst:       feeBuf / 2,
		FundingEst:        0,
		BreakevenMove:     feeBuf,
		ReduceOnlyTP:      false,
		ReduceOnlySL:      false,
		TrailActivation:   t.Config.TrailActivation,
		BreakevenProgress: t.Config.BreakevenProgress,
	})

	minPocket := t.minPocketDistance(entry)

	delay := time.Duration(t.Config.SLSetDelaySec) * time.Second
	t.Logger.Info("Position opened: %s %.4f @ %.2f | TP %.2f  SL %.2f (send after %s, minPocket %.4f)",
		newSide, qty, entry, tp, sl, delay.String(), minPocket)

	t.enqueueDelayedStopIntent(models.StopIntent{
		TradeID: tradeID,
		Side:    newSide,
		Entry:   entry,
		TP:      tp,
		SL:      sl,
		Reason:  "entry_initial",
	}, delay)
	return nil
}

func (t *Trader) calculateInitialTPSL(entry float64, positionSide string) (float64, float64) {
	details := t.calcTPSLDetails(entry, positionSide)
	if details.side == "" {
		return 0, 0
	}

	minPocket := t.minPocketDistance(entry)
	feeBuf := t.feeBuffer(entry)

	minTPFee := 0.0
	if t.Config.TPFeeFloorMult > 0 {
		minTPFee = feeBuf * t.Config.TPFeeFloorMult
	}
	minTPDist := math.Max(details.tpDist, minTPFee)
	requiredTPDist := minTPDist

	// 1) Calculate TP first.
	tpDist := details.tpDist
	ratioAdjusted := false
	if tpDist < requiredTPDist {
		tpDist = requiredTPDist
		ratioAdjusted = true
	}

	tp := entry + tpDist
	if details.side == "SHORT" {
		tp = entry - tpDist
	}

	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}

	tpPreRound := tp
	tp = t.roundTP(entry, tp, tick, details.side)

	// Ensure TP still respects minimum distance after rounding.
	tpDistFinal := math.Abs(tp - entry)
	if tpDistFinal < requiredTPDist {
		need := math.Ceil((requiredTPDist-tpDistFinal)/tick) * tick
		if details.side == "LONG" {
			tp += math.Max(need, tick)
		} else {
			tp -= math.Max(need, tick)
		}
		tpDistFinal = math.Abs(tp - entry)
	}

	// 2) Set SL to a fixed 0.25% distance from entry.
	slRaw, sl := t.fixedStopLoss(entry, details.side, tick)

	roundingApplied := tp != tpPreRound || sl != slRaw
	roundingMode := "long_tp_floor_sl_floor"
	if details.side == "SHORT" {
		roundingMode = "short_tp_ceil_sl_ceil"
	}

	slDistFinal := math.Abs(entry - sl)
	tpTicks := tpDistFinal / tick
	slTicks := slDistFinal / tick
	actualRR := 0.0
	if slDistFinal > 0 {
		actualRR = tpDistFinal / slDistFinal
	}

	t.Logger.Debug(
		"TP/SL calc entry %.2f side %s tick %.4f atr %.4f atrPerc %.4f%% k %.2f volF %.2f regime %s regF %.2f tpPerc raw %.4f%% clamp %.4f%% [%.4f..%.4f] tpDistBase %.4f required %.4f rr %.2f pocket %.4f tp %.2f sl %.2f tpTicks %.1f slTicks %.1f flags ratioAdj=%t rounding=%t mode=%s invariant=%t",
		entry,
		details.side,
		tick,
		details.atr,
		details.atrPercent,
		details.k,
		details.volatilityFactor,
		details.regime,
		details.regimeFactor,
		details.tpPercRaw,
		details.tpPercClamped,
		details.tpPercMin,
		details.tpPercMax,
		details.tpDistBase,
		requiredTPDist,
		actualRR,
		minPocket,
		tp,
		sl,
		tpTicks,
		slTicks,
		ratioAdjusted,
		roundingApplied,
		roundingMode,
		false,
	)

	return tp, sl
}

func (t *Trader) calculateTPSLLegacy(entryPrice float64, positionSide string) (float64, float64) {
	// Deprecated: keep for compatibility but route to the unified TP/SL logic.
	return t.calculateInitialTPSL(entryPrice, positionSide)
}

func (t *Trader) targetRR(regime string) float64 {
	if strings.ToLower(regime) == "trend" {
		val := 1.8
		if t.Config.TargetRRTrend > 0 {
			val = t.Config.TargetRRTrend
		}
		return clamp(val, 1.5, 2.2)
	}
	val := 1.0
	if t.Config.TargetRRRange > 0 {
		val = t.Config.TargetRRRange
	}
	return clamp(val, 0.8, 1.3)
}

func clamp(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func (t *Trader) atrTPCapMult(regime string) float64 {
	if strings.ToLower(regime) == "trend" {
		if t.Config.AtrTPCapMultTrend > 0 {
			return t.Config.AtrTPCapMultTrend
		}
		return 3.5
	}
	if t.Config.AtrTPCapMultRange > 0 {
		return t.Config.AtrTPCapMultRange
	}
	return 2.0
}

func (t *Trader) regimeFactor(regime string) float64 {
	switch strings.ToLower(regime) {
	case "trend":
		return 1.1
	case "range":
		return 0.9
	default:
		return 1.0
	}
}

func (t *Trader) clampTPDistWithAtr(tpDist, atr float64, regime string) (float64, float64, bool) {
	if atr <= 0 {
		return tpDist, 0, false
	}
	cap := atr * t.atrTPCapMult(regime) * t.regimeFactor(regime)
	if cap > 0 && tpDist > cap {
		return cap, cap, true
	}
	return tpDist, cap, false
}

func (t *Trader) minPocketDistance(entry float64) float64 {
	feeBuf := t.feeBuffer(entry)
	pocketFloor := entry * t.Config.SLPocketPerc
	if pocketFloor < 0 {
		pocketFloor = 0
	}
	feeFloor := 0.0
	if t.Config.PocketFeeMult > 0 {
		feeFloor = feeBuf * t.Config.PocketFeeMult
	}
	return math.Max(pocketFloor, feeFloor)
}

func calcSLToBE(side string, entry, pocket float64) float64 {
	if pocket < 0 {
		pocket = 0
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "SHORT":
		return entry - pocket
	case "LONG":
		return entry + pocket
	default:
		return entry
	}
}

func alignSLToBETick(side string, sl, tick float64) float64 {
	if tick <= 0 {
		return sl
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		return math.Ceil(sl/tick) * tick
	case "SHORT":
		return math.Floor(sl/tick) * tick
	default:
		return math.Round(sl/tick) * tick
	}
}

func (t *Trader) partialBEPocket(entry float64) float64 {
	entryAbs := math.Abs(entry)
	if entryAbs == 0 {
		return 0
	}
	entryPocket := entryAbs * math.Max(0, t.Config.SLPocketPerc)
	feePerc := t.Config.RoundTripFeePerc
	if feePerc <= 0 {
		feePerc = 0.0012
	}
	feeFloorMult := t.Config.SLFeeFloorMult
	if feeFloorMult <= 0 {
		feeFloorMult = 1.0
	}
	feePocketMult := t.Config.PocketFeeMult
	if feePocketMult <= 0 {
		feePocketMult = 0
	}
	feeFloor := entryAbs * feePerc * feeFloorMult
	feePocket := feeFloor * feePocketMult
	pocket := math.Max(entryPocket, feePocket)
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	if pocket > 0 {
		pocket = math.Ceil(pocket/tick) * tick
	}
	return pocket
}

func shouldMoveSLToBE(side string, currentSL, targetSL, tick float64) bool {
	eps := math.Max(tick/2, 1e-9)
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		if currentSL <= 0 {
			return true
		}
		return currentSL+eps < targetSL
	case "SHORT":
		if currentSL <= 0 {
			return true
		}
		return currentSL-eps > targetSL
	default:
		return false
	}
}

func (t *Trader) shouldSkipMoveSLToBEIntent(tradeID, side string, targetSL, tick float64) bool {
	if t == nil || t.State == nil || tradeID == "" {
		return false
	}
	t.State.RLock()
	ctrl := t.State.StopCtrl
	lastTradeID := t.State.LastBEIntentTradeID
	lastTargetSL := t.State.LastBEIntentSL
	t.State.RUnlock()

	alreadyDesired := ctrl.TradeID == tradeID && !shouldMoveSLToBE(side, ctrl.DesiredSL, targetSL, tick)
	alreadyApplied := ctrl.TradeID == tradeID && !shouldMoveSLToBE(side, ctrl.AppliedSL, targetSL, tick)
	alreadyQueued := lastTradeID == tradeID && !shouldMoveSLToBE(side, lastTargetSL, targetSL, tick)
	return alreadyDesired || alreadyApplied || alreadyQueued
}

func (t *Trader) roundTP(entry, tp, tick float64, side string) float64 {
	if tick <= 0 {
		return tp
	}
	if side == "LONG" {
		return math.Floor(tp/tick) * tick
	}
	if side == "SHORT" {
		return math.Ceil(tp/tick) * tick
	}
	return math.Round(tp/tick) * tick
}

func (t *Trader) roundSL(entry, sl, tick float64, side string) float64 {
	if tick <= 0 {
		return sl
	}
	if side == "LONG" {
		return math.Floor(sl/tick) * tick
	}
	if side == "SHORT" {
		return math.Ceil(sl/tick) * tick
	}
	return math.Round(sl/tick) * tick
}

func (t *Trader) fixedStopLoss(entry float64, side string, tick float64) (float64, float64) {
	dist := math.Abs(entry) * fixedStopLossFraction
	slRaw := entry - dist
	if side == "SHORT" {
		slRaw = entry + dist
	}
	sl := t.roundSL(entry, slRaw, tick, side)
	for i := 0; i < 4; i++ {
		slDistFinal := math.Abs(entry - sl)
		if slDistFinal >= dist-1e-9 {
			break
		}
		if side == "LONG" {
			sl -= tick
		} else if side == "SHORT" {
			sl += tick
		}
	}
	return slRaw, sl
}

// calculateTPSLWithRatio calculates TP/SL using ATR/projection with dynamic RR and caps.
// TP is calculated based on expected price movement in the next 15 minutes when ATR is unavailable.
func (t *Trader) calculateTPSLWithRatio(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	return t.calculateInitialTPSL(entryPrice, positionSide)
}

func (t *Trader) calcTPSLDetails(entryPrice float64, positionSide string) tpslCalcDetails {
	side := t.PositionManager.NormalizeSide(positionSide)
	if side == "" {
		side = strings.ToUpper(positionSide)
	}
	regime := strings.ToLower(t.State.MarketRegime)
	if regime != "trend" {
		regime = "range"
	}

	atr := indicators.CalculateATR(t.State.Highs, t.State.Lows, t.State.Closes, 14)
	dyn := t.dynamicTP(entryPrice, atr, regime)

	tpDist := 0.0
	if entryPrice > 0 {
		tpDist = entryPrice * dyn.ClampedPercent / 100
	}

	return tpslCalcDetails{
		entry:            entryPrice,
		side:             side,
		regime:           regime,
		atr:              atr,
		atrPercent:       dyn.ATRPercent,
		tpPercRaw:        dyn.RawPercent,
		tpPercClamped:    dyn.ClampedPercent,
		tpPercMin:        dyn.MinPercent,
		tpPercMax:        dyn.MaxPercent,
		k:                dyn.K,
		volatilityFactor: dyn.VolatilityFactor,
		regimeFactor:     dyn.RegimeFactor,
		tpDistBase:       tpDist,
		tpDist:           tpDist,
	}
}

func (t *Trader) buildIndicatorSnapshot(closesCopy []float64) models.IndicatorSnapshot {
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

	return models.IndicatorSnapshot{
		Time:       time.Now(),
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
	minTPPerc := t.Config.DynamicTPMinPerc
	if minTPPerc <= 0 {
		minTPPerc = 0.4
	}
	minTPFrac := minTPPerc / 100

	// Get recent price data for 15-minute projection
	if len(t.State.Closes) < 15 {
		t.Logger.Warning("Not enough data for 15-minute projection, using fallback")
		if positionSide == "LONG" {
			return entryPrice * (1 + minTPFrac)
		}
		return entryPrice * (1 - minTPFrac)
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

	// Ensure TP is reasonable (at least DynamicTPMinPerc away from entry)
	minTPDistance := entryPrice * minTPFrac
	actualTPDistance := math.Abs(tpPrice - entryPrice)

	if actualTPDistance < minTPDistance {
		if positionSide == "LONG" {
			tpPrice = entryPrice * (1 + minTPFrac)
		} else {
			tpPrice = entryPrice * (1 - minTPFrac)
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

	t.enqueueStopIntent(models.StopIntent{
		TradeID: t.activeTradeID(),
		Side:    "LONG",
		Entry:   entry,
		TP:      newTP,
		SL:      newSL,
		Reason:  "adjust_long_signal",
	})
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

	t.enqueueStopIntent(models.StopIntent{
		TradeID: t.activeTradeID(),
		Side:    "SHORT",
		Entry:   entry,
		TP:      newTP,
		SL:      newSL,
		Reason:  "adjust_short_signal",
	})
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

	lastCandleSeq := uint64(0)

	for range ticker.C {
		candleSeq := t.State.CandleSeq.Load()
		if candleSeq == 0 || candleSeq == lastCandleSeq {
			continue
		}
		lastCandleSeq = candleSeq

		if len(t.State.Closes) < t.Config.SmaLen {
			t.Logger.Debug("SMA worker waiting: have %d candles, need %d", len(t.State.Closes), t.Config.SmaLen)
			continue
		}

		closesCopy := append([]float64(nil), t.State.Closes...)
		snap := t.buildIndicatorSnapshot(closesCopy)
		t.State.StatusLock.Lock()
		t.State.LastIndicators = snap
		t.State.StatusLock.Unlock()
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
			CandleSeq:  candleSeq,
			Time:       time.Now(),
		}
		contribCopy := append([]string(nil), sig.Contribs...)
		t.State.StatusLock.Lock()
		t.State.LastSignal = models.SignalSnapshot{
			Kind:       sig.Kind,
			Direction:  sig.Direction,
			Strength:   sig.Strength,
			Contribs:   contribCopy,
			HighConf:   sig.HighConf,
			ClosePrice: sig.ClosePrice,
			CandleSeq:  sig.CandleSeq,
			Time:       sig.Time,
		}
		t.State.StatusLock.Unlock()

		select {
		case t.State.SigChan <- sig:
			t.Logger.Info("%s signal generated (strength=%d, highConf=%t): %v", dir, val, highConf, contrib[dir])
		default:
			t.Logger.Warning("Signal channel full, dropping %s signal", dir)
		}
	}
}

func (t *Trader) closeCurrentPosition(posSide string, qty float64, tradeID, reason string) {
	if blocked, bReason, message := t.isTradingBlocked(); blocked {
		t.Logger.Warning("Close skipped while trading blocked: reason=%s message=%s trade_id=%s", bReason, message, tradeID)
		return
	}
	posSide = t.PositionManager.NormalizeSide(posSide)
	if posSide == "" {
		return
	}
	if qty <= 0 {
		_, _, liveQty, _, _ := t.PositionManager.HasOpenPosition()
		qty = liveQty
	}
	if qty <= 0 {
		return
	}
	reduceSide := "Sell"
	if posSide == "SHORT" {
		reduceSide = "Buy"
	}
	orderID, orderLinkID, err := t.placeMarketOrderWithLifecycle(reduceSide, qty, true, tradeID, "clos")
	if err != nil {
		t.Logger.Error("Error closing position %s: %v", posSide, err)
		if t.currentFSMState() == models.PositionStateClosing {
			t.forceFSM(models.PositionStateOpen, "close_order_failed", tradeID)
		}
		return
	}
	t.bindOrderLifecycle(orderID, orderLinkID, tradeID)
	t.logTradeEvent("close_order", tradeEventLog{
		TradeID: tradeID,
		Side:    posSide,
		Qty:     qty,
		Entry:   t.PositionManager.GetLastEntryPrice(),
	})
	t.Logger.Info("Close order placed: trade_id=%s reason=%s side=%s qty=%.4f orderId=%s", tradeID, reason, posSide, qty, orderID)
}

// closeOppositePosition is retained for compatibility, but reversal is now gated by FSM.
func (t *Trader) closeOppositePosition(newSide string) {
	exists, side, qty, _, _ := t.PositionManager.HasOpenPosition()
	if !exists {
		return
	}

	side = t.PositionManager.NormalizeSide(side)
	newSide = t.PositionManager.NormalizeSide(newSide)

	// Only close if it's truly the opposite side
	if side != "" && side != newSide {
		t.closeCurrentPosition(side, qty, t.activeTradeID(), "legacy_close_opposite")
		t.State.Lock()
		t.State.LastExitAt = time.Now()
		t.State.LastExitDir = side
		t.State.Unlock()
	}
}

// HandleExecutionFill recalculates TP/SL after a fill using actual position size.
func (t *Trader) HandleExecutionFill(evt models.ExecutionEvent) {
	if evt.Qty <= 0 && evt.ClosedSize <= 0 {
		return
	}
	tradeID := evt.TradeID
	if tradeID == "" {
		tradeID = t.ResolveExecutionTradeID(evt.OrderID, evt.ExecID)
	}

	fsm := t.currentFSMState()
	if fsm == models.PositionStateOpening && evt.ReduceOnly {
		t.Logger.Error("FSM mismatch execution: OPENING received reduceOnly execution trade_id=%s exec_id=%s", tradeID, evt.ExecID)
	}
	if fsm == models.PositionStateClosing && !evt.ReduceOnly && evt.PositionSizeAfter > 0 {
		t.Logger.Error("FSM mismatch execution: CLOSING received non-reduce execution trade_id=%s exec_id=%s", tradeID, evt.ExecID)
	}
	if fsm == models.PositionStateFlat && evt.PositionSizeAfter > 0 {
		t.Logger.Error("FSM mismatch execution: FLAT received execution with non-zero positionSizeAfter trade_id=%s exec_id=%s", tradeID, evt.ExecID)
	}

	exists, posSide, posQty, curTP, curSL := t.PositionManager.HasOpenPosition()
	posSide = t.PositionManager.NormalizeSide(posSide)
	t.syncFSMFromPositionSnapshot(exists, posSide, posQty)
	if exists && t.currentFSMState() == models.PositionStateOpening {
		t.State.Lock()
		t.State.OpeningExecAck = true
		if t.State.OpeningPosAck {
			t.State.PositionState = models.PositionStateOpen
		}
		t.State.Unlock()
	}

	if !exists {
		if t.currentFSMState() == models.PositionStateClosing {
			t.forceFSM(models.PositionStateFlat, "execution_confirmed_flat", tradeID)
		}
		return
	}

	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		entry = evt.Price
	}

	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}

	if t.Config != nil && t.Config.EnablePartialBERule && evt.ReduceOnly && evt.ClosedSize > 0 && posQty > 0 {
		pocket := t.partialBEPocket(entry)
		targetSL := calcSLToBE(posSide, entry, pocket)
		targetSL = alignSLToBETick(posSide, targetSL, tick)
		if t.shouldSkipMoveSLToBEIntent(tradeID, posSide, targetSL, tick) {
			t.State.RecordBEIntentSkippedAlreadyBetter()
			return
		}
		if !shouldMoveSLToBE(posSide, curSL, targetSL, tick) {
			t.State.RecordBEIntentSkippedAlreadyBetter()
			return
		}
		tpCurrent := curTP
		if tpCurrent <= 0 {
			tpCurrent, _ = t.calculateInitialTPSL(entry, posSide)
		}
		if tpCurrent > 0 {
			now := time.Now().UTC()
			t.State.Lock()
			t.State.PartialTPDone = true
			t.State.Unlock()
			t.enqueueStopIntent(models.StopIntent{
				Kind:        "MoveSLToBE",
				TradeID:     tradeID,
				LifecycleID: tradeID,
				Side:        posSide,
				Entry:       entry,
				QtyLeft:     posQty,
				FeeEstimate: pocket,
				TP:          tpCurrent,
				SL:          targetSL,
				Breakeven:   true,
				Reason:      "partial_tp",
				TS:          now,
			})
			t.State.RecordBEIntentSent(tradeID, targetSL, now)
			return
		}
		t.Logger.Warning("Partial BE intent skipped: missing TP snapshot trade_id=%s side=%s entry=%.2f", tradeID, posSide, entry)
	}

	tpNew, slNew := t.calculateInitialTPSL(entry, posSide)
	tpNew = t.roundTP(entry, tpNew, tick, posSide)
	slNew = t.roundSL(entry, slNew, tick, posSide)

	feeBuf := t.feeBuffer(entry)
	t.logTradeEvent("fill_sync", tradeEventLog{
		TradeID:           tradeID,
		Side:              posSide,
		Qty:               posQty,
		Entry:             entry,
		TP:                tpNew,
		SL:                slNew,
		TPDist:            math.Abs(tpNew - entry),
		SLDist:            math.Abs(entry - slNew),
		FeeOpenEst:        feeBuf / 2,
		FeeCloseEst:       feeBuf / 2,
		BreakevenMove:     feeBuf,
		ReduceOnlyTP:      false,
		ReduceOnlySL:      false,
		TrailActivation:   t.Config.TrailActivation,
		BreakevenProgress: t.Config.BreakevenProgress,
	})

	if math.Abs(tpNew-curTP) < tick && math.Abs(slNew-curSL) < tick {
		return
	}
	t.enqueueStopIntent(models.StopIntent{
		TradeID: tradeID,
		Side:    posSide,
		Entry:   entry,
		TP:      tpNew,
		SL:      slNew,
		Reason:  "execution_fill_sync",
	})
}

// Trader processes signals and executes trades
func (t *Trader) Trader() {
	signalStrength := map[string]int{"LONG": 0, "SHORT": 0}
	lastDirection := ""
	lastCandleSeq := uint64(0)
	grace := time.Duration(t.Config.GracePeriodSec) * time.Second
	priceBufMult := t.Config.MinReentryFeeBufferMult
	if priceBufMult <= 0 {
		priceBufMult = 2.0
	}

	for sig := range t.State.SigChan {
		if blocked, reason, message := t.isTradingBlocked(); blocked {
			t.Logger.Error("Skipping signal while trading blocked: reason=%s message=%s", reason, message)
			continue
		}

		if sig.CandleSeq != 0 && sig.CandleSeq != lastCandleSeq {
			t.resetSignalStrength(&signalStrength)
			if t.Config.Debug {
				t.Logger.Debug("New candle seq=%d: reset signal strength", sig.CandleSeq)
			}
			lastCandleSeq = sig.CandleSeq
		}

		dir := t.PositionManager.NormalizeSide(sig.Direction)
		if dir == "" {
			continue
		}

		if t.Config.Debug {
			t.Logger.Debug("Signal received: dir=%s strength=%d highConf=%t close=%.2f candleSeq=%d", dir, sig.Strength, sig.HighConf, sig.ClosePrice, sig.CandleSeq)
		}

		now := time.Now()
		exists, posSide, _, _, _ := t.PositionManager.HasOpenPosition()
		posSide = t.PositionManager.NormalizeSide(posSide)

		t.State.RLock()
		lastEntryAt := t.State.LastEntryAt
		lastEntryPrice := t.State.LastEntryPrice
		lastExitAt := t.State.LastExitAt
		lastExitDir := t.State.LastExitDir
		lastEntryDir := t.State.LastEntryDir
		t.State.RUnlock()

		if t.Config.Debug {
			t.Logger.Debug("Position before signal: exists=%t side=%s lastEntryDir=%s lastEntryAt=%s lastExitDir=%s lastExitAt=%s",
				exists, posSide, lastEntryDir, lastEntryAt, lastExitDir, lastExitAt)
		}

		entryForDelta := lastEntryPrice
		if entryForDelta == 0 {
			entryForDelta = t.PositionManager.GetLastEntryPrice()
		}
		if entryForDelta == 0 {
			entryForDelta = sig.ClosePrice
		}
		feeBuf := t.feeBuffer(entryForDelta)
		minDelta := feeBuf * priceBufMult

		graceOk := true
		if exists && grace > 0 && !lastEntryAt.IsZero() && now.Sub(lastEntryAt) < grace {
			graceOk = false
			if t.Config.Debug {
				t.Logger.Debug("Filter graceOk=false dir=%s sinceEntry=%s grace=%s", dir, now.Sub(lastEntryAt).String(), grace)
			}
			continue
		}

		minDeltaOk := true
		if minDelta > 0 {
			if !exists && !lastExitAt.IsZero() && dir == lastExitDir && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				minDeltaOk = false
				if t.Config.Debug {
					t.Logger.Debug("Filter minDeltaOk=false dir=%s delta=%.2f min=%.2f since last exit", dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				continue
			}
			if exists && posSide != "" && dir != posSide && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				minDeltaOk = false
				if t.Config.Debug {
					t.Logger.Debug("Filter minDeltaOk=false dir=%s flip=%s->%s delta=%.2f min=%.2f", dir, posSide, dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				continue
			}
		}

		// Re-entry cooldown to avoid noisy flips
		cooldownOk := true
		cooldown := time.Duration(t.Config.ReentryCooldownSec) * time.Second
		if cooldown > 0 && !sig.HighConf && !sig.Time.IsZero() && !lastExitAt.IsZero() && dir == lastExitDir {
			if sig.Time.Sub(lastExitAt) < cooldown {
				cooldownOk = false
				if t.Config.Debug {
					t.Logger.Debug("Filter cooldownOk=false dir=%s sinceExit=%s cooldown=%s", dir, sig.Time.Sub(lastExitAt).String(), cooldown)
				}
				continue
			}
		}

		duplicateOk := true
		// Allow repeated signals unless we already have an open position on that side
		if dir == lastDirection && !sig.HighConf {
			if exists && posSide == dir {
				duplicateOk = false
				if t.Config.Debug {
					t.Logger.Debug("Filter duplicateOk=false dir=%s posSide=%s lastDirection=%s", dir, posSide, lastDirection)
				}
				continue
			}
		}

		if t.Config.Debug {
			t.Logger.Debug("Filters ok: graceOk=%t minDeltaOk=%t cooldownOk=%t duplicateOk=%t", graceOk, minDeltaOk, cooldownOk, duplicateOk)
		}

		obOK, obReason := t.CheckOrderbookStrength(dir, sig.ClosePrice)
		if t.Config.Debug {
			t.Logger.Debug("Orderbook check: ok=%t reason=%s", obOK, obReason)
		}
		if !obOK {
			continue
		}

		strengthBeforeLong := signalStrength["LONG"]
		strengthBeforeShort := signalStrength["SHORT"]
		signalStrength[dir] += sig.Strength
		strengthAfterLong := signalStrength["LONG"]
		strengthAfterShort := signalStrength["SHORT"]
		if t.Config.Debug {
			t.Logger.Debug("Signal strength update: dir=%s before(L/S)=%d/%d +%d => after(L/S)=%d/%d",
				dir, strengthBeforeLong, strengthBeforeShort, sig.Strength, strengthAfterLong, strengthAfterShort)
		}

		threshold := t.Config.SignalStrengthThreshold
		if sig.HighConf {
			threshold = 1
		}

		if t.Config.Debug {
			t.Logger.Debug("Signal threshold: dir=%s strength=%d threshold=%d highConf=%t", dir, signalStrength[dir], threshold, sig.HighConf)
		}

		// Process LONG signal with proper conflict resolution
		action := ""
		if dir == "LONG" && signalStrength["LONG"] >= threshold {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed LONG signal: strength=%d (threshold=%d) highConf=%t", signalStrength["LONG"], threshold, sig.HighConf)
			}

			t.HandleLongSignal(sig.ClosePrice)
			lastDirection = "LONG"
			t.resetSignalStrength(&signalStrength)
			action = "LONG"
		} else if dir == "SHORT" && signalStrength["SHORT"] >= threshold {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed SHORT signal: strength=%d (threshold=%d) highConf=%t", signalStrength["SHORT"], threshold, sig.HighConf)
			}

			t.HandleShortSignal(sig.ClosePrice)
			lastDirection = "SHORT"
			t.resetSignalStrength(&signalStrength)
			action = "SHORT"
		}

		if action != "" && t.Config.Debug {
			existsAfter, sideAfter, _, _, _ := t.PositionManager.HasOpenPosition()
			t.Logger.Debug("Position after %s signal: exists=%t side=%s", action, existsAfter, t.PositionManager.NormalizeSide(sideAfter))
		}
	}
}

func (t *Trader) resetSignalStrength(m *map[string]int) {
	for k := range *m {
		delete(*m, k)
	}
}

// SyncPositionRealTime keeps TP/SL in sync for open positions.
// SL is always fixed at 0.25% from entry; TP remains dynamic.
func (t *Trader) SyncPositionRealTime() {
	t.Logger.Info("Starting position sync logic...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var lastTPSLAttempt time.Time

	for range ticker.C {
		exists, side, qty, tp, sl := t.PositionManager.HasOpenPosition()
		side = t.PositionManager.NormalizeSide(side)

		t.syncFSMFromPositionSnapshot(exists, side, qty)
		if blocked, _, _ := t.isTradingBlocked(); blocked {
			continue
		}
		if !exists {
			if nextSide, nextPrice, nextTradeID, ok := t.popPendingReverseIfFlat(); ok {
				if nextTradeID == "" {
					nextTradeID = newTradeID()
				}
				if !t.transitionFSM(models.PositionStateOpening, "reverse_confirmed_flat", nextTradeID) {
					t.queuePendingReverse(nextSide, nextPrice, nextTradeID)
					continue
				}
				t.setActiveTradeID(nextTradeID)
				if err := t.openPosition(nextSide, nextPrice, nextTradeID); err != nil {
					t.Logger.Error("Pending reverse open failed: %v", err)
					t.forceFSM(models.PositionStateFlat, "pending_reverse_open_failed", nextTradeID)
				}
			}
			continue
		}
		if side == "" {
			continue
		}

		entry := t.PositionManager.GetLastEntryPrice()
		if entry == 0 {
			continue
		}
		if tp <= 0 || sl <= 0 {
			if !lastTPSLAttempt.IsZero() && time.Since(lastTPSLAttempt) < 10*time.Second {
				continue
			}
			lastTPSLAttempt = time.Now()
			tpNew, slNew := t.calculateInitialTPSL(entry, side)
			t.Logger.Info("Missing TP/SL detected for %s position, setting TP %.2f SL %.2f", side, tpNew, slNew)
			feeBuf := t.feeBuffer(entry)
			t.logTradeEvent("missing_tpsl", tradeEventLog{
				TradeID:           t.activeTradeID(),
				Side:              side,
				Qty:               qty,
				Entry:             entry,
				TP:                tpNew,
				SL:                slNew,
				TPDist:            math.Abs(tpNew - entry),
				SLDist:            math.Abs(entry - slNew),
				FeeOpenEst:        feeBuf / 2,
				FeeCloseEst:       feeBuf / 2,
				BreakevenMove:     feeBuf,
				ReduceOnlyTP:      false,
				ReduceOnlySL:      false,
				TrailActivation:   t.Config.TrailActivation,
				BreakevenProgress: t.Config.BreakevenProgress,
			})
			t.enqueueStopIntent(models.StopIntent{
				TradeID: t.activeTradeID(),
				Side:    side,
				Entry:   entry,
				TP:      tpNew,
				SL:      slNew,
				Reason:  "missing_tpsl_repair",
			})
			continue
		}

		tick := t.State.Instr.TickSize
		if tick <= 0 {
			tick = 0.1
		}

		_, fixedSL := t.fixedStopLoss(entry, side, tick)
		if fixedSL > 0 && math.Abs(sl-fixedSL) >= tick/2 {
			t.Logger.Info("Fixed SL policy: resetting SL for %s from %.2f to %.2f", side, sl, fixedSL)
			t.enqueueStopIntent(models.StopIntent{
				TradeID: t.activeTradeID(),
				Side:    side,
				Entry:   entry,
				TP:      tp,
				SL:      fixedSL,
				Reason:  "fixed_sl_sync",
			})
			sl = fixedSL
		}

		var price float64
		if side == "LONG" {
			price = t.PositionManager.GetLastBidPrice()
		} else {
			price = t.PositionManager.GetLastAskPrice()
		}
		if price == 0 {
			continue
		}

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

		t.State.RLock()
		partialDone := t.State.PartialTPDone
		t.State.RUnlock()
		if !partialDone && t.Config.PartialTakeProfitProgress > 0 && prog >= t.Config.PartialTakeProfitProgress {
			partialQty, remainingQty := partialClosePlan(qty, t.Config.PartialTakeProfitRatio)
			if partialQty > 0 {
				reduceSide := "Sell"
				if side == "SHORT" {
					reduceSide = "Buy"
				}
				tradeID := t.activeTradeID()
				orderID, orderLinkID, err := t.placeMarketOrderWithLifecycle(reduceSide, partialQty, true, tradeID, "ptp")
				if err != nil {
					t.Logger.Error("Partial TP order failed: %v", err)
				} else {
					t.bindOrderLifecycle(orderID, orderLinkID, tradeID)
					t.State.Lock()
					t.State.PartialTPDone = true
					t.State.Unlock()
					t.Logger.Info("Partial TP executed: %.2f (%s), progress=%.0f%%", partialQty, reduceSide, prog*100)
					if remainingQty > 0 {
						tpNew, slNew := t.calculateInitialTPSL(entry, side)
						t.enqueueStopIntent(models.StopIntent{
							TradeID: t.activeTradeID(),
							Side:    side,
							Entry:   entry,
							TP:      tpNew,
							SL:      slNew,
							Reason:  "partial_tp_remainder",
						})
					}
				}
			}
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
	t.State.CandleSeq.Add(1)
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
