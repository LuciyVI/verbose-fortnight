package main

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/models"
)

type fakeExecutionPageClient struct {
	pages      map[string]fakeExecutionPage
	sincePages map[string][]api.ExecutionRecord
	sinceCalls int
}

type fakeExecutionPage struct {
	records    []api.ExecutionRecord
	nextCursor string
	hasMore    bool
	err        error
}

func (f *fakeExecutionPageClient) GetExecutionsPage(_ string, cursor string, _ int) ([]api.ExecutionRecord, string, bool, error) {
	p, ok := f.pages[cursor]
	if !ok {
		return nil, "", false, errors.New("page not found")
	}
	return p.records, p.nextCursor, p.hasMore, p.err
}

func (f *fakeExecutionPageClient) GetExecutionsSince(_ string, sinceExecID string, _ int) ([]api.ExecutionRecord, error) {
	f.sinceCalls++
	if f.sincePages == nil {
		return nil, nil
	}
	records, ok := f.sincePages[sinceExecID]
	if !ok {
		return nil, nil
	}
	return records, nil
}

type fakeExecutionTrader struct {
	processed map[string]struct{}
	applied   []string
}

func newFakeExecutionTrader() *fakeExecutionTrader {
	return &fakeExecutionTrader{
		processed: make(map[string]struct{}),
		applied:   make([]string, 0, 8),
	}
}

func (t *fakeExecutionTrader) ResolveExecutionLifecycleID(orderID, execID, _ string) (string, bool) {
	if execID != "" {
		return "lc-" + execID, false
	}
	return "lc-" + orderID, false
}

func (t *fakeExecutionTrader) ResolveExecutionTradeID(orderID, execID string) string {
	if execID != "" {
		return "tr-" + execID
	}
	return "tr-" + orderID
}

func (t *fakeExecutionTrader) ProcessExecutionEvent(evt models.ExecutionEvent, _ string) bool {
	if evt.ExecID == "" {
		return false
	}
	if _, ok := t.processed[evt.ExecID]; ok {
		return false
	}
	t.processed[evt.ExecID] = struct{}{}
	t.applied = append(t.applied, evt.ExecID)
	return true
}

func resetWSSeenTracker() {
	wsExecutionSeen = newExecutionSeenTracker(1024, 24*time.Hour)
}

func TestBackfillDedupAfterWSExecution(t *testing.T) {
	resetWSSeenTracker()
	cfg = nil
	state := &models.State{}
	trader := newFakeExecutionTrader()
	exec := api.ExecutionRecord{
		ExecID:    "exec-x",
		OrderID:   "order-x",
		Side:      "Buy",
		ExecQty:   0.1,
		ExecPrice: 100,
		ExecTime:  1000,
	}

	if processed, _ := processExecutionRecord(trader, "BTCUSDT", "ws", exec); !processed {
		t.Fatalf("ws execution should be applied once")
	}

	client := &fakeExecutionPageClient{
		pages: map[string]fakeExecutionPage{
			"": {
				records:    []api.ExecutionRecord{exec},
				nextCursor: "cursor-1",
				hasMore:    false,
			},
		},
	}
	stats, err := runExecutionBackfillCycle(client, trader, state, "BTCUSDT", "backfill", 200, 2, 200)
	if err != nil {
		t.Fatalf("runExecutionBackfillCycle error: %v", err)
	}
	if stats.Processed != 0 || stats.Deduped != 1 {
		t.Fatalf("unexpected dedup stats: %+v", stats)
	}
	if len(trader.applied) != 1 {
		t.Fatalf("expected a single applied execution, got %d", len(trader.applied))
	}
}

func TestBackfillPaginationAppliesInExecTimeOrder(t *testing.T) {
	resetWSSeenTracker()
	cfg = nil
	state := &models.State{}
	trader := newFakeExecutionTrader()

	client := &fakeExecutionPageClient{
		pages: map[string]fakeExecutionPage{
			"": {
				records: []api.ExecutionRecord{
					{ExecID: "exec-b", ExecTime: 2000},
					{ExecID: "exec-a", ExecTime: 1000},
				},
				nextCursor: "c2",
				hasMore:    true,
			},
			"c2": {
				records: []api.ExecutionRecord{
					{ExecID: "exec-d", ExecTime: 4000},
					{ExecID: "exec-c", ExecTime: 3000},
				},
				nextCursor: "",
				hasMore:    false,
			},
		},
	}

	stats, err := runExecutionBackfillCycle(client, trader, state, "BTCUSDT", "backfill", 200, 5, 1000)
	if err != nil {
		t.Fatalf("runExecutionBackfillCycle error: %v", err)
	}
	if stats.Pages != 2 {
		t.Fatalf("expected two pages, got %+v", stats)
	}
	wantOrder := []string{"exec-a", "exec-b", "exec-c", "exec-d"}
	if !reflect.DeepEqual(trader.applied, wantOrder) {
		t.Fatalf("unexpected apply order: got=%v want=%v", trader.applied, wantOrder)
	}
}

