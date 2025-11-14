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
	// Higher-order trend filter configuration
	UseHigherTrendFilter       bool   // Enable higher-order trend filtering
	HigherTrendPeriod          int    // Period for higher-order trend (e.g., 50 or 200-period SMA)
	// Regime-based strategy configuration
	UseRegimeBasedStrategy     bool   // Enable regime-based strategy switching
	RegimeTrendThreshold       float64 // Threshold for trend detection (0.0 to 1.0)
	RegimeRangeThreshold       float64 // Threshold for range detection (0.0 to 1.0)
	// Enhanced order book filtering configuration
	UseDynamicOrderbookFilter  bool   // Enable dynamic orderbook filtering
	MinVolumeThreshold         float64 // Minimum volume threshold for confirmation
	VolumeSpikeMultiplier      float64 // Multiplier to detect volume spikes
	MinVolatilityThreshold     float64 // Minimum volatility for dynamic threshold adjustment
	MaxVolatilityThreshold     float64 // Maximum volatility for dynamic threshold adjustment
	BaseOrderbookThreshold     float64 // Base threshold when volatility is at normal levels
	HighVolatilityThreshold    float64 // Threshold when volatility is high
	LowVolatilityThreshold     float64 // Threshold when volatility is low
	// Dynamic risk management configuration
	UseDynamicRiskManagement   bool    // Enable dynamic risk management based on ATR
	ATRMultiplierSL            float64 // ATR multiplier for stop loss (e.g., 1.5 times ATR)
	ATRMultiplierTP            float64 // ATR multiplier for take profit (e.g., 3 times ATR for 2:1 ratio)
	ATRPeriod                  int     // ATR period for calculation (default 14)
	PartialProfitPercentage    float64 // Percentage of position to close when TP hit (e.g., 0.5 = 50%)
	TrailingStopATRMultiplier  float64 // ATR multiplier for trailing stop (e.g., 1.0 times ATR)
	UsePartialProfitTaking     bool    // Enable partial profit taking before trailing the rest
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
		OrderbookStrengthThreshold: 1.3, // Base threshold that will be adjusted dynamically if UseDynamicOrderbookFilter is true
		SignalStrengthThreshold:    2,
		// Signal consolidation defaults
		UseSignalConsolidation:     true,  // Enable by default
		SignalConsolidationMethod:  "weighted", // Use weighted method by default
		PrimarySignalWeight:        3,    // Higher weight for primary signals like SMA
		SecondarySignalWeight:      2,    // Medium weight for secondary signals like MACD
		TertiarySignalWeight:       1,    // Lower weight for tertiary signals like Golden Cross
		QuaternarySignalWeight:     1,    // Lower weight for quaternary signals like Bollinger Bands
		SignalThreshold:            4,    // Minimum total weight to trigger a signal
		// Higher-order trend filter defaults
		UseHigherTrendFilter:       true,  // Enable by default
		HigherTrendPeriod:          50,    // 50-period SMA for higher trend filter
		// Regime-based strategy defaults
		UseRegimeBasedStrategy:     true,  // Enable by default
		RegimeTrendThreshold:       0.6,   // Threshold for trend detection
		RegimeRangeThreshold:       0.4,   // Threshold for range detection
		// Enhanced order book filtering defaults
		UseDynamicOrderbookFilter:  true,  // Enable by default
		MinVolumeThreshold:         1000.0, // Minimum volume threshold in USDT equivalent
		VolumeSpikeMultiplier:      2.0,   // 2x the average volume to detect spikes
		MinVolatilityThreshold:     0.5,   // Minimum volatility (in %) for dynamic adjustment
		MaxVolatilityThreshold:     5.0,   // Maximum volatility (in %) for dynamic adjustment
		BaseOrderbookThreshold:     1.3,   // Base threshold when volatility is at normal levels
		HighVolatilityThreshold:    1.5,   // Higher threshold when volatility is high
		LowVolatilityThreshold:     1.1,   // Lower threshold when volatility is low
		// Dynamic risk management defaults
		UseDynamicRiskManagement:   true,  // Enable by default
		ATRMultiplierSL:            1.5,   // Stop loss at 1.5x ATR
		ATRMultiplierTP:            3.0,   // Take profit at 3x ATR (2:1 ratio with SL)
		ATRPeriod:                  14,    // Use 14-period ATR
		PartialProfitPercentage:    0.5,   // Close 50% when TP hit
		TrailingStopATRMultiplier:  1.0,   // Trailing stop at 1x ATR
		UsePartialProfitTaking:     true,  // Enable partial profit taking by default
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
