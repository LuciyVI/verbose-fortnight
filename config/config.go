package config

import (
	"os"
)

// Config holds application configuration
type Config struct {
	APIKey           string
	APISecret        string
	DemoRESTHost     string
	DemoWSPrivateURL string
	DemoWSPublicURL  string
	PongWait         int64
	PingPeriod       int64
	RecvWindow       string
	AccountType      string
	Symbol           string
	Interval         string
	WindowSize       int
	BbMult           float64
	ContractSize     float64
	ObDepth          int
	TpThresholdQty   float64
	TpOffset         float64
	SlThresholdQty   float64
	SmaLen           int
	SlPerc           float64
	TrailPerc        float64
	Debug            bool
	DynamicTP        bool
	// Signal confirmation thresholds
	OrderbookStrengthThreshold float64
	SignalStrengthThreshold    int
	// Logging configuration
	LogFile       string
	LogMaxSize    int // megabytes
	LogMaxBackups int // number of files
	LogMaxAge     int // days
	LogCompress   bool
	LogLevel      int // 0=DEBUG, 1=INFO, 2=WARNING, 3=ERROR
	// Daemon configuration
	DaemonMode bool
	
	// WebUI configuration
	EnableWebUI bool
}

// LoadConfig loads configuration from environment variables or uses defaults
func LoadConfig() *Config {
	return &Config{
		APIKey:           getEnv("BYBIT_API_KEY", "5dkBCxJ2hGbpc0koc8"),
		APISecret:        getEnv("BYBIT_API_SECRET", "FqeE61Sd9FWO6COtK7siMdaAjH0cwXCxL21F"),
		DemoRESTHost:     getEnv("BYBIT_DEMO_REST_HOST", "https://api-demo.bybit.com"),
		DemoWSPrivateURL: getEnv("BYBIT_DEMO_WS_PRIVATE", "wss://stream-demo.bybit.com/v5/private"),
		DemoWSPublicURL:  getEnv("BYBIT_DEMO_WS_PUBLIC", "wss://stream.bybit.com/v5/public/linear"),
		PongWait:         70,
		PingPeriod:       30,
		RecvWindow:       "5000",
		AccountType:      "UNIFIED",
		Symbol:           "BTCUSDT",
		Interval:         "1",
		WindowSize:       20,
		BbMult:           2.0,
		ContractSize:     0.001,
		ObDepth:          50,
		TpThresholdQty:   500.0,
		TpOffset:         0.008,  // Increased to 0.8% for better R/R ratio
		SlThresholdQty:   500.0,
		SmaLen:           20,
		SlPerc:           0.004,  // Reduced to 0.4% to create 2:1 R/R ratio with TP of 0.8%
		TrailPerc:        0.005,  // Keep trailing stop at 0.5% but adjust how it's applied
		Debug:            false,
		DynamicTP:        false,
		OrderbookStrengthThreshold: 1.05,
		SignalStrengthThreshold:    2,
		// Logging defaults
		LogFile:       getEnv("LOG_FILE", "trading_bot.log"),
		LogMaxSize:    10, // 10 MB
		LogMaxBackups: 5,  // 5 backup files
		LogMaxAge:     30, // 30 days
		LogCompress:   true,
		LogLevel:      1, // INFO level
		// Daemon defaults
		DaemonMode: getEnvAsBool("DAEMON_MODE", false),
		// WebUI defaults
		EnableWebUI: getEnvAsBool("ENABLE_WEBUI", false),
	}
}

// getEnvAsBool gets an environment variable as a boolean value
func getEnvAsBool(key string, defaultValue bool) bool {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}
	// Convert string to bool - "true", "1", "yes", "on" are considered true
	switch value {
	case "true", "1", "yes", "on", "True", "TRUE":
		return true
	default:
		return false
	}
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
