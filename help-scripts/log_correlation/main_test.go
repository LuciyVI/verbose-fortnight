package main

import (
	"bytes"
	"math"
	"testing"
)

func TestExtractOrderbookFeatures(t *testing.T) {
	payload := []byte(`{"topic":"orderbook.50.BTCUSDT","type":"delta","ts":1000,"data":{"s":"BTCUSDT","b":[["100","1.5"],["99.5","0.5"]],"a":[["100.5","2"],["101","3"]],"u":10,"seq":20},"cts":900}`)
	features, topic, err := extractFeatures(payload)
	if err != nil {
		t.Fatalf("extractFeatures error: %v", err)
	}
	if topic != "orderbook.50.BTCUSDT" {
		t.Fatalf("unexpected topic: %s", topic)
	}

	assertFloat(t, features["msg_ts"], 1000)
	assertFloat(t, features["msg_cts"], 900)
	assertFloat(t, features["ob_bid_levels"], 2)
	assertFloat(t, features["ob_ask_levels"], 2)
	assertFloat(t, features["ob_bid_qty_sum"], 2.0)
	assertFloat(t, features["ob_ask_qty_sum"], 5.0)
	assertFloat(t, features["ob_bid_notional_sum"], 199.75)
	assertFloat(t, features["ob_ask_notional_sum"], 504.0)
	assertFloat(t, features["ob_best_bid"], 100.0)
	assertFloat(t, features["ob_best_ask"], 100.5)
	assertFloat(t, features["ob_spread"], 0.5)
	assertFloat(t, features["ob_mid"], 100.25)
	assertFloat(t, features["ob_bid_qty_avg"], 1.0)
	assertFloat(t, features["ob_ask_qty_avg"], 2.5)
	assertFloat(t, features["ob_bid_vwap"], 99.875)
	assertFloat(t, features["ob_ask_vwap"], 100.8)
	assertFloat(t, features["ob_imbalance"], (2.0-5.0)/(2.0+5.0))
}

func TestExtractKlineFeatures(t *testing.T) {
	payload := []byte(`{"topic":"kline.1.BTCUSDT","data":[{"start":10,"end":20,"interval":"1","open":"100","close":"110","high":"120","low":"90","volume":"5","turnover":"550","confirm":false,"timestamp":15}],"ts":25,"type":"snapshot"}`)
	features, topic, err := extractFeatures(payload)
	if err != nil {
		t.Fatalf("extractFeatures error: %v", err)
	}
	if topic != "kline.1.BTCUSDT" {
		t.Fatalf("unexpected topic: %s", topic)
	}

	assertFloat(t, features["msg_ts"], 25)
	assertFloat(t, features["kline_start"], 10)
	assertFloat(t, features["kline_end"], 20)
	assertFloat(t, features["kline_duration"], 10)
	assertFloat(t, features["kline_timestamp"], 15)
	assertFloat(t, features["kline_open"], 100)
	assertFloat(t, features["kline_close"], 110)
	assertFloat(t, features["kline_high"], 120)
	assertFloat(t, features["kline_low"], 90)
	assertFloat(t, features["kline_range"], 30)
	assertFloat(t, features["kline_body"], 10)
	assertFloat(t, features["kline_mid"], 105)
	assertFloat(t, features["kline_volume"], 5)
	assertFloat(t, features["kline_turnover"], 550)
}

func TestCorrelationMatrixPerfect(t *testing.T) {
	rows := []map[string]float64{
		{"a": 1, "b": 2},
		{"a": 2, "b": 4},
		{"a": 3, "b": 6},
	}
	features, matrix := correlationMatrix(rows, 2)
	idxA := indexOf(features, "a")
	idxB := indexOf(features, "b")
	if idxA == -1 || idxB == -1 {
		t.Fatalf("expected features a and b, got %v", features)
	}
	if math.Abs(matrix[idxA][idxB]-1) > 1e-9 {
		t.Fatalf("expected correlation 1, got %v", matrix[idxA][idxB])
	}
	if math.Abs(matrix[idxA][idxA]-1) > 1e-9 {
		t.Fatalf("expected self-correlation 1, got %v", matrix[idxA][idxA])
	}
}

func TestCorrelationMatrixMissingPairs(t *testing.T) {
	rows := []map[string]float64{
		{"a": 1},
		{"b": 2},
		{"a": 2, "b": 4},
	}
	features, matrix := correlationMatrix(rows, 2)
	idxA := indexOf(features, "a")
	idxB := indexOf(features, "b")
	if idxA == -1 || idxB == -1 {
		t.Fatalf("expected features a and b, got %v", features)
	}
	if !math.IsNaN(matrix[idxA][idxB]) {
		t.Fatalf("expected NaN for insufficient pairs, got %v", matrix[idxA][idxB])
	}
}

func TestWritePNG(t *testing.T) {
	features := []string{"A", "B"}
	matrix := [][]float64{
		{1, 0.25},
		{0.25, 1},
	}
	var buf bytes.Buffer
	if err := writePNG(&buf, features, matrix); err != nil {
		t.Fatalf("writePNG error: %v", err)
	}
	data := buf.Bytes()
	if len(data) < 8 {
		t.Fatalf("png output too small: %d", len(data))
	}
	pngSig := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	for i := range pngSig {
		if data[i] != pngSig[i] {
			t.Fatalf("invalid png signature")
		}
	}
}

func assertFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected %.10f, got %.10f", want, got)
	}
}

func indexOf(list []string, target string) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}
