package main

import (
	"bufio"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
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

	sharedforensics "verbose-fortnight/internal/forensics"
)

const lineTimeLayout = "2006/01/02 15:04:05.000000"

type evalConfig struct {
	From        time.Time
	To          time.Time
	Symbol      string
	Logs        string
	Report      string
	Mode        string
	TradeSource string
	Out         string
	TZ          string
}

type executionSample struct {
	TS       time.Time
	ExecID   string
	Symbol   string
	HasMaker bool
	IsMaker  bool
	ExecFee  float64
}

type tradeSummarySample struct {
	TS       time.Time
	TraceKey string
	Symbol   string
	Gross    float64
	FeeTotal float64
	Net      float64
	Duration float64
}

type evalMetrics struct {
	From             string          `json:"from"`
	To               string          `json:"to"`
	Symbol           string          `json:"symbol"`
	Mode             string          `json:"mode"`
	Trades           int             `json:"trades"`
	WinRate          float64         `json:"winrate"`
	AvgWin           float64         `json:"avg_win"`
	AvgLoss          float64         `json:"avg_loss"`
	ProfitFactor     float64         `json:"profit_factor"`
	MakerRatio       float64         `json:"maker_ratio"`
	FeeToGrossRatio  float64         `json:"fee_to_gross_ratio"`
	AvgNetAfterFee   float64         `json:"avg_net_after_fee"`
	AvgDuration      float64         `json:"avg_duration"`
	AvgDurationWin   float64         `json:"avg_duration_win"`
	AvgDurationLoss  float64         `json:"avg_duration_loss"`
	ExecutionSamples int             `json:"execution_samples"`
	TradesSource     string          `json:"tradesSource"`
	Reconstruct      reconstructMeta `json:"reconstruct"`
	Markers          markers         `json:"markers"`
	IO               ioStats         `json:"io"`
	Diagnostics      evalDiagnostics `json:"diagnostics"`
}

type evalResult struct {
	Metrics             evalMetrics
	ExecutionSamples    []executionSample
	TradeSummaries      []tradeSummarySample
	ReconstructedTrades []sharedforensics.ReconstructedTrade
}

var lineTSRegex = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d{6})\s+`)

type markers struct {
	TradeCloseSummaryLines    uint64 `json:"tradeCloseSummaryLines"`
	ExecutionFillLines        uint64 `json:"executionFillLines"`
	ExecutionListResponseLine uint64 `json:"executionListResponseLines"`
	TradeEventLines           uint64 `json:"tradeEventLines"`
}

type ioStats struct {
	FilesTotal            uint64 `json:"filesTotal"`
	FilesRead             uint64 `json:"filesRead"`
	FilesSkippedMissing   uint64 `json:"filesSkippedMissing"`
	FilesSkippedOtherErrs uint64 `json:"filesSkippedOtherErrors"`
	LinesScanned          uint64 `json:"linesScanned"`
}

type evalDiagnostics struct {
	ZeroTradesReason        string   `json:"zeroTradesReason"`
	TopUnmatchedSample      []string `json:"topUnmatchedSamples,omitempty"`
	MakerCoverageAssessment string   `json:"makerCoverageAssessment,omitempty"`
}

type reconstructMeta struct {
	Tier1             int     `json:"tier1"`
	Tier2             int     `json:"tier2"`
	Tier3             int     `json:"tier3"`
	ConfidenceAvg     float64 `json:"confidenceAvg"`
	MissingFeeTrades  int     `json:"missingFeeTrades"`
	MissingSideTrades int     `json:"missingSideTrades"`
}

type evalCollector struct {
	markers          markers
	io               ioStats
	unmatchedSamples []string
	missingWarns     uint64
	otherWarns       uint64
	events           []sharedforensics.Event
}

func (c *evalCollector) addUnmatchedSample(line string) {
	if len(c.unmatchedSamples) >= 10 {
		return
	}
	c.unmatchedSamples = append(c.unmatchedSamples, sanitizeSample(line))
}

func sanitizeSample(s string) string {
	s = strings.TrimSpace(s)
	// Best-effort redaction for credential-looking material.
	redactionWords := []string{"apikey", "api_key", "apisecret", "api_secret", "token", "password", "secret"}
	lower := strings.ToLower(s)
	for _, word := range redactionWords {
		if strings.Contains(lower, word) {
			return "[redacted_sensitive_line]"
		}
	}
	const maxLen = 240
	if len(s) > maxLen {
		return s[:maxLen] + "...(truncated)"
	}
	return s
}

func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
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

func logSkipMissing(path string, err error, collector *evalCollector) {
	if collector == nil {
		return
	}
	collector.missingWarns++
	if collector.missingWarns <= 20 {
		fmt.Fprintf(os.Stderr, "warn: eval_skip_missing_file path=%s err=%v\n", path, err)
		return
	}
	if collector.missingWarns == 21 {
		fmt.Fprintf(os.Stderr, "warn: eval_skip_missing_file further messages suppressed\n")
	}
}

func logSkipOther(path string, err error, collector *evalCollector) {
	if collector == nil {
		return
	}
	collector.otherWarns++
	if collector.otherWarns <= 20 {
		fmt.Fprintf(os.Stderr, "warn: eval_skip_file_error path=%s err=%v\n", path, err)
		return
	}
	if collector.otherWarns == 21 {
		fmt.Fprintf(os.Stderr, "warn: eval_skip_file_error further messages suppressed\n")
	}
}

var (
	legacyKVRegex = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)=([^\s,]+)`)
)

