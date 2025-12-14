package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsDaemonEnvFlag(t *testing.T) {
	t.Setenv("GO_TRADE_DAEMON", "true")
	if !IsDaemon() {
		t.Fatalf("IsDaemon should return true when GO_TRADE_DAEMON=true")
	}
	t.Setenv("GO_TRADE_DAEMON", "false")
	if IsDaemon() {
		t.Fatalf("IsDaemon should return false when GO_TRADE_DAEMON=false")
	}
}

func TestGetExecutablePathReturnsAbs(t *testing.T) {
	path, err := GetExecutablePath()
	if err != nil {
		t.Fatalf("GetExecutablePath error: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("expected absolute path, got %s", path)
	}
}

func TestStopDaemonMissingPIDFile(t *testing.T) {
	dir := t.TempDir()
	// make sure we run in isolated dir so no real pid file is touched
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(wd)

	if err := StopDaemon(); err == nil {
		t.Fatalf("expected error when pid file is missing")
	}
}

// Note: StartDaemon/RestartDaemon spawn real processes; we avoid starting OS processes in unit tests to keep the suite deterministic and side-effect free.
