package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/strategy"
)

const defaultOrderbookEnrichmentMaxStaleness = 1500 * time.Millisecond

type orderbookEnrichmentOptions struct {
	InputCSV       string
	OrderbookJSONL string
	OutputCSV      string
	Symbol         string
	MaxStaleness   time.Duration
}

type orderbookEnrichmentOutput struct {
	OutputCSV   string
	SummaryPath string
	Summary     orderbookEnrichmentSummary
}

type orderbookEnrichmentSummary struct {
	RowsTotal            int                             `json:"rows_total"`
	RowsEnriched         int                             `json:"rows_enriched"`
	OrderbookCoveragePct float64                         `json:"orderbook_coverage_pct"`
	MissingSnapshotCount int                             `json:"missing_snapshot_count"`
	StaleSnapshotCount   int                             `json:"stale_snapshot_count"`
	MinSpreadBps         float64                         `json:"min_spread_bps"`
	MaxSpreadBps         float64                         `json:"max_spread_bps"`
	AvgSpreadBps         float64                         `json:"avg_spread_bps"`
	Warnings             []string                        `json:"warnings,omitempty"`
	InputSnapshots       orderbookJSONLValidationSummary `json:"input_snapshots"`
}

const (
	orderbookValidationUsableForEnrichment      = "USABLE_FOR_ENRICHMENT"
	orderbookValidationDataGapReplayUnavailable = "DATA_GAP_ONLY_REPLAY_UNAVAILABLE"
	orderbookValidationNoValidSnapshots         = "NO_VALID_SNAPSHOTS"
)

type orderbookUnavailableReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

type orderbookJSONLValidationSummary struct {
	TotalSnapshots        int                               `json:"total_snapshots"`
	ValidSnapshots        int                               `json:"valid_snapshots"`
	UnavailableSnapshots  int                               `json:"unavailable_snapshots"`
	ReplaySnapshotCount   int                               `json:"replay_snapshot_count"`
	LiveSnapshotCount     int                               `json:"live_snapshot_count"`
	ValidCoveragePct      float64                           `json:"valid_coverage_pct"`
	Symbols               []string                          `json:"symbols"`
	TimeMin               time.Time                         `json:"time_min,omitempty"`
	TimeMax               time.Time                         `json:"time_max,omitempty"`
	TopUnavailableReasons []orderbookUnavailableReasonCount `json:"top_unavailable_reasons"`
	Recommendation        string                            `json:"recommendation"`
}

type orderbookEnrichmentNumber struct {
	Value   float64
	Present bool
}

type orderbookEnrichmentSnapshot struct {
	Time              time.Time
	Symbol            string
	Available         bool
	UnavailableReason string
	Source            string
	BestBid           orderbookEnrichmentNumber
	BestAsk           orderbookEnrichmentNumber
	BestBidSize       orderbookEnrichmentNumber
	BestAskSize       orderbookEnrichmentNumber
	BidAskImbalance   orderbookEnrichmentNumber
	SpreadBps         orderbookEnrichmentNumber
	OrderbookRatio    orderbookEnrichmentNumber
	DepthLevels       int
}

func runOrderbookEnrichment(opts orderbookEnrichmentOptions) (orderbookEnrichmentOutput, error) {
	if opts.InputCSV == "" {
		return orderbookEnrichmentOutput{}, fmt.Errorf("--enrich-input-csv is required")
	}
	if opts.OrderbookJSONL == "" {
		return orderbookEnrichmentOutput{}, fmt.Errorf("--enrich-orderbook-jsonl is required")
	}
	if opts.OutputCSV == "" {
		return orderbookEnrichmentOutput{}, fmt.Errorf("--enrich-output-csv is required")
	}
	if opts.MaxStaleness <= 0 {
		opts.MaxStaleness = defaultOrderbookEnrichmentMaxStaleness
	}

	candles, err := strategy.LoadReversalReplayCandlesCSV(opts.InputCSV)
	if err != nil {
		return orderbookEnrichmentOutput{}, fmt.Errorf("load enrich input csv: %w", err)
	}
	snapshotReport, err := validateOrderbookJSONL(opts.OrderbookJSONL, opts.Symbol)
	if err != nil {
		return orderbookEnrichmentOutput{}, err
	}
	snapshots, err := loadOrderbookEnrichmentSnapshots(opts.OrderbookJSONL, opts.Symbol)
	if err != nil {
		return orderbookEnrichmentOutput{}, err
	}
	summary, err := writeOrderbookEnrichedCSV(opts.OutputCSV, candles, snapshots, opts.MaxStaleness)
	if err != nil {
		return orderbookEnrichmentOutput{}, err
	}
	summary.InputSnapshots = snapshotReport
	if snapshotReport.Recommendation == orderbookValidationDataGapReplayUnavailable {
		summary.Warnings = append(summary.Warnings, "orderbook JSONL contains only replay_orderbook_snapshot events with orderbook_available=false; collect live_orderbook_snapshot diagnostics before enrichment")
	}
	summaryPath := orderbookEnrichmentSummaryPath(opts.OutputCSV)
	if err := writeJSONFile(summaryPath, summary); err != nil {
		return orderbookEnrichmentOutput{}, err
	}
	return orderbookEnrichmentOutput{
		OutputCSV:   opts.OutputCSV,
		SummaryPath: summaryPath,
		Summary:     summary,
	}, nil
}

