package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	mdindex "verbose-fortnight/internal/marketdata/index"
	"verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/internal/marketdata/storage"
)

type orderbookQueryOptions struct {
	Source   string
	Symbol   string
	From     time.Time
	To       time.Time
	Root     string
	Index    string
	Out      string
	Format   string
	Validate bool
}

type orderbookQuerySummary struct {
	RowsScanned       int64
	RowsExported      int64
	ValidRows         int64
	UnavailableRows   int64
	CoveragePct       float64
	TimeMin           time.Time
	TimeMax           time.Time
	FilesUsed         int
	MissingPartitions []string
	Warnings          []string
	Recommendation    string
}

type coverageOptions struct {
	Source       string
	Symbol       string
	From         time.Time
	To           time.Time
	Index        string
	Root         string
	DeepValidate bool
}

type validateOptions struct {
	Source string
	Symbol string
	From   time.Time
	To     time.Time
	Root   string
	Index  string
	Repair bool
}

func main() {
	if len(os.Args) < 2 {
		printUsage(os.Stderr)
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "orderbook":
		var opts orderbookQueryOptions
		opts, err = parseOrderbookQueryFlags(os.Args[2:])
		if err == nil {
			var summary orderbookQuerySummary
			summary, err = runOrderbookQuery(ctx, opts)
			if err == nil {
				summaryOut := io.Writer(os.Stdout)
				if opts.Out == "" {
					summaryOut = os.Stderr
				}
				printOrderbookQuerySummary(summaryOut, summary)
			}
		}
	case "coverage":
		var opts coverageOptions
		opts, err = parseCoverageFlags(os.Args[2:])
		if err == nil {
			var summary mdindex.CoverageSummary
			summary, err = runCoverage(ctx, opts)
			if err == nil {
				printCoverageSummary(os.Stdout, summary)
			}
		}
	case "validate":
		var opts validateOptions
		opts, err = parseValidateFlags(os.Args[2:])
		if err == nil {
			var summary mdindex.ValidationSummary
			summary, err = runValidate(ctx, opts)
			if err == nil {
				printValidationSummary(os.Stdout, summary)
			}
		}
	default:
		printUsage(os.Stderr)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: market-data-query <orderbook|coverage|validate> [flags]")
}

func parseOrderbookQueryFlags(args []string) (orderbookQueryOptions, error) {
	fs := flag.NewFlagSet("market-data-query orderbook", flag.ContinueOnError)
	source := fs.String("source", "bybit", "market data source")
	symbol := fs.String("symbol", "BTCUSDT", "symbol")
	fromRaw := fs.String("from", "", "inclusive RFC3339 start time")
	toRaw := fs.String("to", "", "inclusive RFC3339 end time")
	root := fs.String("root", filepath.Join("artifacts", "marketdata"), "market data root")
	indexPath := fs.String("index", filepath.Join("artifacts", "marketdata", "index.sqlite"), "SQLite partition index")
	out := fs.String("out", "", "output file; stdout when empty")
	format := fs.String("format", "jsonl", "output format: jsonl or csv")
	validate := fs.Bool("validate", false, "validate matching indexed partitions before export")
	if err := fs.Parse(args); err != nil {
		return orderbookQueryOptions{}, err
	}
	from, to, err := parseRequiredRange(*fromRaw, *toRaw)
	if err != nil {
		return orderbookQueryOptions{}, err
	}
	normalizedFormat := strings.ToLower(strings.TrimSpace(*format))
	if normalizedFormat != "jsonl" && normalizedFormat != "csv" {
		return orderbookQueryOptions{}, fmt.Errorf("--format must be jsonl or csv")
	}
	normalizedSymbol := strings.ToUpper(strings.TrimSpace(*symbol))
	if normalizedSymbol == "" {
		return orderbookQueryOptions{}, fmt.Errorf("--symbol is required")
	}
	return orderbookQueryOptions{
		Source:   strings.ToLower(strings.TrimSpace(*source)),
		Symbol:   normalizedSymbol,
		From:     from,
		To:       to,
		Root:     *root,
		Index:    *indexPath,
		Out:      *out,
		Format:   normalizedFormat,
		Validate: *validate,
	}, nil
}

func parseCoverageFlags(args []string) (coverageOptions, error) {
	fs := flag.NewFlagSet("market-data-query coverage", flag.ContinueOnError)
	source := fs.String("source", "bybit", "market data source")
	symbol := fs.String("symbol", "BTCUSDT", "symbol")
	fromRaw := fs.String("from", "", "inclusive RFC3339 start time")
	toRaw := fs.String("to", "", "inclusive RFC3339 end time")
	indexPath := fs.String("index", filepath.Join("artifacts", "marketdata", "index.sqlite"), "SQLite partition index")
	root := fs.String("root", filepath.Join("artifacts", "marketdata"), "market data root")
	deepValidate := fs.Bool("deep-validate", false, "decompress and validate matching partitions")
	if err := fs.Parse(args); err != nil {
		return coverageOptions{}, err
	}
	from, to, err := parseRequiredRange(*fromRaw, *toRaw)
	if err != nil {
		return coverageOptions{}, err
	}
	sym := strings.ToUpper(strings.TrimSpace(*symbol))
	if sym == "" {
		return coverageOptions{}, fmt.Errorf("--symbol is required")
	}
	return coverageOptions{
		Source:       strings.ToLower(strings.TrimSpace(*source)),
		Symbol:       sym,
		From:         from,
		To:           to,
		Index:        *indexPath,
		Root:         *root,
		DeepValidate: *deepValidate,
	}, nil
}

func parseValidateFlags(args []string) (validateOptions, error) {
	fs := flag.NewFlagSet("market-data-query validate", flag.ContinueOnError)
	source := fs.String("source", "bybit", "market data source")
	symbol := fs.String("symbol", "", "optional symbol")
	fromRaw := fs.String("from", "", "optional inclusive RFC3339 start time")
	toRaw := fs.String("to", "", "optional inclusive RFC3339 end time")
	root := fs.String("root", filepath.Join("artifacts", "marketdata"), "market data root")
	indexPath := fs.String("index", filepath.Join("artifacts", "marketdata", "index.sqlite"), "SQLite partition index")
	repair := fs.Bool("repair", false, "repair counters/ranges or mark unreadable partitions corrupted")
	if err := fs.Parse(args); err != nil {
		return validateOptions{}, err
	}
	from, to, err := parseOptionalRange(*fromRaw, *toRaw)
	if err != nil {
		return validateOptions{}, err
	}
	return validateOptions{
		Source: strings.ToLower(strings.TrimSpace(*source)),
		Symbol: strings.ToUpper(strings.TrimSpace(*symbol)),
		From:   from,
		To:     to,
		Root:   *root,
		Index:  *indexPath,
		Repair: *repair,
	}, nil
}

func runOrderbookQuery(ctx context.Context, opts orderbookQueryOptions) (orderbookQuerySummary, error) {
	db, err := mdindex.Open(opts.Index)
	if err != nil {
		return orderbookQuerySummary{}, err
	}
	defer db.Close()
	filter := mdindex.PartitionFilter{
		Source:   opts.Source,
		Kind:     orderbook.DefaultKind,
		Symbol:   opts.Symbol,
		Interval: orderbook.DefaultInterval,
		From:     opts.From,
		To:       opts.To,
		Statuses: []string{mdindex.StatusOpen, mdindex.StatusClosed},
	}
	partitions, err := db.QueryPartitions(ctx, filter)
	if err != nil {
		return orderbookQuerySummary{}, err
	}
	if opts.Validate {
		validation, err := db.Validate(ctx, mdindex.ValidationOptions{Root: opts.Root, Filter: filter})
		if err != nil {
			return orderbookQuerySummary{}, err
		}
		if len(validation.Issues) > 0 {
			fmt.Fprintf(os.Stderr, "warning: %d validation issues found in matching partitions\n", len(validation.Issues))
		}
	}
	out, closeOut, err := openOutput(opts.Out)
	if err != nil {
		return orderbookQuerySummary{}, err
	}
	defer closeOut()
	summary := orderbookQuerySummary{
		FilesUsed:         len(partitions),
		MissingPartitions: mdindex.MissingDailyPartitions(opts.From, opts.To, partitions),
	}
	var csvWriter *csv.Writer
	if opts.Format == "csv" {
		csvWriter = csv.NewWriter(out)
		if err := csvWriter.Write(orderbook.CSVHeader()); err != nil {
			return orderbookQuerySummary{}, err
		}
	}
	for _, partition := range partitions {
		if mdindex.IsOpenGzipPartition(partition) {
			summary.Warnings = append(summary.Warnings, fmt.Sprintf("%s: open_gzip_partition_not_live_query_safe", partition.FilePath))
		}
		if err := exportPartition(opts, partition, out, csvWriter, &summary); err != nil {
			return orderbookQuerySummary{}, err
		}
	}
	if csvWriter != nil {
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return orderbookQuerySummary{}, err
		}
	}
	summary.CoveragePct = coveragePct(summary.ValidRows, summary.RowsExported)
	switch {
	case summary.RowsExported == 0:
		summary.Recommendation = mdindex.RecommendationNoData
	case summary.CoveragePct >= 80 && len(summary.MissingPartitions) == 0:
		summary.Recommendation = mdindex.RecommendationUsableForEnrichment
	default:
		summary.Recommendation = mdindex.RecommendationPartialData
	}
	return summary, nil
}

