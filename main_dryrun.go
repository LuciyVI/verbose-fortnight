package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
	"verbose-fortnight/status"
)

func applyRuntimeFeatures(state *models.State, cfg *config.Config) {
	if state == nil || cfg == nil {
		return
	}
	state.SetRuntimeFeatures(models.RuntimeFeatures{
		FillJSONLog:          cfg.EnableFillJSONLog,
		ExecutionResponseLog: cfg.EnableExecutionResponseLog,
		LifecycleID:          cfg.EnableLifecycleID,
		ExecutionBackfill:    cfg.EnableExecutionBackfill,
		PartialBERule:        cfg.EnablePartialBERule,
		EdgeFilter:           cfg.EnableEdgeFilter,
		MakerFirst:           cfg.EnableMakerFirst,
		TradeSummaryLog:      cfg.EnableTradeSummaryLog,
		KPIMonitoring:        cfg.EnableKPIMonitoring,
		StatusServer:         status.ShouldStartServer(cfg),
		ConfigEndpoint:       cfg.EnableConfigEndpoint,
		DryRun:               cfg.EnableDryRun,
	})
}

func runDryRunTick(state *models.State, withBackfill bool, ts time.Time) {
	if state == nil {
		return
	}
	state.RecordDryRunTick(ts)
	if withBackfill {
		state.RecordBackfillHeartbeat(ts)
	}
}

func startDryRunWorker(ctx context.Context, state *models.State, withBackfill bool, symbol string) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		logInfo("Dry-run worker started: symbol=%s backfill_stub=%t", symbol, withBackfill)
		for {
			select {
			case <-ctx.Done():
				logInfo("Dry-run worker stopped")
				return
			case now := <-ticker.C:
				runDryRunTick(state, withBackfill, now.UTC())
			}
		}
	}()
}

func shutdownStatusServer(server *http.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func runDryRunMode(state *models.State, statusServer *http.Server) {
	logWarning("Dry-run mode enabled: skipping REST/WS network startup")
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	defer runtimeCancel()

	startDryRunWorker(runtimeCtx, state, cfg != nil && cfg.EnableExecutionBackfill, cfg.Symbol)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	sig := <-signals
	logInfo("Received signal %s, shutting down dry-run mode...", sig)

	runtimeCancel()
	shutdownStatusServer(statusServer)
	syncLoggerSafely()
}

func syncLoggerSafely() {
	if logger == nil {
		return
	}
	if err := logger.Sync(); err != nil {
		logError("Error syncing logger: %v", err)
	}
}
