package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
)

type closedPnlItem struct {
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	CurPnl        string `json:"closedPnl"`
	AvgEntryPrice string `json:"avgEntryPrice"`
	AvgExitPrice  string `json:"avgExitPrice"`
	Qty           string `json:"qty"`
	CreatedTime   string `json:"createdTime"`
	UpdatedTime   string `json:"updatedTime"`
	ExecFee       string `json:"execFee"`
	CumEntryFee   string `json:"cumEntryFee"`
	CumExitFee    string `json:"cumExitFee"`
}

type openPosition struct {
	Side       string
	Size       float64
	AvgPrice   float64
	TakeProfit float64
	StopLoss   float64
}

type closedPnlResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []closedPnlItem `json:"list"`
	} `json:"result"`
}

type exitEvent struct {
	Time          time.Time
	Side          string
	Entry         float64
	TPInit        float64
	TPFinal       float64
	SLInit        float64
	SLFinal       float64
	TrailActive   bool
	TrailDistance float64
	EndReason     string
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func formatFloat(value float64, decimals int) string {
	return fmt.Sprintf("%.*f", decimals, value)
}

func parseTimeMs(msStr string) time.Time {
	ms, _ := strconv.ParseInt(msStr, 10, 64)
	if ms > 1e12 {
		return time.UnixMilli(ms)
	}
	// Some fields come in seconds; multiply if it looks too small
	return time.Unix(ms, 0)
}

func normalizeSide(side string) string {
	switch strings.ToLower(strings.TrimSpace(side)) {
	case "buy", "long":
		return "LONG"
	case "sell", "short":
		return "SHORT"
	default:
		return strings.ToUpper(strings.TrimSpace(side))
	}
}

func calcFee(item closedPnlItem, fallbackFeePerc, entry, exit, qty float64) float64 {
	fee := parseFloat(item.ExecFee)
	if fee == 0 {
		fee = parseFloat(item.CumEntryFee) + parseFloat(item.CumExitFee)
	}
	if fee == 0 && fallbackFeePerc > 0 && qty > 0 {
		price := entry
		if price == 0 {
			price = exit
		} else if exit > 0 {
			price = (entry + exit) / 2
		}
		notional := math.Abs(price * qty)
		fee = notional * fallbackFeePerc
	}
	if fee < 0 {
		fee = -fee
	}
	return fee
}

// CalcPnL returns gross and net PnL using a single sign convention.
// LONG:  gross = (exit - entry) * qty
// SHORT: gross = (entry - exit) * qty
// net = gross - fee (fee is total entry+exit commission, always >= 0)
func CalcPnL(side string, entry, exit, qty, fee float64) (gross, net float64) {
	gross = (exit - entry) * qty
	if strings.EqualFold(side, "sell") || strings.EqualFold(side, "short") {
		gross = (entry - exit) * qty
	}
	net = gross - fee
	return gross, net
}

func collectLogFiles(path string) ([]string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return []string{path}, nil
	}

	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "trading_bot") {
			return nil
		}
		if strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
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