func exportPartition(opts orderbookQueryOptions, partition mdindex.Partition, out io.Writer, csvWriter *csv.Writer, summary *orderbookQuerySummary) error {
	path := storage.ResolvePath(opts.Root, partition.FilePath)
	reader, err := storage.OpenReader(path)
	if err != nil {
		if mdindex.IsOpenGzipPartition(partition) && isUnexpectedEOF(err) {
			return nil
		}
		return err
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		summary.RowsScanned++
		snapshot, ok, err := orderbook.ParseSnapshot([]byte(raw))
		if err != nil {
			return fmt.Errorf("%s: %w", partition.FilePath, err)
		}
		if !ok || !strings.EqualFold(snapshot.Symbol, opts.Symbol) || !orderbook.InTimeRange(snapshot.Timestamp, opts.From, opts.To) {
			continue
		}
		summary.RowsExported++
		if snapshot.ValidTopBook() {
			summary.ValidRows++
		} else if !snapshot.OrderbookAvailable {
			summary.UnavailableRows++
		}
		if summary.TimeMin.IsZero() || snapshot.Timestamp.Before(summary.TimeMin) {
			summary.TimeMin = snapshot.Timestamp
		}
		if summary.TimeMax.IsZero() || snapshot.Timestamp.After(summary.TimeMax) {
			summary.TimeMax = snapshot.Timestamp
		}
		if csvWriter != nil {
			if err := csvWriter.Write(snapshot.CSVRow()); err != nil {
				return err
			}
			continue
		}
		line, err := snapshot.MarshalJSONLine()
		if err != nil {
			return err
		}
		if _, err := out.Write(line); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if mdindex.IsOpenGzipPartition(partition) && isUnexpectedEOF(err) {
			return nil
		}
		return err
	}
	return nil
}

