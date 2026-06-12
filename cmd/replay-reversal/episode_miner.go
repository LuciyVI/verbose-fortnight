package main

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"verbose-fortnight/indicators"
	"verbose-fortnight/strategy"
)

const (
	miningReasonLowerBBReclaimZone   = "lower_bb_reclaim_zone"
	miningReasonRSIOversoldZone      = "rsi_oversold_zone"
	miningReasonFalseReversalZone    = "false_reversal_zone"
	miningReasonTrueReversalLikeZone = "true_reversal_like_zone"
	miningReasonWeakReboundZone      = "weak_rebound_zone"
	miningReasonNoSetupControlWindow = "no_setup_control_window"

	defaultMineWarmupCandles = 219
	defaultMineWindowBefore  = "60"
	defaultMineWindowAfter   = "30"
	defaultMineMinSpacing    = 5

	defaultMineRSIOversold            = 30.0
	defaultMineMinForwardFavorableBps = 30.0
	defaultMineMaxForwardAdverseBps   = 20.0
	defaultMineMinBBTouchCount        = 1
)

type episodeMiningOptions struct {
	InputCSV                string
	OutManifest             string
	Symbol                  string
	Timeframe               string
	WindowBefore            string
	WindowAfter             string
	MinSpacingBars          int
	WarmupCandles           int
	AllowInsufficientWarmup bool
	RSIOversold             float64
	MinForwardFavorableBps  float64
	MaxForwardAdverseBps    float64
	MinBBTouchCount         int
	MaxPerClass             int
	MinControlWindows       int
	IncludeControls         bool
	RunDatasetAfter         bool
	DatasetRunID            string
	DatasetOutRoot          string
	DatasetRunOptions       datasetRunnerOptions
}

type episodeMiningOutput struct {
	ManifestPath string
	SummaryPath  string
	ReportPath   string
	Summary      episodeMiningSummary
}

type minedManifestEpisode struct {
	ID              string               `json:"id"`
	Symbol          string               `json:"symbol"`
	Timeframe       string               `json:"timeframe"`
	InputCSV        string               `json:"input_csv"`
	Start           string               `json:"start"`
	End             string               `json:"end"`
	Warmup          int                  `json:"warmup"`
	MinedReason     string               `json:"mined_reason"`
	MinedAnchorTime string               `json:"mined_anchor_time"`
	MinedFeatures   minedEpisodeFeatures `json:"mined_features"`
}

type minedEpisodeFeatures struct {
	RSIMin                 float64 `json:"rsi_min"`
	BBTouchCount           int     `json:"bb_touch_count"`
	ReclaimSeen            bool    `json:"reclaim_seen"`
	SubsequentLowerLow     bool    `json:"subsequent_lower_low"`
	ForwardReturnBps       float64 `json:"forward_return_bps"`
	ForwardMaxAdverseBps   float64 `json:"forward_max_adverse_bps"`
	ForwardMaxFavorableBps float64 `json:"forward_max_favorable_bps"`
	WarmupRequired         int     `json:"warmup_required"`
	WarmupAvailable        int     `json:"warmup_available"`
	WarmupInsufficient     bool    `json:"warmup_insufficient"`
}

type episodeMiningSummary struct {
	TotalWindowsScanned            int            `json:"total_windows_scanned"`
	EpisodesMined                  int            `json:"episodes_mined"`
	ReasonsDistribution            map[string]int `json:"reasons_distribution"`
	ClassDistributionBeforeDedup   map[string]int `json:"class_distribution_before_dedup"`
	ClassDistributionAfterDedup    map[string]int `json:"class_distribution_after_dedup"`
	DeduplicatedCount              int            `json:"deduplicated_count"`
	WarmupInsufficientCount        int            `json:"warmup_insufficient_count"`
	SkippedInsufficientWarmupCount int            `json:"skipped_insufficient_warmup_count"`
	EarliestValidAnchorTime        string         `json:"earliest_valid_anchor_time,omitempty"`
	CSVStartTime                   string         `json:"csv_start_time,omitempty"`
	CSVEndTime                     string         `json:"csv_end_time,omitempty"`
	TrueReversalLikeCount          int            `json:"true_reversal_like_count"`
	FalseReversalLikeCount         int            `json:"false_reversal_like_count"`
	NoSetupControlCount            int            `json:"no_setup_control_count"`
	OutputManifest                 string         `json:"output_manifest"`
	MiningReport                   string         `json:"mining_report"`
	DatasetRunAfterMine            bool           `json:"run_dataset_after_mine"`
	DatasetRunID                   string         `json:"dataset_run_id,omitempty"`
	DatasetOutputDir               string         `json:"dataset_output_dir,omitempty"`
	DatasetAcceptance              string         `json:"dataset_acceptance,omitempty"`
	DatasetOrdersSent              int            `json:"dataset_orders_sent,omitempty"`
	DatasetSafetyPass              bool           `json:"dataset_safety_pass,omitempty"`
	TotalExtractedEpisodes         int            `json:"total_extracted_episodes,omitempty"`
	CandidateReadyEpisodes         int            `json:"candidate_ready_episodes,omitempty"`
	SetupInvalidatedEpisodes       int            `json:"setup_invalidated_episodes,omitempty"`
}

