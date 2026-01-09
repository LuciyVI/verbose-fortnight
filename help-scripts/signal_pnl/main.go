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
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/config"
)

type signal struct {
	Time   time.Time
	Side   string
	Price  float64
	Source string
}

type kline struct {
	Start time.Time
	Open  float64
	High  float64
	Low   float64
	Close float64
}

func main() {
	logsFlag := flag.String("logs", "trading_bot*.log*", "glob pattern(s) for log files (comma-separated)")
	csvPath := flag.String("csv", "", "CSV with columns time,open,high,low,close; if empty, fetch from Bybit")
	exitMode := flag.String("exit", "opposite", "exit mode: opposite or bars")
	holdBars := flag.Int("hold-bars", 30, "bars to hold when exit=bars, also used for MFE/MAE horizon")
	feePerc := flag.Float64("fee", 0, "round-trip fee percent (e.g. 0.001 for 0.1%); default from config")
	qty := flag.Float64("qty", 1, "position size for absolute PnL")
	intervalFlag := flag.String("interval", "", "kline interval (defaults to config.Interval)")
	symbolFlag := flag.String("symbol", "", "symbol (defaults to config.Symbol)")
	flag.Parse()

	cfg := config.LoadConfig()
	if *symbolFlag != "" {
		cfg.Symbol = *symbolFlag
	}
	if *intervalFlag != "" {
		cfg.Interval = *intervalFlag
	}
	if *feePerc == 0 {
		*feePerc = cfg.RoundTripFeePerc
	}

	signals, err := loadSignals(*logsFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse logs: %v\n", err)
		os.Exit(1)
	}
	if len(signals) == 0 {
		fmt.Println("No signals found.")
		return
	}

	sort.Slice(signals, func(i, j int) bool { return signals[i].Time.Before(signals[j].Time) })

	intervalDur, err := parseInterval(cfg.Interval)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid interval %q: %v\n", cfg.Interval, err)
		os.Exit(1)
	}

	var klines []kline
	if *csvPath != "" {
		klines, err = loadCSV(*csvPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to load CSV: %v\n", err)
			os.Exit(1)
		}
	} else {
		start := signals[0].Time.Add(-intervalDur)
		end := signals[len(signals)-1].Time.Add(intervalDur * time.Duration(*holdBars+1))
		klines, err = fetchKlines(cfg.DemoRESTHost, cfg.Symbol, cfg.Interval, start, end)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to fetch klines: %v\n", err)
			os.Exit(1)
		}
	}

	if len(klines) == 0 {
		fmt.Println("No kline data available.")
		return
	}
	sort.Slice(klines, func(i, j int) bool { return klines[i].Start.Before(klines[j].Start) })

	exitModeLower := strings.ToLower(strings.TrimSpace(*exitMode))
	if exitModeLower != "opposite" && exitModeLower != "bars" {
		fmt.Fprintf(os.Stderr, "unsupported exit mode: %s\n", exitModeLower)
		os.Exit(1)
	}

	results := analyzeSignals(signals, klines, intervalDur, exitModeLower, *holdBars, *feePerc, *qty)
	printResults(results, *feePerc)
}

