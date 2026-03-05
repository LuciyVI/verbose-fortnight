package config

import (
	"strings"
	"testing"
)

func newValidConfigForTest() *Config {
	return &Config{
		Symbol:                    "BTCUSDT",
		ObDepth:                   50,
		OrderbookLevels:           5,
		OrderbookMinDepth:         0,
		RoundTripFeePerc:          0.0012,
		FeeBufferMult:             1.0,
		PartialTakeProfitRatio:    0.4,
		PartialTakeProfitProgress: 0.6,
		ReentryCooldownSec:        30,
		EnableStatusServer:        true,
		StatusAddr:                "127.0.0.1:6061",
	}
}

func TestValidate_DefaultConfigOK(t *testing.T) {
	cfg := newValidConfigForTest()
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected default config to be valid, got %v", err)
	}
}

func TestValidate_RejectsClearlyInvalidValues(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.Symbol = ""
	cfg.OrderbookLevels = 0
	cfg.OrderbookMinDepth = -1
	cfg.RoundTripFeePerc = -0.1
	cfg.FeeBufferMult = 0
	cfg.PartialTakeProfitRatio = 1.5
	cfg.PartialTakeProfitProgress = -0.1
	cfg.ReentryCooldownSec = -1
	cfg.MakerTimeoutMs = -1
	cfg.EdgeGuardSpreadThreshold = -0.1
	cfg.EdgeGuardImpactThreshold = -0.1
	cfg.MakerMaxSlippagePct = -0.1
	cfg.KPISummaryIntervalSec = -1
	cfg.KPIMinMakerRatio = -0.1
	cfg.KPIMaxFeeToGross = -0.1
	cfg.KPIMaxEdgeBlockRate = -0.1
	cfg.StatusAddr = "invalid-addr"
	cfg.EnableStatusServer = true

	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected validation error")
	}
	msg := err.Error()
	for _, needle := range []string{
		"symbol must not be empty",
		"orderbookLevels must be > 0",
		"orderbookMinDepth must be >= 0",
		"roundTripFeePerc must be >= 0",
		"feeBufferMult must be > 0",
		"partialTakeProfitRatio must be in [0,1]",
		"partialTakeProfitProgress must be in [0,1]",
		"reentryCooldownSec must be >= 0",
		"makerTimeoutMs must be >= 0",
		"edgeGuardSpreadThreshold must be >= 0",
		"edgeGuardImpactThreshold must be >= 0",
		"makerMaxSlippagePct must be >= 0",
		"kpiSummaryIntervalSec must be >= 0",
		"kpiMinMakerRatio must be >= 0",
		"kpiMaxFeeToGross must be >= 0",
		"kpiMaxEdgeBlockRate must be >= 0",
		"statusAddr must be host:port",
	} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("expected %q in error, got %q", needle, msg)
		}
	}
}

func TestValidate_AllowsBlankStatusAddrWhenDisabled(t *testing.T) {
	cfg := newValidConfigForTest()
	cfg.EnableStatusServer = false
	cfg.StatusAddr = ""
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected config to be valid when status server disabled, got %v", err)
	}
}
