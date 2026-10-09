package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/logging"
)

type independentAPILog struct {
	mu    sync.Mutex
	lines []string
}

func (l *independentAPILog) write(f string, a ...interface{}) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
	l.mu.Unlock()
}
func (l *independentAPILog) Debug(f string, a ...interface{})   { l.write(f, a...) }
func (l *independentAPILog) Info(f string, a ...interface{})    { l.write(f, a...) }
func (l *independentAPILog) Warning(f string, a ...interface{}) { l.write(f, a...) }
func (l *independentAPILog) Error(f string, a ...interface{})   { l.write(f, a...) }
func (l *independentAPILog) Fatal(f string, a ...interface{})   { l.write(f, a...) }
func (l *independentAPILog) Sync() error                        { return nil }
func (l *independentAPILog) ChangeLogLevel(logging.LogLevel)    {}
func independentAPICfg(host string) *config.Config {
	cfg := config.LoadConfig()
	cfg.APIKey = "offline-key"
	cfg.APISecret = "offline-secret"
	cfg.DemoRESTHost = host
	return cfg
}
func independentAPIInt(cfg *config.Config, name string, value int64) {
	f := reflect.ValueOf(cfg).Elem().FieldByName(name)
	if f.IsValid() {
		f.SetInt(value)
	}
}

func TestIndependentRESTResponseMatrix(t *testing.T) {
	const good = `{"retCode":0,"result":{"list":[{"totalAvailableBalance":"10"}]}}`
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		truncated bool
	}{
		{"429", 429, good, false}, {"500", 500, good, false},
		{"oversize", 200, strings.TrimSuffix(good, "}") + `,"padding":"` + strings.Repeat("x", 2048) + `"}`, false},
		{"body_truncated_after_valid_JSON", 200, good, true},
		{"invalid_JSON", 200, `{"retCode":`, false},
		{"retCode_nonzero", 200, `{"retCode":10001,"retMsg":"invalid request"}`, false},
		{"missing_retCode", 200, `{"result":{"list":[{"totalAvailableBalance":"10"}]}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.truncated {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)+16))
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			cfg := independentAPICfg(srv.URL)
			independentAPIInt(cfg, "RESTMaxResponseBytes", 256)
			log := &independentAPILog{}
			balance, err := NewRESTClient(cfg, log).GetBalance("USDT")
			t.Logf("balance=%g err=%v", balance, err)
			if err == nil {
				t.Errorf("invalid HTTP/body/envelope reported successful balance %g", balance)
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			for _, line := range log.lines {
				if strings.Contains(line, " OK") || strings.Contains(line, "successfully") {
					t.Errorf("error reported as success: %s", line)
				}
			}
		})
	}
}

func TestIndependentRESTDeadlineMatrix(t *testing.T) {
	for _, stage := range []string{"before_headers", "during_body", "context_cancel"} {
		t.Run(stage, func(t *testing.T) {
			reached, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if stage == "during_body" {
					fmt.Fprint(w, `{"retCode":0,`)
					w.(http.Flusher).Flush()
				}
				close(reached)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer srv.Close()
			defer close(release)
			cfg := independentAPICfg(srv.URL)
			independentAPIInt(cfg, "RESTTimeoutMs", 30)
			client := NewRESTClient(cfg, nil)
			injected := reflect.ValueOf(client).Elem().FieldByName("HTTPClient")
			if injected.IsValid() {
				injected.Set(reflect.ValueOf(&http.Client{Timeout: 0}))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f := reflect.ValueOf(client).Elem().FieldByName("Context")
			if f.IsValid() {
				f.Set(reflect.ValueOf(ctx))
			}
			done := make(chan error, 1)
			began := time.Now()
			go func() { _, err := client.GetBalance("USDT"); done <- err }()
			select {
			case <-reached:
			case <-time.After(time.Second):
				t.Fatal("server not reached")
			}
			if stage == "context_cancel" {
				cancel()
			}
			select {
			case err := <-done:
				t.Logf("elapsed=%s err=%v", time.Since(began), err)
				if err == nil {
					t.Error("stalled request returned success")
				}
				if stage == "context_cancel" && !errors.Is(err, context.Canceled) {
					t.Errorf("context error not propagated: %v", err)
				}
			case <-time.After(500 * time.Millisecond):
				t.Error("request did not finish within bounded oracle")
			}
		})
	}
}

func TestIndependentInstrumentBoundaryValidation(t *testing.T) {
	for _, raw := range []string{"0", "-0.1", "NaN", "+Inf", "-Inf", "not-a-number"} {
		t.Run(raw, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"lotSizeFilter":{"qtyStep":%q,"minOrderQty":"0.1","minNotionalValue":"0"},"priceFilter":{"tickSize":"0.1"}}]}}`, raw)
			}))
			defer srv.Close()
			info, err := NewRESTClient(independentAPICfg(srv.URL), nil).GetInstrumentInfo("OFFLINE")
			if err == nil {
				t.Errorf("invalid instrument step reached trading boundary: %+v", info)
			}
		})
	}
}
