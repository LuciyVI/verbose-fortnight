package logger

import (
	"fmt"
	"log"
	"os"
)

const (
	// Colors for different log levels
	clrReset  = "\033[0m"
	clrSig    = "\033[36m" // cyan
	clrOrd    = "\033[32m" // green
	clrErr    = "\033[31m" // red
)

// Level represents the logging level
type Level int

const (
	Debug Level = iota
	Info
	Warn
	Error
)

// Logger represents a logger instance
type Logger struct {
	level Level
	debug bool
}

// New creates a new logger instance
func New(debug bool) *Logger {
	return &Logger{
		level: Debug,
		debug: debug,
	}
}

// NewWithLevel creates a new logger instance with a specific level
func NewWithLevel(debug bool, level Level) *Logger {
	return &Logger{
		level: level,
		debug: debug,
	}
}

// SetLevel sets the logging level
func (l *Logger) SetLevel(level Level) {
	l.level = level
}

// Debug logs a debug message
func (l *Logger) Debug(format string, v ...interface{}) {
	if l.debug && l.level <= Debug {
		log.Printf(format, v...)
	}
}

// Info logs an info message
func (l *Logger) Info(format string, v ...interface{}) {
	if l.level <= Info {
		log.Printf(format, v...)
	}
}

// Warn logs a warning message
func (l *Logger) Warn(format string, v ...interface{}) {
	if l.level <= Warn {
		log.Printf(format, v...)
	}
}

// Error logs an error message
func (l *Logger) Error(format string, v ...interface{}) {
	if l.level <= Error {
		log.Printf(clrErr+format+clrReset, v...)
	}
}

// Signal logs a signal message
func (l *Logger) Signal(format string, v ...interface{}) {
	if l.debug {
		log.Printf(clrSig+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}

// Order logs an order message
func (l *Logger) Order(format string, v ...interface{}) {
	if l.debug {
		log.Printf(clrOrd+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}

// DebugFile logs debug information to a file
func (l *Logger) DebugFile(filename, format string, v ...interface{}) {
	if l.debug {
		file, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return
		}
		defer file.Close()

		content := fmt.Sprintf(format, v...)
		file.WriteString(content + "\n")
	}
}

// LogWithFields logs a message with additional context fields
func (l *Logger) LogWithFields(level Level, fields map[string]interface{}, format string, v ...interface{}) {
	if l.level <= level {
		fieldStr := ""
		for k, v := range fields {
			fieldStr += fmt.Sprintf("[%s:%v] ", k, v)
		}
		log.Printf(fieldStr+format, v...)
	}
}

// SignalWithFields logs a signal message with additional context fields
func (l *Logger) SignalWithFields(fields map[string]interface{}, format string, v ...interface{}) {
	if l.debug {
		fieldStr := ""
		for k, v := range fields {
			fieldStr += fmt.Sprintf("[%s:%v] ", k, v)
		}
		log.Printf(fieldStr+clrSig+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}

// OrderWithFields logs an order message with additional context fields
func (l *Logger) OrderWithFields(fields map[string]interface{}, format string, v ...interface{}) {
	if l.debug {
		fieldStr := ""
		for k, v := range fields {
			fieldStr += fmt.Sprintf("[%s:%v] ", k, v)
		}
		log.Printf(fieldStr+clrOrd+format+clrReset, v...)
	} else {
		log.Printf(format, v...)
	}
}