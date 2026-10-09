package order

import (
	"math"
	"sort"
	"strconv"

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

// FormatQty returns an empty string for invalid inputs and rounds down to the
// instrument step using decimal arithmetic (including non-power-of-ten steps).
func (om *OrderManager) FormatQty(qty, step float64) string {
	return formatQty(qty, step)
}

func (om *OrderManager) PlaceOrderMarket(side string, qty float64, reduceOnly bool) error {
	_, err := om.PlaceOrderMarketWithID(side, qty, reduceOnly)
	return err
}
func (om *OrderManager) PlaceOrderMarketWithID(side string, qty float64, reduceOnly bool) (string, error) {
	return om.PlaceOrderMarketWithLinkID(side, qty, reduceOnly, "")
}
func (om *OrderManager) PlaceOrderMarketWithLinkID(side string, qty float64, reduceOnly bool, link string) (string, error) {
	return om.placeOrder(side, qty, 0, reduceOnly, link, "Market", "IOC", false, false)
}
func (om *OrderManager) PlaceOrderLimitPostOnlyWithLinkID(side string, qty, price float64, reduceOnly bool, link string) (string, error) {
	return om.placeOrder(side, qty, price, reduceOnly, link, "Limit", "PostOnly", false, side == "Sell")
}
func (om *OrderManager) PlaceTakeProfitOrder(side string, qty, price float64) error {
	return om.PlaceTakeProfitOrderWithLink(side, qty, price, "")
}
func (om *OrderManager) PlaceTakeProfitOrderWithLink(side string, qty, price float64, link string) error {
	_, err := om.placeOrder(side, qty, price, true, link, "Limit", "GTC", false, side == "Buy")
	return err
}
func (om *OrderManager) PlaceStopLossOrder(side string, qty, price float64) error {
	return om.PlaceStopLossOrderWithLink(side, qty, price, "")
}
func (om *OrderManager) PlaceStopLossOrderWithLink(side string, qty, price float64, link string) error {
	_, err := om.placeOrder(side, qty, price, true, link, "Limit", "GTC", false, side == "Sell")
	return err
}
func (om *OrderManager) PlaceStopLoss(side string, qty, price float64) error {
	_, err := om.placeOrder(side, qty, price, true, "", "Market", "GTC", true, side == "Sell")
	return err
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
