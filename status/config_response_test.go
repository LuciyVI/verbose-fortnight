package status

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

func TestConfigEndpointReturnsRedactedConfig(t *testing.T) {
	cfg := &config.Config{
		EnableStatusServer:   true,
		EnableConfigEndpoint: true,
		StatusAddr:           "127.0.0.1:6061",
		APIKey:               "LEAK_ME_KEY",
		APISecret:            "LEAK_ME_SECRET",
		Symbol:               "BTCUSDT",
	}
	state := &models.State{}
	state.SetRuntimeFeatures(models.RuntimeFeatures{
		StatusServer: true,
		DryRun:       true,
	})

	mux := buildStatusMux(cfg, state)
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status code: %d body=%s", rec.Code, rec.Body.String())
	}

	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal /config response: %v", err)
	}

	cfgRaw, ok := decoded["config"]
	if !ok {
		t.Fatalf("missing config section: %v", decoded)
	}
	cfgMap, ok := cfgRaw.(map[string]any)
	if !ok {
		t.Fatalf("config section has unexpected type: %T", cfgRaw)
	}
	if cfgMap["APIKey"] != "***redacted***" || cfgMap["APISecret"] != "***redacted***" {
		t.Fatalf("secrets not redacted: %#v", cfgMap)
	}
	if cfgMap["Symbol"] != "BTCUSDT" {
		t.Fatalf("expected non-secret fields preserved, got Symbol=%v", cfgMap["Symbol"])
	}

	if _, ok := decoded["features"]; !ok {
		t.Fatalf("missing features section")
	}
	if _, ok := decoded["build"]; !ok {
		t.Fatalf("missing build section")
	}
}

func TestConfigEndpointDisabledByFlag(t *testing.T) {
	cfg := &config.Config{
		EnableStatusServer:   true,
		EnableConfigEndpoint: false,
		StatusAddr:           "127.0.0.1:6061",
	}
	state := &models.State{}
	mux := buildStatusMux(cfg, state)
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when /config disabled, got %d", rec.Code)
	}
}