func TestBackfillCursorUpdatedFromNextCursor(t *testing.T) {
	resetWSSeenTracker()
	cfg = nil
	state := &models.State{LastExecCursor: "cursor-old"}
	trader := newFakeExecutionTrader()
	client := &fakeExecutionPageClient{
		pages: map[string]fakeExecutionPage{
			"cursor-old": {
				records: []api.ExecutionRecord{
					{ExecID: "exec-1", ExecTime: 1},
				},
				nextCursor: "cursor-new",
				hasMore:    false,
			},
		},
	}

	stats, err := runExecutionBackfillCycle(client, trader, state, "BTCUSDT", "backfill", 200, 1, 200)
	if err != nil {
		t.Fatalf("runExecutionBackfillCycle error: %v", err)
	}
	if stats.CursorAfter != "cursor-new" {
		t.Fatalf("unexpected cursor after in stats: %+v", stats)
	}
	if state.LastExecCursor != "cursor-new" {
		t.Fatalf("state cursor was not updated: %s", state.LastExecCursor)
	}
}

func TestBackfillLegacyLastExecIDMigration(t *testing.T) {
	resetWSSeenTracker()
	cfg = nil
	state := &models.State{
		LastExecID:     "exec-old",
		LastExecCursor: "",
	}
	trader := newFakeExecutionTrader()
	client := &fakeExecutionPageClient{
		pages: map[string]fakeExecutionPage{
			"": {
				records:    []api.ExecutionRecord{{ExecID: "should-not-be-used", ExecTime: 1}},
				nextCursor: "cursor-ignored",
				hasMore:    false,
			},
		},
		sincePages: map[string][]api.ExecutionRecord{
			"exec-old": {
				{ExecID: "exec-new", ExecTime: 2},
			},
		},
	}

	stats, err := runExecutionBackfillCycle(client, trader, state, "BTCUSDT", "backfill", 200, 1, 200)
	if err != nil {
		t.Fatalf("runExecutionBackfillCycle error: %v", err)
	}
	if client.sinceCalls != 1 {
		t.Fatalf("expected legacy since path to be called once, got %d", client.sinceCalls)
	}
	if stats.Processed != 1 || stats.Deduped != 0 {
		t.Fatalf("unexpected migration stats: %+v", stats)
	}
	if state.LastExecCursor != "" {
		t.Fatalf("legacy migration must not set cursor token, got %q", state.LastExecCursor)
	}
}

func TestBackfillErrorKeepsCursorAndTracksError(t *testing.T) {
	resetWSSeenTracker()
	cfg = nil
	state := &models.State{LastExecCursor: "cursor-old"}
	trader := newFakeExecutionTrader()
	client := &fakeExecutionPageClient{
		pages: map[string]fakeExecutionPage{
			"cursor-old": {
				records:    nil,
				nextCursor: "cursor-new",
				hasMore:    false,
				err:        errors.New("page failed"),
			},
		},
	}

	stats, err := runExecutionBackfillCycle(client, trader, state, "BTCUSDT", "backfill", 200, 1, 200)
	if err == nil {
		t.Fatalf("expected backfill cycle error")
	}
	if stats.Errors != 1 {
		t.Fatalf("expected stats.Errors=1, got %+v", stats)
	}
	if stats.CursorAfter != "cursor-old" {
		t.Fatalf("cursor should remain unchanged on error, got %+v", stats)
	}
	if state.LastExecCursor != "cursor-old" {
		t.Fatalf("state cursor must remain unchanged, got %q", state.LastExecCursor)
	}
	if state.RuntimeHealth.LastBackfillError == "" {
		t.Fatalf("expected backfill error message to be recorded")
	}
	if state.RuntimeHealth.LastBackfillErrorTS.IsZero() {
		t.Fatalf("expected backfill error timestamp to be recorded")
	}
}
