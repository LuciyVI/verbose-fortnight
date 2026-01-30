package main

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
)

func TestParseTimeMsHandlesSeconds(t *testing.T) {
	ts := parseTimeMs("1700000000") // seconds
	if ts.Year() < 2023 {
		t.Fatalf("expected converted seconds timestamp, got %v", ts)
	}
}

func TestFetchClosedPnlPagination(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"BTCUSDT","side":"Buy","closedPnl":"1","avgEntryPrice":"10","avgExitPrice":"11","qty":"1","createdTime":"1","updatedTime":"1","execFee":"0"}],"nextPageCursor":"c1"}}`))
		} else {
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"BTCUSDT","side":"Sell","closedPnl":"2","avgEntryPrice":"12","avgExitPrice":"11","qty":"1","createdTime":"2","updatedTime":"2","execFee":"0"}],"nextPageCursor":""}}`))
		}
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	client := api.NewRESTClient(cfg, nil)

	now := time.Now().UnixMilli()
	items, err := fetchClosedPnl(client, "BTCUSDT", now-1000, now)
	if err != nil {
		t.Fatalf("fetchClosedPnl error: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Side != "Buy" || items[1].Side != "Sell" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestCalcPnLShortProfit(t *testing.T) {
	entry := 94000.0
	exit := 92308.2
	qty := 0.001
	fee := 0.1100

	gross, net := CalcPnL("Sell", entry, exit, qty, fee)
	if math.Abs(net-1.5818) > 1e-4 {
		t.Fatalf("short profit net got %.4f want 1.5818", net)
	}
	if gross <= 0 {
		t.Fatalf("short profit gross should be positive, got %.4f", gross)
	}
}

func TestCalcPnLShortLoss(t *testing.T) {
	entry := 90000.0
	exit := 93330.0
	qty := 0.001
	fee := 0.1200

	gross, net := CalcPnL("Sell", entry, exit, qty, fee)
	if math.Abs(net-(-3.4500)) > 1e-4 {
		t.Fatalf("short loss net got %.4f want -3.4500", net)
	}
	if gross >= 0 {
		t.Fatalf("short loss gross should be negative, got %.4f", gross)
	}
}

func TestCalcPnLLongProfit(t *testing.T) {
	gross, net := CalcPnL("Buy", 100, 110, 1, 0.5)
	if math.Abs(gross-10) > 1e-9 {
		t.Fatalf("long profit gross got %.4f want 10.0000", gross)
	}
	if math.Abs(net-9.5) > 1e-9 {
		t.Fatalf("long profit net got %.4f want 9.5000", net)
	}
}

func TestCalcPnLLongLoss(t *testing.T) {
	gross, net := CalcPnL("Buy", 110, 100, 1, 0.5)
	if math.Abs(gross-(-10)) > 1e-9 {
		t.Fatalf("long loss gross got %.4f want -10.0000", gross)
	}
	if math.Abs(net-(-10.5)) > 1e-9 {
		t.Fatalf("long loss net got %.4f want -10.5000", net)
	}
}

func TestCalcPnLZeroMove(t *testing.T) {
	gross, net := CalcPnL("Buy", 100, 100, 1, 0.25)
	if math.Abs(gross) > 1e-9 {
		t.Fatalf("zero move gross got %.6f want 0", gross)
	}
	if math.Abs(net-(-0.25)) > 1e-9 {
		t.Fatalf("zero move net got %.6f want -0.25", net)
	}
}

func TestCalcGrossSymmetryBySide(t *testing.T) {
	entry := 100.0
	exit := 110.0
	qty := 1.0

	longGross, longNet := CalcPnL("Buy", entry, exit, qty, 0.1)
	shortGross, shortNet := CalcPnL("Sell", entry, exit, qty, 0.1)
	if math.Abs(longGross+shortGross) > 1e-9 {
		t.Fatalf("expected gross symmetry, got long %.4f short %.4f", longGross, shortGross)
	}
	if math.Abs(longNet+shortNet+0.2) > 1e-9 {
		t.Fatalf("expected net symmetry with fees, got long %.4f short %.4f", longNet, shortNet)
	}
}
