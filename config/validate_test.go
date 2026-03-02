package config

import (
	"strings"
	"testing"
)

func TestValidate_DefaultConfigOK(t *testing.T) {
	cfg := LoadConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected default config to be valid, got %v", err)
	}
}

func TestValidate_RejectsClearlyInvalidValues(t *testing.T) {
	cfg := LoadConfig()
	cfg.Symbol = ""
	cfg.OrderbookLevels = 0
	cfg.OrderbookMinDepth = -1
	cfg.RoundTripFeePerc = -0.1
	cfg.FeeBufferMult = 0
	cfg.PartialTakeProfitRatio = 1.5
	cfg.PartialTakeProfitProgress = -0.1
	cfg.ReentryCooldownSec = -1
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
		"statusAddr must be host:port",
	} {
		if !strings.Contains(msg, needle) {
			t.Fatalf("expected %q in error, got %q", needle, msg)
		}
	}
}

func TestValidate_AllowsBlankStatusAddrWhenDisabled(t *testing.T) {
	cfg := LoadConfig()
	cfg.EnableStatusServer = false
	cfg.StatusAddr = ""
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected config to be valid when status server disabled, got %v", err)
	}
}
