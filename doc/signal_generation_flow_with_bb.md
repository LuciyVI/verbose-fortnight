# Signal Generation Algorithm Flow (with Bollinger Bands)

```mermaid
flowchart TD
    A[Start - SMA Worker Cycle] --> B[Calculate Technical Indicators]
    B --> C[Get Current Close Price and SMA Value]
    C --> D[Calculate Market Regime and Hysteresis]
    D --> E[Calculate RSI for Logging Only<br/>RSI Temporarily Disabled]
    E --> F[Calculate MACD Line and Signal Line]
    F --> G[Calculate ATR, Bollinger Bands<br/>bbUpper, bbMiddle, bbLower]
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
    T -->|No| V[Quaternary Signal Check:<br/>Bollinger Bands<br/>AND !longSignal AND !shortSignal?]
    
    U --> V
    V -->|Yes| W{Close <= Bollinger Lower Band?}
    W -->|Yes| X[Set longSignal = true<br/>Log Quaternary LONG Signal]
    W -->|No| Y{Close >= Bollinger Upper Band?}
    Y -->|Yes| Z[Set shortSignal = true<br/>Log Quaternary SHORT Signal]
    Y -->|No| AA[No Additional Signals]
    
    X --> AB[Send Signal?]
    Z --> AB
    AA --> AB
    
    AB -->|longSignal = true| AC[Send LONG Signal<br/>Kind: SMA_LONG]
    AB -->|shortSignal = true| AD[Send SHORT Signal<br/>Kind: SMA_SHORT]
    AB -->|Both false| AE[No Signal Sent]
    
    AC --> AF[End Cycle - Wait 1 Second]
    AD --> AF
    AE --> AF
    AF --> A
    
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