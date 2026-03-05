package forensics

import "time"

const (
	EventTradeCloseSummary = "trade_close_summary"
	EventExecutionFill     = "execution_fill"
	EventExecutionListResp = "execution_list_response"
	EventTradeEvent        = "trade_event"
	EventPositionListResp  = "position_list_response"
	EventPositionWSUpdate  = "position_ws_update"
)

const (
	defaultEventScanPadding = 2 * time.Hour
	positionAttachGap       = 20 * time.Minute
)

// Event is a normalized forensic event parsed from runtime logs.
type Event struct {
	TS      time.Time `json:"ts"`
	EpochMS int64     `json:"tsEpochMs"`

	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Type string `json:"eventType"`
	Raw  string `json:"raw,omitempty"`

	Symbol      string `json:"symbol,omitempty"`
	TraceKey    string `json:"traceKey,omitempty"`
	LifecycleID string `json:"lifecycleId,omitempty"`
	TradeID     string `json:"tradeId,omitempty"`
	OrderID     string `json:"orderId,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
	ExecID      string `json:"execId,omitempty"`

	Side         string  `json:"side,omitempty"`
	PositionSide string  `json:"positionSide,omitempty"`
	Qty          float64 `json:"qty,omitempty"`
	Price        float64 `json:"price,omitempty"`
	ClosedSize   float64 `json:"closedSize,omitempty"`
	ReduceOnly   bool    `json:"reduceOnly,omitempty"`
	IsMaker      *bool   `json:"isMaker,omitempty"`

	ExecFee  float64 `json:"execFee,omitempty"`
	ExecPnl  float64 `json:"execPnl,omitempty"`
	ExecTime int64   `json:"execTime,omitempty"`

	CreateType    string `json:"createType,omitempty"`
	StopOrderType string `json:"stopOrderType,omitempty"`
	CloseReason   string `json:"closeReason,omitempty"`

	GrossCalc      float64  `json:"grossCalc,omitempty"`
	GrossSource    string   `json:"grossSource,omitempty"`
	FeeOpenAlloc   float64  `json:"feeOpenAlloc,omitempty"`
	FeeClose       float64  `json:"feeClose,omitempty"`
	FeeTotal       float64  `json:"feeTotal,omitempty"`
	FundingSigned  float64  `json:"fundingSigned,omitempty"`
	RealisedDelta  float64  `json:"realisedDeltaCum,omitempty"`
	NetCalc        float64  `json:"netCalc,omitempty"`
	NetExchange    *float64 `json:"netExchange,omitempty"`
	NetSource      string   `json:"netSource,omitempty"`
	EntryVWAP      float64  `json:"entryVWAP,omitempty"`
	ExitVWAP       float64  `json:"exitVWAP,omitempty"`
	QtyOpened      float64  `json:"qtyOpened,omitempty"`
	QtyClosedTotal float64  `json:"qtyClosedTotal,omitempty"`
	LegsCount      int      `json:"legsCount,omitempty"`
	DurationSec    float64  `json:"durationSec,omitempty"`
	ListLen        int      `json:"listLen,omitempty"`
	FirstExecID    string   `json:"firstExecId,omitempty"`
	LastExecID     string   `json:"lastExecId,omitempty"`
	NextCursor     string   `json:"nextCursor,omitempty"`

	CurRealisedPnl  float64 `json:"curRealisedPnl,omitempty"`
	CumRealisedPnl  float64 `json:"cumRealisedPnl,omitempty"`
	PositionSize    float64 `json:"positionSize,omitempty"`
	PositionAvgPx   float64 `json:"positionAvgPrice,omitempty"`
	TradeEventName  string  `json:"tradeEvent,omitempty"`
	ConfidenceHint  float64 `json:"confidenceHint,omitempty"`
	ClassifierLabel string  `json:"classifierLabel,omitempty"`
}

type LoadStats struct {
	FilesTotal          uint64
	FilesRead           uint64
	FilesSkippedMissing uint64
	FilesSkippedOther   uint64
	LinesScanned        uint64
}

type ReconstructedTrade struct {
	TradeID string `json:"trade_id"`

	Tier       string  `json:"tier"`
	Confidence float64 `json:"confidence"`

	OpenTS      time.Time `json:"open_ts"`
	CloseTS     time.Time `json:"close_ts"`
	DurationSec float64   `json:"duration_sec"`

	Side      string  `json:"side"`
	QtyTotal  float64 `json:"qty_total"`
	Realised  float64 `json:"realised_delta_cum"`
	FeeClose  float64 `json:"fee_close_sum"`
	GrossCalc float64 `json:"gross_calc"`
	NetCalc   float64 `json:"net_calc"`

	MakerFills   int `json:"maker_fills"`
	TakerFills   int `json:"taker_fills"`
	UnknownFills int `json:"unknown_fills"`

	TraceKey    string `json:"trace_key,omitempty"`
	LifecycleID string `json:"lifecycle_id,omitempty"`
	OrderID     string `json:"order_id,omitempty"`
	TradeRefID  string `json:"trade_id_ref,omitempty"`
}

type ReconstructStats struct {
	Tier1             int
	Tier2             int
	Tier3             int
	ConfidenceAvg     float64
	MissingFeeTrades  int
	MissingSideTrades int
}
