package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
	"verbose-fortnight/internal/prices"
	"verbose-fortnight/models"
)

func attachOperationStore(cfg *config.Config, state *models.State, client *api.RESTClient) (*operations.Store, error) {
	state.Lock()
	state.RecoveryRequired = true
	state.RecoveryError = "startup reconciliation required"
	state.TradingBlocked = true
	state.BlockReason = models.BlockedReasonFatalAPI
	state.BlockMessage = state.RecoveryError
	state.Unlock()
	store, err := operations.Open(cfg.OperationStatePath)
	if err != nil {
		state.Lock()
		state.RecoveryError = err.Error()
		state.BlockMessage = err.Error()
		state.Unlock()
		return nil, err
	}
	client.Operations = store
	client.OperationOwner = func() operations.Owner {
		state.RLock()
		defer state.RUnlock()
		return operations.Owner{Lifecycle: state.ActiveTradeID, Side: state.PosSide}
	}
	snap := store.Snapshot()
	state.Lock()
	state.OperationStore = store
	state.PendingOrderSequence = snap.Sequence
	state.PendingOrders = map[uint64]models.PendingOrder{}
	for _, r := range snap.Records {
		if r.Stage == "resolved" || r.Stage == "rejected" {
			continue
		}
		state.PendingOrders[r.Generation] = models.PendingOrder{OrderID: r.OrderID, OrderLinkID: r.Link, Generation: r.Generation}
		if r.Generation > state.PendingOrderGeneration {
			state.PendingOrderGeneration = r.Generation
			state.PendingOrderID = r.OrderID
			state.PendingOrderLinkID = r.Link
		}
	}
	state.OrderReconciliationPending = len(state.PendingOrders) > 0
	state.Unlock()
	return store, nil
}

func applyStartupRecovery(state *models.State, obs api.RecoveryObservation) {
	size, _ := strconv.ParseFloat(obs.Position.Size, 64)
	entry, _ := strconv.ParseFloat(obs.Position.AvgPrice, 64)
	tp, _ := strconv.ParseFloat(obs.Position.TakeProfit, 64)
	sl, _ := strconv.ParseFloat(obs.Position.StopLoss, 64)
	state.Lock()
	state.PendingOrders = map[uint64]models.PendingOrder{}
	state.OrderReconciliationPending = false
	state.PendingOrderGeneration = 0
	state.PendingOrderID = ""
	state.PendingOrderLinkID = ""
	state.RecoveryRequired = false
	state.RecoveryError = ""
	if size > 0 {
		state.SetActiveTradeLocked(obs.Lifecycle)
		state.PosSide = prices.NormalizeSide(obs.Position.Side)
		state.OrderQty = size
		state.PositionState = models.PositionStateOpen
		state.OpeningExecAck = true
		state.OpeningPosAck = true
		state.StopCtrl = models.StopController{TradeID: obs.Lifecycle, AppliedTradeID: obs.Lifecycle, DesiredTP: tp, DesiredSL: sl, AppliedTP: tp, AppliedSL: sl, UpdatedAt: obs.At}
	} else {
		state.SetActiveTradeLocked("")
		state.PosSide = ""
		state.OrderQty = 0
		state.PositionState = models.PositionStateFlat
	}
	if state.ProcessedExec == nil {
		state.ProcessedExec = map[string]time.Time{}
	}
	for _, e := range obs.Executions {
		state.ProcessedExec[e.ID] = obs.At
		state.ExecSeenOrder = append(state.ExecSeenOrder, e.ID)
	}
	state.Unlock()
	state.StatusLock.Lock()
	state.LastPosition = models.PositionSnapshot{Side: prices.NormalizeSide(obs.Position.Side), Size: size, EntryPrice: entry, TakeProfit: tp, StopLoss: sl, UpdatedAt: obs.At}
	state.StatusLock.Unlock()
}

func waitRecoveryBlocked(ctx context.Context, state *models.State, server *http.Server, err error, oneShot bool) {
	state.Lock()
	state.RecoveryRequired = true
	state.RecoveryError = err.Error()
	state.TradingBlocked = true
	state.BlockReason = models.BlockedReasonFatalAPI
	state.BlockMessage = err.Error()
	state.Unlock()
	logError("Trading remains blocked: %v", err)
	if oneShot {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"ready": false, "error": err.Error()})
		os.Exit(1)
	}
	<-ctx.Done()
	if server != nil {
		shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}
}

func printRecoveryResult(store *operations.Store) {
	if err := json.NewEncoder(os.Stdout).Encode(store.Diagnostic()); err != nil {
		logError("recovery diagnostic output: %v", fmt.Errorf("%w", err))
	}
}
