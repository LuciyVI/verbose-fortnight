package strategy

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go-trader/config"
	"go-trader/types"
	"go-trader/logger"
	"go-trader/api"
	"go-trader/indicators"
	"go-trader/position"
	"go-trader/websocket"
	"go-trader/interfaces"
)

// Strategy represents a trading strategy
type Strategy struct {
	config           *config.Config
	logger           *logger.Logger
	api              *api.APIClient
	positionManager  *position.Manager
	wsHub            *websocket.WSHub
	closes           []float64
	highs            []float64
	lows             []float64
	marketRegime     string
	signalStrength   map[string]int
	signalStats      struct {
		total, correct, falsePositive int
		sync.Mutex
	}
}

var _ interfaces.TradingStrategy = (*Strategy)(nil)

// NewStrategy creates a new strategy instance
func NewStrategy(cfg *config.Config, log *logger.Logger, api *api.APIClient, posMgr *position.Manager, wsHub *websocket.WSHub) *Strategy {
	return &Strategy{
		config:          cfg,
		logger:          log,
		api:             api,
		positionManager: posMgr,
		wsHub:           wsHub,
		signalStrength:  make(map[string]int),
		marketRegime:    "range", // default regime
	}
}

// OnClosedCandle processes a closed candle
func (s *Strategy) OnClosedCandle(closePrice float64) {
	s.closes = append(s.closes, closePrice)
	s.highs = append(s.highs, closePrice)
	s.lows = append(s.lows, closePrice)

	// Manage slice length to prevent memory issues
	maxLen := s.config.WindowSize * 100
	if len(s.closes) > maxLen {
		s.closes = s.closes[len(s.closes)-maxLen:]
		s.highs = s.highs[len(s.highs)-maxLen:]
		s.lows = s.lows[len(s.lows)-maxLen:]
	}
}

// SMATradingLogic implements the SMA-based trading logic
func (s *Strategy) SMATradingLogic() {
	const smaLen = 20
	hysteresis := 0.005 // 0.5%

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		if len(s.closes) < smaLen {
			s.logger.Debug("Недостаточно данных для SMA (требуется %d, получено %d)", smaLen, len(s.closes))
			continue
		}

		smaVal := indicators.SMA(s.closes)
		cls := s.closes[len(s.closes)-1]

		s.logger.Debug("SMA: %.2f, Close: %.2f, MarketRegime: %s", smaVal, cls, s.marketRegime)

		// Adapt hysteresis based on market regime
		if s.marketRegime == "trend" {
			hysteresis = 0.01 // Wider hysteresis in trending market
		} else {
			hysteresis = 0.005 // Standard hysteresis in ranging market
		}
		s.logger.Debug("Hysteresis: %.2f", hysteresis)

		// Conditions with hysteresis
		if cls < smaVal*(1-hysteresis) && s.isGoldenCross() {
			s.logger.Signal("Сигнал LONG: Close < SMA*(1-hysteresis) и Golden Cross")
			s.sendSignal(types.Signal{Kind: "STRATEGY_LONG", ClosePrice: cls, Time: time.Now()})
		}
		if cls > smaVal*(1+hysteresis) && s.isGoldenCross() {
			s.logger.Signal("Сигнал SHORT: Close > SMA*(1+hysteresis) и Golden Cross")
			s.sendSignal(types.Signal{Kind: "STRATEGY_SHORT", ClosePrice: cls, Time: time.Now()})
		}
	}
}

// sendSignal sends a signal to the strategy channel
func (s *Strategy) sendSignal(signal types.Signal) {
	// This would send to a channel that the trader goroutine would read from
}

// isGoldenCross checks if there's a golden cross
func (s *Strategy) isGoldenCross() bool {
	if len(s.closes) < 27 {
		s.logger.Debug("Недостаточно данных для MACD (требуется 27, получено %d)", len(s.closes))
		return false
	}

	prevData := s.closes[len(s.closes)-27:] // 27 elements
	currData := s.closes[len(s.closes)-26:] // 26 elements

	prevMacd, prevSignal := indicators.CalculateMACD(prevData)
	currMacd, currSignal := indicators.CalculateMACD(currData)

	s.logger.Debug("Previous MACD: %.2f, Signal: %.2f", prevMacd, prevSignal)
	s.logger.Debug("Current MACD: %.2f, Signal: %.2f", currMacd, currSignal)

	// Check for zero values
	if prevMacd == 0 || prevSignal == 0 || currMacd == 0 || currSignal == 0 {
		s.logger.Debug("Zero MACD values, ignoring signal")
		return false
	}

	// Golden cross condition: MACD crosses signal line from below
	if prevMacd < prevSignal && currMacd > currSignal {
		s.logger.Debug("Golden cross detected: MACD crossed signal line from below")
		return true
	} else {
		s.logger.Debug("Golden cross not detected")
		return false
	}
}

// DetectMarketRegime detects the current market regime (trend or range)
func (s *Strategy) DetectMarketRegime() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		s.detectMarketRegime()
	}
}

