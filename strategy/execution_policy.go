package strategy

import "strings"

// ExecutionPolicy encapsulates entry/reduce order placement behavior.
type ExecutionPolicy interface {
	PlaceEntry(side string, qty float64, lifecycleID string) (orderID, orderLinkID string, err error)
	PlaceReduce(side string, qty float64, lifecycleID, leg string) (orderID, orderLinkID string, err error)
}

// DefaultPolicy preserves the legacy execution path.
type DefaultPolicy struct {
	trader *Trader
}

func (p *DefaultPolicy) PlaceEntry(side string, qty float64, lifecycleID string) (string, string, error) {
	if p == nil || p.trader == nil {
		return "", "", nil
	}
	return p.trader.placeMarketOrderWithLifecycle(side, qty, false, lifecycleID, "entry")
}

func (p *DefaultPolicy) PlaceReduce(side string, qty float64, lifecycleID, leg string) (string, string, error) {
	if p == nil || p.trader == nil {
		return "", "", nil
	}
	leg = strings.TrimSpace(leg)
	if leg == "" {
		leg = "clos"
	}
	return p.trader.placeMarketOrderWithLifecycle(side, qty, true, lifecycleID, leg)
}

// MakerFirstPolicy uses post-only limit first and falls back to market on timeout.
type MakerFirstPolicy struct {
	trader *Trader
}

func (p *MakerFirstPolicy) PlaceEntry(side string, qty float64, lifecycleID string) (string, string, error) {
	if p == nil || p.trader == nil {
		return "", "", nil
	}
	return p.trader.placeMakerFirstOrderWithFallback(side, qty, false, lifecycleID, "entry")
}

func (p *MakerFirstPolicy) PlaceReduce(side string, qty float64, lifecycleID, leg string) (string, string, error) {
	if p == nil || p.trader == nil {
		return "", "", nil
	}
	leg = strings.TrimSpace(leg)
	if leg == "" {
		leg = "clos"
	}
	return p.trader.placeMakerFirstOrderWithFallback(side, qty, true, lifecycleID, leg)
}

func (t *Trader) executionPolicy() ExecutionPolicy {
	if t == nil || t.Config == nil {
		return &DefaultPolicy{trader: t}
	}
	if t.Config.EnableMakerFirst {
		return &MakerFirstPolicy{trader: t}
	}
	return &DefaultPolicy{trader: t}
}

func (t *Trader) placeOrderWithExecutionPolicy(side string, qty float64, reduceOnly bool, lifecycleID, leg string) (orderID, orderLinkID string, err error) {
	policy := t.executionPolicy()
	if reduceOnly {
		return policy.PlaceReduce(side, qty, lifecycleID, leg)
	}
	return policy.PlaceEntry(side, qty, lifecycleID)
}
