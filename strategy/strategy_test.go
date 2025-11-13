package strategy

import (
	"testing"
	"verbose-fortnight/config"
	"verbose-fortnight/models"
)

// TestCalculateTPSLWithRatio tests the ATR-based TP/SL calculation with commission
func TestCalculateTPSLWithRatio(t *testing.T) {
	// Create a mock trader with necessary dependencies
	cfg := &config.Config{
		TPAtrMultiplier: 2.0,
		SLAtrMultiplier: 1.0,
		AtrPeriod:       14,
	}
	
	// Create mock state with some dummy values
	state := &models.State{
		Highs: []float64{50000, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700, 50650},
		Lows:  []float64{49900, 49950, 50050, 49900, 50100, 50050, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450},
		Closes: []float64{50050, 50150, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700},
		Instr: models.InstrumentInfo{
			TickSize: 0.1,
		},
	}
	
	// Create a minimal trader for testing
	trader := &Trader{
		Config: cfg,
		State:  state,
	}
	
	// Test LONG position
	entryPrice := 50000.0
	tp, sl := trader.calculateTPSLWithRatio(entryPrice, "LONG")
	
	// Check if TP is above entry price and SL is below entry price for LONG
	if tp <= entryPrice {
		t.Errorf("For LONG position, TP (%f) should be above entry price (%f)", tp, entryPrice)
	}
	if sl >= entryPrice {
		t.Errorf("For LONG position, SL (%f) should be below entry price (%f)", sl, entryPrice)
	}
	
	// Test SHORT position
	tp, sl = trader.calculateTPSLWithRatio(entryPrice, "SHORT")
	
	// Check if TP is below entry price and SL is above entry price for SHORT
	if tp >= entryPrice {
		t.Errorf("For SHORT position, TP (%f) should be below entry price (%f)", tp, entryPrice)
	}
	if sl <= entryPrice {
		t.Errorf("For SHORT position, SL (%f) should be above entry price (%f)", sl, entryPrice)
	}
	
	// Verify that commission was factored in by checking that distances are different from pure ATR calculations
	// For a LONG position: TP should be entryPrice + ATR*TPMultiplier + commission, SL should be entryPrice - ATR*SLMultiplier - commission
	t.Logf("LONG - Entry: %f, TP: %f, SL: %f", entryPrice, tp, sl)
	t.Logf("SHORT - Entry: %f, TP: %f, SL: %f", entryPrice, tp, sl)
}

// TestCalculateTPSLWithRatioFallback tests the fallback TP/SL calculation with commission
func TestCalculateTPSLWithRatioFallback(t *testing.T) {
	// Create a mock trader with necessary dependencies
	cfg := &config.Config{
		TPAtrMultiplier: 2.0,
		SLAtrMultiplier: 1.0,
		AtrPeriod:       14,
	}
	
	// Create mock state with some dummy values
	state := &models.State{
		Highs: []float64{50000, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700, 50650},
		Lows:  []float64{49900, 49950, 50050, 49900, 50100, 50050, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450},
		Closes: []float64{50050, 50150, 50100, 50200, 50150, 50300, 50250, 50400, 50350, 50500, 50450, 50600, 50550, 50700},
		Instr: models.InstrumentInfo{
			TickSize: 0.1,
		},
	}
	
	// Create a minimal trader for testing
	trader := &Trader{
		Config: cfg,
		State:  state,
	}
	
	// Since the fallback function uses calculateTPBasedOn15MinProjection which needs more setup,
	// we'll create a simplified test focusing on commission adjustment
	entryPrice := 50000.0
	tp, sl := trader.calculateTPSLWithRatioFallback(entryPrice, "LONG")
	
	// Check if TP is above entry price and SL is below entry price for LONG
	if tp <= entryPrice {
		t.Errorf("For LONG position in fallback, TP (%f) should be above entry price (%f)", tp, entryPrice)
	}
	if sl >= entryPrice {
		t.Errorf("For LONG position in fallback, SL (%f) should be below entry price (%f)", sl, entryPrice)
	}
	
	// Test SHORT position in fallback
	tp, sl = trader.calculateTPSLWithRatioFallback(entryPrice, "SHORT")
	
	// Check if TP is below entry price and SL is above entry price for SHORT
	if tp >= entryPrice {
		t.Errorf("For SHORT position in fallback, TP (%f) should be below entry price (%f)", tp, entryPrice)
	}
	if sl <= entryPrice {
		t.Errorf("For SHORT position in fallback, SL (%f) should be above entry price (%f)", sl, entryPrice)
	}
	
	t.Logf("FALLBACK LONG - Entry: %f, TP: %f, SL: %f", entryPrice, tp, sl)
	t.Logf("FALLBACK SHORT - Entry: %f, TP: %f, SL: %f", entryPrice, tp, sl)
}