// detectMarketRegime implements the actual market regime detection logic
func (s *Strategy) detectMarketRegime() {
	// Log: Check length of closes
	s.logger.Debug("detectMarketRegime: Length of closes = %d", len(s.closes))

	// Check if we have enough data for analysis
	if len(s.closes) < 50 {
		s.logger.Debug("detectMarketRegime: Not enough data for market regime detection (need 50, got %d)", len(s.closes))
		return
	}

	// Log: Extract last 50 candles
	recent := s.closes[len(s.closes)-50:]
	s.logger.Debug("detectMarketRegime: Last 50 candles: %v", recent)

	// Calculate max and min price
	maxHigh := indicators.MaxSlice(recent)
	minLow := indicators.MinSlice(recent)
	s.logger.Debug("detectMarketRegime: maxHigh = %.2f, minLow = %.2f", maxHigh, minLow)

	// Calculate range percentage
	rangePerc := (maxHigh - minLow) / minLow * 100
	s.logger.Debug("detectMarketRegime: Market range: %.2f%% (max: %.2f, min: %.2f)", rangePerc, maxHigh, minLow)

	// Determine market regime
	trendThreshold := 3.0 // 3% over 50 candles
	if rangePerc > trendThreshold {
		s.marketRegime = "trend"
		s.logger.Debug("detectMarketRegime: Market regime: Trend (rangePerc = %.2f%% > %.1f%%)", rangePerc, trendThreshold)
	} else {
		s.marketRegime = "range"
		s.logger.Debug("detectMarketRegime: Market regime: Range (rangePerc = %.2f%% <= %.1f%%)", rangePerc, trendThreshold)
	}
}

// CalcTakeProfitATR calculates take profit based on ATR
func (s *Strategy) CalcTakeProfitATR(side string) float64 {
	atr := indicators.CalcATR(s.highs, s.lows, s.closes, 14)
	if atr == 0 {
		atr = 90
	}

	var tp float64
	if side == "LONG" {
		tp = s.wsHub.GetLastAskPrice() + atr*1.5
	} else if side == "SHORT" {
		tp = s.wsHub.GetLastBidPrice() - atr*1.5
	}

	if s.config.TickSize > 0 {
		tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
	}
	return tp
}

// CalcTakeProfitBB calculates take profit based on Bollinger Bands
func (s *Strategy) CalcTakeProfitBB(side string) float64 {
	if len(s.closes) < s.config.WindowSize {
		return 0
	}
	smaVal := indicators.SMA(s.closes)
	stdVal := indicators.StdDev(s.closes)
	var tp float64
	if side == "LONG" {
		tp = smaVal + s.config.BBMult*stdVal
	} else if side == "SHORT" {
		tp = smaVal - s.config.BBMult*stdVal
	}
	if s.config.TickSize > 0 {
		tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
	}
	return tp
}

// CalcTakeProfitVolume calculates take profit based on volume
func (s *Strategy) CalcTakeProfitVolume(side string, thresholdQty float64) float64 {
	var arr []struct{ p, sz float64 }

	asksMap := s.wsHub.GetAsksMap()
	bidsMap := s.wsHub.GetBidsMap()

	if side == "LONG" && len(asksMap) > 0 {
		for ps, sz := range asksMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, struct{ p, sz float64 }{p, sz})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].p < arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= thresholdQty {
				tp := v.p * 0.9995
				if s.config.TickSize > 0 {
					tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[len(arr)-1].p * 1.0005
			if s.config.TickSize > 0 {
				tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
			}
			return tp
		}
	} else if side == "SHORT" && len(bidsMap) > 0 {
		for ps, sz := range bidsMap {
			p, _ := strconv.ParseFloat(ps, 64)
			arr = append(arr, struct{ p, sz float64 }{p, sz})
		}
		sort.Slice(arr, func(i, j int) bool { return arr[i].p > arr[j].p })
		cum := 0.0
		for _, v := range arr {
			cum += v.sz
			if cum >= thresholdQty {
				tp := v.p * 0.9995
				if s.config.TickSize > 0 {
					tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
				}
				return tp
			}
		}
		if len(arr) > 0 {
			tp := arr[0].p * 0.9995
			if s.config.TickSize > 0 {
				tp = math.Round(tp/s.config.TickSize) * s.config.TickSize
			}
			return tp
		}
	}
	return 0
}

// CalcTakeProfitVoting calculates take profit using multiple indicators
func (s *Strategy) CalcTakeProfitVoting(side string) float64 {
	tpBB := s.CalcTakeProfitBB(side)
	tpATR := s.CalcTakeProfitATR(side)
	tpVol := s.CalcTakeProfitVolume(side, s.config.TpThresholdQty)

	switch side {
	case "LONG":
		return indicators.Max(tpBB, tpATR, tpVol)
	case "SHORT":
		return indicators.Min(tpBB, tpATR, tpVol)
	default:
		return 0
	}
}

