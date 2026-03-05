package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseLogEventsFixtures(t *testing.T) {
	loc := time.UTC
	fixtures := []struct {
		name         string
		line         string
		wantType     string
		wantEvents   int
		wantSymbol   string
		wantTraceKey string
	}{
		{
			name:         "trade_close_summary",
			line:         `2026/02/25 00:44:12.123456 strategy.go:1 [INFO] trade_close_summary {"ts":"2026-02-25T00:44:12Z","ts_epoch_ms":1740444252123,"symbol":"BTCUSDT","traceKey":"lc_1","lifecycleId":"lc_1","tradeId":"t_1","orderId":"o_1","orderLinkId":"ol_1","execId":"e_1","positionSide":"LONG","qty_total":0.013,"qty_closed_total":0.013,"entry_vwap":64069,"exit_vwap":64232.5,"gross_calc":2.1255,"gross_source":"position_side","fee_open_alloc":0.5004,"fee_close":0.5004,"fee_total":1.0008,"funding_signed":0,"realised_delta_cum":-3.0312,"net_calc":1.1247,"net_exchange":-3.0312,"net_source":"exchange","close_reason":"tp","legs_count":2}`,
			wantType:     eventTradeCloseSummary,
			wantEvents:   1,
			wantSymbol:   "BTCUSDT",
			wantTraceKey: "lc_1",
		},
		{
			name:         "execution_fill",
			line:         `2026/02/25 00:44:10.000001 main.go:1 [INFO] execution_fill {"ts":"2026-02-25T00:44:10Z","symbol":"BTCUSDT","source":"ws","traceKey":"lc_1","lifecycleId":"lc_1","orderId":"o_1","orderLinkId":"ol_1","execId":"e_2","tradeId":"raw_1","side":"SELL","positionSide":"LONG","execQty":0.013,"execPrice":64232.5,"execFee":0.5004,"execPnl":-2.1255,"execTime":1740444250000,"reduceOnly":true,"createType":"CreateByTakeProfit","stopOrderType":"TakeProfit","isMaker":false}`,
			wantType:     eventExecutionFill,
			wantEvents:   1,
			wantSymbol:   "BTCUSDT",
			wantTraceKey: "lc_1",
		},
		{
			name:       "execution_list_response",
			line:       `2026/02/25 00:45:00.000000 api.go:1 [INFO] execution_list_response {"retCode":0,"list_len":2,"firstExecId":"e_10","lastExecId":"e_11","nextCursor":"c_2"}`,
			wantType:   eventExecutionListResp,
			wantEvents: 1,
		},
		{
			name:       "trade_event",
			line:       `2026/02/25 00:43:00.000000 strategy.go:1 [INFO] trade_event {"event":"entry_intent","symbol":"BTCUSDT","trace_key":"lc_1","lifecycle_id":"lc_1","trade_id":"t_1","side":"LONG","qty":0.013,"entry":64069}`,
			wantType:   eventTradeEvent,
			wantEvents: 1,
		},
		{
			name:       "position_list_response",
			line:       `2026/02/25 00:44:11.123456 api.go:1 [INFO] Received response from exchange for /v5/position/list: Status 200, Body: {"retCode":0,"result":{"list":[{"symbol":"BTCUSDT","side":"Buy","size":"0.013","avgPrice":"64069","curRealisedPnl":"-3.0312","cumRealisedPnl":"42.0"}]}}`,
			wantType:   eventPositionListResp,
			wantEvents: 1,
		},
		{
			name:       "position_ws_update",
			line:       `2026/02/25 00:44:11.123500 main.go:1 [INFO] Received position update from exchange: {"topic":"position","data":[{"symbol":"BTCUSDT","side":"Buy","size":"0.013","avgPrice":"64069","curRealisedPnl":"-3.0312","cumRealisedPnl":"42.0"}]}`,
			wantType:   eventPositionWSUpdate,
			wantEvents: 1,
		},
		{
			name:       "unrelated",
			line:       `2026/02/25 00:44:11.123500 main.go:1 [INFO] some unrelated line`,
			wantEvents: 0,
		},
		{
			name:       "bad_prefix",
			line:       `bad line`,
			wantEvents: 0,
		},
		{
			name:       "bad_json",
			line:       `2026/02/25 00:44:11.123500 main.go:1 [INFO] execution_fill {bad`,
			wantEvents: 0,
		},
		{
			name:         "legacy_execution_fill_text",
			line:         `2026/02/25 00:44:11.123501 main.go:1 [INFO] Execution fill: traceKey=lc_legacy tradeID=t_legacy execSide=SELL positionSide=LONG reduceOnly=true closedSize=0.0130 leavesQty=0.0000 positionSizeAfter=0.0000 execQty=0.0130 execPrice=64232.50 execFee=0.5004 execPnl=-2.1255 createType=CreateByTakeProfit stopOrderType=Stop execId=e_legacy orderId=o_legacy orderLinkId=ol_legacy`,
			wantType:     eventExecutionFill,
			wantEvents:   1,
			wantTraceKey: "lc_legacy",
		},
		{
			name:       "execution_fill_missing_trace",
			line:       `2026/02/25 00:44:10.000002 main.go:1 [INFO] execution_fill {"symbol":"BTCUSDT","execId":"e_3","execQty":0.01,"execPrice":64000}`,
			wantType:   eventExecutionFill,
			wantEvents: 1,
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			evs, ok := parseLogEvents(tc.line, "x.log", 1, loc)
			if tc.wantEvents == 0 {
				if ok && len(evs) > 0 {
					t.Fatalf("expected no events, got %+v", evs)
				}
				return
			}
			if !ok {
				t.Fatalf("expected parse ok")
			}
			if len(evs) != tc.wantEvents {
				t.Fatalf("events len got %d want %d", len(evs), tc.wantEvents)
			}
			if tc.wantType != "" && evs[0].Type != tc.wantType {
				t.Fatalf("event type got %s want %s", evs[0].Type, tc.wantType)
			}
			if tc.wantSymbol != "" && evs[0].Symbol != tc.wantSymbol {
				t.Fatalf("symbol got %s want %s", evs[0].Symbol, tc.wantSymbol)
			}
			if tc.wantTraceKey != "" && !strings.EqualFold(evs[0].TraceKey, tc.wantTraceKey) {
				t.Fatalf("traceKey got %s want %s", evs[0].TraceKey, tc.wantTraceKey)
			}
		})
	}
}

func TestPathLikelyInWindow(t *testing.T) {
	from := time.Date(2026, 2, 24, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 28, 23, 59, 0, 0, time.UTC)
	if !pathLikelyInWindow("logs/2026-02-25/trading_bot.log.gz", time.Time{}, from, to) {
		t.Fatalf("expected in-window path")
	}
	if pathLikelyInWindow("logs/2026-03-01/trading_bot.log.gz", time.Time{}, from, to) {
		t.Fatalf("expected out-of-window path")
	}
	mod := time.Date(2026, 2, 25, 12, 0, 0, 0, time.UTC)
	if !pathLikelyInWindow("logs/trading_bot.log", mod, from, to) {
		t.Fatalf("non-dated path with nearby mtime must be kept")
	}
	oldMod := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if pathLikelyInWindow("logs/trading_bot.log", oldMod, from, to) {
		t.Fatalf("non-dated old file must be skipped")
	}
}
