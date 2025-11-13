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
go build -o go-trade main.go
```

Alternatively, you can run directly without building:

```bash
go run main.go
```

## Run Instructions

### 1. Set up environment variables:

```bash
export BYBIT_API_KEY="your_api_key_here"
export BYBIT_API_SECRET="your_api_secret_here"
```

### 2. Run the application:

```bash
# Run the trading bot
./go-trade -debug
```

### Available Command Line Options:

- `-debug`: Enable debug logs (default: false)

### Example commands:

```bash
# Run with debug mode
./go-trade -debug

# Run without debug mode
./go-trade

# Run directly without building
go run main.go -debug

# Run without building and without debug
go run main.go
```

## Web Interface

The trading bot includes a web interface that provides:
- Real-time dashboard statistics
- Current market data
- Open positions overview
- Recent trades history
- Control panel for the trading bot

To enable the web interface, you need to set the ENABLE_WEBUI environment variable to true:

```bash
export ENABLE_WEBUI=true
```

The web interface will be available at `http://localhost:8080` when the bot is running.

## Configuration

The trading bot uses several configuration parameters that are loaded from the `config` package. You can modify the parameters by editing the configuration file at `config/config.go` or by setting environment variables.

### Key Configuration Parameters:

- `Symbol`: Trading symbol (default: BTCUSDT)
- `Interval`: Candle interval (default: 1m)
- `Contract Size`: Size of each contract (default: 0.001)
- `ATR Take Profit Multiplier`: Multiplier for ATR to calculate Take Profit (default: 2.0)
- `ATR Stop Loss Multiplier`: Multiplier for ATR to calculate Stop Loss (default: 1.0)
- `ATR Period`: Period for ATR calculation (default: 14)
- `Take Profit Percentage`: TP percentage when dynamic TP is disabled (fallback)
- `Stop Loss Percentage`: SL percentage (fallback) (default: 0.4%)
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

## Running as a Service

To run the trading bot as a service for continuous operation, you can use process managers like systemd, supervisor, or pm2:

For systemd (Linux), you can create a service file at `/etc/systemd/system/trading-bot.service`:

```ini
[Unit]
Description=Cryptocurrency Trading Bot
After=network.target

[Service]
Type=simple
User=your-username
WorkingDirectory=/path/to/verbose-fortnight
ExecStart=/path/to/verbose-fortnight/go-trade
Restart=always
RestartSec=10
Environment=BYBIT_API_KEY=your_api_key
Environment=BYBIT_API_SECRET=your_api_secret

[Install]
WantedBy=multi-user.target
```

Then you can manage the service with:
```bash
# Start the service
sudo systemctl start trading-bot

# Enable auto-start on boot
sudo systemctl enable trading-bot

# Check status
sudo systemctl status trading-bot
```

## Additional Notes

- The trading bot connects to Bybit's demo environment by default. To use the live environment, you'll need to modify the WebSocket URLs in the configuration
- Always verify your trading parameters before running with real funds
- The web interface provides real-time monitoring and basic controls for the trading bot
- The application implements proper graceful shutdown, which cancels all active orders and closes connections when terminated
- The application includes several technical indicators like ATR, SMA, Bollinger Bands, and MACD to inform trading decisions
- The bot features dynamic TP/SL based on market volatility using ATR (Average True Range), automatically adjusting stop levels based on current market conditions
- ATR-based TP/SL helps maintain consistent risk/reward ratios regardless of market volatility

## License

This application is provided as-is. Trading cryptocurrencies carries significant risk and you should consult with your financial advisor before making investment decisions.