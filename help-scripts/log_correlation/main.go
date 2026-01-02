package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const publicMsgMarker = "Received public message from exchange:"
const (
	fontWidth   = 5
	fontHeight  = 7
	fontSpacing = 1
	labelMaxLen = 12
)

var font5x7 = map[rune][7]byte{
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F},
	'3': {0x1E, 0x01, 0x01, 0x0E, 0x01, 0x01, 0x1E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x1C},
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E},
	'D': {0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F},
	'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x10, 0x13, 0x11, 0x0F},
	'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'J': {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11},
	'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x11, 0x11, 0x11, 0x11},
	'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D},
	'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E},
	'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E},
	'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x11, 0x15, 0x1B, 0x11},
	'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04},
	'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	'_': {0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1F},
	'.': {0x00, 0x00, 0x00, 0x00, 0x00, 0x04, 0x04},
	'-': {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
}

type logMessage struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	Ts    float64         `json:"ts"`
	Cts   float64         `json:"cts"`
	Data  json.RawMessage `json:"data"`
}

type orderbookData struct {
	Symbol string     `json:"s"`
	Bids   [][]string `json:"b"`
	Asks   [][]string `json:"a"`
	U      float64    `json:"u"`
	Seq    float64    `json:"seq"`
}

type klineData struct {
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Interval  string  `json:"interval"`
	Open      string  `json:"open"`
	Close     string  `json:"close"`
	High      string  `json:"high"`
	Low       string  `json:"low"`
	Volume    string  `json:"volume"`
	Turnover  string  `json:"turnover"`
	Confirm   bool    `json:"confirm"`
	Timestamp float64 `json:"timestamp"`
}

type readCloser struct {
	io.Reader
	closeFn func() error
}

func (r *readCloser) Close() error {
	return r.closeFn()
}

type parseStats struct {
	Lines   int
	Matches int
	Parsed  int
	Skipped int
	Errors  int
}

type corrAccumulator struct {
	index    map[string]int
	features []string
	count    [][]int
	sumX     [][]float64
	sumY     [][]float64
	sumXX    [][]float64
	sumYY    [][]float64
	sumXY    [][]float64
}

func newCorrAccumulator() *corrAccumulator {
	return &corrAccumulator{
		index: make(map[string]int),
	}
}

func (acc *corrAccumulator) addRow(row map[string]float64) {
	if len(row) == 0 {
		return
	}
	idxs := make([]int, 0, len(row))
	vals := make([]float64, 0, len(row))
	for name, val := range row {
		idx := acc.ensureFeature(name)
		idxs = append(idxs, idx)
		vals = append(vals, val)
	}
	for i, idxI := range idxs {
		x := vals[i]
		for j, idxJ := range idxs {
			y := vals[j]
			acc.count[idxI][idxJ]++
			acc.sumX[idxI][idxJ] += x
			acc.sumY[idxI][idxJ] += y
			acc.sumXX[idxI][idxJ] += x * x
			acc.sumYY[idxI][idxJ] += y * y
			acc.sumXY[idxI][idxJ] += x * y
		}
	}
}

func (acc *corrAccumulator) ensureFeature(name string) int {
	if idx, ok := acc.index[name]; ok {
		return idx
	}
	idx := len(acc.features)
	acc.features = append(acc.features, name)
	acc.index[name] = idx
	extendIntMatrix(&acc.count)
	extendFloatMatrix(&acc.sumX)
	extendFloatMatrix(&acc.sumY)
	extendFloatMatrix(&acc.sumXX)
	extendFloatMatrix(&acc.sumYY)
	extendFloatMatrix(&acc.sumXY)
	return idx
}