type miningCandidate struct {
	ID         string
	AnchorIdx  int
	AnchorTime time.Time
	Start      time.Time
	End        time.Time
	Reason     string
	Features   minedEpisodeFeatures
	Score      float64
}

type episodeMiningScanResult struct {
	Candidates                     []miningCandidate
	TotalWindowsScanned            int
	WarmupInsufficientCount        int
	SkippedInsufficientWarmupCount int
	CSVStartTime                   time.Time
	CSVEndTime                     time.Time
	EarliestValidAnchorTime        time.Time
	ClassDistributionBeforeDedup   map[string]int
	ClassDistributionAfterDedup    map[string]int
	DeduplicatedCount              int
}

func runEpisodeMining(opts episodeMiningOptions) (episodeMiningOutput, error) {
	if opts.InputCSV == "" {
		return episodeMiningOutput{}, fmt.Errorf("--mine-input-csv is required")
	}
	if opts.OutManifest == "" {
		return episodeMiningOutput{}, fmt.Errorf("--mine-out-manifest is required")
	}
	opts = normalizeEpisodeMiningOptions(opts)
	if !opts.IncludeControls {
		opts.MinControlWindows = 0
	}
	if opts.MaxPerClass < 0 {
		opts.MaxPerClass = 0
	}
	if opts.MinControlWindows < 0 {
		opts.MinControlWindows = 0
	}

	candles, err := strategy.LoadReversalReplayCandlesCSV(opts.InputCSV)
	if err != nil {
		return episodeMiningOutput{}, fmt.Errorf("load mine input: %w", err)
	}
	before, err := parseWarmupDuration(opts.WindowBefore, opts.Timeframe)
	if err != nil {
		return episodeMiningOutput{}, fmt.Errorf("--mine-window-before: %w", err)
	}
	after, err := parseWarmupDuration(opts.WindowAfter, opts.Timeframe)
	if err != nil {
		return episodeMiningOutput{}, fmt.Errorf("--mine-window-after: %w", err)
	}
	scan := mineReversalEpisodeCandidates(candles, opts, before, after)
	candidates, deduped := deduplicateMiningCandidates(scan.Candidates, opts.MinSpacingBars)
	scan.DeduplicatedCount = deduped
	scan.ClassDistributionAfterDedup = miningReasonDistribution(candidates)
	candidates = selectBalancedMiningCandidates(candidates, opts)

	episodes, err := minedManifestEpisodes(candidates, opts)
	if err != nil {
		return episodeMiningOutput{}, err
	}
	if err := writeMinedManifest(opts.OutManifest, episodes); err != nil {
		return episodeMiningOutput{}, fmt.Errorf("write mined manifest: %w", err)
	}

	reportPath := filepath.Join(filepath.Dir(opts.OutManifest), "mining_report.md")
	summaryPath := filepath.Join(filepath.Dir(opts.OutManifest), "mining_summary.json")
	summary := buildEpisodeMiningSummary(scan, candidates, opts.OutManifest, reportPath)
	if err := writeEpisodeMiningSummary(summaryPath, summary); err != nil {
		return episodeMiningOutput{}, fmt.Errorf("write mining summary: %w", err)
	}
	if err := writeEpisodeMiningReport(reportPath, summary); err != nil {
		return episodeMiningOutput{}, fmt.Errorf("write mining report: %w", err)
	}
	return episodeMiningOutput{
		ManifestPath: opts.OutManifest,
		SummaryPath:  summaryPath,
		ReportPath:   reportPath,
		Summary:      summary,
	}, nil
}