func loadOrderbookEnrichmentSnapshots(path, symbol string) ([]orderbookEnrichmentSnapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open enrich orderbook jsonl: %w", err)
	}
	defer f.Close()

	var snapshots []orderbookEnrichmentSnapshot
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		snapshot, ok, err := parseOrderbookEnrichmentSnapshot([]byte(raw), symbol)
		if err != nil {
			return nil, fmt.Errorf("orderbook jsonl line %d: %w", lineNo, err)
		}
		if !ok || !snapshot.validTopBook() {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read enrich orderbook jsonl: %w", err)
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Time.Before(snapshots[j].Time) })
	return snapshots, nil
}

func parseOrderbookEnrichmentSnapshot(raw []byte, symbol string) (orderbookEnrichmentSnapshot, bool, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(raw, &event); err != nil {
		return orderbookEnrichmentSnapshot{}, false, err
	}
	eventName := rawString(event["event"])
	if eventName != "" && eventName != "orderbook_diagnostic_snapshot" {
		return orderbookEnrichmentSnapshot{}, false, nil
	}
	eventSymbol := rawString(event["symbol"])
	if symbol != "" && eventSymbol != "" && !strings.EqualFold(symbol, eventSymbol) {
		return orderbookEnrichmentSnapshot{}, false, nil
	}
	ts, err := rawEventTime(event["timestamp"])
	if err != nil {
		return orderbookEnrichmentSnapshot{}, false, err
	}
	if ts.IsZero() {
		ts, err = rawEventTime(event["time"])
		if err != nil {
			return orderbookEnrichmentSnapshot{}, false, err
		}
	}
	if ts.IsZero() {
		return orderbookEnrichmentSnapshot{}, false, fmt.Errorf("missing timestamp/time")
	}
	snapshot := orderbookEnrichmentSnapshot{
		Time:              ts,
		Symbol:            eventSymbol,
		Available:         rawBool(event["orderbook_available"]),
		UnavailableReason: firstNonEmpty(rawString(event["unavailable_reason"]), rawString(event["reason"])),
		Source:            rawString(event["source"]),
		BestBid:           rawNumber(event["best_bid"]),
		BestAsk:           rawNumber(event["best_ask"]),
		BestBidSize:       firstNumber(rawNumber(event["best_bid_size"]), rawNumber(event["bid_size_top"])),
		BestAskSize:       firstNumber(rawNumber(event["best_ask_size"]), rawNumber(event["ask_size_top"])),
		BidAskImbalance:   rawNumber(event["bid_ask_imbalance"]),
		SpreadBps:         rawNumber(event["spread_bps"]),
		OrderbookRatio:    rawNumber(event["orderbook_ratio"]),
		DepthLevels:       rawInt(event["depth_levels"]),
	}
	return snapshot, true, nil
}

func (s orderbookEnrichmentSnapshot) validTopBook() bool {
	return s.Available &&
		s.BestBid.Present && s.BestBid.Value > 0 &&
		s.BestAsk.Present && s.BestAsk.Value > 0 &&
		s.BestBidSize.Present && s.BestBidSize.Value > 0 &&
		s.BestAskSize.Present && s.BestAskSize.Value > 0
}

func writeOrderbookEnrichedCSV(path string, candles []strategy.ReversalReplayCandle, snapshots []orderbookEnrichmentSnapshot, maxStaleness time.Duration) (orderbookEnrichmentSummary, error) {
	f, err := createOutputFile(path)
	if err != nil {
		return orderbookEnrichmentSummary{}, err
	}
	w := csv.NewWriter(f)
	summary, writeErr := writeOrderbookEnrichedCSVRows(w, candles, snapshots, maxStaleness)
	if writeErr == nil {
		w.Flush()
		writeErr = w.Error()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return orderbookEnrichmentSummary{}, writeErr
	}
	if closeErr != nil {
		return orderbookEnrichmentSummary{}, closeErr
	}
	return summary, nil
}

