package strategy

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type captureSummaryLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *captureSummaryLogger) Debug(format string, v ...interface{})   {}
func (l *captureSummaryLogger) Warning(format string, v ...interface{}) {}
func (l *captureSummaryLogger) Error(format string, v ...interface{})   {}
func (l *captureSummaryLogger) Fatal(format string, v ...interface{})   {}
func (l *captureSummaryLogger) Sync() error                             { return nil }
func (l *captureSummaryLogger) ChangeLogLevel(level logging.LogLevel)   {}
func (l *captureSummaryLogger) Info(format string, v ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (l *captureSummaryLogger) findLine(prefix string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.HasPrefix(line, prefix) {
			return line, true
		}
	}
	return "", false
}

func (l *captureSummaryLogger) hasLineContaining(needle string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

func TestTradeCloseSummaryStructure(t *testing.T) {
	tr, pm := newPartialBERuleTestTrader()
	logCap := &captureSummaryLogger{}
	tr.Logger = logCap
	tr.Config.EnableTradeSummaryLog = true
	tr.Config.EnablePartialBERule = false
	tr.Config.EnableLifecycleID = false
	tr.State.ActiveTradeID = "trade-summary-1"

	pm.mu.Lock()
	pm.exists = true
	pm.side = "LONG"
	pm.qty = 1.0
	pm.entry = 100.0
	pm.tp = 110.0
	pm.sl = 95.0
	pm.mu.Unlock()
	entryEvt := models.ExecutionEvent{
		TradeID:      "trade-summary-1",
		ExecID:       "exec-open-1",
		OrderID:      "order-open-1",
		OrderLinkID:  "link-open-1",
		ExecSide:     "BUY",
		PositionSide: "LONG",
		ReduceOnly:   false,
		Qty:          1.0,
		Price:        100.0,
		ExecFee:      0.1,
		HasIsMaker:   true,
		IsMaker:      false,
	}
	if !tr.ProcessExecutionEvent(entryEvt, "ws") {
		t.Fatalf("open execution must be processed")
	}

	pm.mu.Lock()
	pm.exists = true
	pm.side = "LONG"
	pm.qty = 0.5
	pm.entry = 100.0
	pm.tp = 110.0
	pm.sl = 95.0
	pm.mu.Unlock()
	partialEvt := models.ExecutionEvent{
		TradeID:           "trade-summary-1",
		ExecID:            "exec-close-1",
		OrderID:           "order-close-1",
		OrderLinkID:       "link-close-1",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               0.5,
		Price:             105.0,
		ExecFee:           0.05,
		ExecPnl:           2.5,
		HasIsMaker:        true,
		IsMaker:           false,
		ClosedSize:        0.5,
		PositionSizeAfter: 0.5,
		CreateType:        "CreateByTakeProfit",
	}
	if !tr.ProcessExecutionEvent(partialEvt, "ws") {
		t.Fatalf("partial close execution must be processed")
	}

	pm.mu.Lock()
	pm.exists = false
	pm.side = ""
	pm.qty = 0
	pm.entry = 100.0
	pm.tp = 0
	pm.sl = 0
	pm.mu.Unlock()
	finalEvt := models.ExecutionEvent{
		TradeID:           "trade-summary-1",
		ExecID:            "exec-close-2",
		OrderID:           "order-close-2",
		OrderLinkID:       "link-close-2",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               0.5,
		Price:             95.0,
		ExecFee:           0.05,
		ExecPnl:           -2.5,
		HasIsMaker:        true,
		IsMaker:           true,
		ClosedSize:        0.5,
		PositionSizeAfter: 0,
		CreateType:        "CreateByStopLoss",
	}
	if !tr.ProcessExecutionEvent(finalEvt, "ws") {
		t.Fatalf("final close execution must be processed")
	}
	counters, _ := tr.State.RuntimeSnapshot()
	if counters.MakerFills != 1 || counters.TakerFills != 2 {
		t.Fatalf("unexpected maker/taker counters: %+v", counters)
	}
	if counters.TotalExecFee <= 0 {
		t.Fatalf("expected positive total exec fee, got %+v", counters)
	}
	if counters.AvgTradeDuration < 0 {
		t.Fatalf("avg trade duration must be non-negative, got %f", counters.AvgTradeDuration)
	}
	if counters.NetAfterFeePerTrade == 0 {
		t.Fatalf("expected net_after_fee_per_trade to be updated, got %+v", counters)
	}

	line, ok := logCap.findLine("trade_close_summary ")
	if !ok {
		t.Fatalf("expected trade_close_summary log line, got lines=%v", logCap.lines)
	}
	payload := strings.TrimPrefix(line, "trade_close_summary ")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("unmarshal trade_close_summary: %v payload=%s", err, payload)
	}

	for _, key := range []string{
		"traceKey",
		"net_source",
		"gross_calc",
		"gross_source",
		"fee_open_alloc",
		"fee_close",
		"fee_total",
		"funding_signed",
		"realised_delta_cum",
		"legs_count",
		"duration_sec",
		"symbol",
		"tradeId",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing %s in summary: %v", key, decoded)
		}
	}
	if decoded["traceKey"] != "trade-summary-1" {
		t.Fatalf("unexpected traceKey: %v", decoded["traceKey"])
	}
	if decoded["net_source"] != "calculated" {
		t.Fatalf("unexpected net_source: %v", decoded["net_source"])
	}
	if decoded["close_reason"] != "sl" {
		t.Fatalf("unexpected close_reason: %v", decoded["close_reason"])
	}
	if decoded["gross_source"] != "position_side" {
		t.Fatalf("unexpected gross_source: %v", decoded["gross_source"])
	}
}

