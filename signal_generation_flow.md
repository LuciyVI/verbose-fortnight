# Signal Generation Algorithm Flow

```mermaid
flowchart TD
    A[Start - SMA Worker Cycle] --> B[Calculate Technical Indicators]
    B --> C[Get Current Close Price and SMA Value]
    C --> D[Calculate Market Regime and Hysteresis]
    D --> E[Calculate RSI for Logging Only\nRSI Temporarily Disabled]
    E --> F[Calculate MACD Line and Signal Line]
    F --> G[Calculate ATR, Bollinger Bands]
    G --> H[Set Hysteresis Value\nBased on Market Regime]
    
    H --> I[Primary Signal Check:\nSMA Crossing Detection]
    I --> J{Is Close below SMA * (1 - hysteresis)?}
    J -->|Yes| K[Set longSignal = true\nLog Primary LONG Signal]
    J -->|No| L{Is Close above SMA * (1 + hysteresis)?}
    L -->|Yes| M[Set shortSignal = true\nLog Primary SHORT Signal]
    L -->|No| N[No Primary Signal]
    
    K --> O{Secondary Signal Check:\nMACD Confirmation\nand not longSignal and not shortSignal?}
    M --> O
    N --> O
    
    O -->|Yes| P{MACD Histogram greater than 0\nand MACD Line greater than Signal Line?}
    O -->|No| T{Tertiary Signal Check:\nGolden Cross\nand not longSignal and not shortSignal?}
    
    P -->|Yes| Q[Set longSignal = true\nLog Secondary LONG Signal]
    P -->|No| R{MACD Histogram less than 0\nand MACD Line less than Signal Line?}
    R -->|Yes| S[Set shortSignal = true\nLog Secondary SHORT Signal]
    R -->|No| T
    
    Q --> T
    S --> T
    T -->|Yes| U[Set longSignal = true\nLog Tertiary LONG Signal]
    T -->|No| V[No Additional Signals]
    
    U --> W{Send Signal?}
    V --> W
    W -->|longSignal = true| X[Send LONG Signal\nKind: SMA_LONG]
    W -->|shortSignal = true| Y[Send SHORT Signal\nKind: SMA_SHORT]
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
