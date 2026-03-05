package main

import (
	"time"

	sharedforensics "verbose-fortnight/internal/forensics"
)

const (
	eventTradeCloseSummary   = sharedforensics.EventTradeCloseSummary
	eventExecutionFill       = sharedforensics.EventExecutionFill
	eventExecutionListResp   = sharedforensics.EventExecutionListResp
	eventTradeEvent          = sharedforensics.EventTradeEvent
	eventPositionListResp    = sharedforensics.EventPositionListResp
	eventPositionWSUpdate    = sharedforensics.EventPositionWSUpdate
	eventPositionDeltaOnly   = "position_delta_only"
	defaultMatchWindow       = 15 * time.Minute
	fallbackMatchWindow      = 60 * time.Minute
	defaultEventScanPadding  = 2 * time.Hour
	defaultPositionAttachGap = 20 * time.Minute
)

type Event = sharedforensics.Event

type TradeCluster struct {
	Key         string    `json:"key"`
	TraceKey    string    `json:"traceKey,omitempty"`
	LifecycleID string    `json:"lifecycleId,omitempty"`
	TradeID     string    `json:"tradeId,omitempty"`
	Symbol      string    `json:"symbol,omitempty"`
	StartTS     time.Time `json:"startTs"`
	EndTS       time.Time `json:"endTs"`

	OrderIDs     []string `json:"orderIds,omitempty"`
	OrderLinkIDs []string `json:"orderLinkIds,omitempty"`
	ExecIDs      []string `json:"execIds,omitempty"`

	Events         []Event `json:"events,omitempty"`
	ExecutionFills []Event `json:"executionFills,omitempty"`
	PositionEvents []Event `json:"positionEvents,omitempty"`

	HasSummary  bool   `json:"hasSummary"`
	CloseReason string `json:"closeReason,omitempty"`
	NetSource   string `json:"netSource,omitempty"`

	PositionSide    string   `json:"positionSide,omitempty"`
	QtyOpened       float64  `json:"qtyOpened,omitempty"`
	QtyClosedTotal  float64  `json:"qtyClosedTotal,omitempty"`
	EntryVWAP       float64  `json:"entryVWAP,omitempty"`
	ExitVWAP        float64  `json:"exitVWAP,omitempty"`
	GrossCalc       float64  `json:"grossCalc,omitempty"`
	GrossSource     string   `json:"grossSource,omitempty"`
	FeeOpenAlloc    float64  `json:"feeOpenAlloc,omitempty"`
	FeeClose        float64  `json:"feeClose,omitempty"`
	FeeTotal        float64  `json:"feeTotal,omitempty"`
	FundingSigned   float64  `json:"fundingSigned,omitempty"`
	RealisedDelta   float64  `json:"realisedDeltaCum,omitempty"`
	NetCalc         float64  `json:"netCalc,omitempty"`
	NetExchange     *float64 `json:"netExchange,omitempty"`
	MatchedBy       string   `json:"matchedBy,omitempty"`
	ClassifierLabel string   `json:"classifierLabel,omitempty"`
}

type ReportTrade struct {
	Index int `json:"index"`

	Time time.Time `json:"time"`

	Symbol       string `json:"symbol"`
	CloseSide    string `json:"closeSide,omitempty"`
	PositionSide string `json:"positionSide,omitempty"`

	Qty           float64 `json:"qty"`
	Entry         float64 `json:"entry"`
	Exit          float64 `json:"exit"`
	Gross         float64 `json:"gross"`
	Fee           float64 `json:"fee"`
	FundingSigned float64 `json:"fundingSigned"`
	Net           float64 `json:"net"`
}

type MatchResult struct {
	Report ReportTrade `json:"report"`

	Matched bool `json:"matched"`

	ClusterKey  string `json:"clusterKey,omitempty"`
	TraceKey    string `json:"traceKey,omitempty"`
	LifecycleID string `json:"lifecycleId,omitempty"`
	TradeID     string `json:"tradeId,omitempty"`

	MatchedStart time.Time `json:"matchedStart,omitempty"`
	MatchedEnd   time.Time `json:"matchedEnd,omitempty"`

	PositionSide string `json:"positionSide,omitempty"`
	CloseReason  string `json:"closeReason,omitempty"`

	QtyLog   float64 `json:"qtyLog,omitempty"`
	EntryLog float64 `json:"entryLog,omitempty"`
	ExitLog  float64 `json:"exitLog,omitempty"`

	FeeOpenAlloc  float64  `json:"feeOpenAlloc,omitempty"`
	FeeClose      float64  `json:"feeClose,omitempty"`
	FeeTotal      float64  `json:"feeTotal,omitempty"`
	FundingSigned float64  `json:"fundingSigned,omitempty"`
	GrossCalc     float64  `json:"grossCalc,omitempty"`
	GrossSource   string   `json:"grossSource,omitempty"`
	NetCalc       float64  `json:"netCalc,omitempty"`
	NetExchange   *float64 `json:"netExchange,omitempty"`
	NetSource     string   `json:"netSource,omitempty"`

	Confidence float64 `json:"confidence"`
	Tier       string  `json:"tier,omitempty"`
	Reason     string  `json:"reason,omitempty"`
}

type ForensicsArtifacts struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Symbol      string         `json:"symbol"`
	From        time.Time      `json:"from"`
	To          time.Time      `json:"to"`
	Clusters    []TradeCluster `json:"clusters"`
	Matches     []MatchResult  `json:"matches"`
}