func TestAnomalyGrossSignMismatch(t *testing.T) {
	if !hasGrossSignMismatch(2.1, -2.1) {
		t.Fatalf("expected mismatch when signs differ")
	}
	if hasGrossSignMismatch(0, -2.1) {
		t.Fatalf("zero execPnl must not trigger mismatch")
	}
	if hasGrossSignMismatch(2.1, 0) {
		t.Fatalf("zero gross must not trigger mismatch")
	}
	if hasGrossSignMismatch(2.1, 2.1) {
		t.Fatalf("same sign must not trigger mismatch")
	}
}

func TestLastWinUpdatedOnProfitableClose(t *testing.T) {
	tr, pm := newPartialBERuleTestTrader()
	logCap := &captureSummaryLogger{}
	tr.Logger = logCap
	tr.Config.EnableTradeSummaryLog = false
	tr.Config.EnablePartialBERule = false
	tr.State.ActiveTradeID = "trade-win-1"

	pm.mu.Lock()
	pm.exists = true
	pm.side = "LONG"
	pm.qty = 1.0
	pm.entry = 100.0
	pm.tp = 110.0
	pm.sl = 95.0
	pm.mu.Unlock()
	openEvt := models.ExecutionEvent{
		TradeID:      "trade-win-1",
		ExecID:       "exec-open-win-1",
		OrderID:      "order-open-win-1",
		OrderLinkID:  "link-open-win-1",
		ExecSide:     "BUY",
		PositionSide: "LONG",
		ReduceOnly:   false,
		Qty:          1.0,
		Price:        100.0,
		ExecFee:      0.05,
	}
	if !tr.ProcessExecutionEvent(openEvt, "ws") {
		t.Fatalf("open execution must be processed")
	}

	pm.mu.Lock()
	pm.exists = false
	pm.side = ""
	pm.qty = 0
	pm.entry = 100.0
	pm.tp = 0
	pm.sl = 0
	pm.mu.Unlock()
	closeEvt := models.ExecutionEvent{
		TradeID:           "trade-win-1",
		ExecID:            "exec-close-win-1",
		OrderID:           "order-close-win-1",
		OrderLinkID:       "link-close-win-1",
		ExecSide:          "SELL",
		PositionSide:      "LONG",
		ReduceOnly:        true,
		Qty:               1.0,
		Price:             101.0,
		ExecFee:           0.05,
		ExecPnl:           1.0,
		ClosedSize:        1.0,
		PositionSizeAfter: 0,
		CreateType:        "CreateByTakeProfit",
	}
	if !tr.ProcessExecutionEvent(closeEvt, "ws") {
		t.Fatalf("close execution must be processed")
	}

	got := tr.State.LastWinMovePctForSide("LONG")
	if got <= 0 {
		t.Fatalf("expected last win move pct to be updated, got %.8f", got)
	}
	if math.Abs(got-0.01) > 1e-9 {
		t.Fatalf("unexpected last win move pct %.8f want 0.01", got)
	}
	if !logCap.hasLineContaining("\"event\":\"last_win_updated\"") {
		t.Fatalf("expected last_win_updated trade_event, got lines=%v", logCap.lines)
	}
}
