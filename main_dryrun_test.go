package main

import (
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestApplyRuntimeFeatures(t *testing.T) {
	state := &models.State{}
	cfg := &config.Config{
		EnableFillJSONLog:       true,
		EnableLifecycleID:       true,
		EnableExecutionBackfill: true,
		EnablePartialBERule:     true,
		EnableEdgeFilter:        true,
		EnableStatusServer:      true,
		StatusAddr:              "127.0.0.1:6061",
		EnableDryRun:            true,
	}

	applyRuntimeFeatures(state, cfg)
	got := state.RuntimeFeaturesSnapshot()
	if !got.FillJSONLog || !got.LifecycleID || !got.ExecutionBackfill || !got.PartialBERule || !got.EdgeFilter || !got.StatusServer || !got.DryRun {
		t.Fatalf("unexpected runtime features snapshot: %+v", got)
	}
}

func TestStatusServerConfigured(t *testing.T) {
	cfg := &config.Config{EnableStatusServer: true, StatusAddr: "127.0.0.1:6061"}
	if !statusServerConfigured(cfg) {
		t.Fatalf("expected status server configured")
	}

	cfg.StatusAddr = ""
	if statusServerConfigured(cfg) {
		t.Fatalf("expected status server disabled for empty addr")
	}

	cfg.EnableStatusServer = false
	cfg.StatusAddr = "127.0.0.1:6061"
	if statusServerConfigured(cfg) {
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

	runDryRunTick(state, true, ts.Add(time.Second))
	counters, health = state.RuntimeSnapshot()
	if counters.DryRunTicks != 2 {
		t.Fatalf("unexpected dry-run ticks after second tick: %d", counters.DryRunTicks)
	}
	if health.LastBackfillCycleTS.IsZero() {
		t.Fatalf("expected backfill cycle timestamp to be set when stub is on")
	}
}
