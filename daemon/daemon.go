package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// IsDaemon checks if the process is running as a daemon/background process
func IsDaemon() bool {
	// On Unix systems, we can check if stdin, stdout, stderr are redirected
	// For Go applications, we'll consider it a daemon if a specific environment variable is set
	return os.Getenv("GO_TRADE_DAEMON") == "true"
}

// StartDaemon starts the application as a background process
func StartDaemon(args []string) error {
	// Get the current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Set up the command with the same arguments
	cmd := exec.Command(execPath, args...)

	// Set environment variable to indicate daemon mode
	env := os.Environ()
	env = append(env, "GO_TRADE_DAEMON=true")
	cmd.Env = env

	// Setup file descriptors for background execution
	if runtime.GOOS != "windows" {
		// On Unix-like systems, redirect stdin, stdout, and stderr
		cmd.Stdin = nil
		// Create log file for output redirection
		// We'll let the logging package handle this instead of redirecting here
	}

	// Start the process
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	// Write PID to file for process management
	pidFile := "go-trade.pid"
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	fmt.Printf("Daemon started with PID: %d. PID file saved as %s\n", cmd.Process.Pid, pidFile)
	return nil
}

// StopDaemon stops the background process
func StopDaemon() error {
	pidFile := "go-trade.pid"
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	var pid int
	_, err = fmt.Sscanf(string(pidData), "%d", &pid)
	if err != nil {
		return fmt.Errorf("failed to parse PID: %w", err)
	}

	// Try to kill the process
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("failed to find process: %w", err)
	}

	if err := process.Kill(); err != nil {
		return fmt.Errorf("failed to kill process: %w", err)
	}

	// Remove the PID file
	if err := os.Remove(pidFile); err != nil {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}

	fmt.Printf("Daemon with PID %d has been stopped.\n", pid)
	return nil
}

// RestartDaemon restarts the daemon process
func RestartDaemon(args []string) error {
	if err := StopDaemon(); err != nil {
		fmt.Printf("Warning: Could not stop daemon: %v\n", err)
		// Continue trying to start anyway
	}

	return StartDaemon(args)
}

// GetExecutablePath returns the current executable path
func GetExecutablePath() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(execPath)
}