func normalizeEpisodeMiningOptions(opts episodeMiningOptions) episodeMiningOptions {
	if opts.Symbol == "" {
		opts.Symbol = "BTCUSDT"
	}
	if opts.Timeframe == "" {
		opts.Timeframe = "1m"
	}
	if strings.TrimSpace(opts.WindowBefore) == "" {
		opts.WindowBefore = defaultMineWindowBefore
	}
	if strings.TrimSpace(opts.WindowAfter) == "" {
		opts.WindowAfter = defaultMineWindowAfter
	}
	if opts.MinSpacingBars <= 0 {
		opts.MinSpacingBars = defaultMineMinSpacing
	}
	if opts.WarmupCandles <= 0 {
		opts.WarmupCandles = defaultMineWarmupCandles
	}
	if opts.RSIOversold <= 0 {
		opts.RSIOversold = defaultMineRSIOversold
	}
	if opts.MinForwardFavorableBps <= 0 {
		opts.MinForwardFavorableBps = defaultMineMinForwardFavorableBps
	}
	if opts.MaxForwardAdverseBps <= 0 {
		opts.MaxForwardAdverseBps = defaultMineMaxForwardAdverseBps
	}
	if opts.MinBBTouchCount <= 0 {
		opts.MinBBTouchCount = defaultMineMinBBTouchCount
	}
	return opts
}

func runEpisodeMiningAndDataset(opts episodeMiningOptions) (episodeMiningOutput, datasetRunOutput, error) {
	mining, err := runEpisodeMining(opts)
	if err != nil {
		return episodeMiningOutput{}, datasetRunOutput{}, err
	}
	datasetOpts := opts.DatasetRunOptions
	datasetOpts.ManifestPath = mining.ManifestPath
	datasetOpts.RunID = opts.DatasetRunID
	datasetOpts.OutRoot = opts.DatasetOutRoot
	output, err := runDatasetRunner(datasetOpts)
	if err != nil {
		return mining, datasetRunOutput{}, err
	}
	mining.Summary.DatasetRunAfterMine = true
	mining.Summary.DatasetRunID = output.RunID
	mining.Summary.DatasetOutputDir = output.RunDir
	mining.Summary.DatasetAcceptance = output.Acceptance.Recommendation
	mining.Summary.DatasetOrdersSent = output.Summary.OrdersSent
	mining.Summary.DatasetSafetyPass = output.Summary.SafetyConstraintsPassed
	mining.Summary.TotalExtractedEpisodes = output.Summary.TotalEpisodes
	mining.Summary.CandidateReadyEpisodes = output.Summary.CandidateReadyEpisodes
	mining.Summary.SetupInvalidatedEpisodes = output.Summary.SetupInvalidatedEpisodes
	if err := writeEpisodeMiningSummary(mining.SummaryPath, mining.Summary); err != nil {
		return mining, output, fmt.Errorf("update mining summary: %w", err)
	}
	if err := writeEpisodeMiningReport(mining.ReportPath, mining.Summary); err != nil {
		return mining, output, fmt.Errorf("update mining report: %w", err)
	}
	return mining, output, nil
}

