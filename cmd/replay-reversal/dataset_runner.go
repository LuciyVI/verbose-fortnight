package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/strategy"
)

const (
	acceptanceDoNotCalibrate          = "DO_NOT_CALIBRATE"
	acceptanceReadyOfflineCalibration = "READY_FOR_OFFLINE_CALIBRATION"
	acceptanceNeedMoreData            = "NEED_MORE_DATA"
	acceptanceRejectNegativeEdge      = "REJECT_NEGATIVE_EDGE"

	defaultMinValidationUniqueMarketCandidates = 10
	defaultMinValidationAvgNetR                = 0
	defaultMinValidationMedianNetR             = 0
	defaultMaxDuplicateCandidateRatio          = 0.75

	datasetSplitAll        = "all"
	datasetSplitTrain      = "train"
	datasetSplitValidation = "validation"

	datasetAggregationUnitExtractedEpisode = "extracted_episode"
)

type datasetRunnerOptions struct {
	ManifestPath                        string
	RunID                               string
	OutRoot                             string
	ReversalEnabled                     bool
	ReversalTradeEnabled                bool
	SetupMemoryBars                     int
	InvalidationATRMult                 float64
	SensitivitySpec                     strategy.ReversalSensitivityOptions
	MinEpisodes                         int
	MinOrderbookCoverage                float64
	MinCandidateEpisodes                int
	MinValidationUniqueMarketCandidates int
	MinValidationAvgNetR                float64
	MinValidationMedianNetR             float64
	MaxDuplicateCandidateRatio          float64
	TrainRatio                          float64
	ValidationStart                     time.Time
	RequireSoftOrderbookRuntime         *bool
}

type datasetManifestEpisode struct {
	Symbol    string
	Timeframe string
	InputCSV  string
	Start     time.Time
	End       time.Time
	Warmup    string
}

type datasetManifestEpisodeRaw struct {
	Symbol        string         `json:"symbol"`
	Timeframe     string         `json:"timeframe"`
	InputCSV      string         `json:"input_csv"`
	Start         string         `json:"start"`
	End           string         `json:"end"`
	Warmup        manifestScalar `json:"warmup"`
	WarmupCandles manifestScalar `json:"warmup_candles"`
}

type manifestScalar string

func (s *manifestScalar) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*s = ""
		return nil
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	switch v := value.(type) {
	case string:
		*s = manifestScalar(strings.TrimSpace(v))
	case json.Number:
		*s = manifestScalar(v.String())
	default:
		return fmt.Errorf("manifest scalar must be a string or number")
	}
	return nil
}

func (s manifestScalar) String() string {
	return strings.TrimSpace(string(s))
}

func (raw datasetManifestEpisodeRaw) warmupValue() string {
	if warmup := raw.Warmup.String(); warmup != "" {
		return warmup
	}
	return raw.WarmupCandles.String()
}

type datasetManifestObject struct {
	Episodes []datasetManifestEpisodeRaw `json:"episodes"`
}

type datasetRunOutput struct {
	RunID             string
	RunDir            string
	Summary           datasetSummary
	Acceptance        datasetAcceptanceResult
	ReportPath        string
	SummaryPath       string
	AcceptancePath    string
	EpisodesIndexPath string
}

type datasetEpisodeRecord struct {
	ManifestIndex int
	RunKey        string
	Split         string
	EpisodePath   string
	Manifest      datasetManifestEpisode
	Dataset       strategy.ReversalEpisodeDataset
}

type datasetSensitivityRun struct {
	RunKey        string
	SourceFile    string
	ManifestIndex int
	RecordIndex   int
	Split         string
	Manifest      datasetManifestEpisode
	Results       []strategy.ReversalSensitivityResult
	OrdersSent    int
}

type datasetSummary struct {
	RunID                               string                                        `json:"run_id"`
	GeneratedAt                         time.Time                                     `json:"generated_at"`
	TotalEpisodes                       int                                           `json:"total_episodes"`
	Symbols                             []string                                      `json:"symbols"`
	Timeframes                          []string                                      `json:"timeframes"`
	TrueReversalCandidateCount          int                                           `json:"true_reversal_candidate_count"`
	FalseReversalInvalidatedCount       int                                           `json:"false_reversal_invalidated_count"`
	NoSetupCount                        int                                           `json:"no_setup_count"`
	DataGapOrderbookUnavailableCount    int                                           `json:"data_gap_orderbook_unavailable_count"`
	AvgOrderbookCoveragePct             float64                                       `json:"avg_orderbook_coverage_pct"`
	CandidateReadyEpisodes              int                                           `json:"candidate_ready_episodes"`
	CandidateReadyEvaluationEvents      int                                           `json:"candidate_ready_evaluation_events"`
	CandidateReadyPipelineDecisions     int                                           `json:"candidate_ready_pipeline_decisions"`
	CandidateReadyUniqueEventTimes      int                                           `json:"candidate_ready_unique_event_times"`
	CandidateReadyExtractedEpisodes     int                                           `json:"candidate_ready_extracted_episodes"`
	SetupDetectedEpisodes               int                                           `json:"setup_detected_episodes"`
	SetupInvalidatedEpisodes            int                                           `json:"setup_invalidated_episodes"`
	ExtractedEpisodesTotal              int                                           `json:"extracted_episodes_total"`
	SensitivityEpisodesTotal            int                                           `json:"sensitivity_episodes_total"`
	AggregationUnit                     string                                        `json:"aggregation_unit"`
	AggregationWarnings                 []string                                      `json:"aggregation_warnings,omitempty"`
	TopMainBlockers                     []datasetCount                                `json:"top_main_blockers"`
	TopInvalidationReasons              []datasetCount                                `json:"top_invalidation_reasons"`
	HighScoreRejectedThreshold          float64                                       `json:"high_score_rejected_threshold"`
	HighScoreRejectedCount              int                                           `json:"high_score_rejected_count"`
	HighScoreRejectedGroups             []datasetHighScoreRejectedGroup               `json:"high_score_rejected_groups"`
	HighScoreRejectedExamples           []datasetHighScoreRejectedExample             `json:"high_score_rejected_examples"`
	MaxScoreDistribution                map[string]int                                `json:"max_score_distribution"`
	CandidateRate                       float64                                       `json:"candidate_rate"`
	InvalidationRate                    float64                                       `json:"invalidation_rate"`
	OrdersSent                          int                                           `json:"orders_sent"`
	SafetyConstraintsPassed             bool                                          `json:"safety_constraints_passed"`
	SafetyViolations                    []string                                      `json:"safety_violations,omitempty"`
	Split                               datasetSplitSummary                           `json:"split"`
	SensitivityAggregation              map[string][]datasetSensitivityAggregate      `json:"sensitivity_aggregation"`
	CandidateOutcomeCount               int                                           `json:"candidate_outcome_count"`
	RawMarketCandidateCount             int                                           `json:"raw_market_candidate_count"`
	UniqueMarketCandidateCount          int                                           `json:"unique_market_candidate_count"`
	DuplicateCandidateCount             int                                           `json:"duplicate_candidate_count"`
	OfflineFilterRejectedCandidateCount int                                           `json:"offline_filter_rejected_candidate_count"`
	OfflineFilterRejectReasons          map[string][]datasetCount                     `json:"offline_filter_reject_reasons,omitempty"`
	CandidateOutcomeAggregation         map[string][]datasetCandidateOutcomeAggregate `json:"candidate_outcome_aggregation"`
	CandidateOutcomeExamples            []datasetCandidateOutcomeExample              `json:"candidate_outcome_examples"`
	StableSensitivityParameters         []datasetStableSensitivityParameters          `json:"stable_sensitivity_parameters"`
	AcceptanceRecommendation            string                                        `json:"acceptance_recommendation"`
}

type datasetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type datasetHighScoreRejectedGroup struct {
	RejectReason       string `json:"reject_reason"`
	InvalidationReason string `json:"invalidation_reason"`
	Count              int    `json:"count"`
}

type datasetHighScoreRejectedExample struct {
	SourceFile                 string  `json:"source_file"`
	RecordIndex                int     `json:"record_index"`
	Symbol                     string  `json:"symbol"`
	Timeframe                  string  `json:"timeframe"`
	EventTime                  string  `json:"event_time"`
	Score                      float64 `json:"score"`
	State                      string  `json:"state"`
	RejectReason               string  `json:"reject_reason"`
	SetupInvalidated           bool    `json:"setup_invalidated"`
	InvalidationReason         string  `json:"invalidation_reason"`
	SetupDetectedAt            string  `json:"setup_detected_at"`
	SetupAgeBars               int     `json:"setup_age_bars"`
	MemoryReclaimUsed          bool    `json:"memory_reclaim_used"`
	OrderbookRatio             float64 `json:"orderbook_ratio"`
	BidAskImbalance            float64 `json:"bid_ask_imbalance"`
	SpreadBps                  float64 `json:"spread_bps"`
	OrderbookHostile           bool    `json:"orderbook_hostile"`
	OrderbookRejectReason      string  `json:"orderbook_reject_reason"`
	OrderbookUnavailableReason string  `json:"orderbook_unavailable_reason"`
	HTFBias                    string  `json:"htf_bias"`
	HTFAllowed                 bool    `json:"htf_allowed"`
	HTFOverrideAllowed         bool    `json:"htf_override_allowed"`
}

type datasetSplitSummary struct {
	Method             string     `json:"method"`
	TrainEpisodes      int        `json:"train_episodes"`
	ValidationEpisodes int        `json:"validation_episodes"`
	TrainRatio         float64    `json:"train_ratio,omitempty"`
	ValidationStart    *time.Time `json:"validation_start,omitempty"`
}

type datasetSensitivityAggregate struct {
	MinRSIDelta                         float64 `json:"min_rsi_delta"`
	NoNewLowBars                        int     `json:"no_new_low_bars"`
	ScoreThreshold                      float64 `json:"score_threshold"`
	MinVolumeRatio                      float64 `json:"min_volume_ratio"`
	SetupMemoryBars                     int     `json:"setup_memory_bars"`
	InvalidationATRMult                 float64 `json:"invalidation_atr_mult"`
	RequireOrderbookNotHostile          bool    `json:"require_orderbook_not_hostile"`
	MinOrderbookRatio                   float64 `json:"min_orderbook_ratio"`
	MinBarsWithoutNewLowAfterReclaim    int     `json:"min_bars_without_new_low_after_reclaim"`
	RequireCloseAboveMiddleBB           bool    `json:"require_close_above_middle_bb"`
	RequireHigherLowAfterReclaim        bool    `json:"require_higher_low_after_reclaim"`
	TotalEpisodes                       int     `json:"total_episodes"`
	CandidateReadyEpisodes              int     `json:"candidate_ready_episodes"`
	UnfilteredCandidateReadyCount       int     `json:"unfiltered_candidate_ready_count"`
	FilteredCandidateReadyCount         int     `json:"filtered_candidate_ready_count"`
	OfflineFilterRejectedCandidateCount int     `json:"offline_filter_rejected_candidate_count"`
	SetupDetectedEpisodes               int     `json:"setup_detected_episodes"`
	SetupInvalidatedEpisodes            int     `json:"setup_invalidated_episodes"`
	AvgMaxScore                         float64 `json:"avg_max_score"`
	CandidateRate                       float64 `json:"candidate_rate"`
	InvalidationRate                    float64 `json:"invalidation_rate"`
	OrdersSent                          int     `json:"orders_sent"`
}

type datasetCandidateOutcomeAggregate struct {
	MinRSIDelta                       float64 `json:"min_rsi_delta"`
	NoNewLowBars                      int     `json:"no_new_low_bars"`
	ScoreThreshold                    float64 `json:"score_threshold"`
	MinVolumeRatio                    float64 `json:"min_volume_ratio"`
	SetupMemoryBars                   int     `json:"setup_memory_bars"`
	InvalidationATRMult               float64 `json:"invalidation_atr_mult"`
	RequireOrderbookNotHostile        bool    `json:"require_orderbook_not_hostile"`
	MinOrderbookRatio                 float64 `json:"min_orderbook_ratio"`
	MinBarsWithoutNewLowAfterReclaim  int     `json:"min_bars_without_new_low_after_reclaim"`
	RequireCloseAboveMiddleBB         bool    `json:"require_close_above_middle_bb"`
	RequireHigherLowAfterReclaim      bool    `json:"require_higher_low_after_reclaim"`
	TPBps                             float64 `json:"tp_bps"`
	SLBps                             float64 `json:"sl_bps"`
	FeeBps                            float64 `json:"fee_bps"`
	SlippageBps                       float64 `json:"slippage_bps"`
	RawCandidateCount                 int     `json:"raw_candidate_count"`
	RawWinningCandidateCount          int     `json:"raw_winning_candidate_count"`
	RawWinRate                        float64 `json:"raw_win_rate"`
	RawAvgNetR                        float64 `json:"raw_avg_net_r"`
	RawMedianNetR                     float64 `json:"raw_median_net_r"`
	UniqueMarketCandidateCount        int     `json:"unique_market_candidate_count"`
	UniqueMarketWinningCandidateCount int     `json:"unique_market_winning_candidate_count"`
	UniqueMarketWinRate               float64 `json:"unique_market_win_rate"`
	UniqueMarketAvgNetR               float64 `json:"unique_market_avg_net_r"`
	UniqueMarketMedianNetR            float64 `json:"unique_market_median_net_r"`
	UniqueMarketAvgMFEBps             float64 `json:"unique_market_avg_mfe_bps"`
	UniqueMarketAvgMAEBps             float64 `json:"unique_market_avg_mae_bps"`
	CandidateCount                    int     `json:"candidate_count"`
	WinningCandidateCount             int     `json:"winning_candidate_count"`
	WinRate                           float64 `json:"win_rate"`
	AvgNetR                           float64 `json:"avg_net_r"`
	MedianNetR                        float64 `json:"median_net_r"`
	AvgMFEBps                         float64 `json:"avg_mfe_bps"`
	AvgMAEBps                         float64 `json:"avg_mae_bps"`
}

type datasetCandidateOutcomeExample struct {
	RunKey                           string  `json:"run_key"`
	SourceFile                       string  `json:"source_file"`
	RecordIndex                      int     `json:"record_index"`
	Split                            string  `json:"split"`
	Symbol                           string  `json:"symbol"`
	Timeframe                        string  `json:"timeframe"`
	EventTime                        string  `json:"event_time"`
	UniqueCandidateKey               string  `json:"unique_candidate_key"`
	MinRSIDelta                      float64 `json:"min_rsi_delta"`
	NoNewLowBars                     int     `json:"no_new_low_bars"`
	ScoreThreshold                   float64 `json:"score_threshold"`
	MinVolumeRatio                   float64 `json:"min_volume_ratio"`
	SetupMemoryBars                  int     `json:"setup_memory_bars"`
	InvalidationATRMult              float64 `json:"invalidation_atr_mult"`
	RequireOrderbookNotHostile       bool    `json:"require_orderbook_not_hostile"`
	MinOrderbookRatio                float64 `json:"min_orderbook_ratio"`
	MinBarsWithoutNewLowAfterReclaim int     `json:"min_bars_without_new_low_after_reclaim"`
	RequireCloseAboveMiddleBB        bool    `json:"require_close_above_middle_bb"`
	RequireHigherLowAfterReclaim     bool    `json:"require_higher_low_after_reclaim"`
	TPBps                            float64 `json:"tp_bps"`
	SLBps                            float64 `json:"sl_bps"`
	ForwardReturn5mBps               float64 `json:"forward_return_5m_bps"`
	ForwardReturn15mBps              float64 `json:"forward_return_15m_bps"`
	ForwardReturn30mBps              float64 `json:"forward_return_30m_bps"`
	MaxFavorable30mBps               float64 `json:"max_favorable_30m_bps"`
	MaxAdverse30mBps                 float64 `json:"max_adverse_30m_bps"`
	TimeToMFE                        string  `json:"time_to_mfe"`
	TimeToMAE                        string  `json:"time_to_mae"`
	WouldHitTP                       bool    `json:"would_hit_tp"`
	WouldHitSL                       bool    `json:"would_hit_sl"`
	HitOrder                         string  `json:"hit_order"`
	NetReturnBps                     float64 `json:"net_return_bps"`
	NetR                             float64 `json:"net_r"`
}