// AdjustTPSL adjusts take profit and stop loss based on price movement
func (s *Strategy) AdjustTPSL(closePrice float64) {
	currentPos, err := s.api.GetPositionList()
	if err != nil {
		s.logger.Error("Error getting position for TP/SL adjustment: %v", err)
		return
	}

	if !currentPos.Exists || currentPos.Side != "LONG" {
		return
	}

	entry, err := s.positionManager.GetLastEntryPriceFromREST()
	if err != nil {
		s.logger.Error("Error getting entry price for TP/SL adjustment: %v", err)
		return
	}

	if entry == 0 {
		s.logger.Error("Could not get valid entry price for TP/SL adjustment")
		return
	}

	atr := indicators.CalcATR(s.highs, s.lows, s.closes, 14)
	if atr == 0 {
		s.logger.Debug("ATR is zero, skipping TP/SL adjustment")
		return
	}

	newTP := closePrice + atr*1.5
	if newTP <= currentPos.TakeProfit {
		return
	}

	newSL := entry + 0.5*(newTP-entry)
	if err := s.api.UpdatePositionTradingStop(currentPos.Side, newTP, newSL); err != nil {
		s.logger.Error("Error updating TP/SL: %v", err)
	} else {
		s.logger.Order("TP/SL updated ▶ TP %.2f  SL %.2f", newTP, newSL)
	}
}

// AdjustTPSLForShort adjusts take profit and stop loss for short positions
func (s *Strategy) AdjustTPSLForShort(closePrice float64) {
	currentPos, err := s.api.GetPositionList()
	if err != nil || !currentPos.Exists || currentPos.Side != "SHORT" {
		return
	}
	
	entry, err := s.positionManager.GetLastEntryPriceFromREST()
	if err != nil || entry == 0 {
		return
	}
	
	// TP = price - 0.2%
	newTP := closePrice * 0.998
	if newTP >= currentPos.TakeProfit*1.001 { // Protection against small changes
		return
	}
	
	// SL = 50% of the way from entry to TP
	newSL := entry - 0.5*(entry-newTP)
	if err := s.api.UpdatePositionTradingStop(currentPos.Side, newTP, newSL); err != nil {
		s.logger.Error("adjustTPSLForShort error: %v", err)
	} else {
		s.logger.Order("Recalculated TP/SL ▶ TP %.2f SL %.2f", newTP, newSL)
	}
}

// HandleSignal processes a trading signal
func (s *Strategy) HandleSignal(signal types.Signal) {
	s.signalStrength[signal.Kind]++

	// Log signal strength
	s.logger.Debug("Signal strength: %v", s.signalStrength)

	// Confirmation from multiple indicators
	if (s.signalStrength["SMA_LONG"] >= 2) && s.wsHub.CheckOrderbookStrength("LONG") {
		s.logger.Debug("Confirmed LONG signal: %d indicators", s.signalStrength["SMA_LONG"])
		s.handleLongSignal(signal.ClosePrice)
		s.resetSignalStrength()
	} else if (s.signalStrength["SMA_SHORT"] >= 2) && s.wsHub.CheckOrderbookStrength("SHORT") {
		s.logger.Debug("Confirmed SHORT signal: %d indicators", s.signalStrength["SMA_SHORT"])
		s.handleShortSignal(signal.ClosePrice)
		s.resetSignalStrength()
	}
}

// resetSignalStrength resets the signal strength map
func (s *Strategy) resetSignalStrength() {
	for k := range s.signalStrength {
		delete(s.signalStrength, k)
	}
}

// handleLongSignal processes a long signal
func (s *Strategy) handleLongSignal(closePrice float64) {
	currentPos, err := s.api.GetPositionList()
	if err != nil {
		s.logger.Error("Error getting position: %v", err)
		return
	}
	
	side := normalizeSide(currentPos.Side)
	newSide := normalizeSide("LONG")

	s.logger.Debug("Checking position: exists=%v, side=%s", currentPos.Exists, side)

	if !currentPos.Exists {
		s.logger.Debug("No open position, opening LONG")
		s.positionManager.OpenPosition(newSide, closePrice)
	} else if side == newSide {
		s.logger.Debug("Position already LONG, updating TP/SL")
		s.AdjustTPSL(closePrice)
	} else {
		s.logger.Debug("Changing side from %s to %s", side, newSide)
		s.positionManager.OpenPosition(newSide, closePrice)
	}
}

// handleShortSignal processes a short signal
func (s *Strategy) handleShortSignal(closePrice float64) {
	currentPos, err := s.api.GetPositionList()
	if err != nil {
		s.logger.Error("Error getting position: %v", err)
		return
	}
	
	if !currentPos.Exists {
		s.logger.Debug("No open position, opening SHORT")
		s.positionManager.OpenPosition("SHORT", closePrice)
	} else if currentPos.Side == "SHORT" {
		s.logger.Debug("Position already SHORT, updating TP/SL")
		s.AdjustTPSLForShort(closePrice)
	} else {
		s.logger.Debug("Changing side from %s to SHORT", currentPos.Side)
		s.positionManager.OpenPosition("SHORT", closePrice)
	}
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