func runCoverage(ctx context.Context, opts coverageOptions) (mdindex.CoverageSummary, error) {
	db, err := mdindex.Open(opts.Index)
	if err != nil {
		return mdindex.CoverageSummary{}, err
	}
	defer db.Close()
	filter := mdindex.PartitionFilter{
		Source:   opts.Source,
		Kind:     orderbook.DefaultKind,
		Symbol:   opts.Symbol,
		Interval: orderbook.DefaultInterval,
		From:     opts.From,
		To:       opts.To,
	}
	summary, err := db.Coverage(ctx, filter)
	if err != nil {
		return mdindex.CoverageSummary{}, err
	}
	if opts.DeepValidate {
		validation, err := db.Validate(ctx, mdindex.ValidationOptions{Root: opts.Root, Filter: filter})
		if err != nil {
			return mdindex.CoverageSummary{}, err
		}
		if len(validation.Issues) > 0 && summary.Recommendation == mdindex.RecommendationUsableForEnrichment {
			summary.Recommendation = mdindex.RecommendationPartialData
		}
	}
	return summary, nil
}

func runValidate(ctx context.Context, opts validateOptions) (mdindex.ValidationSummary, error) {
	db, err := mdindex.Open(opts.Index)
	if err != nil {
		return mdindex.ValidationSummary{}, err
	}
	defer db.Close()
	return db.Validate(ctx, mdindex.ValidationOptions{
		Root:   opts.Root,
		Repair: opts.Repair,
		Filter: mdindex.PartitionFilter{
			Source:   opts.Source,
			Kind:     orderbook.DefaultKind,
			Symbol:   opts.Symbol,
			Interval: orderbook.DefaultInterval,
			From:     opts.From,
			To:       opts.To,
		},
	})
}

