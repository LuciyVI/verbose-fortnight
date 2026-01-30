package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// LogLevel represents the logging level
type LogLevel int

const (
	DEBUG LogLevel = iota
	INFO
	WARNING
	ERROR
)

// Logger wraps the standard log package with file output and rotation
type Logger struct {
	logger     *log.Logger
	fileWriter io.Writer
	level      LogLevel
}

type hourlyLumberjackWriter struct {
	baseDir  string
	baseName string
	ext      string

	maxSize    int
	maxBackups int
	maxAge     int
	compress   bool

	mu           sync.Mutex
	currentKey   string
	currentLog   *lumberjack.Logger
	lastPruneDay string
}

func newHourlyLumberjackWriter(basePath string, maxSize, maxBackups, maxAge int, compress bool) (*hourlyLumberjackWriter, error) {
	baseDir := filepath.Dir(basePath)
	base := filepath.Base(basePath)
	ext := filepath.Ext(base)
	baseName := strings.TrimSuffix(base, ext)
	if baseName == "" {
		return nil, fmt.Errorf("invalid log file: %q", basePath)
	}
	if ext == "" {
		ext = ".log"
	}

	w := &hourlyLumberjackWriter{
		baseDir:      baseDir,
		baseName:     baseName,
		ext:          ext,
		maxSize:      maxSize,
		maxBackups:   maxBackups,
		maxAge:       maxAge,
		compress:     compress,
		currentKey:   "",
		currentLog:   nil,
		lastPruneDay: "",
	}

	if err := w.ensureWriter(time.Now()); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *hourlyLumberjackWriter) hourlyPath(now time.Time) string {
	dayDir := filepath.Join(w.baseDir, now.Format("2006-01-02"))
	filename := fmt.Sprintf("%s-%02d%s", w.baseName, now.Hour(), w.ext)
	return filepath.Join(dayDir, filename)
}

func (w *hourlyLumberjackWriter) ensureWriter(now time.Time) error {
	w.currentKey = now.Format("2006-01-02-15")

	path := w.hourlyPath(now)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	w.currentLog = &lumberjack.Logger{
		Filename:   path,
		MaxSize:    w.maxSize,
		MaxBackups: w.maxBackups,
		MaxAge:     w.maxAge,
		Compress:   w.compress,
	}

	today := now.Format("2006-01-02")
	if w.maxAge > 0 && today != w.lastPruneDay {
		w.lastPruneDay = today
		if err := w.pruneOldDays(now); err != nil {
			return err
		}
	}

	return nil
}

func (w *hourlyLumberjackWriter) ensure(now time.Time) error {
	key := now.Format("2006-01-02-15")
	if w.currentLog != nil && w.currentKey == key {
		return nil
	}

	if w.currentLog != nil {
		_ = w.currentLog.Close()
		w.currentLog = nil
	}
	return w.ensureWriter(now)
}

func (w *hourlyLumberjackWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensure(time.Now()); err != nil {
		return 0, err
	}
	return w.currentLog.Write(p)
}

func (w *hourlyLumberjackWriter) Rotate() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.ensure(time.Now()); err != nil {
		return err
	}
	return w.currentLog.Rotate()
}

func (w *hourlyLumberjackWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.currentLog == nil {
		return nil
	}
	err := w.currentLog.Close()
	w.currentLog = nil
	w.currentKey = ""
	return err
}

func dateOnly(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

func (w *hourlyLumberjackWriter) pruneOldDays(now time.Time) error {
	base := w.baseDir
	cutoff := dateOnly(now).AddDate(0, 0, -(w.maxAge - 1))

	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read log directory %q: %w", base, err)
	}

	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", ent.Name(), now.Location())
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(base, ent.Name()))
		}
	}

	return nil
}

// LoggerInterface defines the interface for logging methods
type LoggerInterface interface {
	Debug(format string, v ...interface{})
	Info(format string, v ...interface{})
	Warning(format string, v ...interface{})
	Error(format string, v ...interface{})
	Fatal(format string, v ...interface{})
	Sync() error
	ChangeLogLevel(level LogLevel)
}

// NewLogger creates a new logger instance with file output and rotation
func NewLogger(logFile string, maxSize, maxBackups, maxAge int, compress bool, level LogLevel) (*Logger, error) {
	fileWriter, err := newHourlyLumberjackWriter(logFile, maxSize, maxBackups, maxAge, compress)
	if err != nil {
		return nil, err
	}

	// Create a multi-writer to log to both file and stdout
	multiWriter := io.MultiWriter(fileWriter, os.Stdout)

	// Create the logger
	logger := log.New(multiWriter, "", log.Ldate|log.Ltime|log.Lmicroseconds|log.Lshortfile)

	return &Logger{
		logger:     logger,
		fileWriter: fileWriter,
		level:      level,
	}, nil
}

// Debug logs a debug message
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.level <= DEBUG {
		l.logger.Output(2, fmt.Sprintf("[DEBUG] "+format, v...))
	}
}

// Info logs an info message
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= INFO {
		l.logger.Output(2, fmt.Sprintf("[INFO]  "+format, v...))
	}
}

// Warning logs a warning message
func (l *Logger) Warning(format string, v ...interface{}) {
	if l.level <= WARNING {
		l.logger.Output(2, fmt.Sprintf("[WARN]  "+format, v...))
	}
}

// Error logs an error message
func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= ERROR {
		l.logger.Output(2, fmt.Sprintf("[ERROR] "+format, v...))
	}
}

// Fatal logs an error message and exits
func (l *Logger) Fatal(format string, v ...interface{}) {
	l.logger.Output(2, fmt.Sprintf("[FATAL] "+format, v...))
	os.Exit(1)
}

// Sync flushes any buffered log entries to the underlying writer
func (l *Logger) Sync() error {
	type rotator interface {
		Rotate() error
	}
	if r, ok := l.fileWriter.(rotator); ok {
		return r.Rotate()
	}
	return nil
}

// ChangeLogLevel changes the logging level at runtime
func (l *Logger) ChangeLogLevel(level LogLevel) {
	l.level = level
}
