package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseAFLStatsUsesNewestPathMetric(t *testing.T) {
	stats, err := parseAFLStats(`
start_time       : 100
last_update      : 300
last_path        : 200
last_find        : 250
fuzzer_pid       : 1234
paths_total      : 10
paths_found      : 2
paths_imported   : 3
`)
	if err != nil {
		t.Fatalf("parse stats: %v", err)
	}
	if got, want := stats.LastNewPath.Unix(), int64(250); got != want {
		t.Fatalf("last new path got %d want %d", got, want)
	}
	if stats.FuzzerPID != 1234 {
		t.Fatalf("fuzzer pid got %d want 1234", stats.FuzzerPID)
	}
	if stats.PathCounter != 15 {
		t.Fatalf("path counter got %d want 15", stats.PathCounter)
	}
}

func TestReadAFLProgressAggregatesMultipleFuzzers(t *testing.T) {
	dir := t.TempDir()
	writeStats(t, filepath.Join(dir, "default", "fuzzer_stats"), `
start_time       : 100
last_update      : 200
last_path        : 150
fuzzer_pid       : 111
paths_total      : 10
`)
	writeStats(t, filepath.Join(dir, "secondary", "fuzzer_stats"), `
start_time       : 110
last_update      : 220
last_find        : 180
fuzzer_pid       : 222
corpus_count     : 9
`)
	progress, err := readAFLProgress(dir)
	if err != nil {
		t.Fatalf("read progress: %v", err)
	}
	if progress.StatsFiles != 2 {
		t.Fatalf("stats files got %d want 2", progress.StatsFiles)
	}
	if got, want := progress.LastNewPath.Unix(), int64(180); got != want {
		t.Fatalf("last new path got %d want %d", got, want)
	}
	if progress.PathCounter != 19 {
		t.Fatalf("path counter got %d want 19", progress.PathCounter)
	}
	if len(progress.PIDs) != 2 || progress.PIDs[0] != 111 || progress.PIDs[1] != 222 {
		t.Fatalf("unexpected pids: %+v", progress.PIDs)
	}
}

func TestProgressTrackerUsesCounterIncreaseAsProgress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "default", "fuzzer_stats")
	writeStats(t, path, `
start_time       : 100
last_update      : 200
last_path        : 100
paths_total      : 10
`)
	tracker := newProgressTracker(dir, time.Unix(1000, 0))
	first, err := tracker.Observe(time.Unix(1000, 0))
	if err != nil {
		t.Fatalf("first observe: %v", err)
	}
	if got, want := first.LastProgress.Unix(), int64(100); got != want {
		t.Fatalf("first last progress got %d want %d", got, want)
	}

	writeStats(t, path, `
start_time       : 100
last_update      : 1100
last_path        : 100
paths_total      : 11
`)
	second, err := tracker.Observe(time.Unix(1100, 0))
	if err != nil {
		t.Fatalf("second observe: %v", err)
	}
	if got, want := second.LastProgress.Unix(), int64(1100); got != want {
		t.Fatalf("counter increase should reset progress: got %d want %d", got, want)
	}
	if second.IdleFor != 0 {
		t.Fatalf("idle after counter progress got %s want 0", second.IdleFor)
	}
}

func TestParseDurationValueSupportsSecondsAndGoDurations(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{raw: "7200", want: 2 * time.Hour},
		{raw: "2h", want: 2 * time.Hour},
		{raw: "30m", want: 30 * time.Minute},
	} {
		got, err := parseDurationValue(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("parse %q got %s want %s", tc.raw, got, tc.want)
		}
	}
}

func writeStats(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir stats dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write stats: %v", err)
	}
}