func (acc *corrAccumulator) correlationMatrix(minPairs int) ([]string, [][]float64) {
	if len(acc.features) == 0 {
		return nil, nil
	}
	features := append([]string(nil), acc.features...)
	sort.Strings(features)

	size := len(features)
	matrix := make([][]float64, size)
	for i := range matrix {
		matrix[i] = make([]float64, size)
	}

	for i, nameI := range features {
		idxI := acc.index[nameI]
		for j, nameJ := range features {
			idxJ := acc.index[nameJ]
			matrix[i][j] = computeCorrelation(
				acc.count[idxI][idxJ],
				acc.sumX[idxI][idxJ],
				acc.sumY[idxI][idxJ],
				acc.sumXX[idxI][idxJ],
				acc.sumYY[idxI][idxJ],
				acc.sumXY[idxI][idxJ],
				minPairs,
			)
		}
	}
	return features, matrix
}

func extendIntMatrix(matrix *[][]int) {
	for i := range *matrix {
		(*matrix)[i] = append((*matrix)[i], 0)
	}
	size := len(*matrix) + 1
	row := make([]int, size)
	*matrix = append(*matrix, row)
}

func extendFloatMatrix(matrix *[][]float64) {
	for i := range *matrix {
		(*matrix)[i] = append((*matrix)[i], 0)
	}
	size := len(*matrix) + 1
	row := make([]float64, size)
	*matrix = append(*matrix, row)
}

type matrixState struct {
	mu       sync.RWMutex
	png      []byte
	stats    parseStats
	features int
	updated  time.Time
	err      string
}

type statsResponse struct {
	Lines    int    `json:"lines"`
	Matches  int    `json:"matches"`
	Parsed   int    `json:"parsed"`
	Skipped  int    `json:"skipped"`
	Errors   int    `json:"errors"`
	Features int    `json:"features"`
	Updated  string `json:"updated"`
	Error    string `json:"error,omitempty"`
}

func newMatrixState() *matrixState {
	return &matrixState{}
}

func (s *matrixState) update(features []string, matrix [][]float64, stats parseStats) {
	png, err := encodePNG(features, matrix)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats = stats
	s.features = len(features)
	if err != nil {
		s.err = err.Error()
		return
	}
	s.png = png
	s.updated = time.Now()
	s.err = ""
}

func (s *matrixState) pngSnapshot() ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.png) == 0 {
		return nil, false
	}
	out := make([]byte, len(s.png))
	copy(out, s.png)
	return out, true
}

func (s *matrixState) statsSnapshot() statsResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()
	updated := ""
	if !s.updated.IsZero() {
		updated = s.updated.Format(time.RFC3339)
	}
	return statsResponse{
		Lines:    s.stats.Lines,
		Matches:  s.stats.Matches,
		Parsed:   s.stats.Parsed,
		Skipped:  s.stats.Skipped,
		Errors:   s.stats.Errors,
		Features: s.features,
		Updated:  updated,
		Error:    s.err,
	}
}