func loadSignals(patterns string) ([]signal, error) {
	var files []string
	for _, pat := range strings.Split(patterns, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		matches, err := filepath.Glob(pat)
		if err != nil {
			return nil, err
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no log files matched %q", patterns)
	}
	sort.Strings(files)

	re := regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d+).*Signal received: dir=(LONG|SHORT).*close=([0-9.]+)`)
	layout := "2006/01/02 15:04:05.000000"

	var out []signal
	for _, path := range files {
		r, err := openMaybeGzip(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			m := re.FindStringSubmatch(line)
			if len(m) != 4 {
				continue
			}
			ts, err := time.ParseInLocation(layout, m[1], time.Local)
			if err != nil {
				continue
			}
			price, _ := strconv.ParseFloat(m[3], 64)
			out = append(out, signal{
				Time:   ts,
				Side:   m[2],
				Price:  price,
				Source: path,
			})
		}
		_ = r.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}
	}
	return out, nil
}

func openMaybeGzip(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{Reader: gz, Closer: multiCloser{gz, f}}, nil
	}
	return f, nil
}

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var firstErr error
	for _, c := range m {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func parseInterval(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	mins, err := strconv.Atoi(raw)
	if err != nil || mins <= 0 {
		return 0, fmt.Errorf("interval must be numeric minutes, got %q", raw)
	}
	return time.Duration(mins) * time.Minute, nil
}

func loadCSV(path string) ([]kline, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1

	var rows [][]string
	for {
		rec, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(rec) == 0 {
			continue
		}
		rows = append(rows, rec)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	startIdx := 0
	if !isNumeric(strings.TrimSpace(rows[0][0])) {
		startIdx = 1
	}

	var out []kline
	for _, rec := range rows[startIdx:] {
		if len(rec) < 5 {
			continue
		}
		ts, err := parseTimeValue(rec[0])
		if err != nil {
			continue
		}
		open := parseFloat(rec[1])
		high := parseFloat(rec[2])
		low := parseFloat(rec[3])
		close := parseFloat(rec[4])
		out = append(out, kline{Start: ts, Open: open, High: high, Low: low, Close: close})
	}
	return out, nil
}

func fetchKlines(host, symbol, interval string, start, end time.Time) ([]kline, error) {
	const path = "/v5/market/kline"
	limit := 200
	intervalDur, err := parseInterval(interval)
	if err != nil {
		return nil, err
	}
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()

	var out []kline
	client := &http.Client{Timeout: 10 * time.Second}

	for startMs < endMs {
		reqEnd := startMs + int64(limit)*intervalDur.Milliseconds()
		if reqEnd > endMs {
			reqEnd = endMs
		}

		req, _ := http.NewRequest("GET", host+path, nil)
		q := req.URL.Query()
		q.Set("category", "linear")
		q.Set("symbol", symbol)
		q.Set("interval", interval)
		q.Set("start", fmt.Sprintf("%d", startMs))
		q.Set("end", fmt.Sprintf("%d", reqEnd))
		q.Set("limit", fmt.Sprintf("%d", limit))
		req.URL.RawQuery = q.Encode()

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var payload struct {
			RetCode int    `json:"retCode"`
			RetMsg  string `json:"retMsg"`
			Result  struct {
				List [][]string `json:"list"`
			} `json:"result"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		if payload.RetCode != 0 {
			return nil, fmt.Errorf("kline error %d: %s", payload.RetCode, payload.RetMsg)
		}
		if len(payload.Result.List) == 0 {
			break
		}

		var maxStart int64
		for _, row := range payload.Result.List {
			if len(row) < 5 {
				continue
			}
			tsMs, _ := strconv.ParseInt(row[0], 10, 64)
			if tsMs > maxStart {
				maxStart = tsMs
			}
			out = append(out, kline{
				Start: time.UnixMilli(tsMs),
				Open:  parseFloat(row[1]),
				High:  parseFloat(row[2]),
				Low:   parseFloat(row[3]),
				Close: parseFloat(row[4]),
			})
		}
		if maxStart == 0 || maxStart+intervalDur.Milliseconds() <= startMs {
			break
		}
		startMs = maxStart + intervalDur.Milliseconds()
	}

	return dedupeKlines(out), nil
}

