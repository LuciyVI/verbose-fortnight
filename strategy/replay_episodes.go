package strategy

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"verbose-fortnight/config"
)

type ReversalEpisodeClass string

const (
	EpisodeTrueReversalCandidate        ReversalEpisodeClass = "true_reversal_candidate"
	EpisodeFalseReversalInvalidated     ReversalEpisodeClass = "false_reversal_invalidated"
	EpisodeNoSetup                      ReversalEpisodeClass = "no_setup"
	EpisodeOrderbookDataGap             ReversalEpisodeClass = "data_gap_orderbook_unavailable"
	reversalMainBlockerSetupInvalidated                      = "setup_invalidated"
)

type ReversalEpisodeSummary struct {
	EpisodeID                string               `json:"episode_id"`
	Classification           ReversalEpisodeClass `json:"classification"`
	Symbol                   string               `json:"symbol"`
	Timeframe                string               `json:"timeframe"`
	Start                    time.Time            `json:"start"`
	End                      time.Time            `json:"end"`
	SetupDetectedCount       int                  `json:"setup_detected_count"`
	SetupInvalidatedCount    int                  `json:"setup_invalidated_count"`
	CandidateReadyCount      int                  `json:"candidate_ready_count"`
	MaxScore                 float64              `json:"max_score"`
	MainBlocker              string               `json:"main_blocker"`
	MainBlockerDetail        string               `json:"main_blocker_detail"`
	MainInvalidationReason   string               `json:"main_invalidation_reason,omitempty"`
	FirstSetupDetectedAt     *time.Time           `json:"first_setup_detected_at,omitempty"`
	FirstInvalidationAt      *time.Time           `json:"first_invalidation_at,omitempty"`
	InvalidationAfterBars    int                  `json:"invalidation_after_bars,omitempty"`
	ReclaimWasRemembered     bool                 `json:"reclaim_was_remembered"`
	OrderbookCoveragePct     float64              `json:"orderbook_coverage_pct"`
	TargetCandles            int                  `json:"target_candles"`
	WarmupCandles            int                  `json:"warmup_candles"`
	ReversalEvaluationEvents int                  `json:"reversal_evaluation_events"`
}

type ReversalEpisodeDataset struct {
	Summary  ReversalEpisodeSummary      `json:"summary"`
	Timeline []ReversalReplayTimelineRow `json:"timeline"`
	Events   []map[string]any            `json:"events"`
}

type ReversalMultiEpisodeSensitivityResult struct {
	MinRSIDelta              float64 `json:"min_rsi_delta"`
	NoNewLowBars             int     `json:"no_new_low_bars"`
	ScoreThreshold           float64 `json:"score_threshold"`
	MinVolumeRatio           float64 `json:"min_volume_ratio"`
	SetupMemoryBars          int     `json:"setup_memory_bars"`
	InvalidationATRMult      float64 `json:"invalidation_atr_mult"`
	TotalEpisodes            int     `json:"total_episodes"`
	CandidateReadyEpisodes   int     `json:"candidate_ready_episodes"`
	FalseInvalidatedEpisodes int     `json:"false_invalidated_episodes"`
	AvgMaxScore              float64 `json:"avg_max_score"`
	OrderbookAvailablePct    float64 `json:"orderbook_available_pct"`
	CandidateRate            float64 `json:"candidate_rate"`
	InvalidationRate         float64 `json:"invalidation_rate"`
	OrdersSent               int     `json:"orders_sent"`
}

func BuildReversalEpisodeSummaries(result ReversalReplayResult, cfg config.ReversalLongConfig, setupOpts ReversalSetupMemoryOptions) []ReversalEpisodeSummary {
	analysis := BuildReversalBlockerAnalysis(result, cfg, setupOpts)
	rows := analysis.Timeline
	if len(rows) == 0 {
		return nil
	}
	if analysis.SetupDetectedCount == 0 {
		return []ReversalEpisodeSummary{buildNoSetupEpisode(result, analysis)}
	}

	var episodes []ReversalEpisodeSummary
	seen := map[time.Time]bool{}
	for i, row := range rows {
		if row.SetupDetectedAt == nil || !row.SetupDetectedAt.Equal(row.Time) || seen[row.Time] {
			continue
		}
		seen[row.Time] = true
		episode := buildSetupEpisode(result, rows, i, analysis)
		episodes = append(episodes, episode)
	}
	if len(episodes) == 0 {
		return []ReversalEpisodeSummary{buildNoSetupEpisode(result, analysis)}
	}
	return episodes
}

