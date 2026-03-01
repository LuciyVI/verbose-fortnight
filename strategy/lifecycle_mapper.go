package strategy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"verbose-fortnight/logging"
)

const (
	lifecycleMapVersion     = 1
	lifecycleOrderTTL       = 48 * time.Hour
	lifecycleExecTTL        = 24 * time.Hour
	lifecycleOrderMax       = 10000
	lifecycleOrderLinkMax   = 10000
	lifecycleExecMax        = 50000
	lifecycleFlushEvery     = 10 * time.Second
	lifecycleOrderLinkLimit = 36
)

type lifecycleEntry struct {
	LifecycleID string `json:"lifecycleId"`
	SeenAtUnix  int64  `json:"seenAtUnix"`
}

type lifecycleMapDisk struct {
	Version         int                       `json:"version"`
	UpdatedAt       string                    `json:"updatedAt"`
	OrderIDToLC     map[string]lifecycleEntry `json:"orderIdToLc"`
	OrderLinkIDToLC map[string]lifecycleEntry `json:"orderLinkIdToLc"`
	ExecIDToLC      map[string]lifecycleEntry `json:"execIdToLc"`
}

type lifecycleMapper struct {
	mu sync.Mutex

	path   string
	logger logging.LoggerInterface
	nowFn  func() time.Time

	orderIDToLC     map[string]lifecycleEntry
	orderLinkIDToLC map[string]lifecycleEntry
	execIDToLC      map[string]lifecycleEntry

	dirty   bool
	flushCh chan struct{}
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func newLifecycleMapper(path string, logger logging.LoggerInterface) *lifecycleMapper {
	m := &lifecycleMapper{
		path:            path,
		logger:          logger,
		nowFn:           time.Now,
		orderIDToLC:     make(map[string]lifecycleEntry),
		orderLinkIDToLC: make(map[string]lifecycleEntry),
		execIDToLC:      make(map[string]lifecycleEntry),
		flushCh:         make(chan struct{}, 1),
		stopCh:          make(chan struct{}),
		doneCh:          make(chan struct{}),
	}
	m.loadFromDisk()
	go m.flushLoop()
	return m
}

func defaultLifecycleMapPath() string {
	return filepath.Join("runtime", "lifecycle_map.json")
}

func (m *lifecycleMapper) Close() {
	if m == nil {
		return
	}
	close(m.stopCh)
	<-m.doneCh
}

func (m *lifecycleMapper) Flush() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushLocked()
}

func (m *lifecycleMapper) BindOrderID(orderID, lifecycleID string) {
	if m == nil || orderID == "" || lifecycleID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFn()
	if m.bindLocked(m.orderIDToLC, orderID, lifecycleID, now) {
		m.compactLocked(now)
		m.markDirtyLocked()
	}
}

func (m *lifecycleMapper) BindOrderLinkID(orderLinkID, lifecycleID string) {
	if m == nil || orderLinkID == "" || lifecycleID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFn()
	if m.bindLocked(m.orderLinkIDToLC, orderLinkID, lifecycleID, now) {
		m.compactLocked(now)
		m.markDirtyLocked()
	}
}

func (m *lifecycleMapper) BindExecID(execID, lifecycleID string) {
	if m == nil || execID == "" || lifecycleID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFn()
	if m.bindLocked(m.execIDToLC, execID, lifecycleID, now) {
		m.compactLocked(now)
		m.markDirtyLocked()
	}
}

