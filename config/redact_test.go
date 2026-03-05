package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactedMapMasksSecrets(t *testing.T) {
	cfg := &Config{
		APIKey:           "MY-SUPER-SECRET-KEY-123",
		APISecret:        "MY-SUPER-SECRET-SECRET-456",
		Symbol:           "BTCUSDT",
		StatusAddr:       "127.0.0.1:6061",
		EnableDryRun:     true,
		DemoRESTHost:     "https://api-demo.bybit.com",
		EnableEdgeFilter: true,
	}

	redacted := RedactedMap(cfg)

	if got := redacted["APIKey"]; got != redactedValue {
		t.Fatalf("APIKey must be redacted, got %v", got)
	}
	if got := redacted["APISecret"]; got != redactedValue {
		t.Fatalf("APISecret must be redacted, got %v", got)
	}
	if got := redacted["Symbol"]; got != "BTCUSDT" {
		t.Fatalf("non-secret field mismatch, got %v", got)
	}
}

func TestRedactedJSONDoesNotLeakSecrets(t *testing.T) {
	cfg := &Config{
		APIKey:    "SENSITIVE_KEY_VALUE",
		APISecret: "SENSITIVE_SECRET_VALUE",
		Symbol:    "BTCUSDT",
	}
	raw, err := RedactedJSON(cfg)
	if err != nil {
		t.Fatalf("RedactedJSON error: %v", err)
	}
	blob := string(raw)
	for _, secret := range []string{"SENSITIVE_KEY_VALUE", "SENSITIVE_SECRET_VALUE"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("redacted json leaked secret %q: %s", secret, blob)
		}
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal redacted json: %v", err)
	}
	if decoded["APIKey"] != redactedValue || decoded["APISecret"] != redactedValue {
		t.Fatalf("expected redacted keys in json, got %#v", decoded)
	}
}