func writeOrderbookEnrichedCSVRows(w *csv.Writer, candles []strategy.ReversalReplayCandle, snapshots []orderbookEnrichmentSnapshot, maxStaleness time.Duration) (orderbookEnrichmentSummary, error) {
	header := []string{
		"time", "open", "high", "low", "close", "volume",
		"best_bid", "best_ask", "best_bid_size", "best_ask_size",
		"bid_ask_imbalance", "spread_bps", "orderbook_ratio",
	}
	if err := w.Write(header); err != nil {
		return orderbookEnrichmentSummary{}, err
	}

	summary := orderbookEnrichmentSummary{RowsTotal: len(candles)}
	spreadCount := 0
	spreadSum := 0.0
	summary.MinSpreadBps = math.Inf(1)
	snapIdx := 0
	var lastValid *orderbookEnrichmentSnapshot
	for _, candle := range candles {
		for snapIdx < len(snapshots) && !snapshots[snapIdx].Time.After(candle.Time) {
			lastValid = &snapshots[snapIdx]
			snapIdx++
		}
		row := []string{
			candle.Time.UTC().Format(time.RFC3339Nano),
			formatEnrichmentFloat(candle.Open),
			formatEnrichmentFloat(candle.High),
			formatEnrichmentFloat(candle.Low),
			formatEnrichmentFloat(candle.Close),
			formatEnrichmentFloat(candle.Volume),
			"", "", "", "", "", "", "",
		}
		if lastValid == nil {
			summary.MissingSnapshotCount++
		} else if age := candle.Time.Sub(lastValid.Time); age < 0 || age > maxStaleness {
			summary.StaleSnapshotCount++
		} else {
			summary.RowsEnriched++
			row[6] = formatEnrichmentNumber(lastValid.BestBid)
			row[7] = formatEnrichmentNumber(lastValid.BestAsk)
			row[8] = formatEnrichmentNumber(lastValid.BestBidSize)
			row[9] = formatEnrichmentNumber(lastValid.BestAskSize)
			row[10] = formatEnrichmentNumber(lastValid.BidAskImbalance)
			row[11] = formatEnrichmentNumber(lastValid.SpreadBps)
			row[12] = formatEnrichmentNumber(lastValid.OrderbookRatio)
			if lastValid.SpreadBps.Present {
				spread := lastValid.SpreadBps.Value
				if spread < summary.MinSpreadBps {
					summary.MinSpreadBps = spread
				}
				if spread > summary.MaxSpreadBps {
					summary.MaxSpreadBps = spread
				}
				spreadSum += spread
				spreadCount++
			}
		}
		if err := w.Write(row); err != nil {
			return orderbookEnrichmentSummary{}, err
		}
	}
	if summary.RowsTotal > 0 {
		summary.OrderbookCoveragePct = float64(summary.RowsEnriched) / float64(summary.RowsTotal) * 100
	}
	if spreadCount > 0 {
		summary.AvgSpreadBps = spreadSum / float64(spreadCount)
	} else {
		summary.MinSpreadBps = 0
	}
	return summary, nil
}

func orderbookEnrichmentSummaryPath(outputCSV string) string {
	ext := filepath.Ext(outputCSV)
	if ext == "" {
		return outputCSV + ".summary.json"
	}
	return strings.TrimSuffix(outputCSV, ext) + ".summary.json"
}