func main() {
	cfg, err := parseFlags()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	res, err := runEval(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := writeEvalOutputs(cfg, res); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("eval completed: trades=%d exec_samples=%d out=%s\n", res.Metrics.Trades, res.Metrics.ExecutionSamples, cfg.Out)
}

func parseFlags() (evalConfig, error) {
	var cfg evalConfig
	var fromRaw, toRaw string
	flag.StringVar(&fromRaw, "from", "", "start time (e.g. 2026-02-24 00:00)")
	flag.StringVar(&toRaw, "to", "", "end time (e.g. 2026-02-28 23:59)")
	flag.StringVar(&cfg.Symbol, "symbol", "BTCUSDT", "symbol filter")
	flag.StringVar(&cfg.Logs, "logs", "./logs", "logs directory or file")
	flag.StringVar(&cfg.Report, "report", "", "optional report path")
	flag.StringVar(&cfg.Mode, "mode", "baseline", "baseline|maker_first|guard_only|all_flags")
	flag.StringVar(&cfg.TradeSource, "trade-source", "summary_only", "summary_only|reconstruct|auto")
	flag.StringVar(&cfg.Out, "out", "/tmp/eval", "output directory")
	flag.StringVar(&cfg.TZ, "tz", "UTC", "timezone for --from/--to when timezone not provided")
	flag.Parse()

	if strings.TrimSpace(fromRaw) == "" || strings.TrimSpace(toRaw) == "" {
		return cfg, errors.New("--from and --to are required")
	}
	loc, err := time.LoadLocation(cfg.TZ)
	if err != nil {
		return cfg, fmt.Errorf("invalid --tz: %w", err)
	}
	cfg.From, err = parseFlexibleTime(fromRaw, loc)
	if err != nil {
		return cfg, fmt.Errorf("invalid --from: %w", err)
	}
	cfg.To, err = parseFlexibleTime(toRaw, loc)
	if err != nil {
		return cfg, fmt.Errorf("invalid --to: %w", err)
	}
	cfg.From = cfg.From.UTC()
	cfg.To = cfg.To.UTC()
	if cfg.To.Before(cfg.From) {
		return cfg, errors.New("--to must be >= --from")
	}
	cfg.Symbol = strings.ToUpper(strings.TrimSpace(cfg.Symbol))
	cfg.Mode = strings.TrimSpace(cfg.Mode)
	if cfg.Mode == "" {
		cfg.Mode = "baseline"
	}
	cfg.TradeSource = strings.ToLower(strings.TrimSpace(cfg.TradeSource))
	if cfg.TradeSource == "" {
		cfg.TradeSource = "summary_only"
	}
	switch cfg.TradeSource {
	case "summary_only", "reconstruct", "auto":
	default:
		return cfg, fmt.Errorf("invalid --trade-source %q (expected summary_only|reconstruct|auto)", cfg.TradeSource)
	}
	return cfg, nil
}

func parseFlexibleTime(raw string, loc *time.Location) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if layout == time.RFC3339Nano || layout == time.RFC3339 {
			if ts, err := time.Parse(layout, raw); err == nil {
				return ts, nil
			}
			continue
		}
		if ts, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported time format %q", raw)
}

func discoverLogFiles(root string) ([]string, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{root}, nil
	}
	files := make([]string, 0, 256)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Live log rotation can remove files while walking.
			if isMissingFileError(walkErr) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".gz") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		gz, gzErr := gzip.NewReader(f)
		if gzErr != nil {
			_ = f.Close()
			return nil, gzErr
		}
		return &combinedReadCloser{Reader: gz, closers: []io.Closer{gz, f}}, nil
	}
	return f, nil
}

type combinedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (c *combinedReadCloser) Close() error {
	var first error
	for _, cl := range c.closers {
		if err := cl.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func runEval(cfg evalConfig) (evalResult, error) {
	files, err := discoverLogFiles(cfg.Logs)
	if err != nil {
		return evalResult{}, err
	}
	collector := &evalCollector{}
	collector.io.FilesTotal = uint64(len(files))
	res := evalResult{}
	execs := make([]executionSample, 0, 2048)
	summaries := make([]tradeSummarySample, 0, 1024)
	collector.events = make([]sharedforensics.Event, 0, 4096)

	for _, path := range files {
		if err := parseLogFile(path, cfg, &execs, &summaries, collector); err != nil {
			if isMissingFileError(err) {
				collector.io.FilesSkippedMissing++
				logSkipMissing(path, err, collector)
				continue
			}
			collector.io.FilesSkippedOtherErrs++
			logSkipOther(path, err, collector)
			continue
		}
		collector.io.FilesRead++
	}

	sort.Slice(execs, func(i, j int) bool {
		if !execs[i].TS.Equal(execs[j].TS) {
			return execs[i].TS.Before(execs[j].TS)
		}
		return execs[i].ExecID < execs[j].ExecID
	})
	sort.Slice(summaries, func(i, j int) bool {
		if !summaries[i].TS.Equal(summaries[j].TS) {
			return summaries[i].TS.Before(summaries[j].TS)
		}
		return summaries[i].TraceKey < summaries[j].TraceKey
	})

	res.ExecutionSamples = execs
	res.TradeSummaries = summaries
	selectedSource := resolveTradeSource(cfg.TradeSource, len(summaries) > 0)
	switch selectedSource {
	case "summary_only":
		res.Metrics = computeMetrics(cfg, execs, summaries)
		res.Metrics.TradesSource = "trade_close_summary"
	case "reconstruct":
		reTrades, reStats := sharedforensics.ReconstructTrades(collector.events, cfg.Symbol)
		res.ReconstructedTrades = reTrades
		res.Metrics = computeMetricsFromReconstructed(cfg, execs, reTrades, reStats)
		res.Metrics.TradesSource = "reconstruct"
		res.Metrics.Reconstruct = reconstructMeta{
			Tier1:             reStats.Tier1,
			Tier2:             reStats.Tier2,
			Tier3:             reStats.Tier3,
			ConfidenceAvg:     reStats.ConfidenceAvg,
			MissingFeeTrades:  reStats.MissingFeeTrades,
			MissingSideTrades: reStats.MissingSideTrades,
		}
	default:
		return evalResult{}, fmt.Errorf("unknown trade source: %s", selectedSource)
	}
	res.Metrics.Markers = collector.markers
	res.Metrics.IO = collector.io
	res.Metrics.Diagnostics.TopUnmatchedSample = append([]string(nil), collector.unmatchedSamples...)
	res.Metrics.Diagnostics.ZeroTradesReason = detectZeroTradesReason(res.Metrics)
	if res.Metrics.MakerRatio == 0 && res.Metrics.ExecutionSamples > 0 {
		res.Metrics.Diagnostics.MakerCoverageAssessment = "insufficient maker flag coverage"
	}
	return res, nil
}

func resolveTradeSource(flagValue string, hasSummary bool) string {
	switch flagValue {
	case "summary_only":
		return "summary_only"
	case "reconstruct":
		return "reconstruct"
	case "auto":
		if hasSummary {
			return "summary_only"
		}
		return "reconstruct"
	default:
		return "summary_only"
	}
}

func detectZeroTradesReason(m evalMetrics) string {
	if m.Trades > 0 {
		return ""
	}
	if m.IO.FilesTotal == 0 {
		return "time_window_has_no_logs"
	}
	if m.IO.FilesRead == 0 && m.IO.FilesSkippedMissing == m.IO.FilesTotal {
		return "all_files_skipped_missing"
	}
	if m.Markers.TradeCloseSummaryLines == 0 {
		return "no_trade_close_summary"
	}
	if m.TradesSource == "reconstruct" && m.Markers.TradeEventLines == 0 && m.Markers.ExecutionFillLines == 0 {
		return "parser_patterns_mismatch"
	}
	if m.Markers.ExecutionFillLines == 0 {
		return "no_execution_fill"
	}
	if m.Trades == 0 && m.Markers.TradeCloseSummaryLines > 0 {
		return "parser_patterns_mismatch"
	}
	return "unknown"
}

func parseLogFile(path string, cfg evalConfig, execs *[]executionSample, summaries *[]tradeSummarySample, collector *evalCollector) error {
	r, err := openMaybeGzip(path)
	if err != nil {
		return err
	}
	defer r.Close()

	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1024*1024)
	sc.Buffer(buf, 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if collector != nil {
			collector.io.LinesScanned++
			if evs, ok := sharedforensics.ParseLogEvents(line, path, lineNo, time.UTC); ok {
				for _, ev := range evs {
					if ev.TS.Before(cfg.From) || ev.TS.After(cfg.To) {
						continue
					}
					collector.events = append(collector.events, ev)
				}
			}
		}
		lineTS, msg, ok := parseLogLinePrefix(line)
		if !ok {
			continue
		}
		if lineTS.Before(cfg.From) || lineTS.After(cfg.To) {
			continue
		}
		if containsFold(msg, "execution_list_response") && collector != nil {
			collector.markers.ExecutionListResponseLine++
		}
		if containsFold(msg, "trade_event") && collector != nil {
			collector.markers.TradeEventLines++
		}
		if payload, markerFound := extractPayloadAfterMarker(msg, "trade_close_summary"); markerFound {
			if collector != nil {
				collector.markers.TradeCloseSummaryLines++
			}
			sample, ok := parseTradeCloseSummary(payload, lineTS, cfg.Symbol)
			if ok {
				*summaries = append(*summaries, sample)
			} else if collector != nil {
				collector.addUnmatchedSample(msg)
			}
			continue
		}

		execMarkerFound := containsFold(msg, "execution_fill") || containsFold(msg, "execution fill:")
		if execMarkerFound {
			if collector != nil {
				collector.markers.ExecutionFillLines++
			}
			if payload, ok := extractPayloadAfterMarker(msg, "execution_fill"); ok {
				if sample, ok := parseExecutionFill(payload, lineTS, cfg.Symbol); ok {
					*execs = append(*execs, sample)
					continue
				}
			}
			// Support legacy textual line format, e.g. "Execution fill: tradeID=... execId=..."
			if payload, ok := extractPayloadAfterMarker(msg, "Execution fill"); ok {
				if sample, ok := parseExecutionFillText(payload, lineTS, cfg.Symbol); ok {
					*execs = append(*execs, sample)
					continue
				}
			}
			if sample, ok := parseExecutionFillText(msg, lineTS, cfg.Symbol); ok {
				*execs = append(*execs, sample)
				continue
			}
			if collector != nil {
				collector.addUnmatchedSample(msg)
			}
		}
	}
	return sc.Err()
}

func parseLogLinePrefix(line string) (time.Time, string, bool) {
	m := lineTSRegex.FindStringSubmatch(line)
	if len(m) != 2 {
		return time.Time{}, "", false
	}
	ts, err := time.ParseInLocation(lineTimeLayout, m[1], time.UTC)
	if err != nil {
		return time.Time{}, "", false
	}
	msg := line
	if idx := strings.Index(line, "] "); idx >= 0 {
		msg = line[idx+2:]
	}
	return ts.UTC(), msg, true
}

func extractPayloadAfterMarker(msg, marker string) (string, bool) {
	idx := strings.Index(strings.ToLower(msg), strings.ToLower(marker))
	if idx < 0 {
		return "", false
	}
	payload := strings.TrimSpace(msg[idx+len(marker):])
	if strings.HasPrefix(payload, ":") {
		payload = strings.TrimSpace(strings.TrimPrefix(payload, ":"))
	}
	return payload, true
}

func parseTradeCloseSummary(payload string, lineTS time.Time, symbol string) (tradeSummarySample, bool) {
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return tradeSummarySample{}, false
	}
	sym := strings.ToUpper(strings.TrimSpace(pickString(m, "symbol", "Symbol", "s")))
	if symbol != "" && sym != "" && sym != symbol {
		return tradeSummarySample{}, false
	}
	net := pickFloat(m, "net_calc", "netCalc")
	netSource := strings.ToLower(strings.TrimSpace(pickString(m, "net_source", "netSource")))
	if netSource == "exchange" {
		if v, ok := pickOptionalFloat(m, "net_exchange", "netExchange"); ok {
			net = v
		}
	}
	feeTotal := pickFloat(m, "fee_total", "feeTotal")
	if feeTotal == 0 {
		feeTotal = math.Abs(pickFloat(m, "fee_open_alloc", "feeOpenAlloc")) + math.Abs(pickFloat(m, "fee_close", "feeClose"))
	}
	ts := asTime(pickAny(m, "ts", "time"), lineTS)
	if sym == "" {
		sym = strings.ToUpper(strings.TrimSpace(symbol))
	}
	return tradeSummarySample{
		TS:       ts,
		TraceKey: pickString(m, "traceKey", "trace_key", "lifecycleId", "lifecycle_id", "tradeId", "tradeID"),
		Symbol:   sym,
		Gross:    pickFloat(m, "gross_calc", "grossCalc"),
		FeeTotal: feeTotal,
		Net:      net,
		Duration: pickFloat(m, "duration_sec", "durationSec"),
	}, true
}

