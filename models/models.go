package models

import (
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// InstrumentInfo holds instrument metadata
type InstrumentInfo struct {
	MinNotional float64
	MinQty      float64
	QtyStep     float64
	TickSize    float64
}

// TPJob represents a take profit job
type TPJob struct {
	Side       string
	Qty        float64
	EntryPrice float64
}

// SignalDetails represents details of a single signal
type SignalDetails struct {
	Kind   string  // "SMA_LONG", "MACD_LONG", etc.
	Weight int     // Weight of this signal
	Value  float64 // Additional value if needed e.g., indicator value
}

// Signal represents a trading signal
type Signal struct {
	Kind       string   // "GC" или "SMA"
	Direction  string   // LONG or SHORT
	Strength   int      // aggregated indicator confirmations
	Contribs   []string // names of indicators that fired
	HighConf   bool     // high-confidence, bypass normal confirmation rules
	ClosePrice float64  // цена закрытия свечи
	CandleSeq  uint64   // monotonic sequence of closed candles
	Time       time.Time
}

// ConsolidatedSignal represents a consolidated trading signal with multiple confirmations
type ConsolidatedSignal struct {
	Kind          string  // Overall signal type: "LONG" or "SHORT"
	ClosePrice    float64 // Current close price
	Time          time.Time
	SignalDetails []SignalDetails // All contributing signals
	TotalWeight   int             // Total weight of all contributing signals
	SignalSource  string          // Source of the signal: "consolidated" or "original"
}

// SignalSnapshot holds the latest emitted signal details for status reporting.
type SignalSnapshot struct {
	Kind       string    `json:"kind"`
	Direction  string    `json:"direction"`
	Strength   int       `json:"strength"`
	Contribs   []string  `json:"contribs,omitempty"`
	HighConf   bool      `json:"highConf"`
	ClosePrice float64   `json:"closePrice"`
	CandleSeq  uint64    `json:"candleSeq"`
	Time       time.Time `json:"time"`
}

// IndicatorSnapshot holds the latest indicator values for status reporting.
type IndicatorSnapshot struct {
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
	HTFBias    string    `json:"htfBias,omitempty"`
}

// PositionSnapshot holds the last known position details for status reporting.
type PositionSnapshot struct {
	Side       string    `json:"side"`
	Size       float64   `json:"size"`
	EntryPrice float64   `json:"entryPrice"`
	TakeProfit float64   `json:"takeProfit"`
	StopLoss   float64   `json:"stopLoss"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// OrderbookMsg represents orderbook message
type OrderbookMsg struct {
	Topic string        `json:"topic"`
	Type  string        `json:"type"` // snapshot | delta
	Data  OrderbookData `json:"data"`
}

// OrderbookData represents orderbook data
type OrderbookData struct {
	B [][]string `json:"b"` // [[price,size]]
	A [][]string `json:"a"`
}

// KlineMsg represents kline message
type KlineMsg struct {
	Topic string      `json:"topic"`
	Data  []KlineData `json:"data"`
}

// KlineData represents kline data
type KlineData struct {
	Open    string `json:"open"`
	High    string `json:"high"`
	Low     string `json:"low"`
	Close   string `json:"close"`
	Volume  string `json:"volume"`
	Confirm bool   `json:"confirm"`
}

// PositionFSMState is the deterministic lifecycle stage of a position.
type PositionFSMState string

const (
	PositionStateFlat    PositionFSMState = "FLAT"
	PositionStateOpening PositionFSMState = "OPENING"
	PositionStateOpen    PositionFSMState = "OPEN"
	PositionStateClosing PositionFSMState = "CLOSING"
)

// StopIntent requests a TP/SL update. Only StopController may write to exchange.
type StopIntent struct {
	Kind        string
	TradeID     string
	LifecycleID string
	Side        string
	Entry       float64
	QtyLeft     float64
	FeeEstimate float64
	TP          float64
	SL          float64
	SLPolicy    string
	// LastWinMovePct stores the latest profitable move fraction used by SL policy (0.01 = 1%).
	LastWinMovePct float64
	// DesiredSLPct is the pre-clamp SL fraction requested by policy (from entry).
	DesiredSLPct float64
	// SLPctFinal is the final SL fraction after clamp/guards (from entry).
	SLPctFinal float64
	// SLPriceFinal is the absolute final SL price intent sent to stop-controller.
	SLPriceFinal float64
	MinSLPct     float64
	MaxSLPct     float64
	Breakeven    bool
	Reason       string
	TS           time.Time
}

// StopController keeps desired/applied TP/SL levels for idempotent writes.
type StopController struct {
	DesiredTP float64
	DesiredSL float64
	AppliedTP float64
	AppliedSL float64
	TradeID   string
	Reason    string
	UpdatedAt time.Time
}

// ExecutionEvent is a normalized execution payload used by strategy/FSM logic.
type ExecutionEvent struct {
	TradeID           string
	ExecID            string
	OrderID           string
	OrderLinkID       string
	ExecSide          string
	PositionSide      string
	HasIsMaker        bool
	IsMaker           bool
	LastLiquidityInd  string
	ReduceOnly        bool
	Qty               float64
	Price             float64
	ExecFee           float64
	ExecPnl           float64
	HasExchangeNet    bool
	ExchangeNet       float64
	CreateType        string
	StopOrderType     string
	ClosedSize        float64
	LeavesQty         float64
	PositionSizeAfter float64
}

// TradingBlockReason is a typed reason for trading gate status.
type TradingBlockReason string

const (
	BlockedReasonNone      TradingBlockReason = ""
	BlockedReasonReconnect TradingBlockReason = "RECONNECT"
	BlockedReasonInvariant TradingBlockReason = "INVARIANT"
	BlockedReasonFatalAPI  TradingBlockReason = "FATAL_API"
	BlockedReasonManual    TradingBlockReason = "MANUAL"
)

// RuntimeCounters contains lightweight operational counters for P2.x features.
type RuntimeCounters struct {
	EdgePass               uint64 `json:"edgePass"`
	EdgeReject             uint64 `json:"edgeReject"`
	EdgeRejectMinDepth     uint64 `json:"edgeRejectMinDepth"`
	EdgeRejectWideSpread   uint64 `json:"edgeRejectWideSpread"`
	EdgeRejectLowImbalance uint64 `json:"edgeRejectLowImbalance"`

	BEIntentSent                 uint64 `json:"beIntentSent"`
	BEIntentSkippedAlreadyBetter uint64 `json:"beIntentSkippedAlreadyBetter"`

	BackfillFetched   uint64 `json:"backfillFetched"`
	BackfillProcessed uint64 `json:"backfillProcessed"`
	BackfillDeduped   uint64 `json:"backfillDeduped"`
	BackfillGaps      uint64 `json:"backfillGaps"`
	BackfillErrors    uint64 `json:"backfillErrors"`
	DryRunTicks       uint64 `json:"dryRunTicks"`

	MakerFills         uint64  `json:"makerFills"`
	TakerFills         uint64  `json:"takerFills"`
	MakerFallbackCount uint64  `json:"makerFallbackCount"`
	MakerTimeoutCount  uint64  `json:"makerTimeoutCount"`
	TotalExecFee       float64 `json:"totalExecFee"`
	TotalGrossRealised float64 `json:"totalGrossRealised"`
	TotalNetRealised   float64 `json:"totalNetRealised"`
	FeeToGrossRatio    float64 `json:"feeToGrossRatio"`
	MakerRatio         float64 `json:"makerRatio"`

	AvgTradeDuration    float64 `json:"avgTradeDuration"`
	AvgWinDuration      float64 `json:"avgWinDuration"`
	AvgLossDuration     float64 `json:"avgLossDuration"`
	AvgNetPerTrade      float64 `json:"avgNetPerTrade"`
	NetAfterFeePerTrade float64 `json:"netAfterFeePerTrade"`

	SLPolicyHalfLastWinCount      uint64 `json:"slPolicyHalfLastWinCount"`
	SLPolicyFallbackSmallWinCount uint64 `json:"slPolicyFallbackSmallWinCount"`
	SLPolicyClampViolationCount   uint64 `json:"slPolicyClampViolationCount"`
}

// RuntimeFeatures exposes enabled/disabled runtime feature flags in status output.
type RuntimeFeatures struct {
	FillJSONLog          bool `json:"fillJsonLog"`
	ExecutionResponseLog bool `json:"executionResponseLog"`
	LifecycleID          bool `json:"lifecycleId"`
	ExecutionBackfill    bool `json:"executionBackfill"`
	PartialBERule        bool `json:"partialBERule"`
	EdgeFilter           bool `json:"edgeFilter"`
	MakerFirst           bool `json:"makerFirst"`
	TradeSummaryLog      bool `json:"tradeSummaryLog"`
	KPIMonitoring        bool `json:"kpiMonitoring"`
	StatusServer         bool `json:"statusServer"`
	ConfigEndpoint       bool `json:"configEndpoint"`
	DryRun               bool `json:"dryRun"`
}

// RuntimeHealth contains recent runtime timestamps/errors for operator diagnostics.
type RuntimeHealth struct {
	LastBackfillCycleTS time.Time `json:"lastBackfillCycleTs"`
	LastBackfillError   string    `json:"lastBackfillError"`
	LastBackfillErrorTS time.Time `json:"lastBackfillErrorTs"`
	LastWSExecutionTS   time.Time `json:"lastWsExecutionTs"`
	LastEdgeDecisionTS  time.Time `json:"lastEdgeDecisionTs"`
	LastDryRunTickTS    time.Time `json:"lastDryRunTickTs"`
	StatusServerError   string    `json:"statusServerError"`
	StatusServerStarted time.Time `json:"statusServerStartedTs"`
}

// CloseFloat returns the close price as float64
func (k KlineData) CloseFloat() float64 {
	f, _ := strconv.ParseFloat(k.Close, 64)
	return f
}

// HighFloat returns the high price as float64
func (k KlineData) HighFloat() float64 {
	f, _ := strconv.ParseFloat(k.High, 64)
	return f
}

// LowFloat returns the low price as float64
func (k KlineData) LowFloat() float64 {
	f, _ := strconv.ParseFloat(k.Low, 64)
	return f
}

// State represents the application state
type State struct {
	// Global variables
	Debug     bool
	DynamicTP bool

	// Atomic pointers for WebSocket connections
	PrivPtr atomic.Pointer[websocket.Conn]
	PubPtr  atomic.Pointer[websocket.Conn]

	// Constants for trading
	SlPerc    float64 // Процент фиксированного SL от цены входа (0.25%)
	TrailPerc float64 // Процент трейлинг-стопа (0.5%)
	SmaLen    int     // окно для SMA-воркера

	// Orderbook state
	OrderbookReady atomic.Bool
	PosSide        string
	OrderQty       float64
	PositionState  PositionFSMState

	// Strategy state
	Closes    []float64
	Highs     []float64
	Lows      []float64
	Volumes   []float64
	HTFCloses []float64
	CandleSeq atomic.Uint64

	// Longer-term data for higher-order trend filter
	LongTermCloses []float64
	LongTermHighs  []float64
	LongTermLows   []float64

	// Instrument information
	Instr InstrumentInfo

	// Orderbook maps with mutex protection
	ObLock             sync.Mutex
	BidsMap            map[string]float64
	AsksMap            map[string]float64
	ObImbalanceHistory []float64

	// Signal statistics
	SignalStats struct {
		Total, Correct, FalsePositive int
		sync.Mutex
	}

	// Profit tracking
	RealizedPnL   float64
	UnrealizedPnL float64
	TotalProfit   float64
	TotalLoss     float64
	sync.RWMutex
	PartialTPDone  bool
	LastExitAt     time.Time
	LastExitDir    string
	LastEntryAt    time.Time
	LastEntryPrice float64
	LastEntryDir   string
	LastOrderID    string
	LastExecID     string
	LastExecCursor string
	ActiveTradeID  string
	PendingSide    string
	PendingPrice   float64
	PendingTradeID string
	OrderTradeIDs  map[string]string
	ExecTradeIDs   map[string]string
	OpeningExecAck bool
	OpeningPosAck  bool
	TradingBlocked bool
	BlockReason    TradingBlockReason
	BlockMessage   string
	BlockSetAt     time.Time
	StopCtrl       StopController
	ProcessedExec  map[string]time.Time
	ExecSeenOrder  []string
	ExecDedupMax   int
	ExecDedupTTL   time.Duration
	// MoveSLToBE dedup guard (per lifecycle/target).
	LastBEIntentTradeID string
	LastBEIntentSL      float64
	LastBEIntentTS      time.Time
	// Last profitable move tracking for SL policy (fractions, e.g. 0.01 = 1%).
	LastWinMovePct      float64
	LastWinMovePctLong  float64
	LastWinMovePctShort float64
	LastWinMoveSide     string
	LastWinMoveSource   string
	LastWinMoveNetPnL   float64
	LastWinMoveEntry    float64
	LastWinMoveExit     float64
	LastWinMoveUpdated  time.Time
	// Runtime counters/health for observability.
	RuntimeCounters RuntimeCounters
	RuntimeHealth   RuntimeHealth
	RuntimeFeatures RuntimeFeatures

	// Internal P5 aggregates used to compute running averages in RuntimeCounters.
	tradeCount       uint64
	winTradeCount    uint64
	lossTradeCount   uint64
	tradeDurationSum float64
	winDurationSum   float64
	lossDurationSum  float64
	netAfterFeeSum   float64

	// Volume tracking
	RecentVolumes []float64 // Store recent volumes for calculating average/surge detection

	// Position tracking for partial profits
	PartialTPTriggered bool    // Flag to indicate if partial profit has been taken
	PartialTPPrice     float64 // Price at which partial profit was triggered

	// Re-entry tracking
	LastExitPrice float64   // Price at which the last position was closed
	LastExitTime  time.Time // Time at which the last position was closed
	LastExitSide  string    // Side of the last closed position ("LONG" or "SHORT")

	// Multi-timeframe analysis
	HigherTimeframeCloses []float64 // Closes for higher timeframe (e.g., 5-min candles aggregated from 1-min)
	HigherTimeframeHighs  []float64 // Highs for higher timeframe
	HigherTimeframeLows   []float64 // Lows for higher timeframe
	HigherTimeframeTrend  string    // Trend direction on higher timeframe ("up", "down", "neutral")
	LastHTFUpdateTime     time.Time // Time of last higher timeframe candle update

	// Divergence tracking
	PreviousPrices           []float64 // Previous prices for divergence detection
	PreviousRSIValues        []float64 // Previous RSI values for divergence detection
	PreviousMACDValues       []float64 // Previous MACD values for divergence detection
	PreviousMACDSignalValues []float64 // Previous MACD signal values for divergence detection

	// Partial profit and trailing tracking
	ActiveTrailingPositions  map[string]bool    // Track which positions are in trailing mode
	PartialProfitTriggers    map[string]bool    // Track if partial profit has been triggered
	TrailingStopLevels       map[string]float64 // Current trailing stop levels for positions
	TrailingTakeProfitLevels map[string]float64 // Current trailing TP levels for positions

	// Status snapshots for external reporting
	StatusLock     sync.RWMutex
	LastSignal     SignalSnapshot
	LastIndicators IndicatorSnapshot
	LastPosition   PositionSnapshot

	// Channels
	TPChan          chan TPJob
	StopIntentChan  chan StopIntent
	SigChan         chan Signal
	MarketRegime    string
	RegimeCandidate string
	RegimeStreak    int
}

func (s *State) RecordEdgeDecision(pass bool, reason string, ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	if pass {
		s.RuntimeCounters.EdgePass++
	} else {
		s.RuntimeCounters.EdgeReject++
		switch reason {
		case "min_depth":
			s.RuntimeCounters.EdgeRejectMinDepth++
		case "wide_spread":
			s.RuntimeCounters.EdgeRejectWideSpread++
		case "low_imbalance":
			s.RuntimeCounters.EdgeRejectLowImbalance++
		}
	}
	s.RuntimeHealth.LastEdgeDecisionTS = ts
	s.Unlock()
}

func (s *State) RecordBEIntentSent(tradeID string, targetSL float64, ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeCounters.BEIntentSent++
	s.LastBEIntentTradeID = tradeID
	s.LastBEIntentSL = targetSL
	s.LastBEIntentTS = ts
	s.Unlock()
}

func (s *State) RecordBEIntentSkippedAlreadyBetter() {
	if s == nil {
		return
	}
	s.Lock()
	s.RuntimeCounters.BEIntentSkippedAlreadyBetter++
	s.Unlock()
}

// RecordLastWinMove stores the latest profitable move fraction used by SL policy.
func (s *State) RecordLastWinMove(side string, movePct, netPnL, entry, exit float64, source string, ts time.Time) {
	if s == nil {
		return
	}
	if movePct <= 0 {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	normSide := strings.ToUpper(strings.TrimSpace(side))
	s.Lock()
	s.LastWinMovePct = movePct
	switch normSide {
	case "LONG":
		s.LastWinMovePctLong = movePct
	case "SHORT":
		s.LastWinMovePctShort = movePct
	}
	s.LastWinMoveSide = normSide
	s.LastWinMoveSource = strings.TrimSpace(source)
	s.LastWinMoveNetPnL = netPnL
	s.LastWinMoveEntry = entry
	s.LastWinMoveExit = exit
	s.LastWinMoveUpdated = ts
	s.Unlock()
}

// LastWinMovePctForSide returns side-specific last winning move when available, then falls back to global.
func (s *State) LastWinMovePctForSide(side string) float64 {
	if s == nil {
		return 0
	}
	normSide := strings.ToUpper(strings.TrimSpace(side))
	s.RLock()
	defer s.RUnlock()
	switch normSide {
	case "LONG":
		if s.LastWinMovePctLong > 0 {
			return s.LastWinMovePctLong
		}
	case "SHORT":
		if s.LastWinMovePctShort > 0 {
			return s.LastWinMovePctShort
		}
	}
	return s.LastWinMovePct
}

func (s *State) RecordBackfillCycle(fetched, processed, deduped, gaps uint64, ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeCounters.BackfillFetched += fetched
	s.RuntimeCounters.BackfillProcessed += processed
	s.RuntimeCounters.BackfillDeduped += deduped
	s.RuntimeCounters.BackfillGaps += gaps
	s.RuntimeHealth.LastBackfillCycleTS = ts
	s.RuntimeHealth.LastBackfillError = ""
	s.RuntimeHealth.LastBackfillErrorTS = time.Time{}
	s.Unlock()
}

func (s *State) RecordBackfillError(errMsg string, ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeCounters.BackfillErrors++
	s.RuntimeHealth.LastBackfillCycleTS = ts
	s.RuntimeHealth.LastBackfillError = errMsg
	s.RuntimeHealth.LastBackfillErrorTS = ts
	s.Unlock()
}

func (s *State) RecordBackfillHeartbeat(ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeHealth.LastBackfillCycleTS = ts
	s.Unlock()
}

func (s *State) RecordWSExecution(ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeHealth.LastWSExecutionTS = ts
	s.Unlock()
}

func (s *State) RecordDryRunTick(ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeCounters.DryRunTicks++
	s.RuntimeHealth.LastDryRunTickTS = ts
	s.Unlock()
}

func (s *State) RecordExecutionQuality(hasMaker bool, isMaker bool, execFeeAbs, grossRealisedDelta, netRealisedDelta float64) {
	if s == nil {
		return
	}
	s.Lock()
	if hasMaker {
		if isMaker {
			s.RuntimeCounters.MakerFills++
		} else {
			s.RuntimeCounters.TakerFills++
		}
	}
	knownFills := s.RuntimeCounters.MakerFills + s.RuntimeCounters.TakerFills
	if knownFills > 0 {
		s.RuntimeCounters.MakerRatio = float64(s.RuntimeCounters.MakerFills) / float64(knownFills)
	} else {
		s.RuntimeCounters.MakerRatio = 0
	}
	if execFeeAbs < 0 {
		execFeeAbs = -execFeeAbs
	}
	s.RuntimeCounters.TotalExecFee += execFeeAbs
	s.RuntimeCounters.TotalGrossRealised += grossRealisedDelta
	s.RuntimeCounters.TotalNetRealised += netRealisedDelta
	denom := s.RuntimeCounters.TotalGrossRealised
	if denom < 0 {
		denom = -denom
	}
	if denom > 1e-9 {
		s.RuntimeCounters.FeeToGrossRatio = s.RuntimeCounters.TotalExecFee / denom
	} else {
		s.RuntimeCounters.FeeToGrossRatio = 0
	}
	s.Unlock()
}

func (s *State) RecordTradeOutcome(durationSec, netAfterFee float64) {
	if s == nil {
		return
	}
	if durationSec < 0 {
		durationSec = 0
	}
	s.Lock()
	s.tradeCount++
	s.tradeDurationSum += durationSec
	if s.tradeCount > 0 {
		s.RuntimeCounters.AvgTradeDuration = s.tradeDurationSum / float64(s.tradeCount)
	}

	if netAfterFee >= 0 {
		s.winTradeCount++
		s.winDurationSum += durationSec
		if s.winTradeCount > 0 {
			s.RuntimeCounters.AvgWinDuration = s.winDurationSum / float64(s.winTradeCount)
		}
	} else {
		s.lossTradeCount++
		s.lossDurationSum += durationSec
		if s.lossTradeCount > 0 {
			s.RuntimeCounters.AvgLossDuration = s.lossDurationSum / float64(s.lossTradeCount)
		}
	}

	s.netAfterFeeSum += netAfterFee
	if s.tradeCount > 0 {
		s.RuntimeCounters.NetAfterFeePerTrade = s.netAfterFeeSum / float64(s.tradeCount)
		s.RuntimeCounters.AvgNetPerTrade = s.RuntimeCounters.NetAfterFeePerTrade
	}
	s.Unlock()
}

func (s *State) RecordMakerTimeout() {
	if s == nil {
		return
	}
	s.Lock()
	s.RuntimeCounters.MakerTimeoutCount++
	s.Unlock()
}

func (s *State) RecordMakerFallback() {
	if s == nil {
		return
	}
	s.Lock()
	s.RuntimeCounters.MakerFallbackCount++
	s.Unlock()
}

func (s *State) RecordSLPolicyDecision(policy, reason string) {
	if s == nil {
		return
	}
	s.Lock()
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "half_last_win":
		s.RuntimeCounters.SLPolicyHalfLastWinCount++
	default:
		switch strings.ToLower(strings.TrimSpace(reason)) {
		case "small_last_win":
			s.RuntimeCounters.SLPolicyFallbackSmallWinCount++
		case "clamp_violation":
			s.RuntimeCounters.SLPolicyClampViolationCount++
		}
	}
	s.Unlock()
}

func (s *State) RecordStatusServerStarted(ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeHealth.StatusServerStarted = ts
	s.RuntimeHealth.StatusServerError = ""
	s.Unlock()
}

func (s *State) RecordStatusServerError(errMsg string, ts time.Time) {
	if s == nil {
		return
	}
	if ts.IsZero() {
		ts = time.Now()
	}
	s.Lock()
	s.RuntimeHealth.StatusServerError = errMsg
	if s.RuntimeHealth.StatusServerStarted.IsZero() {
		s.RuntimeHealth.StatusServerStarted = ts
	}
	s.Unlock()
}

func (s *State) RuntimeSnapshot() (RuntimeCounters, RuntimeHealth) {
	if s == nil {
		return RuntimeCounters{}, RuntimeHealth{}
	}
	s.RLock()
	defer s.RUnlock()
	return s.RuntimeCounters, s.RuntimeHealth
}

func (s *State) SetRuntimeFeatures(features RuntimeFeatures) {
	if s == nil {
		return
	}
	s.Lock()
	s.RuntimeFeatures = features
	s.Unlock()
}

func (s *State) RuntimeFeaturesSnapshot() RuntimeFeatures {
	if s == nil {
		return RuntimeFeatures{}
	}
	s.RLock()
	defer s.RUnlock()
	return s.RuntimeFeatures
}
