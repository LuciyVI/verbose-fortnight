# Trading Bot Architecture & Algorithm Diagrams

## 1. System Architecture

```mermaid
graph TB
    subgraph "Main Application"
        Main[Main Function]
    end
    
    subgraph "Configuration Layer"
        Config[Configuration Package]
    end
    
    subgraph "Data Layer"
        Types[Types Package]
        Logger[Logger Package]
    end
    
    subgraph "External Services"
        WS[WebSocket API]
        REST[REST API]
    end
    
    subgraph "Business Logic Layer"
        A[API Package]
        P[Position Package]
        S[Strategy Package]
        I[Indicators Package]
        W[WebSocket Package]
        Int[Interfaces Package]
    end
    
    Main --> Config
    Main --> Types
    Main --> Logger
    Main --> A
    Main --> P
    Main --> S
    Main --> I
    Main --> W
    
    A --> REST
    W --> WS
    A --> Logger
    P --> A
    S --> A
    S --> P
    S --> W
    S --> I
    P --> Logger
    W --> Logger
    Config --> A
    Config --> P
    Config --> S
    Config --> W
```

## 2. Package Dependencies

```mermaid
graph TD
    Main --> Config
    Main --> Logger
    Main --> API
    Main --> Position
    Main --> Strategy
    Main --> WebSocket
    Main --> Indicators
    
    Config -.-> API
    Config -.-> Position
    Config -.-> Strategy
    Config -.-> WebSocket
    
    Logger --> API
    Logger --> Position
    Logger --> Strategy
    Logger --> WebSocket
    
    API --> REST
    WebSocket --> WS
    
    Position --> API
    Strategy --> API
    Strategy --> Position
    Strategy --> WebSocket
    Strategy --> Indicators
```

## 3. Trading Algorithm Flow

```mermaid
flowchart TD
    A[WebSocket Data Received] --> B{Data Type}
    B -->|K-line| C[K-line Processing]
    B -->|Orderbook| D[Orderbook Processing]
    
    C --> E[Update Historical Data]
    D --> F[Update Orderbook Snapshot]
    
    E --> G[Calculate Indicators]
    F --> G
    G --> H[Simple Moving Average]
    G --> I[MACD Calculation]
    G --> J[ATR Calculation]
    G --> K[Bollinger Bands]
    G --> L[Market Regime Detection]
    
    H --> M[Signal Generation]
    I --> M
    J --> M
    K --> M
    L --> M
    
    M --> N{Signal Conditions Met?}
    N -->|Yes| O[Validate Risk Parameters]
    N -->|No| P[Wait for Next Data]
    P --> A
    
    O --> Q{Risk Check Passed?}
    Q -->|Yes| R[Execute Trade]
    Q -->|No| P
    
    R --> S[Calculate TP/SL]
    S --> T[Monitor Position]
    T --> U{Exit Condition Met?}
    U -->|TP/SL Hit| V[Close Position]
    U -->|Opposite Signal| W[Reverse Position]
    U -->|Still Open| X{Update TP/SL?}
    X -->|Yes| Y[Adjust TP/SL]
    Y --> T
    X -->|No| T
    U -->|Time Expiry| Z[Close Position]
    
    V --> AA[Record Performance]
    W --> AA
    Z --> AA
    AA --> A
```

## 4. Component Interaction Flow

```mermaid
sequenceDiagram
    participant Main as Main App
    participant Config as Config
    participant WS as WebSocket
    participant API as API
    participant Strat as Strategy
    participant Pos as Position
    participant Ind as Indicators
    
    Main->>Config: Load configuration
    Main->>WS: Initialize connections
    Main->>API: Initialize API client
    Main->>Strat: Initialize strategy
    Main->>Pos: Initialize position manager
    
    loop Data Processing
        WS->>Strat: K-line data
        WS->>Strat: Orderbook data
        Strat->>Ind: Request indicators
        Ind-->>Strat: Calculated values
        Strat->>Strat: Generate signals
    end
    
    loop Signal Processing
        Strat->>Pos: Check position
        Pos-->>Strat: Position status
        Strat->>API: Place order if needed
        API-->>Strat: Order confirmation
        Strat->>API: Set TP/SL
        API-->>Strat: TP/SL confirmation
    end
```

## 5. Error Handling Flow

```mermaid
flowchart TD
    A[Error Occurs] --> B{Error Type}
    B -->|API Error| C[Log API Error]
    B -->|Network Error| D[Connection Recovery]
    B -->|Data Error| E[Validate Data]
    B -->|Business Logic Error| F[Handle in Component]
    
    C --> G{Retry Possible?}
    D --> H{Reconnect?}
    E --> I{Skip Data?}
    
    G -->|Yes| J[Retry Operation]
    G -->|No| K[Update Error Stats]
    
    H -->|Yes| L[Reconnect Process]
    H -->|No| K
    
    I -->|Yes| M[Skip and Continue]
    I -->|No| K
    
    J --> K
    L --> K
    M --> K
    F --> K
    
    K --> N[Continue Operation]
```