func mineReversalEpisodeCandidates(candles []strategy.ReversalReplayCandle, opts episodeMiningOptions, before, after time.Duration) episodeMiningScanResult {
	result := episodeMiningScanResult{
		ClassDistributionBeforeDedup: map[string]int{},
		ClassDistributionAfterDedup:  map[string]int{},
	}
	if len(candles) < 30 {
		return result
	}
	result.CSVStartTime = candles[0].Time
	result.CSVEndTime = candles[len(candles)-1].Time
	tf, err := parseTimeframeDuration(opts.Timeframe)
	if err != nil || tf <= 0 {
		tf = time.Minute
	}
	forwardBars := barsForDuration(after, tf)
	if forwardBars <= 0 {
		forwardBars = 1
	}
	beforeBars := barsForDuration(before, tf)
	closes := make([]float64, len(candles))
	highs := make([]float64, len(candles))
	lows := make([]float64, len(candles))
	for i, candle := range candles {
		closes[i] = candle.Close
		highs[i] = candle.High
		lows[i] = candle.Low
	}
	rsiSeries := indicators.RSI(closes, 14)
	bbLower := make([]float64, len(candles))
	for i := range candles {
		_, _, lower := indicators.CalculateBollingerBands(closes[:i+1], 20, 2.0)
		bbLower[i] = lower
	}

	controlStride := opts.MinSpacingBars * 3
	if controlStride < 10 {
		controlStride = 10
	}
	startIdx := 20
	if beforeBars > startIdx {
		startIdx = beforeBars
	}
	for i := startIdx; i < len(candles)-forwardBars; i++ {
		if result.EarliestValidAnchorTime.IsZero() && warmupAvailableBars(candles, candles[i].Time.Add(-before)) >= opts.WarmupCandles {
			result.EarliestValidAnchorTime = candles[i].Time
		}
		if closes[i] <= 0 || bbLower[i] <= 0 {
			continue
		}
		result.TotalWindowsScanned++
		start := candles[i].Time.Add(-before)
		if start.Before(candles[0].Time) {
			start = candles[0].Time
		}
		warmupAvailable := warmupAvailableBars(candles, start)
		features := minedFeaturesAt(i, candles, closes, highs, lows, rsiSeries, bbLower, forwardBars)
		features.WarmupRequired = opts.WarmupCandles
		features.WarmupAvailable = warmupAvailable
		features.WarmupInsufficient = warmupAvailable < opts.WarmupCandles
		reason := classifyMiningReasonWithOptions(features, opts)
		setup := reason != miningReasonNoSetupControlWindow
		if !setup && !opts.IncludeControls {
			continue
		}
		if !setup && i%controlStride != 0 {
			continue
		}
		if !setup && features.RSIMin <= 45 {
			continue
		}
		if features.WarmupInsufficient {
			result.WarmupInsufficientCount++
			if !opts.AllowInsufficientWarmup {
				result.SkippedInsufficientWarmupCount++
				continue
			}
		}
		end := candles[i].Time.Add(after)
		last := candles[len(candles)-1].Time
		if end.After(last) {
			end = last
		}
		candidate := miningCandidate{
			AnchorIdx:  i,
			AnchorTime: candles[i].Time,
			Start:      start,
			End:        end,
			Reason:     reason,
			Features:   features,
			Score:      miningCandidateScore(reason, features),
		}
		result.Candidates = append(result.Candidates, candidate)
		result.ClassDistributionBeforeDedup[reason]++
	}
	return result
}

func barsForDuration(d, timeframe time.Duration) int {
	if d <= 0 || timeframe <= 0 {
		return 0
	}
	return int(math.Ceil(float64(d) / float64(timeframe)))
}

func warmupAvailableBars(candles []strategy.ReversalReplayCandle, start time.Time) int {
	return sort.Search(len(candles), func(i int) bool {
		return !candles[i].Time.Before(start)
	})
}

func minedFeaturesAt(i int, candles []strategy.ReversalReplayCandle, closes, highs, lows, rsiSeries, bbLower []float64, forwardBars int) minedEpisodeFeatures {
	recentStart := i - 5
	if recentStart < 0 {
		recentStart = 0
	}
	rsiMin := 100.0
	if len(rsiSeries) > 0 {
		for j := recentStart; j <= i && j < len(rsiSeries); j++ {
			if rsiSeries[j] > 0 && rsiSeries[j] < rsiMin {
				rsiMin = rsiSeries[j]
			}
		}
	}
	bbTouchCount := 0
	for j := recentStart; j <= i; j++ {
		if bbLower[j] > 0 && (lows[j] <= bbLower[j] || closes[j] <= bbLower[j]) {
			bbTouchCount++
		}
	}
	reclaimSeen := false
	if bbLower[i] > 0 && closes[i] > bbLower[i] {
		for j := i - 3; j < i; j++ {
			if j >= 0 && bbLower[j] > 0 && (lows[j] <= bbLower[j] || closes[j] <= bbLower[j]) {
				reclaimSeen = true
				break
			}
		}
	}
	end := i + forwardBars
	if end >= len(candles) {
		end = len(candles) - 1
	}
	anchorClose := closes[i]
	minFutureLow := lows[i]
	maxFutureHigh := highs[i]
	for j := i + 1; j <= end; j++ {
		if lows[j] < minFutureLow {
			minFutureLow = lows[j]
		}
		if highs[j] > maxFutureHigh {
			maxFutureHigh = highs[j]
		}
	}
	forwardReturn := 0.0
	forwardAdverse := 0.0
	forwardFavorable := 0.0
	if anchorClose > 0 {
		forwardReturn = (closes[end] - anchorClose) / anchorClose * 10000
		forwardAdverse = (minFutureLow - anchorClose) / anchorClose * 10000
		forwardFavorable = (maxFutureHigh - anchorClose) / anchorClose * 10000
	}
	return minedEpisodeFeatures{
		RSIMin:                 roundFloat(rsiMin, 4),
		BBTouchCount:           bbTouchCount,
		ReclaimSeen:            reclaimSeen,
		SubsequentLowerLow:     minFutureLow < lows[i],
		ForwardReturnBps:       roundFloat(forwardReturn, 4),
		ForwardMaxAdverseBps:   roundFloat(forwardAdverse, 4),
		ForwardMaxFavorableBps: roundFloat(forwardFavorable, 4),
	}
}