func openLog(path string) (io.ReadCloser, error) {
	if path == "-" {
		return io.NopCloser(os.Stdin), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return file, nil
	}
	gz, err := gzip.NewReader(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &readCloser{
		Reader: gz,
		closeFn: func() error {
			if err := gz.Close(); err != nil {
				_ = file.Close()
				return err
			}
			return file.Close()
		},
	}, nil
}

func parseMessageLine(line string) (string, bool) {
	idx := strings.Index(line, publicMsgMarker)
	if idx == -1 {
		return "", false
	}
	payload := strings.TrimSpace(line[idx+len(publicMsgMarker):])
	if payload == "" {
		return "", false
	}
	return payload, true
}

func processLogLine(line, topicFilter string, acc *corrAccumulator, stats *parseStats) {
	stats.Lines++
	line = strings.TrimSuffix(line, "\r")
	payload, ok := parseMessageLine(line)
	if !ok {
		return
	}
	stats.Matches++
	features, topic, err := extractFeatures([]byte(payload))
	if err != nil {
		stats.Errors++
		return
	}
	if topicFilter != "" && !strings.Contains(topic, topicFilter) {
		stats.Skipped++
		return
	}
	if len(features) == 0 {
		stats.Skipped++
		return
	}
	if acc != nil {
		acc.addRow(features)
	}
	stats.Parsed++
}

func parseFloat(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func parseLevel(level []string) (float64, float64, bool) {
	if len(level) < 2 {
		return 0, 0, false
	}
	price, ok := parseFloat(level[0])
	if !ok {
		return 0, 0, false
	}
	qty, ok := parseFloat(level[1])
	if !ok {
		return 0, 0, false
	}
	return price, qty, true
}

func extractFeatures(payload []byte) (map[string]float64, string, error) {
	var msg logMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, "", err
	}

	features := make(map[string]float64)
	if msg.Ts != 0 {
		features["msg_ts"] = msg.Ts
	}
	if msg.Cts != 0 {
		features["msg_cts"] = msg.Cts
	}

	switch {
	case strings.HasPrefix(msg.Topic, "orderbook."):
		var ob orderbookData
		if err := json.Unmarshal(msg.Data, &ob); err != nil {
			return nil, msg.Topic, err
		}
		addOrderbookFeatures(features, ob)
		return features, msg.Topic, nil
	case strings.HasPrefix(msg.Topic, "kline."):
		var klines []klineData
		if err := json.Unmarshal(msg.Data, &klines); err != nil {
			var single klineData
			if err := json.Unmarshal(msg.Data, &single); err != nil {
				return nil, msg.Topic, err
			}
			klines = []klineData{single}
		}
		if len(klines) > 0 {
			addKlineFeatures(features, klines[0])
		}
		return features, msg.Topic, nil
	default:
		if len(msg.Data) == 0 {
			return features, msg.Topic, nil
		}
		if err := addGenericFeatures("data", msg.Data, features); err != nil {
			return nil, msg.Topic, err
		}
		return features, msg.Topic, nil
	}
}

func addOrderbookFeatures(features map[string]float64, ob orderbookData) {
	if ob.U != 0 {
		features["ob_u"] = ob.U
	}
	if ob.Seq != 0 {
		features["ob_seq"] = ob.Seq
	}

	bidCount, askCount := 0, 0
	bidQtySum, askQtySum := 0.0, 0.0
	bidNotionalSum, askNotionalSum := 0.0, 0.0
	bidQtyMax, askQtyMax := 0.0, 0.0
	bidPriceMin, bidPriceMax := 0.0, 0.0
	askPriceMin, askPriceMax := 0.0, 0.0
	bidMinSet, bidMaxSet := false, false
	askMinSet, askMaxSet := false, false

	for _, level := range ob.Bids {
		price, qty, ok := parseLevel(level)
		if !ok {
			continue
		}
		bidCount++
		bidQtySum += qty
		bidNotionalSum += price * qty
		if !bidMinSet || price < bidPriceMin {
			bidPriceMin = price
			bidMinSet = true
		}
		if !bidMaxSet || price > bidPriceMax {
			bidPriceMax = price
			bidMaxSet = true
		}
		if qty > bidQtyMax {
			bidQtyMax = qty
		}
	}

	for _, level := range ob.Asks {
		price, qty, ok := parseLevel(level)
		if !ok {
			continue
		}
		askCount++
		askQtySum += qty
		askNotionalSum += price * qty
		if !askMinSet || price < askPriceMin {
			askPriceMin = price
			askMinSet = true
		}
		if !askMaxSet || price > askPriceMax {
			askPriceMax = price
			askMaxSet = true
		}
		if qty > askQtyMax {
			askQtyMax = qty
		}
	}

	features["ob_bid_levels"] = float64(bidCount)
	features["ob_ask_levels"] = float64(askCount)
	features["ob_bid_qty_sum"] = bidQtySum
	features["ob_ask_qty_sum"] = askQtySum
	features["ob_bid_notional_sum"] = bidNotionalSum
	features["ob_ask_notional_sum"] = askNotionalSum

	if bidCount > 0 {
		features["ob_best_bid"] = bidPriceMax
		features["ob_bid_price_min"] = bidPriceMin
		features["ob_bid_price_max"] = bidPriceMax
		features["ob_bid_price_range"] = bidPriceMax - bidPriceMin
		features["ob_bid_qty_max"] = bidQtyMax
		features["ob_bid_qty_avg"] = bidQtySum / float64(bidCount)
		if bidQtySum > 0 {
			features["ob_bid_vwap"] = bidNotionalSum / bidQtySum
		}
	}

	if askCount > 0 {
		features["ob_best_ask"] = askPriceMin
		features["ob_ask_price_min"] = askPriceMin
		features["ob_ask_price_max"] = askPriceMax
		features["ob_ask_price_range"] = askPriceMax - askPriceMin
		features["ob_ask_qty_max"] = askQtyMax
		features["ob_ask_qty_avg"] = askQtySum / float64(askCount)
		if askQtySum > 0 {
			features["ob_ask_vwap"] = askNotionalSum / askQtySum
		}
	}

	if bidMinSet && askMinSet {
		features["ob_spread"] = askPriceMin - bidPriceMax
		features["ob_mid"] = (askPriceMin + bidPriceMax) / 2
	}

	if bidQtySum+askQtySum > 0 {
		features["ob_imbalance"] = (bidQtySum - askQtySum) / (bidQtySum + askQtySum)
	}
	if bidNotionalSum+askNotionalSum > 0 {
		features["ob_notional_imbalance"] = (bidNotionalSum - askNotionalSum) / (bidNotionalSum + askNotionalSum)
	}
}

func addKlineFeatures(features map[string]float64, k klineData) {
	if k.Start != 0 {
		features["kline_start"] = k.Start
	}
	if k.End != 0 {
		features["kline_end"] = k.End
	}
	if k.Timestamp != 0 {
		features["kline_timestamp"] = k.Timestamp
	}
	if k.Start != 0 && k.End != 0 {
		features["kline_duration"] = k.End - k.Start
	}

	open, okOpen := parseFloat(k.Open)
	closeVal, okClose := parseFloat(k.Close)
	high, okHigh := parseFloat(k.High)
	low, okLow := parseFloat(k.Low)
	volume, okVol := parseFloat(k.Volume)
	turnover, okTurnover := parseFloat(k.Turnover)

	if okOpen {
		features["kline_open"] = open
	}
	if okClose {
		features["kline_close"] = closeVal
	}
	if okHigh {
		features["kline_high"] = high
	}
	if okLow {
		features["kline_low"] = low
	}
	if okVol {
		features["kline_volume"] = volume
	}
	if okTurnover {
		features["kline_turnover"] = turnover
	}
	if okHigh && okLow {
		features["kline_range"] = high - low
		features["kline_mid"] = (high + low) / 2
	}
	if okOpen && okClose {
		features["kline_body"] = closeVal - open
	}
}

func addGenericFeatures(prefix string, raw json.RawMessage, out map[string]float64) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	collectNumericFeatures(prefix, v, out)
	return nil
}

