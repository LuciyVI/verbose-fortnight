package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	sharedforensics "verbose-fortnight/internal/forensics"
)

func main() {
	fromFlag := flag.String("from", "", "UTC start time (e.g. 2026-02-24T00:00:00Z or 2026-02-24 00:00)")
	toFlag := flag.String("to", "", "UTC end time (e.g. 2026-02-28T23:59:59Z or 2026-02-28 23:59)")
	symbolFlag := flag.String("symbol", "BTCUSDT", "symbol filter")
	reportFlag := flag.String("report", "", "path to pnl report CSV")
	logsFlag := flag.String("logs", "logs", "logs directory or file")
	outFlag := flag.String("out", "/tmp/forensics", "output directory")
	logTZFlag := flag.String("log-tz", "UTC", "timezone for log line timestamps")
	reportTZFlag := flag.String("report-tz", "UTC", "timezone for report time column")
	flag.Parse()

	if *fromFlag == "" || *toFlag == "" || *reportFlag == "" {
		fmt.Fprintln(os.Stderr, "required flags: --from --to --report")
		os.Exit(2)
	}

	logLoc, err := time.LoadLocation(*logTZFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --log-tz: %v\n", err)
		os.Exit(2)
	}
	reportLoc, err := time.LoadLocation(*reportTZFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --report-tz: %v\n", err)
		os.Exit(2)
	}

	fromTS, err := parseTimeFlag(*fromFlag, reportLoc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --from: %v\n", err)
		os.Exit(2)
	}
	toTS, err := parseTimeFlag(*toFlag, reportLoc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --to: %v\n", err)
		os.Exit(2)
	}
	if toTS.Before(fromTS) {
		fmt.Fprintln(os.Stderr, "--to must be >= --from")
		os.Exit(2)
	}

	if err := ensureDir(*outFlag); err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output dir: %v\n", err)
		os.Exit(1)
	}

	reports, err := loadReportCSV(*reportFlag, *symbolFlag, reportLoc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load report failed: %v\n", err)
		os.Exit(1)
	}
	if len(reports) == 0 {
		fmt.Fprintln(os.Stderr, "report has no rows")
		os.Exit(1)
	}

	events, _, err := sharedforensics.LoadEvents(*logsFlag, fromTS, toTS, logLoc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load events failed: %v\n", err)
		os.Exit(1)
	}

	clusters, positionEvents := buildClusters(events, *symbolFlag)
	matches := matchReports(reports, clusters, positionEvents)

	matchTablePath := resolveOutPath(*outFlag, "forensics_match_table.md")
	mismatchPath := resolveOutPath(*outFlag, "forensics_pnl_mismatch.md")
	clustersPath := resolveOutPath(*outFlag, "forensics_clusters.json")

	if err := writeMatchTable(matchTablePath, matches); err != nil {
		fmt.Fprintf(os.Stderr, "write match table failed: %v\n", err)
		os.Exit(1)
	}
	if err := writeMismatchReport(mismatchPath, matches); err != nil {
		fmt.Fprintf(os.Stderr, "write mismatch report failed: %v\n", err)
		os.Exit(1)
	}
	if err := writeClustersJSON(clustersPath, *symbolFlag, fromTS, toTS, clusters, matches); err != nil {
		fmt.Fprintf(os.Stderr, "write clusters json failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("forensics completed: events=%d clusters=%d matches=%d\n", len(events), len(clusters), len(matches))
	fmt.Printf("match_table: %s\n", matchTablePath)
	fmt.Printf("pnl_mismatch: %s\n", mismatchPath)
	fmt.Printf("clusters: %s\n", clustersPath)
}
