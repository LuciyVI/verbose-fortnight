package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
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
	Fee           string `json:"execFee"`
}

type closedPnlResp struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []closedPnlItem `json:"list"`
	} `json:"result"`
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseTimeMs(msStr string) time.Time {
	ms, _ := strconv.ParseInt(msStr, 10, 64)
	if ms > 1e12 {
		return time.UnixMilli(ms)
	}
	// Some fields come in seconds; multiply if it looks too small
	return time.Unix(ms, 0)
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

func main() {
	hours := flag.Int("hours", 24, "lookback window in hours")
	symbolFlag := flag.String("symbol", "", "instrument symbol (defaults to config Symbol)")
	today := flag.Bool("today", false, "limit to current calendar day (local time); overrides -hours")
	outCSV := flag.String("out", "report.csv", "path to write CSV report (empty to disable)")
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

	if len(items) == 0 {
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
	fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-10s\n", "Time", "Side", "Qty", "Entry", "Exit", "PnL", "Fee")

	var csvBuilder strings.Builder
	if *outCSV != "" {
		csvBuilder.WriteString("time,side,qty,entry,exit,pnl,fee\n")
	}

	var total, wins, losses float64
	for _, it := range items {
		t := parseTimeMs(it.UpdatedTime).In(time.Local).Format("2006-01-02 15:04")
		qty := parseFloat(it.Qty)
		entry := parseFloat(it.AvgEntryPrice)
		exit := parseFloat(it.AvgExitPrice)
		pnl := parseFloat(it.CurPnl)
		fee := parseFloat(it.Fee)

		total += pnl
		if pnl >= 0 {
			wins += pnl
		} else {
			losses += pnl
		}

		fmt.Printf("%-19s %-5s %-10.4f %-10.2f %-10.2f %-10.4f %-10.4f\n",
			t, it.Side, qty, entry, exit, pnl, fee)

		if *outCSV != "" {
			fmt.Fprintf(&csvBuilder, "%s,%s,%.4f,%.2f,%.2f,%.4f,%.4f\n",
				t, it.Side, qty, entry, exit, pnl, fee)
		}
	}
	fmt.Printf("\nTotal PnL: %.4f (wins %.4f, losses %.4f)\n", total, wins, losses)

	if *outCSV != "" {
		if err := os.WriteFile(*outCSV, []byte(csvBuilder.String()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write CSV: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("CSV saved to %s\n", *outCSV)
	}
}
