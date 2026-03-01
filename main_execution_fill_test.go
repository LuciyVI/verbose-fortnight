package main

import "testing"

func TestBuildExecutionFillLogPayload(t *testing.T) {
	item := map[string]interface{}{
		"symbol":           "BTCUSDT",
		"orderId":          "order-1",
		"orderLinkId":      "link-1",
		"execId":           "exec-1",
		"tradeId":          "trade-raw-1",
		"execSide":         "Sell",
		"positionSide":     "Buy",
		"execQty":          "0.013",
		"execPrice":        "64232.5",
		"execFee":          "0.5004",
		"feeRate":          "0.00055",
		"isMaker":          "1",
		"execPnl":          "-2.1255",
		"createType":       "CreateByTakeProfit",
		"stopOrderType":    "Stop",
		"reduceOnly":       true,
		"closeOnTrigger":   false,
		"triggerPrice":     "64230.0",
		"markPrice":        "64231.0",
		"indexPrice":       "64229.5",
		"orderType":        "Market",
		"timeInForce":      "IOC",
		"lastLiquidityInd": "Taker",
	}

	payload := buildExecutionFillLogPayload(item, "BTCUSDT", "ws", "lifecycle-1", false)

	if payload["source"] != "ws" {
		t.Fatalf("source got %v want ws", payload["source"])
	}
	if payload["lifecycleId"] != "lifecycle-1" {
		t.Fatalf("lifecycleId got %v", payload["lifecycleId"])
	}
	if payload["lifecycleInferred"] != false {
		t.Fatalf("lifecycleInferred got %v", payload["lifecycleInferred"])
	}
	if payload["orderId"] != "order-1" || payload["orderLinkId"] != "link-1" || payload["execId"] != "exec-1" {
		t.Fatalf("unexpected ids payload=%v", payload)
	}
	if payload["tradeId"] != "trade-raw-1" {
		t.Fatalf("tradeId got %v", payload["tradeId"])
	}
	if payload["side"] != "SHORT" || payload["positionSide"] != "LONG" {
		t.Fatalf("unexpected side mapping side=%v positionSide=%v", payload["side"], payload["positionSide"])
	}
	if payload["isMaker"] != true {
		t.Fatalf("isMaker got %v want true", payload["isMaker"])
	}
	if payload["lastLiquidityInd"] != "Taker" {
		t.Fatalf("lastLiquidityInd got %v", payload["lastLiquidityInd"])
	}
	if payload["ts"] == "" {
		t.Fatalf("expected non-empty ts")
	}
}

func TestBuildExecutionFillLogPayloadPositionFallback(t *testing.T) {
	item := map[string]interface{}{
		"execSide": "Buy",
		"execId":   "exec-fallback",
	}

	payload := buildExecutionFillLogPayload(item, "BTCUSDT", "backfill", "lifecycle-2", true)
	if payload["positionSide"] != "LONG" {
		t.Fatalf("positionSide fallback got %v want LONG", payload["positionSide"])
	}
	if payload["symbol"] != "BTCUSDT" {
		t.Fatalf("symbol fallback got %v want BTCUSDT", payload["symbol"])
	}
	if payload["lifecycleInferred"] != true {
		t.Fatalf("lifecycleInferred fallback got %v want true", payload["lifecycleInferred"])
	}
}