type datasetStableSensitivityParameters struct {
	MinRSIDelta                      float64 `json:"min_rsi_delta"`
	NoNewLowBars                     int     `json:"no_new_low_bars"`
	ScoreThreshold                   float64 `json:"score_threshold"`
	MinVolumeRatio                   float64 `json:"min_volume_ratio"`
	SetupMemoryBars                  int     `json:"setup_memory_bars"`
	InvalidationATRMult              float64 `json:"invalidation_atr_mult"`
	RequireOrderbookNotHostile       bool    `json:"require_orderbook_not_hostile"`
	MinOrderbookRatio                float64 `json:"min_orderbook_ratio"`
	MinBarsWithoutNewLowAfterReclaim int     `json:"min_bars_without_new_low_after_reclaim"`
	RequireCloseAboveMiddleBB        bool    `json:"require_close_above_middle_bb"`
	RequireHigherLowAfterReclaim     bool    `json:"require_higher_low_after_reclaim"`
	TrainCandidateRate               float64 `json:"train_candidate_rate"`
	ValidationCandidateRate          float64 `json:"validation_candidate_rate"`
	TrainInvalidationRate            float64 `json:"train_invalidation_rate"`
	ValidationInvalidationRate       float64 `json:"validation_invalidation_rate"`
}

type datasetAcceptanceResult struct {
	MinEpisodesPass                        bool     `json:"min_episodes_pass"`
	OrderbookCoveragePass                  bool     `json:"orderbook_coverage_pass"`
	CandidateCountPass                     bool     `json:"candidate_count_pass"`
	SafetyConstraintsPass                  bool     `json:"safety_constraints_pass"`
	OutcomeEnabled                         bool     `json:"outcome_enabled"`
	ValidationUniqueMarketCandidatesPass   bool     `json:"validation_unique_market_candidates_pass"`
	ValidationAvgNetRPass                  bool     `json:"validation_avg_net_r_pass"`
	ValidationMedianNetRPass               bool     `json:"validation_median_net_r_pass"`
	DuplicateCandidateRatioPass            bool     `json:"duplicate_candidate_ratio_pass"`
	TrainValidationAvgNetRSignPass         bool     `json:"train_validation_avg_net_r_sign_pass"`
	Recommendation                         string   `json:"recommendation"`
	MinEpisodes                            int      `json:"min_episodes"`
	ActualEpisodes                         int      `json:"actual_episodes"`
	MinOrderbookCoverage                   float64  `json:"min_orderbook_coverage"`
	ActualOrderbookCoverage                float64  `json:"actual_orderbook_coverage"`
	MinCandidateEpisodes                   int      `json:"min_candidate_episodes"`
	ActualCandidateEpisodes                int      `json:"actual_candidate_episodes"`
	ActualRawMarketCandidates              int      `json:"actual_raw_market_candidates"`
	ActualUniqueMarketCandidates           int      `json:"actual_unique_market_candidates"`
	DuplicateCandidateCount                int      `json:"duplicate_candidate_count"`
	DuplicateCandidateRatio                float64  `json:"duplicate_candidate_ratio"`
	CandidateCountUnit                     string   `json:"candidate_count_unit"`
	MinValidationUniqueMarketCandidates    int      `json:"min_validation_unique_market_candidates"`
	ActualValidationUniqueMarketCandidates int      `json:"actual_validation_unique_market_candidates"`
	MinValidationAvgNetR                   float64  `json:"min_validation_avg_net_r"`
	ActualValidationAvgNetR                float64  `json:"actual_validation_avg_net_r"`
	MinValidationMedianNetR                float64  `json:"min_validation_median_net_r"`
	ActualValidationMedianNetR             float64  `json:"actual_validation_median_net_r"`
	MaxDuplicateCandidateRatio             float64  `json:"max_duplicate_candidate_ratio"`
	ActualTrainAvgNetR                     float64  `json:"actual_train_avg_net_r"`
	TrainValidationAvgNetROppositeSigns    bool     `json:"train_validation_avg_net_r_opposite_signs"`
	OutcomeParameterSet                    string   `json:"outcome_parameter_set,omitempty"`
	SafetyViolations                       []string `json:"safety_violations,omitempty"`
}

type datasetEpisodeIndexRow struct {
	SourceFile                 string  `json:"source_file"`
	RecordIndex                int     `json:"record_index"`
	Symbol                     string  `json:"symbol"`
	Timeframe                  string  `json:"timeframe"`
	EventTime                  string  `json:"event_time"`
	Event                      string  `json:"event"`
	CandidateReady             bool    `json:"candidate_ready"`
	State                      string  `json:"state"`
	RejectReason               string  `json:"reject_reason"`
	SetupDetected              bool    `json:"setup_detected"`
	SetupInvalidated           bool    `json:"setup_invalidated"`
	InvalidationReason         string  `json:"invalidation_reason"`
	SetupDetectedAt            string  `json:"setup_detected_at"`
	SetupAgeBars               int     `json:"setup_age_bars"`
	MemoryReclaimUsed          bool    `json:"memory_reclaim_used"`
	RSI                        float64 `json:"rsi"`
	RSIDelta                   float64 `json:"rsi_delta"`
	RSIDeltaAvailable          bool    `json:"rsi_delta_available"`
	BarsWithoutNewLow          int     `json:"bars_without_new_low"`
	NoNewLowBars               int     `json:"no_new_low_bars"`
	VolumeRatio                float64 `json:"volume_ratio"`
	MinVolumeRatio             float64 `json:"min_volume_ratio"`
	VolumeRatioAvailable       bool    `json:"volume_ratio_available"`
	VolumeSpikePassed          bool    `json:"volume_spike_passed"`
	Score                      float64 `json:"score"`
	ScoreBBReclaim             float64 `json:"score_bb_reclaim"`
	ScoreMACDRecovery          float64 `json:"score_macd_recovery"`
	ScoreNoNewLow              float64 `json:"score_no_new_low"`
	ScoreRSIOversold           float64 `json:"score_rsi_oversold"`
	ScoreRSIRecovery           float64 `json:"score_rsi_recovery"`
	ScoreVolumeSpike           float64 `json:"score_volume_spike"`
	OrderbookAvailable         bool    `json:"orderbook_available"`
	BestBid                    float64 `json:"best_bid"`
	BestAsk                    float64 `json:"best_ask"`
	BestBidSize                float64 `json:"best_bid_size"`
	BestAskSize                float64 `json:"best_ask_size"`
	OrderbookRatio             float64 `json:"orderbook_ratio"`
	BidAskImbalance            float64 `json:"bid_ask_imbalance"`
	SpreadBps                  float64 `json:"spread_bps"`
	OrderbookHostile           bool    `json:"orderbook_hostile"`
	OrderbookRejectReason      string  `json:"orderbook_reject_reason"`
	OrderbookUnavailableReason string  `json:"orderbook_unavailable_reason"`
	HTFBias                    string  `json:"htf_bias"`
	HTFAllowed                 bool    `json:"htf_allowed"`
	HTFOverrideAllowed         bool    `json:"htf_override_allowed"`
	PipelineAllowed            bool    `json:"pipeline_allowed"`
	PipelineSkipReason         string  `json:"pipeline_skip_reason"`
	OrderSent                  bool    `json:"order_sent"`
}

type sensitivityKey struct {
	MinRSIDelta                      float64
	NoNewLowBars                     int
	ScoreThreshold                   float64
	MinVolumeRatio                   float64
	SetupMemoryBars                  int
	InvalidationATRMult              float64
	RequireOrderbookNotHostile       bool
	MinOrderbookRatio                float64
	MinBarsWithoutNewLowAfterReclaim int
	RequireCloseAboveMiddleBB        bool
	RequireHigherLowAfterReclaim     bool
}

type sensitivityAccumulator struct {
	key                                 sensitivityKey
	total                               int
	candidateReadyEpisodes              int
	unfilteredCandidateReadyCount       int
	filteredCandidateReadyCount         int
	offlineFilterRejectedCandidateCount int
	setupDetectedEpisodes               int
	setupInvalidatedEpisodes            int
	maxScoreSum                         float64
	ordersSent                          int
}

type candidateOutcomeKey struct {
	sensitivityKey
	TPBps       float64
	SLBps       float64
	FeeBps      float64
	SlippageBps float64
}

type candidateMarketKey struct {
	sensitivityKey
	Symbol    string
	EventTime string
}

type candidateMarketOccurrenceKey struct {
	candidateMarketKey
	SourceFile  string
	RecordIndex int
}

type candidateOutcomeCanonical struct {
	sourceFile  string
	recordIndex int
	outcome     strategy.ReversalCandidateOutcome
}

type candidateOutcomeStats struct {
	count   int
	wins    int
	netRs   []float64
	netRSum float64
	mfeSum  float64
	maeSum  float64
}

type candidateOutcomeAccumulator struct {
	key       candidateOutcomeKey
	raw       candidateOutcomeStats
	canonical map[candidateMarketKey]candidateOutcomeCanonical
}

type datasetAcceptanceOptions struct {
	MinEpisodes                         int
	MinOrderbookCoverage                float64
	MinCandidateEpisodes                int
	MinValidationUniqueMarketCandidates int
	MinValidationAvgNetR                float64
	MinValidationMedianNetR             float64
	MaxDuplicateCandidateRatio          float64
}

type validationOutcomeRankingOptions struct {
	Best                                bool
	EligibleOnly                        bool
	WarnLowValidationUniqueCandidates   bool
	MinValidationUniqueMarketCandidates int
	MaxDuplicateCandidateRatio          float64
	SafetyConstraintsPass               bool
}

func runDatasetRunner(opts datasetRunnerOptions) (datasetRunOutput, error) {
	if opts.ManifestPath == "" {
		return datasetRunOutput{}, fmt.Errorf("--dataset-manifest is required")
	}
	if opts.OutRoot == "" {
		opts.OutRoot = filepath.Join("artifacts", "replay", "dataset")
	}
	if opts.RunID == "" {
		opts.RunID = time.Now().UTC().Format("20060102T150405Z")
	}
	if opts.MinEpisodes <= 0 {
		opts.MinEpisodes = 50
	}
	if opts.MinOrderbookCoverage <= 0 {
		opts.MinOrderbookCoverage = 0.80
	}
	if opts.MinCandidateEpisodes <= 0 {
		opts.MinCandidateEpisodes = 10
	}
	if opts.MinValidationUniqueMarketCandidates <= 0 {
		opts.MinValidationUniqueMarketCandidates = defaultMinValidationUniqueMarketCandidates
	}
	if opts.MaxDuplicateCandidateRatio <= 0 {
		opts.MaxDuplicateCandidateRatio = defaultMaxDuplicateCandidateRatio
	}
	if opts.TrainRatio <= 0 || opts.TrainRatio >= 1 {
		opts.TrainRatio = 0.70
	}

	manifest, err := readDatasetManifest(opts.ManifestPath)
	if err != nil {
		return datasetRunOutput{}, err
	}
	runDir := filepath.Join(opts.OutRoot, sanitizePathPart(opts.RunID))
	if err := os.MkdirAll(filepath.Join(runDir, "episodes"), 0o755); err != nil {
		return datasetRunOutput{}, err
	}
	if err := os.MkdirAll(filepath.Join(runDir, "blockers"), 0o755); err != nil {
		return datasetRunOutput{}, err
	}
	if err := os.MkdirAll(filepath.Join(runDir, "sensitivity"), 0o755); err != nil {
		return datasetRunOutput{}, err
	}

	cfg := config.LoadConfig()
	cfg.ReversalLong.Enabled = opts.ReversalEnabled
	cfg.ReversalLong.TradeEnabled = opts.ReversalTradeEnabled
	setupOpts := strategy.ReversalSetupMemoryOptions{
		SetupMemoryBars:     opts.SetupMemoryBars,
		InvalidationATRMult: opts.InvalidationATRMult,
	}

	var records []datasetEpisodeRecord
	var sensitivityRuns []datasetSensitivityRun
	var safetyViolations []string
	totalOrdersSent := 0
	for i, item := range manifest {
		runKey := datasetRunKey(i, item)
		sourceCandles, err := strategy.LoadReversalReplayCandlesCSV(item.InputCSV)
		if err != nil {
			return datasetRunOutput{}, fmt.Errorf("manifest episode %d load input_csv: %w", i+1, err)
		}
		warmupDuration, err := parseWarmupDuration(item.Warmup, item.Timeframe)
		if err != nil {
			return datasetRunOutput{}, fmt.Errorf("manifest episode %d warmup: %w", i+1, err)
		}
		replayCandles := filterDatasetCandles(sourceCandles, item.Start, item.End, warmupDuration)
		replayOpts := strategy.ReversalReplayOptions{
			Symbol:                      item.Symbol,
			From:                        item.Start,
			To:                          item.End,
			ReversalEnabled:             opts.ReversalEnabled,
			ReversalTradeEnabled:        opts.ReversalTradeEnabled,
			OrdersEnabled:               false,
			Config:                      cfg,
			RequireSoftOrderbookRuntime: opts.RequireSoftOrderbookRuntime,
		}
		result, err := strategy.RunReversalReplayCandles(replayCandles, replayOpts)
		if err != nil {
			return datasetRunOutput{}, fmt.Errorf("manifest episode %d replay: %w", i+1, err)
		}
		if diagnostic := datasetWarmupReadinessDiagnostic(item, warmupDuration, result); diagnostic != "" {
			return datasetRunOutput{}, fmt.Errorf("manifest episode %d warmup: %s", i+1, diagnostic)
		}
		totalOrdersSent += result.Summary.OrdersSent
		safetyViolations = append(safetyViolations, collectReplaySafetyViolations(runKey, result)...)

		blockerPath := filepath.Join(runDir, "blockers", runKey+".md")
		if err := writeBlockerReport(blockerPath, result, cfg.ReversalLong, setupOpts); err != nil {
			return datasetRunOutput{}, fmt.Errorf("manifest episode %d blocker report: %w", i+1, err)
		}

		datasets := strategy.BuildReversalEpisodeDataset(result, cfg.ReversalLong, setupOpts)
		for j := range datasets {
			datasets[j].Summary.Symbol = item.Symbol
			datasets[j].Summary.Timeframe = item.Timeframe
			if datasets[j].Summary.Start.IsZero() {
				datasets[j].Summary.Start = item.Start
			}
			if datasets[j].Summary.End.IsZero() {
				datasets[j].Summary.End = item.End
			}
			episodeKey := fmt.Sprintf("%s_%s", runKey, sanitizePathPart(datasets[j].Summary.EpisodeID))
			episodePath := filepath.Join(runDir, "episodes", episodeKey+".json")
			if err := writeEpisodeDataset(episodePath, []strategy.ReversalEpisodeDataset{datasets[j]}); err != nil {
				return datasetRunOutput{}, fmt.Errorf("manifest episode %d episode output: %w", i+1, err)
			}
			recordIndex := len(records)
			records = append(records, datasetEpisodeRecord{
				ManifestIndex: i,
				RunKey:        episodeKey,
				EpisodePath:   episodePath,
				Manifest:      item,
				Dataset:       datasets[j],
			})

			episodeCandles := filterDatasetCandles(sourceCandles, datasets[j].Summary.Start, datasets[j].Summary.End.Add(strategy.ReversalCandidateOutcomeWindow), warmupDuration)
			episodeReplayOpts := replayOpts
			episodeReplayOpts.From = datasets[j].Summary.Start
			episodeReplayOpts.To = datasets[j].Summary.End
			sensitivityResults, err := strategy.RunReversalReplaySensitivity(episodeCandles, episodeReplayOpts, opts.SensitivitySpec)
			if err != nil {
				return datasetRunOutput{}, fmt.Errorf("manifest episode %d sensitivity: %w", i+1, err)
			}
			for _, result := range sensitivityResults {
				totalOrdersSent += result.OrdersSent
				if result.OrdersSent != 0 {
					safetyViolations = append(safetyViolations, fmt.Sprintf("%s sensitivity sent %d orders", episodeKey, result.OrdersSent))
				}
			}
			sensitivityPath := filepath.Join(runDir, "sensitivity", episodeKey+".jsonl")
			if err := writeDatasetSensitivityOutput(sensitivityPath, sensitivityResults); err != nil {
				return datasetRunOutput{}, fmt.Errorf("manifest episode %d sensitivity output: %w", i+1, err)
			}
			sensitivityRuns = append(sensitivityRuns, datasetSensitivityRun{
				RunKey:        episodeKey,
				SourceFile:    episodePath,
				ManifestIndex: i,
				RecordIndex:   recordIndex,
				Manifest:      item,
				Results:       sensitivityResults,
				OrdersSent:    sumSensitivityOrders(sensitivityResults),
			})
		}
	}

	assignDatasetSplits(records, sensitivityRuns, opts)
	summary := buildDatasetSummary(opts.RunID, records, sensitivityRuns, totalOrdersSent, safetyViolations, opts, cfg.ReversalLong.ScoreThreshold)
	acceptance := evaluateDatasetAcceptanceWithOptions(summary, acceptanceOptionsFromRunner(opts))
	summary.AcceptanceRecommendation = acceptance.Recommendation

	summaryPath := filepath.Join(runDir, "dataset_summary.json")
	if err := writeJSONFile(summaryPath, summary); err != nil {
		return datasetRunOutput{}, err
	}
	reportPath := filepath.Join(runDir, "dataset_report.md")
	if err := writeDatasetReport(reportPath, summary, acceptance); err != nil {
		return datasetRunOutput{}, err
	}
	acceptancePath := filepath.Join(runDir, "acceptance_report.md")
	if err := writeDatasetAcceptanceReport(acceptancePath, acceptance); err != nil {
		return datasetRunOutput{}, err
	}
	episodesIndexPath := filepath.Join(runDir, "episodes_index.jsonl")
	if err := writeDatasetEpisodesIndex(episodesIndexPath, records); err != nil {
		return datasetRunOutput{}, err
	}

	return datasetRunOutput{
		RunID:             opts.RunID,
		RunDir:            runDir,
		Summary:           summary,
		Acceptance:        acceptance,
		ReportPath:        reportPath,
		SummaryPath:       summaryPath,
		AcceptancePath:    acceptancePath,
		EpisodesIndexPath: episodesIndexPath,
	}, nil
}

