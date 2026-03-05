package main

import (
	"strings"
	"testing"
)

func TestVersionFieldsNonEmptyWhenSet(t *testing.T) {
	prevCommit := gitCommit
	prevBuild := buildTime
	gitCommit = "abc123"
	buildTime = "2026-03-02T00:00:00Z"
	t.Cleanup(func() {
		gitCommit = prevCommit
		buildTime = prevBuild
	})

	info := currentVersionInfo()
	if info.GitCommit != "abc123" {
		t.Fatalf("git commit mismatch: got %q", info.GitCommit)
	}
	if info.BuildTime != "2026-03-02T00:00:00Z" {
		t.Fatalf("build time mismatch: got %q", info.BuildTime)
	}
	if strings.TrimSpace(info.GoVersion) == "" {
		t.Fatalf("go version must not be empty")
	}

	line := formatVersionInfo(info)
	for _, needle := range []string{"commit=abc123", "build_time=2026-03-02T00:00:00Z", "go="} {
		if !strings.Contains(line, needle) {
			t.Fatalf("formatted version missing %q: %q", needle, line)
		}
	}
}
