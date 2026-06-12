package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/strategy"
)

func main() {
	symbol := flag.String("symbol", "BTCUSDT", "symbol to replay")
	fromRaw := flag.String("from", "", "inclusive replay start time, RFC3339")
	toRaw := flag.String("to", "", "inclusive replay end time, RFC3339")
	input := flag.String("input", "", "CSV input with time,open,high,low,close,volume and optional best_bid,best_ask,best_bid_size,best_ask_size")
	out := flag.String("out", "", "optional JSONL output path; summary is printed to stdout")
	blockerReport := flag.String("blocker-report", "", "optional markdown blocker/sensitivity report path")
	episodesOut := flag.String("episodes-out", "", "optional JSON summary of extracted reversal episodes")
	multiEpisodeOut := flag.String("multi-episode-out", "", "optional JSONL aggregate multi-episode sensitivity output")
	datasetManifest := flag.String("dataset-manifest", "", "optional JSON/YAML/CSV manifest for offline dataset replay runs")
	datasetRunID := flag.String("dataset-run-id", "", "optional dataset run id; defaults to current UTC timestamp")
	datasetOutRoot := flag.String("dataset-out-root", filepath.Join("artifacts", "replay", "dataset"), "dataset output root")
	minEpisodes := flag.Int("min-episodes", 50, "minimum extracted episodes required for dataset acceptance")
	minOrderbookCoverage := flag.Float64("min-orderbook-coverage", 0.80, "minimum average orderbook coverage for dataset acceptance; 0.80 means 80%")
	minCandidateEpisodes := flag.Int("min-candidate-episodes", 10, "minimum candidate-ready episodes required for dataset acceptance")
	minValidationUniqueMarketCandidates := flag.Int("min-validation-unique-market-candidates", defaultMinValidationUniqueMarketCandidates, "minimum deduplicated validation market-time outcome candidates required for dataset acceptance")
	minValidationAvgNetR := flag.Float64("min-validation-avg-net-r", defaultMinValidationAvgNetR, "minimum validation avg_net_r for outcome-aware dataset acceptance; pass requires actual > threshold")
	minValidationMedianNetR := flag.Float64("min-validation-median-net-r", defaultMinValidationMedianNetR, "minimum validation median_net_r for outcome-aware dataset acceptance; pass requires actual >= threshold")
	maxDuplicateCandidateRatio := flag.Float64("max-duplicate-candidate-ratio", defaultMaxDuplicateCandidateRatio, "maximum duplicate candidate ratio for outcome-aware dataset acceptance")
	trainRatio := flag.Float64("train-ratio", 0.70, "time-ordered train split ratio when --validation-start is not set")
	validationStartRaw := flag.String("validation-start", "", "optional RFC3339 start time for validation split")
	validateOrderbookJSONLPath := flag.String("validate-orderbook-jsonl", "", "validate orderbook_diagnostic_snapshot JSONL for replay enrichment")
	enrichOrderbook := flag.Bool("enrich-orderbook", false, "enrich replay CSV candles with real orderbook diagnostic snapshots")
	enrichInputCSV := flag.String("enrich-input-csv", "", "input candle CSV for --enrich-orderbook")
	enrichOrderbookJSONL := flag.String("enrich-orderbook-jsonl", "", "JSONL orderbook_diagnostic_snapshot events for --enrich-orderbook")
	enrichOutputCSV := flag.String("enrich-output-csv", "", "output enriched replay CSV for --enrich-orderbook")
	enrichSymbol := flag.String("enrich-symbol", "BTCUSDT", "symbol filter for orderbook enrichment snapshots")
	enrichMaxStalenessMS := flag.Int("enrich-max-staleness-ms", 1500, "maximum age in milliseconds for previous orderbook snapshot enrichment")
	mineEpisodes := flag.Bool("mine-episodes", false, "mine reversal dataset episode manifest from a replay CSV")
	mineInputCSV := flag.String("mine-input-csv", "", "CSV input for --mine-episodes")
	mineOutManifest := flag.String("mine-out-manifest", "", "output JSON dataset manifest for --mine-episodes")
	mineSymbol := flag.String("mine-symbol", "BTCUSDT", "symbol written to mined manifest entries")
	mineTimeframe := flag.String("mine-timeframe", "1m", "timeframe written to mined manifest entries")
	mineWindowBefore := flag.String("mine-window-before", defaultMineWindowBefore, "duration or candle count before mined anchor")
	mineWindowAfter := flag.String("mine-window-after", defaultMineWindowAfter, "duration or candle count after mined anchor")
	mineMinSpacingBars := flag.Int("mine-min-spacing-bars", defaultMineMinSpacing, "minimum bars between mined anchors after deduplication")
	mineAllowInsufficientWarmup := flag.Bool("mine-allow-insufficient-warmup", false, "include mined episodes whose CSV lacks the requested warmup before start")
	mineRSIOversold := flag.Float64("mine-rsi-oversold", defaultMineRSIOversold, "RSI threshold for oversold mining features")
	mineMinForwardFavorableBps := flag.Float64("mine-min-forward-favorable-bps", defaultMineMinForwardFavorableBps, "minimum forward favorable move for true-reversal-like mining")
	mineMaxForwardAdverseBps := flag.Float64("mine-max-forward-adverse-bps", defaultMineMaxForwardAdverseBps, "maximum forward adverse move, in positive bps, for true-reversal-like mining")
	mineMinBBTouchCount := flag.Int("mine-min-bb-touch-count", defaultMineMinBBTouchCount, "minimum lower Bollinger Band touches for BB-based true-reversal-like mining")
	mineMaxPerClass := flag.Int("mine-max-per-class", 0, "maximum mined episodes per class after deduplication; 0 means unlimited")
	mineMinControlWindows := flag.Int("mine-min-control-windows", 0, "minimum no_setup_control_window samples to keep when available")
	mineIncludeControls := flag.Bool("mine-include-controls", true, "include no_setup_control_window samples")
	runDatasetAfterMine := flag.Bool("run-dataset-after-mine", false, "run dataset runner immediately after mining")
	reversalEnabled := flag.Bool("reversal-enabled", false, "enable reversal_long detector")
	reversalTradeEnabled := flag.Bool("reversal-trade-enabled", false, "allow reversal_long candidates into dry-run signal pipeline")
	ordersEnabled := flag.Bool("orders-enabled", false, "must remain false; replay never sends live orders")
	sensitivity := flag.Bool("sensitivity", false, "run offline sensitivity matrix instead of normal event JSONL")
	sensitivityMinRSIDeltaList := flag.String("sensitivity-min-rsi-delta-list", "", "comma-separated MinRSIDelta values for offline sensitivity")
	sensitivityNoNewLowBarsList := flag.String("sensitivity-no-new-low-bars-list", "", "comma-separated NoNewLowBars values for offline sensitivity")
	sensitivityScoreThresholdList := flag.String("sensitivity-score-threshold-list", "", "comma-separated ScoreThreshold values for offline sensitivity")
	sensitivityMinVolumeRatioList := flag.String("sensitivity-min-volume-ratio-list", "", "comma-separated MinVolumeRatio values for offline sensitivity")
	setupMemoryBars := flag.Int("setup-memory-bars", 0, "replay/blocker setup memory bars; sensitivity uses --sensitivity-setup-memory-bars-list, or this value when the list is omitted")
	invalidationATRMult := flag.Float64("invalidation-atr-mult", 0, "replay/blocker setup invalidation threshold in ATR multiples; sensitivity uses --sensitivity-invalidation-atr-mult-list, or this value when the list is omitted")
	sensitivitySetupMemoryBarsList := flag.String("sensitivity-setup-memory-bars-list", "", "comma-separated replay-only SetupMemoryBars values for sensitivity")
	sensitivityInvalidationATRMultList := flag.String("sensitivity-invalidation-atr-mult-list", "", "comma-separated replay-only InvalidationATRMult values for sensitivity")
	sensitivityRequireOrderbookNotHostileList := flag.String("sensitivity-require-orderbook-not-hostile-list", "", "comma-separated replay-only require_orderbook_not_hostile values for sensitivity")
	sensitivityMinOrderbookRatioList := flag.String("sensitivity-min-orderbook-ratio-list", "", "comma-separated replay-only min_orderbook_ratio values for sensitivity")
	sensitivityMinBarsWithoutNewLowAfterReclaimList := flag.String("sensitivity-min-bars-without-new-low-after-reclaim-list", "", "comma-separated replay-only min_bars_without_new_low_after_reclaim values for sensitivity")
	sensitivityRequireCloseAboveMiddleBBList := flag.String("sensitivity-require-close-above-middle-bb-list", "", "comma-separated replay-only require_close_above_middle_bb values for sensitivity")
	sensitivityRequireHigherLowAfterReclaimList := flag.String("sensitivity-require-higher-low-after-reclaim-list", "", "comma-separated replay-only require_higher_low_after_reclaim values for sensitivity")
	outcomeTPBpsList := flag.String("outcome-tp-bps-list", "", "comma-separated offline TP bps values for sensitivity outcome evaluation; default is 50")
	outcomeSLBpsList := flag.String("outcome-sl-bps-list", "", "comma-separated offline SL bps values for sensitivity outcome evaluation; default is 30")
	outcomeFeeBps := flag.Float64("outcome-fee-bps", 0, "offline round-trip fee assumption in bps for sensitivity outcome evaluation")
	outcomeSlippageBps := flag.Float64("outcome-slippage-bps", 0, "offline round-trip slippage assumption in bps for sensitivity outcome evaluation")
	flag.Parse()

	if err := validateReplaySafety(*ordersEnabled); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *validateOrderbookJSONLPath != "" {
		summary, err := validateOrderbookJSONL(*validateOrderbookJSONLPath, "")
		if err != nil {
			fmt.Fprintf(os.Stderr, "validate orderbook jsonl: %v\n", err)
			os.Exit(1)
		}
		writeOrderbookValidationSummary(os.Stdout, summary)
		return
	}
	if *enrichOrderbook {
		output, err := runOrderbookEnrichment(orderbookEnrichmentOptions{
			InputCSV:       *enrichInputCSV,
			OrderbookJSONL: *enrichOrderbookJSONL,
			OutputCSV:      *enrichOutputCSV,
			Symbol:         *enrichSymbol,
			MaxStaleness:   time.Duration(*enrichMaxStalenessMS) * time.Millisecond,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "enrich orderbook: %v\n", err)
			os.Exit(1)
		}
		writeEnrichmentSummary(os.Stdout, output)
		return
	}
	sensitivitySpec, err := parseSensitivityFlags(
		*sensitivityMinRSIDeltaList,
		*sensitivityNoNewLowBarsList,
		*sensitivityScoreThresholdList,
		*sensitivityMinVolumeRatioList,
		*sensitivitySetupMemoryBarsList,
		*sensitivityInvalidationATRMultList,
		*sensitivityRequireOrderbookNotHostileList,
		*sensitivityMinOrderbookRatioList,
		*sensitivityMinBarsWithoutNewLowAfterReclaimList,
		*sensitivityRequireCloseAboveMiddleBBList,
		*sensitivityRequireHigherLowAfterReclaimList,
		*outcomeTPBpsList,
		*outcomeSLBpsList,
		*outcomeFeeBps,
		*outcomeSlippageBps,
		*setupMemoryBars,
		*invalidationATRMult,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid sensitivity flags: %v\n", err)
		os.Exit(2)
	}
	validationStart, err := parseOptionalTime(*validationStartRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --validation-start: %v\n", err)
		os.Exit(2)
	}
	if *mineEpisodes {
		runID := *datasetRunID
		if *runDatasetAfterMine && runID == "" {
			runID = time.Now().UTC().Format("20060102T150405Z")
		}
		outManifest := *mineOutManifest
		if outManifest == "" {
			if *runDatasetAfterMine {
				outManifest = filepath.Join(*datasetOutRoot, sanitizePathPart(runID), "mined_manifest.json")
			} else {
				outManifest = filepath.Join("artifacts", "replay", "mined_manifest.json")
			}
		}
		miningOpts := episodeMiningOptions{
			InputCSV:                *mineInputCSV,
			OutManifest:             outManifest,
			Symbol:                  *mineSymbol,
			Timeframe:               *mineTimeframe,
			WindowBefore:            *mineWindowBefore,
			WindowAfter:             *mineWindowAfter,
			MinSpacingBars:          *mineMinSpacingBars,
			WarmupCandles:           defaultMineWarmupCandles,
			AllowInsufficientWarmup: *mineAllowInsufficientWarmup,
			RSIOversold:             *mineRSIOversold,
			MinForwardFavorableBps:  *mineMinForwardFavorableBps,
			MaxForwardAdverseBps:    *mineMaxForwardAdverseBps,
			MinBBTouchCount:         *mineMinBBTouchCount,
			MaxPerClass:             *mineMaxPerClass,
			MinControlWindows:       *mineMinControlWindows,
			IncludeControls:         *mineIncludeControls,
			RunDatasetAfter:         *runDatasetAfterMine,
			DatasetRunID:            runID,
			DatasetOutRoot:          *datasetOutRoot,
			DatasetRunOptions: datasetRunnerOptions{
				RunID:                               runID,
				OutRoot:                             *datasetOutRoot,
				ReversalEnabled:                     *reversalEnabled,
				ReversalTradeEnabled:                *reversalTradeEnabled,
				SetupMemoryBars:                     *setupMemoryBars,
				InvalidationATRMult:                 *invalidationATRMult,
				SensitivitySpec:                     sensitivitySpec,
				MinEpisodes:                         *minEpisodes,
				MinOrderbookCoverage:                *minOrderbookCoverage,
				MinCandidateEpisodes:                *minCandidateEpisodes,
				MinValidationUniqueMarketCandidates: *minValidationUniqueMarketCandidates,
				MinValidationAvgNetR:                *minValidationAvgNetR,
				MinValidationMedianNetR:             *minValidationMedianNetR,
				MaxDuplicateCandidateRatio:          *maxDuplicateCandidateRatio,
				TrainRatio:                          *trainRatio,
				ValidationStart:                     validationStart,
			},
		}
		if *runDatasetAfterMine {
			mining, output, err := runEpisodeMiningAndDataset(miningOpts)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mine episodes and run dataset: %v\n", err)
				os.Exit(1)
			}
			printMiningSummary(mining)
			fmt.Println("Dataset summary:")
			fmt.Printf("- run_id: %s\n", output.RunID)
			fmt.Printf("- output_dir: %s\n", output.RunDir)
			fmt.Printf("- total_episodes: %d\n", output.Summary.TotalEpisodes)
			fmt.Printf("- candidate_ready_episodes: %d\n", output.Summary.CandidateReadyEpisodes)
			fmt.Printf("- avg_orderbook_coverage_pct: %.2f\n", output.Summary.AvgOrderbookCoveragePct)
			fmt.Printf("- acceptance: %s\n", output.Acceptance.Recommendation)
			fmt.Printf("- summary: %s\n", output.SummaryPath)
			fmt.Printf("- report: %s\n", output.ReportPath)
			fmt.Printf("- acceptance_report: %s\n", output.AcceptancePath)
			fmt.Printf("- episodes_index: %s\n", output.EpisodesIndexPath)
			return
		}
		mining, err := runEpisodeMining(miningOpts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mine episodes: %v\n", err)
			os.Exit(1)
		}
		printMiningSummary(mining)
		return
	}
	if *datasetManifest != "" {
		output, err := runDatasetRunner(datasetRunnerOptions{
			ManifestPath:                        *datasetManifest,
			RunID:                               *datasetRunID,
			OutRoot:                             *datasetOutRoot,
			ReversalEnabled:                     *reversalEnabled,
			ReversalTradeEnabled:                *reversalTradeEnabled,
			SetupMemoryBars:                     *setupMemoryBars,
			InvalidationATRMult:                 *invalidationATRMult,
			SensitivitySpec:                     sensitivitySpec,
			MinEpisodes:                         *minEpisodes,
			MinOrderbookCoverage:                *minOrderbookCoverage,
			MinCandidateEpisodes:                *minCandidateEpisodes,
			MinValidationUniqueMarketCandidates: *minValidationUniqueMarketCandidates,
			MinValidationAvgNetR:                *minValidationAvgNetR,
			MinValidationMedianNetR:             *minValidationMedianNetR,
			MaxDuplicateCandidateRatio:          *maxDuplicateCandidateRatio,
			TrainRatio:                          *trainRatio,
			ValidationStart:                     validationStart,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "run dataset: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Dataset summary:")
		fmt.Printf("- run_id: %s\n", output.RunID)
		fmt.Printf("- output_dir: %s\n", output.RunDir)
		fmt.Printf("- total_episodes: %d\n", output.Summary.TotalEpisodes)
		fmt.Printf("- candidate_ready_episodes: %d\n", output.Summary.CandidateReadyEpisodes)
		fmt.Printf("- avg_orderbook_coverage_pct: %.2f\n", output.Summary.AvgOrderbookCoveragePct)
		fmt.Printf("- acceptance: %s\n", output.Acceptance.Recommendation)
		fmt.Printf("- summary: %s\n", output.SummaryPath)
		fmt.Printf("- report: %s\n", output.ReportPath)
		fmt.Printf("- acceptance_report: %s\n", output.AcceptancePath)
		fmt.Printf("- episodes_index: %s\n", output.EpisodesIndexPath)
		return
	}
	if *input == "" {
		fmt.Fprintln(os.Stderr, "--input is required")
		os.Exit(2)
	}
	from, err := parseOptionalTime(*fromRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --from: %v\n", err)
		os.Exit(2)
	}
	to, err := parseOptionalTime(*toRaw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --to: %v\n", err)
		os.Exit(2)
	}

	candles, err := strategy.LoadReversalReplayCandlesCSV(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load input: %v\n", err)
		os.Exit(1)
	}
	opts := strategy.ReversalReplayOptions{
		Symbol:               *symbol,
		From:                 from,
		To:                   to,
		ReversalEnabled:      *reversalEnabled,
		ReversalTradeEnabled: *reversalTradeEnabled,
		OrdersEnabled:        false,
	}
	if *sensitivity {
		results, err := strategy.RunReversalReplaySensitivity(candles, opts, sensitivitySpec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "run sensitivity: %v\n", err)
			os.Exit(1)
		}
		if err := writeSensitivityOutputs(*out, *blockerReport, results); err != nil {
			fmt.Fprintf(os.Stderr, "write sensitivity output: %v\n", err)
			os.Exit(1)
		}
		if *multiEpisodeOut != "" {
			multiResults, err := strategy.RunReversalMultiEpisodeSensitivity(candles, opts, sensitivitySpec)
			if err != nil {
				fmt.Fprintf(os.Stderr, "run multi-episode sensitivity: %v\n", err)
				os.Exit(1)
			}
			if err := writeMultiEpisodeSensitivityOutput(*multiEpisodeOut, multiResults); err != nil {
				fmt.Fprintf(os.Stderr, "write multi-episode sensitivity output: %v\n", err)
				os.Exit(1)
			}
		}
		printSensitivitySummary(results)
		return
	}

	result, err := strategy.RunReversalReplayCandles(candles, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run replay: %v\n", err)
		os.Exit(1)
	}

	if *out != "" {
		if dir := filepath.Dir(*out); dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintf(os.Stderr, "create --out directory: %v\n", err)
				os.Exit(1)
			}
		}
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create --out: %v\n", err)
			os.Exit(1)
		}
		if err := strategy.WriteReversalReplayJSONL(f, result); err != nil {
			f.Close()
			fmt.Fprintf(os.Stderr, "write events: %v\n", err)
			os.Exit(1)
		}
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close --out: %v\n", err)
			os.Exit(1)
		}
	} else {
		if err := strategy.WriteReversalReplayJSONL(os.Stdout, result); err != nil {
			fmt.Fprintf(os.Stderr, "write events: %v\n", err)
			os.Exit(1)
		}
	}
	if *blockerReport != "" {
		cfg := config.LoadConfig()
		cfg.ReversalLong.Enabled = *reversalEnabled
		cfg.ReversalLong.TradeEnabled = *reversalTradeEnabled
		setupOpts := strategy.ReversalSetupMemoryOptions{SetupMemoryBars: *setupMemoryBars, InvalidationATRMult: *invalidationATRMult}
		if err := writeBlockerReport(*blockerReport, result, cfg.ReversalLong, setupOpts); err != nil {
			fmt.Fprintf(os.Stderr, "write blocker report: %v\n", err)
			os.Exit(1)
		}
	}
	if *episodesOut != "" {
		cfg := config.LoadConfig()
		cfg.ReversalLong.Enabled = *reversalEnabled
		cfg.ReversalLong.TradeEnabled = *reversalTradeEnabled
		setupOpts := strategy.ReversalSetupMemoryOptions{SetupMemoryBars: *setupMemoryBars, InvalidationATRMult: *invalidationATRMult}
		episodes := strategy.BuildReversalEpisodeDataset(result, cfg.ReversalLong, setupOpts)
		if err := writeEpisodeDataset(*episodesOut, episodes); err != nil {
			fmt.Fprintf(os.Stderr, "write episodes output: %v\n", err)
			os.Exit(1)
		}
	}
	s := result.Summary
	fmt.Println("Replay summary:")
	fmt.Printf("- symbol: %s\n", s.Symbol)
	fmt.Printf("- from/to: %s / %s\n", s.From.Format(time.RFC3339), s.To.Format(time.RFC3339))
	fmt.Printf("- candles_loaded: %d\n", s.CandlesLoaded)
	fmt.Printf("- warmup_candles: %d\n", s.WarmupCandles)
	fmt.Printf("- target_candles: %d\n", s.TargetCandles)
	fmt.Printf("- candles_processed: %d\n", s.CandlesProcessed)
	fmt.Printf("- ordinary_long_candidates: %d\n", s.OrdinaryLongCandidates)
	fmt.Printf("- ordinary_short_candidates: %d\n", s.OrdinaryShortCandidates)
	fmt.Printf("- ordinary_signals_emitted: %d\n", s.OrdinarySignalsEmitted)
	fmt.Printf("- reversal_evaluations: %d\n", s.ReversalEvaluations)
	fmt.Printf("- reversal_candidate_ready_count: %d\n", s.ReversalCandidateReadyCount)
	fmt.Printf("- reversal_pipeline_allowed_count: %d\n", s.ReversalPipelineAllowedCount)
	fmt.Printf("- reversal_signals_emitted_dry_run: %d\n", s.ReversalSignalsEmittedDryRun)
	fmt.Printf("- runtime_reversal_pass_count: %d\n", s.RuntimeReversalPassCount)
	fmt.Printf("- runtime_reversal_block_count: %d\n", s.RuntimeReversalBlockCount)
	fmt.Printf("- orders_sent: %d\n", s.OrdersSent)
	fmt.Printf("- blockers: %v\n", s.Blockers)
}

