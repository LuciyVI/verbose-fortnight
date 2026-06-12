package strategy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	orderbookDiagnosticEvent      = "orderbook_diagnostic_snapshot"
	orderbookDiagnosticLiveSource = "live_orderbook_snapshot"
	orderbookDiagnosticReplay     = "replay_orderbook_snapshot"
)

type OrderbookDiagnosticSnapshot struct {
	Timestamp             time.Time `json:"timestamp"`
	Symbol                string    `json:"symbol"`
	Available             bool      `json:"orderbook_available"`
	Reason                string    `json:"reason,omitempty"`
	BestBid               float64   `json:"best_bid,omitempty"`
	BestAsk               float64   `json:"best_ask,omitempty"`
	SpreadBps             float64   `json:"spread_bps,omitempty"`
	BidSizeTop            float64   `json:"bid_size_top,omitempty"`
	AskSizeTop            float64   `json:"ask_size_top,omitempty"`
	BidAskImbalance       float64   `json:"bid_ask_imbalance,omitempty"`
	OrderbookRatio        float64   `json:"orderbook_ratio,omitempty"`
	DepthLevels           int       `json:"depth_levels,omitempty"`
	Source                string    `json:"source"`
	OrderbookHostile      bool      `json:"orderbook_hostile,omitempty"`
	OrderbookAllowed      bool      `json:"orderbook_allowed,omitempty"`
	OrderbookRejectReason string    `json:"orderbook_reject_reason,omitempty"`
}

func ReversalOrderbookDiagnosticFromInput(symbol string, ts time.Time, input ReversalOrderbookInput, source string) OrderbookDiagnosticSnapshot {
	if source == "" {
		source = orderbookDiagnosticReplay
	}
	snap := OrderbookDiagnosticSnapshot{
		Timestamp:       ts,
		Symbol:          symbol,
		Available:       input.Available,
		Reason:          input.UnavailableReason,
		BestBid:         input.BestBid,
		BestAsk:         input.BestAsk,
		SpreadBps:       input.SpreadBps,
		BidSizeTop:      input.BestBidSize,
		AskSizeTop:      input.BestAskSize,
		BidAskImbalance: input.Imbalance,
		OrderbookRatio:  input.Ratio,
		DepthLevels:     input.DepthLevels,
		Source:          source,
	}
	if !snap.Available && snap.Reason == "" {
		snap.Reason = ReversalReasonOrderbookNoData
	}
	return snap
}

func (s OrderbookDiagnosticSnapshot) Event() map[string]any {
	return map[string]any{
		"event":                   orderbookDiagnosticEvent,
		"timestamp":               s.Timestamp,
		"time":                    s.Timestamp,
		"symbol":                  s.Symbol,
		"orderbook_available":     s.Available,
		"reason":                  s.Reason,
		"unavailable_reason":      s.Reason,
		"best_bid":                s.BestBid,
		"best_ask":                s.BestAsk,
		"best_bid_size":           s.BidSizeTop,
		"best_ask_size":           s.AskSizeTop,
		"spread_bps":              s.SpreadBps,
		"bid_size_top":            s.BidSizeTop,
		"ask_size_top":            s.AskSizeTop,
		"bid_ask_imbalance":       s.BidAskImbalance,
		"orderbook_ratio":         s.OrderbookRatio,
		"depth_levels":            s.DepthLevels,
		"source":                  s.Source,
		"orderbook_hostile":       s.OrderbookHostile,
		"orderbook_allowed":       s.OrderbookAllowed,
		"orderbook_reject_reason": s.OrderbookRejectReason,
		"dry_run":                 s.Source != orderbookDiagnosticLiveSource,
		"order_sent":              false,
	}
}

