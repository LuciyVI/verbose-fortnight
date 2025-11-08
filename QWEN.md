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
- Multi-package architecture with clean separation of concerns

## Building and Running

### Prerequisites
- Go 1.23.3 or higher
- Bybit API credentials (demo or live)

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

### Project Structure
The application is organized into multiple packages with specific responsibilities:

- `config/` - Configuration management with environment variable support
- `models/` - Data structures and application state management
- `indicators/` - Technical analysis functions and calculations
- `api/` - Bybit API client implementations and request signing
- `order/` - Order placement and management logic
- `position/` - Position tracking and management functions
- `strategy/` - Trading strategy logic and signal processing
- `daemon/` - Background processes and services
- `internal/` - Internal utilities
- `logging/` - Logging utilities

### Core Features
1. **WebSocket Integration**: Connects to both public (kline/orderbook) and private (position/wallet) streams
2. **Signal Generation**: Uses SMA, MACD, and other indicators to generate trading signals
3. **Position Management**: Opens, closes, and adjusts positions automatically
4. **Take Profit/Stop Loss**: Implements dynamic TP/SL with multiple calculation methods (BB, ATR, volume-based)
5. **Market Regime Detection**: Adapts strategy based on whether market is trending or range-bound

### Configuration Constants
- **API Credentials**: Loaded from environment variables
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
The code follows a multi-package architecture with clear separation of concerns:
- `config/` - Handles application configuration and environment variables
- `models/` - Defines data structures and application state
- `indicators/` - Implements technical analysis calculations
- `api/` - Manages API communication with Bybit
- `order/` - Handles order placement and management
- `position/` - Tracks and manages trading positions
- `strategy/` - Implements trading logic and signal processing
- `daemon/` - Runs background processes and services
- `internal/` - Contains internal utilities
- `logging/` - Manages application logging

### Debugging
- Debug mode can be enabled with `-debug` flag
- Debug logging is implemented with colored output
- Detailed logging for signal generation and position management

### Error Handling
- Comprehensive error handling for API calls
- Automatic reconnection for WebSocket streams
- Fallback mechanisms for missing data

### Security
- API credentials loaded from environment variables, not hardcoded
- All API requests are properly signed using HMAC SHA-256
- Credentials are not logged or exposed in error messages
- Timestamp and recvWindow validation for API requests

## Important Notes

1. **API Credentials**: The application loads API keys from environment variables rather than hardcoding them.
2. **Demo Environment**: The code is configured to use Bybit's demo trading environment by default.
3. **Risk Management**: The bot implements various risk management features including stop-loss, take-profit, and trailing stops.
4. **Testing**: There are comprehensive tests available for testing the key functions across all packages.
5. **Concurrency**: The application uses goroutines extensively for handling WebSocket data, indicators, and trading decisions.
6. **Configuration**: Use environment variables for sensitive configuration (BYBIT_API_KEY, BYBIT_API_SECRET).