package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type statusResponse struct {
	Time         time.Time          `json:"time"`
	Symbol       string             `json:"symbol"`
	Trend        string             `json:"trend"`
	RegimeStreak int                `json:"regimeStreak"`
	CandleSeq    uint64             `json:"candleSeq"`
	Signal       *signalSnapshot    `json:"signal"`
	Indicators   *indicatorSnapshot `json:"indicators"`
	Position     *positionSnapshot  `json:"position"`
}

type signalSnapshot struct {
	Kind       string    `json:"kind"`
	Direction  string    `json:"direction"`
	Strength   int       `json:"strength"`
	Contribs   []string  `json:"contribs"`
	HighConf   bool      `json:"highConf"`
	ClosePrice float64   `json:"closePrice"`
	CandleSeq  uint64    `json:"candleSeq"`
	Time       time.Time `json:"time"`
}

type indicatorSnapshot struct {
	Time       time.Time `json:"time"`
	Close      float64   `json:"close"`
	SMA        float64   `json:"sma"`
	RSI        float64   `json:"rsi"`
	MACDLine   float64   `json:"macdLine"`
	MACDSignal float64   `json:"macdSignal"`
	MACDHist   float64   `json:"macdHist"`
	ATR        float64   `json:"atr"`
	BBUpper    float64   `json:"bbUpper"`
	BBMiddle   float64   `json:"bbMiddle"`
	BBLower    float64   `json:"bbLower"`
	HTFBias    string    `json:"htfBias"`
}

type positionSnapshot struct {
	Side       string    `json:"side"`
	Size       float64   `json:"size"`
	EntryPrice float64   `json:"entryPrice"`
	TakeProfit float64   `json:"takeProfit"`
	StopLoss   float64   `json:"stopLoss"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func main() {
	defaultAddr := os.Getenv("STATUS_ADDR")
	if defaultAddr == "" {
		defaultAddr = "127.0.0.1:6061"
	}

	addr := flag.String("addr", defaultAddr, "status server address or URL")
	jsonOut := flag.Bool("json", false, "print raw JSON")
	timeout := flag.Duration("timeout", 5*time.Second, "HTTP timeout")
	flag.Parse()

	url := strings.TrimSpace(*addr)
	if url == "" {
		fmt.Fprintln(os.Stderr, "status address is empty")
		os.Exit(1)
	}
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}
	url = strings.TrimRight(url, "/") + "/status"

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read response: %v\n", err)
		os.Exit(1)
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "status request error: %s\n%s\n", resp.Status, string(body))
		os.Exit(1)
	}
	if *jsonOut {
		fmt.Println(string(body))
		return
	}

	var payload statusResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		fmt.Fprintf(os.Stderr, "failed to parse JSON: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Time: %s\n", formatTime(payload.Time))
	fmt.Printf("Symbol: %s\n", payload.Symbol)
	fmt.Printf("Trend: %s (streak=%d) candleSeq=%d\n", payload.Trend, payload.RegimeStreak, payload.CandleSeq)

	if payload.Signal == nil || payload.Signal.Time.IsZero() {
		fmt.Println("Signal: none")
	} else {
		fmt.Printf("Signal: %s strength=%d highConf=%t close=%.2f seq=%d time=%s\n",
			payload.Signal.Direction,
			payload.Signal.Strength,
			payload.Signal.HighConf,
			payload.Signal.ClosePrice,
			payload.Signal.CandleSeq,
			formatTime(payload.Signal.Time),
		)
		if len(payload.Signal.Contribs) > 0 {
			fmt.Printf("Signal contribs: %s\n", strings.Join(payload.Signal.Contribs, ", "))
		}
	}

	if payload.Position == nil || payload.Position.Size <= 0 {
		fmt.Println("Position: none")
	} else {
		fmt.Printf("Position: side=%s size=%.4f entry=%.2f TP=%.2f SL=%.2f updated=%s\n",
			payload.Position.Side,
			payload.Position.Size,
			payload.Position.EntryPrice,
			payload.Position.TakeProfit,
			payload.Position.StopLoss,
			formatTime(payload.Position.UpdatedAt),
		)
	}

	if payload.Indicators == nil || payload.Indicators.Time.IsZero() {
		fmt.Println("Indicators: none")
	} else {
		fmt.Printf(
			"Indicators: close=%.2f SMA=%.2f RSI=%.2f MACD=%.4f/%.4f/%.4f ATR=%.4f BB=%.2f/%.2f/%.2f HTF=%s updated=%s\n",
			payload.Indicators.Close,
			payload.Indicators.SMA,
			payload.Indicators.RSI,
			payload.Indicators.MACDLine,
			payload.Indicators.MACDSignal,
			payload.Indicators.MACDHist,
			payload.Indicators.ATR,
			payload.Indicators.BBLower,
			payload.Indicators.BBMiddle,
			payload.Indicators.BBUpper,
			payload.Indicators.HTFBias,
			formatTime(payload.Indicators.Time),
		)
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "n/a"
	}
	return t.UTC().Format(time.RFC3339)
}