func loadExitEvents(logPath string) ([]exitEvent, error) {
	files, err := collectLogFiles(logPath)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	timeLayout := "2006/01/02 15:04:05.000000"
	openRe := regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d+).+Position opened: (LONG|SHORT) ([0-9.]+) @ ([0-9.]+) \| TP ([0-9.]+)  SL ([0-9.]+)`)
	foundRe := regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d+).+Found open position: Side=([A-Za-z]+), Size=([0-9.]+), TP=([0-9.]+), SL=([0-9.]+)`)
	noPosRe := regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\.\d+).+No open positions found`)

	var events []exitEvent

	type session struct {
		active        bool
		start         time.Time
		lastSeen      time.Time
		side          string
		size          float64
		entry         float64
		tpInit        float64
		slInit        float64
		tpFinal       float64
		slFinal       float64
		trailActive   bool
		trailDistance float64
	}

	var cur session

	closeSession := func(end time.Time, reason string) {
		if !cur.active {
			return
		}
		trailDist := 0.0
		trailActive := cur.trailActive
		if cur.slInit > 0 && cur.slFinal > 0 && cur.slFinal != cur.slInit {
			trailDist = math.Abs(cur.slFinal - cur.slInit)
			trailActive = true
		}
		events = append(events, exitEvent{
			Time:          end,
			Side:          cur.side,
			Entry:         cur.entry,
			TPInit:        cur.tpInit,
			TPFinal:       cur.tpFinal,
			SLInit:        cur.slInit,
			SLFinal:       cur.slFinal,
			TrailActive:   trailActive,
			TrailDistance: trailDist,
			EndReason:     reason,
		})
		cur = session{}
	}

	for _, path := range files {
		r, err := openMaybeGzip(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if m := openRe.FindStringSubmatch(line); len(m) == 7 {
				ts, err := time.ParseInLocation(timeLayout, m[1], time.Local)
				if err != nil {
					continue
				}
				side := normalizeSide(m[2])
				size := parseFloat(m[3])
				entry := parseFloat(m[4])
				tp := parseFloat(m[5])
				sl := parseFloat(m[6])
				if cur.active && cur.side != "" && cur.side != side {
					closeSession(ts, "REVERSE")
				}
				if !cur.active {
					cur.active = true
					cur.start = ts
					cur.side = side
					cur.size = size
					cur.entry = entry
					cur.tpInit = tp
					cur.slInit = sl
				}
				cur.lastSeen = ts
				if cur.tpInit == 0 {
					cur.tpInit = tp
				}
				if cur.slInit == 0 {
					cur.slInit = sl
				}
				if cur.tpFinal != 0 && cur.tpFinal != tp {
					cur.trailActive = true
				}
				if cur.slFinal != 0 && cur.slFinal != sl {
					cur.trailActive = true
				}
				cur.tpFinal = tp
				cur.slFinal = sl
				continue
			}
			if m := foundRe.FindStringSubmatch(line); len(m) == 6 {
				ts, err := time.ParseInLocation(timeLayout, m[1], time.Local)
				if err != nil {
					continue
				}
				side := normalizeSide(m[2])
				size := parseFloat(m[3])
				tp := parseFloat(m[4])
				sl := parseFloat(m[5])
				if cur.active && cur.side != "" && cur.side != side {
					closeSession(ts, "REVERSE")
				}
				if !cur.active {
					cur.active = true
					cur.start = ts
					cur.side = side
					cur.size = size
					cur.tpInit = tp
					cur.slInit = sl
				}
				cur.lastSeen = ts
				if cur.tpInit == 0 {
					cur.tpInit = tp
				}
				if cur.slInit == 0 {
					cur.slInit = sl
				}
				if cur.tpFinal != 0 && cur.tpFinal != tp {
					cur.trailActive = true
				}
				if cur.slFinal != 0 && cur.slFinal != sl {
					cur.trailActive = true
				}
				cur.tpFinal = tp
				cur.slFinal = sl
				continue
			}
			if m := noPosRe.FindStringSubmatch(line); len(m) == 2 {
				ts, err := time.ParseInLocation(timeLayout, m[1], time.Local)
				if err != nil {
					continue
				}
				if cur.active {
					closeSession(ts, "FLAT")
				}
			}
		}
		_ = r.Close()
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	return events, nil
}

func pickExitEvent(events []exitEvent, exitTime time.Time, side string, maxDelta time.Duration, used []bool) (int, *exitEvent) {
	bestIdx := -1
	var bestDelta time.Duration
	for i, ev := range events {
		if used[i] {
			continue
		}
		if ev.Side != "" && ev.Side != side {
			continue
		}
		delta := exitTime.Sub(ev.Time)
		if delta < 0 {
			delta = -delta
		}
		if delta > maxDelta {
			continue
		}
		if bestIdx == -1 || delta < bestDelta {
			bestIdx = i
			bestDelta = delta
		}
	}
	if bestIdx == -1 {
		return -1, nil
	}
	return bestIdx, &events[bestIdx]
}

func inferExitReason(exitPrice float64, ev *exitEvent) string {
	if ev == nil {
		return "n/a"
	}
	tol := math.Max(exitPrice*0.0005, 0.5)
	if ev.TPFinal > 0 && math.Abs(exitPrice-ev.TPFinal) <= tol {
		return "TP"
	}
	if ev.SLFinal > 0 && math.Abs(exitPrice-ev.SLFinal) <= tol {
		if ev.TrailActive {
			return "TRAIL"
		}
		return "SL"
	}
	switch ev.EndReason {
	case "REVERSE":
		return "REVERSE"
	case "FLAT":
		return "TIME"
	default:
		return "OTHER"
	}
}

func fetchClosedPnlPage(client *api.RESTClient, symbol string, start, end int64, cursor string) ([]closedPnlItem, string, error) {
	const path = "/v5/position/closed-pnl"

	q := url.Values{}
	q.Set("category", "linear")
	if symbol != "" {
		q.Set("symbol", symbol)
	}
	q.Set("startTime", fmt.Sprintf("%d", start))
	q.Set("endTime", fmt.Sprintf("%d", end))
	q.Set("limit", "200")
	if cursor != "" {
		q.Set("cursor", cursor)
	}

	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := client.SignREST(client.Config.APISecret, ts, client.Config.APIKey, client.Config.RecvWindow, q.Encode())

	req, _ := http.NewRequest("GET", client.Config.DemoRESTHost+path+"?"+q.Encode(), nil)
	req.Header.Set("X-BAPI-API-KEY", client.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", client.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", sig)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	var r struct {
		RetCode int    `json:"retCode"`
		RetMsg  string `json:"retMsg"`
		Result  struct {
			List           []closedPnlItem `json:"list"`
			NextPageCursor string          `json:"nextPageCursor"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, "", err
	}
	if r.RetCode != 0 {
		return nil, "", fmt.Errorf("retCode=%d retMsg=%s", r.RetCode, r.RetMsg)
	}
	return r.Result.List, r.Result.NextPageCursor, nil
}

