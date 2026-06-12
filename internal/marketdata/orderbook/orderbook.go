package orderbook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	EventOrderbookDiagnosticSnapshot = "orderbook_diagnostic_snapshot"
	SourceLiveOrderbookSnapshot      = "live_orderbook_snapshot"
	DefaultBybitRESTHost             = "https://api.bybit.com"
	DefaultKind                      = "orderbook"
	DefaultInterval                  = "1s"
)

type Level struct {
	Price float64
	Size  float64
}

type Snapshot struct {
	Event              string    `json:"event"`
	Source             string    `json:"source"`
	Symbol             string    `json:"symbol"`
	Timestamp          time.Time `json:"timestamp"`
	Time               time.Time `json:"time"`
	OrderbookAvailable bool      `json:"orderbook_available"`
	Reason             string    `json:"reason"`
	BestBid            float64   `json:"best_bid"`
	BestAsk            float64   `json:"best_ask"`
	BestBidSize        float64   `json:"best_bid_size"`
	BestAskSize        float64   `json:"best_ask_size"`
	BidSizeTop         float64   `json:"bid_size_top"`
	AskSizeTop         float64   `json:"ask_size_top"`
	BidAskImbalance    float64   `json:"bid_ask_imbalance"`
	OrderbookRatio     float64   `json:"orderbook_ratio"`
	SpreadBps          float64   `json:"spread_bps"`
	DepthLevels        int       `json:"depth_levels"`
	UnavailableReason  string    `json:"unavailable_reason"`
	OrderSent          bool      `json:"order_sent"`
	DryRun             bool      `json:"dry_run"`
}

type BybitClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NormalizeSymbols(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		out = append(out, symbol)
	}
	return out
}

func NewBybitClient(baseURL string, httpClient *http.Client) *BybitClient {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBybitRESTHost
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &BybitClient{BaseURL: baseURL, HTTPClient: httpClient}
}

