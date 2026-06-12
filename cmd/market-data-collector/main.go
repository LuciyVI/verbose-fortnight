package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	mdindex "verbose-fortnight/internal/marketdata/index"
	"verbose-fortnight/internal/marketdata/orderbook"
	"verbose-fortnight/internal/marketdata/storage"
)

type orderbookFetcher interface {
	FetchOrderbook(ctx context.Context, symbol string, depth int) (orderbook.Snapshot, error)
}

type collectorOptions struct {
	Source           string
	Symbols          []string
	SnapshotInterval time.Duration
	OutRoot          string
	IndexPath        string
	Compress         bool
	Depth            int
}

type collector struct {
	opts    collectorOptions
	db      *mdindex.DB
	fetcher orderbookFetcher
	states  map[string]*partitionState
	logger  *log.Logger
}

type partitionState struct {
	cfg                storage.PartitionConfig
	compressOnFinalize bool
	writer             *storage.PartitionWriter
	partition          mdindex.Partition
}

func main() {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	db, err := mdindex.Open(opts.IndexPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open index: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fetcher := orderbook.NewBybitClient("", nil)
	c := newCollector(opts, db, fetcher, log.New(os.Stderr, "market-data-collector: ", log.LstdFlags|log.LUTC))
	if err := c.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "collector: %v\n", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (collectorOptions, error) {
	fs := flag.NewFlagSet("market-data-collector", flag.ContinueOnError)
	source := fs.String("source", "bybit", "market data source")
	symbols := fs.String("symbols", "BTCUSDT,ETHUSDT,SOLUSDT", "comma-separated symbols to collect")
	snapshotInterval := fs.Duration("snapshot-interval", time.Second, "snapshot interval")
	outRoot := fs.String("out-root", filepath.Join("artifacts", "marketdata"), "partitioned market data root")
	indexPath := fs.String("index", filepath.Join("artifacts", "marketdata", "index.sqlite"), "SQLite partition index path")
	compress := fs.Bool("compress", true, "gzip partition files")
	depth := fs.Int("depth", 5, "orderbook depth levels")
	if err := fs.Parse(args); err != nil {
		return collectorOptions{}, err
	}
	normalizedSource := strings.ToLower(strings.TrimSpace(*source))
	if normalizedSource != "bybit" {
		return collectorOptions{}, fmt.Errorf("--source currently supports only bybit")
	}
	normalizedSymbols := orderbook.NormalizeSymbols(*symbols)
	if len(normalizedSymbols) == 0 {
		return collectorOptions{}, fmt.Errorf("--symbols must include at least one symbol")
	}
	if *snapshotInterval <= 0 {
		return collectorOptions{}, fmt.Errorf("--snapshot-interval must be positive")
	}
	if *depth <= 0 {
		return collectorOptions{}, fmt.Errorf("--depth must be positive")
	}
	return collectorOptions{
		Source:           normalizedSource,
		Symbols:          normalizedSymbols,
		SnapshotInterval: *snapshotInterval,
		OutRoot:          *outRoot,
		IndexPath:        *indexPath,
		Compress:         *compress,
		Depth:            *depth,
	}, nil
}

func newCollector(opts collectorOptions, db *mdindex.DB, fetcher orderbookFetcher, logger *log.Logger) *collector {
	if logger == nil {
		logger = log.New(ioDiscard{}, "", 0)
	}
	return &collector{
		opts:    opts,
		db:      db,
		fetcher: fetcher,
		states:  map[string]*partitionState{},
		logger:  logger,
	}
}

func (c *collector) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.openCurrentPartitions(ctx, time.Now().UTC()); err != nil {
		return err
	}
	c.collectOnce(ctx, time.Now().UTC())
	ticker := time.NewTicker(c.opts.SnapshotInterval)
	defer ticker.Stop()
	flushTicker := time.NewTicker(10 * time.Second)
	defer flushTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return c.shutdown(context.Background())
		case ts := <-ticker.C:
			c.collectOnce(ctx, ts.UTC())
		case <-flushTicker.C:
			c.flushIndex(ctx)
		}
	}
}

func (c *collector) openCurrentPartitions(ctx context.Context, ts time.Time) error {
	for _, symbol := range c.opts.Symbols {
		state := c.stateFor(symbol)
		if err := state.ensurePartition(ctx, c.db, ts); err != nil {
			return fmt.Errorf("open partition for %s: %w", symbol, err)
		}
	}
	return nil
}

func (c *collector) collectOnce(ctx context.Context, ts time.Time) {
	for _, symbol := range c.opts.Symbols {
		fetchCtx, cancel := context.WithTimeout(ctx, c.opts.SnapshotInterval)
		snapshot, err := c.fetcher.FetchOrderbook(fetchCtx, symbol, c.opts.Depth)
		cancel()
		if err != nil {
			c.logger.Printf("fetch %s failed: %v", symbol, err)
			snapshot = orderbook.UnavailableSnapshot(symbol, ts, c.opts.Depth, err.Error())
		}
		if snapshot.Symbol == "" {
			snapshot.Symbol = symbol
		}
		if snapshot.Timestamp.IsZero() {
			snapshot.Timestamp = ts
			snapshot.Time = ts
		}
		state := c.stateFor(symbol)
		if err := state.write(ctx, c.db, snapshot); err != nil {
			c.logger.Printf("write %s failed: %v", symbol, err)
			continue
		}
	}
}

