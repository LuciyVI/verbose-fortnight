package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultAFLStatsDir           = "fuzz/out"
	defaultAFLIdleTimeout        = 2 * time.Hour
	defaultAFLPollInterval       = time.Minute
	defaultAFLTerminateTimeout   = 30 * time.Second
	defaultAFLNoProgressExitCode = 124
)

type watchdogConfig struct {
	StatsDir         string
	IdleTimeout      time.Duration
	PollInterval     time.Duration
	TerminateTimeout time.Duration
	NoProgressExit   int
	Once             bool
	DryRun           bool
}

type aflStats struct {
	Path        string
	FuzzerPID   int
	StartTime   time.Time
	LastUpdate  time.Time
	LastNewPath time.Time
	PathCounter int64
}

type aflProgress struct {
	StatsFiles  int
	PIDs        []int
	StartTime   time.Time
	LastUpdate  time.Time
	LastNewPath time.Time
	PathCounter int64
}

type progressObservation struct {
	Progress     aflProgress
	LastProgress time.Time
	IdleFor      time.Duration
}

type progressTracker struct {
	statsDir     string
	startedAt    time.Time
	lastProgress time.Time
	lastCounter  int64
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, childArgs, err := parseWatchdogArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if cfg.Once {
		return runOnce(cfg, stdout, stderr)
	}
	if len(childArgs) == 0 {
		return monitorExternalFuzzers(cfg, stdout, stderr)
	}
	return monitorChildFuzzer(cfg, childArgs, stdout, stderr)
}

func parseWatchdogArgs(args []string) (watchdogConfig, []string, error) {
	fs := flag.NewFlagSet("afl-idle-watchdog", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	statsDir := fs.String("stats-dir", getenvString("AFL_STATS_DIR", defaultAFLStatsDir), "AFL++ output directory containing fuzzer_stats files")
	idleTimeout := fs.String("idle-timeout", getenvString("AFL_IDLE_TIMEOUT", defaultAFLIdleTimeout.String()), "stop when no new path is observed for this duration")
	pollInterval := fs.String("poll-interval", getenvString("AFL_WATCHDOG_POLL_INTERVAL", defaultAFLPollInterval.String()), "AFL++ metrics polling interval")
	terminateTimeout := fs.String("terminate-timeout", getenvString("AFL_WATCHDOG_TERMINATE_TIMEOUT", defaultAFLTerminateTimeout.String()), "time between SIGTERM and SIGKILL")
	noProgressExit := fs.Int("no-progress-exit-code", getenvInt("AFL_WATCHDOG_EXIT_CODE", defaultAFLNoProgressExitCode), "exit code used when idle timeout stops fuzzing")
	once := fs.Bool("once", false, "check AFL++ metrics once and exit")
	dryRun := fs.Bool("dry-run", false, "report idle timeout without sending signals")
	if err := fs.Parse(args); err != nil {
		return watchdogConfig{}, nil, err
	}

	idle, err := parseDurationValue(*idleTimeout)
	if err != nil {
		return watchdogConfig{}, nil, fmt.Errorf("invalid --idle-timeout: %w", err)
	}
	poll, err := parseDurationValue(*pollInterval)
	if err != nil {
		return watchdogConfig{}, nil, fmt.Errorf("invalid --poll-interval: %w", err)
	}
	term, err := parseDurationValue(*terminateTimeout)
	if err != nil {
		return watchdogConfig{}, nil, fmt.Errorf("invalid --terminate-timeout: %w", err)
	}
	if idle <= 0 {
		return watchdogConfig{}, nil, fmt.Errorf("--idle-timeout must be positive")
	}
	if poll <= 0 {
		return watchdogConfig{}, nil, fmt.Errorf("--poll-interval must be positive")
	}
	if term < 0 {
		return watchdogConfig{}, nil, fmt.Errorf("--terminate-timeout must not be negative")
	}

	return watchdogConfig{
		StatsDir:         strings.TrimSpace(*statsDir),
		IdleTimeout:      idle,
		PollInterval:     poll,
		TerminateTimeout: term,
		NoProgressExit:   *noProgressExit,
		Once:             *once,
		DryRun:           *dryRun,
	}, fs.Args(), nil
}

func runOnce(cfg watchdogConfig, stdout, stderr io.Writer) int {
	tracker := newProgressTracker(cfg.StatsDir, time.Now())
	obs, err := tracker.Observe(time.Now())
	if err != nil {
		fmt.Fprintf(stderr, "afl metrics unavailable: %v\n", err)
		return 1
	}
	writeObservation(stdout, obs, cfg.IdleTimeout)
	if obs.IdleFor >= cfg.IdleTimeout {
		fmt.Fprintf(stderr, "AFL++ idle timeout reached: no new path for %s (threshold %s)\n", obs.IdleFor.Round(time.Second), cfg.IdleTimeout)
		if !cfg.DryRun {
			terminatePIDs(obs.Progress.PIDs, cfg.TerminateTimeout, stderr)
		}
		return cfg.NoProgressExit
	}
	return 0
}

func monitorExternalFuzzers(cfg watchdogConfig, stdout, stderr io.Writer) int {
	tracker := newProgressTracker(cfg.StatsDir, time.Now())
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		obs, err := tracker.Observe(time.Now())
		if err != nil {
			fmt.Fprintf(stderr, "afl metrics unavailable: %v\n", err)
		} else {
			writeObservation(stdout, obs, cfg.IdleTimeout)
			if obs.IdleFor >= cfg.IdleTimeout {
				fmt.Fprintf(stderr, "AFL++ idle timeout reached: no new path for %s (threshold %s)\n", obs.IdleFor.Round(time.Second), cfg.IdleTimeout)
				if !cfg.DryRun {
					terminatePIDs(obs.Progress.PIDs, cfg.TerminateTimeout, stderr)
				}
				return cfg.NoProgressExit
			}
		}
		<-ticker.C
	}
}

