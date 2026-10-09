package operations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var storeOrder = []byte(`{"category":"linear","symbol":"BTCUSDT","side":"Buy","qty":"10","orderLinkId":"stable-one","orderType":"Limit"}`)

func readyStore(t *testing.T, path string) *Store {
	t.Helper()
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.CompleteRecovery(s.BeginRecovery(), s.Snapshot().Revision, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	return s
}
func TestOperationStoreRestartGenerationsAndGate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ops.json")
	s := readyStore(t, p)
	r, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Sending(r); e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if s.CanTrade() == nil || s.Snapshot().Records[0].Stage != "sending" {
		t.Fatal("restart lost uncertainty")
	}
	if e = s.Resolve(r, "terminal proof"); e != nil {
		t.Fatal(e)
	}
	if s.CanTrade() == nil {
		t.Fatal("terminal proof alone bypassed startup checks")
	}
	if e = s.CompleteRecovery(s.BeginRecovery(), s.Snapshot().Revision, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "B"}); e == nil {
		t.Fatal("stable link reused")
	}
	other := []byte(strings.Replace(string(storeOrder), "stable-one", "stable-two", 1))
	r2, e := s.Prepare("/v5/order/create", other, Owner{Lifecycle: "B"})
	if e != nil || r2.Generation <= r.Generation || r2.ID == r.ID {
		t.Fatal("identity reused", r2, e)
	}
	if e = s.Resolve(r, "late A reply"); e != nil {
		t.Fatal(e)
	}
	if s.Snapshot().Records[1].Stage != "intent" {
		t.Fatal("A changed B")
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot permissions", info.Mode())
	}
}
func writeEnvelope(t *testing.T, p string, s Snapshot) {
	t.Helper()
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	data, _ := json.Marshal(map[string]any{"body": json.RawMessage(b), "sha256": hex.EncodeToString(sum[:])})
	if e := os.WriteFile(p, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func TestOperationStoreCorruptionIsNeverEmpty(t *testing.T) {
	for _, kind := range []string{"truncated", "checksum", "version", "missing_records", "duplicate_generation", "temp_debris"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "ops.json")
			s := readyStore(t, p)
			r, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"})
			if e != nil {
				t.Fatal(e)
			}
			snap := s.Snapshot()
			s.Close()
			switch kind {
			case "truncated":
				os.WriteFile(p, []byte(`{"body":`), 0600)
			case "checksum":
				os.WriteFile(p, []byte(`{"body":{},"sha256":"wrong"}`), 0600)
			case "version":
				snap.Version = 99
				writeEnvelope(t, p, snap)
			case "missing_records":
				snap.Records = nil
				writeEnvelope(t, p, snap)
			case "duplicate_generation":
				snap.Records = append(snap.Records, r)
				snap.Sequence++
				writeEnvelope(t, p, snap)
			case "temp_debris":
				os.WriteFile(p+".tmp-crash", []byte("incomplete"), 0600)
			}
			before, _ := os.ReadFile(p)
			if got, e := Open(p); e == nil {
				got.Close()
				t.Fatal("corruption accepted")
			}
			after, _ := os.ReadFile(p)
			if string(before) != string(after) {
				t.Fatal("damaged state overwritten")
			}
		})
	}
}
func TestOperationStoreWriteFailurePoisonAndSingleWriter(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ops.json")
	s := readyStore(t, p)
	defer s.Close()
	if other, e := Open(p); e == nil {
		other.Close()
		t.Fatal("second writer admitted")
	}
	// Replace the destination with a directory: rename must fail even as root.
	os.Remove(p)
	os.Mkdir(p, 0700)
	if _, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"}); e == nil {
		t.Fatal("write failure accepted")
	}
	if s.CanTrade() == nil || s.Diagnostic().Error == "" {
		t.Fatal("write failure did not poison gate")
	}
	if _, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"}); e == nil {
		t.Fatal("poisoned writer retried")
	}
}
func TestOperationStoreRejectsSecretsAndCapturedResults(t *testing.T) {
	s := readyStore(t, filepath.Join(t.TempDir(), "ops.json"))
	defer s.Close()
	bad := []byte(`{"category":"linear","symbol":"BTCUSDT","qty":"1","apiKey":"do-not-persist"}`)
	if _, e := s.Prepare("/v5/order/create", bad, Owner{Lifecycle: "A"}); e == nil || s.Snapshot().Sequence != 0 {
		t.Fatal("secret saved")
	}
	a, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"})
	if e != nil {
		t.Fatal(e)
	}
	s.Sending(a)
	s.Finish(a, []byte(`{"result":{"orderId":"one"}}`), nil, false)
	captured := s.Snapshot().Records
	b, e := s.Prepare("/v5/order/cancel", []byte(`{"category":"linear","symbol":"BTCUSDT","orderId":"one"}`), Owner{Lifecycle: "A"})
	if e != nil {
		t.Fatal(e)
	}
	s.Sending(b)
	if e = s.ObserveCaptured(captured, "one", "stable-one", "Cancelled", 1, true); e != nil {
		t.Fatal(e)
	}
	if s.Snapshot().Records[1].Stage != "sending" {
		t.Fatal("old query resolved cancellation registered later")
	}
}

func TestOperationStoreRecoveryAttemptBarrierAndLostFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ops.json")
	s := readyStore(t, p)
	a := s.BeginRecovery()
	b := s.BeginRecovery()
	if e := s.CompleteRecovery(a, s.Snapshot().Revision, []byte(`{}`)); e == nil || s.CanTrade() == nil {
		t.Fatal("old recovery attempt unblocked newer scan")
	}
	if e := s.CompleteRecovery(b, s.Snapshot().Revision, []byte(`{}`)); e != nil {
		t.Fatal(e)
	}
	r, e := s.Prepare("/v5/order/create", storeOrder, Owner{Lifecycle: "A"})
	if e != nil {
		t.Fatal(e)
	}
	s.BeginRecovery()
	if e = s.Sending(r); e == nil {
		t.Fatal("old prepared mutation sent during recovery")
	}
	s.Close()
	os.Remove(p)
	if got, e := Open(p); e == nil {
		got.Close()
		t.Fatal("lost snapshot silently replaced")
	}
	if _, e = os.Stat(p); !os.IsNotExist(e) {
		t.Fatal("lost state evidence overwritten", e)
	}
}
