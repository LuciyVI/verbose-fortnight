package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"verbose-fortnight/config"
)

func TestRESTDeadlinesAndCancellation(t *testing.T) {
	for _, stage := range []string{"headers", "body", "context"} {
		t.Run(stage, func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "body" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"retCode":0,`)
					w.(http.Flusher).Flush()
				}
				close(started)
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer srv.Close()
			defer close(release)
			cfg := config.LoadConfig()
			cfg.DemoRESTHost = srv.URL
			cfg.RESTTimeoutMs = 40
			c := NewRESTClient(cfg, nil)
			// Even an injected client with no client timeout is bounded by the request context.
			c.HTTPClient = &http.Client{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.Context = ctx
			done := make(chan error, 1)
			go func() { _, err := c.GetBalance("USDT"); done <- err }()
			<-started
			if stage == "context" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stalled request succeeded")
				}
				if stage == "context" && !errors.Is(err, context.Canceled) {
					t.Fatalf("expected context cancellation: %v", err)
				}
				if stage != "context" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expected deadline: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request did not terminate")
			}
		})
	}
}

func TestRESTInvalidResponses(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"malformed", `{"retCode":`}, {"retCode", `{"retCode":10001,"retMsg":"rejected"}`}, {"missing_code", `{"result":{}}`}, {"oversize", strings.Repeat("x", 80)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			cfg := config.LoadConfig()
			cfg.DemoRESTHost = srv.URL
			cfg.RESTMaxResponseBytes = 64
			if _, err := NewRESTClient(cfg, nil).GetBalance("USDT"); err == nil {
				t.Fatal("invalid response succeeded")
			}
		})
	}
	c := NewRESTClient(&config.Config{DemoRESTHost: "://invalid"}, nil)
	if _, err := c.GetBalance("USDT"); err == nil {
		t.Fatal("invalid request URL accepted")
	}
}

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (failingBody) Close() error             { return nil }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRESTReadFailure(t *testing.T) {
	c := NewRESTClient(config.LoadConfig(), nil)
	c.HTTPClient = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: failingBody{}, Header: make(http.Header)}, nil
	})}
	if _, err := c.GetBalance("USDT"); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("body read error lost: %v", err)
	}
}
