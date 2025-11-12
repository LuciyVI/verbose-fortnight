# Signal Generation Algorithm Flow (with Bollinger Bands)

```mermaid
flowchart TD
    A[Start - SMA Worker Cycle] --> B[Calculate technical indicators]
    B --> C[Get Close and SMA]
    C --> D[Determine market regime and hysteresis h]
    D --> E[RSI for logging only]
    E --> F[Compute MACD line, signal, histogram]
    F --> G[Compute ATR and Bollinger Bands]
    G --> H[Set hysteresis by regime]
    H --> I[Compute thresholds low/high from SMA and h\nCompute BB upper/middle/lower]

    I --> J{Close < low?}
    J -->|Yes| K[longSignal=true\nlog: primary LONG]
    J -->|No| L{Close > high?}
    L -->|Yes| M[shortSignal=true\nlog: primary SHORT]
    L -->|No| N[no primary signal]

    K --> O{Secondary: MACD confirm\nonly if no signal}
    M --> O
    N --> O

    O -->|hist>0 and line>signal| Q[longSignal=true\nlog: secondary LONG]
    O -->|hist<0 and line<signal| S[shortSignal=true\nlog: secondary SHORT]
    O -->|otherwise| T{Tertiary: Golden Cross?\nonly if no signal}

    Q --> T
    S --> T
    T -->|Yes| U[longSignal=true\nlog: tertiary LONG]
    T -->|No| V{Quaternary: Bollinger check\nonly if no signal}

    U --> V
    V -->|Yes| W{Close <= BB lower?}
    W -->|Yes| X[longSignal=true\nlog: quaternary LONG]
    W -->|No| Y{Close >= BB upper?}
    Y -->|Yes| Z[shortSignal=true\nlog: quaternary SHORT]
    Y -->|No| AA[no additional signals]

    X --> AB{Send signal?}
    Z --> AB
    AA --> AB

    AB -->|longSignal=true| AC[Send LONG · Kind=SMA_LONG]
    AB -->|shortSignal=true| AD[Send SHORT · Kind=SMA_SHORT]
    AB -->|none| AE[No signal sent]

    AC --> AF[End cycle - wait 1s]
    AD --> AF
    AE --> AF
    AF --> A

    %% Styles
    style A fill:#e1f5fe
    style AF fill:#e1f5fe
    style K fill:#c8e6c9
    style M fill:#ffcdd2
    style Q fill:#c8e6c9
    style S fill:#ffcdd2
    style U fill:#c8e6c9
    style X fill:#c8e6c9
    style Z fill:#ffcdd2
    style AC fill:#a5d6a7
    style AD fill:#ef9a9a
    style AE fill:#b0bec5
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

### 4. Quaternary Signals (Bollinger Bands)
- LONG: When current close price <= Bollinger Lower Band (potential reversal to the upside)
- SHORT: When current close price >= Bollinger Upper Band (potential reversal to the downside)
- Only evaluated if no primary, secondary, or tertiary signal exists

### 5. Hysteresis
- Default: 0.5%
- In trending regime: 1.0% (wider hysteresis)

### 6. Signal Priority
- Only one signal type is sent per cycle
- Priority order: Primary > Secondary > Tertiary > Quaternary
- No signal is sent if conditions are not met

> Note: RSI has been temporarily disabled from signal generation but is still calculated for logging purposes.
