package config

import (
	"fmt"
	"net"
	"strings"
)

// Validate checks only clearly invalid configuration values.
// Defaults remain unchanged; this is fail-fast for obviously broken runtime config.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	var errs []string
	addErr := func(format string, args ...interface{}) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(cfg.Symbol) == "" {
		addErr("symbol must not be empty")
	}
	if cfg.ObDepth <= 0 {
		addErr("obDepth must be > 0, got %d", cfg.ObDepth)
	}
	if cfg.OrderbookLevels <= 0 {
		addErr("orderbookLevels must be > 0, got %d", cfg.OrderbookLevels)
	}
	if cfg.OrderbookMinDepth < 0 {
		addErr("orderbookMinDepth must be >= 0, got %.6f", cfg.OrderbookMinDepth)
	}
	if cfg.RoundTripFeePerc < 0 {
		addErr("roundTripFeePerc must be >= 0, got %.6f", cfg.RoundTripFeePerc)
	}
	if cfg.FeeBufferMult <= 0 {
		addErr("feeBufferMult must be > 0, got %.6f", cfg.FeeBufferMult)
	}
	if cfg.PartialTakeProfitRatio < 0 || cfg.PartialTakeProfitRatio > 1 {
		addErr("partialTakeProfitRatio must be in [0,1], got %.6f", cfg.PartialTakeProfitRatio)
	}
	if cfg.PartialTakeProfitProgress < 0 || cfg.PartialTakeProfitProgress > 1 {
		addErr("partialTakeProfitProgress must be in [0,1], got %.6f", cfg.PartialTakeProfitProgress)
	}
	if cfg.ReentryCooldownSec < 0 {
		addErr("reentryCooldownSec must be >= 0, got %d", cfg.ReentryCooldownSec)
	}

	if cfg.EnableStatusServer {
		addr := strings.TrimSpace(cfg.StatusAddr)
		if addr != "" && !strings.EqualFold(addr, "off") && !strings.EqualFold(addr, "disabled") {
			if _, _, err := net.SplitHostPort(addr); err != nil {
				addErr("statusAddr must be host:port, got %q", cfg.StatusAddr)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}