func (c *collector) flushIndex(ctx context.Context) {
	for _, state := range c.states {
		if err := state.flush(ctx, c.db); err != nil {
			c.logger.Printf("index flush %s failed: %v", state.partition.FilePath, err)
		}
	}
}

func (c *collector) shutdown(ctx context.Context) error {
	var firstErr error
	for _, state := range c.states {
		if err := state.close(ctx, c.db); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (c *collector) stateFor(symbol string) *partitionState {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if state, ok := c.states[symbol]; ok {
		return state
	}
	cfg := storage.PartitionConfig{
		Root:     c.opts.OutRoot,
		Source:   c.opts.Source,
		Kind:     orderbook.DefaultKind,
		Symbol:   symbol,
		Interval: orderbook.DefaultInterval,
		Compress: false,
	}
	state := &partitionState{
		cfg:                cfg,
		compressOnFinalize: c.opts.Compress,
		writer:             storage.NewPartitionWriter(cfg),
	}
	c.states[symbol] = state
	return state
}

func (s *partitionState) ensurePartition(ctx context.Context, db *mdindex.DB, ts time.Time) error {
	targetPath := storage.PartitionPath(s.cfg, ts)
	if s.partition.FilePath == targetPath {
		return nil
	}
	if s.partition.FilePath != "" {
		if _, _, err := s.writer.RotateIfNeeded(ts); err != nil {
			return err
		}
		finalized, err := s.finalizePartition(ctx, db, s.partition)
		if err != nil {
			return err
		}
		s.partition = finalized
	} else {
		if _, _, err := s.writer.RotateIfNeeded(ts); err != nil {
			return err
		}
	}
	partition, err := db.OpenPartition(ctx, mdindex.Partition{
		Source:      s.cfg.Source,
		Kind:        s.cfg.Kind,
		Symbol:      s.cfg.Symbol,
		Interval:    s.cfg.Interval,
		FilePath:    targetPath,
		Compressed:  false,
		Compression: "",
		Status:      mdindex.StatusOpen,
	})
	if err != nil {
		return err
	}
	s.partition = partition
	return nil
}

func (s *partitionState) write(ctx context.Context, db *mdindex.DB, snapshot orderbook.Snapshot) error {
	if err := s.ensurePartition(ctx, db, snapshot.Timestamp); err != nil {
		return err
	}
	raw, err := snapshot.MarshalJSONLine()
	if err != nil {
		return err
	}
	if _, _, err := s.writer.WriteLine(snapshot.Timestamp, raw); err != nil {
		return err
	}
	s.partition.RowsTotal++
	if snapshot.ValidTopBook() {
		s.partition.ValidRows++
	} else if !snapshot.OrderbookAvailable {
		s.partition.UnavailableRows++
	}
	if s.partition.TimeMin.IsZero() || snapshot.Timestamp.Before(s.partition.TimeMin) {
		s.partition.TimeMin = snapshot.Timestamp.UTC()
	}
	if s.partition.TimeMax.IsZero() || snapshot.Timestamp.After(s.partition.TimeMax) {
		s.partition.TimeMax = snapshot.Timestamp.UTC()
	}
	return s.flush(ctx, db)
}

func (s *partitionState) flush(ctx context.Context, db *mdindex.DB) error {
	if s.partition.FilePath == "" {
		return nil
	}
	if err := s.writer.Flush(); err != nil {
		return err
	}
	if meta, err := storage.Metadata(s.partition.FilePath); err == nil {
		s.partition.FileSizeBytes = meta.FileSizeBytes
	}
	return db.UpdatePartition(ctx, s.partition)
}

func (s *partitionState) close(ctx context.Context, db *mdindex.DB) error {
	if s.partition.FilePath == "" {
		return nil
	}
	closeErr := s.writer.Close()
	finalized, finalizeErr := s.finalizePartition(ctx, db, s.partition)
	s.partition = finalized
	if closeErr != nil {
		return closeErr
	}
	return finalizeErr
}

func (s *partitionState) finalizePartition(ctx context.Context, db *mdindex.DB, partition mdindex.Partition) (mdindex.Partition, error) {
	partition.Status = mdindex.StatusClosed
	if s.compressOnFinalize {
		compressedPath, meta, err := storage.CompressFile(partition.FilePath)
		if err != nil {
			return mdindex.Partition{}, err
		}
		partition.FilePath = compressedPath
		partition.Compressed = true
		partition.Compression = "gzip"
		partition.FileSizeBytes = meta.FileSizeBytes
		partition.SHA256 = meta.SHA256
	} else {
		partition.Compressed = false
		partition.Compression = ""
	}
	return db.FinalizePartition(ctx, partition, mdindex.StatusClosed)
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}