func parseExecutionFill(payload string, lineTS time.Time, symbol string) (executionSample, bool) {
	m := map[string]interface{}{}
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		return executionSample{}, false
	}
	sym := strings.ToUpper(strings.TrimSpace(pickString(m, "symbol", "Symbol", "s")))
	if symbol != "" && sym != "" && sym != symbol {
		return executionSample{}, false
	}
	ts := asTime(pickAny(m, "ts", "execTime", "createdTime"), lineTS)
	isMaker, hasMaker := false, false
	if v, ok := pickOptionalBool(m, "isMaker", "is_maker"); ok {
		isMaker, hasMaker = v, true
	}
	if !hasMaker {
		lastLiq := strings.ToLower(strings.TrimSpace(pickString(m, "lastLiquidityInd")))
		switch lastLiq {
		case "addedliquidity":
			isMaker, hasMaker = true, true
		case "removedliquidity":
			isMaker, hasMaker = false, true
		}
	}
	if known, ok := pickOptionalBool(m, "isMakerKnown"); ok && !known {
		hasMaker = false
	}
	if sym == "" {
		sym = strings.ToUpper(strings.TrimSpace(symbol))
	}
	return executionSample{
		TS:       ts,
		ExecID:   pickString(m, "execId", "execID", "exec_id"),
		Symbol:   sym,
		HasMaker: hasMaker,
		IsMaker:  isMaker,
		ExecFee:  math.Abs(pickFloat(m, "execFee", "exec_fee", "fee")),
	}, true
}

