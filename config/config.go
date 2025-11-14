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
	// Signal consolidation configuration
	UseSignalConsolidation     bool  // Enable signal consolidation (AND logic vs OR logic)
	SignalConsolidationMethod  string // "weighted" or "threshold"
	PrimarySignalWeight        int    // Weight for primary signals (like SMA crossing)
	SecondarySignalWeight      int    // Weight for secondary signals (like MACD)
	TertiarySignalWeight       int    // Weight for tertiary signals (like Golden Cross)
	QuaternarySignalWeight     int    // Weight for quaternary signals (like Bollinger Bands)
	SignalThreshold         	int    // Minimum total weight required to trigger a trade
	// Logging configuration
	LogFile       string
	LogMaxSize    int // megabytes
	LogMaxBackups int // number of files
	LogMaxAge     int // days
	LogCompress   bool
	LogLevel      int // 0=DEBUG, 1=INFO, 2=WARNING, 3=ERROR
	// Daemon configuration
	DaemonMode bool
}

// LoadConfig loads configuration from environment variables or uses defaults
func LoadConfig() *Config {
	return &Config{
		APIKey:           getEnv("BYBIT_API_KEY", ""),
		APISecret:        getEnv("BYBIT_API_SECRET", ""),
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
		TpOffset:         0.002,
		SlThresholdQty:   500.0,
		SmaLen:           20,
		SlPerc:           0.01,
		TrailPerc:        0.005,
		Debug:            false,
		DynamicTP:        false,
		OrderbookStrengthThreshold: 1.3,
		SignalStrengthThreshold:    2,
		// Signal consolidation defaults
		UseSignalConsolidation:     true,  // Enable by default
		SignalConsolidationMethod:  "weighted", // Use weighted method by default
		PrimarySignalWeight:        3,    // Higher weight for primary signals like SMA
		SecondarySignalWeight:      2,    // Medium weight for secondary signals like MACD
		TertiarySignalWeight:       1,    // Lower weight for tertiary signals like Golden Cross
		QuaternarySignalWeight:     1,    // Lower weight for quaternary signals like Bollinger Bands
		SignalThreshold:            4,    // Minimum total weight to trigger a signal
		// Logging defaults
		LogFile:       getEnv("LOG_FILE", "trading_bot.log"),
		LogMaxSize:    10, // 10 MB
		LogMaxBackups: 5,  // 5 backup files
		LogMaxAge:     30, // 30 days
		LogCompress:   true,
		LogLevel:      1, // INFO level
		// Daemon defaults
		DaemonMode: getEnvAsBool("DAEMON_MODE", false),
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