func readDatasetManifest(path string) ([]datasetManifestEpisode, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ext := strings.ToLower(filepath.Ext(path))
	var episodes []datasetManifestEpisodeRaw
	switch {
	case ext == ".csv":
		episodes, err = parseDatasetManifestCSV(bytes.NewReader(raw))
	case ext == ".yaml" || ext == ".yml":
		episodes, err = parseDatasetManifestYAML(string(raw))
	default:
		episodes, err = parseDatasetManifestJSON(raw)
	}
	if err != nil {
		return nil, err
	}
	if len(episodes) == 0 {
		return nil, fmt.Errorf("manifest has no episodes")
	}
	baseDir := filepath.Dir(path)
	out := make([]datasetManifestEpisode, 0, len(episodes))
	for i, episode := range episodes {
		parsed, err := parseDatasetManifestEpisode(episode, baseDir)
		if err != nil {
			return nil, fmt.Errorf("episode %d: %w", i+1, err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseDatasetManifestJSON(raw []byte) ([]datasetManifestEpisodeRaw, error) {
	var list []datasetManifestEpisodeRaw
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		return list, nil
	}
	var object datasetManifestObject
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	return object.Episodes, nil
}

func parseDatasetManifestCSV(r io.Reader) ([]datasetManifestEpisodeRaw, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("csv manifest must include a header and at least one data row")
	}
	header := make(map[string]int, len(rows[0]))
	for i, col := range rows[0] {
		header[strings.ToLower(strings.TrimSpace(col))] = i
	}
	required := []string{"symbol", "timeframe", "input_csv", "start", "end"}
	for _, col := range required {
		if _, ok := header[col]; !ok {
			return nil, fmt.Errorf("csv manifest missing required column %q", col)
		}
	}
	out := make([]datasetManifestEpisodeRaw, 0, len(rows)-1)
	for _, row := range rows[1:] {
		out = append(out, datasetManifestEpisodeRaw{
			Symbol:        csvValue(row, header, "symbol"),
			Timeframe:     csvValue(row, header, "timeframe"),
			InputCSV:      csvValue(row, header, "input_csv"),
			Start:         csvValue(row, header, "start"),
			End:           csvValue(row, header, "end"),
			Warmup:        manifestScalar(csvValue(row, header, "warmup")),
			WarmupCandles: manifestScalar(csvValue(row, header, "warmup_candles")),
		})
	}
	return out, nil
}

func parseDatasetManifestYAML(raw string) ([]datasetManifestEpisodeRaw, error) {
	var out []datasetManifestEpisodeRaw
	var current map[string]string
	flush := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, datasetManifestEpisodeRaw{
			Symbol:        current["symbol"],
			Timeframe:     current["timeframe"],
			InputCSV:      current["input_csv"],
			Start:         current["start"],
			End:           current["end"],
			Warmup:        manifestScalar(current["warmup"]),
			WarmupCandles: manifestScalar(current["warmup_candles"]),
		})
		current = nil
	}
	for _, line := range strings.Split(raw, "\n") {
		line = stripYAMLComment(line)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || trimmed == "episodes:" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			flush()
			current = make(map[string]string)
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if trimmed == "" {
				continue
			}
		} else if current == nil {
			current = make(map[string]string)
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			return nil, fmt.Errorf("unsupported yaml line %q", trimmed)
		}
		current[strings.TrimSpace(key)] = trimManifestScalar(value)
	}
	flush()
	return out, nil
}

func parseDatasetManifestEpisode(raw datasetManifestEpisodeRaw, baseDir string) (datasetManifestEpisode, error) {
	symbol := strings.TrimSpace(raw.Symbol)
	if symbol == "" {
		return datasetManifestEpisode{}, fmt.Errorf("symbol is required")
	}
	timeframe := strings.TrimSpace(raw.Timeframe)
	if timeframe == "" {
		return datasetManifestEpisode{}, fmt.Errorf("timeframe is required")
	}
	input := strings.TrimSpace(raw.InputCSV)
	if input == "" {
		return datasetManifestEpisode{}, fmt.Errorf("input_csv is required")
	}
	if !filepath.IsAbs(input) {
		input = filepath.Join(baseDir, input)
	}
	start, err := parseOptionalTime(strings.TrimSpace(raw.Start))
	if err != nil || start.IsZero() {
		if err == nil {
			err = fmt.Errorf("start is required")
		}
		return datasetManifestEpisode{}, fmt.Errorf("start: %w", err)
	}
	end, err := parseOptionalTime(strings.TrimSpace(raw.End))
	if err != nil || end.IsZero() {
		if err == nil {
			err = fmt.Errorf("end is required")
		}
		return datasetManifestEpisode{}, fmt.Errorf("end: %w", err)
	}
	if end.Before(start) {
		return datasetManifestEpisode{}, fmt.Errorf("end before start")
	}
	return datasetManifestEpisode{
		Symbol:    symbol,
		Timeframe: timeframe,
		InputCSV:  input,
		Start:     start,
		End:       end,
		Warmup:    raw.warmupValue(),
	}, nil
}

func csvValue(row []string, header map[string]int, name string) string {
	idx, ok := header[name]
	if !ok {
		return ""
	}
	if idx >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[idx])
}

func stripYAMLComment(line string) string {
	inSingle := false
	inDouble := false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func trimManifestScalar(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, `"'`)
	return strings.TrimSpace(raw)
}

func parseWarmupDuration(raw, timeframe string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	cleaned := strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(raw, "bars"), "candles"))
	cleaned = strings.TrimSpace(cleaned)
	count, err := strconv.Atoi(cleaned)
	if err != nil {
		return 0, fmt.Errorf("warmup must be a duration or candle count: %w", err)
	}
	tf, err := parseTimeframeDuration(timeframe)
	if err != nil {
		return 0, err
	}
	return time.Duration(count) * tf, nil
}

func parseTimeframeDuration(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return 0, fmt.Errorf("timeframe is required for candle-count warmup")
	}
	if d, err := time.ParseDuration(raw); err == nil {
		return d, nil
	}
	switch raw {
	case "d", "1d":
		return 24 * time.Hour, nil
	case "w", "1w":
		return 7 * 24 * time.Hour, nil
	}
	minutes, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("unsupported timeframe %q", raw)
	}
	if minutes <= 0 {
		return 0, fmt.Errorf("timeframe must be positive")
	}
	return time.Duration(minutes) * time.Minute, nil
}

func filterDatasetCandles(candles []strategy.ReversalReplayCandle, start, end time.Time, warmup time.Duration) []strategy.ReversalReplayCandle {
	if start.IsZero() && end.IsZero() {
		return candles
	}
	from := start
	if warmup > 0 {
		from = start.Add(-warmup)
	}
	out := make([]strategy.ReversalReplayCandle, 0, len(candles))
	for _, candle := range candles {
		if !from.IsZero() && candle.Time.Before(from) {
			continue
		}
		if !end.IsZero() && candle.Time.After(end) {
			continue
		}
		out = append(out, candle)
	}
	return out
}