func buildNoSetupEpisode(result ReversalReplayResult, analysis ReversalBlockerAnalysis) ReversalEpisodeSummary {
	coverage := orderbookCoveragePct(analysis.Timeline)
	class := EpisodeNoSetup
	if coverage == 0 {
		class = EpisodeOrderbookDataGap
	}
	return ReversalEpisodeSummary{
		EpisodeID:                "episode_0001",
		Classification:           class,
		Symbol:                   result.Summary.Symbol,
		Timeframe:                "1m",
		Start:                    result.Summary.From,
		End:                      result.Summary.To,
		MaxScore:                 analysis.MaxScore,
		MainBlocker:              analysis.MainBlocker,
		MainBlockerDetail:        analysis.MainBlockerDetail,
		OrderbookCoveragePct:     coverage,
		TargetCandles:            len(analysis.Timeline),
		WarmupCandles:            result.Summary.WarmupCandles,
		ReversalEvaluationEvents: len(analysis.Timeline),
	}
}

func buildSetupEpisode(result ReversalReplayResult, rows []ReversalReplayTimelineRow, setupIndex int, analysis ReversalBlockerAnalysis) ReversalEpisodeSummary {
	setupTime := rows[setupIndex].Time
	episodeRows := []ReversalReplayTimelineRow{rows[setupIndex]}
	for i := setupIndex + 1; i < len(rows); i++ {
		row := rows[i]
		if row.SetupDetectedAt != nil && row.SetupDetectedAt.Equal(row.Time) {
			break
		}
		episodeRows = append(episodeRows, row)
		if row.SetupInvalidated || row.SetupMemoryCandidateReady || row.CandidateReady {
			break
		}
	}

	summary := ReversalEpisodeSummary{
		EpisodeID:                fmt.Sprintf("episode_%04d", setupIndex+1),
		Classification:           EpisodeOrderbookDataGap,
		Symbol:                   result.Summary.Symbol,
		Timeframe:                "1m",
		Start:                    episodeRows[0].Time,
		End:                      episodeRows[len(episodeRows)-1].Time,
		SetupDetectedCount:       1,
		MainBlocker:              analysis.MainBlocker,
		MainBlockerDetail:        analysis.MainBlockerDetail,
		FirstSetupDetectedAt:     timePtr(setupTime),
		OrderbookCoveragePct:     orderbookCoveragePct(episodeRows),
		TargetCandles:            len(episodeRows),
		WarmupCandles:            result.Summary.WarmupCandles,
		ReversalEvaluationEvents: len(episodeRows),
	}
	for _, row := range episodeRows {
		score := row.Score
		if row.SetupMemoryScore > score {
			score = row.SetupMemoryScore
		}
		if score > summary.MaxScore {
			summary.MaxScore = score
		}
		if row.CandidateReady || row.SetupMemoryCandidateReady {
			summary.CandidateReadyCount++
		}
		if row.SetupInvalidated {
			summary.SetupInvalidatedCount++
			if summary.FirstInvalidationAt == nil {
				summary.FirstInvalidationAt = timePtr(row.Time)
				summary.InvalidationAfterBars = row.SetupAgeBars
				summary.MainInvalidationReason = row.InvalidationReason
			}
		}
		if row.MemoryReclaimUsed {
			summary.ReclaimWasRemembered = true
		}
	}
	switch {
	case summary.CandidateReadyCount > 0:
		summary.Classification = EpisodeTrueReversalCandidate
	case summary.SetupInvalidatedCount > 0:
		summary.Classification = EpisodeFalseReversalInvalidated
		summary.MainBlocker = reversalMainBlockerSetupInvalidated
		summary.MainBlockerDetail = summary.MainInvalidationReason
	case summary.OrderbookCoveragePct == 0:
		summary.Classification = EpisodeOrderbookDataGap
	default:
		summary.Classification = EpisodeNoSetup
	}
	return summary
}

func orderbookCoveragePct(rows []ReversalReplayTimelineRow) float64 {
	if len(rows) == 0 {
		return 0
	}
	available := 0
	for _, row := range rows {
		if row.OrderbookAvailable {
			available++
		}
	}
	return float64(available) / float64(len(rows)) * 100
}

func WriteReversalEpisodeSummariesJSON(w io.Writer, episodes []ReversalEpisodeSummary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(episodes)
}

func BuildReversalEpisodeDataset(result ReversalReplayResult, cfg config.ReversalLongConfig, setupOpts ReversalSetupMemoryOptions) []ReversalEpisodeDataset {
	summaries := BuildReversalEpisodeSummaries(result, cfg, setupOpts)
	analysis := BuildReversalBlockerAnalysis(result, cfg, setupOpts)
	datasets := make([]ReversalEpisodeDataset, 0, len(summaries))
	for _, summary := range summaries {
		dataset := ReversalEpisodeDataset{Summary: summary}
		for _, row := range analysis.Timeline {
			if !row.Time.Before(summary.Start) && !row.Time.After(summary.End) {
				dataset.Timeline = append(dataset.Timeline, row)
			}
		}
		for _, event := range result.Events {
			ts := timeValue(event["time"])
			if ts.IsZero() {
				ts = timeValue(event["timestamp"])
			}
			if ts.IsZero() || (!ts.Before(summary.Start) && !ts.After(summary.End)) {
				dataset.Events = append(dataset.Events, event)
			}
		}
		datasets = append(datasets, dataset)
	}
	return datasets
}

