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
	var starts, ends, limits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		q := r.URL.Query()
		starts = append(starts, q.Get("startTime"))
		ends = append(ends, q.Get("endTime"))
		limits = append(limits, q.Get("limit"))
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
	if len(starts) != callCount || len(ends) != callCount || len(limits) != callCount {
		t.Fatalf("unexpected query capture sizes starts=%d ends=%d limits=%d calls=%d", len(starts), len(ends), len(limits), callCount)
	}
	for i := range starts {
		if starts[i] == "" || ends[i] == "" {
			t.Fatalf("expected start/end on request %d, got start=%q end=%q", i+1, starts[i], ends[i])
		}
		if limits[i] != "200" {
			t.Fatalf("expected limit=200 on request %d, got %q", i+1, limits[i])
		}
	}
}

func TestFetchLatestClosedPnlUsesRemainingLimitWithoutTimeRange(t *testing.T) {
	callCount := 0
	var starts, ends, limits, cursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		q := r.URL.Query()
		starts = append(starts, q.Get("startTime"))
		ends = append(ends, q.Get("endTime"))
		limits = append(limits, q.Get("limit"))
		cursors = append(cursors, q.Get("cursor"))
		if callCount == 1 {
			_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"BTCUSDT","side":"Buy","closedPnl":"1","avgEntryPrice":"10","avgExitPrice":"11","qty":"1","createdTime":"1","updatedTime":"1","execFee":"0"},{"symbol":"BTCUSDT","side":"Sell","closedPnl":"2","avgEntryPrice":"12","avgExitPrice":"11","qty":"1","createdTime":"2","updatedTime":"2","execFee":"0"}],"nextPageCursor":"c1"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"list":[{"symbol":"BTCUSDT","side":"Buy","closedPnl":"3","avgEntryPrice":"13","avgExitPrice":"12","qty":"1","createdTime":"3","updatedTime":"3","execFee":"0"},{"symbol":"BTCUSDT","side":"Sell","closedPnl":"4","avgEntryPrice":"14","avgExitPrice":"13","qty":"1","createdTime":"4","updatedTime":"4","execFee":"0"}],"nextPageCursor":"c2"}}`))
	}))
	defer srv.Close()

	cfg := config.LoadConfig()
	cfg.DemoRESTHost = srv.URL
	cfg.APIKey = "k"
	cfg.APISecret = "s"
	client := api.NewRESTClient(cfg, nil)

	items, err := fetchLatestClosedPnl(client, "BTCUSDT", 3)
	if err != nil {
		t.Fatalf("fetchLatestClosedPnl error: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if callCount != 2 {
		t.Fatalf("expected 2 calls, got %d", callCount)
	}
	if items[0].Side != "Buy" || items[1].Side != "Sell" || items[2].Side != "Buy" {
		t.Fatalf("unexpected items sequence: %#v", items)
	}
	if limits[0] != "3" || limits[1] != "1" {
		t.Fatalf("expected limits [3 1], got %v", limits)
	}
	if starts[0] != "" || starts[1] != "" || ends[0] != "" || ends[1] != "" {
		t.Fatalf("expected no time range params, got starts=%v ends=%v", starts, ends)
	}
	if cursors[0] != "" || cursors[1] != "c1" {
		t.Fatalf("unexpected cursors: %v", cursors)
	}
}

func TestCalcPnLByPositionSide(t *testing.T) {
	tests := []struct {
		name         string
		positionSide string
		entry        float64
		exit         float64
		qty          float64
		wantGross    float64
	}{
		{
			name:         "long profit",
			positionSide: "Buy",
			entry:        100,
			exit:         110,
			qty:          1,
			wantGross:    10,
		},
		{
			name:         "short loss",
			positionSide: "Sell",
			entry:        100,
			exit:         110,
			qty:          1,
			wantGross:    -10,
		},
		{
			name:         "short profit",
			positionSide: "Sell",
			entry:        110,
			exit:         100,
			qty:          1,
			wantGross:    10,
		},
	}

	for _, tc := range tests {
		got := CalcPnLByPositionSide(tc.positionSide, tc.entry, tc.exit, tc.qty)
		if math.Abs(got-tc.wantGross) > 1e-9 {
			t.Fatalf("%s gross got %.6f want %.6f", tc.name, got, tc.wantGross)
		}
	}
}

func TestResolveNetPnLUsesExchangeValue(t *testing.T) {
	item := closedPnlItem{CurPnl: "1.2345"}
	net, fromExchange := resolveNetPnL(item, 2.0, 0.3, 0)
	if !fromExchange {
		t.Fatalf("expected exchange net source")
	}
	if math.Abs(net-1.2345) > 1e-9 {
		t.Fatalf("expected net 1.2345, got %.6f", net)
	}
}

func TestResolveNetPnLUsesExchangeZeroValue(t *testing.T) {
	item := closedPnlItem{CurPnl: "0"}
	net, fromExchange := resolveNetPnL(item, 2.0, 0.3, 0)
	if !fromExchange {
		t.Fatalf("expected exchange net source for explicit zero")
	}
	if math.Abs(net) > 1e-9 {
		t.Fatalf("expected zero net, got %.6f", net)
	}
}

func TestResolveNetPnLFallbackToCalculated(t *testing.T) {
	item := closedPnlItem{CurPnl: ""}
	tests := []struct {
		name        string
		gross       float64
		fee         float64
		fundingCost float64
	}{
		{name: "funding cost positive", gross: 2.0, fee: 0.3, fundingCost: 0.1},
		{name: "funding cost negative", gross: 2.0, fee: 0.3, fundingCost: -0.1},
	}
	for _, tc := range tests {
		net, fromExchange := resolveNetPnL(item, tc.gross, tc.fee, tc.fundingCost)
		if fromExchange {
			t.Fatalf("%s: expected calculated net source", tc.name)
		}
		expected := tc.gross - tc.fee - tc.fundingCost
		if math.Abs(net-expected) > 1e-9 {
			t.Fatalf("%s: expected net %.6f, got %.6f", tc.name, expected, net)
		}
	}
}

func TestPositionSideFromCloseSide(t *testing.T) {
	tests := []struct {
		closeSide string
		want      string
	}{
		{closeSide: "Buy", want: "Sell"},
		{closeSide: "Sell", want: "Buy"},
		{closeSide: " buy ", want: "Sell"},
		{closeSide: "unknown", want: ""},
	}
	for _, tc := range tests {
		got := positionSideFromCloseSide(tc.closeSide)
		if got != tc.want {
			t.Fatalf("closeSide=%q got %q want %q", tc.closeSide, got, tc.want)
		}
	}
}

func TestLogSideFromPositionSide(t *testing.T) {
	tests := []struct {
		positionSide string
		want         string
	}{
		{positionSide: "Buy", want: "LONG"},
		{positionSide: "Sell", want: "SHORT"},
		{positionSide: " buy ", want: "LONG"},
		{positionSide: "unknown", want: "UNKNOWN"},
	}
	for _, tc := range tests {
		got := logSideFromPositionSide(tc.positionSide)
		if got != tc.want {
			t.Fatalf("positionSide=%q got %q want %q", tc.positionSide, got, tc.want)
		}
	}
}

func TestCalcFees(t *testing.T) {
	t.Run("cum fees", func(t *testing.T) {
		item := closedPnlItem{
			CumEntryFee: "-0.4",
			CumExitFee:  "0.6",
		}
		entryFee, exitFee, totalFee := calcFees(item, 0, 0, 0, 0)
		if math.Abs(entryFee-0.4) > 1e-9 || math.Abs(exitFee-0.6) > 1e-9 || math.Abs(totalFee-1.0) > 1e-9 {
			t.Fatalf("cum fees got entry=%.6f exit=%.6f total=%.6f", entryFee, exitFee, totalFee)
		}
	})

	t.Run("exec fee fallback", func(t *testing.T) {
		item := closedPnlItem{ExecFee: "-0.7"}
		entryFee, exitFee, totalFee := calcFees(item, 0, 0, 0, 0)
		if math.Abs(entryFee) > 1e-9 || math.Abs(exitFee-0.7) > 1e-9 || math.Abs(totalFee-0.7) > 1e-9 {
			t.Fatalf("exec fee fallback got entry=%.6f exit=%.6f total=%.6f", entryFee, exitFee, totalFee)
		}
	})
}

func TestFundingSign(t *testing.T) {
	t.Run("negative funding reduces net", func(t *testing.T) {
		item := closedPnlItem{FundingFee: "-0.1"}
		fundingSigned, found := parseFunding(item)
		if !found {
			t.Fatalf("expected funding to be found")
		}
		calcNet := calcNetWithFundingSigned(2.0, 0.3, fundingSigned)
		if math.Abs(calcNet-1.6) > 1e-9 {
			t.Fatalf("calc net got %.6f want 1.6", calcNet)
		}
	})

	t.Run("positive funding increases net", func(t *testing.T) {
		item := closedPnlItem{FundingFee: "0.1"}
		fundingSigned, found := parseFunding(item)
		if !found {
			t.Fatalf("expected funding to be found")
		}
		calcNet := calcNetWithFundingSigned(2.0, 0.3, fundingSigned)
		if math.Abs(calcNet-1.8) > 1e-9 {
			t.Fatalf("calc net got %.6f want 1.8", calcNet)
		}
	})
}