func rawEventTime(raw json.RawMessage) (time.Time, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return parseEnrichmentTime(text)
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

func parseEnrichmentTime(raw string) (time.Time, error) {
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

func rawNumber(raw json.RawMessage) orderbookEnrichmentNumber {
	if len(raw) == 0 || string(raw) == "null" {
		return orderbookEnrichmentNumber{}
	}
	var number json.Number
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&number); err == nil {
		value, err := number.Float64()
		if err == nil {
			return orderbookEnrichmentNumber{Value: value, Present: true}
		}
	}
	text := rawString(raw)
	if text == "" {
		return orderbookEnrichmentNumber{}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return orderbookEnrichmentNumber{}
	}
	return orderbookEnrichmentNumber{Value: value, Present: true}
}

func rawInt(raw json.RawMessage) int {
	if len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var value int
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	text := rawString(raw)
	if text == "" {
		return 0
	}
	value, _ = strconv.Atoi(text)
	return value
}

func firstNumber(values ...orderbookEnrichmentNumber) orderbookEnrichmentNumber {
	for _, value := range values {
		if value.Present {
			return value
		}
	}
	return orderbookEnrichmentNumber{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatEnrichmentNumber(value orderbookEnrichmentNumber) string {
	if !value.Present {
		return ""
	}
	return formatEnrichmentFloat(value.Value)
}

func formatEnrichmentFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func validateOrderbookJSONL(path, symbol string) (orderbookJSONLValidationSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return orderbookJSONLValidationSummary{}, fmt.Errorf("open validate orderbook jsonl: %w", err)
	}
	defer f.Close()
	return validateOrderbookJSONLReader(f, symbol)
}

func validateOrderbookJSONLReader(r io.Reader, symbol string) (orderbookJSONLValidationSummary, error) {
	summary := orderbookJSONLValidationSummary{}
	symbols := map[string]struct{}{}
	reasons := map[string]int{}
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		snapshot, ok, err := parseOrderbookEnrichmentSnapshot([]byte(raw), symbol)
		if err != nil {
			return orderbookJSONLValidationSummary{}, fmt.Errorf("orderbook jsonl line %d: %w", lineNo, err)
		}
		if !ok {
			continue
		}
		summary.TotalSnapshots++
		if snapshot.Symbol != "" {
			symbols[snapshot.Symbol] = struct{}{}
		}
		if summary.TimeMin.IsZero() || snapshot.Time.Before(summary.TimeMin) {
			summary.TimeMin = snapshot.Time
		}
		if summary.TimeMax.IsZero() || snapshot.Time.After(summary.TimeMax) {
			summary.TimeMax = snapshot.Time
		}
		switch snapshot.Source {
		case "replay_orderbook_snapshot":
			summary.ReplaySnapshotCount++
		case "live_orderbook_snapshot":
			summary.LiveSnapshotCount++
		}
		if snapshot.Available {
			if snapshot.validTopBook() {
				summary.ValidSnapshots++
			}
			continue
		}
		summary.UnavailableSnapshots++
		reason := snapshot.UnavailableReason
		if reason == "" {
			reason = "unknown"
		}
		reasons[reason]++
	}
	if err := scanner.Err(); err != nil {
		return orderbookJSONLValidationSummary{}, fmt.Errorf("read validate orderbook jsonl: %w", err)
	}
	if summary.TotalSnapshots > 0 {
		summary.ValidCoveragePct = float64(summary.ValidSnapshots) / float64(summary.TotalSnapshots) * 100
	}
	summary.Symbols = sortedStringKeys(symbols)
	summary.TopUnavailableReasons = sortedUnavailableReasons(reasons, 10)
	summary.Recommendation = orderbookJSONLRecommendation(summary)
	return summary, nil
}

func orderbookJSONLRecommendation(summary orderbookJSONLValidationSummary) string {
	if summary.ValidSnapshots > 0 {
		return orderbookValidationUsableForEnrichment
	}
	if summary.TotalSnapshots > 0 &&
		summary.ReplaySnapshotCount == summary.TotalSnapshots &&
		summary.UnavailableSnapshots == summary.TotalSnapshots {
		return orderbookValidationDataGapReplayUnavailable
	}
	return orderbookValidationNoValidSnapshots
}

func sortedStringKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedUnavailableReasons(values map[string]int, limit int) []orderbookUnavailableReasonCount {
	out := make([]orderbookUnavailableReasonCount, 0, len(values))
	for reason, count := range values {
		out = append(out, orderbookUnavailableReasonCount{Reason: reason, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reason < out[j].Reason
	})
	if limit > 0 && len(out) > limit {
		return out[:limit]
	}
	return out
}

func writeOrderbookValidationSummary(w io.Writer, summary orderbookJSONLValidationSummary) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(summary)
}

func writeEnrichmentSummary(w io.Writer, output orderbookEnrichmentOutput) {
	fmt.Fprintln(w, "Orderbook enrichment summary:")
	fmt.Fprintf(w, "- rows_total: %d\n", output.Summary.RowsTotal)
	fmt.Fprintf(w, "- rows_enriched: %d\n", output.Summary.RowsEnriched)
	fmt.Fprintf(w, "- orderbook_coverage_pct: %.2f\n", output.Summary.OrderbookCoveragePct)
	fmt.Fprintf(w, "- missing_snapshot_count: %d\n", output.Summary.MissingSnapshotCount)
	fmt.Fprintf(w, "- stale_snapshot_count: %d\n", output.Summary.StaleSnapshotCount)
	for _, warning := range output.Summary.Warnings {
		fmt.Fprintf(w, "- warning: %s\n", warning)
	}
	fmt.Fprintf(w, "- output_csv: %s\n", output.OutputCSV)
	fmt.Fprintf(w, "- summary: %s\n", output.SummaryPath)
}