func classifyMiningReason(features minedEpisodeFeatures) string {
	return classifyMiningReasonWithOptions(features, normalizeEpisodeMiningOptions(episodeMiningOptions{}))
}

func classifyMiningReasonWithOptions(features minedEpisodeFeatures, opts episodeMiningOptions) string {
	setup := features.RSIMin <= opts.RSIOversold || features.BBTouchCount > 0 || features.ReclaimSeen
	if !setup {
		if features.ForwardMaxFavorableBps >= opts.MinForwardFavorableBps && features.ForwardReturnBps > 0 {
			return miningReasonWeakReboundZone
		}
		return miningReasonNoSetupControlWindow
	}
	if features.SubsequentLowerLow && features.ForwardMaxAdverseBps <= -opts.MaxForwardAdverseBps {
		return miningReasonFalseReversalZone
	}
	strongSetup := features.RSIMin <= opts.RSIOversold || (features.BBTouchCount >= opts.MinBBTouchCount && features.ReclaimSeen)
	controlledAdverse := features.ForwardMaxAdverseBps >= -opts.MaxForwardAdverseBps
	if strongSetup &&
		controlledAdverse &&
		!features.SubsequentLowerLow &&
		features.ForwardMaxFavorableBps >= opts.MinForwardFavorableBps &&
		features.ForwardReturnBps > 0 {
		return miningReasonTrueReversalLikeZone
	}
	if features.ForwardMaxFavorableBps >= opts.MinForwardFavorableBps && features.ForwardReturnBps > 0 {
		return miningReasonWeakReboundZone
	}
	if features.ReclaimSeen {
		return miningReasonLowerBBReclaimZone
	}
	if features.RSIMin <= opts.RSIOversold {
		return miningReasonRSIOversoldZone
	}
	return miningReasonLowerBBReclaimZone
}

func miningCandidateScore(reason string, features minedEpisodeFeatures) float64 {
	if reason == miningReasonNoSetupControlWindow {
		return 1 + math.Min(math.Abs(features.ForwardReturnBps)/100, 1)
	}
	score := float64(features.BBTouchCount) * 8
	if features.RSIMin < 30 {
		score += (30 - features.RSIMin) * 2
	}
	if features.ReclaimSeen {
		score += 25
	}
	if features.SubsequentLowerLow {
		score += 15 + math.Min(math.Abs(features.ForwardMaxAdverseBps)/5, 40)
	}
	if reason == miningReasonTrueReversalLikeZone {
		score += 20 + math.Min(features.ForwardMaxFavorableBps/5, 40)
	}
	return score
}

