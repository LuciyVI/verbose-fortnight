package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func independentChildEnvironment() []string {
	var environment []string
	for _, variable := range os.Environ() {
		switch strings.SplitN(variable, "=", 2)[0] {
		case "PATH", "HOME", "TMPDIR", "TZ", "GOCACHE", "GOMODCACHE", "GOPATH", "GOROOT":
			environment = append(environment, variable)
		}
	}
	return environment
}

// Exercise the actual compiled CLI, with every exchange endpoint redirected to
// a local tripwire and an environment that contains no inherited API keys.
func TestIndependentDryRunProcess(t *testing.T) {
	var requests atomic.Int64
	exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "dry-run contacted exchange", 500)
	}))
	defer exchange.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	binary := filepath.Join(t.TempDir(), "go-trade")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	cmd := exec.Command(binary, "-debug=0")
	cmd.Env = independentChildEnvironment()
	cmd.Env = append(cmd.Env, "ENABLE_DRY_RUN=1", "ENABLE_STATUS_SERVER=1", "ENABLE_CONFIG_ENDPOINT=1", "ENABLE_EXECUTION_BACKFILL=1", "DAEMON_MODE=0", "STATUS_ADDR="+addr,
		"BYBIT_API_KEY=offline-key", "BYBIT_API_SECRET=offline-secret", "BYBIT_DEMO_REST_HOST="+exchange.URL,
		"BYBIT_DEMO_WS_PRIVATE="+strings.Replace(exchange.URL, "http://", "ws://", 1), "BYBIT_DEMO_WS_PUBLIC="+strings.Replace(exchange.URL, "http://", "ws://", 1), "LOG_FILE="+filepath.Join(t.TempDir(), "dry.log"))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			cmd.Process.Kill()
			<-done
		}
	}()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	var status map[string]any
	ready := false
	for !ready {
		select {
		case err := <-done:
			waited = true
			t.Fatalf("early child exit: %v output=%s", err, output.String())
		case <-deadline.C:
			t.Fatal("dry-run diagnostics never advanced")
		case <-ticker.C:
			response, err := client.Get("http://" + addr + "/status")
			if err != nil {
				continue
			}
			err = json.NewDecoder(response.Body).Decode(&status)
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			counters, ok := status["counters"].(map[string]any)
			ready = ok && counters["dryRunTicks"].(float64) >= 1
		}
	}
	features := status["features"].(map[string]any)
	health := status["health"].(map[string]any)
	counters := status["counters"].(map[string]any)
	if features["dryRun"] != true || features["executionBackfill"] != true || fmt.Sprint(health["lastDryRunTickTs"]) == "0001-01-01T00:00:00Z" || fmt.Sprint(health["lastBackfillCycleTs"]) == "0001-01-01T00:00:00Z" || counters["backfillFetched"] != float64(0) {
		t.Errorf("incorrect dry-run diagnostics: %+v", status)
	}
	response, err := client.Get("http://" + addr + "/config")
	if err != nil {
		t.Fatal(err)
	}
	var configResponse map[string]any
	err = json.NewDecoder(response.Body).Decode(&configResponse)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	configMap := configResponse["config"].(map[string]any)
	for key, want := range map[string]any{"OrderBalancePct": float64(1), "RESTTimeoutMs": float64(5000), "RESTMaxResponseBytes": float64(1048576), "OrderReconcileTimeoutMs": float64(3000), "MakerTimeoutMs": float64(800), "LogLevel": float64(0)} {
		if got := configMap[key]; got != want {
			t.Errorf("documented default/flag %s: got=%v want=%v", key, got, want)
		}
	}
	if configMap["APIKey"] != "***redacted***" || configMap["APISecret"] != "***redacted***" {
		t.Error("/config exposed offline credentials")
	}
	started := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		waited = true
		if err != nil {
			t.Errorf("SIGTERM exit=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SIGTERM shutdown exceeded 2s")
	}
	if requests.Load() != 0 {
		t.Errorf("dry-run exchange calls=%d", requests.Load())
	}
	encoded, _ := json.Marshal(status)
	t.Logf("exchangeCalls=%d SIGTERM=%s exit=%v defaults/flags verified status=%s", requests.Load(), time.Since(started), cmd.ProcessState, encoded)
}

func TestIndependentStartupRESTSIGTERM(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "go-trade")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	for _, stage := range []string{"before_headers", "during_body"} {
		t.Run(stage, func(t *testing.T) {
			reached, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			exchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if calls.Add(1) != 1 {
					http.Error(w, "unexpected extra request", 500)
					return
				}
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
			defer exchange.Close()
			defer close(release)
			cmd := exec.Command(binary, "-debug=0")
			cmd.Env = append(independentChildEnvironment(), "ENABLE_DRY_RUN=0", "ENABLE_STATUS_SERVER=0", "REST_TIMEOUT_MS=10000", "BYBIT_API_KEY=offline-key", "BYBIT_API_SECRET=offline-secret", "BYBIT_DEMO_REST_HOST="+exchange.URL,
				"BYBIT_DEMO_WS_PRIVATE="+strings.Replace(exchange.URL, "http://", "ws://", 1), "BYBIT_DEMO_WS_PUBLIC="+strings.Replace(exchange.URL, "http://", "ws://", 1), "LOG_FILE="+filepath.Join(t.TempDir(), "startup.log"))
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				if !waited {
					cmd.Process.Kill()
					<-done
				}
			}()
			select {
			case <-reached:
			case <-time.After(3 * time.Second):
				t.Fatal("CLI never reached local startup REST")
			}
			started := time.Now()
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				waited = true
				t.Logf("stage=%s SIGTERM=%s exit=%v requests=%d output=%s", stage, time.Since(started), err, calls.Load(), output.String())
				// Go versions differ in whether NotifyContext reports Canceled or
				// the signal as its cancellation cause. Both must reach the log.
				cancellationReported := strings.Contains(output.String(), "context canceled") || strings.Contains(output.String(), "terminated signal received")
				if err == nil || !cancellationReported || strings.Contains(output.String(), "API connection established successfully") || calls.Load() != 1 {
					t.Error("startup cancellation was lost/reported successful/retried")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("CLI failed to stop during stalled REST")
			}
		})
	}
}
