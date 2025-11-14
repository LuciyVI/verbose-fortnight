# Signal Generation Algorithm Flow

```mermaid
flowchart TD
    A[Start - SMA Worker Cycle] --> B[Calculate Technical Indicators]
    B --> C[Get Current Close Price and SMA Value]
    C --> D[Calculate Market Regime and Hysteresis]
    D --> E[Calculate RSI for Logging Only<br/>RSI Temporarily Disabled]
    E --> F[Calculate MACD Line and Signal Line]
    F --> G[Calculate ATR, Bollinger Bands]
    G --> H[Set Hysteresis Value<br/>Based on Market Regime]
    
    H --> I[Primary Signal Check:<br/>SMA Crossing Detection]
    I --> J{Close < SMA * (1-hysteresis)?}
    J -->|Yes| K[Set longSignal = true<br/>Log Primary LONG Signal]
    J -->|No| L{Close > SMA * (1+hysteresis)?}
    L -->|Yes| M[Set shortSignal = true<br/>Log Primary SHORT Signal]
    L -->|No| N[No Primary Signal]
    
    K --> O{Secondary Signal Check:<br/>MACD Confirmation<br/>AND !longSignal AND !shortSignal?}
    M --> O
    N --> O
    
    O -->|Yes| P{MACD Histogram > 0<br/>AND MACD Line > Signal Line?}
    O -->|No| T{Tertiary Signal Check:<br/>Golden Cross<br/>AND !longSignal AND !shortSignal?}
    
    P -->|Yes| Q[Set longSignal = true<br/>Log Secondary LONG Signal]
    P -->|No| R{MACD Histogram < 0<br/>AND MACD Line < Signal Line?}
    R -->|Yes| S[Set shortSignal = true<br/>Log Secondary SHORT Signal]
    R -->|No| T
    
    Q --> T
    S --> T
    T -->|Yes| U[Set longSignal = true<br/>Log Tertiary LONG Signal]
    T -->|No| V[No Additional Signals]
    
    U --> W{Send Signal?}
    V --> W
    W -->|longSignal = true| X[Send LONG Signal<br/>Kind: SMA_LONG]
    W -->|shortSignal = true| Y[Send SHORT Signal<br/>Kind: SMA_SHORT]
    W -->|Both false| Z[No Signal Sent]
    
    X --> AA[End Cycle - Wait 1 Second]
    Y --> AA
    Z --> AA
    AA --> A
    
    style A fill:#e1f5fe
    style AA fill:#e1f5fe
    style K fill:#c8e6c9
    style M fill:#ffcdd2
    style Q fill:#c8e6c9
    style S fill:#ffcdd2
    style U fill:#c8e6c9
    style X fill:#a5d6a7
    style Y fill:#ef9a9a
    style Z fill:#b0bec5
```

## Signal Generation Algorithm Summary

### 1. Primary Signals (SMA Crossing)
- LONG: When current close price < SMA * (1 - hysteresis)
- SHORT: When current close price > SMA * (1 + hysteresis)

### 2. Secondary Signals (MACD Confirmation)
- LONG: When MACD histogram > 0 AND MACD line > Signal line
- SHORT: When MACD histogram < 0 AND MACD line < Signal line
- Only evaluated if no primary signal exists

### 3. Tertiary Signals (Golden Cross)
- LONG: When golden cross detected
- Only evaluated if no primary or secondary signal exists

### 4. Hysteresis
- Default: 0.5%
- In trending regime: 1.0% (wider hysteresis)

### 5. Signal Priority
- Only one signal type is sent per cycle
- Priority order: Primary > Secondary > Tertiary
- No signal is sent if conditions are not met

> Note: RSI has been temporarily disabled from signal generation but is still calculated for logging purposes.