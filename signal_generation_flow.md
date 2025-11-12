# Signal Generation Algorithm Flow

```mermaid
flowchart TD
    A[Start - SMA Worker Cycle] --> B[Calculate technical indicators]
    B --> C[Get Close and SMA]
    C --> D[Determine market regime and hysteresis h]
    D --> E[RSI for logging only]
    E --> F[Compute MACD line, signal, histogram]
    F --> G[Compute ATR and Bollinger Bands]
    G --> H[Set hysteresis by regime]
    H --> I[Compute thresholds low/high]

    I --> J{Is Close < low?}
    J -->|Yes| K[longSignal=true; log primary LONG]
    J -->|No| L{Is Close > high?}
    L -->|Yes| M[shortSignal=true; log primary SHORT]
    L -->|No| N[no primary signal]

    K --> O{Secondary check: MACD confirm\nonly if no signal}
    M --> O
    N --> O

    O -->|hist>0 and line>signal| Q[longSignal=true; log secondary LONG]
    O -->|hist<0 and line<signal| S[shortSignal=true; log secondary SHORT]
    O -->|otherwise| T{Tertiary check: Golden Cross?\nonly if no signal}

    Q --> T
    S --> T
    T -->|Yes| U[longSignal=true; log tertiary LONG]
    T -->|No| V[no additional signals]

    U --> W{Send signal?}
    V --> W
    W -->|longSignal=true| X[Send LONG; Kind=SMA_LONG]
    W -->|shortSignal=true| Y[Send SHORT; Kind=SMA_SHORT]
    W -->|none| Z[No signal sent]

    X --> AA[End cycle - wait 1s]
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