func writeDatasetSensitivityOutput(path string, results []strategy.ReversalSensitivityResult) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	if err := strategy.WriteReversalSensitivityJSONL(f, results); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeJSONFile(path string, value any) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeDatasetEpisodesIndex(path string, records []datasetEpisodeRecord) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, row := range buildDatasetEpisodeIndexRows(records) {
		if err := enc.Encode(row); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

func buildDatasetEpisodeIndexRows(records []datasetEpisodeRecord) []datasetEpisodeIndexRow {
	var rows []datasetEpisodeIndexRow
	for recordIndex, record := range records {
		sourceFile := record.EpisodePath
		if sourceFile == "" {
			sourceFile = record.RunKey
		}
		timelineByTime := datasetTimelineByEventTime(record.Dataset.Timeline)
		for _, event := range record.Dataset.Events {
			eventName := stringEventValue(event["event"])
			if eventName != "reversal_long_evaluation" && eventName != "reversal_long_pipeline_decision" {
				continue
			}
			ts := timeValueFromEvent(event["time"])
			if ts.IsZero() {
				ts = timeValueFromEvent(event["timestamp"])
			}
			eventTime := ""
			if !ts.IsZero() {
				eventTime = ts.UTC().Format(time.RFC3339Nano)
			}
			row := datasetEpisodeIndexRow{
				SourceFile:                 sourceFile,
				RecordIndex:                recordIndex,
				Symbol:                     record.Dataset.Summary.Symbol,
				Timeframe:                  record.Dataset.Summary.Timeframe,
				EventTime:                  eventTime,
				Event:                      eventName,
				CandidateReady:             boolEventValue(event["candidate_ready"]),
				State:                      stringEventValue(event["state"]),
				RejectReason:               stringEventValue(event["reject_reason"]),
				SetupDetected:              boolEventValue(event["setup_detected"]),
				SetupInvalidated:           boolEventValue(event["setup_invalidated"]),
				InvalidationReason:         stringEventValue(event["invalidation_reason"]),
				SetupDetectedAt:            stringEventValue(event["setup_detected_at"]),
				SetupAgeBars:               intEventValue(event["setup_age_bars"]),
				MemoryReclaimUsed:          boolEventValue(event["memory_reclaim_used"]),
				RSI:                        floatEventValue(event["rsi"]),
				RSIDelta:                   floatEventValue(event["rsi_delta"]),
				RSIDeltaAvailable:          boolEventValue(event["rsi_delta_available"]),
				BarsWithoutNewLow:          intEventValue(event["bars_without_new_low"]),
				NoNewLowBars:               intEventValue(event["no_new_low_bars"]),
				Score:                      floatEventValue(event["score"]),
				VolumeRatio:                floatEventValue(event["volume_ratio"]),
				MinVolumeRatio:             floatEventValue(event["min_volume_ratio"]),
				VolumeRatioAvailable:       boolEventValue(event["volume_ratio_available"]),
				VolumeSpikePassed:          boolEventValue(event["volume_spike_passed"]),
				ScoreBBReclaim:             floatEventValue(event["score_bb_reclaim"]),
				ScoreMACDRecovery:          floatEventValue(event["score_macd_recovery"]),
				ScoreNoNewLow:              floatEventValue(event["score_no_new_low"]),
				ScoreRSIOversold:           floatEventValue(event["score_rsi_oversold"]),
				ScoreRSIRecovery:           floatEventValue(event["score_rsi_recovery"]),
				ScoreVolumeSpike:           floatEventValue(event["score_volume_spike"]),
				OrderbookAvailable:         boolEventValue(event["orderbook_available"]),
				BestBid:                    floatEventValue(event["best_bid"]),
				BestAsk:                    floatEventValue(event["best_ask"]),
				BestBidSize:                floatEventValue(event["best_bid_size"]),
				BestAskSize:                floatEventValue(event["best_ask_size"]),
				OrderbookRatio:             floatEventValue(event["orderbook_ratio"]),
				BidAskImbalance:            floatEventValue(event["bid_ask_imbalance"]),
				SpreadBps:                  floatEventValue(event["spread_bps"]),
				OrderbookHostile:           boolEventValue(event["orderbook_hostile"]),
				OrderbookRejectReason:      stringEventValue(event["orderbook_reject_reason"]),
				OrderbookUnavailableReason: stringEventValue(event["orderbook_unavailable_reason"]),
				HTFBias:                    stringEventValue(event["htf_bias"]),
				HTFAllowed:                 boolEventValue(event["htf_allowed"]),
				HTFOverrideAllowed:         boolEventValue(event["htf_override_allowed"]),
				PipelineAllowed:            boolEventValue(event["pipeline_allowed"]),
				PipelineSkipReason:         stringEventValue(event["pipeline_skip_reason"]),
				OrderSent:                  boolEventValue(event["order_sent"]),
			}
			if row.BidAskImbalance == 0 {
				row.BidAskImbalance = floatEventValue(event["orderbook_imbalance"])
			}
			if row.Symbol == "" {
				row.Symbol = stringEventValue(event["symbol"])
			}
			if timelineRow, ok := timelineByTime[eventTimeKey(ts)]; ok {
				applyTimelineDiagnosticsToIndexRow(&row, timelineRow)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func datasetTimelineByEventTime(timeline []strategy.ReversalReplayTimelineRow) map[string]strategy.ReversalReplayTimelineRow {
	out := make(map[string]strategy.ReversalReplayTimelineRow, len(timeline))
	for _, row := range timeline {
		key := eventTimeKey(row.Time)
		if key == "" {
			continue
		}
		out[key] = row
	}
	return out
}

func applyTimelineDiagnosticsToIndexRow(row *datasetEpisodeIndexRow, timeline strategy.ReversalReplayTimelineRow) {
	if row == nil {
		return
	}
	if row.State == "" {
		row.State = timeline.State
	}
	if row.RejectReason == "" {
		row.RejectReason = timeline.RejectReason
	}
	if timeline.SetupDetectedAt != nil {
		row.SetupDetected = true
		row.SetupDetectedAt = timeline.SetupDetectedAt.UTC().Format(time.RFC3339Nano)
	}
	row.SetupInvalidated = timeline.SetupInvalidated
	row.InvalidationReason = timeline.InvalidationReason
	row.SetupAgeBars = timeline.SetupAgeBars
	row.MemoryReclaimUsed = timeline.MemoryReclaimUsed
}

func eventTimeKey(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func datasetRunKey(index int, episode datasetManifestEpisode) string {
	start := episode.Start.Format("20060102T150405Z")
	return sanitizePathPart(fmt.Sprintf("%04d_%s_%s_%s", index+1, episode.Symbol, episode.Timeframe, start))
}

func sanitizePathPart(raw string) string {
	if raw == "" {
		return "dataset"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func collectReplaySafetyViolations(runKey string, result strategy.ReversalReplayResult) []string {
	var violations []string
	if result.Summary.OrdersSent != 0 {
		violations = append(violations, fmt.Sprintf("%s replay sent %d orders", runKey, result.Summary.OrdersSent))
	}
	for i, event := range result.Events {
		if boolEventValue(event["order_sent"]) {
			violations = append(violations, fmt.Sprintf("%s event %d has order_sent=true", runKey, i))
		}
		if boolEventValue(event["orders_enabled"]) {
			violations = append(violations, fmt.Sprintf("%s event %d has orders_enabled=true", runKey, i))
		}
	}
	return violations
}

func boolEventValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func intEventValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case int32:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func stringEventValue(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func datasetWarmupReadinessDiagnostic(episode datasetManifestEpisode, warmup time.Duration, result strategy.ReversalReplayResult) string {
	if warmup > 0 || result.Summary.WarmupCandles > 0 || result.Summary.ReversalEvaluations == 0 {
		return ""
	}
	event, ok := firstReversalEvaluationEvent(result)
	if !ok {
		return ""
	}
	missing := datasetMissingWarmupIndicators(event)
	if len(missing) == 0 {
		return ""
	}
	rawWarmup := strings.TrimSpace(episode.Warmup)
	if rawWarmup == "" {
		rawWarmup = "missing"
	}
	ts := timeValueFromEvent(event["time"])
	timeText := ""
	if !ts.IsZero() {
		timeText = " at " + ts.Format(time.RFC3339)
	}
	return fmt.Sprintf("warmup is %s and indicators are not ready at replay start%s (%s); set manifest warmup to a duration or candle count before the episode start; warmup_candles is accepted as a backward-compatible alias", rawWarmup, timeText, strings.Join(missing, ", "))
}

func firstReversalEvaluationEvent(result strategy.ReversalReplayResult) (map[string]any, bool) {
	for _, event := range result.Events {
		if fmt.Sprint(event["event"]) == "reversal_long_evaluation" {
			return event, true
		}
	}
	return nil, false
}

func datasetMissingWarmupIndicators(event map[string]any) []string {
	var missing []string
	if floatEventValue(event["rsi"]) == 0 {
		missing = append(missing, "rsi=0")
	}
	if floatEventValue(event["atr"]) == 0 {
		missing = append(missing, "atr=0")
	}
	if bbPosition := strings.TrimSpace(fmt.Sprint(event["bb_position"])); bbPosition == "" || bbPosition == "not_available" {
		missing = append(missing, "bb_position=not_available")
	}
	if !boolEventValue(event["rsi_delta_available"]) {
		missing = append(missing, "rsi_delta_not_available")
	}
	if !boolEventValue(event["macd_hist_delta_available"]) {
		missing = append(missing, "macd_hist_delta_not_available")
	}
	return missing
}

func floatEventValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

func timeValueFromEvent(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		ts, _ := time.Parse(time.RFC3339, v)
		return ts.UTC()
	default:
		return time.Time{}
	}
}

func sumSensitivityOrders(results []strategy.ReversalSensitivityResult) int {
	total := 0
	for _, result := range results {
		total += result.OrdersSent
	}
	return total
}

func assignDatasetSplits(records []datasetEpisodeRecord, sensitivityRuns []datasetSensitivityRun, opts datasetRunnerOptions) {
	recordSplitByManifest := make(map[int]string)
	sortedIndexes := make([]int, len(records))
	for i := range records {
		sortedIndexes[i] = i
	}
	sort.Slice(sortedIndexes, func(i, j int) bool {
		left := records[sortedIndexes[i]].Dataset.Summary.Start
		right := records[sortedIndexes[j]].Dataset.Summary.Start
		if left.Equal(right) {
			return records[sortedIndexes[i]].RunKey < records[sortedIndexes[j]].RunKey
		}
		return left.Before(right)
	})
	if !opts.ValidationStart.IsZero() {
		for _, idx := range sortedIndexes {
			split := datasetSplitTrain
			if !records[idx].Dataset.Summary.Start.Before(opts.ValidationStart) {
				split = datasetSplitValidation
			}
			records[idx].Split = split
			if _, exists := recordSplitByManifest[records[idx].ManifestIndex]; !exists || split == datasetSplitValidation {
				recordSplitByManifest[records[idx].ManifestIndex] = split
			}
		}
	} else {
		trainCount := len(records)
		if len(records) > 1 {
			trainCount = int(math.Round(float64(len(records)) * opts.TrainRatio))
			if trainCount <= 0 {
				trainCount = 1
			}
			if trainCount >= len(records) {
				trainCount = len(records) - 1
			}
		}
		for position, idx := range sortedIndexes {
			split := datasetSplitTrain
			if position >= trainCount {
				split = datasetSplitValidation
			}
			records[idx].Split = split
			if _, exists := recordSplitByManifest[records[idx].ManifestIndex]; !exists || split == datasetSplitValidation {
				recordSplitByManifest[records[idx].ManifestIndex] = split
			}
		}
	}
	for i := range sensitivityRuns {
		recordIndex := sensitivityRuns[i].RecordIndex
		if recordIndex >= 0 && recordIndex < len(records) && sensitivityRuns[i].RunKey == records[recordIndex].RunKey {
			sensitivityRuns[i].Split = records[recordIndex].Split
		} else if split := recordSplitByManifest[sensitivityRuns[i].ManifestIndex]; split != "" {
			sensitivityRuns[i].Split = split
		} else {
			sensitivityRuns[i].Split = datasetSplitTrain
		}
	}
}

func buildDatasetSummary(runID string, records []datasetEpisodeRecord, sensitivityRuns []datasetSensitivityRun, ordersSent int, safetyViolations []string, opts datasetRunnerOptions, scoreThresholdOverride ...float64) datasetSummary {
	highScoreThreshold := config.DefaultReversalLongConfig().ScoreThreshold
	if len(scoreThresholdOverride) > 0 && scoreThresholdOverride[0] > 0 {
		highScoreThreshold = scoreThresholdOverride[0]
	}
	summary := datasetSummary{
		RunID:                       runID,
		GeneratedAt:                 time.Now().UTC(),
		HighScoreRejectedThreshold:  highScoreThreshold,
		MaxScoreDistribution:        map[string]int{"lt_0_50": 0, "0_50_to_0_69": 0, "0_70_to_0_79": 0, "gte_0_80": 0},
		OrdersSent:                  ordersSent,
		SafetyViolations:            safetyViolations,
		SafetyConstraintsPassed:     ordersSent == 0 && len(safetyViolations) == 0,
		AggregationUnit:             datasetAggregationUnitExtractedEpisode,
		SensitivityAggregation:      map[string][]datasetSensitivityAggregate{},
		OfflineFilterRejectReasons:  map[string][]datasetCount{},
		CandidateOutcomeAggregation: map[string][]datasetCandidateOutcomeAggregate{},
	}
	symbols := map[string]bool{}
	timeframes := map[string]bool{}
	mainBlockers := map[string]int{}
	invalidationReasons := map[string]int{}
	candidateReadyUniqueEventTimes := map[string]bool{}
	var coverageSum float64
	for _, record := range records {
		episode := record.Dataset.Summary
		summary.TotalEpisodes++
		symbols[episode.Symbol] = true
		timeframes[episode.Timeframe] = true
		coverageSum += episode.OrderbookCoveragePct
		switch episode.Classification {
		case strategy.EpisodeTrueReversalCandidate:
			summary.TrueReversalCandidateCount++
		case strategy.EpisodeFalseReversalInvalidated:
			summary.FalseReversalInvalidatedCount++
		case strategy.EpisodeNoSetup:
			summary.NoSetupCount++
		case strategy.EpisodeOrderbookDataGap:
			summary.DataGapOrderbookUnavailableCount++
		}
		if episode.CandidateReadyCount > 0 {
			summary.CandidateReadyEpisodes++
		}
		for _, event := range record.Dataset.Events {
			if !boolEventValue(event["candidate_ready"]) {
				continue
			}
			eventName := stringEventValue(event["event"])
			switch eventName {
			case "reversal_long_evaluation":
				summary.CandidateReadyEvaluationEvents++
			case "reversal_long_pipeline_decision":
				summary.CandidateReadyPipelineDecisions++
			default:
				continue
			}
			ts := timeValueFromEvent(event["time"])
			if ts.IsZero() {
				ts = timeValueFromEvent(event["timestamp"])
			}
			timeKey := stringEventValue(event["time"])
			if !ts.IsZero() {
				timeKey = ts.UTC().Format(time.RFC3339Nano)
			}
			if timeKey != "" {
				sourceFile := record.EpisodePath
				if sourceFile == "" {
					sourceFile = record.RunKey
				}
				candidateReadyUniqueEventTimes[sourceFile+"|"+timeKey] = true
			}
		}
		if episode.SetupDetectedCount > 0 {
			summary.SetupDetectedEpisodes++
		}
		if episode.SetupInvalidatedCount > 0 {
			summary.SetupInvalidatedEpisodes++
		}
		if episode.MainBlocker != "" {
			mainBlockers[episode.MainBlocker]++
		}
		if episode.MainInvalidationReason != "" {
			invalidationReasons[episode.MainInvalidationReason]++
		}
		summary.MaxScoreDistribution[maxScoreBucket(episode.MaxScore)]++
		if record.Split == datasetSplitValidation {
			summary.Split.ValidationEpisodes++
		} else {
			summary.Split.TrainEpisodes++
		}
	}
	if summary.TotalEpisodes > 0 {
		summary.AvgOrderbookCoveragePct = coverageSum / float64(summary.TotalEpisodes)
		summary.CandidateRate = float64(summary.CandidateReadyEpisodes) / float64(summary.TotalEpisodes)
		summary.InvalidationRate = float64(summary.SetupInvalidatedEpisodes) / float64(summary.TotalEpisodes)
	}
	summary.CandidateReadyExtractedEpisodes = summary.CandidateReadyEpisodes
	summary.CandidateReadyUniqueEventTimes = len(candidateReadyUniqueEventTimes)
	summary.ExtractedEpisodesTotal = summary.TotalEpisodes
	summary.SensitivityEpisodesTotal = countSensitivityEpisodeRuns(sensitivityRuns, "")
	summary.AggregationWarnings = datasetAggregationWarnings(summary, sensitivityRuns)
	summary.Symbols = sortedSet(symbols)
	summary.Timeframes = sortedSet(timeframes)
	summary.TopMainBlockers = topCounts(mainBlockers, 10)
	summary.TopInvalidationReasons = topCounts(invalidationReasons, 10)
	summary.HighScoreRejectedCount, summary.HighScoreRejectedGroups, summary.HighScoreRejectedExamples = summarizeHighScoreRejectedEvaluations(buildDatasetEpisodeIndexRows(records), highScoreThreshold)
	if !opts.ValidationStart.IsZero() {
		summary.Split.Method = "validation_start"
		t := opts.ValidationStart
		summary.Split.ValidationStart = &t
	} else {
		summary.Split.Method = "train_ratio"
		summary.Split.TrainRatio = opts.TrainRatio
	}
	summary.SensitivityAggregation[datasetSplitAll] = aggregateSensitivityRuns(sensitivityRuns, "")
	summary.SensitivityAggregation[datasetSplitTrain] = aggregateSensitivityRuns(sensitivityRuns, datasetSplitTrain)
	summary.SensitivityAggregation[datasetSplitValidation] = aggregateSensitivityRuns(sensitivityRuns, datasetSplitValidation)
	summary.OfflineFilterRejectedCandidateCount, summary.OfflineFilterRejectReasons[datasetSplitAll] = offlineFilterRejectReasonSummary(sensitivityRuns, "")
	_, summary.OfflineFilterRejectReasons[datasetSplitTrain] = offlineFilterRejectReasonSummary(sensitivityRuns, datasetSplitTrain)
	_, summary.OfflineFilterRejectReasons[datasetSplitValidation] = offlineFilterRejectReasonSummary(sensitivityRuns, datasetSplitValidation)
	summary.CandidateOutcomeAggregation[datasetSplitAll] = aggregateCandidateOutcomeRuns(sensitivityRuns, "")
	summary.CandidateOutcomeAggregation[datasetSplitTrain] = aggregateCandidateOutcomeRuns(sensitivityRuns, datasetSplitTrain)
	summary.CandidateOutcomeAggregation[datasetSplitValidation] = aggregateCandidateOutcomeRuns(sensitivityRuns, datasetSplitValidation)
	summary.CandidateOutcomeCount = candidateOutcomeCount(sensitivityRuns, "")
	summary.RawMarketCandidateCount, summary.UniqueMarketCandidateCount = candidateOutcomeMarketCounts(sensitivityRuns, "")
	summary.DuplicateCandidateCount = summary.RawMarketCandidateCount - summary.UniqueMarketCandidateCount
	summary.CandidateOutcomeExamples = candidateOutcomeExamples(sensitivityRuns, 20)
	summary.StableSensitivityParameters = stableSensitivityParameters(summary.SensitivityAggregation[datasetSplitTrain], summary.SensitivityAggregation[datasetSplitValidation])
	return summary
}

func maxScoreBucket(score float64) string {
	switch {
	case score < 0.50:
		return "lt_0_50"
	case score < 0.70:
		return "0_50_to_0_69"
	case score < 0.80:
		return "0_70_to_0_79"
	default:
		return "gte_0_80"
	}
}

func sortedSet(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func topCounts(counts map[string]int, limit int) []datasetCount {
	out := make([]datasetCount, 0, len(counts))
	for value, count := range counts {
		out = append(out, datasetCount{Value: value, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Value < out[j].Value
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func summarizeHighScoreRejectedEvaluations(rows []datasetEpisodeIndexRow, threshold float64) (int, []datasetHighScoreRejectedGroup, []datasetHighScoreRejectedExample) {
	type groupKey struct {
		rejectReason       string
		invalidationReason string
	}
	groupCounts := map[groupKey]int{}
	var examples []datasetHighScoreRejectedExample
	total := 0
	for _, row := range rows {
		if row.Event != "reversal_long_evaluation" || row.CandidateReady || row.Score < threshold {
			continue
		}
		total++
		key := groupKey{rejectReason: row.RejectReason, invalidationReason: row.InvalidationReason}
		groupCounts[key]++
		examples = append(examples, datasetHighScoreRejectedExample{
			SourceFile:                 row.SourceFile,
			RecordIndex:                row.RecordIndex,
			Symbol:                     row.Symbol,
			Timeframe:                  row.Timeframe,
			EventTime:                  row.EventTime,
			Score:                      row.Score,
			State:                      row.State,
			RejectReason:               row.RejectReason,
			SetupInvalidated:           row.SetupInvalidated,
			InvalidationReason:         row.InvalidationReason,
			SetupDetectedAt:            row.SetupDetectedAt,
			SetupAgeBars:               row.SetupAgeBars,
			MemoryReclaimUsed:          row.MemoryReclaimUsed,
			OrderbookRatio:             row.OrderbookRatio,
			BidAskImbalance:            row.BidAskImbalance,
			SpreadBps:                  row.SpreadBps,
			OrderbookHostile:           row.OrderbookHostile,
			OrderbookRejectReason:      row.OrderbookRejectReason,
			OrderbookUnavailableReason: row.OrderbookUnavailableReason,
			HTFBias:                    row.HTFBias,
			HTFAllowed:                 row.HTFAllowed,
			HTFOverrideAllowed:         row.HTFOverrideAllowed,
		})
	}
	groups := make([]datasetHighScoreRejectedGroup, 0, len(groupCounts))
	for key, count := range groupCounts {
		groups = append(groups, datasetHighScoreRejectedGroup{
			RejectReason:       key.rejectReason,
			InvalidationReason: key.invalidationReason,
			Count:              count,
		})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		if groups[i].RejectReason != groups[j].RejectReason {
			return groups[i].RejectReason < groups[j].RejectReason
		}
		return groups[i].InvalidationReason < groups[j].InvalidationReason
	})
	sort.Slice(examples, func(i, j int) bool {
		if examples[i].Score != examples[j].Score {
			return examples[i].Score > examples[j].Score
		}
		if examples[i].EventTime != examples[j].EventTime {
			return examples[i].EventTime < examples[j].EventTime
		}
		return examples[i].SourceFile < examples[j].SourceFile
	})
	if len(examples) > 20 {
		examples = examples[:20]
	}
	return total, groups, examples
}

func countSensitivityEpisodeRuns(runs []datasetSensitivityRun, split string) int {
	total := 0
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		if len(run.Results) == 0 {
			continue
		}
		total++
	}
	return total
}

func datasetAggregationWarnings(summary datasetSummary, sensitivityRuns []datasetSensitivityRun) []string {
	var warnings []string
	if summary.AggregationUnit != datasetAggregationUnitExtractedEpisode {
		warnings = append(warnings, fmt.Sprintf("sensitivity aggregation unit is %s; compare totals only within the same unit", summary.AggregationUnit))
	}
	if summary.SensitivityEpisodesTotal != summary.ExtractedEpisodesTotal {
		warnings = append(warnings, fmt.Sprintf("sensitivity episode total mismatch: extracted_episodes_total=%d sensitivity_episodes_total=%d aggregation_unit=%s", summary.ExtractedEpisodesTotal, summary.SensitivityEpisodesTotal, summary.AggregationUnit))
	}
	trainSensitivityTotal := countSensitivityEpisodeRuns(sensitivityRuns, datasetSplitTrain)
	if trainSensitivityTotal != summary.Split.TrainEpisodes {
		warnings = append(warnings, fmt.Sprintf("train sensitivity episode total mismatch: train_episodes=%d sensitivity_train_episodes=%d aggregation_unit=%s", summary.Split.TrainEpisodes, trainSensitivityTotal, summary.AggregationUnit))
	}
	validationSensitivityTotal := countSensitivityEpisodeRuns(sensitivityRuns, datasetSplitValidation)
	if validationSensitivityTotal != summary.Split.ValidationEpisodes {
		warnings = append(warnings, fmt.Sprintf("validation sensitivity episode total mismatch: validation_episodes=%d sensitivity_validation_episodes=%d aggregation_unit=%s", summary.Split.ValidationEpisodes, validationSensitivityTotal, summary.AggregationUnit))
	}
	return warnings
}

func aggregateSensitivityRuns(runs []datasetSensitivityRun, split string) []datasetSensitivityAggregate {
	accs := map[sensitivityKey]*sensitivityAccumulator{}
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		for _, result := range run.Results {
			key := sensitivityKeyFromResult(result)
			acc := accs[key]
			if acc == nil {
				acc = &sensitivityAccumulator{key: key}
				accs[key] = acc
			}
			acc.total++
			acc.maxScoreSum += result.MaxScore
			acc.ordersSent += result.OrdersSent
			acc.unfilteredCandidateReadyCount += resultUnfilteredCandidateReadyCount(result)
			acc.filteredCandidateReadyCount += result.CandidateReadyCount
			acc.offlineFilterRejectedCandidateCount += result.OfflineFilterRejectedCandidateCount
			if result.CandidateReadyCount > 0 {
				acc.candidateReadyEpisodes++
			}
			if result.SetupDetectedCount > 0 {
				acc.setupDetectedEpisodes++
			}
			if result.SetupInvalidatedCount > 0 {
				acc.setupInvalidatedEpisodes++
			}
		}
	}
	out := make([]datasetSensitivityAggregate, 0, len(accs))
	for _, acc := range accs {
		item := datasetSensitivityAggregate{
			MinRSIDelta:                         acc.key.MinRSIDelta,
			NoNewLowBars:                        acc.key.NoNewLowBars,
			ScoreThreshold:                      acc.key.ScoreThreshold,
			MinVolumeRatio:                      acc.key.MinVolumeRatio,
			SetupMemoryBars:                     acc.key.SetupMemoryBars,
			InvalidationATRMult:                 acc.key.InvalidationATRMult,
			RequireOrderbookNotHostile:          acc.key.RequireOrderbookNotHostile,
			MinOrderbookRatio:                   acc.key.MinOrderbookRatio,
			MinBarsWithoutNewLowAfterReclaim:    acc.key.MinBarsWithoutNewLowAfterReclaim,
			RequireCloseAboveMiddleBB:           acc.key.RequireCloseAboveMiddleBB,
			RequireHigherLowAfterReclaim:        acc.key.RequireHigherLowAfterReclaim,
			TotalEpisodes:                       acc.total,
			CandidateReadyEpisodes:              acc.candidateReadyEpisodes,
			UnfilteredCandidateReadyCount:       acc.unfilteredCandidateReadyCount,
			FilteredCandidateReadyCount:         acc.filteredCandidateReadyCount,
			OfflineFilterRejectedCandidateCount: acc.offlineFilterRejectedCandidateCount,
			SetupDetectedEpisodes:               acc.setupDetectedEpisodes,
			SetupInvalidatedEpisodes:            acc.setupInvalidatedEpisodes,
			OrdersSent:                          acc.ordersSent,
		}
		if acc.total > 0 {
			item.AvgMaxScore = acc.maxScoreSum / float64(acc.total)
			item.CandidateRate = float64(acc.candidateReadyEpisodes) / float64(acc.total)
			item.InvalidationRate = float64(acc.setupInvalidatedEpisodes) / float64(acc.total)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return sensitivityAggregateLess(out[i], out[j])
	})
	return out
}

func offlineFilterRejectReasonSummary(runs []datasetSensitivityRun, split string) (int, []datasetCount) {
	counts := map[string]int{}
	total := 0
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		for _, result := range run.Results {
			total += result.OfflineFilterRejectedCandidateCount
			if len(result.OfflineFilterRejectReasons) == 0 && result.OfflineFilterRejectedCandidateCount > 0 {
				counts["unknown"] += result.OfflineFilterRejectedCandidateCount
				continue
			}
			for reason, count := range result.OfflineFilterRejectReasons {
				counts[reason] += count
			}
		}
	}
	return total, topCounts(counts, 0)
}

func aggregateCandidateOutcomeRuns(runs []datasetSensitivityRun, split string) []datasetCandidateOutcomeAggregate {
	accs := map[candidateOutcomeKey]*candidateOutcomeAccumulator{}
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		sourceFile := candidateOutcomeSourceFile(run)
		for _, result := range run.Results {
			baseKey := sensitivityKeyFromResult(result)
			for _, outcome := range result.CandidateOutcomes {
				key := candidateOutcomeKey{
					sensitivityKey: baseKey,
					TPBps:          outcome.TPBps,
					SLBps:          outcome.SLBps,
					FeeBps:         outcome.FeeBps,
					SlippageBps:    outcome.SlippageBps,
				}
				acc := accs[key]
				if acc == nil {
					acc = &candidateOutcomeAccumulator{key: key}
					accs[key] = acc
				}
				addCandidateOutcomeStats(&acc.raw, outcome)
				if acc.canonical == nil {
					acc.canonical = map[candidateMarketKey]candidateOutcomeCanonical{}
				}
				marketKey := candidateOutcomeMarketKey(run, baseKey, outcome)
				candidate := candidateOutcomeCanonical{
					sourceFile:  sourceFile,
					recordIndex: run.RecordIndex,
					outcome:     outcome,
				}
				if current, ok := acc.canonical[marketKey]; !ok || candidateOutcomeCanonicalLess(candidate, current) {
					acc.canonical[marketKey] = candidate
				}
			}
		}
	}
	out := make([]datasetCandidateOutcomeAggregate, 0, len(accs))
	for _, acc := range accs {
		uniqueStats := candidateOutcomeStats{}
		for _, candidate := range acc.canonical {
			addCandidateOutcomeStats(&uniqueStats, candidate.outcome)
		}
		item := datasetCandidateOutcomeAggregate{
			MinRSIDelta:                      acc.key.MinRSIDelta,
			NoNewLowBars:                     acc.key.NoNewLowBars,
			ScoreThreshold:                   acc.key.ScoreThreshold,
			MinVolumeRatio:                   acc.key.MinVolumeRatio,
			SetupMemoryBars:                  acc.key.SetupMemoryBars,
			InvalidationATRMult:              acc.key.InvalidationATRMult,
			RequireOrderbookNotHostile:       acc.key.RequireOrderbookNotHostile,
			MinOrderbookRatio:                acc.key.MinOrderbookRatio,
			MinBarsWithoutNewLowAfterReclaim: acc.key.MinBarsWithoutNewLowAfterReclaim,
			RequireCloseAboveMiddleBB:        acc.key.RequireCloseAboveMiddleBB,
			RequireHigherLowAfterReclaim:     acc.key.RequireHigherLowAfterReclaim,
			TPBps:                            acc.key.TPBps,
			SLBps:                            acc.key.SLBps,
			FeeBps:                           acc.key.FeeBps,
			SlippageBps:                      acc.key.SlippageBps,
		}
		applyRawCandidateOutcomeStats(&item, acc.raw)
		applyUniqueCandidateOutcomeStats(&item, uniqueStats)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return candidateOutcomeAggregateLess(out[i], out[j])
	})
	return out
}

func sensitivityKeyFromResult(result strategy.ReversalSensitivityResult) sensitivityKey {
	return sensitivityKey{
		MinRSIDelta:                      result.MinRSIDelta,
		NoNewLowBars:                     result.NoNewLowBars,
		ScoreThreshold:                   result.ScoreThreshold,
		MinVolumeRatio:                   result.MinVolumeRatio,
		SetupMemoryBars:                  result.SetupMemoryBars,
		InvalidationATRMult:              result.InvalidationATRMult,
		RequireOrderbookNotHostile:       result.RequireOrderbookNotHostile,
		MinOrderbookRatio:                result.MinOrderbookRatio,
		MinBarsWithoutNewLowAfterReclaim: result.MinBarsWithoutNewLowAfterReclaim,
		RequireCloseAboveMiddleBB:        result.RequireCloseAboveMiddleBB,
		RequireHigherLowAfterReclaim:     result.RequireHigherLowAfterReclaim,
	}
}

func resultUnfilteredCandidateReadyCount(result strategy.ReversalSensitivityResult) int {
	if result.UnfilteredCandidateReadyCount > 0 || result.OfflineFilterRejectedCandidateCount > 0 {
		return result.UnfilteredCandidateReadyCount
	}
	return result.CandidateReadyCount
}

func candidateOutcomeMarketKey(run datasetSensitivityRun, key sensitivityKey, outcome strategy.ReversalCandidateOutcome) candidateMarketKey {
	return candidateMarketKey{
		sensitivityKey: key,
		Symbol:         run.Manifest.Symbol,
		EventTime:      eventTimeKey(outcome.Time),
	}
}

func candidateOutcomeOccurrenceKey(run datasetSensitivityRun, key sensitivityKey, outcome strategy.ReversalCandidateOutcome) candidateMarketOccurrenceKey {
	return candidateMarketOccurrenceKey{
		candidateMarketKey: candidateOutcomeMarketKey(run, key, outcome),
		SourceFile:         candidateOutcomeSourceFile(run),
		RecordIndex:        run.RecordIndex,
	}
}

func candidateOutcomeSourceFile(run datasetSensitivityRun) string {
	if run.SourceFile != "" {
		return run.SourceFile
	}
	if run.RunKey != "" {
		return run.RunKey
	}
	return fmt.Sprintf("record_%d", run.RecordIndex)
}

// Canonical duplicate rule: choose the lexicographically earliest source_file,
// then the lowest record_index for the same symbol, event time, and sensitivity parameters.
func candidateOutcomeCanonicalLess(left, right candidateOutcomeCanonical) bool {
	if left.sourceFile != right.sourceFile {
		return left.sourceFile < right.sourceFile
	}
	return left.recordIndex < right.recordIndex
}

func addCandidateOutcomeStats(stats *candidateOutcomeStats, outcome strategy.ReversalCandidateOutcome) {
	stats.count++
	if outcome.NetReturnBps > 0 {
		stats.wins++
	}
	stats.netRSum += outcome.NetR
	stats.netRs = append(stats.netRs, outcome.NetR)
	stats.mfeSum += outcome.MaxFavorable30mBps
	stats.maeSum += outcome.MaxAdverse30mBps
}

func applyRawCandidateOutcomeStats(item *datasetCandidateOutcomeAggregate, stats candidateOutcomeStats) {
	item.RawCandidateCount = stats.count
	item.RawWinningCandidateCount = stats.wins
	item.CandidateCount = stats.count
	item.WinningCandidateCount = stats.wins
	if stats.count == 0 {
		return
	}
	item.RawWinRate = float64(stats.wins) / float64(stats.count)
	item.RawAvgNetR = stats.netRSum / float64(stats.count)
	item.RawMedianNetR = medianFloat64(stats.netRs)
	item.WinRate = item.RawWinRate
	item.AvgNetR = item.RawAvgNetR
	item.MedianNetR = item.RawMedianNetR
	item.AvgMFEBps = stats.mfeSum / float64(stats.count)
	item.AvgMAEBps = stats.maeSum / float64(stats.count)
}

func applyUniqueCandidateOutcomeStats(item *datasetCandidateOutcomeAggregate, stats candidateOutcomeStats) {
	item.UniqueMarketCandidateCount = stats.count
	item.UniqueMarketWinningCandidateCount = stats.wins
	if stats.count == 0 {
		return
	}
	item.UniqueMarketWinRate = float64(stats.wins) / float64(stats.count)
	item.UniqueMarketAvgNetR = stats.netRSum / float64(stats.count)
	item.UniqueMarketMedianNetR = medianFloat64(stats.netRs)
	item.UniqueMarketAvgMFEBps = stats.mfeSum / float64(stats.count)
	item.UniqueMarketAvgMAEBps = stats.maeSum / float64(stats.count)
}

func candidateOutcomeCount(runs []datasetSensitivityRun, split string) int {
	total := 0
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		for _, result := range run.Results {
			total += len(result.CandidateOutcomes)
		}
	}
	return total
}

func candidateOutcomeMarketCounts(runs []datasetSensitivityRun, split string) (int, int) {
	rawOccurrences := map[candidateMarketOccurrenceKey]bool{}
	uniqueMarkets := map[candidateMarketKey]bool{}
	for _, run := range runs {
		if split != "" && run.Split != split {
			continue
		}
		for _, result := range run.Results {
			baseKey := sensitivityKeyFromResult(result)
			for _, outcome := range result.CandidateOutcomes {
				rawOccurrences[candidateOutcomeOccurrenceKey(run, baseKey, outcome)] = true
				uniqueMarkets[candidateOutcomeMarketKey(run, baseKey, outcome)] = true
			}
		}
	}
	return len(rawOccurrences), len(uniqueMarkets)
}

func candidateOutcomeExamples(runs []datasetSensitivityRun, limit int) []datasetCandidateOutcomeExample {
	var examples []datasetCandidateOutcomeExample
	for _, run := range runs {
		sourceFile := candidateOutcomeSourceFile(run)
		for _, result := range run.Results {
			baseKey := sensitivityKeyFromResult(result)
			for _, outcome := range result.CandidateOutcomes {
				marketKey := candidateOutcomeMarketKey(run, baseKey, outcome)
				examples = append(examples, datasetCandidateOutcomeExample{
					RunKey:                           run.RunKey,
					SourceFile:                       sourceFile,
					RecordIndex:                      run.RecordIndex,
					Split:                            run.Split,
					Symbol:                           run.Manifest.Symbol,
					Timeframe:                        run.Manifest.Timeframe,
					EventTime:                        outcome.Time.UTC().Format(time.RFC3339Nano),
					UniqueCandidateKey:               candidateMarketKeyString(marketKey),
					MinRSIDelta:                      result.MinRSIDelta,
					NoNewLowBars:                     result.NoNewLowBars,
					ScoreThreshold:                   result.ScoreThreshold,
					MinVolumeRatio:                   result.MinVolumeRatio,
					SetupMemoryBars:                  result.SetupMemoryBars,
					InvalidationATRMult:              result.InvalidationATRMult,
					RequireOrderbookNotHostile:       result.RequireOrderbookNotHostile,
					MinOrderbookRatio:                result.MinOrderbookRatio,
					MinBarsWithoutNewLowAfterReclaim: result.MinBarsWithoutNewLowAfterReclaim,
					RequireCloseAboveMiddleBB:        result.RequireCloseAboveMiddleBB,
					RequireHigherLowAfterReclaim:     result.RequireHigherLowAfterReclaim,
					TPBps:                            outcome.TPBps,
					SLBps:                            outcome.SLBps,
					ForwardReturn5mBps:               outcome.ForwardReturn5mBps,
					ForwardReturn15mBps:              outcome.ForwardReturn15mBps,
					ForwardReturn30mBps:              outcome.ForwardReturn30mBps,
					MaxFavorable30mBps:               outcome.MaxFavorable30mBps,
					MaxAdverse30mBps:                 outcome.MaxAdverse30mBps,
					TimeToMFE:                        outcome.TimeToMFE,
					TimeToMAE:                        outcome.TimeToMAE,
					WouldHitTP:                       outcome.WouldHitTP,
					WouldHitSL:                       outcome.WouldHitSL,
					HitOrder:                         outcome.HitOrder,
					NetReturnBps:                     outcome.NetReturnBps,
					NetR:                             outcome.NetR,
				})
			}
		}
	}
	sort.Slice(examples, func(i, j int) bool {
		if examples[i].Split != examples[j].Split {
			return examples[i].Split == datasetSplitValidation
		}
		if examples[i].EventTime != examples[j].EventTime {
			return examples[i].EventTime < examples[j].EventTime
		}
		if examples[i].RunKey != examples[j].RunKey {
			return examples[i].RunKey < examples[j].RunKey
		}
		return examples[i].NetR > examples[j].NetR
	})
	if limit > 0 && len(examples) > limit {
		examples = examples[:limit]
	}
	return examples
}

func candidateMarketKeyString(key candidateMarketKey) string {
	return fmt.Sprintf("symbol=%s|event_time=%s|min_rsi_delta=%.12g|no_new_low_bars=%d|score_threshold=%.12g|min_volume_ratio=%.12g|setup_memory_bars=%d|invalidation_atr_mult=%.12g|require_orderbook_not_hostile=%t|min_orderbook_ratio=%.12g|min_bars_without_new_low_after_reclaim=%d|require_close_above_middle_bb=%t|require_higher_low_after_reclaim=%t",
		key.Symbol,
		key.EventTime,
		key.MinRSIDelta,
		key.NoNewLowBars,
		key.ScoreThreshold,
		key.MinVolumeRatio,
		key.SetupMemoryBars,
		key.InvalidationATRMult,
		key.RequireOrderbookNotHostile,
		key.MinOrderbookRatio,
		key.MinBarsWithoutNewLowAfterReclaim,
		key.RequireCloseAboveMiddleBB,
		key.RequireHigherLowAfterReclaim)
}

func candidateOutcomeKeyString(key candidateOutcomeKey) string {
	return fmt.Sprintf("min_rsi_delta=%.12g|no_new_low_bars=%d|score_threshold=%.12g|min_volume_ratio=%.12g|setup_memory_bars=%d|invalidation_atr_mult=%.12g|require_orderbook_not_hostile=%t|min_orderbook_ratio=%.12g|min_bars_without_new_low_after_reclaim=%d|require_close_above_middle_bb=%t|require_higher_low_after_reclaim=%t|tp_bps=%.12g|sl_bps=%.12g|fee_bps=%.12g|slippage_bps=%.12g",
		key.MinRSIDelta,
		key.NoNewLowBars,
		key.ScoreThreshold,
		key.MinVolumeRatio,
		key.SetupMemoryBars,
		key.InvalidationATRMult,
		key.RequireOrderbookNotHostile,
		key.MinOrderbookRatio,
		key.MinBarsWithoutNewLowAfterReclaim,
		key.RequireCloseAboveMiddleBB,
		key.RequireHigherLowAfterReclaim,
		key.TPBps,
		key.SLBps,
		key.FeeBps,
		key.SlippageBps)
}

func sensitivityAggregateLess(left, right datasetSensitivityAggregate) bool {
	if left.MinRSIDelta != right.MinRSIDelta {
		return left.MinRSIDelta < right.MinRSIDelta
	}
	if left.NoNewLowBars != right.NoNewLowBars {
		return left.NoNewLowBars < right.NoNewLowBars
	}
	if left.ScoreThreshold != right.ScoreThreshold {
		return left.ScoreThreshold < right.ScoreThreshold
	}
	if left.MinVolumeRatio != right.MinVolumeRatio {
		return left.MinVolumeRatio < right.MinVolumeRatio
	}
	if left.SetupMemoryBars != right.SetupMemoryBars {
		return left.SetupMemoryBars < right.SetupMemoryBars
	}
	if left.InvalidationATRMult != right.InvalidationATRMult {
		return left.InvalidationATRMult < right.InvalidationATRMult
	}
	if left.RequireOrderbookNotHostile != right.RequireOrderbookNotHostile {
		return !left.RequireOrderbookNotHostile && right.RequireOrderbookNotHostile
	}
	if left.MinOrderbookRatio != right.MinOrderbookRatio {
		return left.MinOrderbookRatio < right.MinOrderbookRatio
	}
	if left.MinBarsWithoutNewLowAfterReclaim != right.MinBarsWithoutNewLowAfterReclaim {
		return left.MinBarsWithoutNewLowAfterReclaim < right.MinBarsWithoutNewLowAfterReclaim
	}
	if left.RequireCloseAboveMiddleBB != right.RequireCloseAboveMiddleBB {
		return !left.RequireCloseAboveMiddleBB && right.RequireCloseAboveMiddleBB
	}
	if left.RequireHigherLowAfterReclaim != right.RequireHigherLowAfterReclaim {
		return !left.RequireHigherLowAfterReclaim && right.RequireHigherLowAfterReclaim
	}
	return false
}

func candidateOutcomeAggregateLess(left, right datasetCandidateOutcomeAggregate) bool {
	if left.MinRSIDelta != right.MinRSIDelta {
		return left.MinRSIDelta < right.MinRSIDelta
	}
	if left.NoNewLowBars != right.NoNewLowBars {
		return left.NoNewLowBars < right.NoNewLowBars
	}
	if left.ScoreThreshold != right.ScoreThreshold {
		return left.ScoreThreshold < right.ScoreThreshold
	}
	if left.MinVolumeRatio != right.MinVolumeRatio {
		return left.MinVolumeRatio < right.MinVolumeRatio
	}
	if left.SetupMemoryBars != right.SetupMemoryBars {
		return left.SetupMemoryBars < right.SetupMemoryBars
	}
	if left.InvalidationATRMult != right.InvalidationATRMult {
		return left.InvalidationATRMult < right.InvalidationATRMult
	}
	if left.RequireOrderbookNotHostile != right.RequireOrderbookNotHostile {
		return !left.RequireOrderbookNotHostile && right.RequireOrderbookNotHostile
	}
	if left.MinOrderbookRatio != right.MinOrderbookRatio {
		return left.MinOrderbookRatio < right.MinOrderbookRatio
	}
	if left.MinBarsWithoutNewLowAfterReclaim != right.MinBarsWithoutNewLowAfterReclaim {
		return left.MinBarsWithoutNewLowAfterReclaim < right.MinBarsWithoutNewLowAfterReclaim
	}
	if left.RequireCloseAboveMiddleBB != right.RequireCloseAboveMiddleBB {
		return !left.RequireCloseAboveMiddleBB && right.RequireCloseAboveMiddleBB
	}
	if left.RequireHigherLowAfterReclaim != right.RequireHigherLowAfterReclaim {
		return !left.RequireHigherLowAfterReclaim && right.RequireHigherLowAfterReclaim
	}
	if left.TPBps != right.TPBps {
		return left.TPBps < right.TPBps
	}
	if left.SLBps != right.SLBps {
		return left.SLBps < right.SLBps
	}
	if left.FeeBps != right.FeeBps {
		return left.FeeBps < right.FeeBps
	}
	return left.SlippageBps < right.SlippageBps
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func stableSensitivityParameters(train, validation []datasetSensitivityAggregate) []datasetStableSensitivityParameters {
	validationByKey := make(map[sensitivityKey]datasetSensitivityAggregate, len(validation))
	for _, item := range validation {
		validationByKey[sensitivityKeyFromAggregate(item)] = item
	}
	var stable []datasetStableSensitivityParameters
	for _, trainItem := range train {
		validationItem, ok := validationByKey[sensitivityKeyFromAggregate(trainItem)]
		if !ok || trainItem.TotalEpisodes == 0 || validationItem.TotalEpisodes == 0 {
			continue
		}
		if trainItem.CandidateReadyEpisodes == 0 || validationItem.CandidateReadyEpisodes == 0 {
			continue
		}
		if math.Abs(trainItem.CandidateRate-validationItem.CandidateRate) > 0.10 {
			continue
		}
		if math.Abs(trainItem.InvalidationRate-validationItem.InvalidationRate) > 0.10 {
			continue
		}
		stable = append(stable, datasetStableSensitivityParameters{
			MinRSIDelta:                      trainItem.MinRSIDelta,
			NoNewLowBars:                     trainItem.NoNewLowBars,
			ScoreThreshold:                   trainItem.ScoreThreshold,
			MinVolumeRatio:                   trainItem.MinVolumeRatio,
			SetupMemoryBars:                  trainItem.SetupMemoryBars,
			InvalidationATRMult:              trainItem.InvalidationATRMult,
			RequireOrderbookNotHostile:       trainItem.RequireOrderbookNotHostile,
			MinOrderbookRatio:                trainItem.MinOrderbookRatio,
			MinBarsWithoutNewLowAfterReclaim: trainItem.MinBarsWithoutNewLowAfterReclaim,
			RequireCloseAboveMiddleBB:        trainItem.RequireCloseAboveMiddleBB,
			RequireHigherLowAfterReclaim:     trainItem.RequireHigherLowAfterReclaim,
			TrainCandidateRate:               trainItem.CandidateRate,
			ValidationCandidateRate:          validationItem.CandidateRate,
			TrainInvalidationRate:            trainItem.InvalidationRate,
			ValidationInvalidationRate:       validationItem.InvalidationRate,
		})
	}
	return stable
}

func sensitivityKeyFromAggregate(item datasetSensitivityAggregate) sensitivityKey {
	return sensitivityKey{
		MinRSIDelta:                      item.MinRSIDelta,
		NoNewLowBars:                     item.NoNewLowBars,
		ScoreThreshold:                   item.ScoreThreshold,
		MinVolumeRatio:                   item.MinVolumeRatio,
		SetupMemoryBars:                  item.SetupMemoryBars,
		InvalidationATRMult:              item.InvalidationATRMult,
		RequireOrderbookNotHostile:       item.RequireOrderbookNotHostile,
		MinOrderbookRatio:                item.MinOrderbookRatio,
		MinBarsWithoutNewLowAfterReclaim: item.MinBarsWithoutNewLowAfterReclaim,
		RequireCloseAboveMiddleBB:        item.RequireCloseAboveMiddleBB,
		RequireHigherLowAfterReclaim:     item.RequireHigherLowAfterReclaim,
	}
}

func evaluateDatasetAcceptance(summary datasetSummary, minEpisodes int, minOrderbookCoverage float64, minCandidateEpisodes int) datasetAcceptanceResult {
	return evaluateDatasetAcceptanceWithOptions(summary, datasetAcceptanceOptions{
		MinEpisodes:          minEpisodes,
		MinOrderbookCoverage: minOrderbookCoverage,
		MinCandidateEpisodes: minCandidateEpisodes,
	})
}

func acceptanceOptionsFromRunner(opts datasetRunnerOptions) datasetAcceptanceOptions {
	return datasetAcceptanceOptions{
		MinEpisodes:                         opts.MinEpisodes,
		MinOrderbookCoverage:                opts.MinOrderbookCoverage,
		MinCandidateEpisodes:                opts.MinCandidateEpisodes,
		MinValidationUniqueMarketCandidates: opts.MinValidationUniqueMarketCandidates,
		MinValidationAvgNetR:                opts.MinValidationAvgNetR,
		MinValidationMedianNetR:             opts.MinValidationMedianNetR,
		MaxDuplicateCandidateRatio:          opts.MaxDuplicateCandidateRatio,
	}
}

func normalizeDatasetAcceptanceOptions(opts datasetAcceptanceOptions) datasetAcceptanceOptions {
	if opts.MinEpisodes <= 0 {
		opts.MinEpisodes = 50
	}
	if opts.MinOrderbookCoverage <= 0 {
		opts.MinOrderbookCoverage = 0.80
	}
	if opts.MinCandidateEpisodes <= 0 {
		opts.MinCandidateEpisodes = 10
	}
	if opts.MinValidationUniqueMarketCandidates <= 0 {
		opts.MinValidationUniqueMarketCandidates = defaultMinValidationUniqueMarketCandidates
	}
	if opts.MaxDuplicateCandidateRatio <= 0 {
		opts.MaxDuplicateCandidateRatio = defaultMaxDuplicateCandidateRatio
	}
	return opts
}

func evaluateDatasetAcceptanceWithOptions(summary datasetSummary, opts datasetAcceptanceOptions) datasetAcceptanceResult {
	opts = normalizeDatasetAcceptanceOptions(opts)
	coverageThresholdPct := normalizeCoverageThresholdPct(opts.MinOrderbookCoverage)
	safetyPass := summary.SafetyConstraintsPassed || (summary.OrdersSent == 0 && len(summary.SafetyViolations) == 0)
	actualCandidateCount := summary.CandidateReadyEpisodes
	candidateCountUnit := "candidate_ready_extracted_episode"
	if summary.RawMarketCandidateCount > 0 || summary.UniqueMarketCandidateCount > 0 {
		actualCandidateCount = summary.UniqueMarketCandidateCount
		candidateCountUnit = "unique_market_candidate"
	}
	result := datasetAcceptanceResult{
		MinEpisodes:                          opts.MinEpisodes,
		ActualEpisodes:                       summary.TotalEpisodes,
		MinOrderbookCoverage:                 coverageThresholdPct / 100,
		ActualOrderbookCoverage:              summary.AvgOrderbookCoveragePct / 100,
		MinCandidateEpisodes:                 opts.MinCandidateEpisodes,
		ActualCandidateEpisodes:              actualCandidateCount,
		ActualRawMarketCandidates:            summary.RawMarketCandidateCount,
		ActualUniqueMarketCandidates:         summary.UniqueMarketCandidateCount,
		DuplicateCandidateCount:              summary.DuplicateCandidateCount,
		CandidateCountUnit:                   candidateCountUnit,
		MinValidationUniqueMarketCandidates:  opts.MinValidationUniqueMarketCandidates,
		MinValidationAvgNetR:                 opts.MinValidationAvgNetR,
		MinValidationMedianNetR:              opts.MinValidationMedianNetR,
		MaxDuplicateCandidateRatio:           opts.MaxDuplicateCandidateRatio,
		ValidationUniqueMarketCandidatesPass: true,
		ValidationAvgNetRPass:                true,
		ValidationMedianNetRPass:             true,
		DuplicateCandidateRatioPass:          true,
		TrainValidationAvgNetRSignPass:       true,
		SafetyViolations:                     summary.SafetyViolations,
	}
	if summary.RawMarketCandidateCount > 0 {
		result.DuplicateCandidateRatio = float64(summary.DuplicateCandidateCount) / float64(summary.RawMarketCandidateCount)
	}
	result.MinEpisodesPass = summary.TotalEpisodes >= opts.MinEpisodes
	result.OrderbookCoveragePass = summary.AvgOrderbookCoveragePct >= coverageThresholdPct
	result.CandidateCountPass = actualCandidateCount >= opts.MinCandidateEpisodes
	result.SafetyConstraintsPass = safetyPass
	applyOutcomeAcceptance(&result, summary, opts)
	switch {
	case !result.SafetyConstraintsPass:
		result.Recommendation = acceptanceDoNotCalibrate
	case !result.OrderbookCoveragePass:
		result.Recommendation = acceptanceDoNotCalibrate
	case !result.MinEpisodesPass:
		result.Recommendation = acceptanceNeedMoreData
	case !result.CandidateCountPass:
		result.Recommendation = acceptanceNeedMoreData
	case result.OutcomeEnabled && !result.DuplicateCandidateRatioPass:
		result.Recommendation = acceptanceNeedMoreData
	case result.OutcomeEnabled && !result.ValidationUniqueMarketCandidatesPass:
		result.Recommendation = acceptanceNeedMoreData
	case result.OutcomeEnabled && (!result.ValidationAvgNetRPass || !result.ValidationMedianNetRPass || !result.TrainValidationAvgNetRSignPass):
		result.Recommendation = acceptanceRejectNegativeEdge
	default:
		result.Recommendation = acceptanceReadyOfflineCalibration
	}
	return result
}

func applyOutcomeAcceptance(result *datasetAcceptanceResult, summary datasetSummary, opts datasetAcceptanceOptions) {
	if result == nil {
		return
	}
	validation := summary.CandidateOutcomeAggregation[datasetSplitValidation]
	if len(validation) == 0 {
		result.OutcomeEnabled = false
		return
	}
	result.OutcomeEnabled = true
	result.DuplicateCandidateRatioPass = result.DuplicateCandidateRatio <= opts.MaxDuplicateCandidateRatio

	selected, ok := selectAcceptanceValidationOutcome(validation, opts.MinValidationUniqueMarketCandidates)
	if !ok {
		result.ValidationUniqueMarketCandidatesPass = false
		result.ValidationAvgNetRPass = false
		result.ValidationMedianNetRPass = false
		result.TrainValidationAvgNetRSignPass = false
		return
	}
	result.ActualValidationUniqueMarketCandidates = selected.UniqueMarketCandidateCount
	result.ActualValidationAvgNetR = selected.UniqueMarketAvgNetR
	result.ActualValidationMedianNetR = selected.UniqueMarketMedianNetR
	result.OutcomeParameterSet = candidateOutcomeKeyString(candidateOutcomeKeyFromAggregate(selected))
	result.ValidationUniqueMarketCandidatesPass = selected.UniqueMarketCandidateCount >= opts.MinValidationUniqueMarketCandidates
	result.ValidationAvgNetRPass = selected.UniqueMarketAvgNetR > opts.MinValidationAvgNetR
	result.ValidationMedianNetRPass = selected.UniqueMarketMedianNetR >= opts.MinValidationMedianNetR

	if train, ok := matchingTrainOutcome(summary.CandidateOutcomeAggregation[datasetSplitTrain], selected); ok {
		result.ActualTrainAvgNetR = train.UniqueMarketAvgNetR
		result.TrainValidationAvgNetROppositeSigns = oppositeSigns(train.UniqueMarketAvgNetR, selected.UniqueMarketAvgNetR)
	}
	result.TrainValidationAvgNetRSignPass = !result.TrainValidationAvgNetROppositeSigns || selected.UniqueMarketAvgNetR > 0
}

func selectAcceptanceValidationOutcome(items []datasetCandidateOutcomeAggregate, minUniqueCandidates int) (datasetCandidateOutcomeAggregate, bool) {
	if len(items) == 0 {
		return datasetCandidateOutcomeAggregate{}, false
	}
	eligible := make([]datasetCandidateOutcomeAggregate, 0, len(items))
	for _, item := range items {
		if item.UniqueMarketCandidateCount >= minUniqueCandidates {
			eligible = append(eligible, item)
		}
	}
	if len(eligible) == 0 {
		eligible = append(eligible, items...)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].UniqueMarketAvgNetR != eligible[j].UniqueMarketAvgNetR {
			return eligible[i].UniqueMarketAvgNetR > eligible[j].UniqueMarketAvgNetR
		}
		if eligible[i].UniqueMarketCandidateCount != eligible[j].UniqueMarketCandidateCount {
			return eligible[i].UniqueMarketCandidateCount > eligible[j].UniqueMarketCandidateCount
		}
		return candidateOutcomeAggregateLess(eligible[i], eligible[j])
	})
	return eligible[0], true
}

func matchingTrainOutcome(train []datasetCandidateOutcomeAggregate, validation datasetCandidateOutcomeAggregate) (datasetCandidateOutcomeAggregate, bool) {
	key := candidateOutcomeKeyFromAggregate(validation)
	for _, item := range train {
		if candidateOutcomeKeyFromAggregate(item) == key {
			return item, true
		}
	}
	return datasetCandidateOutcomeAggregate{}, false
}

func oppositeSigns(left, right float64) bool {
	return (left < 0 && right > 0) || (left > 0 && right < 0)
}

func normalizeCoverageThresholdPct(value float64) float64 {
	if value <= 1 {
		return value * 100
	}
	return value
}

func writeDatasetReport(path string, summary datasetSummary, acceptance datasetAcceptanceResult) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# Reversal Dataset Report")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "run_id: `%s`\n\n", summary.RunID)
	fmt.Fprintf(f, "episodes: total=%d train=%d validation=%d\n\n", summary.TotalEpisodes, summary.Split.TrainEpisodes, summary.Split.ValidationEpisodes)
	fmt.Fprintf(f, "extracted_episodes_total: `%d`\n\n", summary.ExtractedEpisodesTotal)
	fmt.Fprintf(f, "sensitivity_episodes_total: `%d`\n\n", summary.SensitivityEpisodesTotal)
	fmt.Fprintf(f, "aggregation_unit: `%s`\n\n", summary.AggregationUnit)
	if len(summary.AggregationWarnings) > 0 {
		fmt.Fprintln(f, "aggregation_warnings:")
		for _, warning := range summary.AggregationWarnings {
			fmt.Fprintf(f, "- %s\n", warning)
		}
		fmt.Fprintln(f)
	}
	fmt.Fprintf(f, "symbols: `%s`\n\n", strings.Join(summary.Symbols, ", "))
	fmt.Fprintf(f, "timeframes: `%s`\n\n", strings.Join(summary.Timeframes, ", "))
	fmt.Fprintf(f, "classification: true_reversal_candidate=%d false_reversal_invalidated=%d no_setup=%d data_gap_orderbook_unavailable=%d\n\n",
		summary.TrueReversalCandidateCount, summary.FalseReversalInvalidatedCount, summary.NoSetupCount, summary.DataGapOrderbookUnavailableCount)
	fmt.Fprintf(f, "candidate_ready_episodes: `%d`\n\n", summary.CandidateReadyEpisodes)
	fmt.Fprintln(f, "candidate_ready_episodes counts extracted episodes whose summary has at least one candidate-ready evaluation or setup-memory candidate. Event counters below are separate so evaluation and pipeline events are not double-counted.")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "candidate_ready_evaluation_events: `%d`\n\n", summary.CandidateReadyEvaluationEvents)
	fmt.Fprintf(f, "candidate_ready_pipeline_decisions: `%d`\n\n", summary.CandidateReadyPipelineDecisions)
	fmt.Fprintf(f, "candidate_ready_unique_event_times: `%d`\n\n", summary.CandidateReadyUniqueEventTimes)
	fmt.Fprintf(f, "candidate_ready_extracted_episodes: `%d`\n\n", summary.CandidateReadyExtractedEpisodes)
	fmt.Fprintf(f, "setup_detected_episodes: `%d`\n\n", summary.SetupDetectedEpisodes)
	fmt.Fprintf(f, "setup_invalidated_episodes: `%d`\n\n", summary.SetupInvalidatedEpisodes)
	fmt.Fprintf(f, "avg_orderbook_coverage_pct: `%.2f`\n\n", summary.AvgOrderbookCoveragePct)
	fmt.Fprintf(f, "candidate_rate: `%.4f`\n\n", summary.CandidateRate)
	fmt.Fprintf(f, "invalidation_rate: `%.4f`\n\n", summary.InvalidationRate)
	fmt.Fprintf(f, "offline_filter_rejected_candidate_count: `%d`\n\n", summary.OfflineFilterRejectedCandidateCount)
	fmt.Fprintf(f, "safety_constraints_passed: `%t`\n\n", summary.SafetyConstraintsPassed)
	fmt.Fprintf(f, "acceptance_recommendation: `%s`\n\n", acceptance.Recommendation)

	fmt.Fprintln(f, "## Top Main Blockers")
	fmt.Fprintln(f)
	writeCountTable(f, summary.TopMainBlockers)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Top Invalidation Reasons")
	fmt.Fprintln(f)
	writeCountTable(f, summary.TopInvalidationReasons)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## High-Score Rejected Evaluations")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "threshold: `%.3f`\n\n", summary.HighScoreRejectedThreshold)
	fmt.Fprintf(f, "score >= threshold and candidate_ready=false: `%d`\n\n", summary.HighScoreRejectedCount)
	fmt.Fprintln(f, "### By Reject And Invalidation Reason")
	fmt.Fprintln(f)
	writeHighScoreRejectedGroupTable(f, summary.HighScoreRejectedGroups)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "### Top 20 Examples")
	fmt.Fprintln(f)
	writeHighScoreRejectedExamplesTable(f, summary.HighScoreRejectedExamples)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Sensitivity Aggregation")
	for _, split := range []string{datasetSplitAll, datasetSplitTrain, datasetSplitValidation} {
		fmt.Fprintln(f)
		fmt.Fprintf(f, "### %s\n\n", split)
		writeSensitivityTable(f, summary.SensitivityAggregation[split])
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Offline Filter Rejected Candidate Reasons")
	for _, split := range []string{datasetSplitAll, datasetSplitTrain, datasetSplitValidation} {
		fmt.Fprintln(f)
		fmt.Fprintf(f, "### %s\n\n", split)
		writeCountTable(f, summary.OfflineFilterRejectReasons[split])
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Candidate Outcome Summary")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Offline-only TP/SL simulation over the 30 minute forward window. Parameters are not selected automatically.")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "candidate_outcome_count: `%d`\n\n", summary.CandidateOutcomeCount)
	fmt.Fprintf(f, "raw_market_candidate_count: `%d`\n\n", summary.RawMarketCandidateCount)
	fmt.Fprintf(f, "unique_market_candidate_count: `%d`\n\n", summary.UniqueMarketCandidateCount)
	fmt.Fprintf(f, "duplicate_candidate_count: `%d`\n\n", summary.DuplicateCandidateCount)
	fmt.Fprintln(f, "## Raw Candidate Outcome Summary")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Raw metrics preserve the extracted-episode candidate counts before market-time deduplication.")
	fmt.Fprintln(f)
	for _, split := range []string{datasetSplitAll, datasetSplitTrain, datasetSplitValidation} {
		fmt.Fprintf(f, "### %s\n\n", split)
		writeCandidateOutcomeTable(f, summary.CandidateOutcomeAggregation[split])
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, "## Deduplicated Market-Time Outcome Summary")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Deduplication key: symbol + event_time + sensitivity parameter set. When overlapping extracted episodes emit the same key, the canonical event is the lexicographically earliest source_file, with lowest record_index as the tie-breaker.")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "duplicate_candidate_count: `%d`\n\n", summary.DuplicateCandidateCount)
	for _, split := range []string{datasetSplitAll, datasetSplitTrain, datasetSplitValidation} {
		fmt.Fprintf(f, "### %s\n\n", split)
		writeDeduplicatedCandidateOutcomeTable(f, summary.CandidateOutcomeAggregation[split])
		fmt.Fprintln(f)
	}
	rankingOpts := validationOutcomeRankingOptions{
		Best:                                true,
		MinValidationUniqueMarketCandidates: acceptance.MinValidationUniqueMarketCandidates,
		MaxDuplicateCandidateRatio:          acceptance.MaxDuplicateCandidateRatio,
		SafetyConstraintsPass:               acceptance.SafetyConstraintsPass,
	}
	fmt.Fprintln(f, "## Best eligible parameter sets by validation avg_net_r")
	fmt.Fprintln(f)
	eligibleRankingOpts := rankingOpts
	eligibleRankingOpts.EligibleOnly = true
	writeValidationOutcomeRankingTable(f, summary.CandidateOutcomeAggregation[datasetSplitTrain], summary.CandidateOutcomeAggregation[datasetSplitValidation], eligibleRankingOpts)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Best raw parameter sets by validation avg_net_r")
	fmt.Fprintln(f)
	rawRankingOpts := rankingOpts
	rawRankingOpts.WarnLowValidationUniqueCandidates = true
	writeValidationOutcomeRankingTable(f, summary.CandidateOutcomeAggregation[datasetSplitTrain], summary.CandidateOutcomeAggregation[datasetSplitValidation], rawRankingOpts)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Worst parameter sets")
	fmt.Fprintln(f)
	worstRankingOpts := rankingOpts
	worstRankingOpts.Best = false
	writeValidationOutcomeRankingTable(f, summary.CandidateOutcomeAggregation[datasetSplitTrain], summary.CandidateOutcomeAggregation[datasetSplitValidation], worstRankingOpts)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Candidate examples")
	fmt.Fprintln(f)
	writeCandidateOutcomeExamplesTable(f, summary.CandidateOutcomeExamples)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Stable Train/Validation Parameters")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Informational only; parameters are not selected automatically.")
	if len(summary.StableSensitivityParameters) == 0 {
		fmt.Fprintln(f)
		fmt.Fprintln(f, "No candidate-producing parameter set was stable across train and validation.")
		return nil
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | train_candidate_rate | validation_candidate_rate | train_invalidation_rate | validation_invalidation_rate |")
	fmt.Fprintln(f, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: |")
	for _, item := range summary.StableSensitivityParameters {
		fmt.Fprintf(f, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %.4f | %.4f | %.4f | %.4f |\n",
			item.MinRSIDelta, item.NoNewLowBars, item.ScoreThreshold, item.MinVolumeRatio, item.SetupMemoryBars, item.InvalidationATRMult,
			item.RequireOrderbookNotHostile, item.MinOrderbookRatio, item.MinBarsWithoutNewLowAfterReclaim, item.RequireCloseAboveMiddleBB, item.RequireHigherLowAfterReclaim,
			item.TrainCandidateRate, item.ValidationCandidateRate, item.TrainInvalidationRate, item.ValidationInvalidationRate)
	}
	return nil
}

func writeDatasetAcceptanceReport(path string, acceptance datasetAcceptanceResult) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# Reversal Dataset Acceptance")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "- minimum episodes: %s (actual=%d threshold=%d)\n", passFail(acceptance.MinEpisodesPass), acceptance.ActualEpisodes, acceptance.MinEpisodes)
	fmt.Fprintf(f, "- orderbook coverage: %s (actual=%.4f threshold=%.4f)\n", passFail(acceptance.OrderbookCoveragePass), acceptance.ActualOrderbookCoverage, acceptance.MinOrderbookCoverage)
	fmt.Fprintf(f, "- candidate count: %s (actual=%d threshold=%d unit=%s)\n", passFail(acceptance.CandidateCountPass), acceptance.ActualCandidateEpisodes, acceptance.MinCandidateEpisodes, acceptance.CandidateCountUnit)
	if acceptance.ActualRawMarketCandidates > 0 || acceptance.ActualUniqueMarketCandidates > 0 {
		fmt.Fprintf(f, "- raw market candidates: %d\n", acceptance.ActualRawMarketCandidates)
		fmt.Fprintf(f, "- unique market candidates: %d\n", acceptance.ActualUniqueMarketCandidates)
		fmt.Fprintf(f, "- duplicate candidate count: %d\n", acceptance.DuplicateCandidateCount)
	}
	fmt.Fprintf(f, "- safety constraints: %s\n", passFail(acceptance.SafetyConstraintsPass))
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Outcome Gates")
	fmt.Fprintf(f, "- outcome enabled: %t\n", acceptance.OutcomeEnabled)
	fmt.Fprintf(f, "- validation unique candidates: %s (actual=%d threshold=%d)\n",
		acceptanceGateStatus(acceptance.OutcomeEnabled, acceptance.ValidationUniqueMarketCandidatesPass),
		acceptance.ActualValidationUniqueMarketCandidates,
		acceptance.MinValidationUniqueMarketCandidates)
	fmt.Fprintf(f, "- validation avg_net_r: %s (actual=%.4f threshold>%.4f)\n",
		acceptanceGateStatus(acceptance.OutcomeEnabled, acceptance.ValidationAvgNetRPass),
		acceptance.ActualValidationAvgNetR,
		acceptance.MinValidationAvgNetR)
	fmt.Fprintf(f, "- validation median_net_r: %s (actual=%.4f threshold>=%.4f)\n",
		acceptanceGateStatus(acceptance.OutcomeEnabled, acceptance.ValidationMedianNetRPass),
		acceptance.ActualValidationMedianNetR,
		acceptance.MinValidationMedianNetR)
	fmt.Fprintf(f, "- duplicate candidate ratio: %s (actual=%.4f threshold<=%.4f)\n",
		acceptanceGateStatus(acceptance.OutcomeEnabled, acceptance.DuplicateCandidateRatioPass),
		acceptance.DuplicateCandidateRatio,
		acceptance.MaxDuplicateCandidateRatio)
	fmt.Fprintf(f, "- train/validation avg_net_r sign: %s (train=%.4f validation=%.4f opposite_signs=%t)\n",
		acceptanceGateStatus(acceptance.OutcomeEnabled, acceptance.TrainValidationAvgNetRSignPass),
		acceptance.ActualTrainAvgNetR,
		acceptance.ActualValidationAvgNetR,
		acceptance.TrainValidationAvgNetROppositeSigns)
	if acceptance.OutcomeParameterSet != "" {
		fmt.Fprintf(f, "- outcome parameter set: `%s`\n", acceptance.OutcomeParameterSet)
	}
	if len(acceptance.SafetyViolations) > 0 {
		fmt.Fprintln(f)
		fmt.Fprintln(f, "## Safety Violations")
		for _, violation := range acceptance.SafetyViolations {
			fmt.Fprintf(f, "- %s\n", violation)
		}
	}
	fmt.Fprintln(f)
	fmt.Fprintf(f, "recommendation: `%s`\n", acceptance.Recommendation)
	return nil
}

func passFail(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

func acceptanceGateStatus(applied, pass bool) string {
	if !applied {
		return "N/A"
	}
	return passFail(pass)
}

func writeCountTable(w io.Writer, counts []datasetCount) {
	if len(counts) == 0 {
		fmt.Fprintln(w, "No entries.")
		return
	}
	fmt.Fprintln(w, "| value | count |")
	fmt.Fprintln(w, "| --- | ---: |")
	for _, item := range counts {
		fmt.Fprintf(w, "| %s | %d |\n", item.Value, item.Count)
	}
}

func writeHighScoreRejectedGroupTable(w io.Writer, groups []datasetHighScoreRejectedGroup) {
	if len(groups) == 0 {
		fmt.Fprintln(w, "No high-score rejected evaluations.")
		return
	}
	fmt.Fprintln(w, "| reject_reason | invalidation_reason | count |")
	fmt.Fprintln(w, "| --- | --- | ---: |")
	for _, item := range groups {
		fmt.Fprintf(w, "| %s | %s | %d |\n",
			markdownTableValue(item.RejectReason),
			markdownTableValue(item.InvalidationReason),
			item.Count)
	}
}

func writeHighScoreRejectedExamplesTable(w io.Writer, examples []datasetHighScoreRejectedExample) {
	if len(examples) == 0 {
		fmt.Fprintln(w, "No examples.")
		return
	}
	fmt.Fprintln(w, "| source_file | event_time | score | state | reject_reason | invalidation_reason | setup_invalidated | setup_age_bars | orderbook_ratio | spread_bps | orderbook_hostile | orderbook_reject_reason |")
	fmt.Fprintln(w, "| --- | --- | ---: | --- | --- | --- | --- | ---: | ---: | ---: | --- | --- |")
	for _, item := range examples {
		fmt.Fprintf(w, "| %s | %s | %.3f | %s | %s | %s | %t | %d | %.4f | %.4f | %t | %s |\n",
			markdownTableValue(item.SourceFile),
			markdownTableValue(item.EventTime),
			item.Score,
			markdownTableValue(item.State),
			markdownTableValue(item.RejectReason),
			markdownTableValue(item.InvalidationReason),
			item.SetupInvalidated,
			item.SetupAgeBars,
			item.OrderbookRatio,
			item.SpreadBps,
			item.OrderbookHostile,
			markdownTableValue(item.OrderbookRejectReason))
	}
}

func markdownTableValue(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	return value
}

func writeSensitivityTable(w io.Writer, items []datasetSensitivityAggregate) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No sensitivity results.")
		return
	}
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | episodes | candidate_episodes | raw_candidates | filtered_candidates | rejected_candidates | invalidated | candidate_rate | invalidation_rate | avg_max_score | orders_sent |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, item := range items {
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %d | %d | %d | %d | %d | %d | %.4f | %.4f | %.3f | %d |\n",
			item.MinRSIDelta, item.NoNewLowBars, item.ScoreThreshold, item.MinVolumeRatio, item.SetupMemoryBars, item.InvalidationATRMult,
			item.RequireOrderbookNotHostile, item.MinOrderbookRatio, item.MinBarsWithoutNewLowAfterReclaim, item.RequireCloseAboveMiddleBB, item.RequireHigherLowAfterReclaim,
			item.TotalEpisodes, item.CandidateReadyEpisodes, item.UnfilteredCandidateReadyCount, item.FilteredCandidateReadyCount, item.OfflineFilterRejectedCandidateCount,
			item.SetupInvalidatedEpisodes, item.CandidateRate, item.InvalidationRate, item.AvgMaxScore, item.OrdersSent)
	}
}

