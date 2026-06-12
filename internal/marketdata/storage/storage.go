package storage

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PartitionConfig struct {
	Root     string
	Source   string
	Kind     string
	Symbol   string
	Interval string
	Compress bool
}

type FileMetadata struct {
	SHA256        string
	FileSizeBytes int64
}

type PartitionWriter struct {
	cfg        PartitionConfig
	currentDay string
	path       string
	file       *os.File
	buf        *bufio.Writer
	gzipWriter *gzip.Writer
	writer     io.Writer
}

type readCloser struct {
	reader  io.Reader
	closers []io.Closer
}

func NormalizeConfig(cfg PartitionConfig) PartitionConfig {
	if strings.TrimSpace(cfg.Root) == "" {
		cfg.Root = filepath.Join("artifacts", "marketdata")
	}
	cfg.Source = strings.ToLower(strings.TrimSpace(cfg.Source))
	if cfg.Source == "" {
		cfg.Source = "bybit"
	}
	cfg.Kind = strings.ToLower(strings.TrimSpace(cfg.Kind))
	if cfg.Kind == "" {
		cfg.Kind = "orderbook"
	}
	cfg.Symbol = strings.ToUpper(strings.TrimSpace(cfg.Symbol))
	cfg.Interval = strings.TrimSpace(cfg.Interval)
	if cfg.Interval == "" {
		cfg.Interval = "1s"
	}
	return cfg
}

func PartitionPath(cfg PartitionConfig, ts time.Time) string {
	cfg = NormalizeConfig(cfg)
	day := ts.UTC()
	parts := []string{
		cfg.Root,
		cfg.Source,
		cfg.Kind,
		cfg.Symbol,
		cfg.Interval,
		day.Format("2006"),
		day.Format("01"),
		day.Format("02") + ".jsonl",
	}
	path := filepath.Join(parts...)
	if cfg.Compress {
		path += ".gz"
	}
	return path
}

func CompressedPath(path string) string {
	if strings.HasSuffix(path, ".gz") {
		return path
	}
	return path + ".gz"
}

func PartitionDay(ts time.Time) string {
	return ts.UTC().Format("2006-01-02")
}

func NewPartitionWriter(cfg PartitionConfig) *PartitionWriter {
	return &PartitionWriter{cfg: NormalizeConfig(cfg)}
}

func (w *PartitionWriter) CurrentPath() string {
	if w == nil {
		return ""
	}
	return w.path
}

func (w *PartitionWriter) WriteLine(ts time.Time, raw []byte) (path string, rotated bool, err error) {
	if w == nil {
		return "", false, fmt.Errorf("nil partition writer")
	}
	oldPath := w.path
	if err := w.ensureOpen(ts); err != nil {
		return "", false, err
	}
	path = w.path
	rotated = oldPath != "" && oldPath != w.path
	if len(raw) == 0 {
		return path, rotated, nil
	}
	if _, err := w.writer.Write(raw); err != nil {
		return path, rotated, err
	}
	if raw[len(raw)-1] != '\n' {
		if _, err := w.writer.Write([]byte("\n")); err != nil {
			return path, rotated, err
		}
	}
	return path, rotated, nil
}

func (w *PartitionWriter) RotateIfNeeded(ts time.Time) (string, bool, error) {
	if w == nil {
		return "", false, fmt.Errorf("nil partition writer")
	}
	oldPath := w.path
	oldDay := w.currentDay
	if oldPath == "" {
		if err := w.ensureOpen(ts); err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	newDay := PartitionDay(ts)
	if newDay == oldDay {
		return "", false, nil
	}
	if err := w.closeCurrent(); err != nil {
		return oldPath, true, err
	}
	if err := w.ensureOpen(ts); err != nil {
		return oldPath, true, err
	}
	return oldPath, true, nil
}

func (w *PartitionWriter) Flush() error {
	if w == nil {
		return nil
	}
	var firstErr error
	if w.gzipWriter != nil {
		if err := w.gzipWriter.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if w.buf != nil {
		if err := w.buf.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if w.file != nil {
		if err := w.file.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (w *PartitionWriter) Close() error {
	if w == nil {
		return nil
	}
	return w.closeCurrent()
}

func (w *PartitionWriter) ensureOpen(ts time.Time) error {
	targetDay := PartitionDay(ts)
	targetPath := PartitionPath(w.cfg, ts)
	if w.file != nil && w.currentDay == targetDay && w.path == targetPath {
		return nil
	}
	if w.file != nil {
		if err := w.closeCurrent(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file = file
	w.buf = bufio.NewWriterSize(file, 256*1024)
	w.writer = w.buf
	w.gzipWriter = nil
	if w.cfg.Compress {
		w.gzipWriter = gzip.NewWriter(w.buf)
		w.writer = w.gzipWriter
	}
	w.currentDay = targetDay
	w.path = targetPath
	return nil
}

func (w *PartitionWriter) closeCurrent() error {
	var firstErr error
	if w.gzipWriter != nil {
		if err := w.gzipWriter.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.gzipWriter = nil
	}
	if w.buf != nil {
		if err := w.buf.Flush(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.buf = nil
	}
	if w.file != nil {
		if err := w.file.Sync(); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := w.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		w.file = nil
	}
	w.writer = nil
	return firstErr
}

func OpenReader(path string) (io.ReadCloser, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			file.Close()
			return nil, err
		}
		return &readCloser{reader: gz, closers: []io.Closer{gz, file}}, nil
	}
	return file, nil
}

func (r *readCloser) Read(p []byte) (int, error) {
	return r.reader.Read(p)
}

func (r *readCloser) Close() error {
	var firstErr error
	for _, closer := range r.closers {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func Metadata(path string) (FileMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return FileMetadata{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return FileMetadata{}, err
	}
	return FileMetadata{
		SHA256:        hex.EncodeToString(hash.Sum(nil)),
		FileSizeBytes: size,
	}, nil
}

func CompressFile(path string) (string, FileMetadata, error) {
	if strings.HasSuffix(path, ".gz") {
		meta, err := Metadata(path)
		return path, meta, err
	}
	dst := CompressedPath(path)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if meta, metaErr := Metadata(dst); metaErr == nil {
				return dst, meta, nil
			}
		}
		return "", FileMetadata{}, err
	}
	src, err := os.Open(path)
	if err != nil {
		return "", FileMetadata{}, err
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", FileMetadata{}, err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return "", FileMetadata{}, err
	}
	gz := gzip.NewWriter(out)
	_, copyErr := io.Copy(gz, src)
	closeGzipErr := gz.Close()
	closeFileErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return "", FileMetadata{}, copyErr
	}
	if closeGzipErr != nil {
		_ = os.Remove(tmp)
		return "", FileMetadata{}, closeGzipErr
	}
	if closeFileErr != nil {
		_ = os.Remove(tmp)
		return "", FileMetadata{}, closeFileErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", FileMetadata{}, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", FileMetadata{}, err
	}
	meta, err := Metadata(dst)
	if err != nil {
		return "", FileMetadata{}, err
	}
	return dst, meta, nil
}

func ResolvePath(root, filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	if _, err := os.Stat(filePath); err == nil {
		return filePath
	}
	if strings.TrimSpace(root) == "" {
		return filePath
	}
	return filepath.Join(root, filePath)
}
