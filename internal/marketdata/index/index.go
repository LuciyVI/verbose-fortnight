package index

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/internal/marketdata/storage"

	_ "modernc.org/sqlite"
)

const (
	StatusOpen      = "open"
	StatusClosed    = "closed"
	StatusCorrupted = "corrupted"

	RecommendationUsableForEnrichment = "USABLE_FOR_ENRICHMENT"
	RecommendationPartialData         = "PARTIAL_DATA"
	RecommendationNoData              = "NO_DATA"

	TimeLayout = "2006-01-02T15:04:05.000000000Z"
)

type DB struct {
	db *sql.DB
}

type Partition struct {
	ID              int64
	Source          string
	Kind            string
	Symbol          string
	Interval        string
	FilePath        string
	TimeMin         time.Time
	TimeMax         time.Time
	RowsTotal       int64
	ValidRows       int64
	UnavailableRows int64
	CoveragePct     float64
	Compressed      bool
	Compression     string
	SHA256          string
	FileSizeBytes   int64
	Status          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type PartitionFilter struct {
	Source   string
	Kind     string
	Symbol   string
	Interval string
	From     time.Time
	To       time.Time
	Statuses []string
}

type CoverageSummary struct {
	RowsTotal         int64
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

type ValidationOptions struct {
	Root   string
	Filter PartitionFilter
	Repair bool
}

type ValidationIssue struct {
	FilePath string
	Message  string
}

type ValidationSummary struct {
	PartitionsChecked  int
	Warnings           []ValidationIssue
	Issues             []ValidationIssue
	MissingFiles       int
	CorruptedFiles     int
	CounterMismatches  int
	RepairedPartitions int
}

func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		path = filepath.Join("artifacts", "marketdata", "index.sqlite")
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	out := &DB{db: db}
	if err := out.EnsureSchema(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return out, nil
}

func OpenDB(db *sql.DB) *DB {
	return &DB{db: db}
}

func (d *DB) Close() error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

func (d *DB) EnsureSchema(ctx context.Context) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("nil index db")
	}
	_, err := d.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS marketdata_partitions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source TEXT NOT NULL,
  kind TEXT NOT NULL,
  symbol TEXT NOT NULL,
  interval TEXT NOT NULL,
  file_path TEXT NOT NULL UNIQUE,
  time_min TEXT,
  time_max TEXT,
  rows_total INTEGER NOT NULL DEFAULT 0,
  valid_rows INTEGER NOT NULL DEFAULT 0,
  unavailable_rows INTEGER NOT NULL DEFAULT 0,
  coverage_pct REAL NOT NULL DEFAULT 0,
  compressed INTEGER NOT NULL DEFAULT 0,
  compression TEXT NOT NULL DEFAULT '',
  sha256 TEXT NOT NULL DEFAULT '',
  file_size_bytes INTEGER NOT NULL DEFAULT 0,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_marketdata_partitions_lookup
  ON marketdata_partitions(source, kind, symbol, interval, time_min, time_max);
CREATE INDEX IF NOT EXISTS idx_marketdata_partitions_status
  ON marketdata_partitions(status);
`)
	return err
}

func (d *DB) OpenPartition(ctx context.Context, partition Partition) (Partition, error) {
	if d == nil || d.db == nil {
		return Partition{}, fmt.Errorf("nil index db")
	}
	partition = normalizePartition(partition)
	now := formatTime(time.Now().UTC())
	_, err := d.db.ExecContext(ctx, `
INSERT INTO marketdata_partitions (
  source, kind, symbol, interval, file_path, time_min, time_max,
  rows_total, valid_rows, unavailable_rows, coverage_pct,
  compressed, compression, sha256, file_size_bytes, status, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(file_path) DO UPDATE SET
  source=excluded.source,
  kind=excluded.kind,
  symbol=excluded.symbol,
  interval=excluded.interval,
  compressed=excluded.compressed,
  compression=excluded.compression,
  status=?,
  updated_at=excluded.updated_at
`,
		partition.Source,
		partition.Kind,
		partition.Symbol,
		partition.Interval,
		partition.FilePath,
		formatOptionalTime(partition.TimeMin),
		formatOptionalTime(partition.TimeMax),
		partition.RowsTotal,
		partition.ValidRows,
		partition.UnavailableRows,
		coveragePct(partition.ValidRows, partition.RowsTotal),
		boolInt(partition.Compressed),
		partition.Compression,
		partition.SHA256,
		partition.FileSizeBytes,
		StatusOpen,
		now,
		now,
		StatusOpen,
	)
	if err != nil {
		return Partition{}, err
	}
	return d.PartitionByPath(ctx, partition.FilePath)
}

func (d *DB) PartitionByPath(ctx context.Context, filePath string) (Partition, error) {
	row := d.db.QueryRowContext(ctx, selectPartitionSQL()+` WHERE file_path = ?`, filePath)
	return scanPartition(row)
}

func (d *DB) UpdatePartition(ctx context.Context, partition Partition) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("nil index db")
	}
	partition = normalizePartition(partition)
	partition.CoveragePct = coveragePct(partition.ValidRows, partition.RowsTotal)
	if partition.Status == "" {
		partition.Status = StatusOpen
	}
	if partition.ID > 0 {
		_, err := d.db.ExecContext(ctx, `
UPDATE marketdata_partitions SET
  file_path=?,
  time_min=?,
  time_max=?,
  rows_total=?,
  valid_rows=?,
  unavailable_rows=?,
  coverage_pct=?,
  compressed=?,
  compression=?,
  sha256=?,
  file_size_bytes=?,
  status=?,
  updated_at=?
WHERE id=?
`,
			partition.FilePath,
			formatOptionalTime(partition.TimeMin),
			formatOptionalTime(partition.TimeMax),
			partition.RowsTotal,
			partition.ValidRows,
			partition.UnavailableRows,
			partition.CoveragePct,
			boolInt(partition.Compressed),
			partition.Compression,
			partition.SHA256,
			partition.FileSizeBytes,
			partition.Status,
			formatTime(time.Now().UTC()),
			partition.ID,
		)
		return err
	}
	_, err := d.db.ExecContext(ctx, `
UPDATE marketdata_partitions SET
  time_min=?,
  time_max=?,
  rows_total=?,
  valid_rows=?,
  unavailable_rows=?,
  coverage_pct=?,
  compressed=?,
  compression=?,
  sha256=?,
  file_size_bytes=?,
  status=?,
  updated_at=?
WHERE file_path=?
`,
		formatOptionalTime(partition.TimeMin),
		formatOptionalTime(partition.TimeMax),
		partition.RowsTotal,
		partition.ValidRows,
		partition.UnavailableRows,
		partition.CoveragePct,
		boolInt(partition.Compressed),
		partition.Compression,
		partition.SHA256,
		partition.FileSizeBytes,
		partition.Status,
		formatTime(time.Now().UTC()),
		partition.FilePath,
	)
	return err
}

func (d *DB) FinalizePartition(ctx context.Context, partition Partition, status string) (Partition, error) {
	if status == "" {
		status = StatusClosed
	}
	partition.Status = status
	if meta, err := storage.Metadata(partition.FilePath); err == nil {
		partition.SHA256 = meta.SHA256
		partition.FileSizeBytes = meta.FileSizeBytes
	}
	if err := d.UpdatePartition(ctx, partition); err != nil {
		return Partition{}, err
	}
	return d.PartitionByPath(ctx, partition.FilePath)
}

func (d *DB) QueryPartitions(ctx context.Context, filter PartitionFilter) ([]Partition, error) {
	if d == nil || d.db == nil {
		return nil, fmt.Errorf("nil index db")
	}
	filter = normalizeFilter(filter)
	args := []any{filter.Source, filter.Kind, filter.Interval}
	clauses := []string{"source = ?", "kind = ?", "interval = ?"}
	if filter.Symbol != "" {
		clauses = append(clauses, "symbol = ?")
		args = append(args, filter.Symbol)
	}
	if !filter.From.IsZero() {
		clauses = append(clauses, "(time_max = '' OR time_max >= ?)")
		args = append(args, formatTime(filter.From.UTC()))
	}
	if !filter.To.IsZero() {
		clauses = append(clauses, "(time_min = '' OR time_min <= ?)")
		args = append(args, formatTime(filter.To.UTC()))
	}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, 0, len(filter.Statuses))
		for _, status := range filter.Statuses {
			placeholders = append(placeholders, "?")
			args = append(args, status)
		}
		clauses = append(clauses, "status IN ("+strings.Join(placeholders, ",")+")")
	}
	query := selectPartitionSQL() + " WHERE " + strings.Join(clauses, " AND ") + " ORDER BY time_min, file_path"
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Partition
	for rows.Next() {
		partition, err := scanPartition(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, partition)
	}
	return out, rows.Err()
}

func (d *DB) Coverage(ctx context.Context, filter PartitionFilter) (CoverageSummary, error) {
	filter.Statuses = []string{StatusOpen, StatusClosed}
	partitions, err := d.QueryPartitions(ctx, filter)
	if err != nil {
		return CoverageSummary{}, err
	}
	summary := CoverageSummary{FilesUsed: len(partitions)}
	for _, partition := range partitions {
		if IsOpenGzipPartition(partition) {
			summary.Warnings = append(summary.Warnings, partition.FilePath+": open_gzip_partition_not_live_query_safe")
		}
		summary.RowsTotal += partition.RowsTotal
		summary.ValidRows += partition.ValidRows
		summary.UnavailableRows += partition.UnavailableRows
		if !partition.TimeMin.IsZero() && (summary.TimeMin.IsZero() || partition.TimeMin.Before(summary.TimeMin)) {
			summary.TimeMin = partition.TimeMin
		}
		if !partition.TimeMax.IsZero() && (summary.TimeMax.IsZero() || partition.TimeMax.After(summary.TimeMax)) {
			summary.TimeMax = partition.TimeMax
		}
	}
	summary.CoveragePct = coveragePct(summary.ValidRows, summary.RowsTotal)
	summary.MissingPartitions = MissingDailyPartitions(filter.From, filter.To, partitions)
	switch {
	case summary.RowsTotal == 0:
		summary.Recommendation = RecommendationNoData
	case summary.CoveragePct >= 80 && len(summary.MissingPartitions) == 0:
		summary.Recommendation = RecommendationUsableForEnrichment
	default:
		summary.Recommendation = RecommendationPartialData
	}
	return summary, nil
}

func MissingDailyPartitions(from, to time.Time, partitions []Partition) []string {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil
	}
	covered := map[string]bool{}
	for _, partition := range partitions {
		start := partition.TimeMin
		end := partition.TimeMax
		if start.IsZero() || end.IsZero() {
			continue
		}
		for day := utcMidnight(start); !day.After(utcMidnight(end)); day = day.AddDate(0, 0, 1) {
			covered[day.Format("2006-01-02")] = true
		}
	}
	var missing []string
	for day := utcMidnight(from); !day.After(utcMidnight(to)); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		if !covered[key] {
			missing = append(missing, key)
		}
	}
	return missing
}

func (d *DB) Validate(ctx context.Context, opts ValidationOptions) (ValidationSummary, error) {
	partitions, err := d.QueryPartitions(ctx, opts.Filter)
	if err != nil {
		return ValidationSummary{}, err
	}
	summary := ValidationSummary{PartitionsChecked: len(partitions)}
	for _, partition := range partitions {
		if IsOpenGzipPartition(partition) {
			summary.Warnings = append(summary.Warnings, ValidationIssue{
				FilePath: partition.FilePath,
				Message:  "open_gzip_partition_not_live_query_safe",
			})
		}
		path := storage.ResolvePath(opts.Root, partition.FilePath)
		actual, readErr := scanPartitionFile(path)
		if readErr != nil {
			if IsOpenGzipPartition(partition) && isUnexpectedEOF(readErr) {
				continue
			}
			summary.Issues = append(summary.Issues, ValidationIssue{FilePath: partition.FilePath, Message: readErr.Error()})
			if errors.Is(readErr, os.ErrNotExist) {
				summary.MissingFiles++
			} else {
				summary.CorruptedFiles++
			}
			if opts.Repair {
				partition.Status = StatusCorrupted
				_ = d.UpdatePartition(ctx, partition)
				summary.RepairedPartitions++
			}
			continue
		}
		mismatch := false
		if actual.RowsTotal != partition.RowsTotal ||
			actual.ValidRows != partition.ValidRows ||
			actual.UnavailableRows != partition.UnavailableRows {
			mismatch = true
			summary.Issues = append(summary.Issues, ValidationIssue{FilePath: partition.FilePath, Message: "indexed counters do not match file contents"})
			summary.CounterMismatches++
		}
		if !sameOptionalTime(actual.TimeMin, partition.TimeMin) || !sameOptionalTime(actual.TimeMax, partition.TimeMax) {
			mismatch = true
			summary.Issues = append(summary.Issues, ValidationIssue{FilePath: partition.FilePath, Message: "indexed time range does not match file contents"})
		}
		if opts.Repair && mismatch {
			partition.TimeMin = actual.TimeMin
			partition.TimeMax = actual.TimeMax
			partition.RowsTotal = actual.RowsTotal
			partition.ValidRows = actual.ValidRows
			partition.UnavailableRows = actual.UnavailableRows
			partition.CoveragePct = coveragePct(actual.ValidRows, actual.RowsTotal)
			partition.Status = StatusClosed
			if meta, err := storage.Metadata(path); err == nil {
				partition.FileSizeBytes = meta.FileSizeBytes
				partition.SHA256 = meta.SHA256
			}
			_ = d.UpdatePartition(ctx, partition)
			summary.RepairedPartitions++
		}
	}
	return summary, nil
}

func IsOpenGzipPartition(partition Partition) bool {
	return partition.Status == StatusOpen && (partition.Compressed || strings.HasSuffix(partition.FilePath, ".gz") || partition.Compression == "gzip")
}

func scanPartitionFile(path string) (Partition, error) {
	reader, err := storage.OpenReader(path)
	if err != nil {
		return Partition{}, err
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 16*1024*1024)
	var out Partition
	for scanner.Scan() {
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" {
			continue
		}
		snapshot, ok, err := orderbook.ParseSnapshot([]byte(raw))
		if err != nil {
			return Partition{}, err
		}
		if !ok {
			continue
		}
		out.RowsTotal++
		if snapshot.ValidTopBook() {
			out.ValidRows++
		} else if !snapshot.OrderbookAvailable {
			out.UnavailableRows++
		}
		if out.TimeMin.IsZero() || snapshot.Timestamp.Before(out.TimeMin) {
			out.TimeMin = snapshot.Timestamp
		}
		if out.TimeMax.IsZero() || snapshot.Timestamp.After(out.TimeMax) {
			out.TimeMax = snapshot.Timestamp
		}
	}
	if err := scanner.Err(); err != nil {
		return Partition{}, err
	}
	return out, nil
}

func isUnexpectedEOF(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "unexpected EOF")
}

func selectPartitionSQL() string {
	return `SELECT id, source, kind, symbol, interval, file_path, time_min, time_max,
rows_total, valid_rows, unavailable_rows, coverage_pct, compressed, compression,
sha256, file_size_bytes, status, created_at, updated_at FROM marketdata_partitions`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPartition(row scanner) (Partition, error) {
	var partition Partition
	var timeMin, timeMax, createdAt, updatedAt string
	var compressed int
	err := row.Scan(
		&partition.ID,
		&partition.Source,
		&partition.Kind,
		&partition.Symbol,
		&partition.Interval,
		&partition.FilePath,
		&timeMin,
		&timeMax,
		&partition.RowsTotal,
		&partition.ValidRows,
		&partition.UnavailableRows,
		&partition.CoveragePct,
		&compressed,
		&partition.Compression,
		&partition.SHA256,
		&partition.FileSizeBytes,
		&partition.Status,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return Partition{}, err
	}
	partition.Compressed = compressed != 0
	partition.TimeMin, _ = parseDBTime(timeMin)
	partition.TimeMax, _ = parseDBTime(timeMax)
	partition.CreatedAt, _ = parseDBTime(createdAt)
	partition.UpdatedAt, _ = parseDBTime(updatedAt)
	return partition, nil
}

func normalizePartition(partition Partition) Partition {
	partition.Source = strings.ToLower(strings.TrimSpace(partition.Source))
	if partition.Source == "" {
		partition.Source = "bybit"
	}
	partition.Kind = strings.ToLower(strings.TrimSpace(partition.Kind))
	if partition.Kind == "" {
		partition.Kind = "orderbook"
	}
	partition.Symbol = strings.ToUpper(strings.TrimSpace(partition.Symbol))
	partition.Interval = strings.TrimSpace(partition.Interval)
	if partition.Interval == "" {
		partition.Interval = "1s"
	}
	partition.Compression = strings.ToLower(strings.TrimSpace(partition.Compression))
	if partition.Compressed && partition.Compression == "" {
		partition.Compression = "gzip"
	}
	if !partition.Compressed {
		partition.Compression = ""
	}
	if partition.Status == "" {
		partition.Status = StatusOpen
	}
	partition.CoveragePct = coveragePct(partition.ValidRows, partition.RowsTotal)
	return partition
}

func normalizeFilter(filter PartitionFilter) PartitionFilter {
	filter.Source = strings.ToLower(strings.TrimSpace(filter.Source))
	if filter.Source == "" {
		filter.Source = "bybit"
	}
	filter.Kind = strings.ToLower(strings.TrimSpace(filter.Kind))
	if filter.Kind == "" {
		filter.Kind = "orderbook"
	}
	filter.Symbol = strings.ToUpper(strings.TrimSpace(filter.Symbol))
	filter.Interval = strings.TrimSpace(filter.Interval)
	if filter.Interval == "" {
		filter.Interval = "1s"
	}
	return filter
}

func coveragePct(validRows, rowsTotal int64) float64 {
	if rowsTotal == 0 {
		return 0
	}
	return float64(validRows) / float64(rowsTotal) * 100
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatOptionalTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return formatTime(ts)
}

func formatTime(ts time.Time) string {
	return ts.UTC().Format(TimeLayout)
}

func parseDBTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(TimeLayout, raw)
}

func sameOptionalTime(left, right time.Time) bool {
	if left.IsZero() || right.IsZero() {
		return left.IsZero() && right.IsZero()
	}
	return left.UTC().Equal(right.UTC())
}

func utcMidnight(ts time.Time) time.Time {
	ts = ts.UTC()
	return time.Date(ts.Year(), ts.Month(), ts.Day(), 0, 0, 0, 0, time.UTC)
}
