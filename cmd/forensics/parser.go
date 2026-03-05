package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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

func parseLogEvents(rawLine, sourceFile string, lineNo int, loc *time.Location) ([]Event, bool) {
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
		if ev, ok := parseJSONEvent(eventTradeCloseSummary, msg[len("trade_close_summary "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "execution_fill "):
		if ev, ok := parseJSONEvent(eventExecutionFill, msg[len("execution_fill "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "Execution fill:"):
		if ev, ok := parseLegacyExecutionFill(msg, logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "execution_list_response "):
		if ev, ok := parseJSONEvent(eventExecutionListResp, msg[len("execution_list_response "):], logTS, sourceFile, lineNo, rawLine); ok {
			return []Event{ev}, true
		}
	case strings.HasPrefix(msg, "trade_event "):
		if ev, ok := parseJSONEvent(eventTradeEvent, msg[len("trade_event "):], logTS, sourceFile, lineNo, rawLine); ok {
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

		CreateType:    firstNonEmpty(asString(m["createType"]), asString(m["create_type"])),
		StopOrderType: firstNonEmpty(asString(m["stopOrderType"]), asString(m["stop_order_type"])),
		CloseReason:   asString(m["close_reason"]),

		GrossCalc:      asFloat(m["gross_calc"]),
		GrossSource:    asString(m["gross_source"]),
		FeeOpenAlloc:   asFloat(m["fee_open_alloc"]),
		FeeClose:       asFloat(m["fee_close"]),
		FeeTotal:       asFloat(m["fee_total"]),
		FundingSigned:  asFloat(m["funding_signed"]),
		RealisedDelta:  asFloat(m["realised_delta_cum"]),
		NetCalc:        asFloat(m["net_calc"]),
		NetSource:      asString(m["net_source"]),
		EntryVWAP:      asFloat(m["entry_vwap"]),
		ExitVWAP:       asFloat(m["exit_vwap"]),
		QtyOpened:      firstNonZero(asFloat(m["qty_opened"]), asFloat(m["qty_total"])),
		QtyClosedTotal: asFloat(m["qty_closed_total"]),
		LegsCount:      int(asInt64(m["legs_count"])),
		DurationSec:    asFloat(m["duration_sec"]),
		ListLen:        int(asInt64(m["list_len"])),
		FirstExecID:    asString(m["firstExecId"]),
		LastExecID:     asString(m["lastExecId"]),
		NextCursor:     asString(m["nextCursor"]),
		TradeEventName: asString(m["event"]),
	}
	if netEx, ok := asOptionalFloat(m["net_exchange"]); ok {
		ev.NetExchange = &netEx
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
			Type:           eventPositionListResp,
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
			Type:           eventPositionWSUpdate,
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
		Type:        eventExecutionFill,
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

func loadEvents(logsRoot string, fromTS, toTS time.Time, loc *time.Location) ([]Event, error) {
	files, err := discoverLogFiles(logsRoot, fromTS, toTS)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no log files under %s", logsRoot)
	}

	lowBound := fromTS.Add(-defaultEventScanPadding)
	highBound := toTS.Add(defaultEventScanPadding)
	events := make([]Event, 0, 1024)

	for _, path := range files {
		rc, err := openMaybeGzip(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(rc)
		buf := make([]byte, 0, 1024*1024)
		sc.Buffer(buf, 8*1024*1024)

		lineNo := 0
		for sc.Scan() {
			lineNo++
			evs, ok := parseLogEvents(sc.Text(), path, lineNo, loc)
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
		_ = rc.Close()
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
	return events, nil
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
		// For non-dated paths, include only files recently modified around the window.
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

func asOptionalFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case nil:
		return 0, false
	case string:
		s := strings.TrimSpace(x)
		if s == "" || strings.EqualFold(s, "null") {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	default:
		f := asFloat(v)
		return f, true
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