func fetchClosedPnl(client *api.RESTClient, symbol string, start, end int64) ([]closedPnlItem, error) {
	var all []closedPnlItem
	cursor := ""
	for {
		page, next, err := fetchClosedPnlPage(client, symbol, start, end, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" || len(page) == 0 {
			break
		}
		cursor = next
	}
	return all, nil
}

func fetchOpenPositions(client *api.RESTClient, symbol string) ([]openPosition, error) {
	list, err := client.GetPositionList(symbol)
	if err != nil {
		return nil, err
	}
	positions := make([]openPosition, 0, len(list))
	for _, item := range list {
		size := parseFloat(item.Size)
		if size <= 0 {
			continue
		}
		positions = append(positions, openPosition{
			Side:       item.Side,
			Size:       size,
			AvgPrice:   parseFloat(item.AvgPrice),
			TakeProfit: parseFloat(item.TakeProfit),
			StopLoss:   parseFloat(item.StopLoss),
		})
	}
	return positions, nil
}

func main() {
	hours := flag.Int("hours", 24, "lookback window in hours")
	symbolFlag := flag.String("symbol", "", "instrument symbol (defaults to config Symbol)")
	today := flag.Bool("today", false, "limit to current calendar day (local time); overrides -hours")
	outCSV := flag.String("out", "report.csv", "path to write CSV report (empty to disable)")
	logPath := flag.String("logs", "logs", "log file or directory for exit reason join (supports .log/.log.gz)")
	joinWindowSec := flag.Int("log-join-window", 300, "max seconds between log exit event and trade exit time")
	flag.Parse()

	cfg := config.LoadConfig()
	if *symbolFlag != "" {
		cfg.Symbol = *symbolFlag
	}

	client := api.NewRESTClient(cfg, nil)

	now := time.Now()
	end := now.UnixMilli()
	start := end - int64(time.Duration(*hours)*time.Hour/time.Millisecond)
	if *today {
		startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		start = startOfDay.UnixMilli()
	}

	items, err := fetchClosedPnl(client, cfg.Symbol, start, end)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error fetching closed PnL: %v\n", err)
		os.Exit(1)
	}

	var exitEvents []exitEvent
	if *logPath != "" {
		events, err := loadExitEvents(*logPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "log join disabled: %v\n", err)
		} else {
			exitEvents = events
		}
	}
	usedEvents := make([]bool, len(exitEvents))
	joinWindow := time.Duration(*joinWindowSec) * time.Second

	openPositions, openErr := fetchOpenPositions(client, cfg.Symbol)
	if openErr != nil {
		fmt.Fprintf(os.Stderr, "error fetching open positions: %v\n", openErr)
	}
	if len(items) == 0 && len(openPositions) == 0 {
		fmt.Println("No closed positions in the selected window.")
		return
	}

	sort.Slice(items, func(i, j int) bool {
		return parseTimeMs(items[i].UpdatedTime).Before(parseTimeMs(items[j].UpdatedTime))
	})

	windowLabel := fmt.Sprintf("last %dh", *hours)
	if *today {
		windowLabel = "today"
	}

	fmt.Printf("Closed PnL %s for %s\n", windowLabel, cfg.Symbol)
	fmt.Println("Sign convention: LONG gross=(exit-entry)*qty, SHORT gross=(entry-exit)*qty, net=gross-fee")
	fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-10s %-10s %-10s %-8s %-8s %-8s %-8s %-12s\n",
		"Time", "Side", "Qty", "Entry", "Exit", "GrossPnL", "Fee", "NetPnL", "ExitReason", "SLinit", "SLfinal", "TPinit", "TPfinal", "Trail")

	var csvBuilder strings.Builder
	if *outCSV != "" {
		csvBuilder.WriteString("time,side,qty,entry,exit,gross_pnl,fee,net_pnl,exit_reason,sl_init,sl_final,tp_init,tp_final,trail_active,trail_distance\n")
	}

	var totalNet, totalGross, totalFee, wins, losses float64
	for _, it := range items {
		t := parseTimeMs(it.UpdatedTime).In(time.Local).Format("2006-01-02 15:04")
		qty := parseFloat(it.Qty)
		entry := parseFloat(it.AvgEntryPrice)
		exit := parseFloat(it.AvgExitPrice)
		fee := calcFee(it, cfg.RoundTripFeePerc, entry, exit, qty)
		gross, net := CalcPnL(it.Side, entry, exit, qty, fee)
		exitReason := "n/a"
		slInit := ""
		slFinal := ""
		tpInit := ""
		tpFinal := ""
		trailActive := ""
		trailDistance := ""

		if len(exitEvents) > 0 {
			exitTime := parseTimeMs(it.UpdatedTime).In(time.Local)
			side := normalizeSide(it.Side)
			if idx, ev := pickExitEvent(exitEvents, exitTime, side, joinWindow, usedEvents); ev != nil {
				usedEvents[idx] = true
				exitReason = inferExitReason(exit, ev)
				if ev.SLInit > 0 {
					slInit = formatFloat(ev.SLInit, 2)
				}
				if ev.SLFinal > 0 {
					slFinal = formatFloat(ev.SLFinal, 2)
				}
				if ev.TPInit > 0 {
					tpInit = formatFloat(ev.TPInit, 2)
				}
				if ev.TPFinal > 0 {
					tpFinal = formatFloat(ev.TPFinal, 2)
				}
				if ev.TrailActive {
					trailActive = "true"
					if ev.TrailDistance > 0 {
						trailDistance = formatFloat(ev.TrailDistance, 2)
					}
				} else {
					trailActive = "false"
				}
			}
		}

		totalNet += net
		totalGross += gross
		totalFee += fee
		if net >= 0 {
			wins += net
		} else {
			losses += net
		}

		fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-10s %-10s %-10s %-8s %-8s %-8s %-8s %-12s\n",
			t,
			it.Side,
			formatFloat(qty, 4),
			formatFloat(entry, 2),
			formatFloat(exit, 2),
			formatFloat(gross, 4),
			formatFloat(fee, 4),
			formatFloat(net, 4),
			exitReason,
			slInit,
			slFinal,
			tpInit,
			tpFinal,
			trailActive,
		)

		if *outCSV != "" {
			fmt.Fprintf(&csvBuilder, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
				t,
				it.Side,
				formatFloat(qty, 4),
				formatFloat(entry, 2),
				formatFloat(exit, 2),
				formatFloat(gross, 4),
				formatFloat(fee, 4),
				formatFloat(net, 4),
				exitReason,
				slInit,
				slFinal,
				tpInit,
				tpFinal,
				trailActive,
				trailDistance,
			)
		}
	}
	for _, op := range openPositions {
		fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-10s %-10s %-10s %-8s %-8s %-8s %-8s %-12s\n",
			"OPEN",
			op.Side,
			formatFloat(op.Size, 4),
			formatFloat(op.AvgPrice, 2),
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
			"",
		)
		if *outCSV != "" {
			fmt.Fprintf(&csvBuilder, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
				"OPEN",
				op.Side,
				formatFloat(op.Size, 4),
				formatFloat(op.AvgPrice, 2),
				"",
				"",
				"",
				"",
				"",
				"",
				"",
				"",
				"",
				"",
				"",
			)
		}
	}

	fmt.Printf("\nTotal GrossPnL: %.4f\n", totalGross)
	fmt.Printf("Total Fee: %.4f\n", totalFee)
	fmt.Printf("Total NetPnL: %.4f (wins %.4f, losses %.4f)\n", totalNet, wins, losses)

	if *outCSV != "" {
		if err := os.WriteFile(*outCSV, []byte(csvBuilder.String()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write CSV: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("CSV saved to %s\n", *outCSV)
	}
}