func parseSensitivityFlags(
	minRSIRaw, noNewLowRaw, scoreRaw, volumeRaw, setupMemoryRaw, invalidationATRRaw string,
	requireOrderbookNotHostileRaw, minOrderbookRatioRaw, minBarsWithoutNewLowAfterReclaimRaw, requireCloseAboveMiddleBBRaw, requireHigherLowAfterReclaimRaw string,
	outcomeTPRaw, outcomeSLRaw string,
	outcomeFeeBps, outcomeSlippageBps float64,
	setupMemorySingle int,
	invalidationATRSingle float64,
) (strategy.ReversalSensitivityOptions, error) {
	if strings.TrimSpace(setupMemoryRaw) == "" && setupMemorySingle != 0 {
		setupMemoryRaw = strconv.Itoa(setupMemorySingle)
	}
	if strings.TrimSpace(invalidationATRRaw) == "" && invalidationATRSingle != 0 {
		invalidationATRRaw = strconv.FormatFloat(invalidationATRSingle, 'f', -1, 64)
	}
	minRSI, err := parseFloatList(minRSIRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-min-rsi-delta-list: %w", err)
	}
	noNewLow, err := parseIntList(noNewLowRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-no-new-low-bars-list: %w", err)
	}
	score, err := parseFloatList(scoreRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-score-threshold-list: %w", err)
	}
	volume, err := parseFloatList(volumeRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-min-volume-ratio-list: %w", err)
	}
	setupMemoryBars, err := parseIntList(setupMemoryRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-setup-memory-bars-list: %w", err)
	}
	invalidationATRMult, err := parseFloatList(invalidationATRRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-invalidation-atr-mult-list: %w", err)
	}
	requireOrderbookNotHostile, err := parseBoolList(requireOrderbookNotHostileRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-require-orderbook-not-hostile-list: %w", err)
	}
	minOrderbookRatio, err := parseFloatList(minOrderbookRatioRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-min-orderbook-ratio-list: %w", err)
	}
	minBarsWithoutNewLowAfterReclaim, err := parseIntList(minBarsWithoutNewLowAfterReclaimRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-min-bars-without-new-low-after-reclaim-list: %w", err)
	}
	requireCloseAboveMiddleBB, err := parseBoolList(requireCloseAboveMiddleBBRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-require-close-above-middle-bb-list: %w", err)
	}
	requireHigherLowAfterReclaim, err := parseBoolList(requireHigherLowAfterReclaimRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("sensitivity-require-higher-low-after-reclaim-list: %w", err)
	}
	outcomeTPBps, err := parseFloatList(outcomeTPRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("outcome-tp-bps-list: %w", err)
	}
	outcomeSLBps, err := parseFloatList(outcomeSLRaw)
	if err != nil {
		return strategy.ReversalSensitivityOptions{}, fmt.Errorf("outcome-sl-bps-list: %w", err)
	}
	return strategy.ReversalSensitivityOptions{
		MinRSIDeltaList:                      minRSI,
		NoNewLowBarsList:                     noNewLow,
		ScoreThresholdList:                   score,
		MinVolumeRatioList:                   volume,
		SetupMemoryBarsList:                  setupMemoryBars,
		InvalidationATRMultList:              invalidationATRMult,
		RequireOrderbookNotHostileList:       requireOrderbookNotHostile,
		MinOrderbookRatioList:                minOrderbookRatio,
		MinBarsWithoutNewLowAfterReclaimList: minBarsWithoutNewLowAfterReclaim,
		RequireCloseAboveMiddleBBList:        requireCloseAboveMiddleBB,
		RequireHigherLowAfterReclaimList:     requireHigherLowAfterReclaim,
		OutcomeTPBpsList:                     outcomeTPBps,
		OutcomeSLBpsList:                     outcomeSLBps,
		OutcomeFeeBps:                        outcomeFeeBps,
		OutcomeSlippageBps:                   outcomeSlippageBps,
	}, nil
}

