// Package operations provides a single-writer, crash-safe mutation ledger.
package operations

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const Version = 1
const maxFileBytes = 16 << 20

var ErrBlocked = errors.New("durable recovery gate is blocked")

type Owner struct {
	Lifecycle string `json:"lifecycle"`
	Side      string `json:"side,omitempty"`
}
type ownerKey struct{}

func WithOwner(ctx context.Context, owner Owner) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ownerKey{}, owner)
}
func ContextOwner(ctx context.Context) Owner {
	if ctx != nil {
		if o, ok := ctx.Value(ownerKey{}).(Owner); ok {
			return o
		}
	}
	return Owner{}
}
func HasOwner(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(ownerKey{}).(Owner)
	return ok
}

type Record struct {
	ID         string          `json:"id"`
	Generation uint64          `json:"generation"`
	Kind       string          `json:"kind"`
	Symbol     string          `json:"symbol"`
	Link       string          `json:"orderLinkId"`
	Owner      Owner           `json:"owner"`
	Payload    json.RawMessage `json:"payload"`
	Stage      string          `json:"stage"`
	OrderID    string          `json:"orderId,omitempty"`
	Status     string          `json:"orderStatus,omitempty"`
	CumExecQty float64         `json:"cumExecQty"`
	CreatedMs  int64           `json:"createdMs"`
	Outcome    string          `json:"outcome,omitempty"`
}
type Snapshot struct {
	Version      int             `json:"version"`
	Instance     string          `json:"instance"`
	Sequence     uint64          `json:"sequence"`
	Revision     uint64          `json:"revision"`
	Records      []Record        `json:"records"`
	Audit        json.RawMessage `json:"audit,omitempty"`
	BlockedAudit json.RawMessage `json:"blockedAudit,omitempty"`
}
type Diagnostic struct {
	Path     string   `json:"path"`
	Ready    bool     `json:"ready"`
	Error    string   `json:"error,omitempty"`
	Revision uint64   `json:"revision"`
	Records  []Record `json:"operations"`
}
type Store struct {
	mu              sync.Mutex
	path            string
	lock            *os.File
	snapshot        Snapshot
	startupComplete bool
	recoveryAttempt uint64
	failure         error
	// Hook is an injectable barrier for process tests, never configured by env.
	// Called after intent fsync, before result fsync, and after result fsync.
	Hook   func(string, Record)
	IOHook func(string)
}

func strictJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing or incomplete JSON")
	}
	return nil
}