func writeCandidateOutcomeTable(w io.Writer, items []datasetCandidateOutcomeAggregate) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No candidate outcomes.")
		return
	}
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | tp_bps | sl_bps | fee_bps | slippage_bps | raw_candidate_count | raw_win_rate | raw_avg_net_r | raw_median_net_r | raw_avg_mfe_bps | raw_avg_mae_bps |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, item := range items {
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %.2f | %.2f | %.2f | %.2f | %d | %.4f | %.4f | %.4f | %.2f | %.2f |\n",
			item.MinRSIDelta, item.NoNewLowBars, item.ScoreThreshold, item.MinVolumeRatio, item.SetupMemoryBars, item.InvalidationATRMult,
			item.RequireOrderbookNotHostile, item.MinOrderbookRatio, item.MinBarsWithoutNewLowAfterReclaim, item.RequireCloseAboveMiddleBB, item.RequireHigherLowAfterReclaim,
			item.TPBps, item.SLBps, item.FeeBps, item.SlippageBps, item.RawCandidateCount, item.RawWinRate, item.RawAvgNetR, item.RawMedianNetR, item.AvgMFEBps, item.AvgMAEBps)
	}
}

func writeDeduplicatedCandidateOutcomeTable(w io.Writer, items []datasetCandidateOutcomeAggregate) {
	if len(items) == 0 {
		fmt.Fprintln(w, "No candidate outcomes.")
		return
	}
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | tp_bps | sl_bps | fee_bps | slippage_bps | raw_candidate_count | unique_market_candidate_count | duplicate_candidate_count | unique_market_win_rate | unique_market_avg_net_r | unique_market_median_net_r | unique_market_avg_mfe_bps | unique_market_avg_mae_bps |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, item := range items {
		duplicateCount := item.RawCandidateCount - item.UniqueMarketCandidateCount
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %.2f | %.2f | %.2f | %.2f | %d | %d | %d | %.4f | %.4f | %.4f | %.2f | %.2f |\n",
			item.MinRSIDelta, item.NoNewLowBars, item.ScoreThreshold, item.MinVolumeRatio, item.SetupMemoryBars, item.InvalidationATRMult,
			item.RequireOrderbookNotHostile, item.MinOrderbookRatio, item.MinBarsWithoutNewLowAfterReclaim, item.RequireCloseAboveMiddleBB, item.RequireHigherLowAfterReclaim,
			item.TPBps, item.SLBps, item.FeeBps, item.SlippageBps, item.RawCandidateCount, item.UniqueMarketCandidateCount, duplicateCount,
			item.UniqueMarketWinRate, item.UniqueMarketAvgNetR, item.UniqueMarketMedianNetR, item.UniqueMarketAvgMFEBps, item.UniqueMarketAvgMAEBps)
	}
}