func WriteReversalEpisodeDatasetJSON(w io.Writer, episodes []ReversalEpisodeDataset) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(episodes)
}

func WriteReversalEpisodeSummariesJSONL(w io.Writer, episodes []ReversalEpisodeSummary) error {
	enc := json.NewEncoder(w)
	for _, episode := range episodes {
		if err := enc.Encode(episode); err != nil {
			return err
		}
	}
	return nil
}

func RunReversalMultiEpisodeSensitivity(candles []ReversalReplayCandle, opts ReversalReplayOptions, spec ReversalSensitivityOptions) ([]ReversalMultiEpisodeSensitivityResult, error) {
	baseCfg := config.LoadConfig()
	if opts.Config != nil {
		cfgCopy := *opts.Config
		baseCfg = &cfgCopy
	}
	spec = normalizeSensitivityOptions(spec, baseCfg.ReversalLong)
	results := make([]ReversalMultiEpisodeSensitivityResult, 0, len(spec.MinRSIDeltaList)*len(spec.NoNewLowBarsList)*len(spec.ScoreThresholdList)*len(spec.MinVolumeRatioList)*len(spec.SetupMemoryBarsList)*len(spec.InvalidationATRMultList))
	for _, minRSIDelta := range spec.MinRSIDeltaList {
		for _, noNewLowBars := range spec.NoNewLowBarsList {
			for _, scoreThreshold := range spec.ScoreThresholdList {
				for _, minVolumeRatio := range spec.MinVolumeRatioList {
					for _, setupMemoryBars := range spec.SetupMemoryBarsList {
						for _, invalidationATRMult := range spec.InvalidationATRMultList {
							cfgCopy := *baseCfg
							cfgCopy.ReversalLong.MinRSIDelta = minRSIDelta
							cfgCopy.ReversalLong.NoNewLowBars = noNewLowBars
							cfgCopy.ReversalLong.ScoreThreshold = scoreThreshold
							cfgCopy.ReversalLong.MinVolumeRatio = minVolumeRatio
							runOpts := opts
							runOpts.Config = &cfgCopy
							runOpts.OrdersEnabled = false
							result, err := RunReversalReplayCandles(candles, runOpts)
							if err != nil {
								return nil, err
							}
							episodes := BuildReversalEpisodeSummaries(result, cfgCopy.ReversalLong, ReversalSetupMemoryOptions{
								SetupMemoryBars:     setupMemoryBars,
								InvalidationATRMult: invalidationATRMult,
							})
							aggregate := AggregateReversalMultiEpisodeSensitivity(episodes)
							aggregate.MinRSIDelta = minRSIDelta
							aggregate.NoNewLowBars = noNewLowBars
							aggregate.ScoreThreshold = scoreThreshold
							aggregate.MinVolumeRatio = minVolumeRatio
							aggregate.SetupMemoryBars = setupMemoryBars
							aggregate.InvalidationATRMult = invalidationATRMult
							aggregate.OrdersSent = result.Summary.OrdersSent
							results = append(results, aggregate)
						}
					}
				}
			}
		}
	}
	return results, nil
}

func AggregateReversalMultiEpisodeSensitivity(episodes []ReversalEpisodeSummary) ReversalMultiEpisodeSensitivityResult {
	var out ReversalMultiEpisodeSensitivityResult
	out.TotalEpisodes = len(episodes)
	if len(episodes) == 0 {
		return out
	}
	var maxScoreSum, coverageSum float64
	for _, episode := range episodes {
		maxScoreSum += episode.MaxScore
		coverageSum += episode.OrderbookCoveragePct
		switch episode.Classification {
		case EpisodeTrueReversalCandidate:
			out.CandidateReadyEpisodes++
		case EpisodeFalseReversalInvalidated:
			out.FalseInvalidatedEpisodes++
		}
	}
	out.AvgMaxScore = maxScoreSum / float64(len(episodes))
	out.OrderbookAvailablePct = coverageSum / float64(len(episodes))
	out.CandidateRate = float64(out.CandidateReadyEpisodes) / float64(len(episodes))
	out.InvalidationRate = float64(out.FalseInvalidatedEpisodes) / float64(len(episodes))
	return out
}

func WriteReversalMultiEpisodeSensitivityJSONL(w io.Writer, results []ReversalMultiEpisodeSensitivityResult) error {
	enc := json.NewEncoder(w)
	for _, result := range results {
		if err := enc.Encode(result); err != nil {
			return err
		}
	}
	return nil
}
