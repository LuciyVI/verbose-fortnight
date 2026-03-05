package main

import (
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
	"verbose-fortnight/status"
)

func TestApplyRuntimeFeatures(t *testing.T) {
	state := &models.State{}
	cfg := &config.Config{
		EnableFillJSONLog:       true,
		EnableLifecycleID:       true,
		EnableExecutionBackfill: true,
		EnablePartialBERule:     true,
		EnableEdgeFilter:        true,
		EnableMakerFirst:        true,
		EnableTradeSummaryLog:   true,
		EnableKPIMonitoring:     true,
		EnableStatusServer:      true,
		EnableConfigEndpoint:    true,
		StatusAddr:              "127.0.0.1:6061",
		EnableDryRun:            true,
	}

	applyRuntimeFeatures(state, cfg)
	got := state.RuntimeFeaturesSnapshot()
	if !got.FillJSONLog || !got.LifecycleID || !got.ExecutionBackfill || !got.PartialBERule || !got.EdgeFilter || !got.MakerFirst || !got.TradeSummaryLog || !got.KPIMonitoring || !got.StatusServer || !got.ConfigEndpoint || !got.DryRun {
		t.Fatalf("unexpected runtime features snapshot: %+v", got)
	}
	if got.StatusServer != status.ShouldStartServer(cfg) {
		t.Fatalf("status feature mismatch: got=%t want=%t", got.StatusServer, status.ShouldStartServer(cfg))
	}
}

func TestStatusServerStartCondition(t *testing.T) {
	cfg := &config.Config{EnableStatusServer: true, StatusAddr: "127.0.0.1:6061"}
	if !status.ShouldStartServer(cfg) {
		t.Fatalf("expected status server configured")
	}

	cfg.StatusAddr = ""
	if status.ShouldStartServer(cfg) {
		t.Fatalf("expected status server disabled for empty addr")
	}

	cfg.EnableStatusServer = false
	cfg.StatusAddr = "127.0.0.1:6061"
	if status.ShouldStartServer(cfg) {
		t.Fatalf("expected status server disabled when flag is off")
	}
}

func TestRunDryRunTick(t *testing.T) {
	state := &models.State{}
	ts := time.Unix(1700000000, 0).UTC()

	runDryRunTick(state, false, ts)
	counters, health := state.RuntimeSnapshot()
	if counters.DryRunTicks != 1 {
		t.Fatalf("unexpected dry-run ticks: %d", counters.DryRunTicks)
	}
	if !health.LastDryRunTickTS.Equal(ts) {
		t.Fatalf("unexpected dry-run ts: %s", health.LastDryRunTickTS)
	}
	if !health.LastBackfillCycleTS.IsZero() {
		t.Fatalf("backfill ts must stay zero when backfill stub is off")
	}
	if counters.BackfillFetched != 0 || counters.BackfillProcessed != 0 || counters.BackfillDeduped != 0 || counters.BackfillGaps != 0 {
		t.Fatalf("dry-run tick without backfill must not change backfill counters: %+v", counters)
	}

	runDryRunTick(state, true, ts.Add(time.Second))
	counters, health = state.RuntimeSnapshot()
	if counters.DryRunTicks != 2 {
		t.Fatalf("unexpected dry-run ticks after second tick: %d", counters.DryRunTicks)
	}
	if health.LastBackfillCycleTS.IsZero() {
		t.Fatalf("expected backfill cycle timestamp to be set when stub is on")
	}
	if counters.BackfillFetched != 0 || counters.BackfillProcessed != 0 || counters.BackfillDeduped != 0 || counters.BackfillGaps != 0 {
		t.Fatalf("dry-run heartbeat must not change backfill counters: %+v", counters)
	}
}

func TestSyncLoggerSafelyWithNilLogger(t *testing.T) {
	prev := logger
	logger = nil
	defer func() { logger = prev }()
	syncLoggerSafely()
}
