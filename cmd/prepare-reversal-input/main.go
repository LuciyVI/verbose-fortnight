package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type kline struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

func main() {
	symbol := flag.String("symbol", "BTCUSDT", "symbol")
	interval := flag.String("interval", "1", "Bybit kline interval")
	fromRaw := flag.String("from", "2026-05-20T10:00:00Z", "fetch start, RFC3339")
	toRaw := flag.String("to", "2026-05-20T13:50:52Z", "fetch end, RFC3339")
	host := flag.String("host", "https://api.bybit.com", "public market-data REST host")
	out := flag.String("out", "artifacts/replay/reversal_BTCUSDT_20260520.csv", "output CSV path")
	flag.Parse()

	from, err := time.Parse(time.RFC3339, *fromRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --from: %v\n", err)
		os.Exit(2)
	}
	to, err := time.Parse(time.RFC3339, *toRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --to: %v\n", err)
		os.Exit(2)
	}
	if !stringsHasHTTPPrefix(*host) {
		fmt.Fprintln(os.Stderr, "--host must be an http(s) public market-data host")
		os.Exit(2)
	}

	klines, err := fetchKlines(*host, *symbol, *interval, from.UTC(), to.UTC())
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch public klines: %v\n", err)
		os.Exit(1)
	}
	if len(klines) == 0 {
		fmt.Fprintln(os.Stderr, "no klines returned")
		os.Exit(1)
	}
	if err := writeReplayCSV(*out, klines); err != nil {
		fmt.Fprintf(os.Stderr, "write csv: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Prepared replay CSV: %s\n", *out)
	fmt.Printf("- symbol: %s\n", *symbol)
	fmt.Printf("- fetch_from/to: %s / %s\n", from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	fmt.Printf("- candles_loaded: %d\n", len(klines))
	fmt.Println("- orderbook: unavailable (historical top-book snapshots are not fetched)")
}

func stringsHasHTTPPrefix(host string) bool {
	return strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://")
}

func fetchKlines(host, symbol, interval string, start, end time.Time) ([]kline, error) {
	const path = "/v5/market/kline"
	const limit = 1000
	intervalDur := time.Minute
	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	client := &http.Client{Timeout: 15 * time.Second}
	var out []kline

	for startMs < endMs {
		reqEnd := startMs + int64(limit)*intervalDur.Milliseconds()
		if reqEnd > endMs {
			reqEnd = endMs
		}
		req, err := http.NewRequest("GET", host+path, nil)
		if err != nil {
			return nil, err
		}
		q := req.URL.Query()
		q.Set("category", "linear")
		q.Set("symbol", symbol)
		q.Set("interval", interval)
		q.Set("start", strconv.FormatInt(startMs, 10))
		q.Set("end", strconv.FormatInt(reqEnd, 10))
		q.Set("limit", strconv.Itoa(limit))
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
			return nil, fmt.Errorf("bybit kline error %d: %s", payload.RetCode, payload.RetMsg)
		}
		if len(payload.Result.List) == 0 {
			break
		}
		var maxStart int64
		for _, row := range payload.Result.List {
			if len(row) < 6 {
				continue
			}
			tsMs, _ := strconv.ParseInt(row[0], 10, 64)
			if tsMs > maxStart {
				maxStart = tsMs
			}
			out = append(out, kline{
				Time:   time.UnixMilli(tsMs).UTC(),
				Open:   parseFloat(row[1]),
				High:   parseFloat(row[2]),
				Low:    parseFloat(row[3]),
				Close:  parseFloat(row[4]),
				Volume: parseFloat(row[5]),
			})
		}
		nextStart := maxStart + intervalDur.Milliseconds()
		if maxStart == 0 || nextStart <= startMs {
			break
		}
		startMs = nextStart
	}
	return dedupeKlines(out), nil
}

func writeReplayCSV(path string, klines []kline) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"time", "open", "high", "low", "close", "volume", "best_bid", "best_ask", "best_bid_size", "best_ask_size", "bid_ask_imbalance", "spread_bps"}); err != nil {
		return err
	}
	for _, k := range klines {
		if err := w.Write([]string{
			k.Time.Format(time.RFC3339),
			formatFloat(k.Open),
			formatFloat(k.High),
			formatFloat(k.Low),
			formatFloat(k.Close),
			formatFloat(k.Volume),
			"", "", "", "", "", "",
		}); err != nil {
			return err
		}
	}
	return w.Error()
}

func dedupeKlines(in []kline) []kline {
	seen := make(map[int64]kline, len(in))
	for _, k := range in {
		seen[k.Time.UnixMilli()] = k
	}
	out := make([]kline, 0, len(seen))
	for _, k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out
}

func parseFloat(raw string) float64 {
	v, _ := strconv.ParseFloat(raw, 64)
	return v
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