func writeValidationOutcomeRankingTable(w io.Writer, train, validation []datasetCandidateOutcomeAggregate, opts validationOutcomeRankingOptions) {
	if len(validation) == 0 {
		fmt.Fprintln(w, "No validation candidate outcomes.")
		return
	}
	opts = normalizeValidationOutcomeRankingOptions(opts)
	if opts.EligibleOnly {
		fmt.Fprintf(w, "Eligibility: validation_unique_market_candidates >= `%d`, duplicate_candidate_ratio <= `%.4f`, safety_constraints_passed=`%t`.\n\n",
			opts.MinValidationUniqueMarketCandidates,
			opts.MaxDuplicateCandidateRatio,
			opts.SafetyConstraintsPass)
	}
	trainByKey := make(map[candidateOutcomeKey]datasetCandidateOutcomeAggregate, len(train))
	for _, item := range train {
		trainByKey[candidateOutcomeKeyFromAggregate(item)] = item
	}
	items := append([]datasetCandidateOutcomeAggregate(nil), validation...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].UniqueMarketAvgNetR != items[j].UniqueMarketAvgNetR {
			if opts.Best {
				return items[i].UniqueMarketAvgNetR > items[j].UniqueMarketAvgNetR
			}
			return items[i].UniqueMarketAvgNetR < items[j].UniqueMarketAvgNetR
		}
		if items[i].UniqueMarketCandidateCount != items[j].UniqueMarketCandidateCount {
			return items[i].UniqueMarketCandidateCount > items[j].UniqueMarketCandidateCount
		}
		return candidateOutcomeAggregateLess(items[i], items[j])
	})
	if opts.EligibleOnly {
		eligible := make([]datasetCandidateOutcomeAggregate, 0, len(items))
		if opts.SafetyConstraintsPass {
			for _, item := range items {
				if item.UniqueMarketCandidateCount >= opts.MinValidationUniqueMarketCandidates &&
					candidateOutcomeDuplicateRatio(item) <= opts.MaxDuplicateCandidateRatio {
					eligible = append(eligible, item)
				}
			}
		}
		items = eligible
		if len(items) == 0 {
			fmt.Fprintln(w, "No eligible validation candidate outcomes.")
			return
		}
	}
	if len(items) > 20 {
		items = items[:20]
	}
	if opts.WarnLowValidationUniqueCandidates {
		lowSampleCount := 0
		for _, item := range items {
			if item.UniqueMarketCandidateCount < opts.MinValidationUniqueMarketCandidates {
				lowSampleCount++
			}
		}
		if lowSampleCount > 0 {
			fmt.Fprintf(w, "Warning: %d of the top %d raw parameter sets have validation_unique_market_candidates below threshold %d. Prefer the eligible ranking for calibration decisions.\n\n",
				lowSampleCount,
				len(items),
				opts.MinValidationUniqueMarketCandidates)
		}
	}
	fmt.Fprintln(w, "| min_rsi_delta | no_new_low_bars | score_threshold | min_volume_ratio | setup_memory_bars | invalidation_atr_mult | require_ob_not_hostile | min_ob_ratio | min_bars_no_new_low_after_reclaim | require_close_above_mid_bb | require_higher_low_after_reclaim | tp_bps | sl_bps | validation_unique_market_candidates | validation_avg_net_r | validation_median_net_r | validation_avg_mfe_bps | validation_avg_mae_bps | train_avg_net_r | train_validation_avg_net_r_delta | duplicate_candidate_ratio | validation_win_rate | validation_raw_candidates | train_unique_market_candidates | train_avg_mfe_bps | train_avg_mae_bps | train_raw_candidates |")
	fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: | --- | ---: | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |")
	for _, item := range items {
		trainItem := trainByKey[candidateOutcomeKeyFromAggregate(item)]
		trainValidationAvgNetRDelta := trainItem.UniqueMarketAvgNetR - item.UniqueMarketAvgNetR
		fmt.Fprintf(w, "| %.3f | %d | %.3f | %.3f | %d | %.3f | %t | %.3f | %d | %t | %t | %.2f | %.2f | %d | %.4f | %.4f | %.2f | %.2f | %.4f | %.4f | %.4f | %.4f | %d | %d | %.2f | %.2f | %d |\n",
			item.MinRSIDelta, item.NoNewLowBars, item.ScoreThreshold, item.MinVolumeRatio, item.SetupMemoryBars, item.InvalidationATRMult,
			item.RequireOrderbookNotHostile, item.MinOrderbookRatio, item.MinBarsWithoutNewLowAfterReclaim, item.RequireCloseAboveMiddleBB, item.RequireHigherLowAfterReclaim,
			item.TPBps, item.SLBps, item.UniqueMarketCandidateCount, item.UniqueMarketAvgNetR, item.UniqueMarketMedianNetR,
			item.UniqueMarketAvgMFEBps, item.UniqueMarketAvgMAEBps, trainItem.UniqueMarketAvgNetR, trainValidationAvgNetRDelta,
			candidateOutcomeDuplicateRatio(item), item.UniqueMarketWinRate, item.RawCandidateCount, trainItem.UniqueMarketCandidateCount,
			trainItem.UniqueMarketAvgMFEBps, trainItem.UniqueMarketAvgMAEBps, trainItem.RawCandidateCount)
	}
}