func (m *lifecycleMapper) Resolve(orderID, execID, orderLinkID, activeLifecycleID string) (string, bool) {
	if m == nil {
		if activeLifecycleID != "" {
			return activeLifecycleID, true
		}
		return "", true
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.nowFn()
	m.compactLocked(now)

	if lifecycleID := m.resolveByOrderLinkLocked(orderLinkID, now); lifecycleID != "" {
		m.bindIdentifiersLocked(orderID, execID, orderLinkID, lifecycleID, now)
		return lifecycleID, false
	}
	if lifecycleID := m.lookupLocked(m.orderIDToLC, orderID, now); lifecycleID != "" {
		m.bindIdentifiersLocked(orderID, execID, orderLinkID, lifecycleID, now)
		return lifecycleID, false
	}
	if lifecycleID := m.lookupLocked(m.execIDToLC, execID, now); lifecycleID != "" {
		m.bindIdentifiersLocked(orderID, execID, orderLinkID, lifecycleID, now)
		return lifecycleID, false
	}
	if activeLifecycleID != "" {
		m.bindIdentifiersLocked(orderID, execID, orderLinkID, activeLifecycleID, now)
		return activeLifecycleID, true
	}

	lifecycleID := newTradeID()
	m.bindIdentifiersLocked(orderID, execID, orderLinkID, lifecycleID, now)
	return lifecycleID, true
}

func lifecycleOrderLinkID(lifecycleID, leg string) string {
	lifecycleID = strings.TrimSpace(lifecycleID)
	if lifecycleID == "" {
		return ""
	}
	if compact, ok := compactLifecycleID(lifecycleID); ok {
		legCode := lifecycleLegCode(leg)
		nonce := base36Rune(time.Now().UnixNano())
		candidate := fmt.Sprintf("lc%s%s%s", compact, legCode, nonce)
		if len(candidate) <= lifecycleOrderLinkLimit {
			return candidate
		}
	}

	// Fallback for non-UUID/non-ULID lifecycle ids.
	leg = strings.ToLower(strings.TrimSpace(leg))
	if leg == "" {
		leg = "ord"
	}
	if len(leg) > 4 {
		leg = leg[:4]
	}
	base := strings.ReplaceAll(lifecycleID, "-", "")
	available := lifecycleOrderLinkLimit - len("lc_") - len("_") - len(leg)
	if available <= 0 {
		available = lifecycleOrderLinkLimit
	}
	if len(base) > available {
		base = base[:available]
	}
	return fmt.Sprintf("lc_%s_%s", base, leg)
}

func (m *lifecycleMapper) resolveByOrderLinkLocked(orderLinkID string, now time.Time) string {
	if lifecycleID := m.lookupLocked(m.orderLinkIDToLC, orderLinkID, now); lifecycleID != "" {
		return lifecycleID
	}
	oid := strings.TrimSpace(orderLinkID)
	if looksLikeLifecycleID(oid) {
		m.bindLocked(m.orderLinkIDToLC, oid, oid, now)
		m.markDirtyLocked()
		return oid
	}
	if lifecycleID := lifecycleFromOrderLinkID(oid); lifecycleID != "" {
		m.bindLocked(m.orderLinkIDToLC, oid, lifecycleID, now)
		m.markDirtyLocked()
		return lifecycleID
	}
	return ""
}

func lifecycleLegCode(leg string) string {
	switch strings.ToLower(strings.TrimSpace(leg)) {
	case "entry", "open":
		return "e"
	case "close", "clos", "exit":
		return "c"
	case "tp", "tpsl":
		return "t"
	case "sl", "stop":
		return "s"
	case "ptp", "partial":
		return "p"
	default:
		return "o"
	}
}

func compactLifecycleID(lifecycleID string) (string, bool) {
	id := strings.TrimSpace(lifecycleID)
	if len(id) == 36 && strings.Count(id, "-") == 4 {
		compact := strings.ReplaceAll(id, "-", "")
		if len(compact) == 32 && isHexLowerOrUpper(compact) {
			return strings.ToLower(compact), true
		}
	}
	if len(id) == 26 {
		return strings.ToUpper(id), true
	}
	return "", false
}

func lifecycleFromOrderLinkID(orderLinkID string) string {
	oid := strings.TrimSpace(orderLinkID)
	if oid == "" {
		return ""
	}

	// New format: lc<core><leg><nonce>, where core is UUID compact(32) or ULID(26).
	if strings.HasPrefix(oid, "lc") {
		payload := oid[2:]
		if len(payload) >= 28 { // ulid(26)+leg+nonce
			core := payload[:len(payload)-2]
			if len(core) == 32 && isHexLowerOrUpper(core) {
				return uuidFromCompact(strings.ToLower(core))
			}
			if len(core) == 26 {
				return strings.ToUpper(core)
			}
		}
	}

	// Backward compatibility: lc_<core>_<leg>
	if strings.HasPrefix(oid, "lc_") {
		parts := strings.Split(oid, "_")
		if len(parts) >= 3 {
			core := strings.TrimSpace(parts[1])
			if len(core) == 32 && isHexLowerOrUpper(core) {
				return uuidFromCompact(strings.ToLower(core))
			}
			if len(core) == 26 {
				return strings.ToUpper(core)
			}
		}
	}

	return ""
}

func uuidFromCompact(core string) string {
	if len(core) != 32 {
		return ""
	}
	return fmt.Sprintf("%s-%s-%s-%s-%s", core[0:8], core[8:12], core[12:16], core[16:20], core[20:32])
}

func isHexLowerOrUpper(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func base36Rune(n int64) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n < 0 {
		n = -n
	}
	return string(digits[n%36])
}

func (m *lifecycleMapper) bindIdentifiersLocked(orderID, execID, orderLinkID, lifecycleID string, now time.Time) {
	changed := false
	if m.bindLocked(m.orderIDToLC, orderID, lifecycleID, now) {
		changed = true
	}
	if m.bindLocked(m.orderLinkIDToLC, orderLinkID, lifecycleID, now) {
		changed = true
	}
	if m.bindLocked(m.execIDToLC, execID, lifecycleID, now) {
		changed = true
	}
	if changed {
		m.compactLocked(now)
		m.markDirtyLocked()
	}
}

func (m *lifecycleMapper) bindLocked(dst map[string]lifecycleEntry, key, lifecycleID string, now time.Time) bool {
	key = strings.TrimSpace(key)
	lifecycleID = strings.TrimSpace(lifecycleID)
	if key == "" || lifecycleID == "" {
		return false
	}
	prev, ok := dst[key]
	entry := lifecycleEntry{
		LifecycleID: lifecycleID,
		SeenAtUnix:  now.Unix(),
	}
	if ok && prev.LifecycleID == entry.LifecycleID && prev.SeenAtUnix == entry.SeenAtUnix {
		return false
	}
	dst[key] = entry
	return true
}

func (m *lifecycleMapper) lookupLocked(src map[string]lifecycleEntry, key string, now time.Time) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	entry, ok := src[key]
	if !ok {
		return ""
	}
	entry.SeenAtUnix = now.Unix()
	src[key] = entry
	return entry.LifecycleID
}

