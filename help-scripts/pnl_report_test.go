package main

import (
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