func Open(path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	freshLock := err == nil
	if errors.Is(err, os.ErrExist) {
		lock, err = os.OpenFile(path+".lock", os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("operation store already in use: %w", err)
	}
	s := &Store{path: path, lock: lock}
	fail := func(err error) (*Store, error) {
		s.Close()
		return nil, fmt.Errorf("operation store %s: %w", path, err)
	}
	debris, err := filepath.Glob(path + ".tmp-*")
	if err != nil || len(debris) != 0 {
		return fail(fmt.Errorf("incomplete snapshot files require inspection: %v (%v)", debris, err))
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if !freshLock {
			return fail(fmt.Errorf("snapshot missing for an existing store; preserve evidence and restore/reconcile it"))
		}
		var nonce [16]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return fail(err)
		}
		s.snapshot = Snapshot{Version: Version, Instance: hex.EncodeToString(nonce[:]), Records: []Record{}}
		if err = s.persistLocked(s.snapshot); err != nil {
			return fail(err)
		}
		return s, nil
	}
	if err != nil {
		return fail(err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fail(err)
	}
	if len(data) > maxFileBytes {
		return fail(fmt.Errorf("snapshot too large"))
	}
	var envelope struct {
		Body   json.RawMessage `json:"body"`
		SHA256 string          `json:"sha256"`
	}
	if err = strictJSON(data, &envelope); err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(envelope.Body)
	if envelope.SHA256 != hex.EncodeToString(sum[:]) {
		return fail(fmt.Errorf("snapshot checksum mismatch"))
	}
	if err = strictJSON(envelope.Body, &s.snapshot); err != nil {
		return fail(err)
	}
	if err = validate(s.snapshot); err != nil {
		return fail(err)
	}
	return s, nil
}

func validate(s Snapshot) error {
	if s.Version != Version {
		return fmt.Errorf("unsupported snapshot version %d", s.Version)
	}
	if len(s.Instance) != 32 || s.Records == nil || uint64(len(s.Records)) != s.Sequence || s.Revision < s.Sequence {
		return fmt.Errorf("incomplete snapshot header")
	}
	if _, err := hex.DecodeString(s.Instance); err != nil {
		return fmt.Errorf("invalid snapshot instance")
	}
	ids, links, gens := map[string]bool{}, map[string]bool{}, map[uint64]bool{}
	for _, r := range s.Records {
		if r.ID != fmt.Sprintf("%s-%d", s.Instance, r.Generation) || r.Generation == 0 || r.Generation > s.Sequence || ids[r.ID] || gens[r.Generation] || r.Link == "" || r.Owner.Lifecycle == "" || r.Symbol == "" || r.CreatedMs <= 0 || math.IsNaN(r.CumExecQty) || math.IsInf(r.CumExecQty, 0) || r.CumExecQty < 0 {
			return fmt.Errorf("invalid/duplicate operation identity or generation")
		}
		ids[r.ID], gens[r.Generation] = true, true
		switch r.Stage {
		case "intent", "sending", "unknown", "acknowledged", "resolved", "rejected":
		default:
			return fmt.Errorf("unsupported operation stage %q", r.Stage)
		}
		if err := validatePayload(r.Kind, r.Payload); err != nil {
			return err
		}
		var p map[string]any
		json.Unmarshal(r.Payload, &p)
		if p["symbol"] != r.Symbol {
			return fmt.Errorf("operation payload target mismatch")
		}
		if r.Kind == "/v5/order/create" {
			if r.CumExecQty > Quantity(r) {
				return fmt.Errorf("operation cumulative fill exceeds requested quantity")
			}
			if links[r.Link] {
				return fmt.Errorf("duplicate orderLinkId")
			}
			links[r.Link] = true
			if p["orderLinkId"] != r.Link {
				return fmt.Errorf("operation payload identity mismatch")
			}
		}
	}
	return nil
}

func validatePayload(kind string, payload []byte) error {
	switch kind {
	case "/v5/order/create", "/v5/order/cancel", "/v5/order/cancel-all", "/v5/position/trading-stop", "external/order":
	default:
		return fmt.Errorf("unsupported mutation %q", kind)
	}
	var p map[string]any
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, k := range []string{"category", "symbol", "positionIdx", "side", "orderType", "qty", "price", "stopLossPrice", "timeInForce", "reduceOnly", "orderLinkId", "orderId", "takeProfit", "stopLoss", "tpslMode"} {
		allowed[k] = true
	}
	for k := range p {
		if !allowed[k] {
			return fmt.Errorf("unsupported/secret payload field %q", k)
		}
		switch k {
		case "positionIdx":
			if v, ok := p[k].(float64); !ok || v != 0 {
				return fmt.Errorf("invalid operation positionIdx")
			}
		case "reduceOnly":
			if _, ok := p[k].(bool); !ok {
				return fmt.Errorf("invalid reduceOnly")
			}
		default:
			if _, ok := p[k].(string); !ok {
				return fmt.Errorf("non-scalar operation field %q", k)
			}
		}
	}
	symbol, ok := p["symbol"].(string)
	if p["category"] != "linear" || !ok || symbol == "" {
		return fmt.Errorf("invalid mutation target")
	}
	if kind == "/v5/order/create" || kind == "external/order" {
		qty, ok := p["qty"].(string)
		q, e := strconv.ParseFloat(qty, 64)
		if !ok || e != nil || q <= 0 || math.IsNaN(q) || math.IsInf(q, 0) {
			return fmt.Errorf("invalid operation quantity")
		}
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failure = fmt.Errorf("operation store closed")
	if s.lock != nil {
		err := s.lock.Close()
		s.lock = nil
		return err
	}
	return nil
}
func clone(v Snapshot) Snapshot {
	b, _ := json.Marshal(v)
	var out Snapshot
	json.Unmarshal(b, &out)
	return out
}
func (s *Store) Snapshot() Snapshot { s.mu.Lock(); defer s.mu.Unlock(); return clone(s.snapshot) }
func (s *Store) readyLocked() bool {
	if s.failure != nil || !s.startupComplete {
		return false
	}
	for _, r := range s.snapshot.Records {
		if r.Stage == "sending" || r.Stage == "unknown" || (r.Stage == "acknowledged" && r.Kind != "/v5/order/create") {
			return false
		}
	}
	return true
}
func (s *Store) CanTrade() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	if !s.readyLocked() {
		return ErrBlocked
	}
	return nil
}
func (s *Store) BeginRecovery() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startupComplete = false
	s.recoveryAttempt++
	return s.recoveryAttempt
}
func (s *Store) Diagnostic() Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := Diagnostic{Path: s.path, Ready: s.readyLocked(), Revision: s.snapshot.Revision, Records: clone(s.snapshot).Records}
	if s.failure != nil {
		d.Error = s.failure.Error()
	} else if !d.Ready {
		d.Error = "startup reconciliation or unresolved mutation"
	}
	return d
}

