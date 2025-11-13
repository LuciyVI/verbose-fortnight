// WebUI JavaScript for Trading Bot Dashboard
let ws = null;
let isConnected = false;
let reconnectInterval = null;

// DOM Elements
const elements = {
    totalPnL: document.getElementById('totalPnL'),
    totalTrades: document.getElementById('totalTrades'),
    winRate: document.getElementById('winRate'),
    activePositions: document.getElementById('activePositions'),
    currentPrice: document.getElementById('currentPrice'),
    dailyChange: document.getElementById('dailyChange'),
    smaValue: document.getElementById('smaValue'),
    rsiValue: document.getElementById('rsiValue'),
    recentTradesTable: document.getElementById('recentTradesTable'),
    openPositionsTable: document.getElementById('openPositionsTable'),
    toggleBotBtn: document.getElementById('toggleBotBtn'),
    tradingMode: document.getElementById('tradingMode'),
    positionSize: document.getElementById('positionSize'),
    tpOffset: document.getElementById('tpOffset'),
    slPercentage: document.getElementById('slPercentage'),
    enableLogging: document.getElementById('enableLogging'),
    websocketStatus: document.querySelector('.websocket-status')
};

// WebSocket connection management
function connectWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws`;
    
    try {
        ws = new WebSocket(wsUrl);
        
        ws.onopen = function(event) {
            console.log('Connected to WebSocket');
            isConnected = true;
            updateConnectionStatus();
            sendMessage({ type: 'subscribe', data: 'dashboard' });
        };
        
        ws.onmessage = function(event) {
            const data = JSON.parse(event.data);
            updateDashboard(data);
        };
        
        ws.onclose = function(event) {
            console.log('Disconnected from WebSocket');
            isConnected = false;
            updateConnectionStatus();
            scheduleReconnect();
        };
        
        ws.onerror = function(error) {
            console.error('WebSocket error:', error);
            isConnected = false;
            updateConnectionStatus();
        };
    } catch (error) {
        console.error('Failed to create WebSocket connection:', error);
        scheduleReconnect();
    }
}

function scheduleReconnect() {
    if (reconnectInterval) return;  // Already scheduled
    
    setTimeout(() => {
        console.log('Attempting to reconnect...');
        connectWebSocket();
        reconnectInterval = null;
    }, 5000);  // Reconnect after 5 seconds
}

function updateConnectionStatus() {
    if (isConnected) {
        document.querySelector('.websocket-status').classList.remove('connecting');
        document.querySelector('.websocket-status').classList.add('connected');
    } else {
        document.querySelector('.websocket-status').classList.remove('connected');
        document.querySelector('.websocket-status').classList.add('connecting');
    }
}

function sendMessage(message) {
    if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(message));
    } else {
        console.warn('WebSocket is not open. Cannot send message.');
    }
}

// Dashboard data update functions
function updateDashboard(data) {
    if (data.type === 'dashboard_update') {
        updateStats(data.payload);
        updateMarketData(data.payload);
        updateRecentTrades(data.payload.recentTrades || []);
        updateOpenPositions(data.payload.openPositions || []);
    }
}

function updateStats(stats) {
    if (elements.totalPnL) elements.totalPnL.textContent = `$${(stats.totalPnL || 0).toFixed(2)}`;
    if (elements.totalTrades) elements.totalTrades.textContent = stats.totalTrades || 0;
    if (elements.winRate) elements.winRate.textContent = `${(stats.winRate || 0).toFixed(2)}%`;
    if (elements.activePositions) elements.activePositions.textContent = stats.activePositions || 0;
}

function updateMarketData(marketData) {
    if (elements.currentPrice) elements.currentPrice.value = (marketData.currentPrice || 0).toFixed(2);
    if (elements.dailyChange) elements.dailyChange.value = `${(marketData.dailyChange || 0).toFixed(2)}%`;
    if (elements.smaValue) elements.smaValue.value = (marketData.smaValue || 0).toFixed(2);
    if (elements.rsiValue) elements.rsiValue.value = (marketData.rsiValue || 0).toFixed(2);
}

function updateRecentTrades(trades) {
    if (!elements.recentTradesTable) return;
    
    elements.recentTradesTable.innerHTML = '';
    
    if (trades.length === 0) {
        elements.recentTradesTable.innerHTML = '<tr><td colspan="4" class="text-center">No recent trades</td></tr>';
        return;
    }
    
    // Limit to the 10 most recent trades
    const recent = trades.slice(-10).reverse();
    
    recent.forEach(trade => {
        const row = document.createElement('tr');
        
        // Format time
        const timeString = trade.timestamp ? new Date(trade.timestamp).toLocaleTimeString() : 'N/A';
        
        // Determine trade side styling
        const sideClass = trade.side === 'BUY' || trade.side === 'LONG' || trade.side === 'buy' ? 'text-success' : 'text-danger';
        const sideIcon = trade.side === 'BUY' || trade.side === 'LONG' || trade.side === 'buy' ? 'fa-arrow-up' : 'fa-arrow-down';
        
        // Format P&L with color
        const pnlClass = trade.pnl && parseFloat(trade.pnl) >= 0 ? 'text-success' : 'text-danger';
        const pnlValue = trade.pnl ? parseFloat(trade.pnl).toFixed(4) : '0.0000';
        
        row.innerHTML = `
            <td>${timeString}</td>
            <td class="${sideClass}"><i class="fas ${sideIcon}"></i> ${trade.side}</td>
            <td>${trade.price || '0.00'}</td>
            <td class="${pnlClass}">${pnlValue}</td>
        `;
        
        elements.recentTradesTable.appendChild(row);
    });
}

function updateOpenPositions(positions) {
    if (!elements.openPositionsTable) return;
    
    elements.openPositionsTable.innerHTML = '';
    
    if (positions.length === 0) {
        elements.openPositionsTable.innerHTML = '<tr><td colspan="9" class="text-center">No open positions</td></tr>';
        return;
    }
    
    positions.forEach(position => {
        const row = document.createElement('tr');
        
        // Determine position side styling
        const sideClass = position.side === 'LONG' || position.side === 'Buy' || position.side === 'BUY' ? 'text-success' : 'text-danger';
        const sideIcon = position.side === 'LONG' || position.side === 'Buy' || position.side === 'BUY' ? 'fa-arrow-up' : 'fa-arrow-down';
        
        // Format values
        const size = position.size || '0.000';
        const entryPrice = position.entryPrice ? parseFloat(position.entryPrice).toFixed(2) : '0.00';
        const markPrice = position.markPrice ? parseFloat(position.markPrice).toFixed(2) : '0.00';
        const takeProfit = position.takeProfit ? parseFloat(position.takeProfit).toFixed(2) : '0.00';
        const stopLoss = position.stopLoss ? parseFloat(position.stopLoss).toFixed(2) : '0.00';
        const upnl = position.unrealizedPnL ? parseFloat(position.unrealizedPnL).toFixed(4) : '0.0000';
        const upnlClass = position.unrealizedPnL && parseFloat(position.unrealizedPnL) >= 0 ? 'text-success' : 'text-danger';
        
        row.innerHTML = `
            <td>${position.symbol || 'BTCUSDT'}</td>
            <td class="${sideClass}"><i class="fas ${sideIcon}"></i> ${position.side}</td>
            <td>${size}</td>
            <td>${entryPrice}</td>
            <td>${markPrice}</td>
            <td>${takeProfit}</td>
            <td>${stopLoss}</td>
            <td class="${upnlClass}">${upnl}</td>
            <td>
                <button class="btn btn-sm btn-outline-danger close-position-btn" data-symbol="${position.symbol}" data-side="${position.side}">
                    Close
                </button>
            </td>
        `;
        
        elements.openPositionsTable.appendChild(row);
    });
    
    // Add event listeners to close position buttons
    document.querySelectorAll('.close-position-btn').forEach(button => {
        button.addEventListener('click', function() {
            const symbol = this.getAttribute('data-symbol');
            closePosition(symbol);
        });
    });
}

// Control functions
function toggleBot() {
    const isRunning = document.getElementById('toggleBotBtn').textContent.includes('Start');
    const action = isRunning ? 'start' : 'stop';
    
    sendMessage({
        type: 'bot_control',
        data: { action: action }
    });
    
    // Update button text and class
    const toggleBtn = document.getElementById('toggleBotBtn');
    if (isRunning) {
        toggleBtn.innerHTML = '<i class="fas fa-stop"></i> Stop Bot';
        toggleBtn.className = 'btn btn-danger';
    } else {
        toggleBtn.innerHTML = '<i class="fas fa-play"></i> Start Bot';
        toggleBtn.className = 'btn btn-success';
    }
}

function closePosition(symbol) {
    if (confirm(`Are you sure you want to close position for ${symbol}?`)) {
        sendMessage({
            type: 'close_position',
            data: { symbol: symbol }
        });
    }
}

function updateSettings() {
    const settings = {
        tradingMode: document.getElementById('tradingMode').value,
        positionSize: parseFloat(document.getElementById('positionSize').value),
        tpOffset: parseFloat(document.getElementById('tpOffset').value),
        slPercentage: parseFloat(document.getElementById('slPercentage').value),
        enableLogging: document.getElementById('enableLogging').checked,
        // Add ATR parameters that might be accessible from main controls
        TPAtrMultiplier: parseFloat(document.getElementById('atrTpMultiplier')?.value || '2.0'),
        SLAtrMultiplier: parseFloat(document.getElementById('atrSlMultiplier')?.value || '1.0'),
        AtrPeriod: parseInt(document.getElementById('atrPeriod')?.value || '14')
    };
    
    sendMessage({
        type: 'update_settings',
        data: settings
    });
}

// Initialize the page
document.addEventListener('DOMContentLoaded', function() {
    // Add event listeners
    document.getElementById('toggleBotBtn')?.addEventListener('click', toggleBot);
    
    // Add event listeners to input controls for auto-update
    document.getElementById('tradingMode')?.addEventListener('change', updateSettings);
    document.getElementById('positionSize')?.addEventListener('change', updateSettings);
    document.getElementById('tpOffset')?.addEventListener('change', updateSettings);
    document.getElementById('slPercentage')?.addEventListener('change', updateSettings);
    document.getElementById('enableLogging')?.addEventListener('change', updateSettings);
    
    // Settings modal save button
    document.getElementById('saveSettingsBtn')?.addEventListener('click', function() {
        // Collect settings from modal inputs and send to server
        const settings = {
            apiKey: document.getElementById('apiKey').value,
            apiSecret: document.getElementById('apiSecret').value,
            signalThreshold: parseInt(document.getElementById('signalThreshold').value),
            orderbookThreshold: parseFloat(document.getElementById('orderbookThreshold').value),
            TPAtrMultiplier: parseFloat(document.getElementById('atrTpMultiplier').value),
            SLAtrMultiplier: parseFloat(document.getElementById('atrSlMultiplier').value),
            AtrPeriod: parseInt(document.getElementById('atrPeriod').value),
            trailStopEnabled: document.getElementById('trailStopEnabled').checked
        };
        
        sendMessage({
            type: 'update_settings',
            data: settings
        });
        
        // Close the modal
        const modal = bootstrap.Modal.getInstance(document.getElementById('settingsModal'));
        if (modal) {
            modal.hide();
        }
    });
    
    // Connect to WebSocket
    connectWebSocket();
});

// Handle reload when disconnected
window.addEventListener('beforeunload', function(e) {
    if (ws) {
        ws.close();
    }
});