func (c *BybitClient) FetchOrderbook(ctx context.Context, symbol string, depth int) (Snapshot, error) {
	if c == nil {
		c = NewBybitClient("", nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if depth <= 0 {
		depth = 5
	}
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return Snapshot{}, fmt.Errorf("symbol is required")
	}
	q := url.Values{}
	q.Set("category", "linear")
	q.Set("symbol", symbol)
	q.Set("limit", strconv.Itoa(depth))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/v5/market/orderbook?"+q.Encode(), nil)
	if err != nil {
		return Snapshot{}, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return Snapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Snapshot{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Snapshot{}, fmt.Errorf("bybit orderbook http status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	snapshot, err := ParseBybitOrderbookResponse(body, symbol, depth, time.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func ParseBybitOrderbookResponse(raw []byte, fallbackSymbol string, depth int, fallbackTime time.Time) (Snapshot, error) {
	var response struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			Symbol string          `json:"s"`
			Bids   [][]string      `json:"b"`
			Asks   [][]string      `json:"a"`
			TS     json.RawMessage `json:"ts"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return Snapshot{}, err
	}
	if response.RetCode != 0 {
		return Snapshot{}, fmt.Errorf("bybit orderbook retCode %d: %s", response.RetCode, response.RetMsg)
	}
	symbol := strings.ToUpper(strings.TrimSpace(response.Result.Symbol))
	if symbol == "" {
		symbol = strings.ToUpper(strings.TrimSpace(fallbackSymbol))
	}
	ts, err := parseEventTime(response.Result.TS)
	if err != nil {
		return Snapshot{}, err
	}
	if ts.IsZero() {
		ts = fallbackTime
	}
	bids := parseLevels(response.Result.Bids)
	asks := parseLevels(response.Result.Asks)
	return SnapshotFromLevels(symbol, ts, bids, asks, depth, "")
}

func SnapshotFromLevels(symbol string, ts time.Time, bids, asks []Level, depth int, unavailableReason string) (Snapshot, error) {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	ts = ts.UTC()
	if depth <= 0 {
		depth = 5
	}
	snapshot := Snapshot{
		Event:              EventOrderbookDiagnosticSnapshot,
		Source:             SourceLiveOrderbookSnapshot,
		Symbol:             strings.ToUpper(strings.TrimSpace(symbol)),
		Timestamp:          ts,
		Time:               ts,
		DepthLevels:        depth,
		UnavailableReason:  strings.TrimSpace(unavailableReason),
		Reason:             strings.TrimSpace(unavailableReason),
		OrderSent:          false,
		DryRun:             false,
		OrderbookAvailable: false,
	}
	bids = cleanLevels(bids, true)
	asks = cleanLevels(asks, false)
	if len(bids) == 0 || len(asks) == 0 {
		if snapshot.UnavailableReason == "" {
			snapshot.UnavailableReason = "orderbook_data_not_available"
			snapshot.Reason = snapshot.UnavailableReason
		}
		return snapshot, nil
	}
	topBidDepth := sumTop(bids, depth)
	topAskDepth := sumTop(asks, depth)
	bestBid := bids[0]
	bestAsk := asks[0]
	snapshot.OrderbookAvailable = true
	snapshot.BestBid = bestBid.Price
	snapshot.BestAsk = bestAsk.Price
	snapshot.BestBidSize = bestBid.Size
	snapshot.BestAskSize = bestAsk.Size
	snapshot.BidSizeTop = topBidDepth
	snapshot.AskSizeTop = topAskDepth
	snapshot.UnavailableReason = ""
	snapshot.Reason = ""
	mid := (bestBid.Price + bestAsk.Price) / 2
	if mid > 0 {
		snapshot.SpreadBps = (bestAsk.Price - bestBid.Price) / mid * 10000
	}
	if topAskDepth > 0 {
		snapshot.OrderbookRatio = topBidDepth / topAskDepth
	}
	if topBidDepth+topAskDepth > 0 {
		snapshot.BidAskImbalance = (topBidDepth - topAskDepth) / (topBidDepth + topAskDepth)
	}
	if !snapshot.ValidTopBook() {
		return snapshot, fmt.Errorf("invalid top book for %s", snapshot.Symbol)
	}
	return snapshot, nil
}

func UnavailableSnapshot(symbol string, ts time.Time, depth int, reason string) Snapshot {
	if strings.TrimSpace(reason) == "" {
		reason = "orderbook_data_not_available"
	}
	snapshot, _ := SnapshotFromLevels(symbol, ts, nil, nil, depth, reason)
	return snapshot
}

func (s Snapshot) ValidTopBook() bool {
	return s.OrderbookAvailable &&
		s.BestBid > 0 &&
		s.BestAsk > 0 &&
		s.BestBidSize > 0 &&
		s.BestAskSize > 0 &&
		s.BestAsk >= s.BestBid
}

func (s Snapshot) MarshalJSONLine() ([]byte, error) {
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func ParseSnapshot(raw []byte) (Snapshot, bool, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Snapshot{}, false, nil
	}
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return Snapshot{}, false, err
	}
	event := rawString(fields["event"])
	if event != "" && event != EventOrderbookDiagnosticSnapshot {
		return Snapshot{}, false, nil
	}
	ts, err := parseEventTime(fields["timestamp"])
	if err != nil {
		return Snapshot{}, false, err
	}
	if ts.IsZero() {
		ts, err = parseEventTime(fields["time"])
		if err != nil {
			return Snapshot{}, false, err
		}
	}
	if ts.IsZero() {
		return Snapshot{}, false, fmt.Errorf("missing timestamp/time")
	}
	snapshot := Snapshot{
		Event:              firstNonEmpty(event, EventOrderbookDiagnosticSnapshot),
		Source:             rawString(fields["source"]),
		Symbol:             strings.ToUpper(rawString(fields["symbol"])),
		Timestamp:          ts,
		Time:               ts,
		OrderbookAvailable: rawBool(fields["orderbook_available"]),
		Reason:             rawString(fields["reason"]),
		BestBid:            rawFloat(fields["best_bid"]),
		BestAsk:            rawFloat(fields["best_ask"]),
		BestBidSize:        firstPositive(rawFloat(fields["best_bid_size"]), rawFloat(fields["bid_size_top"])),
		BestAskSize:        firstPositive(rawFloat(fields["best_ask_size"]), rawFloat(fields["ask_size_top"])),
		BidSizeTop:         firstPositive(rawFloat(fields["bid_size_top"]), rawFloat(fields["best_bid_size"])),
		AskSizeTop:         firstPositive(rawFloat(fields["ask_size_top"]), rawFloat(fields["best_ask_size"])),
		BidAskImbalance:    rawFloat(fields["bid_ask_imbalance"]),
		OrderbookRatio:     rawFloat(fields["orderbook_ratio"]),
		SpreadBps:          rawFloat(fields["spread_bps"]),
		DepthLevels:        rawInt(fields["depth_levels"]),
		UnavailableReason:  rawString(fields["unavailable_reason"]),
		OrderSent:          rawBool(fields["order_sent"]),
		DryRun:             rawBool(fields["dry_run"]),
	}
	if snapshot.Source == "" {
		snapshot.Source = SourceLiveOrderbookSnapshot
	}
	if snapshot.UnavailableReason == "" {
		snapshot.UnavailableReason = snapshot.Reason
	}
	if snapshot.Reason == "" {
		snapshot.Reason = snapshot.UnavailableReason
	}
	return snapshot, true, nil
}

func InTimeRange(ts, from, to time.Time) bool {
	ts = ts.UTC()
	if !from.IsZero() && ts.Before(from.UTC()) {
		return false
	}
	if !to.IsZero() && ts.After(to.UTC()) {
		return false
	}
	return true
}

func CSVHeader() []string {
	return []string{
		"event", "source", "symbol", "timestamp", "time", "orderbook_available",
		"best_bid", "best_ask", "best_bid_size", "best_ask_size",
		"bid_size_top", "ask_size_top", "bid_ask_imbalance", "orderbook_ratio",
		"spread_bps", "depth_levels", "unavailable_reason", "order_sent", "dry_run",
	}
}

func (s Snapshot) CSVRow() []string {
	return []string{
		s.Event,
		s.Source,
		s.Symbol,
		s.Timestamp.UTC().Format(time.RFC3339Nano),
		s.Time.UTC().Format(time.RFC3339Nano),
		strconv.FormatBool(s.OrderbookAvailable),
		formatFloat(s.BestBid),
		formatFloat(s.BestAsk),
		formatFloat(s.BestBidSize),
		formatFloat(s.BestAskSize),
		formatFloat(s.BidSizeTop),
		formatFloat(s.AskSizeTop),
		formatFloat(s.BidAskImbalance),
		formatFloat(s.OrderbookRatio),
		formatFloat(s.SpreadBps),
		strconv.Itoa(s.DepthLevels),
		s.UnavailableReason,
		strconv.FormatBool(s.OrderSent),
		strconv.FormatBool(s.DryRun),
	}
}

func parseLevels(raw [][]string) []Level {
	out := make([]Level, 0, len(raw))
	for _, pair := range raw {
		if len(pair) < 2 {
			continue
		}
		price, err1 := strconv.ParseFloat(strings.TrimSpace(pair[0]), 64)
		size, err2 := strconv.ParseFloat(strings.TrimSpace(pair[1]), 64)
		if err1 == nil && err2 == nil && price > 0 && size > 0 {
			out = append(out, Level{Price: price, Size: size})
		}
	}
	return out
}

func cleanLevels(levels []Level, bids bool) []Level {
	out := make([]Level, 0, len(levels))
	for _, level := range levels {
		if level.Price > 0 && level.Size > 0 && !math.IsNaN(level.Price) && !math.IsNaN(level.Size) {
			out = append(out, level)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if bids {
			return out[i].Price > out[j].Price
		}
		return out[i].Price < out[j].Price
	})
	return out
}

func sumTop(levels []Level, depth int) float64 {
	if depth <= 0 || depth > len(levels) {
		depth = len(levels)
	}
	total := 0.0
	for i := 0; i < depth; i++ {
		total += levels[i].Size
	}
	return total
}

func parseEventTime(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return ParseTime(text)
	}
	var number json.Number
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&number); err != nil {
		return time.Time{}, err
	}
	n, err := number.Int64()
	if err != nil {
		return time.Time{}, err
	}
	if n > 10_000_000_000 {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Unix(n, 0).UTC(), nil
}

func ParseTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return ts.UTC(), nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("time must be RFC3339 or unix milliseconds: %w", err)
	}
	if n > 10_000_000_000 {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Unix(n, 0).UTC(), nil
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.TrimSpace(value)
	}
	var anyValue any
	if err := json.Unmarshal(raw, &anyValue); err != nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(anyValue))
}

func rawBool(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	text := strings.ToLower(rawString(raw))
	return text == "true" || text == "1" || text == "yes"
}

func rawFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var number json.Number
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&number); err == nil {
		value, err := number.Float64()
		if err == nil {
			return value
		}
	}
	value, _ := strconv.ParseFloat(rawString(raw), 64)
	return value
}

func rawInt(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	value, _ = strconv.Atoi(rawString(raw))
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPositive(values ...float64) float64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