func collectNumericFeatures(prefix string, v any, out map[string]float64) {
	switch typed := v.(type) {
	case map[string]any:
		for key, val := range typed {
			collectNumericFeatures(prefix+"."+key, val, out)
		}
	case []any:
		if len(typed) == 0 {
			out[prefix+".count"] = 0
			return
		}
		nums := make([]float64, 0, len(typed))
		for _, item := range typed {
			val, ok := numericValue(item)
			if !ok {
				out[prefix+".count"] = float64(len(typed))
				return
			}
			nums = append(nums, val)
		}
		minVal := nums[0]
		maxVal := nums[0]
		sum := 0.0
		for _, val := range nums {
			if val < minVal {
				minVal = val
			}
			if val > maxVal {
				maxVal = val
			}
			sum += val
		}
		out[prefix+".count"] = float64(len(nums))
		out[prefix+".sum"] = sum
		out[prefix+".min"] = minVal
		out[prefix+".max"] = maxVal
		out[prefix+".mean"] = sum / float64(len(nums))
	default:
		if val, ok := numericValue(typed); ok {
			out[prefix] = val
		}
	}
}

func numericValue(v any) (float64, bool) {
	switch typed := v.(type) {
	case float64:
		return typed, true
	case string:
		return parseFloat(typed)
	default:
		return 0, false
	}
}