func parseFloatList(raw string) ([]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]float64, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func parseIntList(raw string) ([]int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func parseBoolList(raw string) ([]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	values := make([]bool, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseBool(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func writeSensitivityOutputs(outPath, reportPath string, results []strategy.ReversalSensitivityResult) error {
	if outPath != "" {
		f, err := createOutputFile(outPath)
		if err != nil {
			return err
		}
		if err := strategy.WriteReversalSensitivityJSONL(f, results); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	} else {
		if err := strategy.WriteReversalSensitivityJSONL(os.Stdout, results); err != nil {
			return err
		}
	}
	if reportPath != "" {
		f, err := createOutputFile(reportPath)
		if err != nil {
			return err
		}
		if err := strategy.WriteReversalSensitivityMarkdown(f, results); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

func writeMultiEpisodeSensitivityOutput(path string, results []strategy.ReversalMultiEpisodeSensitivityResult) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	if err := strategy.WriteReversalMultiEpisodeSensitivityJSONL(f, results); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeEpisodeDataset(path string, episodes []strategy.ReversalEpisodeDataset) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	if err := strategy.WriteReversalEpisodeDatasetJSON(f, episodes); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeBlockerReport(path string, result strategy.ReversalReplayResult, cfg config.ReversalLongConfig, setupOpts strategy.ReversalSetupMemoryOptions) error {
	f, err := createOutputFile(path)
	if err != nil {
		return err
	}
	if err := strategy.WriteReversalBlockerMarkdown(f, result, cfg, setupOpts); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func validateReplaySafety(ordersEnabled bool) error {
	if ordersEnabled {
		return fmt.Errorf("orders-enabled=true is not supported by replay-reversal; refusing to run")
	}
	return nil
}

func createOutputFile(path string) (*os.File, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return os.Create(path)
}

func printSensitivitySummary(results []strategy.ReversalSensitivityResult) {
	candidateCombos := 0
	var first *strategy.ReversalSensitivityResult
	for i := range results {
		if results[i].CandidateReadyCount > 0 {
			candidateCombos++
			if first == nil {
				first = &results[i]
			}
		}
	}
	fmt.Println("Sensitivity summary:")
	fmt.Printf("- combinations: %d\n", len(results))
	fmt.Printf("- combinations_with_candidate_ready: %d\n", candidateCombos)
	if first != nil && first.FirstCandidateTime != nil {
		fmt.Printf("- first_candidate_combo: min_rsi_delta=%.3f no_new_low_bars=%d score_threshold=%.3f min_volume_ratio=%.3f setup_memory_bars=%d invalidation_atr_mult=%.3f first_candidate_time=%s\n",
			first.MinRSIDelta, first.NoNewLowBars, first.ScoreThreshold, first.MinVolumeRatio, first.SetupMemoryBars, first.InvalidationATRMult, first.FirstCandidateTime.Format(time.RFC3339))
	} else {
		fmt.Println("- first_candidate_combo: none")
	}
	fmt.Println("- orders_sent: 0")
}

func printMiningSummary(output episodeMiningOutput) {
	fmt.Println("Mining summary:")
	fmt.Printf("- windows_scanned: %d\n", output.Summary.TotalWindowsScanned)
	fmt.Printf("- episodes_mined: %d\n", output.Summary.EpisodesMined)
	fmt.Printf("- deduplicated_count: %d\n", output.Summary.DeduplicatedCount)
	fmt.Printf("- warmup_insufficient_count: %d\n", output.Summary.WarmupInsufficientCount)
	fmt.Printf("- skipped_insufficient_warmup_count: %d\n", output.Summary.SkippedInsufficientWarmupCount)
	fmt.Printf("- true_reversal_like_count: %d\n", output.Summary.TrueReversalLikeCount)
	fmt.Printf("- false_reversal_like_count: %d\n", output.Summary.FalseReversalLikeCount)
	fmt.Printf("- no_setup_control_count: %d\n", output.Summary.NoSetupControlCount)
	fmt.Printf("- manifest: %s\n", output.ManifestPath)
	fmt.Printf("- summary: %s\n", output.SummaryPath)
	fmt.Printf("- report: %s\n", output.ReportPath)
}

func parseOptionalTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	ts, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, err
	}
	return ts.UTC(), nil
}