func openOutput(path string) (io.Writer, func(), error) {
	if strings.TrimSpace(path) == "" {
		return os.Stdout, func() {}, nil
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return file, func() { _ = file.Close() }, nil
}

func printOrderbookQuerySummary(w io.Writer, summary orderbookQuerySummary) {
	fmt.Fprintf(w, "rows_scanned: %d\n", summary.RowsScanned)
	fmt.Fprintf(w, "rows_exported: %d\n", summary.RowsExported)
	fmt.Fprintf(w, "valid_rows: %d\n", summary.ValidRows)
	fmt.Fprintf(w, "unavailable_rows: %d\n", summary.UnavailableRows)
	fmt.Fprintf(w, "coverage_pct: %.2f\n", summary.CoveragePct)
	fmt.Fprintf(w, "time_min: %s\n", formatSummaryTime(summary.TimeMin))
	fmt.Fprintf(w, "time_max: %s\n", formatSummaryTime(summary.TimeMax))
	fmt.Fprintf(w, "files_used: %d\n", summary.FilesUsed)
	fmt.Fprintf(w, "missing_partitions: %d", len(summary.MissingPartitions))
	if len(summary.MissingPartitions) > 0 {
		fmt.Fprintf(w, " %s", strings.Join(summary.MissingPartitions, ","))
	}
	fmt.Fprintln(w)
	for _, warning := range summary.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
	fmt.Fprintf(w, "recommendation: %s\n", summary.Recommendation)
}

func printCoverageSummary(w io.Writer, summary mdindex.CoverageSummary) {
	fmt.Fprintf(w, "rows_total: %d\n", summary.RowsTotal)
	fmt.Fprintf(w, "valid_rows: %d\n", summary.ValidRows)
	fmt.Fprintf(w, "unavailable_rows: %d\n", summary.UnavailableRows)
	fmt.Fprintf(w, "coverage_pct: %.2f\n", summary.CoveragePct)
	fmt.Fprintf(w, "time_min: %s\n", formatSummaryTime(summary.TimeMin))
	fmt.Fprintf(w, "time_max: %s\n", formatSummaryTime(summary.TimeMax))
	fmt.Fprintf(w, "files_used: %d\n", summary.FilesUsed)
	fmt.Fprintf(w, "missing_partitions: %d", len(summary.MissingPartitions))
	if len(summary.MissingPartitions) > 0 {
		fmt.Fprintf(w, " %s", strings.Join(summary.MissingPartitions, ","))
	}
	fmt.Fprintln(w)
	for _, warning := range summary.Warnings {
		fmt.Fprintf(w, "warning: %s\n", warning)
	}
	fmt.Fprintf(w, "recommendation: %s\n", summary.Recommendation)
}

func printValidationSummary(w io.Writer, summary mdindex.ValidationSummary) {
	fmt.Fprintf(w, "partitions_checked: %d\n", summary.PartitionsChecked)
	fmt.Fprintf(w, "warnings: %d\n", len(summary.Warnings))
	fmt.Fprintf(w, "issues: %d\n", len(summary.Issues))
	fmt.Fprintf(w, "missing_files: %d\n", summary.MissingFiles)
	fmt.Fprintf(w, "corrupted_files: %d\n", summary.CorruptedFiles)
	fmt.Fprintf(w, "counter_mismatches: %d\n", summary.CounterMismatches)
	fmt.Fprintf(w, "repaired_partitions: %d\n", summary.RepairedPartitions)
	for _, warning := range summary.Warnings {
		fmt.Fprintf(w, "warning: %s: %s\n", warning.FilePath, warning.Message)
	}
	for _, issue := range summary.Issues {
		fmt.Fprintf(w, "- %s: %s\n", issue.FilePath, issue.Message)
	}
}

func parseRequiredRange(fromRaw, toRaw string) (time.Time, time.Time, error) {
	from, to, err := parseOptionalRange(fromRaw, toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if from.IsZero() || to.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("--from and --to are required")
	}
	return from, to, nil
}

func parseOptionalRange(fromRaw, toRaw string) (time.Time, time.Time, error) {
	var from, to time.Time
	var err error
	if strings.TrimSpace(fromRaw) != "" {
		from, err = orderbook.ParseTime(fromRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--from: %w", err)
		}
	}
	if strings.TrimSpace(toRaw) != "" {
		to, err = orderbook.ParseTime(toRaw)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("--to: %w", err)
		}
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("--to must be >= --from")
	}
	return from, to, nil
}

func coveragePct(valid, total int64) float64 {
	if total == 0 {
		return 0
	}
	return float64(valid) / float64(total) * 100
}

func formatSummaryTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339Nano)
}

func isUnexpectedEOF(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected EOF")
}