func (m *lifecycleMapper) compactLocked(now time.Time) {
	pruneByTTL(m.orderIDToLC, lifecycleOrderTTL, now)
	pruneByTTL(m.orderLinkIDToLC, lifecycleOrderTTL, now)
	pruneByTTL(m.execIDToLC, lifecycleExecTTL, now)

	trimToLimit(m.orderIDToLC, lifecycleOrderMax)
	trimToLimit(m.orderLinkIDToLC, lifecycleOrderLinkMax)
	trimToLimit(m.execIDToLC, lifecycleExecMax)
}

func pruneByTTL(m map[string]lifecycleEntry, ttl time.Duration, now time.Time) {
	if ttl <= 0 || len(m) == 0 {
		return
	}
	cutoff := now.Add(-ttl).Unix()
	for k, v := range m {
		if v.SeenAtUnix < cutoff {
			delete(m, k)
		}
	}
}

func trimToLimit(m map[string]lifecycleEntry, limit int) {
	if limit <= 0 || len(m) <= limit {
		return
	}
	type kv struct {
		Key string
		TS  int64
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{Key: k, TS: v.SeenAtUnix})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].TS < items[j].TS })
	toDrop := len(items) - limit
	for i := 0; i < toDrop; i++ {
		delete(m, items[i].Key)
	}
}

func looksLikeLifecycleID(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if len(v) == 26 {
		return true // ULID
	}
	return len(v) == 36 && strings.Count(v, "-") == 4
}

func (m *lifecycleMapper) markDirtyLocked() {
	m.dirty = true
	select {
	case m.flushCh <- struct{}{}:
	default:
	}
}

func (m *lifecycleMapper) flushLoop() {
	defer close(m.doneCh)
	ticker := time.NewTicker(lifecycleFlushEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.flushIfDirty()
		case <-m.flushCh:
			// Debounce by letting ticker handle the actual flush.
		case <-m.stopCh:
			m.flushIfDirty()
			return
		}
	}
}

func (m *lifecycleMapper) flushIfDirty() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.dirty {
		return
	}
	if err := m.flushLocked(); err != nil && m.logger != nil {
		m.logger.Error("lifecycle mapper flush failed: %v", err)
	}
}

func (m *lifecycleMapper) flushLocked() error {
	if !m.dirty {
		return nil
	}
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data := lifecycleMapDisk{
		Version:         lifecycleMapVersion,
		UpdatedAt:       m.nowFn().UTC().Format(time.RFC3339Nano),
		OrderIDToLC:     copyLifecycleMap(m.orderIDToLC),
		OrderLinkIDToLC: copyLifecycleMap(m.orderLinkIDToLC),
		ExecIDToLC:      copyLifecycleMap(m.execIDToLC),
	}
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return err
	}
	m.dirty = false
	return nil
}

func copyLifecycleMap(src map[string]lifecycleEntry) map[string]lifecycleEntry {
	if len(src) == 0 {
		return map[string]lifecycleEntry{}
	}
	dst := make(map[string]lifecycleEntry, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (m *lifecycleMapper) loadFromDisk() {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var disk lifecycleMapDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		if m.logger != nil {
			m.logger.Warning("lifecycle mapper load skipped: invalid json: %v", err)
		}
		return
	}
	if disk.Version != lifecycleMapVersion {
		if m.logger != nil {
			m.logger.Warning("lifecycle mapper load skipped: unsupported version=%d", disk.Version)
		}
		return
	}
	m.orderIDToLC = copyLifecycleMap(disk.OrderIDToLC)
	m.orderLinkIDToLC = copyLifecycleMap(disk.OrderLinkIDToLC)
	m.execIDToLC = copyLifecycleMap(disk.ExecIDToLC)
	m.compactLocked(m.nowFn())
}
