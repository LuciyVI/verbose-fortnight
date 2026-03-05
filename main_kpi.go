package main

import (
	"context"
	"encoding/json"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

type kpiSnapshot struct {
	MakerRatio      float64 `json:"maker_ratio"`
	FeeToGrossRatio float64 `json:"fee_to_gross_ratio"`
	AvgNetAfterFee  float64 `json:"avg_net_after_fee"`
	WouldBlockRate  float64 `json:"would_block_rate"`
	AvgDurationWin  float64 `json:"avg_duration_win"`
	AvgDurationLoss float64 `json:"avg_duration_loss"`
}

type kpiViolation struct {
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`
	Rule      string  `json:"rule"`
}

func computeKPISnapshot(counters models.RuntimeCounters) kpiSnapshot {
	wouldBlockRate := 0.0
	totalEdge := counters.EdgePass + counters.EdgeReject
	if totalEdge > 0 {
		wouldBlockRate = float64(counters.EdgeReject) / float64(totalEdge)
	}
	return kpiSnapshot{
		MakerRatio:      counters.MakerRatio,
		FeeToGrossRatio: counters.FeeToGrossRatio,
		AvgNetAfterFee:  counters.AvgNetPerTrade,
		WouldBlockRate:  wouldBlockRate,
		AvgDurationWin:  counters.AvgWinDuration,
		AvgDurationLoss: counters.AvgLossDuration,
	}
}

func evaluateKPIViolations(cfg *config.Config, snap kpiSnapshot) []kpiViolation {
	if cfg == nil {
		return nil
	}
	violations := make([]kpiViolation, 0, 4)
	if cfg.KPIMinMakerRatio > 0 && snap.MakerRatio < cfg.KPIMinMakerRatio {
		violations = append(violations, kpiViolation{Metric: "maker_ratio", Value: snap.MakerRatio, Threshold: cfg.KPIMinMakerRatio, Rule: "min"})
	}
	if cfg.KPIMaxFeeToGross > 0 && snap.FeeToGrossRatio > cfg.KPIMaxFeeToGross {
		violations = append(violations, kpiViolation{Metric: "fee_to_gross_ratio", Value: snap.FeeToGrossRatio, Threshold: cfg.KPIMaxFeeToGross, Rule: "max"})
	}
	if cfg.KPIMinNetPerTrade > 0 && snap.AvgNetAfterFee < cfg.KPIMinNetPerTrade {
		violations = append(violations, kpiViolation{Metric: "avg_net_after_fee", Value: snap.AvgNetAfterFee, Threshold: cfg.KPIMinNetPerTrade, Rule: "min"})
	}
	if cfg.KPIMaxEdgeBlockRate > 0 && snap.WouldBlockRate > cfg.KPIMaxEdgeBlockRate {
		violations = append(violations, kpiViolation{Metric: "would_block_rate", Value: snap.WouldBlockRate, Threshold: cfg.KPIMaxEdgeBlockRate, Rule: "max"})
	}
	return violations
}

func startKPIMonitorWorker(ctx context.Context, state *models.State, cfg *config.Config) {
	if state == nil || cfg == nil || !cfg.EnableKPIMonitoring {
		return
	}
	interval := cfg.KPISummaryIntervalSec
	if interval <= 0 {
		interval = 60
	}
	go func() {
		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				counters, _ := state.RuntimeSnapshot()
				snap := computeKPISnapshot(counters)
				payload := map[string]interface{}{
					"ts":                 time.Now().UTC().Format(time.RFC3339Nano),
					"maker_ratio":        snap.MakerRatio,
					"fee_to_gross_ratio": snap.FeeToGrossRatio,
					"avg_net_after_fee":  snap.AvgNetAfterFee,
					"would_block_rate":   snap.WouldBlockRate,
					"avg_duration_win":   snap.AvgDurationWin,
					"avg_duration_loss":  snap.AvgDurationLoss,
				}
				if raw, err := json.Marshal(payload); err == nil {
					logInfo("kpi_summary %s", string(raw))
				} else {
					logInfo("kpi_summary marshal_error=%v", err)
				}

				violations := evaluateKPIViolations(cfg, snap)
				for _, v := range violations {
					if raw, err := json.Marshal(v); err == nil {
						logWarning("kpi_violation %s", string(raw))
					} else {
						logWarning("kpi_violation marshal_error=%v metric=%s", err, v.Metric)
					}
				}
			}
		}
	}()
}
