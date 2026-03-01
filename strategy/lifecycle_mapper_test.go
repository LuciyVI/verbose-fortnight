package strategy

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLifecycleMapperResolveOrderLinkPriority(t *testing.T) {
	m := newLifecycleMapper(filepath.Join(t.TempDir(), "lifecycle_map.json"), nil)
	defer m.Close()

	m.BindOrderID("order-1", "lc-order")
	m.BindOrderLinkID("link-1", "lc-link")

	got, inferred := m.Resolve("order-1", "exec-1", "link-1", "")
	if inferred {
		t.Fatalf("expected non-inferred lifecycle for mapped orderLinkId")
	}
	if got != "lc-link" {
		t.Fatalf("resolve by orderLink priority got %q want %q", got, "lc-link")
	}

	gotExec, inferredExec := m.Resolve("", "exec-1", "", "")
	if inferredExec {
		t.Fatalf("expected exec lifecycle to be mapped after resolve")
	}
	if gotExec != "lc-link" {
		t.Fatalf("exec mapping got %q want %q", gotExec, "lc-link")
	}
}

func TestLifecycleMapperPersistenceSaveLoadResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lifecycle_map.json")
	m := newLifecycleMapper(path, nil)
	m.BindOrderID("order-42", "lc-42")
	m.BindOrderLinkID("link-42", "lc-42")
	m.BindExecID("exec-42", "lc-42")
	if err := m.Flush(); err != nil {
		t.Fatalf("flush error: %v", err)
	}
	m.Close()

	m2 := newLifecycleMapper(path, nil)
	defer m2.Close()

	got, inferred := m2.Resolve("order-42", "", "", "")
	if inferred {
		t.Fatalf("expected persisted orderId mapping to be non-inferred")
	}
	if got != "lc-42" {
		t.Fatalf("loaded mapping got %q want %q", got, "lc-42")
	}

	gotExec, inferredExec := m2.Resolve("", "exec-42", "", "")
	if inferredExec {
		t.Fatalf("expected persisted execId mapping to be non-inferred")
	}
	if gotExec != "lc-42" {
		t.Fatalf("loaded exec mapping got %q want %q", gotExec, "lc-42")
	}
}

func TestLifecycleMapperTTLCleanup(t *testing.T) {
	m := newLifecycleMapper(filepath.Join(t.TempDir(), "lifecycle_map.json"), nil)
	defer m.Close()

	base := time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC)
	m.mu.Lock()
	m.nowFn = func() time.Time { return base }
	m.mu.Unlock()

	m.BindOrderID("order-old", "lc-old")
	m.BindOrderLinkID("link-old", "lc-old")
	m.BindExecID("exec-old", "lc-old")

	m.mu.Lock()
	m.nowFn = func() time.Time { return base.Add(49 * time.Hour) }
	m.mu.Unlock()

	_, _ = m.Resolve("", "", "", "")

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.orderIDToLC["order-old"]; ok {
		t.Fatalf("order mapping must be expired by TTL")
	}
	if _, ok := m.orderLinkIDToLC["link-old"]; ok {
		t.Fatalf("orderLink mapping must be expired by TTL")
	}
	if _, ok := m.execIDToLC["exec-old"]; ok {
		t.Fatalf("exec mapping must be expired by TTL")
	}
}

func TestLifecycleOrderLinkIDEncodesLifecycleAndLeg(t *testing.T) {
	lc := "2d8f6e5f-8700-41a8-8f2e-3acc2b9f9d10"
	entryLink := lifecycleOrderLinkID(lc, "entry")
	closeLink := lifecycleOrderLinkID(lc, "close")
	if entryLink == "" || closeLink == "" {
		t.Fatalf("expected non-empty links")
	}
	if len(entryLink) > lifecycleOrderLinkLimit || len(closeLink) > lifecycleOrderLinkLimit {
		t.Fatalf("orderLinkId length exceeds limit: entry=%d close=%d", len(entryLink), len(closeLink))
	}
	if entryLink == closeLink {
		t.Fatalf("expected leg-specific orderLinkId values, got equal %q", entryLink)
	}
	if got := lifecycleFromOrderLinkID(entryLink); got != lc {
		t.Fatalf("decoded entry lifecycle got %q want %q", got, lc)
	}
	if got := lifecycleFromOrderLinkID(closeLink); got != lc {
		t.Fatalf("decoded close lifecycle got %q want %q", got, lc)
	}
}

func TestLifecycleMapperOrderLinkWinsOverOrderIDWithDirectLifecycleLink(t *testing.T) {
	m := newLifecycleMapper(filepath.Join(t.TempDir(), "lifecycle_map.json"), nil)
	defer m.Close()

	m.BindOrderID("order-1", "lc-order")
	lc2 := "2d8f6e5f-8700-41a8-8f2e-3acc2b9f9d10"
	orderLinkID := lifecycleOrderLinkID(lc2, "entry")

	got, inferred := m.Resolve("order-1", "exec-1", orderLinkID, "")
	if inferred {
		t.Fatalf("expected non-inferred lifecycle for direct orderLinkId")
	}
	if got != lc2 {
		t.Fatalf("orderLink must win over orderId mapping: got=%q want=%q", got, lc2)
	}
}

func TestLifecycleMapperResolveDirectLifecycleOrderLink(t *testing.T) {
	m := newLifecycleMapper(filepath.Join(t.TempDir(), "lifecycle_map.json"), nil)
	defer m.Close()

	lc := "2d8f6e5f-8700-41a8-8f2e-3acc2b9f9d10"
	got, inferred := m.Resolve("order-x", "exec-x", lc, "")
	if inferred {
		t.Fatalf("expected non-inferred lifecycle for direct lifecycle orderLinkId")
	}
	if got != lc {
		t.Fatalf("resolve by direct orderLink got %q want %q", got, lc)
	}
}
