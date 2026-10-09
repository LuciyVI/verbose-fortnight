# Go Trade - Cryptocurrency Trading Bot

A sophisticated cryptocurrency trading bot written in Go that implements automated trading strategies using the Bybit exchange API.

## Table of Contents
- [Features](#features)
- [Architecture](#architecture)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Configuration](#configuration)
- [Usage](#usage)
- [Project Structure](#project-structure)
- [Security](#security)
- [Testing](#testing)
- [Development](#development)
- [License](#license)

## Features

- **Real-time Trading**: WebSocket integration for live market data and orderbook updates
- **Technical Indicators**: Multiple indicators including SMA, ATR, Bollinger Bands, MACD, and RSI
- **Dynamic Position Management**: Smart TP/SL calculation using multiple methods
- **Risk Management**: Trailing stop-loss functionality and position sizing controls
- **Market Regime Detection**: Adapts strategy based on whether market is trending or range-bound
- **Multi-package Architecture**: Clean separation of concerns for better maintainability
- **Secure Configuration**: API credentials loaded from environment variables

## Architecture

The application is organized into the following logical packages:

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

## Prerequisites

- Go 1.23.3 or higher
- Bybit API credentials (demo or live)

## Installation

1. Clone the repository:
```bash
git clone https://github.com/LuciyVI/verbose-fortnight.git
cd verbose-fortnight
```

2. Install dependencies:
```bash
go mod tidy
```

## Configuration

The application uses environment variables for sensitive configuration:

```bash
export BYBIT_API_KEY="your_api_key"
export BYBIT_API_SECRET="your_api_secret"

# Optional configuration (with defaults shown):
export BYBIT_DEMO_REST_HOST="https://api-demo.bybit.com"
export BYBIT_DEMO_WS_PRIVATE="wss://stream-demo.bybit.com/v5/private"
export BYBIT_DEMO_WS_PUBLIC="wss://stream-demo.bybit.com/v5/public/linear"
export ORDER_BALANCE_PCT="1.0" # actual default: 100% of available USDT balance (0 = min size)

# Dynamic TP parameters (percent units, e.g. 0.4 = 0.4%)
export DYNAMIC_TP_K="1.0"
export DYNAMIC_TP_VOLATILITY_FACTOR="6.0"
export DYNAMIC_TP_MIN_PERC="0.7"
export DYNAMIC_TP_MAX_PERC="0.7"
```

Position size is a notional allocation, not an allowed loss: `ORDER_BALANCE_PCT=1.0` targets a position notional equal to the available balance, subject to instrument minimums and step rounding. Loss depends on the SL distance, executions, fees and slippage. This change preserves the sizing default and strategy parameters.

REST defaults are `REST_TIMEOUT_MS=5000`, `REST_MAX_RESPONSE_BYTES=1048576` and `ORDER_RECONCILE_TIMEOUT_MS=3000`. Zero uses these bounded defaults; negative settings are invalid. Responses have a hard maximum of 16 MiB.

A placement timeout can hide an accepted order. The bot looks up the original `orderLinkId` and never blindly repeats order creation. Maker fallback waits for a terminal order snapshot and uses its final cumulative fill to calculate the remainder. If acceptance or cancellation cannot be reconciled, new trading is blocked pending recovery. See [the recovery runbook](docs/runbook.md#order-reconciliation-and-stop-lifecycles).

Every REST mutation is recorded before sending in `OPERATION_STATE_PATH` (default `state/operations.json`, relative to the working directory). The snapshot includes operation ID, monotonically increasing generation, lifecycle, stable orderLinkId, exact payload and result. It contains no API keys or secrets. File fsync, atomic rename and parent-directory fsync commit each update; an exclusive lock permits one process per store. Use a persistent local filesystem and a distinct path for each account/symbol. State is ignored by Git.

Startup and private-stream resync reconcile the durable operations, all active orders, executions, current position and actual TP/SL before permitting trading. This also runs when the file did not exist. Missing/ambiguous order results, executable remainders, conflicting executions, unknown position ownership and invalid stops keep trading blocked. No mutation is replayed during recovery. Interrupted partial fills are observed; recovery does not place their unfilled remainder automatically. Manual clear cannot bypass the recovery gate.

`/status.recovery` exposes readiness, the state path, errors and operations; `tradingBlocked` and `blockMessage` show the overall gate. `STARTUP_RECONCILE_TIMEOUT_MS=30000` bounds the full scan (zero uses the default). `go run . -reconcile-only -debug=0` runs the same read-only reconciliation, persists its conclusions and exits 0 on recovery or 1 while blocked, without starting WS or strategy workers. `ENABLE_DRY_RUN=1` bypasses exchange access and the operation store, so it does not verify recovery.

Corrupt, incomplete, unsupported or locked state, including a missing snapshot with an existing lock file, fails closed and is never silently replaced. Preserve the file, `.lock` and `.tmp-*` evidence; follow [the recovery runbook](docs/runbook.md#order-reconciliation-and-stop-lifecycles) before repair. REST history/pagination limits and truncated execution boundaries also block recovery.

TP/SL wire prices use the instrument tick with decimal arithmetic. Rounding moves toward entry: LONG TP down/SL up, SHORT TP up/SL down. Quantized values that violate invariants are rejected; explicit breakeven intents may retain SL equal to entry. Applied levels are the actual payload values after a successful REST acknowledgement. A final position observation checks target and direction; exchange close/reopen can still race the interval between observation and application. Context cancellation cannot roll back a mutation already accepted remotely.

`ENABLE_DRY_RUN=1` starts diagnostics and health workers without exchange REST/WS connections. It does not simulate signals, orders, fills, TP/SL or strategy profitability.

## Usage

### Running the Bot

```bash
# Build the application
go build -o go-trade .

# Run with debug logging
./go-trade -debug=0

# Or run directly with debug
go run . -debug=0
```

### Command Line Options

- `-debug=0`: numeric log level: 0=DEBUG, 1=INFO, 2=WARNING, 3=ERROR.

## Project Structure

```
verbose-fortnight/
├── config/           # Configuration management
│   └── config.go     # Environment variable loading
├── models/           # Data structures  
│   └── models.go     # Type definitions and state
├── indicators/       # Technical analysis
│   └── indicators.go # Indicator calculations
├── api/              # API client implementations
│   └── api.go        # Bybit API functions
├── order/            # Order management
│   └── order.go      # Order placement logic
├── position/         # Position management
│   └── position.go   # Position functions
├── strategy/         # Trading logic
│   └── strategy.go   # Strategy implementation
├── daemon/           # Background processes and services
├── internal/         # Internal utilities
├── logging/          # Logging utilities
├── main.go           # Application entry point
├── go.mod            # Module definition
├── go.sum            # Dependency checksums
├── QWEN.md           # Qwen context file
├── README.md         # This file
└── trading_bot.log   # Trading bot log output
```

## Security

### API Credentials
- API keys are loaded from environment variables, not hardcoded in the source code
- Credentials are not logged or exposed in error messages
- All API requests are properly signed using HMAC SHA-256
- Timestamp and recvWindow validation for API requests

### Best Practices
- Never commit API credentials to version control
- Use Bybit demo account for testing
- Regularly rotate API keys
- Use keys with minimal required permissions
- Follow security best practices for storing and managing API credentials

## Testing

Run all tests:
```bash
go test ./...
```

Run tests for a specific package:
```bash
go test ./indicators
go test ./api
go test ./strategy
```

Run tests with coverage:
```bash
go test -cover ./...
```

## Trading Strategy

The bot implements a multi-indicator approach:

1. **Signal Generation**: Uses SMA and MACD for signal generation
2. **Confirmation**: Validates signals with orderbook strength analysis
3. **Position Sizing**: Dynamic position sizing based on balance
4. **Take Profit**: Multiple TP calculation methods (ATR, BB, volume-based)
5. **Stop Loss**: Fixed SL percentage or dynamic trailing stops
6. **Risk Management**: Market regime detection to adapt strategy

## Development

### Development Conventions
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

### Adding New Indicators
1. Add the indicator function to `indicators/indicators.go`
2. Write corresponding tests in `indicators/indicators_test.go`
3. Import and use the indicator in the strategy package

### Modifying Trading Logic
1. Adjust signal generation in `strategy/strategy.go`
2. Update position management logic as needed
3. Test thoroughly before deployment

### Contributing
1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Ensure all tests pass
6. Submit a pull request

## Risk Disclaimer

This software is provided for educational purposes only. Cryptocurrency trading involves substantial risk and may not be suitable for all investors. Past performance does not guarantee future results. Users should carefully consider their investment objectives and risk tolerance before using this software.

The authors are not responsible for any financial losses incurred while using this software.

## License

This project is licensed under the MIT License - see the LICENSE file for details.