func readLogFeatures(path, topicFilter string, acc *corrAccumulator) (parseStats, error) {
	reader, err := openLog(path)
	if err != nil {
		return parseStats{}, err
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	const maxLineSize = 10 * 1024 * 1024
	scanner.Buffer(make([]byte, 1024), maxLineSize)

	stats := parseStats{}

	for scanner.Scan() {
		processLogLine(scanner.Text(), topicFilter, acc, &stats)
	}
	if err := scanner.Err(); err != nil {
		return stats, err
	}
	if stats.Matches == 0 {
		return stats, errors.New("no matching public exchange messages found")
	}
	return stats, nil
}

func correlationMatrix(rows []map[string]float64, minPairs int) ([]string, [][]float64) {
	acc := newCorrAccumulator()
	for _, row := range rows {
		acc.addRow(row)
	}
	return acc.correlationMatrix(minPairs)
}

func computeCorrelation(count int, sumX, sumY, sumXX, sumYY, sumXY float64, minPairs int) float64 {
	if count < minPairs {
		return math.NaN()
	}
	denomX := float64(count)*sumXX - sumX*sumX
	denomY := float64(count)*sumYY - sumY*sumY
	denom := math.Sqrt(denomX * denomY)
	if denom == 0 {
		return math.NaN()
	}
	return (float64(count)*sumXY - sumX*sumY) / denom
}

func writeCSV(out io.Writer, features []string, matrix [][]float64) error {
	writer := csv.NewWriter(out)
	header := append([]string{""}, features...)
	if err := writer.Write(header); err != nil {
		return err
	}
	for i, name := range features {
		row := make([]string, 0, len(features)+1)
		row = append(row, name)
		for j := range features {
			val := matrix[i][j]
			if math.IsNaN(val) {
				row = append(row, "")
			} else {
				row = append(row, fmt.Sprintf("%.6f", val))
			}
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

func writePNG(out io.Writer, features []string, matrix [][]float64) error {
	if len(features) == 0 {
		return errors.New("no features to render")
	}
	size := len(features)
	scale := 1
	glyphWidth := fontWidth * scale
	glyphHeight := fontHeight * scale
	spacing := fontSpacing * scale

	valueChars := 5
	valueWidth := textWidth(valueChars, scale)
	cellWidth := valueWidth + 6
	cellHeight := glyphHeight + 6

	leftMargin := labelMaxLen*(glyphWidth+spacing) + 8
	topMargin := labelMaxLen*(glyphHeight+spacing) + 8

	width := leftMargin + cellWidth*size + 2
	height := topMargin + cellHeight*size + 2

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	fillRect(img, image.Rect(0, 0, width, height), color.RGBA{255, 255, 255, 255})

	for i := 0; i < size; i++ {
		for j := 0; j < size; j++ {
			val := matrix[i][j]
			cellColor := correlationColor(val)
			x0 := leftMargin + j*cellWidth
			y0 := topMargin + i*cellHeight
			fillRect(img, image.Rect(x0, y0, x0+cellWidth, y0+cellHeight), cellColor)

			text := formatCorr(val)
			textW := textWidth(len(text), scale)
			textX := x0 + (cellWidth-textW)/2
			textY := y0 + (cellHeight-glyphHeight)/2
			drawString(img, textX, textY, text, textColorFor(cellColor), scale)
		}
	}

	gridColor := color.RGBA{220, 220, 220, 255}
	for i := 0; i <= size; i++ {
		y := topMargin + i*cellHeight
		fillRect(img, image.Rect(leftMargin, y, leftMargin+cellWidth*size, y+1), gridColor)
	}
	for j := 0; j <= size; j++ {
		x := leftMargin + j*cellWidth
		fillRect(img, image.Rect(x, topMargin, x+1, topMargin+cellHeight*size), gridColor)
	}

	for i, name := range features {
		label := normalizeLabel(name, labelMaxLen)
		textW := textWidth(len(label), scale)
		x := leftMargin - textW - 4
		y := topMargin + i*cellHeight + (cellHeight-glyphHeight)/2
		drawString(img, x, y, label, color.RGBA{0, 0, 0, 255}, scale)
	}

	for j, name := range features {
		label := normalizeLabel(name, labelMaxLen)
		labelHeight := textHeight(len(label), scale)
		x := leftMargin + j*cellWidth + (cellWidth-glyphWidth)/2
		y := topMargin - labelHeight - 2
		if y < 2 {
			y = 2
		}
		drawStringVertical(img, x, y, label, color.RGBA{0, 0, 0, 255}, scale)
	}

	return png.Encode(out, img)
}

func normalizeLabel(input string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	input = strings.ToUpper(input)
	var b strings.Builder
	count := 0
	for _, r := range input {
		if count >= maxLen {
			break
		}
		if isAllowedGlyph(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
		count++
	}
	return b.String()
}

func isAllowedGlyph(r rune) bool {
	if r >= 'A' && r <= 'Z' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	switch r {
	case '_', '.', '-':
		return true
	default:
		return false
	}
}

func textWidth(chars int, scale int) int {
	if chars <= 0 {
		return 0
	}
	return chars*fontWidth*scale + (chars-1)*fontSpacing*scale
}

func textHeight(chars int, scale int) int {
	if chars <= 0 {
		return 0
	}
	return chars*fontHeight*scale + (chars-1)*fontSpacing*scale
}

func drawString(img *image.RGBA, x, y int, s string, fg color.RGBA, scale int) {
	cursor := x
	for _, r := range s {
		drawGlyph(img, cursor, y, r, fg, scale)
		cursor += fontWidth*scale + fontSpacing*scale
	}
}

func drawStringVertical(img *image.RGBA, x, y int, s string, fg color.RGBA, scale int) {
	cursor := y
	for _, r := range s {
		drawGlyph(img, x, cursor, r, fg, scale)
		cursor += fontHeight*scale + fontSpacing*scale
	}
}

func drawGlyph(img *image.RGBA, x, y int, r rune, fg color.RGBA, scale int) {
	glyph, ok := font5x7[r]
	if !ok {
		glyph = font5x7['_']
	}
	for row := 0; row < fontHeight; row++ {
		bits := glyph[row]
		for col := 0; col < fontWidth; col++ {
			if bits&(1<<(fontWidth-1-col)) == 0 {
				continue
			}
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x+col*scale+dx, y+row*scale+dy, fg)
				}
			}
		}
	}
}

func fillRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	draw.Draw(img, rect, &image.Uniform{C: c}, image.Point{}, draw.Src)
}

func formatCorr(val float64) string {
	if math.IsNaN(val) {
		return "NaN"
	}
	if math.Abs(val) < 0.005 {
		val = 0
	}
	return fmt.Sprintf("%.2f", val)
}

func correlationColor(val float64) color.RGBA {
	if math.IsNaN(val) {
		return color.RGBA{200, 200, 200, 255}
	}
	if val < -1 {
		val = -1
	}
	if val > 1 {
		val = 1
	}
	t := (val + 1) / 2
	blue := color.RGBA{46, 107, 197, 255}
	red := color.RGBA{208, 65, 60, 255}
	white := color.RGBA{255, 255, 255, 255}
	if t < 0.5 {
		return blendColor(blue, white, t/0.5)
	}
	return blendColor(white, red, (t-0.5)/0.5)
}

func blendColor(a, b color.RGBA, t float64) color.RGBA {
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return color.RGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: 255,
	}
}

