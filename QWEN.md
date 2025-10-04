# Qwen Code Context File

## Project Overview

This is a cryptocurrency trading bot written in Go that implements automated trading strategies using the Bybit exchange API. The bot uses WebSocket connections to receive real-time market data and executes trades based on technical indicators. Key features include:

- Real-time market data via WebSocket streams (klines and orderbook)
- Dynamic take-profit (TP) and stop-loss (SL) calculations using multiple indicators
- Support for long and short positions with automatic position management
- SMA (Simple Moving Average) and ATR (Average True Range) indicators
- Bollinger Bands for take-profit calculations
- MACD (Moving Average Convergence Divergence) indicator implementation
- Volume-based signal confirmation
- Market regime detection (trend vs range-bound)
- Trailing stop-loss functionality

## Building and Running

### Prerequisites
- Go 1.23.3 or higher
- Bybit API credentials (already included in code as constants)

### Build Commands
```bash
# Build the application
go build -o go-trade main.go

# Run with debug mode
go run main.go -debug

# Run without debug mode
go run main.go
```

### Testing
```bash
# Run tests
go test ./...
```

## Architecture and Key Components

### Core Features
1. **WebSocket Integration**: Connects to both public (kline/orderbook) and private (position/wallet) streams
2. **Signal Generation**: Uses SMA, MACD, and other indicators to generate trading signals
3. **Position Management**: Opens, closes, and adjusts positions automatically
4. **Take Profit/Stop Loss**: Implements dynamic TP/SL with multiple calculation methods (BB, ATR, volume-based)
5. **Market Regime Detection**: Adapts strategy based on whether market is trending or range-bound

### Configuration Constants
- **API Credentials**: Hardcoded in the source code (for demo environment)
- **Symbol**: BTCUSDT
- **Interval**: 1-minute candles
- **Contract Size**: 0.001 BTC per contract

### Key Functions
- `openPosition()`: Opens a new position with TP/SL
- `calcTakeProfitATR()`: Calculate TP based on ATR
- `calcTakeProfitBB()`: Calculate TP based on Bollinger Bands
- `calcTakeProfitVolume()`: Calculate TP based on volume/liquidity walls
- `hasOpenPosition()`: Check for existing positions
- `smaWorker()`: Runs SMA-based signal generation in a goroutine
- `trader()`: Executes trades based on signals
- `isGoldenCross()`: Checks for MACD golden cross signal

### Indicators Implemented
- SMA (Simple Moving Average)
- ATR (Average True Range)
- Bollinger Bands
- MACD (Moving Average Convergence Divergence)
- RSI (Relative Strength Index)

## Development Conventions

### Code Structure
The code is organized in logical sections with clear comments:
- Global variables and utilities
- Configuration constants
- Strategy state management
- Indicators calculation
- API signing helpers
- REST API calls
- Order placement
- WebSocket handlers
- Position management
- Main function with goroutines

### Debugging
- Debug mode can be enabled with `-debug` flag
- Debug logging is implemented with colored output
- Detailed logging for signal generation and position management

### Error Handling
- Comprehensive error handling for API calls
- Automatic reconnection for WebSocket streams
- Fallback mechanisms for missing data

### Security
- HMAC SHA-256 signing for API requests
- Secure handling of API credentials
- Timestamp and recvWindow validation

## Important Notes

1. **API Credentials**: The API key and secret are hardcoded in the source code. This is appropriate for a demo environment but not for production.
2. **Demo Environment**: The code uses Bybit's demo trading environment by default.
3. **Risk Management**: The bot implements various risk management features including stop-loss, take-profit, and trailing stops.
4. **Testing**: There is a test file available for testing some of the key functions.
5. **Concurrency**: The application uses goroutines extensively for handling WebSocket data, indicators, and trading decisions.