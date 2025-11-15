# Trading Algorithm Documentation

## Overview
The trading bot implements a multi-indicator strategy that combines technical analysis, market regime detection, and risk management to generate trading signals and execute positions. The algorithm operates on real-time market data and makes decisions based on various technical indicators and market conditions.

## Core Components

### 1. Data Processing Pipeline

#### K-line Processing
- Receives real-time k-line (candlestick) data from WebSocket
- Maintains historical k-line data in memory for technical analysis
- Processes closed candles to generate trading signals
- Manages data buffer to prevent memory overflow

#### Orderbook Processing
- Maintains real-time orderbook snapshots and updates (deltas)
- Tracks bid/ask prices and order volumes
- Calculates orderbook depth and strength indicators
- Provides market microstructure information for trading decisions

### 2. Technical Indicators

#### Simple Moving Average (SMA)
- Calculates 20-period SMA for trend identification
- Uses hysteresis to reduce false signals
- Adapts to market regime (trending vs ranging)

#### MACD (Moving Average Convergence Divergence)
- Calculates 12/26/9 EMA-based MACD
- Identifies golden cross and death cross signals
- Used in conjunction with other indicators for confirmation

#### Average True Range (ATR)
- Calculates 14-period ATR for volatility measurement
- Used for dynamic stop-loss and take-profit levels
- Provides market condition assessment

#### Bollinger Bands
- Calculates Bollinger Bands with 2.0 standard deviation
- Used for take-profit level determination
- Identifies potential reversal points

#### Market Regime Detection
- Analyzes price range over 50-period lookback
- Classifies market as trending or ranging based on price movement
- Adjusts trading parameters based on market regime

### 3. Signal Generation

#### Long Signal Conditions
- Price crosses below SMA with hysteresis
- Golden cross confirmed by MACD
- Orderbook strength confirms long bias
- Multiple indicator confirmation required

#### Short Signal Conditions
- Price crosses above SMA with hysteresis
- Death cross confirmed by MACD
- Orderbook strength confirms short bias
- Multiple indicator confirmation required

### 4. Risk Management

#### Position Sizing
- Fixed position size based on account balance
- Minimum position size enforced by exchange requirements
- Risk per trade limited by configuration parameters

#### Take Profit (TP) Calculation
- Multiple methods for TP calculation:
  - ATR-based (1.5 * ATR from entry)
  - Bollinger Bands-based
  - Volume-based (based on orderbook walls)
- Voting system selects the best TP value
- Fallback to fixed percentage offset

#### Stop Loss (SL) Management
- Initial SL set as percentage from entry price
- Trailing SL that follows favorable price movements
- Dynamic adjustment based on market conditions

### 5. Trading Logic

#### Entry Conditions
1. **Signal Confirmation**: At least 2 indicators confirm the signal
2. **Orderbook Validation**: Sufficient depth on the opposite side
3. **Market Regime**: Strategy adapts to trending or ranging conditions
4. **Risk Check**: Position size within limits and account has sufficient balance

#### Position Management
1. **Opening**: Market order with fixed quantity
2. **Closing Opposite**: Closes existing opposite position before opening new one
3. **TP/SL Setting**: Automatically sets take-profit and stop-loss levels
4. **Monitoring**: Continuously monitors position and adjusts TP/SL as needed

#### Exit Conditions
- Take-profit level reached
- Stop-loss level reached
- Opposite signal received (position reversal)
- Manual intervention (not implemented in current version)

### 6. Execution Flow

```
┌─────────────────┐
│ WebSocket Data  │
└─────────┬───────┘
          ▼
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│  K-line Data    │    │ Orderbook Data  │    │  Indicator      │
│ Processing      │    │ Processing      │    │ Calculations    │
└─────────┬───────┘    └─────────┬───────┘    └─────────┬───────┘
          ▼                      ▼                      ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Signal Generation                            │
│  - SMA + MACD confirmation                                      │
│  - Orderbook strength validation                                │
│  - Multiple indicator voting                                    │
└─────────────────────────────┬───────────────────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Risk Management                               │
│  - Position sizing                                              │
│  - TP/SL calculation                                            │
│  - Market regime adaptation                                     │
└─────────────────────────────┬───────────────────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                    Execution Decision                           │
│  - Validate risk parameters                                     │
│  - Check account balance                                        │
│  - Place market order if all conditions met                     │
└─────────────────────────────┬───────────────────────────────────┘
                              ▼
┌─────────────────────────────────────────────────────────────────┐
│                   Position Monitoring                           │
│  - Track P&L                                                    │
│  - Adjust TP/SL based on market movement                        │
│  - Handle position closure                                      │
└─────────────────────────────────────────────────────────────────┘
```

### 7. Market Regime Adaptation

#### Trending Market
- Wider hysteresis for SMA signals (1%)
- Higher ATR multiplier for TP levels
- Less frequent signal generation
- Longer holding periods

#### Ranging Market
- Tighter hysteresis for SMA signals (0.5%)
- Lower ATR multiplier for TP levels
- More frequent signal validation
- Shorter holding periods

### 8. Performance Metrics

#### Signal Statistics
- Total signals generated
- Correct signal rate
- False positive rate
- Accuracy percentage

#### Trading Performance
- Win/loss ratio
- Average profit per trade
- Maximum drawdown
- Sharpe ratio (simplified)

### 9. Configuration Parameters

#### Trading Parameters
- `TPPercent`: Take-profit percentage (default 0.5%)
- `SlPercent`: Stop-loss percentage (default 0.1%)
- `Symbol`: Trading pair (default BTCUSDT)
- `WindowSize`: Lookback window for indicators (default 20)

#### Technical Parameters
- `BBMult`: Bollinger Bands multiplier (default 2.0)
- `TpThresholdQty`: Volume threshold for TP calculation (default 500.0)
- `TrendThreshold`: Threshold for trend detection (default 3.0%)

### 10. Safety Mechanisms

#### Circuit Breakers
- Stops trading if consecutive losses exceed threshold
- Pauses strategy if API errors occur frequently
- Implements daily loss limits

#### Error Handling
- Graceful degradation when API unavailable
- Reconnection logic for WebSocket connections
- Fallback strategies when primary indicators fail