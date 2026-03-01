package main

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
	"verbose-fortnight/strategy"
)

type executionBatchStats struct {
	Fetched      int
	Processed    int
	Deduped      int
	Pages        int
	Gaps         int
	Errors       int
	CursorBefore string
	CursorAfter  string
}

type executionPageClient interface {
	GetExecutionsPage(symbol, cursor string, limit int) ([]api.ExecutionRecord, string, bool, error)
}

type executionSinceClient interface {
	GetExecutionsSince(symbol, sinceExecID string, limit int) ([]api.ExecutionRecord, error)
}

type executionEventProcessor interface {
	ResolveExecutionLifecycleID(orderID, execID, orderLinkID string) (string, bool)
	ResolveExecutionTradeID(orderID, execID string) string
	ProcessExecutionEvent(evt models.ExecutionEvent, source string) bool
}

type executionSeenTracker struct {
	mu      sync.Mutex
	seenAt  map[string]time.Time
	seenOrd []string
	maxSize int
	ttl     time.Duration
}

func newExecutionSeenTracker(maxSize int, ttl time.Duration) *executionSeenTracker {
	if maxSize <= 0 {
		maxSize = 50000
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &executionSeenTracker{
		seenAt:  make(map[string]time.Time, maxSize),
		seenOrd: make([]string, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (t *executionSeenTracker) compact(now time.Time) {
	for len(t.seenOrd) > 0 {
		head := t.seenOrd[0]
		ts, ok := t.seenAt[head]
		if !ok {
			t.seenOrd = t.seenOrd[1:]
			continue
		}
		if now.Sub(ts) > t.ttl || len(t.seenAt) > t.maxSize {
			delete(t.seenAt, head)
			t.seenOrd = t.seenOrd[1:]
			continue
		}
		break
	}
}

func (t *executionSeenTracker) mark(execID string) {
	execID = strings.TrimSpace(execID)
	if execID == "" {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.compact(now)
	t.seenAt[execID] = now
	t.seenOrd = append(t.seenOrd, execID)
	for len(t.seenAt) > t.maxSize && len(t.seenOrd) > 0 {
		head := t.seenOrd[0]
		t.seenOrd = t.seenOrd[1:]
		delete(t.seenAt, head)
	}
}

func (t *executionSeenTracker) seen(execID string) bool {
	execID = strings.TrimSpace(execID)
	if execID == "" {
		return false
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.compact(now)
	ts, ok := t.seenAt[execID]
	if !ok {
		return false
	}
	return now.Sub(ts) <= t.ttl
}

var wsExecutionSeen = newExecutionSeenTracker(50000, 24*time.Hour)

func markExecutionSeenFromWS(execID string) {
	wsExecutionSeen.mark(execID)
}

func executionCursor(state *models.State) string {
	if state == nil {
		return ""
	}
	state.RLock()
	defer state.RUnlock()
	if strings.TrimSpace(state.LastExecCursor) != "" {
		return strings.TrimSpace(state.LastExecCursor)
	}
	return ""
}

func executionLegacyBoundary(state *models.State) string {
	if state == nil {
		return ""
	}
	state.RLock()
	defer state.RUnlock()
	// Soft migration: if cursor is not initialized yet, continue from legacy exec-id boundary.
	if strings.TrimSpace(state.LastExecCursor) == "" {
		return strings.TrimSpace(state.LastExecID)
	}
	return ""
}

func updateExecutionCursor(state *models.State, nextCursor string) {
	if state == nil {
		return
	}
	state.Lock()
	state.LastExecCursor = strings.TrimSpace(nextCursor)
	state.Unlock()
}

func executionRecordTime(rec api.ExecutionRecord) int64 {
	if rec.ExecTime > 0 {
		return rec.ExecTime
	}
	return rec.CreatedTime
}

func sortExecutionsOldToNew(records []api.ExecutionRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		ti := executionRecordTime(records[i])
		tj := executionRecordTime(records[j])
		if ti != tj {
			return ti < tj
		}
		return records[i].ExecID < records[j].ExecID
	})
}

func processExecutionRecord(trader executionEventProcessor, symbol, source string, exec api.ExecutionRecord) (processed bool, wsSeen bool) {
	if trader == nil {
		return false, false
	}
	if source == "backfill" {
		wsSeen = wsExecutionSeen.seen(exec.ExecID)
	}
	tradeID := ""
	lifecycleInferred := false
	if cfg != nil && cfg.EnableLifecycleID {
		tradeID, lifecycleInferred = trader.ResolveExecutionLifecycleID(exec.OrderID, exec.ExecID, exec.OrderLinkID)
	} else {
		tradeID = trader.ResolveExecutionTradeID(exec.OrderID, exec.ExecID)
	}
	if lifecycleInferred {
		logWarning("Lifecycle inferred for %s execution: orderId=%s orderLinkId=%s execId=%s lifecycleId=%s",
			source, exec.OrderID, exec.OrderLinkID, exec.ExecID, tradeID)
	}
	if cfg != nil && cfg.EnableFillJSONLog {
		logExecutionFillJSON(map[string]interface{}{
			"symbol":      symbol,
			"orderId":     exec.OrderID,
			"orderLinkId": exec.OrderLinkID,
			"execId":      exec.ExecID,
			"side":        exec.Side,
			"execQty":     exec.ExecQty,
			"execPrice":   exec.ExecPrice,
			"execTime":    exec.ExecTime,
			"createdTime": exec.CreatedTime,
		}, symbol, source, tradeID, lifecycleInferred)
	}
	processed = trader.ProcessExecutionEvent(models.ExecutionEvent{
		TradeID:      tradeID,
		ExecID:       exec.ExecID,
		OrderID:      exec.OrderID,
		OrderLinkID:  exec.OrderLinkID,
		ExecSide:     normalizeSide(exec.Side),
		PositionSide: normalizeSide(exec.Side),
		Qty:          exec.ExecQty,
		Price:        exec.ExecPrice,
	}, source)
	return processed, wsSeen
}

func fetchExecutionPages(client executionPageClient, symbol, cursor, legacySinceExecID string, limit, maxPages, maxRecords int) ([]api.ExecutionRecord, string, int, bool, error) {
	if client == nil {
		return nil, cursor, 0, false, nil
	}
	if limit <= 0 {
		limit = 200
	}
	if maxPages <= 0 {
		maxPages = 3
	}
	if maxRecords <= 0 {
		maxRecords = 600
	}
	nextCursor := strings.TrimSpace(cursor)
	if nextCursor == "" && strings.TrimSpace(legacySinceExecID) != "" {
		if sinceClient, ok := client.(executionSinceClient); ok {
			legacyRecords, err := sinceClient.GetExecutionsSince(symbol, strings.TrimSpace(legacySinceExecID), maxRecords)
			if err != nil {
				return nil, nextCursor, 0, true, err
			}
			return legacyRecords, nextCursor, 1, true, nil
		}
	}
	records := make([]api.ExecutionRecord, 0, maxRecords)
	pages := 0
	for pages < maxPages && len(records) < maxRecords {
		page, pageNextCursor, hasMore, err := client.GetExecutionsPage(symbol, nextCursor, limit)
		if err != nil {
			return records, nextCursor, pages, false, err
		}
		pages++
		if len(page) == 0 {
			nextCursor = strings.TrimSpace(pageNextCursor)
			break
		}
		for _, rec := range page {
			records = append(records, rec)
			if len(records) >= maxRecords {
				break
			}
		}
		nextCursor = strings.TrimSpace(pageNextCursor)
		if !hasMore || nextCursor == "" {
			break
		}
	}
	return records, nextCursor, pages, false, nil
}

func runExecutionBackfillCycle(client executionPageClient, trader executionEventProcessor, state *models.State, symbol, source string, limit, maxPages, maxRecords int) (executionBatchStats, error) {
	stats := executionBatchStats{CursorBefore: executionCursor(state)}
	legacySinceExecID := executionLegacyBoundary(state)
	records, nextCursor, pages, legacyMode, err := fetchExecutionPages(client, symbol, stats.CursorBefore, legacySinceExecID, limit, maxPages, maxRecords)
	stats.Pages = pages
	stats.Fetched = len(records)
	if err != nil {
		stats.Errors++
		stats.CursorAfter = stats.CursorBefore
		if source == "backfill" && state != nil {
			state.RecordBackfillError(err.Error(), time.Now().UTC())
		}
		return stats, err
	}
	sortExecutionsOldToNew(records)
	for _, rec := range records {
		processed, seenWS := processExecutionRecord(trader, symbol, source, rec)
		if processed {
			stats.Processed++
			if source == "backfill" && !seenWS {
				stats.Gaps++
			}
			continue
		}
		stats.Deduped++
	}
	if legacyMode {
		// Keep cursor unchanged in legacy mode; this avoids invalid cursor tokens during migration.
		stats.CursorAfter = stats.CursorBefore
	} else {
		updateExecutionCursor(state, nextCursor)
		stats.CursorAfter = nextCursor
	}
	if source == "backfill" && state != nil {
		state.RecordBackfillCycle(
			uint64(stats.Fetched),
			uint64(stats.Processed),
			uint64(stats.Deduped),
			uint64(stats.Gaps),
			time.Now().UTC(),
		)
	}
	return stats, nil
}

func executionBackfillSleep(base, jitter time.Duration) time.Duration {
	if base <= 0 {
		base = 10 * time.Second
	}
	if jitter <= 0 {
		return base
	}
	n := time.Now().UnixNano()
	if n < 0 {
		n = -n
	}
	return base + time.Duration(n%int64(jitter))
}

func executionBackoffInterval(base time.Duration, errorsInRow int, maxBackoff time.Duration) time.Duration {
	if base <= 0 {
		base = 10 * time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = 2 * time.Minute
	}
	if errorsInRow <= 0 {
		return base
	}
	mult := 1 << min(errorsInRow, 6)
	backoff := base * time.Duration(mult)
	if backoff > maxBackoff {
		return maxBackoff
	}
	return backoff
}

func startExecutionBackfillWorker(ctx context.Context, apiClient *api.RESTClient, trader *strategy.Trader, state *models.State, symbol string) {
	if cfg == nil || !cfg.EnableExecutionBackfill {
		return
	}
	go func() {
		const (
			baseInterval = 10 * time.Second
			maxJitter    = 3 * time.Second
			pageLimit    = 200
			maxPages     = 5
			maxRecords   = 1000
			maxBackoff   = 2 * time.Minute
			errorLogMin  = 30 * time.Second
		)
		logInfo("Execution backfill worker started: symbol=%s interval=%s jitter<=%s", symbol, baseInterval, maxJitter)
		errStreak := 0
		currentInterval := baseInterval
		lastLoggedBackoff := time.Duration(0)
		lastErrorLogAt := time.Time{}
		for {
			sleepFor := executionBackfillSleep(currentInterval, maxJitter)
			timer := time.NewTimer(sleepFor)
			select {
			case <-ctx.Done():
				timer.Stop()
				logInfo("Execution backfill worker stopped")
				return
			case <-timer.C:
			}

			started := time.Now()
			stats, err := runExecutionBackfillCycle(apiClient, trader, state, symbol, "backfill", pageLimit, maxPages, maxRecords)
			if err != nil {
				errStreak++
				currentInterval = executionBackoffInterval(baseInterval, errStreak, maxBackoff)
				now := time.Now()
				shouldLog := currentInterval != lastLoggedBackoff
				if !shouldLog {
					shouldLog = lastErrorLogAt.IsZero() || now.Sub(lastErrorLogAt) >= errorLogMin
				}
				if shouldLog {
					logWarning("Execution backfill cycle failed: cursor_before=%s pages=%d errors=%d next_backoff=%s err=%v",
						stats.CursorBefore, stats.Pages, stats.Errors, currentInterval, err)
					lastLoggedBackoff = currentInterval
					lastErrorLogAt = now
				}
				continue
			}
			errStreak = 0
			currentInterval = baseInterval
			lastLoggedBackoff = 0
			lastErrorLogAt = time.Time{}
			delay := time.Since(started)
			logInfo("Execution backfill cycle: fetched=%d processed=%d deduped=%d pages=%d cursor_before=%s cursor_after=%s duration_ms=%d",
				stats.Fetched, stats.Processed, stats.Deduped, stats.Pages, stats.CursorBefore, stats.CursorAfter, delay.Milliseconds())
			if stats.Gaps > 0 {
				logWarning("Execution backfill gap detected: gaps=%d processed=%d deduped=%d cursor_before=%s cursor_after=%s",
					stats.Gaps, stats.Processed, stats.Deduped, stats.CursorBefore, stats.CursorAfter)
			}
		}
	}()
}