func dedupeKlines(in []kline) []kline {
	seen := make(map[int64]kline, len(in))
	for _, k := range in {
		seen[k.Start.UnixMilli()] = k
	}
	var out []kline
	for _, k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func parseFloat(raw string) float64 {
	val, _ := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return val
}

func isNumeric(raw string) bool {
	_, err := strconv.ParseFloat(raw, 64)
	return err == nil
}

func parseTimeValue(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, errors.New("empty time")
	}
	if isNumeric(raw) {
		val, _ := strconv.ParseInt(raw, 10, 64)
		if val > 1e12 {
			return time.UnixMilli(val), nil
		}
		return time.Unix(val, 0), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	layout := "2006-01-02 15:04:05"
	return time.ParseInLocation(layout, raw, time.Local)
}

type signalResult struct {
	Signal     signal
	ExitPrice  float64
	ExitTime   time.Time
	ExitReason string
	HoldBars   int
	PnL        float64
	PnLPct     float64
	MFE        float64
	MFEPct     float64
	MAE        float64
	MAEPct     float64
}

func analyzeSignals(signals []signal, klines []kline, interval time.Duration, exitMode string, holdBars int, feePerc, qty float64) []signalResult {
	var results []signalResult
	for i, sig := range signals {
		entryPrice := sig.Price
		if entryPrice == 0 {
			continue
		}
		startIdx := findBarAfter(klines, sig.Time)
		if startIdx == -1 {
			continue
		}

		var exitPrice float64
		var exitTime time.Time
		exitReason := ""
		endIdx := -1

		if exitMode == "opposite" {
			for j := i + 1; j < len(signals); j++ {
				if signals[j].Side != sig.Side {
					exitPrice = signals[j].Price
					exitTime = signals[j].Time
					exitReason = "opposite"
					endIdx = findBarAfter(klines, exitTime)
					break
				}
			}
			if endIdx == -1 {
				endIdx = min(startIdx+holdBars, len(klines)-1)
				exitPrice = klines[endIdx].Close
				exitTime = klines[endIdx].Start
				exitReason = "horizon"
			}
		} else {
			endIdx = min(startIdx+holdBars, len(klines)-1)
			exitPrice = klines[endIdx].Close
			exitTime = klines[endIdx].Start
			exitReason = "bars"
		}

		mfe, mae := computeMfeMae(sig.Side, entryPrice, klines, startIdx, endIdx)
		pnl := directionalPnL(sig.Side, entryPrice, exitPrice, qty)
		pnlNet := pnl - math.Abs(entryPrice*qty*feePerc)
		pnlPct := 0.0
		if entryPrice > 0 {
			pnlPct = (pnlNet / (entryPrice * qty)) * 100
		}

		results = append(results, signalResult{
			Signal:     sig,
			ExitPrice:  exitPrice,
			ExitTime:   exitTime,
			ExitReason: exitReason,
			HoldBars:   endIdx - startIdx,
			PnL:        pnlNet,
			PnLPct:     pnlPct,
			MFE:        mfe,
			MFEPct:     toPct(mfe, entryPrice),
			MAE:        mae,
			MAEPct:     toPct(mae, entryPrice),
		})
	}
	return results
}

func computeMfeMae(side string, entry float64, klines []kline, startIdx, endIdx int) (float64, float64) {
	if startIdx < 0 || endIdx < 0 || startIdx >= len(klines) {
		return 0, 0
	}
	if endIdx >= len(klines) {
		endIdx = len(klines) - 1
	}
	maxHigh := klines[startIdx].High
	minLow := klines[startIdx].Low
	for i := startIdx; i <= endIdx; i++ {
		if klines[i].High > maxHigh {
			maxHigh = klines[i].High
		}
		if klines[i].Low < minLow {
			minLow = klines[i].Low
		}
	}

	if side == "SHORT" {
		mfe := entry - minLow
		mae := entry - maxHigh
		return mfe, mae
	}
	mfe := maxHigh - entry
	mae := minLow - entry
	return mfe, mae
}

func directionalPnL(side string, entry, exit, qty float64) float64 {
	if strings.EqualFold(side, "SHORT") {
		return (entry - exit) * qty
	}
	return (exit - entry) * qty
}

func toPct(val, entry float64) float64 {
	if entry == 0 {
		return 0
	}
	return (val / entry) * 100
}

func findBarAfter(klines []kline, t time.Time) int {
	idx := sort.Search(len(klines), func(i int) bool {
		return klines[i].Start.After(t)
	})
	if idx >= len(klines) {
		return -1
	}
	return idx
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func printResults(results []signalResult, feePerc float64) {
	fmt.Printf("%-19s %-6s %-10s %-10s %-10s %-10s %-10s %-8s %-8s %-8s\n",
		"Time", "Side", "Entry", "Exit", "PnL", "PnL%", "ExitWhy", "MFE%", "MAE%", "Hold")

	total := 0.0
	totalPct := 0.0
	for _, r := range results {
		fmt.Printf("%-19s %-6s %-10.2f %-10.2f %-10.4f %-10.2f %-10s %-8.2f %-8.2f %-8d\n",
			r.Signal.Time.In(time.Local).Format("2006-01-02 15:04"),
			r.Signal.Side,
			r.Signal.Price,
			r.ExitPrice,
			r.PnL,
			r.PnLPct,
			r.ExitReason,
			r.MFEPct,
			r.MAEPct,
			r.HoldBars,
		)
		total += r.PnL
		totalPct += r.PnLPct
	}

	fmt.Printf("\nSignals: %d | Fee: %.4f\n", len(results), feePerc)
	fmt.Printf("Total PnL: %.4f | Sum PnL%%: %.2f\n", total, totalPct)
}
