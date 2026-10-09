package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRESTShutdownHelper(t *testing.T) {
	if os.Getenv("REST_SHUTDOWN_HELPER") != "1" {
		return
	}
	os.Args = []string{"go-trade"}
	main()
}

func TestProcessStopsDuringStalledStartupREST(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRESTShutdownHelper$")
	cmd.Env = append(os.Environ(), "REST_SHUTDOWN_HELPER=1", "BYBIT_DEMO_REST_HOST="+srv.URL, "BYBIT_API_KEY=fake", "BYBIT_API_SECRET=fake", "ENABLE_DRY_RUN=0", "ENABLE_STATUS_SERVER=0", "REST_TIMEOUT_MS=10000", "LOG_FILE="+filepath.Join(t.TempDir(), "shutdown.log"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("child did not reach mocked REST")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("process failed to stop during stalled REST")
	}
}