func normalizeValidationOutcomeRankingOptions(opts validationOutcomeRankingOptions) validationOutcomeRankingOptions {
	if opts.MinValidationUniqueMarketCandidates <= 0 {
		opts.MinValidationUniqueMarketCandidates = defaultMinValidationUniqueMarketCandidates
	}
	if opts.MaxDuplicateCandidateRatio <= 0 {
		opts.MaxDuplicateCandidateRatio = defaultMaxDuplicateCandidateRatio
	}
	return opts
}

func candidateOutcomeDuplicateRatio(item datasetCandidateOutcomeAggregate) float64 {
	if item.RawCandidateCount <= 0 {
		return 0
	}
	duplicateCount := item.RawCandidateCount - item.UniqueMarketCandidateCount
	if duplicateCount <= 0 {
		return 0
	}
	return float64(duplicateCount) / float64(item.RawCandidateCount)
}

func writeCandidateOutcomeExamplesTable(w io.Writer, examples []datasetCandidateOutcomeExample) {
	if len(examples) == 0 {
		fmt.Fprintln(w, "No candidate outcome examples.")
		return
	}
	fmt.Fprintln(w, "| split | run_key | source_file | event_time | unique_candidate_key | tp_bps | sl_bps | fwd_5m_bps | fwd_15m_bps | fwd_30m_bps | mfe_30m_bps | mae_30m_bps | time_to_mfe | time_to_mae | hit_order | net_return_bps | net_r |")
	fmt.Fprintln(w, "| --- | --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- | ---: | ---: |")
	for _, item := range examples {
		fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %s | %s | %s | %.2f | %.4f |\n",
			markdownTableValue(item.Split),
			markdownTableValue(item.RunKey),
			markdownTableValue(item.SourceFile),
			markdownTableValue(item.EventTime),
			markdownTableValue(item.UniqueCandidateKey),
			item.TPBps,
			item.SLBps,
			item.ForwardReturn5mBps,
			item.ForwardReturn15mBps,
			item.ForwardReturn30mBps,
			item.MaxFavorable30mBps,
			item.MaxAdverse30mBps,
			markdownTableValue(item.TimeToMFE),
			markdownTableValue(item.TimeToMAE),
			markdownTableValue(item.HitOrder),
			item.NetReturnBps,
			item.NetR)
	}
}

func candidateOutcomeKeyFromAggregate(item datasetCandidateOutcomeAggregate) candidateOutcomeKey {
	return candidateOutcomeKey{
		sensitivityKey: sensitivityKey{
			MinRSIDelta:                      item.MinRSIDelta,
			NoNewLowBars:                     item.NoNewLowBars,
			ScoreThreshold:                   item.ScoreThreshold,
			MinVolumeRatio:                   item.MinVolumeRatio,
			SetupMemoryBars:                  item.SetupMemoryBars,
			InvalidationATRMult:              item.InvalidationATRMult,
			RequireOrderbookNotHostile:       item.RequireOrderbookNotHostile,
			MinOrderbookRatio:                item.MinOrderbookRatio,
			MinBarsWithoutNewLowAfterReclaim: item.MinBarsWithoutNewLowAfterReclaim,
			RequireCloseAboveMiddleBB:        item.RequireCloseAboveMiddleBB,
			RequireHigherLowAfterReclaim:     item.RequireHigherLowAfterReclaim,
		},
		TPBps:       item.TPBps,
		SLBps:       item.SLBps,
		FeeBps:      item.FeeBps,
		SlippageBps: item.SlippageBps,
	}
}