func monitorChildFuzzer(cfg watchdogConfig, childArgs []string, stdout, stderr io.Writer) int {
	cmd := exec.Command(childArgs[0], childArgs[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "start fuzz command: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "started fuzz command pid=%d stats_dir=%s idle_timeout=%s\n", cmd.Process.Pid, cfg.StatsDir, cfg.IdleTimeout)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tracker := newProgressTracker(cfg.StatsDir, time.Now())
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return exitCodeFromWait(err)
		case <-ticker.C:
			obs, err := tracker.Observe(time.Now())
			if err != nil {
				fmt.Fprintf(stderr, "afl metrics unavailable: %v\n", err)
				continue
			}
			writeObservation(stdout, obs, cfg.IdleTimeout)
			if obs.IdleFor < cfg.IdleTimeout {
				continue
			}
			fmt.Fprintf(stderr, "AFL++ idle timeout reached: no new path for %s (threshold %s)\n", obs.IdleFor.Round(time.Second), cfg.IdleTimeout)
			if !cfg.DryRun {
				terminateProcessGroup(cmd.Process.Pid, cfg.TerminateTimeout, stderr)
			}
			select {
			case <-done:
			case <-time.After(cfg.TerminateTimeout + time.Second):
			}
			return cfg.NoProgressExit
		}
	}
}

func newProgressTracker(statsDir string, now time.Time) *progressTracker {
	return &progressTracker{statsDir: statsDir, startedAt: now}
}

func (t *progressTracker) Observe(now time.Time) (progressObservation, error) {
	progress, err := readAFLProgress(t.statsDir)
	if err != nil {
		return progressObservation{}, err
	}
	metricProgressAt := progress.LastNewPath
	if metricProgressAt.IsZero() {
		metricProgressAt = progress.StartTime
	}
	if metricProgressAt.IsZero() {
		metricProgressAt = t.startedAt
	}
	if t.lastProgress.IsZero() {
		t.lastProgress = metricProgressAt
		t.lastCounter = progress.PathCounter
	}
	if progress.PathCounter > t.lastCounter {
		t.lastCounter = progress.PathCounter
		t.lastProgress = now
	}
	if metricProgressAt.After(t.lastProgress) {
		t.lastProgress = metricProgressAt
	}
	return progressObservation{
		Progress:     progress,
		LastProgress: t.lastProgress,
		IdleFor:      now.Sub(t.lastProgress),
	}, nil
}

