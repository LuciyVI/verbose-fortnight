package constants

// Market sides
const (
	Long  = "LONG"
	Short = "SHORT"
)

// Order types
const (
	Market = "Market"
	Limit  = "Limit"
)

// Order sides
const (
	Buy  = "Buy"
	Sell = "Sell"
)

// Timeframes
const (
	Minute1  = "1"
	Minute5  = "5"
	Minute15 = "15"
	Minute30 = "30"
	Hour1    = "60"
	Hour4    = "240"
)

// Default values
const (
	DefaultATRPeriod      = 14
	DefaultSMAWindow      = 20
	DefaultBollingerMult  = 2.0
	DefaultBollingerWindow = 20
	DefaultMACDFast       = 12
	DefaultMACDSlow       = 26
	DefaultMACDSignal     = 9
)

// Risk management
const (
	DefaultTPAtrMultiplier = 2.0
	DefaultSLAtrMultiplier = 1.0
)