func (t *Trader) currentOrderbookDiagnosticSnapshot(ts time.Time, source string) OrderbookDiagnosticSnapshot {
	if t == nil || t.State == nil {
		return OrderbookDiagnosticSnapshot{
			Timestamp: ts,
			Source:    source,
			Reason:    ReversalReasonOrderbookNoData,
		}
	}
	symbol := ""
	if t.Config != nil {
		symbol = t.Config.Symbol
	}
	input := t.reversalOrderbookInput()
	if !input.Available && input.UnavailableReason == "" {
		input.UnavailableReason = ReversalReasonOrderbookNoData
	}
	return ReversalOrderbookDiagnosticFromInput(symbol, ts, input, source)
}

func (t *Trader) orderbookSnapshotJSONLPath() string {
	if t == nil || t.Config == nil {
		return ""
	}
	return strings.TrimSpace(t.Config.OrderbookSnapshotJSONLPath)
}

func (t *Trader) orderbookSnapshotInterval() time.Duration {
	if t == nil || t.Config == nil || t.Config.OrderbookSnapshotIntervalMS <= 0 {
		return time.Second
	}
	return time.Duration(t.Config.OrderbookSnapshotIntervalMS) * time.Millisecond
}

// StartOrderbookSnapshotRecorder writes periodic diagnostic-only top-book snapshots when configured.
func (t *Trader) StartOrderbookSnapshotRecorder(ctx context.Context) bool {
	if t.orderbookSnapshotJSONLPath() == "" {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	interval := t.orderbookSnapshotInterval()
	go func() {
		t.RecordLiveOrderbookDiagnosticSnapshot(t.now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ts := <-ticker.C:
				t.RecordLiveOrderbookDiagnosticSnapshot(ts.UTC())
			}
		}
	}()
	return true
}

// RecordLiveOrderbookDiagnosticSnapshot appends one live top-book diagnostic snapshot when JSONL recording is configured.
func (t *Trader) RecordLiveOrderbookDiagnosticSnapshot(ts time.Time) bool {
	path := t.orderbookSnapshotJSONLPath()
	if path == "" {
		return false
	}
	if ts.IsZero() {
		ts = t.now()
	}
	snap := t.currentOrderbookDiagnosticSnapshot(ts.UTC(), orderbookDiagnosticLiveSource)
	if err := appendOrderbookDiagnosticJSONL(path, snap.Event()); err != nil {
		if t != nil && t.Logger != nil {
			t.Logger.Warning("orderbook diagnostic snapshot jsonl write failed: %v", err)
		}
		return false
	}
	if t != nil && t.State != nil {
		t.State.RecordOrderbookSnapshotHealth(snap.Available, snap.Timestamp, snap.Reason)
	}
	t.logOrderbookDiagnosticSnapshot(snap)
	return true
}

func (t *Trader) logReversalOrderbookDiagnosticSnapshot(ts time.Time) {
	if t == nil || t.Logger == nil || t.Config == nil || !t.Config.ReversalLong.Enabled {
		return
	}
	snap := t.currentOrderbookDiagnosticSnapshot(ts, orderbookDiagnosticLiveSource)
	t.logOrderbookDiagnosticSnapshot(snap)
}

func (t *Trader) logOrderbookDiagnosticSnapshot(snap OrderbookDiagnosticSnapshot) {
	if t == nil || t.Logger == nil {
		return
	}
	t.Logger.Debug("event=%s timestamp=%s symbol=%s source=%s orderbook_available=%t reason=%s best_bid=%.8f best_ask=%.8f spread_bps=%.4f bid_size_top=%.4f ask_size_top=%.4f bid_ask_imbalance=%.4f orderbook_ratio=%.4f depth_levels=%d order_sent=false",
		orderbookDiagnosticEvent,
		snap.Timestamp.Format(time.RFC3339Nano),
		snap.Symbol,
		snap.Source,
		snap.Available,
		snap.Reason,
		snap.BestBid,
		snap.BestAsk,
		snap.SpreadBps,
		snap.BidSizeTop,
		snap.AskSizeTop,
		snap.BidAskImbalance,
		snap.OrderbookRatio,
		snap.DepthLevels,
	)
}

func appendOrderbookDiagnosticJSONL(path string, event map[string]any) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	writeErr := enc.Encode(event)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}