func deduplicateMiningCandidates(candidates []miningCandidate, minSpacingBars int) ([]miningCandidate, int) {
	if minSpacingBars < 0 {
		minSpacingBars = 0
	}
	sorted := append([]miningCandidate(nil), candidates...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Score == sorted[j].Score {
			return sorted[i].AnchorTime.Before(sorted[j].AnchorTime)
		}
		return sorted[i].Score > sorted[j].Score
	})
	selected := make([]miningCandidate, 0, len(sorted))
	for _, candidate := range sorted {
		tooClose := false
		for _, kept := range selected {
			if absInt(candidate.AnchorIdx-kept.AnchorIdx) < minSpacingBars {
				tooClose = true
				break
			}
		}
		if tooClose {
			continue
		}
		selected = append(selected, candidate)
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].AnchorTime.Before(selected[j].AnchorTime)
	})
	for i := range selected {
		selected[i].ID = fmt.Sprintf("%s_%s_%04d", selected[i].Reason, selected[i].AnchorTime.UTC().Format("20060102T150405Z"), i+1)
	}
	return selected, len(candidates) - len(selected)
}

func selectBalancedMiningCandidates(candidates []miningCandidate, opts episodeMiningOptions) []miningCandidate {
	if opts.MaxPerClass <= 0 && opts.MinControlWindows <= 0 {
		return candidates
	}
	byReason := map[string][]miningCandidate{}
	for _, candidate := range candidates {
		if candidate.Reason == miningReasonNoSetupControlWindow && !opts.IncludeControls {
			continue
		}
		byReason[candidate.Reason] = append(byReason[candidate.Reason], candidate)
	}
	var selected []miningCandidate
	for reason, group := range byReason {
		sort.Slice(group, func(i, j int) bool {
			if group[i].Score == group[j].Score {
				return group[i].AnchorTime.Before(group[j].AnchorTime)
			}
			return group[i].Score > group[j].Score
		})
		limit := len(group)
		if opts.MaxPerClass > 0 && opts.MaxPerClass < limit {
			limit = opts.MaxPerClass
		}
		if reason == miningReasonNoSetupControlWindow && opts.MinControlWindows > limit {
			limit = opts.MinControlWindows
			if limit > len(group) {
				limit = len(group)
			}
		}
		selected = append(selected, group[:limit]...)
	}
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].AnchorTime.Before(selected[j].AnchorTime)
	})
	return selected
}

func miningReasonDistribution(candidates []miningCandidate) map[string]int {
	out := map[string]int{}
	for _, candidate := range candidates {
		out[candidate.Reason]++
	}
	return out
}

func minedManifestEpisodes(candidates []miningCandidate, opts episodeMiningOptions) ([]minedManifestEpisode, error) {
	inputPath, err := filepath.Abs(opts.InputCSV)
	if err != nil {
		return nil, err
	}
	episodes := make([]minedManifestEpisode, 0, len(candidates))
	for _, candidate := range candidates {
		episodes = append(episodes, minedManifestEpisode{
			ID:              candidate.ID,
			Symbol:          opts.Symbol,
			Timeframe:       opts.Timeframe,
			InputCSV:        inputPath,
			Start:           candidate.Start.UTC().Format(time.RFC3339),
			End:             candidate.End.UTC().Format(time.RFC3339),
			Warmup:          opts.WarmupCandles,
			MinedReason:     candidate.Reason,
			MinedAnchorTime: candidate.AnchorTime.UTC().Format(time.RFC3339),
			MinedFeatures:   candidate.Features,
		})
	}
	return episodes, nil
}

func writeMinedManifest(path string, episodes []minedManifestEpisode) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(episodes)
}

func buildEpisodeMiningSummary(scan episodeMiningScanResult, candidates []miningCandidate, manifestPath, reportPath string) episodeMiningSummary {
	summary := episodeMiningSummary{
		TotalWindowsScanned:            scan.TotalWindowsScanned,
		EpisodesMined:                  len(candidates),
		ReasonsDistribution:            map[string]int{},
		ClassDistributionBeforeDedup:   scan.ClassDistributionBeforeDedup,
		ClassDistributionAfterDedup:    scan.ClassDistributionAfterDedup,
		DeduplicatedCount:              scan.DeduplicatedCount,
		WarmupInsufficientCount:        scan.WarmupInsufficientCount,
		SkippedInsufficientWarmupCount: scan.SkippedInsufficientWarmupCount,
		OutputManifest:                 manifestPath,
		MiningReport:                   reportPath,
	}
	if !scan.CSVStartTime.IsZero() {
		summary.CSVStartTime = scan.CSVStartTime.UTC().Format(time.RFC3339)
	}
	if !scan.CSVEndTime.IsZero() {
		summary.CSVEndTime = scan.CSVEndTime.UTC().Format(time.RFC3339)
	}
	if !scan.EarliestValidAnchorTime.IsZero() {
		summary.EarliestValidAnchorTime = scan.EarliestValidAnchorTime.UTC().Format(time.RFC3339)
	}
	for _, candidate := range candidates {
		summary.ReasonsDistribution[candidate.Reason]++
		switch candidate.Reason {
		case miningReasonTrueReversalLikeZone:
			summary.TrueReversalLikeCount++
		case miningReasonFalseReversalZone:
			summary.FalseReversalLikeCount++
		case miningReasonNoSetupControlWindow:
			summary.NoSetupControlCount++
		}
	}
	return summary
}