func readAFLProgress(statsDir string) (aflProgress, error) {
	files, err := discoverStatsFiles(statsDir)
	if err != nil {
		return aflProgress{}, err
	}
	if len(files) == 0 {
		return aflProgress{}, fmt.Errorf("no fuzzer_stats files under %s", statsDir)
	}
	progress := aflProgress{StatsFiles: len(files)}
	pids := map[int]bool{}
	for _, path := range files {
		stats, err := parseStatsFile(path)
		if err != nil {
			return aflProgress{}, err
		}
		if stats.FuzzerPID > 0 {
			pids[stats.FuzzerPID] = true
		}
		if stats.StartTime.After(progress.StartTime) {
			progress.StartTime = stats.StartTime
		}
		if stats.LastUpdate.After(progress.LastUpdate) {
			progress.LastUpdate = stats.LastUpdate
		}
		if stats.LastNewPath.After(progress.LastNewPath) {
			progress.LastNewPath = stats.LastNewPath
		}
		progress.PathCounter += stats.PathCounter
	}
	for pid := range pids {
		progress.PIDs = append(progress.PIDs, pid)
	}
	sort.Ints(progress.PIDs)
	return progress, nil
}

func discoverStatsFiles(root string) ([]string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("stats dir is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if filepath.Base(root) == "fuzzer_stats" {
			return []string{root}, nil
		}
		return nil, fmt.Errorf("%s is not a directory or fuzzer_stats file", root)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if d.Name() == "fuzzer_stats" {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func parseStatsFile(path string) (aflStats, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return aflStats{}, err
	}
	stats, err := parseAFLStats(string(raw))
	if err != nil {
		return aflStats{}, fmt.Errorf("%s: %w", path, err)
	}
	stats.Path = path
	return stats, nil
}

func parseAFLStats(raw string) (aflStats, error) {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if len(values) == 0 {
		return aflStats{}, errors.New("empty AFL++ stats")
	}
	stats := aflStats{
		FuzzerPID:  intField(values, "fuzzer_pid"),
		StartTime:  unixTimeField(values, "start_time"),
		LastUpdate: unixTimeField(values, "last_update"),
	}
	lastFind := unixTimeField(values, "last_find")
	lastPath := unixTimeField(values, "last_path")
	if lastFind.After(lastPath) {
		stats.LastNewPath = lastFind
	} else {
		stats.LastNewPath = lastPath
	}
	stats.PathCounter = sumIntFields(values,
		"paths_total",
		"paths_found",
		"paths_imported",
		"corpus_count",
		"saved_paths",
		"queued_paths",
	)
	return stats, nil
}

func writeObservation(w io.Writer, obs progressObservation, threshold time.Duration) {
	fmt.Fprintf(w, "afl metrics: stats_files=%d pids=%v path_counter=%d last_new_path=%s last_progress=%s idle_for=%s threshold=%s\n",
		obs.Progress.StatsFiles,
		obs.Progress.PIDs,
		obs.Progress.PathCounter,
		formatTime(obs.Progress.LastNewPath),
		formatTime(obs.LastProgress),
		obs.IdleFor.Round(time.Second),
		threshold,
	)
}

func terminatePIDs(pids []int, timeout time.Duration, stderr io.Writer) {
	if len(pids) == 0 {
		fmt.Fprintln(stderr, "no AFL++ fuzzer_pid values found to terminate")
		return
	}
	for _, pid := range pids {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	time.Sleep(timeout)
	for _, pid := range pids {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func terminateProcessGroup(pid int, timeout time.Duration, stderr io.Writer) {
	if pid <= 0 {
		return
	}
	pgid := -pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	time.Sleep(timeout)
	if processAlive(pid) {
		_ = syscall.Kill(pgid, syscall.SIGKILL)
	}
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

func exitCodeFromWait(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func unixTimeField(values map[string]string, key string) time.Time {
	seconds := int64Field(values, key)
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func intField(values map[string]string, key string) int {
	return int(int64Field(values, key))
}

func int64Field(values map[string]string, key string) int64 {
	raw := strings.TrimSpace(values[key])
	if raw == "" {
		return 0
	}
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func sumIntFields(values map[string]string, keys ...string) int64 {
	var total int64
	for _, key := range keys {
		total += int64Field(values, key)
	}
	return total
}

func parseDurationValue(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("duration is empty")
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	return time.ParseDuration(raw)
}

func getenvString(key, defaultValue string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return defaultValue
}

func getenvInt(key string, defaultValue int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return defaultValue
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return value
}
