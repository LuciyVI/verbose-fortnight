package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
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

func calcPnl(side string, entry, exit, qty, fee float64) float64 {
	pnl := (exit - entry) * qty
	if strings.EqualFold(side, "sell") || strings.EqualFold(side, "short") {
		pnl = (entry - exit) * qty
	}
	return pnl - fee
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
	fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-12s %-10s\n",
		"Time", "Side", "Qty", "Entry", "Exit", "PnL", "Closed P&L", "Fee")

	var csvBuilder strings.Builder
	if *outCSV != "" {
		csvBuilder.WriteString("time,side,qty,entry,exit,pnl,closed_pnl,fee\n")
	}

	var total, wins, losses, totalClosed float64
	for _, it := range items {
		t := parseTimeMs(it.UpdatedTime).In(time.Local).Format("2006-01-02 15:04")
		qty := parseFloat(it.Qty)
		entry := parseFloat(it.AvgEntryPrice)
		exit := parseFloat(it.AvgExitPrice)
		fee := calcFee(it, cfg.RoundTripFeePerc, entry, exit, qty)
		pnl := calcPnl(it.Side, entry, exit, qty, fee)
		closedPnl := parseFloat(it.CurPnl)

		total += pnl
		totalClosed += closedPnl
		if pnl >= 0 {
			wins += pnl
		} else {
			losses += pnl
		}

		fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-12s %-10s\n",
			t,
			it.Side,
			formatFloat(qty, 4),
			formatFloat(entry, 2),
			formatFloat(exit, 2),
			formatFloat(pnl, 4),
			formatFloat(closedPnl, 4),
			formatFloat(fee, 4),
		)

		if *outCSV != "" {
			fmt.Fprintf(&csvBuilder, "%s,%s,%s,%s,%s,%s,%s,%s\n",
				t,
				it.Side,
				formatFloat(qty, 4),
				formatFloat(entry, 2),
				formatFloat(exit, 2),
				formatFloat(pnl, 4),
				formatFloat(closedPnl, 4),
				formatFloat(fee, 4),
			)
		}
	}
	for _, op := range openPositions {
		fmt.Printf("%-19s %-5s %-10s %-10s %-10s %-10s %-12s %-10s\n",
			"OPEN",
			op.Side,
			formatFloat(op.Size, 4),
			formatFloat(op.AvgPrice, 2),
			"",
			"",
			"",
			"",
		)
		if *outCSV != "" {
			fmt.Fprintf(&csvBuilder, "%s,%s,%s,%s,%s,%s,%s,%s\n",
				"OPEN",
				op.Side,
				formatFloat(op.Size, 4),
				formatFloat(op.AvgPrice, 2),
				"",
				"",
				"",
				"",
			)
		}
	}

	fmt.Printf("\nTotal PnL: %.4f (wins %.4f, losses %.4f)\n", total, wins, losses)
	fmt.Printf("Total Closed P&L: %.4f\n", totalClosed)

	if *outCSV != "" {
		if err := os.WriteFile(*outCSV, []byte(csvBuilder.String()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write CSV: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("CSV saved to %s\n", *outCSV)
	}
}
