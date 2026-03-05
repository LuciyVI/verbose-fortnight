package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadReportCSV(path, symbol string, loc *time.Location) ([]ReportTrade, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("read report header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[normalizeCSVHeader(h)] = i
	}

	required := []string{"time", "qty", "entry", "exit", "grosspnl", "fee", "netpnl"}
	for _, key := range required {
		if _, ok := col[key]; !ok {
			return nil, fmt.Errorf("report missing required column %q", key)
		}
	}

	trades := make([]ReportTrade, 0, 32)
	idx := 0
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read report row: %w", err)
		}
		if len(rec) == 0 {
			continue
		}
		idx++
		rt, ok := parseReportTradeRow(rec, idx, symbol, loc, col)
		if ok {
			trades = append(trades, rt)
		}
	}
	return trades, nil
}

func parseReportTradeRow(rec []string, idx int, symbol string, loc *time.Location, col map[string]int) (ReportTrade, bool) {
	get := func(key string) string {
		i, ok := col[key]
		if !ok || i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	ts, ok := parseReportTime(get("time"), loc)
	if !ok {
		return ReportTrade{}, false
	}

	closeSide := firstNonEmpty(get("closeside"), get("close"), get("side"))
	positionSide := firstNonEmpty(get("positionside"), inferPositionSide(closeSide))
	funding := asFloat(get("fundingsigned"))

	return ReportTrade{
		Index:         idx,
		Time:          ts,
		Symbol:        symbol,
		CloseSide:     closeSide,
		PositionSide:  positionSide,
		Qty:           asFloat(get("qty")),
		Entry:         asFloat(get("entry")),
		Exit:          asFloat(get("exit")),
		Gross:         asFloat(get("grosspnl")),
		Fee:           asFloat(get("fee")),
		FundingSigned: funding,
		Net:           asFloat(get("netpnl")),
	}, true
}

func normalizeCSVHeader(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	repl := strings.NewReplacer(" ", "", "_", "", "-", "", "(", "", ")", "")
	return repl.Replace(s)
}

func parseReportTime(raw string, loc *time.Location) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if ts, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return ts.UTC(), true
		}
	}
	// allow unix ms as string
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
		if ms < 1e12 {
			ms *= 1000
		}
		return time.UnixMilli(ms).UTC(), true
	}
	return time.Time{}, false
}

func parseTimeFlag(raw string, loc *time.Location) (time.Time, error) {
	if ts, ok := parseReportTime(raw, loc); ok {
		return ts, nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format: %q", raw)
}

func inferPositionSide(closeSide string) string {
	switch strings.ToLower(strings.TrimSpace(closeSide)) {
	case "buy", "long":
		return "SHORT"
	case "sell", "short":
		return "LONG"
	default:
		return ""
	}
}