func parseExecutionFillText(payload string, lineTS time.Time, symbol string) (executionSample, bool) {
	if strings.TrimSpace(payload) == "" {
		return executionSample{}, false
	}
	fields := map[string]string{}
	for _, m := range legacyKVRegex.FindAllStringSubmatch(payload, -1) {
		if len(m) != 3 {
			continue
		}
		fields[strings.ToLower(strings.TrimSpace(m[1]))] = strings.TrimSpace(m[2])
	}
	if len(fields) == 0 {
		return executionSample{}, false
	}
	sym := strings.ToUpper(strings.TrimSpace(fields["symbol"]))
	if sym == "" {
		sym = strings.ToUpper(strings.TrimSpace(fields["s"]))
	}
	if symbol != "" && sym != "" && sym != symbol {
		return executionSample{}, false
	}
	if sym == "" {
		sym = strings.ToUpper(strings.TrimSpace(symbol))
	}
	execID := fields["execid"]
	if execID == "" {
		execID = fields["exec_id"]
	}

	isMaker, hasMaker := false, false
	if raw, ok := fields["ismaker"]; ok {
		if v, ok := asOptionalBool(raw); ok {
			isMaker, hasMaker = v, true
		}
	}
	if !hasMaker {
		switch strings.ToLower(strings.TrimSpace(fields["lastliquidityind"])) {
		case "addedliquidity":
			isMaker, hasMaker = true, true
		case "removedliquidity":
			isMaker, hasMaker = false, true
		}
	}
	fee := 0.0
	if raw, ok := fields["execfee"]; ok {
		fee = math.Abs(asFloat(raw))
	}
	return executionSample{
		TS:       lineTS.UTC(),
		ExecID:   execID,
		Symbol:   sym,
		HasMaker: hasMaker,
		IsMaker:  isMaker,
		ExecFee:  fee,
	}, true
}

func computeMetrics(cfg evalConfig, execs []executionSample, summaries []tradeSummarySample) evalMetrics {
	m := evalMetrics{
		From:             cfg.From.Format(time.RFC3339),
		To:               cfg.To.Format(time.RFC3339),
		Symbol:           cfg.Symbol,
		Mode:             cfg.Mode,
		Trades:           len(summaries),
		ExecutionSamples: len(execs),
	}
	var winCount int
	var sumWin, sumLoss float64
	var sumNet float64
	var sumDur, sumDurWin, sumDurLoss float64
	var winDurCount, lossDurCount int
	var sumFee, sumGross float64

	for _, s := range summaries {
		sumNet += s.Net
		sumDur += s.Duration
		sumFee += math.Abs(s.FeeTotal)
		sumGross += s.Gross
		if s.Net > 0 {
			winCount++
			sumWin += s.Net
			sumDurWin += s.Duration
			winDurCount++
		} else if s.Net < 0 {
			sumLoss += s.Net
			sumDurLoss += s.Duration
			lossDurCount++
		}
	}
	if m.Trades > 0 {
		m.WinRate = float64(winCount) / float64(m.Trades)
		m.AvgNetAfterFee = sumNet / float64(m.Trades)
		m.AvgDuration = sumDur / float64(m.Trades)
	}
	if winCount > 0 {
		m.AvgWin = sumWin / float64(winCount)
	}
	lossCount := m.Trades - winCount
	if lossCount > 0 {
		m.AvgLoss = sumLoss / float64(lossCount)
	}
	if sumLoss < 0 {
		m.ProfitFactor = sumWin / math.Abs(sumLoss)
	}
	if winDurCount > 0 {
		m.AvgDurationWin = sumDurWin / float64(winDurCount)
	}
	if lossDurCount > 0 {
		m.AvgDurationLoss = sumDurLoss / float64(lossDurCount)
	}
	if absGross := math.Abs(sumGross); absGross > 1e-9 {
		m.FeeToGrossRatio = sumFee / absGross
	}
	maker, taker := 0, 0
	for _, e := range execs {
		if !e.HasMaker {
			continue
		}
		if e.IsMaker {
			maker++
		} else {
			taker++
		}
	}
	if maker+taker > 0 {
		m.MakerRatio = float64(maker) / float64(maker+taker)
	}
	return m
}

