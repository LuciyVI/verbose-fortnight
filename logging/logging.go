package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

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
	// Ensure the log directory exists
	logDir := filepath.Dir(logFile)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	// Create lumberjack logger for file rotation
	fileWriter := &lumberjack.Logger{
		Filename:   logFile,
		MaxSize:    maxSize,    // megabytes
		MaxBackups: maxBackups, // number of backups
		MaxAge:     maxAge,     // days
		Compress:   compress,   // disabled by default
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
	if fileLogger, ok := l.fileWriter.(*lumberjack.Logger); ok {
		// We need to close and reopen the file to ensure it's flushed
		return fileLogger.Rotate()
	}
	return nil
}

// ChangeLogLevel changes the logging level at runtime
func (l *Logger) ChangeLogLevel(level LogLevel) {
	l.level = level
}