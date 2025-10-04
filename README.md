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

## Architecture

The application is organized into the following logical packages:

- `config/` - Configuration management with environment variable support
- `models/` - Data structures and application state management
- `indicators/` - Technical analysis functions and calculations
- `api/` - Bybit API client implementations and request signing
- `order/` - Order placement and management logic
- `position/` - Position tracking and management functions
- `strategy/` - Trading strategy logic and signal processing

## Prerequisites

- Go 1.23.3 or higher
- Bybit API credentials (demo or live)

## Installation

1. Clone the repository:
```bash
git clone https://github.com/yourusername/verbose-fortnight.git
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
export BYBIT_DEMO_WS_PUBLIC="wss://stream.bybit.com/v5/public/linear"
```

## Usage

### Running the Bot

```bash
# Build the application
go build -o go-trade

# Run with debug logging
./go-trade -debug

# Or run directly with debug
go run main.go -debug
```

### Command Line Options

- `-debug`: Enable debug logging with colored output

## Project Structure

```
verbose-fortnight/
├── config/           # Configuration management
│   └── config.go     # Environment variable loading
├── models/           # Data structures  
│   └── models.go     # Type definitions and state
├── indicators/       # Technical analysis
│   ├── indicators.go # Indicator calculations
│   └── indicators_test.go # Tests
├── api/              # API client implementations
│   └── api.go        # Bybit API functions
├── order/            # Order management
│   └── order.go      # Order placement logic
├── position/         # Position management
│   └── position.go   # Position functions
├── strategy/         # Trading logic
│   └── strategy.go   # Strategy implementation
├── main.go           # Application entry point
├── go.mod            # Module definition
├── go.sum            # Dependency checksums
└── README.md         # This file
```

## Security

### API Credentials
- API keys are loaded from environment variables, not hardcoded
- Credentials are not logged or exposed in error messages
- All API requests are properly signed using HMAC SHA-256

### Best Practices
- Never commit API credentials to version control
- Use Bybit demo account for testing
- Regularly rotate API keys
- Use keys with minimal required permissions

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