func computeMetricsFromReconstructed(cfg evalConfig, execs []executionSample, trades []sharedforensics.ReconstructedTrade, stats sharedforensics.ReconstructStats) evalMetrics {
	m := evalMetrics{
		From:             cfg.From.Format(time.RFC3339),
		To:               cfg.To.Format(time.RFC3339),
		Symbol:           cfg.Symbol,
		Mode:             cfg.Mode,
		Trades:           len(trades),
		ExecutionSamples: len(execs),
	}
	var winCount int
	var sumWin, sumLoss float64
	var sumNet float64
	var sumDur, sumDurWin, sumDurLoss float64
	var winDurCount, lossDurCount int
	var sumFee, sumGross float64
	var maker, taker, unknown int

	for _, tr := range trades {
		net := tr.NetCalc
		if net == 0 {
			net = tr.Realised - tr.FeeClose
		}
		gross := tr.GrossCalc
		if gross == 0 {
			gross = net + tr.FeeClose
		}
		fee := math.Abs(tr.FeeClose)
		sumNet += net
		sumDur += tr.DurationSec
		sumFee += fee
		sumGross += gross
		if net > 0 {
			winCount++
			sumWin += net
			sumDurWin += tr.DurationSec
			winDurCount++
		} else if net < 0 {
			sumLoss += net
			sumDurLoss += tr.DurationSec
			lossDurCount++
		}
		maker += tr.MakerFills
		taker += tr.TakerFills
		unknown += tr.UnknownFills
	}

	if m.Trades > 0 {
		m.WinRate = float64(winCount) / float64(m.Trades)
		m.AvgNetAfterFee = sumNet / float64(m.Trades)
		m.AvgDuration = sumDur / float64(m.Trades)
	}
	if winCount > 0 {
		m.AvgWin = sumWin / float64(winCount)
	}
	lossCount := m.Trades - winCount
	if lossCount > 0 {
		m.AvgLoss = sumLoss / float64(lossCount)
	}
	if sumLoss < 0 {
		m.ProfitFactor = sumWin / math.Abs(sumLoss)
	}
	if winDurCount > 0 {
		m.AvgDurationWin = sumDurWin / float64(winDurCount)
	}
	if lossDurCount > 0 {
		m.AvgDurationLoss = sumDurLoss / float64(lossDurCount)
	}
	if absGross := math.Abs(sumGross); absGross > 1e-9 {
		m.FeeToGrossRatio = sumFee / absGross
	}
	if maker+taker > 0 {
		m.MakerRatio = float64(maker) / float64(maker+taker)
	}
	if unknown > 0 && maker+taker == 0 {
		m.Diagnostics.MakerCoverageAssessment = "insufficient maker flag coverage"
	}
	m.Reconstruct = reconstructMeta{
		Tier1:             stats.Tier1,
		Tier2:             stats.Tier2,
		Tier3:             stats.Tier3,
		ConfidenceAvg:     stats.ConfidenceAvg,
		MissingFeeTrades:  stats.MissingFeeTrades,
		MissingSideTrades: stats.MissingSideTrades,
	}
	return m
}