// persistLocked commits only after file fsync, atomic rename and parent fsync.
// Any failure poisons this process. A leftover temp file blocks the next open.
func (s *Store) persistLocked(next Snapshot) error {
	body, err := json.Marshal(next)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	data, err := json.Marshal(struct {
		Body   json.RawMessage `json:"body"`
		SHA256 string          `json:"sha256"`
	}{body, hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	if len(data) > maxFileBytes {
		return fmt.Errorf("operation snapshot exceeds %d bytes", maxFileBytes)
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), filepath.Base(s.path)+".tmp-")
	if err != nil {
		return err
	}
	name := f.Name()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil && s.IOHook != nil {
		s.IOHook("snapshot_written")
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err == nil {
		var dir *os.File
		dir, err = os.Open(filepath.Dir(s.path))
		if err == nil {
			err = dir.Sync()
			ce := dir.Close()
			if err == nil {
				err = ce
			}
		}
	}
	if err != nil {
		return err
	}
	s.snapshot = next
	return nil
}
func (s *Store) commitLocked(next Snapshot) error {
	next.Revision++
	err := s.persistLocked(next)
	if err != nil {
		s.failure = fmt.Errorf("durable write failed: %w", err)
	}
	return err
}

func (s *Store) Prepare(kind string, payload []byte, owner Owner) (Record, error) {
	s.mu.Lock()
	if !s.readyLocked() {
		err := s.failure
		if err == nil {
			err = ErrBlocked
		}
		s.mu.Unlock()
		return Record{}, err
	}
	if kind == "external/order" {
		s.mu.Unlock()
		return Record{}, fmt.Errorf("external orders cannot be submitted")
	}
	if err := validatePayload(kind, payload); err != nil {
		s.mu.Unlock()
		return Record{}, err
	}
	var p map[string]any
	json.Unmarshal(payload, &p)
	if s.snapshot.Sequence == ^uint64(0) {
		s.mu.Unlock()
		return Record{}, fmt.Errorf("operation generation exhausted")
	}
	gen := s.snapshot.Sequence + 1
	id := fmt.Sprintf("%s-%d", s.snapshot.Instance, gen)
	link, _ := p["orderLinkId"].(string)
	orderID, _ := p["orderId"].(string)
	if kind == "/v5/order/create" {
		if link == "" {
			s.mu.Unlock()
			return Record{}, fmt.Errorf("durable create requires orderLinkId")
		}
		for _, r := range s.snapshot.Records {
			if r.Kind == kind && r.Link == link {
				s.mu.Unlock()
				return Record{}, fmt.Errorf("orderLinkId already registered; reconcile instead of POST")
			}
		}
	}
	if kind == "/v5/order/cancel" {
		for _, r := range s.snapshot.Records {
			if r.Kind == "/v5/order/create" && ((orderID != "" && r.OrderID == orderID) || (link != "" && r.Link == link)) {
				owner = r.Owner
				if link == "" {
					link = r.Link
				}
				break
			}
		}
	}
	if link == "" {
		link = "op-" + id
	} // local correlation only for endpoints without orderLinkId
	if owner.Lifecycle == "" {
		owner.Lifecycle = id
	}
	if owner.Side == "" {
		owner.Side, _ = p["side"].(string)
	}
	r := Record{ID: id, Generation: gen, Kind: kind, Symbol: p["symbol"].(string), Link: link, Owner: owner, Payload: append([]byte(nil), payload...), Stage: "intent", OrderID: orderID, CreatedMs: time.Now().UnixMilli()}
	next := clone(s.snapshot)
	next.Sequence = gen
	next.Records = append(next.Records, r)
	err := s.commitLocked(next)
	s.mu.Unlock()
	if err == nil && s.Hook != nil {
		s.Hook("intent_saved", r)
	}
	return r, err
}
func (s *Store) Sending(r Record) error {
	s.mu.Lock()
	err := s.failure
	if err == nil && !s.startupComplete {
		err = ErrBlocked
	}
	if err == nil {
		err = s.updateLocked(r, "sending", "", "", 0)
	}
	s.mu.Unlock()
	if err == nil && s.Hook != nil {
		s.Hook("sending_saved", r)
	}
	return err
}
func (s *Store) update(r Record, stage, outcome, status string, cum float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(r, stage, outcome, status, cum)
}
func (s *Store) updateLocked(r Record, stage, outcome, status string, cum float64) error {
	if s.failure != nil {
		return s.failure
	}
	next := clone(s.snapshot)
	for i, v := range next.Records {
		if v.ID == r.ID && v.Generation == r.Generation {
			if v.Stage == "resolved" || v.Stage == "rejected" {
				if stage != v.Stage || cum != v.CumExecQty || (status != "" && v.Status != "" && status != v.Status) {
					return fmt.Errorf("stale operation result")
				}
				if outcome == v.Outcome {
					return nil
				}
			}
			v.Stage, v.Outcome = stage, outcome
			if r.OrderID != "" {
				v.OrderID = r.OrderID
			}
			if status != "" {
				v.Status = status
			}
			if cum < v.CumExecQty {
				return fmt.Errorf("regressing cumulative execution")
			}
			v.CumExecQty = cum
			next.Records[i] = v
			return s.commitLocked(next)
		}
	}
	return fmt.Errorf("stale operation generation")
}
func (s *Store) Finish(r Record, raw []byte, requestErr error, definitelyRejected bool) error {
	if s.Hook != nil {
		s.Hook("response_received", r)
	}
	stage, outcome := "acknowledged", "acknowledged"
	if requestErr != nil {
		stage, outcome = "unknown", "response lost or ambiguous"
		if definitelyRejected {
			stage, outcome = "rejected", "explicit rejection"
		}
	}
	var response struct {
		Result struct {
			OrderID string `json:"orderId"`
		} `json:"result"`
	}
	if requestErr == nil {
		json.Unmarshal(raw, &response)
		r.OrderID = response.Result.OrderID
		if r.Kind == "/v5/order/create" && r.OrderID == "" {
			stage, outcome = "unknown", "missing orderId"
		}
		if r.Kind == "/v5/position/trading-stop" {
			stage, outcome = "resolved", "stop acknowledged"
		}
	}
	err := s.update(r, stage, outcome, "", 0)
	if err == nil && s.Hook != nil {
		s.Hook("result_saved", r)
	}
	return err
}
func (s *Store) Resolve(r Record, outcome string) error {
	if s.Hook != nil {
		s.Hook("reconciliation_result", r)
	}
	return s.update(r, "resolved", outcome, r.Status, r.CumExecQty)
}

// ObserveOrder correlates terminal/accepted snapshots with all their exact
// registrations, never a single mutable "current pending" slot.
func (s *Store) ObserveOrder(id, link, status string, cum float64, terminal bool) error {
	return s.ObserveCaptured(s.Snapshot().Records, id, link, status, cum, terminal)
}
func (s *Store) ObserveCaptured(rows []Record, id, link, status string, cum float64, terminal bool) error {
	for _, r := range rows {
		if r.Stage == "resolved" || r.Stage == "rejected" || r.Kind == "/v5/position/trading-stop" || r.Kind == "/v5/order/cancel-all" {
			continue
		}
		if !((r.OrderID != "" && r.OrderID == id && (link == "" || r.Link == link || r.Kind == "external/order")) || (r.Link == link && link != "")) {
			continue
		}
		if r.Kind == "/v5/order/create" && cum > Quantity(r) {
			return fmt.Errorf("observed fill exceeds durable requested quantity")
		}
		r.OrderID = id
		stage := "acknowledged"
		if terminal {
			stage = "resolved"
		}
		if err := s.update(r, stage, "exchange observation", status, cum); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) CompleteRecovery(attempt, revision uint64, audit json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	if attempt != s.recoveryAttempt || revision != s.snapshot.Revision {
		return fmt.Errorf("recovery snapshot changed")
	}
	for _, r := range s.snapshot.Records {
		if r.Stage != "resolved" && r.Stage != "rejected" {
			return fmt.Errorf("unresolved operation %s", r.ID)
		}
	}
	next := clone(s.snapshot)
	next.Audit = append([]byte(nil), audit...)
	next.BlockedAudit = nil
	if err := s.commitLocked(next); err != nil {
		return err
	}
	s.startupComplete = true
	return nil
}

func Quantity(r Record) float64 {
	var p map[string]any
	json.Unmarshal(r.Payload, &p)
	q, _ := p["qty"].(string)
	v, _ := strconv.ParseFloat(q, 64)
	return v
}

// Discover retains exchange orders absent from the local ledger. Such orders
// are never replayed and cannot authorize adoption of an unknown live position.
func (s *Store) Discover(id, link, symbol, side, qty string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	for _, r := range s.snapshot.Records {
		if r.OrderID == id || (link != "" && r.Link == link) {
			return nil
		}
	}
	if s.snapshot.Sequence == ^uint64(0) {
		return fmt.Errorf("generation exhausted")
	}
	g := s.snapshot.Sequence + 1
	opID := fmt.Sprintf("%s-%d", s.snapshot.Instance, g)
	if link == "" {
		link = "external-" + id
	}
	p, _ := json.Marshal(map[string]any{"category": "linear", "symbol": symbol, "side": side, "qty": qty, "orderId": id})
	if err := validatePayload("external/order", p); err != nil {
		return err
	}
	r := Record{ID: opID, Generation: g, Kind: "external/order", Symbol: symbol, Link: link, Owner: Owner{Lifecycle: "external-" + opID}, Payload: p, Stage: "unknown", OrderID: id, CreatedMs: time.Now().UnixMilli()}
	next := clone(s.snapshot)
	next.Sequence = g
	next.Records = append(next.Records, r)
	return s.commitLocked(next)
}

// A blocked audit is diagnostic evidence, never permission to trade.
func (s *Store) SaveBlockedAudit(audit json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	next := clone(s.snapshot)
	next.BlockedAudit = append([]byte(nil), audit...)
	return s.commitLocked(next)
}
