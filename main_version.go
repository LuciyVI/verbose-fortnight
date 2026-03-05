package main

import (
	"fmt"
	"io"
	"runtime"
	"strings"
)

var (
	gitCommit = "dev"
	buildTime = "unknown"
)

type versionInfo struct {
	GitCommit string
	BuildTime string
	GoVersion string
}

func currentVersionInfo() versionInfo {
	commit := strings.TrimSpace(gitCommit)
	if commit == "" {
		commit = "dev"
	}
	builtAt := strings.TrimSpace(buildTime)
	if builtAt == "" {
		builtAt = "unknown"
	}
	return versionInfo{
		GitCommit: commit,
		BuildTime: builtAt,
		GoVersion: runtime.Version(),
	}
}

func formatVersionInfo(v versionInfo) string {
	return fmt.Sprintf("build_info commit=%s build_time=%s go=%s", v.GitCommit, v.BuildTime, v.GoVersion)
}

func printVersion(w io.Writer) {
	_, _ = fmt.Fprintln(w, formatVersionInfo(currentVersionInfo()))
}