func writeEpisodeMiningSummary(path string, summary episodeMiningSummary) error {
	return writeJSONFile(path, summary)
}

func writeEpisodeMiningReport(path string, summary episodeMiningSummary) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# Reversal Episode Mining Report")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "total_windows_scanned: `%d`\n\n", summary.TotalWindowsScanned)
	fmt.Fprintf(f, "episodes_mined: `%d`\n\n", summary.EpisodesMined)
	fmt.Fprintf(f, "deduplicated_count: `%d`\n\n", summary.DeduplicatedCount)
	fmt.Fprintf(f, "warmup_insufficient_count: `%d`\n\n", summary.WarmupInsufficientCount)
	fmt.Fprintf(f, "skipped_insufficient_warmup_count: `%d`\n\n", summary.SkippedInsufficientWarmupCount)
	fmt.Fprintf(f, "csv_start_time: `%s`\n\n", summary.CSVStartTime)
	fmt.Fprintf(f, "csv_end_time: `%s`\n\n", summary.CSVEndTime)
	fmt.Fprintf(f, "earliest_valid_anchor_time: `%s`\n\n", summary.EarliestValidAnchorTime)
	fmt.Fprintf(f, "true_reversal_like_count: `%d`\n\n", summary.TrueReversalLikeCount)
	fmt.Fprintf(f, "false_reversal_like_count: `%d`\n\n", summary.FalseReversalLikeCount)
	fmt.Fprintf(f, "no_setup_control_count: `%d`\n\n", summary.NoSetupControlCount)
	fmt.Fprintf(f, "output_manifest: `%s`\n\n", summary.OutputManifest)
	if summary.DatasetRunAfterMine {
		fmt.Fprintf(f, "dataset_run_id: `%s`\n\n", summary.DatasetRunID)
		fmt.Fprintf(f, "dataset_output_dir: `%s`\n\n", summary.DatasetOutputDir)
		fmt.Fprintf(f, "dataset_acceptance: `%s`\n\n", summary.DatasetAcceptance)
		fmt.Fprintf(f, "dataset_orders_sent: `%d`\n\n", summary.DatasetOrdersSent)
		fmt.Fprintf(f, "dataset_safety_pass: `%t`\n\n", summary.DatasetSafetyPass)
		fmt.Fprintf(f, "total_extracted_episodes: `%d`\n\n", summary.TotalExtractedEpisodes)
	}
	fmt.Fprintln(f, "## Class Distribution Before Dedup")
	fmt.Fprintln(f)
	writeMiningReasonDistribution(f, summary.ClassDistributionBeforeDedup)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Class Distribution After Dedup")
	fmt.Fprintln(f)
	writeMiningReasonDistribution(f, summary.ClassDistributionAfterDedup)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "## Reasons")
	fmt.Fprintln(f)
	writeMiningReasonDistribution(f, summary.ReasonsDistribution)
	return nil
}

func writeMiningReasonDistribution(w interface {
	Write([]byte) (int, error)
}, distribution map[string]int) {
	if len(distribution) == 0 {
		fmt.Fprintln(w, "No entries.")
		return
	}
	fmt.Fprintln(w, "| reason | count |")
	fmt.Fprintln(w, "| --- | ---: |")
	reasons := make([]string, 0, len(distribution))
	for reason := range distribution {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(w, "| %s | %d |\n", reason, distribution[reason])
	}
}

func roundFloat(value float64, places int) float64 {
	if places <= 0 {
		return math.Round(value)
	}
	multiplier := math.Pow10(places)
	return math.Round(value*multiplier) / multiplier
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