func textColorFor(bg color.RGBA) color.RGBA {
	luma := 0.299*float64(bg.R) + 0.587*float64(bg.G) + 0.114*float64(bg.B)
	if luma < 128 {
		return color.RGBA{255, 255, 255, 255}
	}
	return color.RGBA{0, 0, 0, 255}
}

func encodePNG(features []string, matrix [][]float64) ([]byte, error) {
	var buf bytes.Buffer
	if err := writePNG(&buf, features, matrix); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func clampRefreshMs(interval time.Duration) int {
	if interval <= 0 {
		return 1000
	}
	ms := int(interval / time.Millisecond)
	if ms < 250 {
		return 250
	}
	return ms
}

func webHTML(refreshMs int) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Correlation Matrix</title>
  <style>
    body { font-family: Arial, sans-serif; margin: 16px; background: #f5f5f5; }
    h1 { margin: 0 0 8px 0; font-size: 20px; }
    #status { font-size: 12px; color: #333; margin-bottom: 8px; }
    #matrix { max-width: 100%%; height: auto; border: 1px solid #ccc; background: #fff; }
  </style>
</head>
<body>
  <h1>Correlation Matrix</h1>
  <div id="status">loading...</div>
  <img id="matrix" alt="Correlation matrix heatmap">
  <script>
    const refreshMs = %d;
    function refresh() {
      const img = document.getElementById('matrix');
      img.src = '/matrix.png?t=' + Date.now();
      fetch('/stats')
        .then(r => r.json())
        .then(data => {
          let text = 'lines=' + data.lines + ' matches=' + data.matches + ' parsed=' + data.parsed +
            ' skipped=' + data.skipped + ' errors=' + data.errors + ' features=' + data.features;
          if (data.updated) { text += ' updated=' + data.updated; }
          if (data.error) { text += ' error=' + data.error; }
          document.getElementById('status').textContent = text;
        })
        .catch(() => {
          document.getElementById('status').textContent = 'failed to load stats';
        });
    }
    setInterval(refresh, refreshMs);
    refresh();
  </script>
</body>
</html>`, refreshMs)
}

func startWebUI(addr string, state *matrixState, refresh time.Duration) (*http.Server, error) {
	if state == nil {
		return nil, errors.New("web ui requires matrix state")
	}
	refreshMs := clampRefreshMs(refresh)
	html := webHTML(refreshMs)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	})
	mux.HandleFunc("/matrix.png", func(w http.ResponseWriter, r *http.Request) {
		pngBytes, ok := state.pngSnapshot()
		if !ok {
			http.Error(w, "no matrix yet", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(pngBytes)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		resp := state.statsSnapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	go func() {
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "web ui error: %v\n", err)
		}
	}()
	return server, nil
}

func resolvePNGPath(outPath, pngPath string) string {
	if pngPath != "" {
		return pngPath
	}
	if outPath == "" {
		return ""
	}
	ext := filepath.Ext(outPath)
	base := strings.TrimSuffix(outPath, ext)
	if base == "" {
		base = outPath
	}
	return base + ".png"
}

func writeCSVToFile(path string, features []string, matrix [][]float64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writeCSV(file, features, matrix); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func writePNGToFile(path string, features []string, matrix [][]float64) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := writePNG(file, features, matrix); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func shouldRender(last time.Time, interval time.Duration) bool {
	if interval <= 0 {
		return true
	}
	return time.Since(last) >= interval
}

func reopenIfTruncated(path string, bytesRead int64, file **os.File, reader **bufio.Reader) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, nil
	}
	if info.Size() >= bytesRead {
		return false, nil
	}
	newFile, err := os.Open(path)
	if err != nil {
		return false, err
	}
	if *file != nil {
		_ = (*file).Close()
	}
	*file = newFile
	*reader = bufio.NewReader(newFile)
	return true, nil
}

func followLog(path, topicFilter string, acc *corrAccumulator, stats *parseStats, interval time.Duration, render func() error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	reader := bufio.NewReader(file)
	lastRender := time.Now().Add(-interval)
	pending := false
	partial := ""
	var bytesRead int64

	for {
		line, err := reader.ReadString('\n')
		if err == nil {
			bytesRead += int64(len(line))
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			line = partial + line
			partial = ""
			processLogLine(line, topicFilter, acc, stats)
			pending = true
			if shouldRender(lastRender, interval) {
				if err := render(); err != nil {
					return err
				}
				lastRender = time.Now()
				pending = false
			}
			continue
		}
		if errors.Is(err, io.EOF) {
			if line != "" {
				bytesRead += int64(len(line))
				partial += line
			}
			if pending && shouldRender(lastRender, interval) {
				if err := render(); err != nil {
					return err
				}
				lastRender = time.Now()
				pending = false
			}
			rotated, err := reopenIfTruncated(path, bytesRead, &file, &reader)
			if err != nil {
				return err
			}
			if rotated {
				bytesRead = 0
				partial = ""
			}
			time.Sleep(200 * time.Millisecond)
			continue
		}
		return err
	}
}

func main() {
	logPath := flag.String("log", "trading_bot.log", "path to log file (use - for stdin, supports .gz)")
	topicFilter := flag.String("topic", "", "only include messages where topic contains this substring")
	outPath := flag.String("out", "", "write CSV to this path (default stdout)")
	pngPath := flag.String("png", "", "write PNG heatmap (default derived from -out)")
	minPairs := flag.Int("min-pairs", 2, "minimum paired samples required for correlation")
	follow := flag.Bool("follow", false, "tail log and refresh outputs in real time")
	interval := flag.Duration("interval", 2*time.Second, "refresh interval when -follow is enabled")
	web := flag.Bool("web", false, "serve web UI with correlation heatmap")
	addr := flag.String("addr", "127.0.0.1:8080", "web UI listen address")
	flag.Parse()

	finalPNG := resolvePNGPath(*outPath, *pngPath)
	var state *matrixState
	if *web {
		state = newMatrixState()
		if _, err := startWebUI(*addr, state, *interval); err != nil {
			fmt.Fprintf(os.Stderr, "failed to start web ui: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "web ui: http://%s\n", *addr)
	}

	if *follow {
		if *logPath == "-" || strings.HasSuffix(*logPath, ".gz") {
			fmt.Fprintln(os.Stderr, "follow mode requires a regular log file (no stdin or .gz)")
			os.Exit(1)
		}
		if !*web && *outPath == "" && finalPNG == "" {
			fmt.Fprintln(os.Stderr, "follow mode requires -out, -png, or -web")
			os.Exit(1)
		}

		acc := newCorrAccumulator()
		stats := parseStats{}
		render := func() error {
			features, matrix := acc.correlationMatrix(*minPairs)
			if len(features) == 0 {
				return nil
			}
			if state != nil {
				state.update(features, matrix, stats)
			}
			if *outPath != "" {
				if err := writeCSVToFile(*outPath, features, matrix); err != nil {
					return err
				}
			}
			if finalPNG != "" {
				if err := writePNGToFile(finalPNG, features, matrix); err != nil {
					return err
				}
			}
			fmt.Fprintf(os.Stderr, "updated lines=%d matches=%d parsed=%d skipped=%d errors=%d features=%d\n",
				stats.Lines, stats.Matches, stats.Parsed, stats.Skipped, stats.Errors, len(features))
			return nil
		}
		if err := followLog(*logPath, *topicFilter, acc, &stats, *interval, render); err != nil {
			fmt.Fprintf(os.Stderr, "follow error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	acc := newCorrAccumulator()
	stats, err := readLogFeatures(*logPath, *topicFilter, acc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "log parse error: %v\n", err)
		os.Exit(1)
	}

	features, matrix := acc.correlationMatrix(*minPairs)
	if len(features) == 0 {
		fmt.Fprintln(os.Stderr, "no numeric features extracted")
		os.Exit(1)
	}
	if state != nil {
		state.update(features, matrix, stats)
	}

	if *outPath == "" {
		if err := writeCSV(os.Stdout, features, matrix); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write correlation matrix: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := writeCSVToFile(*outPath, features, matrix); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write correlation matrix: %v\n", err)
			os.Exit(1)
		}
	}

	if finalPNG != "" {
		if err := writePNGToFile(finalPNG, features, matrix); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write png: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "lines=%d matches=%d parsed=%d skipped=%d errors=%d features=%d\n",
		stats.Lines, stats.Matches, stats.Parsed, stats.Skipped, stats.Errors, len(features))

	if *web {
		select {}
	}
}
