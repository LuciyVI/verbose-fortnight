# Trading Bot with Web UI - Build and Run Instructions

This document provides instructions for building and running the integrated trading bot with web interface.

## Prerequisites

1. **Go**: You need Go version 1.23.3 or higher installed on your system
2. **Environment Variables**: You need to set up the following environment variables to connect to Bybit exchange:
   - `BYBIT_API_KEY`: Your Bybit API key
   - `BYBIT_API_SECRET`: Your Bybit API secret
3. **Git** (optional, for cloning the repository)

## Build Instructions

### 1. Navigate to the project directory:

```bash
cd /path/to/verbose-fortnight
```

### 2. Build the integrated trading bot:

```bash
# Build the integrated trading bot application
go build -o integrated_trading_bot ./cmd/integrated_trading_bot/
```

Alternatively, you can build directly using:

```bash
go build -o integrated_trading_bot ./cmd/integrated_trading_bot/main.go
```

## Run Instructions

### 1. Set up environment variables:

```bash
export BYBIT_API_KEY="your_api_key_here"
export BYBIT_API_SECRET="your_api_secret_here"
```

### 2. Run the application with web UI enabled:

```bash
# Run the integrated bot with web UI
./integrated_trading_bot -webui -web-port=8080 -debug
```

### Available Command Line Options:

- `-webui`: Enable the web interface (default: false)
- `-web-port`: Port for the web interface (default: 8080)
- `-debug`: Enable debug logs (default: false)
- `-start-daemon`: Start the application as a daemon
- `-stop-daemon`: Stop the daemon process
- `-restart-daemon`: Restart the daemon process

### Example commands:

```bash
# Run with web UI on default port 8080
./integrated_trading_bot -webui

# Run with web UI on a custom port
./integrated_trading_bot -webui -web-port=9000

# Run with debug and web UI
./integrated_trading_bot -webui -debug

# Run as a daemon
./integrated_trading_bot -start-daemon
```

## Accessing the Web Interface

If you've started the application with `-webui`, you can access the dashboard by opening your browser and navigating to:

- `http://localhost:8080` (or the port you specified with `-web-port`)

The web interface provides:
- Real-time dashboard statistics
- Current market data
- Open positions overview
- Recent trades history
- Control panel for the trading bot

## Configuration

The trading bot uses several configuration parameters that are loaded from the `config` package. You can modify the parameters by editing the configuration file at `config/config.go` or by setting environment variables.

### Key Configuration Parameters:

- `Symbol`: Trading symbol (default: BTCUSDT)
- `Interval`: Candle interval (default: 1m)
- `Contract Size`: Size of each contract (default: 0.001)
- `Take Profit Percentage`: TP percentage when dynamic TP is disabled
- `Stop Loss Percentage`: SL percentage (default: 0.4%)
- `Trailing Stop Percentage`: Trailing stop percentage (default: 0.1%)

## Development and Testing

### To run in development mode with continuous rebuilding:

```bash
# Install go-watch if not already installed
go install github.com/canthefason/go-watcher@latest

# Run with auto-reload
go-watcher -c "go run cmd/integrated_trading_bot/main.go -webui -debug"
```

### To run without building a binary:

```bash
go run cmd/integrated_trading_bot/main.go -webui -debug
```

## Troubleshooting

### Common Issues:

1. **API Authentication Failed**:
   - Make sure your `BYBIT_API_KEY` and `BYBIT_API_SECRET` are correctly set
   - Verify that your API key has the necessary permissions

2. **Port Already in Use**:
   - If the web UI port is already in use, try a different port with `-web-port`

3. **Connection Issues**:
   - Verify internet connectivity
   - Check firewall settings that might block WebSocket connections

### Debugging:

1. Use the `-debug` flag to get more detailed logs
2. Check the generated log files (default: `trading_bot.log`)
3. Verify that all required environment variables are set

## Running as a Daemon

The integrated trading bot supports running as a daemon for continuous operation:

```bash
# Start the daemon
./integrated_trading_bot -start-daemon -webui

# Stop the daemon
./integrated_trading_bot -stop-daemon

# Restart the daemon
./integrated_trading_bot -restart-daemon
```

## Additional Notes

- The trading bot connects to Bybit's demo environment by default. To use the live environment, you'll need to modify the WebSocket URLs in the configuration
- Always verify your trading parameters before running with real funds
- The web interface provides real-time monitoring and basic controls for the trading bot
- The application implements proper graceful shutdown, which cancels all active orders and closes connections when terminated
- The application includes several technical indicators like ATR, SMA, Bollinger Bands, and MACD to inform trading decisions

## License

This application is provided as-is. Trading cryptocurrencies carries significant risk and you should consult with your financial advisor before making investment decisions.