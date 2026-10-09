package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
)

func TestIndependentRecoveryOlderScanCannotUnblockNewScan(t *testing.T) {
	reached := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	release := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v5/order/realtime" {
			n := int(calls.Add(1)) - 1
			if n < 2 {
				close(reached[n])
				select {
				case <-release[n]:
				case <-r.Context().Done():
					return
				}
			}
		}
		fmt.Fprint(w, `{"retCode":0,"result":{"list":[]}}`)
	}))
	defer srv.Close()
	cfg := config.LoadConfig()
	cfg.APIKey = "offline-key"
	cfg.APISecret = "offline-secret"
	cfg.DemoRESTHost = srv.URL
	cfg.StartupReconcileTimeoutMs = 2000
	cfg.RESTTimeoutMs = 1000
	store, err := operations.Open(filepath.Join(t.TempDir(), "ops.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := NewRESTClient(cfg, nil)
	c.Operations = store
	start := func() <-chan error {
		done := make(chan error, 1)
		go func() { _, err := c.ReconcileStartup(context.Background()); done <- err }()
		return done
	}
	wait := func(ch <-chan struct{}) {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("scan did not reach barrier")
		}
	}
	a := start()
	wait(reached[0])
	b := start()
	wait(reached[1])
	close(release[0])
	select {
	case err := <-a:
		if err == nil {
			t.Fatal("older scan accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("older scan did not complete")
	}
	if store.CanTrade() == nil {
		t.Fatal("older scan released gate while newer scan was pending")
	}
	close(release[1])
	select {
	case err := <-b:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("newer scan did not complete")
	}
	if store.CanTrade() != nil {
		t.Fatal("completed current scan remained blocked")
	}
}