func writeEvalOutputs(cfg evalConfig, res evalResult) error {
	if err := os.MkdirAll(cfg.Out, 0o755); err != nil {
		return err
	}
	jsonPath := filepath.Join(cfg.Out, "p6_eval_metrics.json")
	mdPath := filepath.Join(cfg.Out, "p6_eval_summary.md")
	reconPath := filepath.Join(cfg.Out, "p8_reconstructed_trades.tsv")

	data, err := json.MarshalIndent(res.Metrics, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(jsonPath, data, 0o644); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# P6 Evaluation Summary\n\n")
	b.WriteString(fmt.Sprintf("- Symbol: `%s`\n", res.Metrics.Symbol))
	b.WriteString(fmt.Sprintf("- Window: `%s` .. `%s`\n", res.Metrics.From, res.Metrics.To))
	b.WriteString(fmt.Sprintf("- Mode: `%s`\n", res.Metrics.Mode))
	b.WriteString(fmt.Sprintf("- Trade source: `%s`\n", res.Metrics.TradesSource))
	b.WriteString(fmt.Sprintf("- Trades: `%d`\n", res.Metrics.Trades))
	b.WriteString(fmt.Sprintf("- Execution samples: `%d`\n\n", res.Metrics.ExecutionSamples))

	if res.Metrics.TradesSource == "reconstruct" {
		b.WriteString("## Reconstruction\n\n")
		b.WriteString("| Item | Value |\n")
		b.WriteString("|---|---:|\n")
		b.WriteString(fmt.Sprintf("| tier1 | %d |\n", res.Metrics.Reconstruct.Tier1))
		b.WriteString(fmt.Sprintf("| tier2 | %d |\n", res.Metrics.Reconstruct.Tier2))
		b.WriteString(fmt.Sprintf("| tier3 | %d |\n", res.Metrics.Reconstruct.Tier3))
		b.WriteString(fmt.Sprintf("| confidenceAvg | %.6f |\n", res.Metrics.Reconstruct.ConfidenceAvg))
		b.WriteString(fmt.Sprintf("| missingFeeTrades | %d |\n", res.Metrics.Reconstruct.MissingFeeTrades))
		b.WriteString(fmt.Sprintf("| missingSideTrades | %d |\n\n", res.Metrics.Reconstruct.MissingSideTrades))
	}

	b.WriteString("## I/O + Markers\n\n")
	b.WriteString("| Item | Value |\n")
	b.WriteString("|---|---:|\n")
	b.WriteString(fmt.Sprintf("| io.filesTotal | %d |\n", res.Metrics.IO.FilesTotal))
	b.WriteString(fmt.Sprintf("| io.filesRead | %d |\n", res.Metrics.IO.FilesRead))
	b.WriteString(fmt.Sprintf("| io.filesSkippedMissing | %d |\n", res.Metrics.IO.FilesSkippedMissing))
	b.WriteString(fmt.Sprintf("| io.filesSkippedOtherErrors | %d |\n", res.Metrics.IO.FilesSkippedOtherErrs))
	b.WriteString(fmt.Sprintf("| io.linesScanned | %d |\n", res.Metrics.IO.LinesScanned))
	b.WriteString(fmt.Sprintf("| markers.tradeCloseSummaryLines | %d |\n", res.Metrics.Markers.TradeCloseSummaryLines))
	b.WriteString(fmt.Sprintf("| markers.executionFillLines | %d |\n", res.Metrics.Markers.ExecutionFillLines))
	b.WriteString(fmt.Sprintf("| markers.executionListResponseLines | %d |\n", res.Metrics.Markers.ExecutionListResponseLine))
	b.WriteString(fmt.Sprintf("| markers.tradeEventLines | %d |\n\n", res.Metrics.Markers.TradeEventLines))

	b.WriteString("| KPI | Value |\n")
	b.WriteString("|---|---:|\n")
	b.WriteString(fmt.Sprintf("| winrate | %.6f |\n", res.Metrics.WinRate))
	b.WriteString(fmt.Sprintf("| avg win | %.6f |\n", res.Metrics.AvgWin))
	b.WriteString(fmt.Sprintf("| avg loss | %.6f |\n", res.Metrics.AvgLoss))
	b.WriteString(fmt.Sprintf("| profit factor | %.6f |\n", res.Metrics.ProfitFactor))
	b.WriteString(fmt.Sprintf("| maker ratio | %.6f |\n", res.Metrics.MakerRatio))
	b.WriteString(fmt.Sprintf("| fee to gross ratio | %.6f |\n", res.Metrics.FeeToGrossRatio))
	b.WriteString(fmt.Sprintf("| avg net after fee | %.6f |\n", res.Metrics.AvgNetAfterFee))
	b.WriteString(fmt.Sprintf("| avg duration | %.6f |\n", res.Metrics.AvgDuration))
	b.WriteString(fmt.Sprintf("| avg duration win | %.6f |\n", res.Metrics.AvgDurationWin))
	b.WriteString(fmt.Sprintf("| avg duration loss | %.6f |\n", res.Metrics.AvgDurationLoss))
	if strings.TrimSpace(res.Metrics.Diagnostics.MakerCoverageAssessment) != "" {
		b.WriteString(fmt.Sprintf("\n- maker coverage: `%s`\n", res.Metrics.Diagnostics.MakerCoverageAssessment))
	}

	if res.Metrics.Trades == 0 {
		b.WriteString("\n## Zero-trade Diagnostics\n\n")
		b.WriteString(fmt.Sprintf("- zeroTradesReason: `%s`\n", res.Metrics.Diagnostics.ZeroTradesReason))
		b.WriteString("- Ensure runtime logs include `trade_close_summary` (usually `ENABLE_TRADE_SUMMARY_LOG=1`).\n")
		b.WriteString("- Ensure fill logs include `execution_fill` JSON or legacy `Execution fill:` lines (`ENABLE_FILL_JSON_LOG=1`).\n")
		b.WriteString("- Check `--from/--to` and `--tz`; the parser assumes line prefix `YYYY/MM/DD HH:MM:SS.ffffff` in UTC.\n")
		b.WriteString("- Marker patterns used: `trade_close_summary`, `execution_fill`, `Execution fill:`, `execution_list_response`, `trade_event`.\n")
		if len(res.Metrics.Diagnostics.TopUnmatchedSample) > 0 {
			b.WriteString("\n### Top Unmatched Samples\n\n")
			for _, sample := range res.Metrics.Diagnostics.TopUnmatchedSample {
				b.WriteString(fmt.Sprintf("- `%s`\n", sample))
			}
		}
	}
	if err := os.WriteFile(mdPath, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return writeReconstructedTradesTSV(reconPath, res.ReconstructedTrades)
}

func writeReconstructedTradesTSV(path string, trades []sharedforensics.ReconstructedTrade) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	w.Comma = '\t'
	header := []string{
		"trade_id", "tier", "confidence",
		"open_ts", "close_ts", "duration_sec",
		"side", "qty_total", "realised_delta_cum",
		"fee_close_sum",
		"maker_fills", "taker_fills", "unknown_fills",
		"trace_key", "lifecycle_id", "order_id", "trade_id_ref",
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for _, tr := range trades {
		row := []string{
			tr.TradeID,
			tr.Tier,
			fmt.Sprintf("%.6f", tr.Confidence),
			tr.OpenTS.UTC().Format(time.RFC3339Nano),
			tr.CloseTS.UTC().Format(time.RFC3339Nano),
			fmt.Sprintf("%.6f", tr.DurationSec),
			tr.Side,
			fmt.Sprintf("%.6f", tr.QtyTotal),
			fmt.Sprintf("%.6f", tr.Realised),
			fmt.Sprintf("%.6f", tr.FeeClose),
			strconv.Itoa(tr.MakerFills),
			strconv.Itoa(tr.TakerFills),
			strconv.Itoa(tr.UnknownFills),
			tr.TraceKey,
			tr.LifecycleID,
			tr.OrderID,
			tr.TradeRefID,
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

func asString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

func pickAny(m map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			return v
		}
	}
	return nil
}

func pickString(m map[string]interface{}, keys ...string) string {
	return asString(pickAny(m, keys...))
}

func pickFloat(m map[string]interface{}, keys ...string) float64 {
	return asFloat(pickAny(m, keys...))
}

func pickOptionalFloat(m map[string]interface{}, keys ...string) (float64, bool) {
	return asOptionalFloat(pickAny(m, keys...))
}

func pickOptionalBool(m map[string]interface{}, keys ...string) (bool, bool) {
	return asOptionalBool(pickAny(m, keys...))
}

func asFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		if strings.TrimSpace(t) == "" {
			return 0
		}
		f, _ := strconvParseFloat(t)
		return f
	default:
		return 0
	}
}

func asOptionalFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		return t, true
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	case string:
		if strings.TrimSpace(t) == "" || strings.EqualFold(strings.TrimSpace(t), "null") {
			return 0, false
		}
		f, err := strconvParseFloat(t)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func asOptionalBool(v interface{}) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case float64:
		if t == 1 {
			return true, true
		}
		if t == 0 {
			return false, true
		}
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return false, false
		}
		if f == 1 {
			return true, true
		}
		if f == 0 {
			return false, true
		}
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		switch s {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func asTime(v interface{}, fallback time.Time) time.Time {
	if v == nil {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		if t <= 0 {
			return fallback
		}
		// Heuristic: epoch ms if large enough.
		if t > 1e12 {
			return time.UnixMilli(int64(t)).UTC()
		}
		return time.Unix(int64(t), 0).UTC()
	case int64:
		if t <= 0 {
			return fallback
		}
		if t > 1e12 {
			return time.UnixMilli(t).UTC()
		}
		return time.Unix(t, 0).UTC()
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return asTime(f, fallback)
		}
		return fallback
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return fallback
		}
		ts, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			if t2, err2 := time.Parse(time.RFC3339, s); err2 == nil {
				return t2.UTC()
			}
			if num, nErr := strconvParseFloat(s); nErr == nil {
				return asTime(num, fallback)
			}
			return fallback
		}
		return ts.UTC()
	default:
		return fallback
	}
}

func strconvParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}
