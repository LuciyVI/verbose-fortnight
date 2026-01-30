package config

import (
	"os"
	"strconv"
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
	OrderBalancePct  float64 // 0 uses minimum order size
	ObDepth          int
	TpThresholdQty   float64
	TpOffset         float64
	SlThresholdQty   float64
	SmaLen           int
	// Adaptive thresholds
	AtrDeadbandMult    float64
	RSILow             float64
	RSIHigh            float64
	HTFWindow          int
	HTFMaLen           int
	AtrSLMult          float64
	AtrTPMult          float64
	TargetRRRange      float64
	TargetRRTrend      float64
	AtrTPCapMultRange  float64
	AtrTPCapMultTrend  float64
	EnableTPSLStage1   bool
	SlPerc             float64
	TrailPerc          float64
	TrailActivation    float64
	TrailTightPerc     float64
	BreakevenProgress  float64
	RoundTripFeePerc   float64
	FeeBufferMult      float64
	MinProfitPerc      float64
	MinTrailRR         float64
	ReentryCooldownSec int
	VolumeSpikeMult    float64
	MinVolume          float64
	Debug              bool
	DynamicTP          bool
	// Dynamic TP configuration (percent units, e.g. 0.4 = 0.4%)
	DynamicTPK                float64
	DynamicTPVolatilityFactor float64
	DynamicTPMinPerc          float64
	DynamicTPMaxPerc          float64
	// Signal confirmation thresholds
	OrderbookStrengthThreshold float64
	OrderbookLevels            int
	OrderbookMinDepth          float64
	OrderbookStabilityLookback int
	OrderbookStabilityRange    float64
	OrderbookMedianRatioMult   float64
	SignalStrengthThreshold    int
	RegimePersistence          int
	PartialTakeProfitRatio     float64
	PartialTakeProfitProgress  float64
	SLSetDelaySec              int
	SLPocketPerc               float64
	PocketFeeMult              float64
	SLFeeFloorMult             float64
	TPFeeFloorMult             float64
	GracePeriodSec             int
	MinReentryFeeBufferMult    float64
	// Logging configuration
	LogFile       string
	LogMaxSize    int // megabytes
	LogMaxBackups int // number of files
	LogMaxAge     int // days
	LogCompress   bool
	LogLevel      int // 0=DEBUG, 1=INFO, 2=WARNING, 3=ERROR
	// Status server configuration
	StatusAddr string
	// Daemon configuration
	DaemonMode bool
	// Trailing configuration
	DisableTrailing bool
}

// LoadConfig loads configuration from environment variables or uses defaults
func LoadConfig() *Config {
	return &Config{
		APIKey:           getEnv("BYBIT_API_KEY", ""),
		APISecret:        getEnv("BYBIT_API_SECRET", ""),
		DemoRESTHost:     getEnv("BYBIT_DEMO_REST_HOST", "https://api-demo.bybit.com"),
		DemoWSPrivateURL: getEnv("BYBIT_DEMO_WS_PRIVATE", "wss://stream-demo.bybit.com/v5/private"),
		// Use demo public stream by default to keep REST/WS environments consistent
		DemoWSPublicURL: getEnv("BYBIT_DEMO_WS_PUBLIC", "wss://stream-demo.bybit.com/v5/public/linear"),
		PongWait:        70,
		PingPeriod:      30,
		RecvWindow:      "5000",
		AccountType:     "UNIFIED",
		Symbol:          "BTCUSDT",
		Interval:        "1",
		WindowSize:      20,
		BbMult:          2.0,
		ContractSize:    0.001,
		// OrderBalancePct is a balance fraction: 1.0 = 100% of balance.
		OrderBalancePct: getEnvAsFloat("ORDER_BALANCE_PCT", 1.0),
		ObDepth:         50,
		TpThresholdQty:  500.0,
		TpOffset:        0.002,
		SlThresholdQty:  500.0,
		SmaLen:          20,
		AtrDeadbandMult: 0.25,
		// Enable RSI gating by default to avoid over-trading noise
		RSILow:                     45,
		RSIHigh:                    55,
		HTFWindow:                  60,
		HTFMaLen:                   20,
		AtrSLMult:                  1.5, // wider SL to avoid noise
		AtrTPMult:                  3.0, // restore wider TP
		TargetRRRange:              1.0,
		TargetRRTrend:              1.8,
		AtrTPCapMultRange:          2.0,
		AtrTPCapMultTrend:          3.5,
		EnableTPSLStage1:           true,
		SlPerc:                     0.01,
		TrailPerc:                  0.005,
		TrailActivation:            0.6,
		TrailTightPerc:             0.0015,
		BreakevenProgress:          1.0,    // disable early BE; rely on static SL/TP
		RoundTripFeePerc:           0.0012, // ~0.12% taker+taker on BTCUSDT
		FeeBufferMult:              1.1,    // pad fee buffer a bit to cover slippage
		MinProfitPerc:              0.0025, // require at least 0.25% projected move for TP
		MinTrailRR:                 0.8,    // trail only after ~0.8R is reached
		ReentryCooldownSec:         30,
		VolumeSpikeMult:            0,
		MinVolume:                  0,
		Debug:                      false,
		DynamicTP:                  false,
		DynamicTPK:                 getEnvAsFloat("DYNAMIC_TP_K", 1.0),
		DynamicTPVolatilityFactor:  getEnvAsFloat("DYNAMIC_TP_VOLATILITY_FACTOR", 6.0),
		DynamicTPMinPerc:           getEnvAsFloat("DYNAMIC_TP_MIN_PERC", 0.4),
		DynamicTPMaxPerc:           getEnvAsFloat("DYNAMIC_TP_MAX_PERC", 1.8),
		OrderbookStrengthThreshold: 0.5,
		OrderbookLevels:            5,
		OrderbookMinDepth:          0,
		// Trim orderbook imbalance history to avoid perpetual instability filtering
		OrderbookStabilityLookback: 8,
		OrderbookStabilityRange:    3.0,
		OrderbookMedianRatioMult:   0.08,
		SignalStrengthThreshold:    3,
		RegimePersistence:          3,
		PartialTakeProfitRatio:     0.4, // close 40% at first target
		PartialTakeProfitProgress:  0.6, // first target at 60% progress to TP
		SLSetDelaySec:              1,   // delay before sending SL to avoid immediate noise
		SLPocketPerc:               0.0005,
		PocketFeeMult:              2.0,
		SLFeeFloorMult:             4.0,
		TPFeeFloorMult:             5.0,
		GracePeriodSec:             45,  // do not flip/close within this time after entry
		MinReentryFeeBufferMult:    3.0, // require at least 3x fee buffer move before re-enter/flip
		// Logging defaults
		LogFile:       getEnv("LOG_FILE", "logs/trading_bot.log"),
		LogMaxSize:    10, // 10 MB
		LogMaxBackups: 5,  // 5 backup files
		LogMaxAge:     30, // 30 days
		LogCompress:   true,
		LogLevel:      1, // INFO level
		// Status server defaults
		StatusAddr: getEnv("STATUS_ADDR", "127.0.0.1:6061"),
		// Daemon defaults
		DaemonMode: getEnvAsBool("DAEMON_MODE", false),
		// Trailing defaults
		DisableTrailing: true,
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

func getEnvAsFloat(key string, defaultValue float64) float64 {
	value := getEnv(key, "")
	if value == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return defaultValue
	}
	return parsed
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
