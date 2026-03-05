package forensics

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var logLineRE = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d{1,6})\s+\S+\s+\[[A-Z]+\]\s+(.*)$`)
var legacyExecKVRE = regexp.MustCompile(`([A-Za-z0-9_]+)=([^\s]+)`)
var pathDateRE = regexp.MustCompile(`\d{4}-\d{2}-\d{2}`)

func ParseLogEvents(rawLine, sourceFile string, lineNo int, loc *time.Location) ([]Event, bool) {
	m := logLineRE.FindStringSubmatch(rawLine)
	if len(m) != 3 {
		return nil, false
	}
	logTS, ok := parseLogTS(m[1], loc)
	if !ok {
		return nil, false
	}
	msg := strings.TrimSpace(m[2])
	switch {
	case strings.HasPrefix(msg, "trade_close_summary "):
		if ev, ok := parseJSONEvent(EventTradeCloseSummary, msg[len("trade_close_summary "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "execution_fill "):
		if ev, ok := parseJSONEvent(EventExecutionFill, msg[len("execution_fill "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "Execution fill:"):
		if ev, ok := parseLegacyExecutionFill(msg, logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "execution_list_response "):
		if ev, ok := parseJSONEvent(EventExecutionListResp, msg[len("execution_list_response "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "trade_event "):
		if ev, ok := parseJSONEvent(EventTradeEvent, msg[len("trade_event "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.Contains(msg, "/v5/position/list") && strings.Contains(msg, "Body:"):
		if events := parsePositionListResponse(msg, logTS, sourceFile, lineNo, rawLine); len(events) > 0 {
			return events, true
		}
	case strings.HasPrefix(msg, "Received position update from exchange:"):
		payload := strings.TrimSpace(strings.TrimPrefix(msg, "Received position update from exchange:"))
		if events := parsePositionWSUpdate(payload, logTS, sourceFile, lineNo, rawLine); len(events) > 0 {
			return events, true
		}
	}
	return nil, false
}

func LoadEvents(logsRoot string, fromTS, toTS time.Time, loc *time.Location) ([]Event, LoadStats, error) {
	files, err := discoverLogFiles(logsRoot, fromTS, toTS)
	if err != nil {
		return nil, LoadStats{}, err
	}
	stats := LoadStats{FilesTotal: uint64(len(files))}
	if len(files) == 0 {
		return nil, stats, nil
	}

	lowBound := fromTS.Add(-defaultEventScanPadding)
	highBound := toTS.Add(defaultEventScanPadding)
	events := make([]Event, 0, 1024)

	for _, path := range files {
		rc, err := openMaybeGzip(path)
		if err != nil {
			if isMissingFileError(err) {
				stats.FilesSkippedMissing++
				continue
			}
			stats.FilesSkippedOther++
			continue
		}

		sc := bufio.NewScanner(rc)
		buf := make([]byte, 0, 1024*1024)
		sc.Buffer(buf, 8*1024*1024)

		lineNo := 0
		for sc.Scan() {
			lineNo++
			stats.LinesScanned++
			evs, ok := ParseLogEvents(sc.Text(), path, lineNo, loc)
			if !ok {
				continue
			}
			for _, ev := range evs {
				if ev.TS.Before(lowBound) || ev.TS.After(highBound) {
					continue
				}
				events = append(events, ev)
			}
		}
		if err := sc.Err(); err != nil {
			if isMissingFileError(err) {
				stats.FilesSkippedMissing++
			} else {
				stats.FilesSkippedOther++
			}
			_ = rc.Close()
			continue
		}
		_ = rc.Close()
		stats.FilesRead++
	}

	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].TS.Equal(events[j].TS) {
			return events[i].TS.Before(events[j].TS)
		}
		if events[i].File != events[j].File {
			return events[i].File < events[j].File
		}
		return events[i].Line < events[j].Line
	})
	return events, stats, nil
}

func parseJSONEvent(eventType, payload string, fallbackTS time.Time, sourceFile string, lineNo int, rawLine string) (Event, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return Event{}, false
	}
	ts, epoch := resolveEventTS(m, fallbackTS)
	ev := Event{
		TS:      ts,
		EpochMS: epoch,
		File:    sourceFile,
		Line:    lineNo,
		Type:    eventType,
		Raw:     rawLine,
		Symbol:  asString(m["symbol"]),

		TraceKey:    firstNonEmpty(asString(m["traceKey"]), asString(m["trace_key"])),
		LifecycleID: firstNonEmpty(asString(m["lifecycleId"]), asString(m["lifecycle_id"])),
		TradeID:     firstNonEmpty(asString(m["tradeId"]), asString(m["tradeID"]), asString(m["trade_id"])),
		OrderID:     firstNonEmpty(asString(m["orderId"]), asString(m["order_id"])),
		OrderLinkID: firstNonEmpty(asString(m["orderLinkId"]), asString(m["orderLinkID"]), asString(m["order_link_id"])),
		ExecID:      firstNonEmpty(asString(m["execId"]), asString(m["execID"])),

		Side:         firstNonEmpty(asString(m["side"]), asString(m["execSide"]), asString(m["exec_side"])),
		PositionSide: firstNonEmpty(asString(m["positionSide"]), asString(m["position_side"])),
		Qty:          firstNonZero(asFloat(m["qty"]), asFloat(m["execQty"]), asFloat(m["exec_qty"])),
		Price:        firstNonZero(asFloat(m["price"]), asFloat(m["execPrice"]), asFloat(m["exec_price"])),
		ClosedSize:   asFloat(m["closedSize"]),
		ReduceOnly:   asBool(m["reduceOnly"]),

		ExecFee:  firstNonZero(asFloat(m["execFee"]), asFloat(m["exec_fee"])),
		ExecPnl:  firstNonZero(asFloat(m["execPnl"]), asFloat(m["exec_pnl"]), asFloat(m["closedPnl"])),
		ExecTime: firstNonZeroInt64(asInt64(m["execTime"]), asInt64(m["exec_time"])),

		CreateType:     firstNonEmpty(asString(m["createType"]), asString(m["create_type"])),
		StopOrderType:  firstNonEmpty(asString(m["stopOrderType"]), asString(m["stop_order_type"])),
		TradeEventName: asString(m["event"]),
	}
	if b, ok := asOptionalBool(m["isMaker"]); ok {
		ev.IsMaker = &b
	}
	if ev.TraceKey == "" {
		ev.TraceKey = firstNonEmpty(ev.LifecycleID, ev.TradeID, ev.OrderID, ev.OrderLinkID, ev.ExecID)
	}
	return ev, true
}

func parsePositionListResponse(msg string, fallbackTS time.Time, sourceFile string, lineNo int, rawLine string) []Event {
	idx := strings.Index(msg, "Body:")
	if idx < 0 {
		return nil
	}
	body := strings.TrimSpace(msg[idx+len("Body:"):])
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		return nil
	}
	listRaw, ok := result["list"].([]any)
	if !ok || len(listRaw) == 0 {
		return nil
	}
	events := make([]Event, 0, len(listRaw))
	for _, row := range listRaw {
		item, ok := row.(map[string]any)
		if !ok {
			continue
		}
		ts, epoch := resolveEventTS(item, fallbackTS)
		ev := Event{
			TS:             ts,
			EpochMS:        epoch,
			File:           sourceFile,
			Line:           lineNo,
			Type:           EventPositionListResp,
			Raw:            rawLine,
			Symbol:         asString(item["symbol"]),
			Side:           asString(item["side"]),
			PositionSize:   asFloat(item["size"]),
			PositionAvgPx:  firstNonZero(asFloat(item["avgPrice"]), asFloat(item["entryPrice"])),
			CurRealisedPnl: asFloat(item["curRealisedPnl"]),
			CumRealisedPnl: asFloat(item["cumRealisedPnl"]),
			TraceKey:       firstNonEmpty(asString(item["traceKey"]), asString(item["lifecycleId"]), asString(item["tradeId"])),
		}
		events = append(events, ev)
	}
	return events
}

func parsePositionWSUpdate(payload string, fallbackTS time.Time, sourceFile string, lineNo int, rawLine string) []Event {
	var env map[string]any
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		return nil
	}
	topic := asString(env["topic"])
	if topic != "position" && !strings.HasPrefix(topic, "position") {
		return nil
	}
	data, ok := env["data"].([]any)
	if !ok || len(data) == 0 {
		return nil
	}
	events := make([]Event, 0, len(data))
	for _, row := range data {
		item, ok := row.(map[string]any)
		if !ok {
			continue
		}
		ts, epoch := resolveEventTS(item, fallbackTS)
		events = append(events, Event{
			TS:             ts,
			EpochMS:        epoch,
			File:           sourceFile,
			Line:           lineNo,
			Type:           EventPositionWSUpdate,
			Raw:            rawLine,
			Symbol:         asString(item["symbol"]),
			Side:           asString(item["side"]),
			PositionSize:   asFloat(item["size"]),
			PositionAvgPx:  firstNonZero(asFloat(item["avgPrice"]), asFloat(item["entryPrice"])),
			CurRealisedPnl: asFloat(item["curRealisedPnl"]),
			CumRealisedPnl: asFloat(item["cumRealisedPnl"]),
			TraceKey:       firstNonEmpty(asString(item["traceKey"]), asString(item["lifecycleId"]), asString(item["tradeId"])),
		})
	}
	return events
}

func parseLegacyExecutionFill(msg string, fallbackTS time.Time, sourceFile string, lineNo int, rawLine string) (Event, bool) {
	kv := make(map[string]string, 16)
	for _, m := range legacyExecKVRE.FindAllStringSubmatch(msg, -1) {
		if len(m) != 3 {
			continue
		}
		key := strings.TrimSpace(m[1])
		val := strings.Trim(strings.TrimSpace(m[2]), ",")
		kv[key] = val
	}
	if len(kv) == 0 {
		return Event{}, false
	}
	ev := Event{
		TS:          fallbackTS.UTC(),
		EpochMS:     fallbackTS.UTC().UnixMilli(),
		File:        sourceFile,
		Line:        lineNo,
		Type:        EventExecutionFill,
		Raw:         rawLine,
		Symbol:      kv["symbol"],
		TraceKey:    kv["traceKey"],
		LifecycleID: kv["lifecycleId"],
		TradeID:     firstNonEmpty(kv["tradeID"], kv["tradeId"], kv["trade_id"]),
		OrderID:     firstNonEmpty(kv["orderId"], kv["orderID"], kv["order_id"]),
		OrderLinkID: firstNonEmpty(kv["orderLinkId"], kv["orderLinkID"], kv["order_link_id"]),
		ExecID:      firstNonEmpty(kv["execId"], kv["execID"], kv["exec_id"]),
		Side:        firstNonEmpty(kv["execSide"], kv["side"]),
		PositionSide: firstNonEmpty(
			kv["positionSide"], kv["position_side"],
		),
		Qty:           asFloat(kv["execQty"]),
		Price:         asFloat(kv["execPrice"]),
		ExecFee:       asFloat(kv["execFee"]),
		ExecPnl:       asFloat(kv["execPnl"]),
		ClosedSize:    asFloat(kv["closedSize"]),
		ReduceOnly:    asBool(kv["reduceOnly"]),
		CreateType:    kv["createType"],
		StopOrderType: kv["stopOrderType"],
	}
	if ev.ExecID == "" && ev.OrderID == "" && ev.TradeID == "" {
		return Event{}, false
	}
	if ev.TraceKey == "" {
		ev.TraceKey = firstNonEmpty(ev.LifecycleID, ev.TradeID, ev.OrderID, ev.OrderLinkID, ev.ExecID)
	}
	return ev, true
}

func discoverLogFiles(root string, fromTS, toTS time.Time) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("empty logs path")
	}
	stat, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		return []string{root}, nil
	}
	files := make([]string, 0, 128)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if isMissingFileError(walkErr) {
				return nil
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz") || strings.HasSuffix(name, ".gz") {
			var modTime time.Time
			if info, infoErr := d.Info(); infoErr == nil {
				modTime = info.ModTime().UTC()
			}
			if !pathLikelyInWindow(path, modTime, fromTS, toTS) {
				return nil
			}
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func pathLikelyInWindow(path string, modTime, fromTS, toTS time.Time) bool {
	path = filepath.ToSlash(path)
	matches := pathDateRE.FindAllString(path, -1)
	fromDay := dayFloorUTC(fromTS)
	toDay := dayFloorUTC(toTS)
	if len(matches) == 0 {
		if modTime.IsZero() {
			return false
		}
		low := fromDay.Add(-24 * time.Hour)
		high := toDay.Add(48 * time.Hour)
		return !modTime.Before(low) && !modTime.After(high)
	}
	for _, s := range matches {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			continue
		}
		day := d.UTC()
		if !day.Before(fromDay) && !day.After(toDay) {
			return true
		}
	}
	return false
}

func dayFloorUTC(ts time.Time) time.Time {
	ts = ts.UTC()
	return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
}

func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return multiCloserReader{Reader: gz, Closer: multiCloser{gz, f}}, nil
	}
	return f, nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var first error
	for _, c := range m {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type multiCloserReader struct {
	io.Reader
	io.Closer
}

func parseLogTS(raw string, loc *time.Location) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	layouts := []string{
		"2006/01/02 15:04:05.000000",
		"2006/01/02 15:04:05.00000",
		"2006/01/02 15:04:05.0000",
		"2006/01/02 15:04:05.000",
	}
	for _, layout := range layouts {
		ts, err := time.ParseInLocation(layout, raw, loc)
		if err == nil {
			return ts.UTC(), true
		}
	}
	return time.Time{}, false
}

func resolveEventTS(payload map[string]any, fallback time.Time) (time.Time, int64) {
	if payload != nil {
		if ms := asInt64(payload["ts_epoch_ms"]); ms > 0 {
			return time.UnixMilli(ms).UTC(), ms
		}
		if ms := asInt64(payload["execTime"]); ms > 0 {
			return time.UnixMilli(ms).UTC(), ms
		}
		for _, key := range []string{"ts", "time"} {
			if s := strings.TrimSpace(asString(payload[key])); s != "" {
				if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
					return ts.UTC(), ts.UTC().UnixMilli()
				}
			}
		}
	}
	if fallback.IsZero() {
		return fallback, 0
	}
	return fallback.UTC(), fallback.UTC().UnixMilli()
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(x)
	case json.Number:
		return x.String()
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(x, 'f', -1, 64))
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func asFloat(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case float64:
		return x
	case float32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		f, _ := strconv.ParseFloat(s, 64)
		return f
	default:
		s := strings.TrimSpace(fmt.Sprint(x))
		f, _ := strconv.ParseFloat(s, 64)
		return f
	}
}

func asInt64(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		i, err := x.Int64()
		if err == nil {
			return i
		}
		f, ferr := x.Float64()
		if ferr != nil {
			return 0
		}
		return int64(f)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		i, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return i
		}
		f, ferr := strconv.ParseFloat(s, 64)
		if ferr != nil {
			return 0
		}
		return int64(f)
	default:
		return 0
	}
}

func asBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "y", "on":
			return true
		default:
			return false
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	default:
		return false
	}
}

func asOptionalBool(v any) (bool, bool) {
	switch x := v.(type) {
	case nil:
		return false, false
	case bool:
		return x, true
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		if s == "" {
			return false, false
		}
		switch s {
		case "1", "true", "yes", "y", "on":
			return true, true
		case "0", "false", "no", "n", "off":
			return false, true
		default:
			return false, false
		}
	default:
		return asBool(v), true
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonZero(values ...float64) float64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func isMissingFileError(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such file or directory") || strings.Contains(msg, "file does not exist")
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func inferPositionSideFromExecSide(execSide string) string {
	switch strings.ToUpper(strings.TrimSpace(execSide)) {
	case "BUY":
		return "SHORT"
	case "SELL":
		return "LONG"
	default:
		return ""
	}
}

func normalizeSide(side string) string {
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG", "BUY":
		return "LONG"
	case "SHORT", "SELL":
		return "SHORT"
	default:
		return ""
	}
}

func parseReasonFromCreateType(ct, sot string) string {
	joined := strings.ToLower(ct + " " + sot)
	switch {
	case strings.Contains(joined, "takeprofit"), strings.Contains(joined, "tp"):
		return "tp"
	case strings.Contains(joined, "stoploss"), strings.Contains(joined, "sl"):
		return "sl"
	case strings.Contains(joined, "trail"):
		return "trail"
	default:
		return "unknown"
	}
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

type positionDelta struct {
	TS       time.Time
	Symbol   string
	DeltaCum float64
	PrevSize float64
	CurrSize float64
	Side     string
	Used     bool
	PrevTS   time.Time
}

func ReconstructTrades(events []Event, symbol string) ([]ReconstructedTrade, ReconstructStats) {
	filtered := make([]Event, 0, len(events))
	for _, ev := range events {
		if symbol != "" && ev.Symbol != "" && !strings.EqualFold(ev.Symbol, symbol) {
			continue
		}
		filtered = append(filtered, ev)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if !filtered[i].TS.Equal(filtered[j].TS) {
			return filtered[i].TS.Before(filtered[j].TS)
		}
		return filtered[i].Line < filtered[j].Line
	})

	posEvents := make([]Event, 0, 64)
	clusters := map[string][]Event{}
	for _, ev := range filtered {
		if ev.Type == EventPositionListResp || ev.Type == EventPositionWSUpdate {
			posEvents = append(posEvents, ev)
		}
		key := firstNonEmpty(ev.TraceKey, ev.LifecycleID, ev.TradeID, ev.OrderID, ev.OrderLinkID)
		if key == "" {
			continue
		}
		clusters[key] = append(clusters[key], ev)
	}

	deltas := buildPositionDeltas(posEvents, symbol)
	trades := make([]ReconstructedTrade, 0, len(clusters)+len(deltas))
	stats := ReconstructStats{}
	idSeq := 0
	keys := make([]string, 0, len(clusters))
	for k := range clusters {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		evs := clusters[key]
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].TS.Before(evs[j].TS) })
		idSeq++
		if tr, ok := reconstructTier1Trade(idSeq, key, evs); ok {
			attachNearestDelta(&tr, deltas)
			trades = append(trades, tr)
			stats.Tier1++
			continue
		}
		if tr, ok := reconstructTier2Trade(idSeq, key, evs, deltas); ok {
			trades = append(trades, tr)
			stats.Tier2++
		}
	}

	for _, d := range deltas {
		if d.Used {
			continue
		}
		if math.Abs(d.DeltaCum) < 1e-12 {
			continue
		}
		idSeq++
		side := normalizeSide(d.Side)
		if side == "" {
			side = "UNKNOWN"
		}
		tr := ReconstructedTrade{
			TradeID:      fmt.Sprintf("recon-%06d", idSeq),
			Tier:         "tier3",
			Confidence:   0.55,
			OpenTS:       d.PrevTS,
			CloseTS:      d.TS,
			DurationSec:  d.TS.Sub(d.PrevTS).Seconds(),
			Side:         side,
			QtyTotal:     math.Abs(d.PrevSize - d.CurrSize),
			Realised:     d.DeltaCum,
			FeeClose:     0,
			GrossCalc:    d.DeltaCum,
			NetCalc:      d.DeltaCum,
			UnknownFills: 1,
		}
		if tr.DurationSec < 0 {
			tr.DurationSec = 0
		}
		trades = append(trades, tr)
		stats.Tier3++
	}

	sort.SliceStable(trades, func(i, j int) bool { return trades[i].CloseTS.Before(trades[j].CloseTS) })
	var sumConf float64
	for _, tr := range trades {
		sumConf += tr.Confidence
		if tr.FeeClose == 0 {
			stats.MissingFeeTrades++
		}
		if normalizeSide(tr.Side) == "" {
			stats.MissingSideTrades++
		}
	}
	if len(trades) > 0 {
		stats.ConfidenceAvg = sumConf / float64(len(trades))
	}
	return trades, stats
}

func reconstructTier1Trade(idSeq int, key string, evs []Event) (ReconstructedTrade, bool) {
	fills := make([]Event, 0, len(evs))
	for _, ev := range evs {
		if ev.Type == EventExecutionFill {
			fills = append(fills, ev)
		}
	}
	if len(fills) == 0 {
		return ReconstructedTrade{}, false
	}
	sort.SliceStable(fills, func(i, j int) bool { return fills[i].TS.Before(fills[j].TS) })
	closeFills := make([]Event, 0, len(fills))
	for _, f := range fills {
		isClose := f.ReduceOnly || f.ClosedSize > 0 || strings.Contains(strings.ToLower(f.CreateType), "takeprofit") || strings.Contains(strings.ToLower(f.CreateType), "stop")
		if isClose {
			closeFills = append(closeFills, f)
		}
	}
	if len(closeFills) == 0 {
		return ReconstructedTrade{}, false
	}
	openTS := fills[0].TS
	closeTS := closeFills[len(closeFills)-1].TS
	if closeTS.Before(openTS) {
		openTS = closeTS
	}

	side := normalizeSide(firstNonEmpty(closeFills[len(closeFills)-1].PositionSide, fills[0].PositionSide))
	if side == "" {
		side = normalizeSide(inferPositionSideFromExecSide(closeFills[len(closeFills)-1].Side))
	}

	var qty, pnl, fee float64
	maker, taker, unknown := 0, 0, 0
	for _, f := range closeFills {
		q := f.ClosedSize
		if q <= 0 {
			q = f.Qty
		}
		qty += math.Abs(q)
		pnl += f.ExecPnl
		fee += math.Abs(f.ExecFee)
		if f.IsMaker == nil {
			unknown++
		} else if *f.IsMaker {
			maker++
		} else {
			taker++
		}
	}
	if qty == 0 {
		for _, f := range fills {
			qty += math.Abs(f.Qty)
		}
	}
	if pnl == 0 {
		pnl = -fee
	}
	tr := ReconstructedTrade{
		TradeID:      fmt.Sprintf("recon-%06d", idSeq),
		Tier:         "tier1",
		Confidence:   0.97,
		OpenTS:       openTS,
		CloseTS:      closeTS,
		DurationSec:  closeTS.Sub(openTS).Seconds(),
		Side:         firstNonEmpty(side, "UNKNOWN"),
		QtyTotal:     qty,
		Realised:     pnl,
		FeeClose:     fee,
		GrossCalc:    pnl + fee,
		NetCalc:      pnl - fee,
		MakerFills:   maker,
		TakerFills:   taker,
		UnknownFills: unknown,
		TraceKey:     firstNonEmpty(fills[0].TraceKey, key),
		LifecycleID:  fills[0].LifecycleID,
		OrderID:      fills[0].OrderID,
		TradeRefID:   fills[0].TradeID,
	}
	if tr.DurationSec < 0 {
		tr.DurationSec = 0
	}
	return tr, true
}

func reconstructTier2Trade(idSeq int, key string, evs []Event, deltas []positionDelta) (ReconstructedTrade, bool) {
	hasTradeEvent := false
	for _, ev := range evs {
		if ev.Type == EventTradeEvent {
			hasTradeEvent = true
			break
		}
	}
	if !hasTradeEvent {
		return ReconstructedTrade{}, false
	}
	ref := evs[len(evs)-1].TS
	bestIdx := -1
	bestGap := positionAttachGap + time.Second
	for i := range deltas {
		if deltas[i].Used {
			continue
		}
		gap := absDuration(deltas[i].TS.Sub(ref))
		if gap <= positionAttachGap && gap < bestGap {
			bestGap = gap
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return ReconstructedTrade{}, false
	}
	d := &deltas[bestIdx]
	d.Used = true
	openTS := evs[0].TS
	closeTS := d.TS
	side := "UNKNOWN"
	for _, ev := range evs {
		if s := normalizeSide(firstNonEmpty(ev.PositionSide, ev.Side)); s != "" {
			side = s
			break
		}
	}
	tr := ReconstructedTrade{
		TradeID:      fmt.Sprintf("recon-%06d", idSeq),
		Tier:         "tier2",
		Confidence:   0.82,
		OpenTS:       openTS,
		CloseTS:      closeTS,
		DurationSec:  closeTS.Sub(openTS).Seconds(),
		Side:         side,
		QtyTotal:     math.Abs(d.PrevSize - d.CurrSize),
		Realised:     d.DeltaCum,
		FeeClose:     0,
		GrossCalc:    d.DeltaCum,
		NetCalc:      d.DeltaCum,
		UnknownFills: 1,
		TraceKey:     key,
	}
	if tr.DurationSec < 0 {
		tr.DurationSec = 0
	}
	return tr, true
}

func attachNearestDelta(tr *ReconstructedTrade, deltas []positionDelta) {
	if tr == nil {
		return
	}
	bestIdx := -1
	bestGap := positionAttachGap + time.Second
	for i := range deltas {
		if deltas[i].Used {
			continue
		}
		gap := absDuration(deltas[i].TS.Sub(tr.CloseTS))
		if gap <= positionAttachGap && gap < bestGap {
			bestGap = gap
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return
	}
	deltas[bestIdx].Used = true
	tr.Realised = deltas[bestIdx].DeltaCum
	tr.GrossCalc = tr.Realised + tr.FeeClose
	tr.NetCalc = tr.Realised - tr.FeeClose
}

func buildPositionDeltas(positionEvents []Event, symbol string) []positionDelta {
	filtered := make([]Event, 0, len(positionEvents))
	for _, ev := range positionEvents {
		if symbol != "" && ev.Symbol != "" && !strings.EqualFold(ev.Symbol, symbol) {
			continue
		}
		filtered = append(filtered, ev)
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].TS.Before(filtered[j].TS) })
	if len(filtered) < 2 {
		return nil
	}
	out := make([]positionDelta, 0, len(filtered)/2)
	for i := 1; i < len(filtered); i++ {
		prev := filtered[i-1]
		cur := filtered[i]
		delta := cur.CumRealisedPnl - prev.CumRealisedPnl
		if math.Abs(delta) < 1e-12 {
			continue
		}
		out = append(out, positionDelta{
			TS:       cur.TS,
			Symbol:   firstNonEmpty(cur.Symbol, prev.Symbol),
			DeltaCum: delta,
			PrevSize: prev.PositionSize,
			CurrSize: cur.PositionSize,
			Side:     normalizeSide(firstNonEmpty(cur.Side, prev.Side)),
			PrevTS:   prev.TS,
		})
	}
	return out
}
