# Architecture Documentation

## Overview
The trading bot is built with a modular architecture, separating concerns into distinct packages that each handle specific aspects of the trading system. This design promotes maintainability, testability, and scalability.

## Package Structure

### 1. `main` Package
- Entry point of the application
- Orchestrates all other components
- Handles command-line arguments
- Initializes configuration and all services

### 2. `config` Package
- Manages application configuration
- Provides default configuration values
- Supports loading configuration from JSON files
- Handles sensitive information like API keys

### 3. `types` Package
- Defines all data structures used across the application
- Includes types for trading entities (positions, orders, klines, etc.)
- Provides type safety and consistency across packages

### 4. `logger` Package
- Implements structured logging with multiple levels (Debug, Info, Warn, Error)
- Provides context-aware logging with additional fields
- Supports color-coded output for different log types
- Handles both console and file logging

### 5. `api` Package
- Handles all REST API communications with the trading exchange
- Manages authentication and signing of requests
- Implements rate limiting and error handling
- Provides methods for order placement, position management, and market data retrieval

### 6. `position` Package
- Manages trading positions (entry, exit, TP/SL)
- Handles position lifecycle and state transitions
- Implements position sizing logic
- Coordinates entry and exit strategies

### 7. `websocket` Package
- Manages WebSocket connections for real-time data
- Handles public data streams (klines, orderbook)
- Manages private data streams (positions, orders)
- Implements connection management and reconnection logic

### 8. `strategy` Package
- Implements trading strategies and signal generation
- Contains logic for technical analysis and indicator calculations
- Handles signal processing and execution decisions
- Manages market regime detection

### 9. `indicators` Package
- Contains technical analysis functions (SMA, ATR, RSI, MACD, etc.)
- Provides mathematical calculations for trading indicators
- Implements statistical functions used across the system

### 10. `interfaces` Package
- Defines interfaces for all major components
- Enables dependency injection and mocking for testing
- Provides abstraction layers between components

## Component Interactions

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│    Main App     │────│   Strategy      │────│   Indicators    │
└─────────────────┘    └─────────────────┘    └─────────────────┘
          │                       │                       │
          ▼                       ▼                       ▼
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   WebSocket     │────│   Position      │────│      API        │
└─────────────────┘    └─────────────────┘    └─────────────────┘
          │                       │                       │
          └───────────────────────┼───────────────────────┘
                                  ▼
                         ┌─────────────────┐
                         │    Logger       │
                         └─────────────────┘
                                  │
                                  ▼
                         ┌─────────────────┐
                         │    Config       │
                         └─────────────────┘
```

## Design Patterns

- **Dependency Injection**: Services are provided to components at initialization
- **Interface Segregation**: Each package implements specific interfaces for better testability
- **Single Responsibility**: Each package has a clear, focused purpose
- **Layered Architecture**: Clear separation between business logic and infrastructure

## Configuration Management

- API keys and secrets are kept in configuration files
- Runtime configuration can be loaded from JSON files
- Default configuration values are provided for all parameters
- Environment-specific configurations are supported

## Error Handling Strategy

- All errors are properly wrapped with context using `fmt.Errorf` and `%w`
- Comprehensive error logging with contextual information
- Graceful degradation when possible
- Clear error propagation throughout the system