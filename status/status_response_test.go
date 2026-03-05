package status

import (
	"encoding/json"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestBuildStatusResponseIncludesCountersHealthFeatures(t *testing.T) {
	cfg := &config.Config{Symbol: "BTCUSDT"}
	state := &models.State{
		MarketRegime: "range",
		RegimeStreak: 2,
	}
	state.CandleSeq.Store(42)
	state.StatusLock.Lock()
	state.LastSignal = models.SignalSnapshot{
		Kind:      "test",
		Direction: "LONG",
		Time:      time.Now().UTC(),
	}
	state.StatusLock.Unlock()
	state.SetRuntimeFeatures(models.RuntimeFeatures{
		StatusServer: true,
		DryRun:       true,
	})
	state.RecordDryRunTick(time.Now().UTC())

	resp := buildStatusResponse(cfg, state)

	raw, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal status response: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal status response: %v", err)
	}
	if _, ok := decoded["statusSchemaVersion"]; !ok {
		t.Fatalf("missing statusSchemaVersion in status response")
	}
	if _, ok := decoded["buildInfo"]; !ok {
		t.Fatalf("missing buildInfo in status response")
	}
	buildInfoRaw, ok := decoded["buildInfo"]
	if !ok {
		t.Fatalf("missing buildInfo in status response")
	}
	buildInfo, ok := buildInfoRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("buildInfo has unexpected type: %T", buildInfoRaw)
	}
	for _, field := range []string{"commit", "build_time", "go"} {
		if _, ok := buildInfo[field]; !ok {
			t.Fatalf("buildInfo.%s missing", field)
		}
	}
	if _, ok := decoded["counters"]; !ok {
		t.Fatalf("missing counters in status response")
	}
	countersRaw, ok := decoded["counters"]
	if !ok {
		t.Fatalf("missing counters in status response")
	}
	counters, ok := countersRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("counters has unexpected type: %T", countersRaw)
	}
	for _, field := range []string{
		"makerFills",
		"takerFills",
		"makerFallbackCount",
		"makerTimeoutCount",
		"totalExecFee",
		"totalGrossRealised",
		"totalNetRealised",
		"feeToGrossRatio",
		"makerRatio",
		"avgNetPerTrade",
	} {
		if _, ok := counters[field]; !ok {
			t.Fatalf("counters.%s missing", field)
		}
	}
	if _, ok := decoded["health"]; !ok {
		t.Fatalf("missing health in status response")
	}
	featuresRaw, ok := decoded["features"]
	if !ok {
		t.Fatalf("missing features in status response")
	}
	features, ok := featuresRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("features has unexpected type: %T", featuresRaw)
	}
	if _, ok := features["statusServer"]; !ok {
		t.Fatalf("features.statusServer missing")
	}
	if _, ok := features["dryRun"]; !ok {
		t.Fatalf("features.dryRun missing")
	}
}
