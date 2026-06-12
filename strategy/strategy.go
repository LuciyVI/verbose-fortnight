package strategy

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/indicators"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/order"
	"verbose-fortnight/position"
)

// Trader handles trading logic and signal processing
type Trader struct {
	APIClient       *api.RESTClient
	Config          *config.Config
	State           *models.State
	OrderManager    *order.OrderManager
	PositionManager *position.PositionManager
	Logger          logging.LoggerInterface
	Now             func() time.Time

	ReversalLongDetector *ReversalLongDetector
}

type tpslCalcDetails struct {
	entry            float64
	side             string
	regime           string
	atr              float64
	atrPercent       float64
	tpPercRaw        float64
	tpPercClamped    float64
	tpPercMin        float64
	tpPercMax        float64
	k                float64
	volatilityFactor float64
	regimeFactor     float64

	tpDistBase float64
	tpDist     float64
}

type dynamicTPDetails struct {
	ATRPercent       float64
	K                float64
	VolatilityFactor float64
	RegimeFactor     float64
	RawPercent       float64
	ClampedPercent   float64
	MinPercent       float64
	MaxPercent       float64
}

const (
	tpslWarningRawRRBelowTarget         = "RAW_RR_BELOW_TARGET"
	tpslWarningNetRRBelowTarget         = "NET_RR_BELOW_TARGET"
	tpslWarningNetTPNonPositive         = "NET_TP_NON_POSITIVE"
	tpslWarningNetSLNonPositive         = "NET_SL_NON_POSITIVE"
	tpslWarningWrongSideTP              = "WRONG_SIDE_TP"
	tpslWarningWrongSideSL              = "WRONG_SIDE_SL"
	tpslWarningTPDistanceLESLDistance   = "TP_DISTANCE_LE_SL_DISTANCE"
	tpslWarningRoundingWorsenedRR       = "ROUNDING_WORSENED_RR"
	tpslWarningNegativeBreakeven        = "NEGATIVE_BREAKEVEN"
	tpslWarningFinalDiffGTOneTick       = "FINAL_DIFF_GT_ONE_TICK"
	tpslWarningDynamicTPDisabledButUsed = "DYNAMIC_TP_DISABLED_BUT_USED"
	tpslWarningConfigUnusedTargetRR     = "CONFIG_UNUSED_TARGET_RR"
	tpslWarningConfigUnusedATRCap       = "CONFIG_UNUSED_ATR_TP_CAP"
	tpslWarningConfigUnusedMinProfit    = "CONFIG_UNUSED_MIN_PROFIT_PERC"
	tpslWarningConfigUnusedTPFeeFloor   = "CONFIG_UNUSED_TP_FEE_FLOOR_MULT"
	tpslWarningConfigUnusedSLFeeFloor   = "CONFIG_UNUSED_SL_FEE_FLOOR_MULT"
)

type tpslDiagnosticContext struct {
	Symbol    string
	Reason    string
	SignalID  string
	CandleSeq uint64
	Qty       float64
	Timestamp time.Time
	Log       bool
}

// TPSLDiagnosticsInput contains deterministic inputs for TP/SL diagnostics.
type TPSLDiagnosticsInput struct {
	Symbol    string
	Side      string
	Reason    string
	SignalID  string
	CandleSeq uint64
	Timestamp time.Time

	Qty   float64
	Entry float64

	RawTP     float64
	RawSL     float64
	RoundedTP float64
	RoundedSL float64
	FinalTP   float64
	FinalSL   float64

	MarketRegime string
	ATR          float64
	ATRPeriod    int

	DynamicTPEnabled        bool
	DynamicTPUsed           bool
	DynamicTPRawPercent     float64
	DynamicTPClampedPercent float64
	DynamicTPMinPercent     float64
	DynamicTPMaxPercent     float64
	DynamicTPK              float64
	DynamicTPVolatility     float64
	DynamicTPRegimeFactor   float64

	TargetRR              float64
	TargetRRSource        string
	TargetRRUsed          bool
	TargetRRConfigPresent bool
	ATRCapConfigPresent   bool
	ATRCapUsed            bool

	TickSize             float64
	TPRoundingDirection  string
	SLRoundingDirection  string
	RoundingMode         string
	MinPocketDistance    float64
	MinProfitPerc        float64
	TPFeeFloorMult       float64
	SLFeeFloorMult       float64
	MinRequiredTPDist    float64
	MinRequiredSLDist    float64
	TPDistanceIncreased  bool
	SLDistanceIncreased  bool
	MinProfitUsed        bool
	TPFeeFloorUsed       bool
	SLFeeFloorUsed       bool
	FinalTPDistSource    string
	RoundTripFeePerc     float64
	MakerTakerAssumption string
	FeeAdjustedBuffer    float64
	PositionIdx          int
	TPSLMode             string
	TriggerBy            string
	FormattedTP          string
	FormattedSL          string
}

// TPSLDiagnostics captures TP/SL, RR, fee, rounding, and validation diagnostics.
type TPSLDiagnostics struct {
	Symbol    string
	Side      string
	Reason    string
	SignalID  string
	CandleSeq uint64
	Timestamp time.Time

	Qty   float64
	Entry float64

	RawTP     float64
	RawSL     float64
	RoundedTP float64
	RoundedSL float64
	FinalTP   float64
	FinalSL   float64

	RawTPDistAbs   float64
	RawSLDistAbs   float64
	TPDistAbs      float64
	SLDistAbs      float64
	TPDistPct      float64
	SLDistPct      float64
	RawRR          float64
	FinalRR        float64
	TargetRR       float64
	TargetRRSource string
	TargetRRUsed   bool

	GrossTPPnL               float64
	GrossSLLoss              float64
	FeeRate                  float64
	MakerTakerAssumption     string
	EstimatedOpenFee         float64
	EstimatedTPCloseFee      float64
	EstimatedSLCloseFee      float64
	EstimatedRoundTripFeeTP  float64
	EstimatedRoundTripFeeSL  float64
	NetTPPnL                 float64
	NetSLLoss                float64
	NetRR                    float64
	FeeAdjustedBreakeven     float64
	EstimatedLossAtCurrentSL float64
	BreakevenSafe            bool

	ATR          float64
	ATRPeriod    int
	MarketRegime string

	DynamicTPEnabled        bool
	DynamicTPUsed           bool
	DynamicTPRawPercent     float64
	DynamicTPClampedPercent float64
	DynamicTPMinPercent     float64
	DynamicTPMaxPercent     float64
	DynamicTPK              float64
	DynamicTPVolatility     float64
	DynamicTPRegimeFactor   float64

	TickSize            float64
	TPRoundingDirection string
	SLRoundingDirection string
	RoundingMode        string
	RoundingWorsenedRR  bool

	MinPocketDistance   float64
	MinProfitPerc       float64
	TPFeeFloorMult      float64
	SLFeeFloorMult      float64
	MinRequiredTPDist   float64
	MinRequiredSLDist   float64
	TPDistanceIncreased bool
	SLDistanceIncreased bool
	MinProfitUsed       bool
	TPFeeFloorUsed      bool
	SLFeeFloorUsed      bool
	MinDistanceOk       bool
	FinalTPDistSource   string

	TPOnCorrectSide bool
	SLOnCorrectSide bool
	Valid           bool
	NetRROk         bool
	Warnings        []string

	PositionIdx int
	TPSLMode    string
	TriggerBy   string
	FormattedTP string
	FormattedSL string
}

func (t *Trader) dynamicTP(entry, atr float64, regime string) dynamicTPDetails {
	k := t.Config.DynamicTPK
	if k <= 0 {
		k = 1.0
	}
	volFactor := t.Config.DynamicTPVolatilityFactor
	if volFactor <= 0 {
		volFactor = 6.0
	}
	minPerc := t.Config.DynamicTPMinPerc
	if minPerc <= 0 {
		minPerc = 0.4
	}
	maxPerc := t.Config.DynamicTPMaxPerc
	if maxPerc <= 0 {
		maxPerc = 1.8
	}
	if maxPerc < minPerc {
		maxPerc = minPerc
	}

	regimeFactor := t.regimeFactor(regime)

	atrPerc := 0.0
	if entry > 0 && atr > 0 {
		atrPerc = atr / entry * 100
	}
	raw := k * atrPerc * volFactor * regimeFactor
	clamped := clamp(raw, minPerc, maxPerc)
	if math.IsNaN(clamped) || math.IsInf(clamped, 0) {
		clamped = minPerc
	}

	return dynamicTPDetails{
		ATRPercent:       atrPerc,
		K:                k,
		VolatilityFactor: volFactor,
		RegimeFactor:     regimeFactor,
		RawPercent:       raw,
		ClampedPercent:   clamped,
		MinPercent:       minPerc,
		MaxPercent:       maxPerc,
	}
}

func appendTPSLWarning(warnings []string, warning string) []string {
	return addUniqueString(warnings, warning)
}

func tpslDirectionalDistances(side string, entry, tp, sl float64) (float64, float64, bool, bool) {
	switch strings.ToUpper(side) {
	case "LONG":
		return tp - entry, entry - sl, tp > entry, sl < entry
	case "SHORT":
		return entry - tp, sl - entry, tp < entry, sl > entry
	default:
		return 0, 0, false, false
	}
}

func tpslRatio(numerator, denominator float64) float64 {
	if denominator <= 0 {
		return 0
	}
	return numerator / denominator
}

func tpslPct(distance, entry float64) float64 {
	if entry <= 0 {
		return 0
	}
	return distance / entry * 100
}

// BuildTPSLDiagnostics calculates deterministic TP/SL diagnostics without changing trading decisions.
func BuildTPSLDiagnostics(in TPSLDiagnosticsInput) TPSLDiagnostics {
	side := strings.ToUpper(in.Side)
	finalTP := in.FinalTP
	if finalTP == 0 {
		finalTP = in.RoundedTP
	}
	if finalTP == 0 {
		finalTP = in.RawTP
	}
	finalSL := in.FinalSL
	if finalSL == 0 {
		finalSL = in.RoundedSL
	}
	if finalSL == 0 {
		finalSL = in.RawSL
	}
	roundedTP := in.RoundedTP
	if roundedTP == 0 {
		roundedTP = finalTP
	}
	roundedSL := in.RoundedSL
	if roundedSL == 0 {
		roundedSL = finalSL
	}
	rawTP := in.RawTP
	if rawTP == 0 {
		rawTP = finalTP
	}
	rawSL := in.RawSL
	if rawSL == 0 {
		rawSL = finalSL
	}

	rawTPDist, rawSLDist, _, _ := tpslDirectionalDistances(side, in.Entry, rawTP, rawSL)
	finalTPDist, finalSLDist, tpCorrect, slCorrect := tpslDirectionalDistances(side, in.Entry, finalTP, finalSL)
	if rawTPDist < 0 {
		rawTPDist = math.Abs(rawTPDist)
	}
	if rawSLDist < 0 {
		rawSLDist = math.Abs(rawSLDist)
	}
	if finalTPDist < 0 {
		finalTPDist = math.Abs(finalTPDist)
	}
	if finalSLDist < 0 {
		finalSLDist = math.Abs(finalSLDist)
	}

	rawRR := tpslRatio(rawTPDist, rawSLDist)
	finalRR := tpslRatio(finalTPDist, finalSLDist)
	feeRate := in.RoundTripFeePerc
	if feeRate <= 0 {
		feeRate = 0.0012
	}
	feeHalf := feeRate / 2
	openFee := in.Entry * in.Qty * feeHalf
	tpCloseFee := finalTP * in.Qty * feeHalf
	slCloseFee := finalSL * in.Qty * feeHalf
	roundTripFeeTP := openFee + tpCloseFee
	roundTripFeeSL := openFee + slCloseFee
	grossTP := finalTPDist * in.Qty
	grossSL := finalSLDist * in.Qty
	netTP := grossTP - roundTripFeeTP
	netSL := grossSL + roundTripFeeSL
	netRR := tpslRatio(netTP, netSL)

	feeBuffer := in.FeeAdjustedBuffer
	if feeBuffer == 0 && in.Entry > 0 {
		feeBuffer = in.Entry * feeRate
	}
	feeAdjustedBreakeven := in.Entry
	breakevenSafe := false
	estimatedLossAtSL := netSL
	switch side {
	case "LONG":
		feeAdjustedBreakeven = in.Entry + feeBuffer
		breakevenSafe = finalSL >= feeAdjustedBreakeven
		if finalSL >= in.Entry {
			estimatedLossAtSL = math.Max(0, (feeAdjustedBreakeven-finalSL)*in.Qty)
		}
	case "SHORT":
		feeAdjustedBreakeven = in.Entry - feeBuffer
		breakevenSafe = finalSL <= feeAdjustedBreakeven
		if finalSL <= in.Entry {
			estimatedLossAtSL = math.Max(0, (finalSL-feeAdjustedBreakeven)*in.Qty)
		}
	}

	tick := in.TickSize
	eps := math.Max(1e-9, tick*1e-9)
	minDistanceOk := true
	if in.MinRequiredTPDist > 0 && finalTPDist+eps < in.MinRequiredTPDist {
		minDistanceOk = false
	}
	if in.MinRequiredSLDist > 0 && finalSLDist+eps < in.MinRequiredSLDist {
		minDistanceOk = false
	}
	targetRR := in.TargetRR
	if targetRR == 0 {
		targetRR = 1
	}

	warnings := []string{}
	if rawRR > 0 && rawRR+eps < targetRR {
		warnings = appendTPSLWarning(warnings, tpslWarningRawRRBelowTarget)
	}
	if netRR > 0 && netRR+eps < targetRR {
		warnings = appendTPSLWarning(warnings, tpslWarningNetRRBelowTarget)
	}
	if netTP <= 0 && in.Qty > 0 {
		warnings = appendTPSLWarning(warnings, tpslWarningNetTPNonPositive)
	}
	if netSL <= 0 && in.Qty > 0 {
		warnings = appendTPSLWarning(warnings, tpslWarningNetSLNonPositive)
	}
	if !tpCorrect {
		warnings = appendTPSLWarning(warnings, tpslWarningWrongSideTP)
	}
	if !slCorrect {
		warnings = appendTPSLWarning(warnings, tpslWarningWrongSideSL)
	}
	if targetRR > 1 && finalTPDist <= finalSLDist+eps {
		warnings = appendTPSLWarning(warnings, tpslWarningTPDistanceLESLDistance)
	}
	roundingWorsened := rawRR > 0 && finalRR+eps < rawRR
	if roundingWorsened {
		warnings = appendTPSLWarning(warnings, tpslWarningRoundingWorsenedRR)
	}
	if tick > 0 && (math.Abs(finalTP-rawTP) > tick+eps || math.Abs(finalSL-rawSL) > tick+eps) {
		warnings = appendTPSLWarning(warnings, tpslWarningFinalDiffGTOneTick)
	}
	if side == "LONG" && finalSL >= in.Entry && !breakevenSafe {
		warnings = appendTPSLWarning(warnings, tpslWarningNegativeBreakeven)
	}
	if side == "SHORT" && finalSL <= in.Entry && !breakevenSafe {
		warnings = appendTPSLWarning(warnings, tpslWarningNegativeBreakeven)
	}
	if in.DynamicTPUsed && !in.DynamicTPEnabled {
		warnings = appendTPSLWarning(warnings, tpslWarningDynamicTPDisabledButUsed)
	}
	if in.TargetRRConfigPresent && !in.TargetRRUsed {
		warnings = appendTPSLWarning(warnings, tpslWarningConfigUnusedTargetRR)
	}
	if in.ATRCapConfigPresent && !in.ATRCapUsed {
		warnings = appendTPSLWarning(warnings, tpslWarningConfigUnusedATRCap)
	}
	if in.MinProfitPerc > 0 && !in.MinProfitUsed {
		warnings = appendTPSLWarning(warnings, tpslWarningConfigUnusedMinProfit)
	}
	if in.TPFeeFloorMult > 0 && !in.TPFeeFloorUsed {
		warnings = appendTPSLWarning(warnings, tpslWarningConfigUnusedTPFeeFloor)
	}
	if in.SLFeeFloorMult > 0 && !in.SLFeeFloorUsed {
		warnings = appendTPSLWarning(warnings, tpslWarningConfigUnusedSLFeeFloor)
	}

	formattedTP := in.FormattedTP
	if formattedTP == "" {
		formattedTP = fmt.Sprintf("%.2f", finalTP)
	}
	formattedSL := in.FormattedSL
	if formattedSL == "" {
		formattedSL = fmt.Sprintf("%.2f", finalSL)
	}
	mode := in.TPSLMode
	if mode == "" {
		mode = "Full"
	}
	triggerBy := in.TriggerBy
	if triggerBy == "" {
		triggerBy = "exchange_default"
	}
	assumption := in.MakerTakerAssumption
	if assumption == "" {
		assumption = "round_trip_fee_split_50_50"
	}

	return TPSLDiagnostics{
		Symbol:                   in.Symbol,
		Side:                     side,
		Reason:                   in.Reason,
		SignalID:                 in.SignalID,
		CandleSeq:                in.CandleSeq,
		Timestamp:                in.Timestamp,
		Qty:                      in.Qty,
		Entry:                    in.Entry,
		RawTP:                    rawTP,
		RawSL:                    rawSL,
		RoundedTP:                roundedTP,
		RoundedSL:                roundedSL,
		FinalTP:                  finalTP,
		FinalSL:                  finalSL,
		RawTPDistAbs:             rawTPDist,
		RawSLDistAbs:             rawSLDist,
		TPDistAbs:                finalTPDist,
		SLDistAbs:                finalSLDist,
		TPDistPct:                tpslPct(finalTPDist, in.Entry),
		SLDistPct:                tpslPct(finalSLDist, in.Entry),
		RawRR:                    rawRR,
		FinalRR:                  finalRR,
		TargetRR:                 targetRR,
		TargetRRSource:           in.TargetRRSource,
		TargetRRUsed:             in.TargetRRUsed,
		GrossTPPnL:               grossTP,
		GrossSLLoss:              grossSL,
		FeeRate:                  feeRate,
		MakerTakerAssumption:     assumption,
		EstimatedOpenFee:         openFee,
		EstimatedTPCloseFee:      tpCloseFee,
		EstimatedSLCloseFee:      slCloseFee,
		EstimatedRoundTripFeeTP:  roundTripFeeTP,
		EstimatedRoundTripFeeSL:  roundTripFeeSL,
		NetTPPnL:                 netTP,
		NetSLLoss:                netSL,
		NetRR:                    netRR,
		FeeAdjustedBreakeven:     feeAdjustedBreakeven,
		EstimatedLossAtCurrentSL: estimatedLossAtSL,
		BreakevenSafe:            breakevenSafe,
		ATR:                      in.ATR,
		ATRPeriod:                in.ATRPeriod,
		MarketRegime:             in.MarketRegime,
		DynamicTPEnabled:         in.DynamicTPEnabled,
		DynamicTPUsed:            in.DynamicTPUsed,
		DynamicTPRawPercent:      in.DynamicTPRawPercent,
		DynamicTPClampedPercent:  in.DynamicTPClampedPercent,
		DynamicTPMinPercent:      in.DynamicTPMinPercent,
		DynamicTPMaxPercent:      in.DynamicTPMaxPercent,
		DynamicTPK:               in.DynamicTPK,
		DynamicTPVolatility:      in.DynamicTPVolatility,
		DynamicTPRegimeFactor:    in.DynamicTPRegimeFactor,
		TickSize:                 tick,
		TPRoundingDirection:      in.TPRoundingDirection,
		SLRoundingDirection:      in.SLRoundingDirection,
		RoundingMode:             in.RoundingMode,
		RoundingWorsenedRR:       roundingWorsened,
		MinPocketDistance:        in.MinPocketDistance,
		MinProfitPerc:            in.MinProfitPerc,
		TPFeeFloorMult:           in.TPFeeFloorMult,
		SLFeeFloorMult:           in.SLFeeFloorMult,
		MinRequiredTPDist:        in.MinRequiredTPDist,
		MinRequiredSLDist:        in.MinRequiredSLDist,
		TPDistanceIncreased:      in.TPDistanceIncreased,
		SLDistanceIncreased:      in.SLDistanceIncreased,
		MinProfitUsed:            in.MinProfitUsed,
		TPFeeFloorUsed:           in.TPFeeFloorUsed,
		SLFeeFloorUsed:           in.SLFeeFloorUsed,
		MinDistanceOk:            minDistanceOk,
		FinalTPDistSource:        in.FinalTPDistSource,
		TPOnCorrectSide:          tpCorrect,
		SLOnCorrectSide:          slCorrect,
		Valid:                    tpCorrect && slCorrect && minDistanceOk,
		NetRROk:                  netRR >= targetRR,
		Warnings:                 warnings,
		PositionIdx:              in.PositionIdx,
		TPSLMode:                 mode,
		TriggerBy:                triggerBy,
		FormattedTP:              formattedTP,
		FormattedSL:              formattedSL,
	}
}

func (t *Trader) logTPSLDiagnostics(d TPSLDiagnostics) {
	if t == nil || t.Logger == nil {
		return
	}
	ts := d.Timestamp
	if ts.IsZero() {
		ts = t.now()
	}
	t.Logger.Info("[TP_SL_INPUT] timestamp=%s symbol=%s side=%s qty=%.8f entry=%.8f marketRegime=%s atr=%.8f atrPeriod=%d candleSeq=%d signalId=%s reason=%q",
		ts.Format(time.RFC3339Nano), d.Symbol, d.Side, d.Qty, d.Entry, strings.ToUpper(d.MarketRegime), d.ATR, d.ATRPeriod, d.CandleSeq, d.SignalID, d.Reason)
	t.Logger.Info("[TP_SL_CALC] side=%s entry=%.8f rawTP=%.8f rawSL=%.8f finalTP=%.8f finalSL=%.8f tpDistAbs=%.8f slDistAbs=%.8f tpDistPct=%.6f slDistPct=%.6f rawRR=%.6f finalRR=%.6f targetRR=%.6f targetRRSource=%s targetRRUsed=%t finalTPDistSource=%s",
		d.Side, d.Entry, d.RawTP, d.RawSL, d.FinalTP, d.FinalSL, d.TPDistAbs, d.SLDistAbs, d.TPDistPct, d.SLDistPct, d.RawRR, d.FinalRR, d.TargetRR, d.TargetRRSource, d.TargetRRUsed, d.FinalTPDistSource)
	t.Logger.Info("[TP_SL_RR] side=%s rawRR=%.6f finalRR=%.6f targetRR=%.6f targetRRSource=%s targetRRUsed=%t tpDistAbs=%.8f slDistAbs=%.8f",
		d.Side, d.RawRR, d.FinalRR, d.TargetRR, d.TargetRRSource, d.TargetRRUsed, d.TPDistAbs, d.SLDistAbs)
	t.Logger.Info("[TP_SL_NET_RR] side=%s qty=%.8f feeRate=%.8f feeAssumption=%s grossTP=%.8f grossSL=%.8f estimatedOpenFee=%.8f estimatedCloseFeeTP=%.8f estimatedCloseFeeSL=%.8f estimatedRoundTripFeeTP=%.8f estimatedRoundTripFeeSL=%.8f netTP=%.8f netSL=%.8f netRR=%.6f targetRR=%.6f",
		d.Side, d.Qty, d.FeeRate, d.MakerTakerAssumption, d.GrossTPPnL, d.GrossSLLoss, d.EstimatedOpenFee, d.EstimatedTPCloseFee, d.EstimatedSLCloseFee, d.EstimatedRoundTripFeeTP, d.EstimatedRoundTripFeeSL, d.NetTPPnL, d.NetSLLoss, d.NetRR, d.TargetRR)
	t.Logger.Info("[TP_SL_ROUNDING] side=%s tickSize=%.8f rawTP=%.8f rawSL=%.8f roundedTP=%.8f roundedSL=%.8f finalTP=%.8f finalSL=%.8f tpRoundingDirection=%s slRoundingDirection=%s roundingMode=%s roundingWorsenedRR=%t",
		d.Side, d.TickSize, d.RawTP, d.RawSL, d.RoundedTP, d.RoundedSL, d.FinalTP, d.FinalSL, d.TPRoundingDirection, d.SLRoundingDirection, d.RoundingMode, d.RoundingWorsenedRR)
	t.Logger.Info("[TP_SL_VALIDATE] side=%s valid=%t tpOnCorrectSide=%t slOnCorrectSide=%t minDistanceOk=%t netRROk=%t breakevenSafe=%t feeAdjustedBreakeven=%.8f currentSL=%.8f estimatedLossAtCurrentSL=%.8f warnings=%v",
		d.Side, d.Valid, d.TPOnCorrectSide, d.SLOnCorrectSide, d.MinDistanceOk, d.NetRROk, d.BreakevenSafe, d.FeeAdjustedBreakeven, d.FinalSL, d.EstimatedLossAtCurrentSL, d.Warnings)
	t.Logger.Info("[TP_SL_CALC] side=%s atr=%.8f atrPeriod=%d dynamicTPEnabled=%t dynamicTPUsed=%t dynamicTPRawPercent=%.8f dynamicTPClampedPercent=%.8f minTPPercent=%.8f maxTPPercent=%.8f dynamicTPK=%.8f volatilityFactor=%.8f regimeFactor=%.8f minPocketDistance=%.8f minProfitPerc=%.8f minProfitUsed=%t tpFeeFloorMult=%.8f tpFeeFloorUsed=%t slFeeFloorMult=%.8f slFeeFloorUsed=%t minRequiredTPDist=%.8f minRequiredSLDist=%.8f tpDistanceIncreased=%t slDistanceIncreased=%t",
		d.Side, d.ATR, d.ATRPeriod, d.DynamicTPEnabled, d.DynamicTPUsed, d.DynamicTPRawPercent, d.DynamicTPClampedPercent, d.DynamicTPMinPercent, d.DynamicTPMaxPercent, d.DynamicTPK, d.DynamicTPVolatility, d.DynamicTPRegimeFactor, d.MinPocketDistance, d.MinProfitPerc, d.MinProfitUsed, d.TPFeeFloorMult, d.TPFeeFloorUsed, d.SLFeeFloorMult, d.SLFeeFloorUsed, d.MinRequiredTPDist, d.MinRequiredSLDist, d.TPDistanceIncreased, d.SLDistanceIncreased)
	for _, warning := range d.Warnings {
		t.Logger.Warning("[TP_SL_WARNING] type=%s symbol=%s side=%s qty=%.8f entry=%.8f rawRR=%.6f finalRR=%.6f netRR=%.6f targetRR=%.6f tp=%.8f sl=%.8f feeAdjustedBreakeven=%.8f reason=%q",
			warning, d.Symbol, d.Side, d.Qty, d.Entry, d.RawRR, d.FinalRR, d.NetRR, d.TargetRR, d.FinalTP, d.FinalSL, d.FeeAdjustedBreakeven, d.Reason)
	}
}

func (t *Trader) logTPSLSend(d TPSLDiagnostics) {
	if t == nil || t.Logger == nil {
		return
	}
	ts := d.Timestamp
	if ts.IsZero() {
		ts = t.now()
	}
	t.Logger.Info("[TP_SL_SEND] timestamp=%s symbol=%s side=%s qty=%.8f entry=%.8f finalTP=%.8f finalSL=%.8f formattedTP=%s formattedSL=%s positionIdx=%d tpslMode=%s triggerBy=%s signalId=%s candleSeq=%d reason=%q",
		ts.Format(time.RFC3339Nano), d.Symbol, d.Side, d.Qty, d.Entry, d.FinalTP, d.FinalSL, d.FormattedTP, d.FormattedSL, d.PositionIdx, d.TPSLMode, d.TriggerBy, d.SignalID, d.CandleSeq, d.Reason)
}

func (t *Trader) logTPSLResult(d TPSLDiagnostics, err error) {
	if t == nil || t.Logger == nil {
		return
	}
	ts := d.Timestamp
	if ts.IsZero() {
		ts = t.now()
	}
	if err != nil {
		t.Logger.Error("[TP_SL_RESULT] timestamp=%s success=false symbol=%s side=%s finalTP=%.8f finalSL=%.8f err=%q signalId=%s candleSeq=%d reason=%q",
			ts.Format(time.RFC3339Nano), d.Symbol, d.Side, d.FinalTP, d.FinalSL, err.Error(), d.SignalID, d.CandleSeq, d.Reason)
		return
	}
	t.Logger.Info("[TP_SL_RESULT] timestamp=%s success=true symbol=%s side=%s finalTP=%.8f finalSL=%.8f signalId=%s candleSeq=%d reason=%q",
		ts.Format(time.RFC3339Nano), d.Symbol, d.Side, d.FinalTP, d.FinalSL, d.SignalID, d.CandleSeq, d.Reason)
}

// NewTrader creates a new trader instance
func NewTrader(apiClient *api.RESTClient, cfg *config.Config, state *models.State, logger logging.LoggerInterface) *Trader {
	orderManager := order.NewOrderManager(apiClient, cfg, state, logger)
	positionManager := position.NewPositionManager(apiClient, cfg, state, logger)

	return &Trader{
		APIClient:            apiClient,
		Config:               cfg,
		State:                state,
		OrderManager:         orderManager,
		PositionManager:      positionManager,
		Logger:               logger,
		ReversalLongDetector: NewReversalLongDetector(cfg.ReversalLong),
	}
}

func (t *Trader) adaptiveDeadband(atr, price, base float64) float64 {
	if base == 0 {
		base = price * 0.001
	}
	if atr > 0 {
		return math.Max(base, atr*t.Config.AtrDeadbandMult)
	}
	return base
}

func (t *Trader) now() time.Time {
	if t != nil && t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// feeBuffer returns the minimum price move (in price units) needed to cover a round trip with fees and slippage padding
func (t *Trader) feeBuffer(entry float64) float64 {
	bufPerc := t.Config.RoundTripFeePerc
	if bufPerc <= 0 {
		bufPerc = 0.0012
	}
	mult := t.Config.FeeBufferMult
	if mult <= 0 {
		mult = 1.0
	}
	buf := entry * bufPerc * mult
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	if buf > 0 {
		buf = math.Ceil(buf/tick) * tick
	}
	return buf
}

func (t *Trader) higherTimeframeBias(atr float64) string {
	snapshot := t.State.GetOhlcvSnapshot()
	return t.higherTimeframeBiasForCloses(atr, snapshot.Closes)
}

func (t *Trader) higherTimeframeBiasForCloses(atr float64, closes []float64) string {
	win := t.Config.HTFWindow
	if len(closes) < win || win == 0 {
		return ""
	}
	window := closes[len(closes)-win:]
	short := window
	if len(short) > t.Config.HTFMaLen && t.Config.HTFMaLen > 0 {
		short = short[len(short)-t.Config.HTFMaLen:]
	}
	maLong := indicators.SMA(window)
	maShort := indicators.SMA(short)
	slope := window[len(window)-1] - window[0]
	bias := ""
	atrGate := t.adaptiveDeadband(atr, window[len(window)-1], 0)
	if maShort > maLong && slope > atrGate {
		bias = "LONG"
	} else if maShort < maLong && slope < -atrGate {
		bias = "SHORT"
	}
	return bias
}

// TPWorker processes take profit jobs
func (t *Trader) TPWorker() {
	t.Logger.Info("Starting TP worker to process take profit jobs...")

	for job := range t.State.TPChan {
		t.Logger.Info("Received TP job: Side=%s, Qty=%.4f, EntryPrice=%.2f", job.Side, job.Qty, job.EntryPrice)

		exists, side, qty, _, _ := t.PositionManager.HasOpenPosition()
		if !exists || t.PositionManager.NormalizeSide(job.Side) != t.PositionManager.NormalizeSide(side) || job.Qty != qty {
			t.Logger.Info("Position mismatch, skipping TP job")
			continue
		}

		// Calculate TP based on 15-minute price projection
		t.Logger.Debug("Calculating TP based on 15-minute price projection...")
		entryPrice := job.EntryPrice
		if entryPrice == 0 {
			entryPrice = t.PositionManager.GetLastEntryPrice()
		}
		if entryPrice == 0 {
			t.Logger.Error("Cannot find entry price - skipping TP")
			continue
		}

		// Use our new 15-minute projection method
		finalTP := t.calculateTPBasedOn15MinProjection(entryPrice, job.Side)
		t.Logger.Info("TP calculated using 15-minute projection: %.2f", finalTP)

		if finalTP == 0 || math.IsNaN(finalTP) {
			entryPrice := job.EntryPrice
			if entryPrice == 0 {
				entryPrice = t.PositionManager.GetLastEntryPrice()
			}
			if entryPrice == 0 {
				t.Logger.Error("Cannot find entry price - skipping TP")
				continue
			}
			// Ensure TickSize is valid before using it
			tickSize := t.State.Instr.TickSize
			if tickSize <= 0 {
				tickSize = 0.1 // Default fallback
			}
			if job.Side == "LONG" {
				finalTP = math.Round(entryPrice*(1+t.Config.TpOffset)/tickSize) * tickSize
			} else {
				finalTP = math.Round(entryPrice*(1-t.Config.TpOffset)/tickSize) * tickSize
			}
			t.Logger.Info("Using fallback TP calculation: %.2f", finalTP)
		}

		// Calculate TP/SL with 2:1 ratio
		entryPrice = job.EntryPrice
		if entryPrice == 0 {
			entryPrice = t.PositionManager.GetLastEntryPrice()
		}
		if entryPrice == 0 {
			entryPrice = finalTP // Fallback if we can't get entry price
		}

		// Apply 2:1 ratio between TP and SL based on the unified TP/SL calculation.
		finalTP, _, diag := t.calculateInitialTPSLWithDiagnostics(entryPrice, job.Side, tpslDiagnosticContext{
			Symbol: t.Config.Symbol,
			Reason: "tp_worker_take_profit_order",
			Qty:    job.Qty,
			Log:    true,
		})

		orderSide := "Sell"
		if job.Side == "SHORT" {
			orderSide = "Buy"
		}

		t.Logger.Info("[TP_SL_SEND] timestamp=%s action=take_profit_order symbol=%s side=%s orderSide=%s qty=%.8f entry=%.8f finalTP=%.8f finalSL=%.8f formattedTP=%s formattedSL=%s reason=%q",
			diag.Timestamp.Format(time.RFC3339Nano), diag.Symbol, diag.Side, orderSide, job.Qty, entryPrice, finalTP, diag.FinalSL, diag.FormattedTP, "not_sent", diag.Reason)
		t.Logger.Info("Placing take profit order: %s, Qty=%.4f, Price=%.2f", orderSide, job.Qty, finalTP)
		if err := t.OrderManager.PlaceTakeProfitOrder(orderSide, job.Qty, finalTP); err != nil {
			t.Logger.Error("[TP_SL_RESULT] timestamp=%s action=take_profit_order success=false symbol=%s side=%s finalTP=%.8f err=%q reason=%q",
				t.now().Format(time.RFC3339Nano), diag.Symbol, diag.Side, finalTP, err.Error(), diag.Reason)
			t.Logger.Error("Error placing TP: %v", err)
		} else {
			t.Logger.Info("[TP_SL_RESULT] timestamp=%s action=take_profit_order success=true symbol=%s side=%s finalTP=%.8f reason=%q",
				t.now().Format(time.RFC3339Nano), diag.Symbol, diag.Side, finalTP, diag.Reason)
			t.Logger.Info("Successfully placed take profit order")
		}
	}
}

type orderbookLevel struct {
	price float64
	size  float64
}

type orderbookSnapshot struct {
	bids []orderbookLevel
	asks []orderbookLevel
}

func (t *Trader) readOrderbookSnapshot() orderbookSnapshot {
	t.State.ObLock.Lock()
	bidsCopy := make(map[string]float64, len(t.State.BidsMap))
	asksCopy := make(map[string]float64, len(t.State.AsksMap))
	for k, v := range t.State.BidsMap {
		bidsCopy[k] = v
	}
	for k, v := range t.State.AsksMap {
		asksCopy[k] = v
	}
	t.State.ObLock.Unlock()

	var bids, asks []orderbookLevel
	for ps, sz := range bidsCopy {
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil {
			continue
		}
		bids = append(bids, orderbookLevel{price: p, size: sz})
	}
	for ps, sz := range asksCopy {
		p, err := strconv.ParseFloat(ps, 64)
		if err != nil {
			continue
		}
		asks = append(asks, orderbookLevel{price: p, size: sz})
	}

	sort.Slice(bids, func(i, j int) bool { return bids[i].price > bids[j].price })
	sort.Slice(asks, func(i, j int) bool { return asks[i].price < asks[j].price })

	return orderbookSnapshot{bids: bids, asks: asks}
}

func sumOrderbookTop(src []orderbookLevel, take int) float64 {
	lim := take
	if len(src) < take {
		lim = len(src)
	}
	acc := 0.0
	for i := 0; i < lim; i++ {
		acc += src[i].size
	}
	return acc
}

func sumOrderbookAll(src []orderbookLevel) float64 {
	acc := 0.0
	for _, lv := range src {
		acc += lv.size
	}
	return acc
}

func medianFloat64(arr []float64) float64 {
	if len(arr) == 0 {
		return 0
	}
	tmp := append([]float64(nil), arr...)
	sort.Float64s(tmp)
	return tmp[len(tmp)/2]
}

func trimOrderbookHistory(history []float64, lookback int) []float64 {
	if len(history) > lookback && lookback > 0 {
		return history[len(history)-lookback:]
	}
	return history
}

func normalizeOrderbookSide(side string) string {
	switch strings.ToUpper(side) {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}

func orderbookHistoryName(side string) string {
	switch side {
	case "LONG":
		return "long"
	case "SHORT":
		return "short"
	default:
		return ""
	}
}

func (t *Trader) orderbookHistoryCopy(side string) []float64 {
	t.State.RLock()
	defer t.State.RUnlock()

	switch side {
	case "LONG":
		return append([]float64(nil), t.State.ObImbalanceHistoryLong...)
	case "SHORT":
		return append([]float64(nil), t.State.ObImbalanceHistoryShort...)
	default:
		return nil
	}
}

func (t *Trader) recordOrderbookImbalance(side string, ratio float64) {
	t.State.Lock()
	switch side {
	case "LONG":
		t.State.ObImbalanceHistoryLong = append(t.State.ObImbalanceHistoryLong, ratio)
		t.State.ObImbalanceHistoryLong = trimOrderbookHistory(t.State.ObImbalanceHistoryLong, t.Config.OrderbookStabilityLookback)
	case "SHORT":
		t.State.ObImbalanceHistoryShort = append(t.State.ObImbalanceHistoryShort, ratio)
		t.State.ObImbalanceHistoryShort = trimOrderbookHistory(t.State.ObImbalanceHistoryShort, t.Config.OrderbookStabilityLookback)
	}
	t.State.Unlock()
}

// EvaluateOrderbookStrength evaluates the current orderbook once and records imbalance history in one place.
func (t *Trader) EvaluateOrderbookStrength(side string, lastPrice float64) models.OrderbookResult {
	side = normalizeOrderbookSide(side)
	snapshot := t.readOrderbookSnapshot()
	ohlcvSnapshot := t.State.GetOhlcvSnapshot()

	historyCopy := t.orderbookHistoryCopy(side)

	result, recordHistory := t.evaluateOrderbookStrength(side, lastPrice, snapshot, historyCopy, ohlcvSnapshot.Closes, ohlcvSnapshot.Volumes)
	if recordHistory {
		t.recordOrderbookImbalance(side, result.Ratio)
	}
	return result
}

// evaluateOrderbookStrength is pure with respect to shared state; callers pass snapshots and handle history mutation.
func (t *Trader) evaluateOrderbookStrength(side string, lastPrice float64, snapshot orderbookSnapshot, historyCopy, closesCopy, volumesCopy []float64) (models.OrderbookResult, bool) {
	result := models.OrderbookResult{
		Evaluated: true,
		Side:      side,
		Threshold: t.Config.OrderbookStrengthThreshold,
		History:   orderbookHistoryName(side),
	}

	if side == "" {
		result.Reason = "invalid side"
		return result, false
	}

	if len(snapshot.bids) == 0 || len(snapshot.asks) == 0 {
		result.Reason = "orderbook empty"
		return result, false
	}

	levels := t.Config.OrderbookLevels
	if levels <= 0 {
		levels = 3
	}

	topBidDepth := sumOrderbookTop(snapshot.bids, levels)
	topAskDepth := sumOrderbookTop(snapshot.asks, levels)
	totalDepth := sumOrderbookAll(snapshot.bids) + sumOrderbookAll(snapshot.asks)

	if totalDepth < t.Config.OrderbookMinDepth {
		result.Reason = fmt.Sprintf("depth low (%.2f < %.2f)", totalDepth, t.Config.OrderbookMinDepth)
		return result, false
	}

	// Ratio is sideDepth/oppositeDepth: LONG uses bid/ask, SHORT uses ask/bid.
	// 1.0 is balanced depth; values above 1.0 mean the requested side dominates.
	var ratio float64
	if side == "LONG" && topAskDepth > 0 {
		ratio = topBidDepth / topAskDepth
	} else if side == "SHORT" && topBidDepth > 0 {
		ratio = topAskDepth / topBidDepth
	}
	result.Ratio = ratio
	if topBidDepth+topAskDepth > 0 {
		result.Imbalance = (topBidDepth - topAskDepth) / (topBidDepth + topAskDepth)
	}

	// When no explicit strength threshold is set, skip advanced stability gating to let signals pass
	if t.Config.OrderbookStrengthThreshold == 0 {
		if ratio == 0 {
			result.Reason = "orderbook ratio zero"
			return result, false
		}
		result.Passed = true
		result.Reason = fmt.Sprintf("ratio %.2f depth %.2f", ratio, totalDepth)
		return result, false
	}

	historyWithCurrent := append(historyCopy, ratio)
	historyWithCurrent = trimOrderbookHistory(historyWithCurrent, t.Config.OrderbookStabilityLookback)

	stable := true
	stabilityRange := t.Config.OrderbookStabilityRange
	if stabilityRange <= 0 {
		stabilityRange = 0.6
	}
	if len(historyWithCurrent) >= 3 {
		minR, maxR := historyWithCurrent[0], historyWithCurrent[0]
		for _, v := range historyWithCurrent[1:] {
			if v < minR {
				minR = v
			}
			if v > maxR {
				maxR = v
			}
		}
		if maxR-minR > stabilityRange {
			stable = false
		}
	}

	medianRatio := medianFloat64(historyWithCurrent)
	medianMult := t.Config.OrderbookMedianRatioMult
	if medianMult <= 0 {
		medianMult = 0.5
	}
	if medianRatio > 0 && ratio < medianRatio*medianMult {
		result.Reason = fmt.Sprintf("ratio %.2f below median %.2f", ratio, medianRatio)
		return result, true
	}

	// Correlate imbalance with recent price drift
	priceDrift := 0.0
	if ln := len(closesCopy); ln >= 2 {
		priceDrift = closesCopy[ln-1] - closesCopy[ln-2]
	}

	// Volume confirmation: require last volume spike relative to median if configured
	volOk := true
	volReason := ""
	lastVol := 0.0
	medVol := 0.0
	if len(volumesCopy) > 5 && t.Config.VolumeSpikeMult > 0 {
		medVol = medianFloat64(volumesCopy[len(volumesCopy)-5:])
		lastVol = volumesCopy[len(volumesCopy)-1]
		if medVol > 0 && lastVol < medVol*t.Config.VolumeSpikeMult {
			volOk = false
			volReason = fmt.Sprintf("volume %.2f below spike %.2f", lastVol, medVol*t.Config.VolumeSpikeMult)
		} else if t.Config.MinVolume > 0 && lastVol < t.Config.MinVolume {
			volOk = false
			volReason = fmt.Sprintf("volume %.2f below minimum %.2f", lastVol, t.Config.MinVolume)
		}
	}
	if !volOk {
		if t.Config.Debug {
			t.Logger.Debug("Orderbook volume check: ok=false reason=%s lastVol=%.2f medVol=%.2f min=%.2f", volReason, lastVol, medVol, t.Config.MinVolume)
		}
		result.Reason = volReason
		return result, true
	}
	if t.Config.Debug && t.Config.VolumeSpikeMult > 0 && len(volumesCopy) > 5 {
		t.Logger.Debug("Orderbook volume check: ok=true lastVol=%.2f medVol=%.2f spike=%.2f min=%.2f",
			lastVol, medVol, medVol*t.Config.VolumeSpikeMult, t.Config.MinVolume)
	}

	threshold := t.Config.OrderbookStrengthThreshold
	if ratio == 0 {
		result.Reason = "orderbook ratio zero"
		return result, true
	}
	if ratio < threshold {
		result.Reason = fmt.Sprintf("ratio %.2f below %.2f", ratio, threshold)
		return result, true
	}
	if !stable {
		result.Reason = fmt.Sprintf("ratio unstable %.2f", ratio)
		return result, true
	}
	if side == "LONG" && priceDrift < 0 && lastPrice != 0 {
		result.Reason = "ratio bullish but price drifting down"
		return result, true
	}
	if side == "SHORT" && priceDrift > 0 && lastPrice != 0 {
		result.Reason = "ratio bearish but price drifting up"
		return result, true
	}

	result.Passed = true
	result.Reason = fmt.Sprintf("ratio %.2f depth %.2f", ratio, totalDepth)
	return result, true
}

// CheckOrderbookStrength inspects multi-level orderbook imbalance and stability.
func (t *Trader) CheckOrderbookStrength(side string, lastPrice float64) (bool, string) {
	result := t.EvaluateOrderbookStrength(side, lastPrice)
	return result.Passed, result.Reason
}

func cloneOrderbookResult(result *models.OrderbookResult) *models.OrderbookResult {
	if result == nil {
		return nil
	}
	copy := *result
	return &copy
}

func orderbookResultLog(result *models.OrderbookResult) string {
	if result == nil {
		return "not_evaluated"
	}
	return fmt.Sprintf("passed=%t side=%s history=%s ratio=%.2f imbalance=%.4f threshold=%.2f reason=%s",
		result.Passed, result.Side, result.History, result.Ratio, result.Imbalance, result.Threshold, result.Reason)
}

// DetectMarketRegime detects market regime (trend vs range)
func (t *Trader) DetectMarketRegime() {
	minLen := 50
	if t.Config.HTFWindow > minLen {
		minLen = t.Config.HTFWindow
	}
	snapshot := t.State.GetOhlcvSnapshot()
	closes := snapshot.Closes
	if len(closes) < minLen {
		return
	}

	recent := closes[len(closes)-50:]
	t.Logger.Debug("Detecting market regime with %d recent closes", len(recent))

	// Calculate multiple metrics for better regime detection
	rangePerc := calculateRangePercentage(recent)
	t.Logger.Debug("Range percentage calculated: %.2f", rangePerc)

	volatilityRegime := detectVolatilityRegime(closes)
	t.Logger.Debug("Volatility regime detected: %s", volatilityRegime)

	trendStrength := calculateTrendStrength(recent)
	t.Logger.Debug("Trend strength calculated: %.2f", trendStrength)

	t.Logger.Info("Market regime metrics - RangePerc: %.2f, VolatilityRegime: %s, TrendStrength: %.2f",
		rangePerc, volatilityRegime, trendStrength)

	candidate := "range"
	if trendStrength > 0.55 && rangePerc > 2.0 {
		candidate = "trend"
	} else if volatilityRegime == "low" && rangePerc < 1.5 {
		candidate = "range"
	}
	regimeUpdate := t.State.UpdateMarketRegime(candidate, t.Config.RegimePersistence)
	if regimeUpdate.CandidateReset {
		t.Logger.Info("Regime vote reset to %s", candidate)
	}

	if regimeUpdate.MarketRegimeChanged {
		t.Logger.Info("Market regime persisted for %d votes → %s", regimeUpdate.RegimeStreak, candidate)
	}
}

// calculateRangePercentage calculates the percentage range of price movement
func calculateRangePercentage(closes []float64) float64 {
	if len(closes) == 0 {
		return 0
	}

	maxHigh := indicators.MaxSlice(closes)
	minLow := indicators.MinSlice(closes)

	if minLow <= 0 {
		return 0
	}

	return (maxHigh - minLow) / minLow * 100
}

// detectVolatilityRegime determines if the market has high or low volatility
func detectVolatilityRegime(closes []float64) string {
	if len(closes) < 50 {
		return "unknown"
	}

	// Use the most recent 20 closes to calculate recent volatility
	recent := closes[len(closes)-20:]

	// Calculate standard deviation as a volatility measure
	mean := 0.0
	for _, c := range recent {
		mean += c
	}
	mean /= float64(len(recent))

	var variance float64
	for _, c := range recent {
		variance += (c - mean) * (c - mean)
	}
	variance /= float64(len(recent))
	stdDev := math.Sqrt(variance)

	// Calculate coefficient of variation
	if mean != 0 {
		cv := stdDev / math.Abs(mean) * 100

		if cv > 5.0 { // High threshold for high volatility in crypto
			return "high"
		} else {
			return "low"
		}
	}

	return "unknown"
}

// calculateTrendStrength measures the strength of the current trend
func calculateTrendStrength(closes []float64) float64 {
	if len(closes) < 2 {
		return 0
	}

	totalChange := math.Abs(closes[len(closes)-1] - closes[0])
	totalDistance := 0.0

	// Sum of absolute changes between each candle to see total movement vs net trend
	for i := 1; i < len(closes); i++ {
		totalDistance += math.Abs(closes[i] - closes[i-1])
	}

	if totalDistance == 0 {
		return 0
	}

	// A strong trend would have totalChange close to totalDistance
	// A ranging market would have totalDistance much larger than totalChange
	trendRatio := totalChange / totalDistance

	// Normalize to 0-1 scale
	return math.Min(trendRatio*2, 1.0) // Multiply by 2 to increase sensitivity
}

// HandleLongSignal processes a long signal
func (t *Trader) HandleLongSignal(closePrice float64) bool {
	return t.handleLongSignal(models.Signal{Kind: "LONG", Direction: "LONG", ClosePrice: closePrice})
}

func (t *Trader) handleLongSignal(sig models.Signal) bool {
	exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
	side = t.PositionManager.NormalizeSide(side)
	newSide := t.PositionManager.NormalizeSide("LONG")

	if !exists {
		if t.Config.Debug {
			t.Logger.Debug("openPosition called: side=%s reason=no_existing_position", newSide)
		}
		return t.openPositionWithSignal(newSide, sig.ClosePrice, sig)
	} else if side == newSide {
		t.adjustTPSL(sig.ClosePrice)
		return false
	} else {
		t.Logger.Warning("LONG signal sees opposite position %s; reversal must be handled by Trader before HandleLongSignal", side)
		return false
	}
}

// HandleShortSignal processes a short signal
func (t *Trader) HandleShortSignal(closePrice float64) bool {
	return t.handleShortSignal(models.Signal{Kind: "SHORT", Direction: "SHORT", ClosePrice: closePrice})
}

func (t *Trader) handleShortSignal(sig models.Signal) bool {
	exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
	side = t.PositionManager.NormalizeSide(side)
	newSide := t.PositionManager.NormalizeSide("SHORT")

	if !exists {
		if t.Config.Debug {
			t.Logger.Debug("openPosition called: side=%s reason=no_existing_position", newSide)
		}
		return t.openPositionWithSignal(newSide, sig.ClosePrice, sig)
	} else if side == newSide {
		t.adjustTPSLForShort(sig.ClosePrice)
		return false
	} else {
		t.Logger.Warning("SHORT signal sees opposite position %s; reversal must be handled by Trader before HandleShortSignal", side)
		return false
	}
}

// roundDownToStep avoids exceeding the target notional while keeping step alignment.
func roundDownToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Floor(qty/step+1e-9) * step
}

// roundUpToStep enforces exchange minimums while keeping step alignment.
func roundUpToStep(qty, step float64) float64 {
	if step <= 0 {
		return qty
	}
	return math.Ceil(qty/step-1e-9) * step
}

type positionSizeDecision struct {
	NormalQty              float64
	FinalQty               float64
	MinRequiredQty         float64
	Multiplier             float64
	RiskProfileApplied     bool
	InvalidMultiplier      bool
	MultiplierClampedToMin bool
}

func (t *Trader) calculatePositionSize(price, balance float64, sig models.Signal) (positionSizeDecision, error) {
	step := t.State.Instr.QtyStep
	if step <= 0 {
		step = t.State.Instr.MinQty
	}
	if step <= 0 {
		return positionSizeDecision{}, fmt.Errorf("invalid instrument step size: %.8f", step)
	}

	minQty := math.Max(t.State.Instr.MinQty, step)
	minRequired := minQty
	if price > 0 && t.State.Instr.MinNotional > 0 {
		minNotionalQty := t.State.Instr.MinNotional / price
		if minNotionalQty > minRequired {
			minRequired = minNotionalQty
		}
	}
	minRequired = roundUpToStep(minRequired, step)

	normalQty := minRequired
	if t.Config.OrderBalancePct > 0 && price > 0 {
		targetQty := (balance * t.Config.OrderBalancePct) / price
		normalQty = roundDownToStep(targetQty, step)
		if normalQty < minRequired {
			normalQty = minRequired
		}
	}

	decision := positionSizeDecision{
		NormalQty:      normalQty,
		FinalQty:       normalQty,
		MinRequiredQty: minRequired,
		Multiplier:     1,
	}
	if sig.Mode != models.ModeReversalLong {
		return decision, nil
	}

	rawMultiplier := t.Config.ReversalLong.PositionSizeMultiplier
	multiplier := safeReversalPositionSizeMultiplier(t.Config.ReversalLong)
	decision.RiskProfileApplied = true
	decision.Multiplier = multiplier
	decision.InvalidMultiplier = rawMultiplier <= 0 || rawMultiplier > 1

	reversalQty := roundDownToStep(normalQty*multiplier, step)
	if reversalQty < minRequired {
		reversalQty = minRequired
		decision.MultiplierClampedToMin = true
	}
	if reversalQty > normalQty {
		reversalQty = normalQty
	}
	decision.FinalQty = reversalQty
	return decision, nil
}

// openPosition opens a new position
func (t *Trader) openPosition(newSide string, price float64) bool {
	return t.openPositionWithSignal(newSide, price, models.Signal{Kind: newSide, Direction: newSide, ClosePrice: price})
}

func (t *Trader) openPositionWithSignal(newSide string, price float64, sig models.Signal) bool {
	t.Logger.Info("Attempting to open new position: %s @ price %.2f", newSide, price)
	t.State.Lock()
	t.State.PartialTPDone = false
	t.State.Unlock()

	reversalRisk := t.reversalLongRuntimeRisk(sig, price)
	if reversalRisk.IsReversal && !t.Config.ReversalLong.TradeEnabled {
		t.Logger.Info("event=reversal_long_risk mode=%s final_decision=%s", sig.Mode, ReversalReasonTradeDisabled)
		return false
	}
	if reversalRisk.IsReversal && t.Config.ReversalLong.RequireStop && !reversalRisk.StopValid {
		t.Logger.Info("event=reversal_long_runtime_gate symbol=%s mode=%s side=%s trade_enabled=%t strength=0 required_strength=%d passed_strength=false position_open=false pending_order=false orderbook_runtime_required=%t orderbook_runtime_available=false orderbook_runtime_allowed=false orderbook_runtime_reject_reason= local_low=%.8f stop_below_local_low_bps=%.4f stop_price=%.8f stop_valid=false risk_profile_applied=false position_size_multiplier=%.4f normal_size=0 final_size=0 final_decision=%s",
			t.Config.Symbol, sig.Mode, newSide, t.Config.ReversalLong.TradeEnabled, t.runtimeStrengthThreshold(sig), t.Config.ReversalLong.RequireSoftOrderbookAtRuntime, reversalRisk.LocalLow, t.Config.ReversalLong.StopBelowLocalLowBps, reversalRisk.StopPrice, reversalRisk.PositionSizeMultiplier, reversalRisk.RejectReason)
		return false
	}

	// Calculate quantity
	t.Logger.Info("Fetching balance from exchange...")
	bal, err := t.APIClient.GetBalance("USDT")
	if err != nil {
		t.Logger.Error("Error getting balance: %v", err)
		return false
	}
	t.Logger.Info("Current balance: %.2f USDT", bal)

	sizeDecision, err := t.calculatePositionSize(price, bal, sig)
	if err != nil {
		t.Logger.Error("%v", err)
		return false
	}
	qty := sizeDecision.FinalQty
	if sizeDecision.InvalidMultiplier {
		t.Logger.Warning("Invalid reversal position size multiplier %.4f, using safe default %.4f", t.Config.ReversalLong.PositionSizeMultiplier, sizeDecision.Multiplier)
	}
	t.logReversalLongRisk(sig, sizeDecision, reversalRisk)
	if bal < price*qty {
		t.Logger.Error("Insufficient balance: %.2f USDT", bal)
		return false
	}

	// Place market order
	orderSide := "Buy"
	if t.PositionManager.NormalizeSide(newSide) == "SHORT" {
		orderSide = "Sell"
	}
	t.Logger.Info("Placing market order: %s %.4f", orderSide, qty)
	if err := t.OrderManager.PlaceOrderMarket(orderSide, qty, false); err != nil {
		t.Logger.Error("Error opening position %s: %v", newSide, err)
		return false
	}
	t.Logger.Info("Successfully placed market order to open position %s", newSide)

	// Set TP/SL with 2:1 ratio based on 15-minute projection
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		entry = price
	}

	t.State.Lock()
	t.State.LastEntryAt = t.now()
	t.State.LastEntryPrice = entry
	t.State.LastEntryDir = t.PositionManager.NormalizeSide(newSide)
	t.State.Unlock()

	// Calculate TP/SL with 2:1 ratio based on 15-minute projection
	tp, sl, diag := t.calculateInitialTPSLWithDiagnostics(entry, newSide, tpslDiagnosticContext{
		Symbol: t.Config.Symbol,
		Reason: "open_position",
		Qty:    qty,
		Log:    sig.Mode != models.ModeReversalLong,
	})
	if sig.Mode == models.ModeReversalLong {
		reversalRisk = t.reversalLongRuntimeRisk(sig, entry)
		if t.Config.ReversalLong.RequireStop && !reversalRisk.StopValid {
			t.Logger.Error("event=reversal_long_risk mode=%s final_decision=%s local_low=%.8f stop_price=%.8f entry=%.8f", sig.Mode, reversalRisk.RejectReason, reversalRisk.LocalLow, reversalRisk.StopPrice, entry)
			return false
		}
		sl = reversalRisk.StopPrice
		diag = t.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
			Symbol:    t.Config.Symbol,
			Reason:    "open_position_reversal",
			Qty:       qty,
			SignalID:  fmt.Sprintf("reversal_long:%d", sig.CandleSeq),
			CandleSeq: sig.CandleSeq,
			Log:       true,
		}, newSide, qty, entry, tp, sl)
		t.logTPSLDiagnostics(diag)
		t.Logger.Info("event=reversal_long_tpsl mode=%s risk_profile=reversal reversal_stop_source=local_low reversal_timeout_bars=%d local_low=%.8f stop_below_local_low_bps=%.4f finalTP=%.8f finalSL=%.8f",
			sig.Mode, t.Config.ReversalLong.TimeoutBars, reversalRisk.LocalLow, t.Config.ReversalLong.StopBelowLocalLowBps, tp, sl)
	}

	minPocket := t.minPocketDistance(entry)

	delay := time.Duration(t.Config.SLSetDelaySec) * time.Second
	t.Logger.Info("Position opened: %s %.4f @ %.2f | TP %.2f  SL %.2f (send after %s, minPocket %.4f)",
		newSide, qty, entry, tp, sl, delay.String(), minPocket)

	go func(tpVal, slVal float64, sendDiag TPSLDiagnostics) {
		if delay > 0 {
			time.Sleep(delay)
		}
		sendDiag.FinalTP = tpVal
		sendDiag.FinalSL = slVal
		sendDiag.FormattedTP = fmt.Sprintf("%.2f", tpVal)
		sendDiag.FormattedSL = fmt.Sprintf("%.2f", slVal)
		if err := t.updatePositionTPSLWithDiagnostics(sendDiag); err != nil {
			t.Logger.Error("Error setting TP/SL after delay: %v", err)
			return
		}
		t.Logger.Info("TP/SL placed ▶ TP %.2f  SL %.2f", tpVal, slVal)
	}(tp, sl, diag)
	return true
}

func (t *Trader) calculateInitialTPSL(entry float64, positionSide string) (float64, float64) {
	tp, sl, _ := t.calculateInitialTPSLWithDiagnostics(entry, positionSide, tpslDiagnosticContext{})
	return tp, sl
}

func (t *Trader) calculateInitialTPSLWithDiagnostics(entry float64, positionSide string, ctx tpslDiagnosticContext) (float64, float64, TPSLDiagnostics) {
	details := t.calcTPSLDetails(entry, positionSide)
	if details.side == "" {
		return 0, 0, TPSLDiagnostics{}
	}

	minPocket := t.minPocketDistance(entry)
	// SL is derived from TP (SLdist = TPdist/2), so the SL-side constraint we enforce here
	// is the "pocket" distance. Fee floors and minimum profit are enforced on the TP side.
	minSLDist := minPocket
	requiredTPDist := math.Max(details.tpDist, 2*minSLDist)

	// 1) Calculate TP first.
	tpDist := details.tpDist
	ratioAdjusted := false
	if tpDist < requiredTPDist {
		tpDist = requiredTPDist
		ratioAdjusted = true
	}

	tp := entry + tpDist
	if details.side == "SHORT" {
		tp = entry - tpDist
	}

	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}

	tpPreRound := tp
	tp = t.roundTP(entry, tp, tick, details.side)

	// Ensure TP still respects minimum distance after rounding.
	tpDistFinal := math.Abs(tp - entry)
	if tpDistFinal < requiredTPDist {
		need := math.Ceil((requiredTPDist-tpDistFinal)/tick) * tick
		if details.side == "LONG" {
			tp += math.Max(need, tick)
		} else {
			tp -= math.Max(need, tick)
		}
		tpDistFinal = math.Abs(tp - entry)
	}

	// 2) After TP is set, derive SL from TP distance: SLdist = TPdist/2.
	sl := entry - tpDistFinal/2
	if details.side == "SHORT" {
		sl = entry + tpDistFinal/2
	}
	slPreRound := sl
	sl = t.roundSL(entry, sl, tick, details.side)

	// If rounding pulled SL too close, widen TP (and recompute SL) until pocket/floors hold.
	invariantAdjusted := false
	for i := 0; i < 8; i++ {
		slDistFinal := math.Abs(entry - sl)
		// Keep SL strictly outside the pocket after rounding.
		if slDistFinal > minSLDist+1e-9 {
			break
		}
		if details.side == "LONG" {
			tp += 2 * tick
		} else {
			tp -= 2 * tick
		}
		invariantAdjusted = true
		tpDistFinal = math.Abs(tp - entry)
		sl = entry - tpDistFinal/2
		if details.side == "SHORT" {
			sl = entry + tpDistFinal/2
		}
		sl = t.roundSL(entry, sl, tick, details.side)
	}

	roundingApplied := tp != tpPreRound || sl != slPreRound
	roundingMode := "long_tp_floor_sl_ceil"
	if details.side == "SHORT" {
		roundingMode = "short_tp_ceil_sl_floor"
	}

	slDistFinal := math.Abs(entry - sl)
	tpTicks := tpDistFinal / tick
	slTicks := slDistFinal / tick
	actualRR := 0.0
	if slDistFinal > 0 {
		actualRR = tpDistFinal / slDistFinal
	}

	t.Logger.Debug(
		"TP/SL calc entry %.2f side %s tick %.4f atr %.4f atrPerc %.4f%% k %.2f volF %.2f regime %s regF %.2f tpPerc raw %.4f%% clamp %.4f%% [%.4f..%.4f] tpDistBase %.4f required %.4f rr %.2f pocket %.4f tp %.2f sl %.2f tpTicks %.1f slTicks %.1f flags ratioAdj=%t rounding=%t mode=%s invariant=%t",
		entry,
		details.side,
		tick,
		details.atr,
		details.atrPercent,
		details.k,
		details.volatilityFactor,
		details.regime,
		details.regimeFactor,
		details.tpPercRaw,
		details.tpPercClamped,
		details.tpPercMin,
		details.tpPercMax,
		details.tpDistBase,
		requiredTPDist,
		actualRR,
		minPocket,
		tp,
		sl,
		tpTicks,
		slTicks,
		ratioAdjusted,
		roundingApplied,
		roundingMode,
		invariantAdjusted,
	)

	diag := t.buildTPSLDiagnosticsFromCalc(ctx, details, tpPreRound, slPreRound, tp, sl, tick, requiredTPDist, minSLDist, ratioAdjusted, roundingApplied, invariantAdjusted, roundingMode)
	if ctx.Log {
		t.logTPSLDiagnostics(diag)
	}

	return tp, sl, diag
}

func (t *Trader) targetRRSource(regime string) string {
	if strings.ToLower(regime) == "trend" {
		if t.Config.TargetRRTrend > 0 {
			return "config_trend"
		}
		return "default_trend"
	}
	if t.Config.TargetRRRange > 0 {
		return "config_range"
	}
	return "default_range"
}

func (t *Trader) tpslRoundingDirections(side string) (string, string, string) {
	if side == "SHORT" {
		return "up", "down", "short_tp_ceil_sl_floor"
	}
	return "down", "up", "long_tp_floor_sl_ceil"
}

func (t *Trader) buildTPSLDiagnosticsFromCalc(ctx tpslDiagnosticContext, details tpslCalcDetails, rawTP, rawSL, finalTP, finalSL, tick, minRequiredTPDist, minRequiredSLDist float64, tpDistanceIncreased, roundingApplied, slDistanceIncreased bool, roundingMode string) TPSLDiagnostics {
	symbol := ctx.Symbol
	if symbol == "" {
		symbol = t.Config.Symbol
	}
	ts := ctx.Timestamp
	if ts.IsZero() {
		ts = t.now()
	}
	tpRoundDir, slRoundDir, defaultRoundingMode := t.tpslRoundingDirections(details.side)
	if roundingMode == "" {
		roundingMode = defaultRoundingMode
	}
	targetRR := t.targetRR(details.regime)
	targetRRConfigPresent := t.Config.TargetRRRange > 0 || t.Config.TargetRRTrend > 0
	atrCapConfigPresent := t.Config.AtrTPCapMultRange > 0 || t.Config.AtrTPCapMultTrend > 0
	return BuildTPSLDiagnostics(TPSLDiagnosticsInput{
		Symbol:                  symbol,
		Side:                    details.side,
		Reason:                  ctx.Reason,
		SignalID:                ctx.SignalID,
		CandleSeq:               ctx.CandleSeq,
		Timestamp:               ts,
		Qty:                     ctx.Qty,
		Entry:                   details.entry,
		RawTP:                   rawTP,
		RawSL:                   rawSL,
		RoundedTP:               finalTP,
		RoundedSL:               finalSL,
		FinalTP:                 finalTP,
		FinalSL:                 finalSL,
		MarketRegime:            details.regime,
		ATR:                     details.atr,
		ATRPeriod:               14,
		DynamicTPEnabled:        t.Config.DynamicTP,
		DynamicTPUsed:           true,
		DynamicTPRawPercent:     details.tpPercRaw,
		DynamicTPClampedPercent: details.tpPercClamped,
		DynamicTPMinPercent:     details.tpPercMin,
		DynamicTPMaxPercent:     details.tpPercMax,
		DynamicTPK:              details.k,
		DynamicTPVolatility:     details.volatilityFactor,
		DynamicTPRegimeFactor:   details.regimeFactor,
		TargetRR:                targetRR,
		TargetRRSource:          t.targetRRSource(details.regime),
		TargetRRUsed:            false,
		TargetRRConfigPresent:   targetRRConfigPresent,
		ATRCapConfigPresent:     atrCapConfigPresent,
		ATRCapUsed:              false,
		TickSize:                tick,
		TPRoundingDirection:     tpRoundDir,
		SLRoundingDirection:     slRoundDir,
		RoundingMode:            roundingMode,
		MinPocketDistance:       minRequiredSLDist,
		MinProfitPerc:           t.Config.MinProfitPerc,
		TPFeeFloorMult:          t.Config.TPFeeFloorMult,
		SLFeeFloorMult:          t.Config.SLFeeFloorMult,
		MinRequiredTPDist:       minRequiredTPDist,
		MinRequiredSLDist:       minRequiredSLDist,
		TPDistanceIncreased:     tpDistanceIncreased,
		SLDistanceIncreased:     slDistanceIncreased,
		FinalTPDistSource:       "dynamic_tp_clamped_percent",
		RoundTripFeePerc:        t.Config.RoundTripFeePerc,
		MakerTakerAssumption:    "round_trip_fee_split_50_50",
		FeeAdjustedBuffer:       t.feeBuffer(details.entry),
		PositionIdx:             0,
		TPSLMode:                "Full",
		TriggerBy:               "exchange_default",
		FormattedTP:             fmt.Sprintf("%.2f", finalTP),
		FormattedSL:             fmt.Sprintf("%.2f", finalSL),
	})
}

func (t *Trader) buildTPSLDiagnosticsForUpdate(ctx tpslDiagnosticContext, side string, qty, entry, tp, sl float64) TPSLDiagnostics {
	details := t.calcTPSLDetails(entry, side)
	if details.side == "" {
		details.side = t.PositionManager.NormalizeSide(side)
	}
	if details.side == "" {
		details.side = strings.ToUpper(side)
	}
	if details.entry == 0 {
		details.entry = entry
	}
	tick := t.State.Instr.TickSize
	if tick <= 0 {
		tick = 0.1
	}
	ctx.Qty = qty
	return t.buildTPSLDiagnosticsFromCalc(ctx, details, tp, sl, tp, sl, tick, 0, 0, false, false, false, "precomputed_update")
}

func (t *Trader) updatePositionTPSLWithDiagnostics(diag TPSLDiagnostics) error {
	t.logTPSLSend(diag)
	err := t.PositionManager.UpdatePositionTPSL(diag.Symbol, diag.FinalTP, diag.FinalSL)
	t.logTPSLResult(diag, err)
	return err
}

func (t *Trader) calculateTPSLLegacy(entryPrice float64, positionSide string) (float64, float64) {
	// Deprecated: keep for compatibility but route to the unified TP/SL logic.
	return t.calculateInitialTPSL(entryPrice, positionSide)
}

func (t *Trader) targetRR(regime string) float64 {
	if strings.ToLower(regime) == "trend" {
		val := 1.8
		if t.Config.TargetRRTrend > 0 {
			val = t.Config.TargetRRTrend
		}
		return clamp(val, 1.5, 2.2)
	}
	val := 1.0
	if t.Config.TargetRRRange > 0 {
		val = t.Config.TargetRRRange
	}
	return clamp(val, 0.8, 1.3)
}

func clamp(val, min, max float64) float64 {
	if val < min {
		return min
	}
	if val > max {
		return max
	}
	return val
}

func (t *Trader) atrTPCapMult(regime string) float64 {
	if strings.ToLower(regime) == "trend" {
		if t.Config.AtrTPCapMultTrend > 0 {
			return t.Config.AtrTPCapMultTrend
		}
		return 3.5
	}
	if t.Config.AtrTPCapMultRange > 0 {
		return t.Config.AtrTPCapMultRange
	}
	return 2.0
}

func (t *Trader) regimeFactor(regime string) float64 {
	switch strings.ToLower(regime) {
	case "trend":
		return 1.1
	case "range":
		return 0.9
	default:
		return 1.0
	}
}

func (t *Trader) clampTPDistWithAtr(tpDist, atr float64, regime string) (float64, float64, bool) {
	if atr <= 0 {
		return tpDist, 0, false
	}
	cap := atr * t.atrTPCapMult(regime) * t.regimeFactor(regime)
	if cap > 0 && tpDist > cap {
		return cap, cap, true
	}
	return tpDist, cap, false
}

func (t *Trader) minPocketDistance(entry float64) float64 {
	feeBuf := t.feeBuffer(entry)
	pocketMult := t.Config.PocketFeeMult
	if pocketMult <= 0 {
		pocketMult = 2.0
	}
	return math.Max(entry*t.Config.SLPocketPerc, feeBuf*pocketMult)
}

func (t *Trader) roundTP(entry, tp, tick float64, side string) float64 {
	if tick <= 0 {
		return tp
	}
	if side == "LONG" {
		return math.Floor(tp/tick) * tick
	}
	if side == "SHORT" {
		return math.Ceil(tp/tick) * tick
	}
	return math.Round(tp/tick) * tick
}

func (t *Trader) roundSL(entry, sl, tick float64, side string) float64 {
	if tick <= 0 {
		return sl
	}
	if side == "LONG" {
		return math.Ceil(sl/tick) * tick
	}
	if side == "SHORT" {
		return math.Floor(sl/tick) * tick
	}
	return math.Round(sl/tick) * tick
}

// calculateTPSLWithRatio calculates TP/SL using ATR/projection with dynamic RR and caps.
// TP is calculated based on expected price movement in the next 15 minutes when ATR is unavailable.
func (t *Trader) calculateTPSLWithRatio(entryPrice float64, positionSide string) (takeProfit float64, stopLoss float64) {
	return t.calculateInitialTPSL(entryPrice, positionSide)
}

func (t *Trader) calcTPSLDetails(entryPrice float64, positionSide string) tpslCalcDetails {
	side := t.PositionManager.NormalizeSide(positionSide)
	if side == "" {
		side = strings.ToUpper(positionSide)
	}
	regime := strings.ToLower(t.State.GetMarketRegime())
	if regime != "trend" {
		regime = "range"
	}

	snapshot := t.State.GetOhlcvSnapshot()
	atr := indicators.CalculateATR(snapshot.Highs, snapshot.Lows, snapshot.Closes, 14)
	dyn := t.dynamicTP(entryPrice, atr, regime)

	tpDist := 0.0
	if entryPrice > 0 {
		tpDist = entryPrice * dyn.ClampedPercent / 100
	}

	return tpslCalcDetails{
		entry:            entryPrice,
		side:             side,
		regime:           regime,
		atr:              atr,
		atrPercent:       dyn.ATRPercent,
		tpPercRaw:        dyn.RawPercent,
		tpPercClamped:    dyn.ClampedPercent,
		tpPercMin:        dyn.MinPercent,
		tpPercMax:        dyn.MaxPercent,
		k:                dyn.K,
		volatilityFactor: dyn.VolatilityFactor,
		regimeFactor:     dyn.RegimeFactor,
		tpDistBase:       tpDist,
		tpDist:           tpDist,
	}
}

const (
	bbPositionNotAvailable      = "not_available"
	bbPositionBelowLower        = "below_lower"
	bbPositionInsideBelowMiddle = "inside_below_middle"
	bbPositionInsideAboveMiddle = "inside_above_middle"
	bbPositionAboveUpper        = "above_upper"
)

func bollingerBandsAvailable(upper, middle, lower float64) bool {
	return upper != 0 && middle != 0 && lower != 0 && upper >= middle && middle >= lower
}

func volumeDiagnostics(closes, volumes []float64, window int) (volume, volumeSMA, volumeRatio float64, available bool) {
	if len(closes) == 0 || len(volumes) == 0 || len(volumes) != len(closes) {
		return 0, 0, 0, false
	}
	volume = volumes[len(volumes)-1]
	recent := volumes
	if window > 0 && len(recent) > window {
		recent = recent[len(recent)-window:]
	}
	volumeSMA = indicators.SMA(recent)
	if volumeSMA == 0 {
		return volume, volumeSMA, 0, false
	}
	return volume, volumeSMA, volume / volumeSMA, true
}

func rsiDiagnostics(rsiValues []float64, period int) (rsi, rsiPrev, rsiDelta float64, deltaAvailable bool) {
	if len(rsiValues) == 0 {
		return 0, 0, 0, false
	}
	lastIdx := len(rsiValues) - 1
	if math.IsNaN(rsiValues[lastIdx]) {
		return 0, 0, 0, false
	}
	rsi = rsiValues[lastIdx]
	prevIdx := lastIdx - 1
	if prevIdx < period || math.IsNaN(rsiValues[prevIdx]) {
		return rsi, 0, 0, false
	}
	rsiPrev = rsiValues[prevIdx]
	return rsi, rsiPrev, rsi - rsiPrev, true
}

func macdHistDiagnostics(closes []float64, currentHist float64) (previousHist, delta float64, available bool) {
	if len(closes) < 27 {
		return 0, 0, false
	}
	_, _, previousHist = indicators.MACD(closes[:len(closes)-1])
	if math.IsNaN(previousHist) || math.IsNaN(currentHist) {
		return 0, 0, false
	}
	return previousHist, currentHist - previousHist, true
}

func bbPositionForClose(closePrice, lower, middle, upper float64) string {
	if !bollingerBandsAvailable(upper, middle, lower) {
		return bbPositionNotAvailable
	}
	switch {
	case closePrice < lower:
		return bbPositionBelowLower
	case closePrice >= lower && closePrice < middle:
		return bbPositionInsideBelowMiddle
	case closePrice >= middle && closePrice <= upper:
		return bbPositionInsideAboveMiddle
	case closePrice > upper:
		return bbPositionAboveUpper
	default:
		return bbPositionNotAvailable
	}
}

func bbReclaimFromBands(previousClose, previousLow, previousLower, currentClose, currentLow, currentLower float64, available bool) (wasBelowLowerBB, returnedInsideBB, reclaimAvailable bool) {
	if !available {
		return false, false, false
	}
	wasBelowLowerBB = previousClose < previousLower || previousLow < previousLower || currentLow < currentLower
	return wasBelowLowerBB, wasBelowLowerBB && currentClose > currentLower, true
}

func bbReclaimDiagnostics(closes, lows []float64, currentLower float64, currentBBAvailable bool) (wasBelowLowerBB, returnedInsideBB, reclaimAvailable bool) {
	if !currentBBAvailable || len(closes) < 21 || len(lows) != len(closes) {
		return false, false, false
	}
	previousUpper, previousMiddle, previousLower := indicators.CalculateBollingerBands(closes[:len(closes)-1], 20, 2.0)
	previousBBAvailable := bollingerBandsAvailable(previousUpper, previousMiddle, previousLower)
	return bbReclaimFromBands(
		closes[len(closes)-2],
		lows[len(lows)-2],
		previousLower,
		closes[len(closes)-1],
		lows[len(lows)-1],
		currentLower,
		previousBBAvailable,
	)
}

func formatDiagnosticFloat(value float64, available bool, format string) string {
	if !available {
		return "not_available"
	}
	return fmt.Sprintf(format, value)
}

func formatDiagnosticBool(value bool, available bool) string {
	if !available {
		return "not_available"
	}
	return fmt.Sprintf("%t", value)
}

func (t *Trader) logIndicatorSnapshot(snap models.IndicatorSnapshot, regime string) {
	t.Logger.Debug("Indicators - close=%.2f sma=%.2f volume=%s volume_sma=%s volume_ratio=%s rsi=%.2f rsi_prev=%s rsi_delta=%s macd=%.4f macd_signal=%.4f macd_hist=%.4f macd_hist_prev=%s macd_hist_delta=%s bb_lower=%.2f bb_middle=%.2f bb_upper=%.2f bb_position=%s was_below_lower_bb=%s returned_inside_bb=%s atr=%.4f htf=%s regime=%s",
		snap.Close,
		snap.SMA,
		formatDiagnosticFloat(snap.Volume, snap.VolumeAvailable, "%.4f"),
		formatDiagnosticFloat(snap.VolumeSMA, snap.VolumeAvailable, "%.4f"),
		formatDiagnosticFloat(snap.VolumeRatio, snap.VolumeAvailable, "%.4f"),
		snap.RSI,
		formatDiagnosticFloat(snap.RSIPrev, snap.RSIDeltaAvailable, "%.2f"),
		formatDiagnosticFloat(snap.RSIDelta, snap.RSIDeltaAvailable, "%.2f"),
		snap.MACDLine,
		snap.MACDSignal,
		snap.MACDHist,
		formatDiagnosticFloat(snap.MACDHistPrev, snap.MACDHistDeltaAvailable, "%.4f"),
		formatDiagnosticFloat(snap.MACDHistDelta, snap.MACDHistDeltaAvailable, "%.4f"),
		snap.BBLower,
		snap.BBMiddle,
		snap.BBUpper,
		snap.BBPosition,
		formatDiagnosticBool(snap.WasBelowLowerBB, snap.BBReclaimAvailable),
		formatDiagnosticBool(snap.ReturnedInsideBB, snap.BBReclaimAvailable),
		snap.ATR,
		snap.HTFBias,
		regime,
	)
}

func (t *Trader) buildIndicatorSnapshot(snapshot models.OhlcvSnapshot) models.IndicatorSnapshot {
	closesCopy := snapshot.Closes
	cls := closesCopy[len(closesCopy)-1]
	low := cls
	if len(snapshot.Lows) > 0 {
		low = snapshot.Lows[len(snapshot.Lows)-1]
	}
	recent := closesCopy
	if len(recent) > t.Config.SmaLen && t.Config.SmaLen > 0 {
		recent = recent[len(recent)-t.Config.SmaLen:]
	}
	smaVal := indicators.SMA(recent)

	volume, volumeSMA, volumeRatio, volumeAvailable := volumeDiagnostics(closesCopy, snapshot.Volumes, t.Config.SmaLen)

	rsiValues := indicators.RSI(closesCopy, 14)
	rsi, rsiPrev, rsiDelta, rsiDeltaAvailable := rsiDiagnostics(rsiValues, 14)

	macdLine, signalLine, macdHist := indicators.MACD(closesCopy)
	macdHistPrev, macdHistDelta, macdHistDeltaAvailable := macdHistDiagnostics(closesCopy, macdHist)
	atr := indicators.CalculateATR(snapshot.Highs, snapshot.Lows, closesCopy, 14)
	bbUpper, bbMiddle, bbLower := indicators.CalculateBollingerBands(closesCopy, 20, 2.0)
	bbAvailable := bollingerBandsAvailable(bbUpper, bbMiddle, bbLower)
	bbPosition := bbPositionForClose(cls, bbLower, bbMiddle, bbUpper)
	wasBelowLowerBB, returnedInsideBB, bbReclaimAvailable := bbReclaimDiagnostics(closesCopy, snapshot.Lows, bbLower, bbAvailable)
	htfBias := t.higherTimeframeBiasForCloses(atr, closesCopy)

	return models.IndicatorSnapshot{
		Time:                   t.now(),
		Close:                  cls,
		Low:                    low,
		SMA:                    smaVal,
		Volume:                 volume,
		VolumeSMA:              volumeSMA,
		VolumeRatio:            volumeRatio,
		VolumeAvailable:        volumeAvailable,
		RSI:                    rsi,
		RSIPrev:                rsiPrev,
		RSIDelta:               rsiDelta,
		RSIDeltaAvailable:      rsiDeltaAvailable,
		MACDLine:               macdLine,
		MACDSignal:             signalLine,
		MACDHist:               macdHist,
		MACDHistPrev:           macdHistPrev,
		MACDHistDelta:          macdHistDelta,
		MACDHistDeltaAvailable: macdHistDeltaAvailable,
		ATR:                    atr,
		BBUpper:                bbUpper,
		BBMiddle:               bbMiddle,
		BBLower:                bbLower,
		BBPosition:             bbPosition,
		WasBelowLowerBB:        wasBelowLowerBB,
		ReturnedInsideBB:       returnedInsideBB,
		BBReclaimAvailable:     bbReclaimAvailable,
		HTFBias:                htfBias,
	}
}

// calculateTPBasedOn15MinProjection calculates TP based on expected price movement in next 15 minutes
func (t *Trader) calculateTPBasedOn15MinProjection(entryPrice float64, positionSide string) float64 {
	minTPPerc := t.Config.DynamicTPMinPerc
	if minTPPerc <= 0 {
		minTPPerc = 0.4
	}
	minTPFrac := minTPPerc / 100

	// Get recent price data for 15-minute projection
	snapshot := t.State.GetOhlcvSnapshot()
	if len(snapshot.Closes) < 15 {
		t.Logger.Warning("Not enough data for 15-minute projection, using fallback")
		if positionSide == "LONG" {
			return entryPrice * (1 + minTPFrac)
		}
		return entryPrice * (1 - minTPFrac)
	}

	// Use the last 15 minutes of data (15 one-minute candles)
	recentCloses := snapshot.Closes
	if len(recentCloses) > 15 {
		recentCloses = recentCloses[len(recentCloses)-15:]
	}

	// Calculate average price movement per minute
	priceChanges := make([]float64, len(recentCloses)-1)
	for i := 1; i < len(recentCloses); i++ {
		priceChanges[i-1] = recentCloses[i] - recentCloses[i-1]
	}

	// Calculate average movement per minute
	avgMovementPerMin := 0.0
	for _, change := range priceChanges {
		avgMovementPerMin += change
	}
	avgMovementPerMin /= float64(len(priceChanges))

	// Project movement for 15 minutes
	projectedMovement := avgMovementPerMin * 15

	// Apply momentum factor to make projections more realistic
	momentumFactor := 0.7 // Reduce projection to account for mean reversion
	projectedMovement *= momentumFactor

	move := projectedMovement
	if positionSide == "LONG" {
		move = math.Abs(projectedMovement)
	} else {
		move = -math.Abs(projectedMovement)
	}
	tpPrice := entryPrice + move

	// Ensure TP is reasonable (at least DynamicTPMinPerc away from entry)
	minTPDistance := entryPrice * minTPFrac
	actualTPDistance := math.Abs(tpPrice - entryPrice)

	if actualTPDistance < minTPDistance {
		if positionSide == "LONG" {
			tpPrice = entryPrice * (1 + minTPFrac)
		} else {
			tpPrice = entryPrice * (1 - minTPFrac)
		}
	}

	t.Logger.Debug("15-minute TP projection - Entry: %.2f, Avg movement/minute: %.4f, Projected movement: %.4f, TP: %.2f",
		entryPrice, avgMovementPerMin, projectedMovement, tpPrice)

	return tpPrice
}

// adjustTPSL adjusts TP/SL for long positions with 2:1 ratio
func (t *Trader) adjustTPSL(closePrice float64) {
	t.Logger.Info("Adjusting TP/SL for LONG position, current price: %.2f", closePrice)

	exists, side, qty, curTP, _ := t.PositionManager.HasOpenPosition()
	if !exists || t.PositionManager.NormalizeSide(side) != "LONG" {
		t.Logger.Info("No LONG position found, skipping TP/SL adjustment")
		return
	}
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		t.Logger.Error("Could not get entry price, skipping TP/SL adjustment")
		return
	}

	newTP, newSL, diag := t.calculateInitialTPSLWithDiagnostics(entry, "LONG", tpslDiagnosticContext{
		Symbol: t.Config.Symbol,
		Reason: "adjust_same_side_long",
		Qty:    qty,
		Log:    true,
	})
	if math.Abs(newTP-curTP) < t.State.Instr.TickSize && newSL == 0 {
		return
	}

	t.Logger.Info("Sending TP/SL update to exchange: TP %.2f, SL %.2f", newTP, newSL)
	if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
		t.Logger.Error("Error updating TP/SL: %v", err)
	} else {
		t.Logger.Info("TP/SL updated ▶ TP %.2f  SL %.2f", newTP, newSL)
	}
}

// adjustTPSLForShort adjusts TP/SL for short positions with 2:1 ratio
func (t *Trader) adjustTPSLForShort(closePrice float64) {
	t.Logger.Info("Adjusting TP/SL for SHORT position, current price: %.2f", closePrice)

	exists, side, qty, curTP, _ := t.PositionManager.HasOpenPosition()
	if !exists || side != "SHORT" {
		t.Logger.Info("No SHORT position found, skipping TP/SL adjustment")
		return
	}
	entry := t.PositionManager.GetLastEntryPrice()
	if entry == 0 {
		t.Logger.Error("Could not get entry price, skipping TP/SL adjustment")
		return
	}

	newTP, newSL, diag := t.calculateInitialTPSLWithDiagnostics(entry, "SHORT", tpslDiagnosticContext{
		Symbol: t.Config.Symbol,
		Reason: "adjust_same_side_short",
		Qty:    qty,
		Log:    true,
	})
	if math.Abs(newTP-curTP) < t.State.Instr.TickSize && newSL == 0 {
		return
	}

	t.Logger.Info("Sending TP/SL update to exchange: TP %.2f, SL %.2f", newTP, newSL)
	if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
		t.Logger.Error("adjustTPSLForShort error: %v", err)
	} else {
		t.Logger.Info("Recalculated TP/SL ▶ TP %.2f SL %.2f", newTP, newSL)
	}
}

const tieStrengthNoSignalReason = "tie_strength_no_signal"

func chooseSignalDirection(strength map[string]int) (string, int, string) {
	longStrength := strength["LONG"]
	shortStrength := strength["SHORT"]

	if longStrength > shortStrength {
		return "LONG", longStrength, ""
	}
	if shortStrength > longStrength {
		return "SHORT", shortStrength, ""
	}
	if longStrength > 0 {
		return "", 0, tieStrengthNoSignalReason
	}
	return "", 0, ""
}

type rsiPolicyDecision struct {
	Mode         string
	Value        float64
	Low          float64
	High         float64
	AllowLong    bool
	AllowShort   bool
	LongScore    int
	ShortScore   int
	LongContrib  string
	ShortContrib string
	Impact       string
}

func normalizeRSIMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case config.RSIModeOff:
		return config.RSIModeOff
	case config.RSIModeLog:
		return config.RSIModeLog
	case config.RSIModeScore:
		return config.RSIModeScore
	case config.RSIModeFilter, "":
		return config.RSIModeFilter
	default:
		return config.RSIModeFilter
	}
}

func evaluateRSIPolicy(rsi, low, high float64, mode string) rsiPolicyDecision {
	decision := rsiPolicyDecision{
		Mode:       normalizeRSIMode(mode),
		Value:      rsi,
		Low:        low,
		High:       high,
		AllowLong:  true,
		AllowShort: true,
		Impact:     "not_used",
	}

	switch decision.Mode {
	case config.RSIModeOff:
		decision.Impact = "off"
	case config.RSIModeLog:
		decision.Impact = "log_only"
	case config.RSIModeFilter:
		if rsi == 0 {
			decision.Impact = "filter_no_value"
			return decision
		}
		decision.AllowLong = rsi <= low
		decision.AllowShort = rsi >= high
		switch {
		case !decision.AllowLong && !decision.AllowShort:
			decision.Impact = "filter_blocks_long_short"
		case !decision.AllowLong:
			decision.Impact = "filter_blocks_long"
		case !decision.AllowShort:
			decision.Impact = "filter_blocks_short"
		default:
			decision.Impact = "filter_allows_long_short"
		}
	case config.RSIModeScore:
		if rsi == 0 {
			decision.Impact = "score_no_value"
			return decision
		}
		if rsi <= low {
			decision.LongScore = 1
			decision.LongContrib = "rsi-oversold"
		}
		if rsi >= high {
			decision.ShortScore = 1
			decision.ShortContrib = "rsi-overbought"
		}
		switch {
		case decision.LongScore > 0 && decision.ShortScore > 0:
			decision.Impact = "score_long_short"
		case decision.LongScore > 0:
			decision.Impact = "score_long"
		case decision.ShortScore > 0:
			decision.Impact = "score_short"
		default:
			decision.Impact = "score_no_threshold"
		}
	}

	return decision
}

func applyRSIScore(strength map[string]int, contrib map[string][]string, decision rsiPolicyDecision) {
	if decision.LongScore > 0 {
		strength["LONG"] += decision.LongScore
		contrib["LONG"] = append(contrib["LONG"], decision.LongContrib)
	}
	if decision.ShortScore > 0 {
		strength["SHORT"] += decision.ShortScore
		contrib["SHORT"] = append(contrib["SHORT"], decision.ShortContrib)
	}
}

const (
	filterRSIConfirmation       = "rsi_filter"
	filterHTFConfirmation       = "htf_filter"
	filterOrderbookConfirmation = "orderbook_confirmation"
	filterGracePeriod           = "grace_period"
	filterMinimumPriceDelta     = "minimum_price_delta"
	filterCooldown              = "cooldown"
	filterDuplicatePosition     = "duplicate_position"
	filterMinimumStrength       = "minimum_strength"
	filterNoScore               = "no_score"
	filterReversalRiskProfile   = "reversal_risk_profile"
)

type scoredSignal struct {
	Snapshot    models.IndicatorSnapshot
	CandleSeq   uint64
	Time        time.Time
	Regime      string
	Deadband    float64
	RSIDecision rsiPolicyDecision

	LongScore    int
	ShortScore   int
	LongReasons  []string
	ShortReasons []string
}

type filteredSignal struct {
	Accepted bool
	Signal   models.Signal
	Scored   scoredSignal
}

type orderbookEvaluator func(side string, lastPrice float64) models.OrderbookResult

func addUniqueString(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func addDebugFilter(debug *models.SignalDebug, name string) {
	debug.AppliedFilters = addUniqueString(debug.AppliedFilters, name)
}

func rejectDebug(debug *models.SignalDebug, filterName, reason string) {
	debug.RejectedBy = addUniqueString(debug.RejectedBy, filterName)
	if reason != "" {
		debug.Reasons = append(debug.Reasons, reason)
	}
}

func cloneSignalDebug(debug models.SignalDebug) models.SignalDebug {
	return models.SignalDebug{
		LongScore:                      debug.LongScore,
		ShortScore:                     debug.ShortScore,
		ChosenDirection:                debug.ChosenDirection,
		AppliedFilters:                 append([]string(nil), debug.AppliedFilters...),
		RejectedBy:                     append([]string(nil), debug.RejectedBy...),
		Reasons:                        append([]string(nil), debug.Reasons...),
		VolumeRatio:                    debug.VolumeRatio,
		VolumeRatioAvailable:           debug.VolumeRatioAvailable,
		RSIDelta:                       debug.RSIDelta,
		RSIDeltaAvailable:              debug.RSIDeltaAvailable,
		MACDHistDelta:                  debug.MACDHistDelta,
		MACDHistDeltaAvailable:         debug.MACDHistDeltaAvailable,
		BBPosition:                     debug.BBPosition,
		WasBelowLowerBB:                debug.WasBelowLowerBB,
		ReturnedInsideBB:               debug.ReturnedInsideBB,
		BBReclaimAvailable:             debug.BBReclaimAvailable,
		ReversalScore:                  debug.ReversalScore,
		ReversalState:                  debug.ReversalState,
		ReversalLowestLow:              debug.ReversalLowestLow,
		ReversalStopPrice:              debug.ReversalStopPrice,
		ReversalTimeoutBars:            debug.ReversalTimeoutBars,
		ReversalPositionSizeMultiplier: debug.ReversalPositionSizeMultiplier,
		ReversalSoftOrderbookAllowed:   debug.ReversalSoftOrderbookAllowed,
		ReversalHTFOverrideAllowed:     debug.ReversalHTFOverrideAllowed,
	}
}

func (s *scoredSignal) addScore(side, reason string) {
	switch side {
	case "LONG":
		s.LongScore++
		s.LongReasons = append(s.LongReasons, reason)
	case "SHORT":
		s.ShortScore++
		s.ShortReasons = append(s.ShortReasons, reason)
	}
}

func (s scoredSignal) reasonsForSide(side string) []string {
	switch side {
	case "LONG":
		return append([]string(nil), s.LongReasons...)
	case "SHORT":
		return append([]string(nil), s.ShortReasons...)
	default:
		return nil
	}
}

func signalDebugFromScore(scored scoredSignal) models.SignalDebug {
	reasons := make([]string, 0, len(scored.LongReasons)+len(scored.ShortReasons)+3)
	for _, reason := range scored.LongReasons {
		reasons = append(reasons, "LONG:"+reason)
	}
	for _, reason := range scored.ShortReasons {
		reasons = append(reasons, "SHORT:"+reason)
	}
	if scored.RSIDecision.Mode != "" {
		reasons = append(reasons, fmt.Sprintf("rsi mode=%s value=%.2f impact=%s", scored.RSIDecision.Mode, scored.RSIDecision.Value, scored.RSIDecision.Impact))
	}
	if scored.Snapshot.HTFBias != "" {
		reasons = append(reasons, "htf_bias="+scored.Snapshot.HTFBias)
	}
	if scored.Regime != "" {
		reasons = append(reasons, "regime="+scored.Regime)
	}
	return models.SignalDebug{
		LongScore:              scored.LongScore,
		ShortScore:             scored.ShortScore,
		Reasons:                reasons,
		VolumeRatio:            scored.Snapshot.VolumeRatio,
		VolumeRatioAvailable:   scored.Snapshot.VolumeAvailable,
		RSIDelta:               scored.Snapshot.RSIDelta,
		RSIDeltaAvailable:      scored.Snapshot.RSIDeltaAvailable,
		MACDHistDelta:          scored.Snapshot.MACDHistDelta,
		MACDHistDeltaAvailable: scored.Snapshot.MACDHistDeltaAvailable,
		BBPosition:             scored.Snapshot.BBPosition,
		WasBelowLowerBB:        scored.Snapshot.WasBelowLowerBB,
		ReturnedInsideBB:       scored.Snapshot.ReturnedInsideBB,
		BBReclaimAvailable:     scored.Snapshot.BBReclaimAvailable,
	}
}

// ScoreSignal calculates indicator scores only. Hard filters are applied later.
func (t *Trader) ScoreSignal(snap models.IndicatorSnapshot, candleSeq uint64, closesCopy []float64) scoredSignal {
	return t.scoreSignalWithRegime(snap, candleSeq, closesCopy, t.State.GetMarketRegime())
}

func (t *Trader) scoreSignalWithRegime(snap models.IndicatorSnapshot, candleSeq uint64, closesCopy []float64, regime string) scoredSignal {
	baseDeadband := 0.005
	if regime == "trend" {
		baseDeadband = 0.01
	}
	deadband := t.adaptiveDeadband(snap.ATR, snap.Close, baseDeadband)

	rsiDecision := evaluateRSIPolicy(snap.RSI, t.Config.RSILow, t.Config.RSIHigh, t.Config.RSIMode)
	scored := scoredSignal{
		Snapshot:    snap,
		CandleSeq:   candleSeq,
		Time:        t.now(),
		Deadband:    deadband,
		RSIDecision: rsiDecision,
	}

	if regime == "" {
		regime = "trend"
	}
	scored.Regime = regime

	if rsiDecision.Mode == config.RSIModeScore {
		if rsiDecision.LongScore > 0 {
			scored.addScore("LONG", rsiDecision.LongContrib)
		}
		if rsiDecision.ShortScore > 0 {
			scored.addScore("SHORT", rsiDecision.ShortContrib)
		}
	}

	// RSI divergence is a score contribution only when RSI is configured as a scoring factor.
	if rsiDecision.Mode == config.RSIModeScore && len(closesCopy) >= 3 {
		cNow := closesCopy[len(closesCopy)-1]
		cPrev := closesCopy[len(closesCopy)-3]
		rsiValues := indicators.RSI(closesCopy, 14)
		if len(rsiValues) >= 3 {
			rNow := rsiValues[len(rsiValues)-1]
			rPrev := rsiValues[len(rsiValues)-3]
			if cNow < cPrev && rNow > rPrev {
				scored.addScore("LONG", "rsi-div-bull")
			}
			if cNow > cPrev && rNow < rPrev {
				scored.addScore("SHORT", "rsi-div-bear")
			}
		}
	}

	if regime == "trend" {
		if snap.Close > snap.SMA+deadband {
			scored.addScore("LONG", "sma-breakout")
		}
		if snap.Close < snap.SMA-deadband {
			scored.addScore("SHORT", "sma-breakdown")
		}
		if snap.MACDHist > 0 && snap.MACDLine > snap.MACDSignal {
			scored.addScore("LONG", "macd-trend")
		}
		if snap.MACDHist < 0 && snap.MACDLine < snap.MACDSignal {
			scored.addScore("SHORT", "macd-trend")
		}
		if snap.Close >= snap.BBUpper+deadband {
			scored.addScore("LONG", "bb-breakout")
		}
		if snap.Close <= snap.BBLower-deadband {
			scored.addScore("SHORT", "bb-breakout")
		}
	} else {
		if snap.Close <= snap.BBLower-deadband {
			scored.addScore("LONG", "bb-revert")
		}
		if snap.Close >= snap.BBUpper+deadband {
			scored.addScore("SHORT", "bb-revert")
		}
		if snap.Close < snap.SMA-deadband {
			scored.addScore("LONG", "sma-revert")
		}
		if snap.Close > snap.SMA+deadband {
			scored.addScore("SHORT", "sma-revert")
		}
		if snap.MACDHist < 0 {
			scored.addScore("LONG", "macd-mean")
		}
		if snap.MACDHist > 0 {
			scored.addScore("SHORT", "macd-mean")
		}
	}

	return scored
}

func signalFromScore(scored scoredSignal, dir string, strength int, orderbook *models.OrderbookResult, debug models.SignalDebug) models.Signal {
	signalTime := scored.Time
	if signalTime.IsZero() {
		signalTime = time.Now()
	}
	return models.Signal{
		Kind:       dir,
		Direction:  dir,
		Strength:   strength,
		Contribs:   scored.reasonsForSide(dir),
		HighConf:   false,
		ClosePrice: scored.Snapshot.Close,
		CandleSeq:  scored.CandleSeq,
		Time:       signalTime,
		Orderbook:  orderbook,
		Debug:      debug,
	}
}

func sideRejected(rejected []string) bool {
	return len(rejected) > 0
}

// FilterSignal applies hard filters without changing the score calculated by ScoreSignal.
func (t *Trader) FilterSignal(scored scoredSignal, evaluateOrderbook orderbookEvaluator) filteredSignal {
	debug := signalDebugFromScore(scored)
	var longRejectedBy, shortRejectedBy []string

	if scored.RSIDecision.Mode == config.RSIModeFilter {
		addDebugFilter(&debug, filterRSIConfirmation)
		if !scored.RSIDecision.AllowLong {
			longRejectedBy = addUniqueString(longRejectedBy, filterRSIConfirmation)
			debug.Reasons = append(debug.Reasons, fmt.Sprintf("LONG:rsi_filter_blocked rsi=%.2f low=%.2f", scored.RSIDecision.Value, scored.RSIDecision.Low))
		}
		if !scored.RSIDecision.AllowShort {
			shortRejectedBy = addUniqueString(shortRejectedBy, filterRSIConfirmation)
			debug.Reasons = append(debug.Reasons, fmt.Sprintf("SHORT:rsi_filter_blocked rsi=%.2f high=%.2f", scored.RSIDecision.Value, scored.RSIDecision.High))
		}
	}

	switch scored.Snapshot.HTFBias {
	case "LONG":
		addDebugFilter(&debug, filterHTFConfirmation)
		shortRejectedBy = addUniqueString(shortRejectedBy, filterHTFConfirmation)
		debug.Reasons = append(debug.Reasons, "SHORT:htf_filter_blocked bias=LONG")
	case "SHORT":
		addDebugFilter(&debug, filterHTFConfirmation)
		longRejectedBy = addUniqueString(longRejectedBy, filterHTFConfirmation)
		debug.Reasons = append(debug.Reasons, "LONG:htf_filter_blocked bias=SHORT")
	}

	longCandidate := scored.LongScore > 0 && !sideRejected(longRejectedBy)
	shortCandidate := scored.ShortScore > 0 && !sideRejected(shortRejectedBy)

	dir := ""
	strength := 0
	switch {
	case longCandidate && shortCandidate:
		var reason string
		dir, strength, reason = chooseSignalDirection(map[string]int{
			"LONG":  scored.LongScore,
			"SHORT": scored.ShortScore,
		})
		if reason == tieStrengthNoSignalReason {
			rejectDebug(&debug, tieStrengthNoSignalReason, fmt.Sprintf("%s: long=%d short=%d", tieStrengthNoSignalReason, scored.LongScore, scored.ShortScore))
		}
	case longCandidate:
		dir = "LONG"
		strength = scored.LongScore
	case shortCandidate:
		dir = "SHORT"
		strength = scored.ShortScore
	default:
		if scored.LongScore == 0 && scored.ShortScore == 0 {
			rejectDebug(&debug, filterNoScore, "no_score: long=0 short=0")
		}
		for _, filterName := range longRejectedBy {
			if scored.LongScore > 0 {
				rejectDebug(&debug, filterName, "LONG rejected before direction selection")
			}
		}
		for _, filterName := range shortRejectedBy {
			if scored.ShortScore > 0 {
				rejectDebug(&debug, filterName, "SHORT rejected before direction selection")
			}
		}
	}

	debug.ChosenDirection = dir
	if dir == "" || strength == 0 || len(debug.RejectedBy) > 0 {
		return filteredSignal{
			Accepted: false,
			Signal:   signalFromScore(scored, dir, strength, nil, debug),
			Scored:   scored,
		}
	}

	var obResult *models.OrderbookResult
	if evaluateOrderbook != nil {
		addDebugFilter(&debug, filterOrderbookConfirmation)
		evaluated := evaluateOrderbook(dir, scored.Snapshot.Close)
		obResult = &evaluated
		debug.Reasons = append(debug.Reasons, fmt.Sprintf("%s:orderbook %s", dir, orderbookResultLog(obResult)))
		if !evaluated.Passed {
			rejectDebug(&debug, filterOrderbookConfirmation, fmt.Sprintf("%s:orderbook_rejected reason=%s", dir, evaluated.Reason))
			return filteredSignal{
				Accepted: false,
				Signal:   signalFromScore(scored, dir, strength, obResult, debug),
				Scored:   scored,
			}
		}
	}

	return filteredSignal{
		Accepted: true,
		Signal:   signalFromScore(scored, dir, strength, obResult, debug),
		Scored:   scored,
	}
}

func (t *Trader) updateLastSignalSnapshot(sig models.Signal) {
	contribCopy := append([]string(nil), sig.Contribs...)
	orderbookCopy := cloneOrderbookResult(sig.Orderbook)
	debugCopy := cloneSignalDebug(sig.Debug)

	t.State.StatusLock.Lock()
	t.State.LastSignal = models.SignalSnapshot{
		Kind:       sig.Kind,
		Direction:  sig.Direction,
		Mode:       sig.Mode,
		Strength:   sig.Strength,
		Contribs:   contribCopy,
		HighConf:   sig.HighConf,
		ClosePrice: sig.ClosePrice,
		CandleSeq:  sig.CandleSeq,
		Time:       sig.Time,
		Orderbook:  orderbookCopy,
		Debug:      debugCopy,
	}
	t.State.StatusLock.Unlock()
}

// EmitSignal records the filtered signal context and sends accepted signals to the trader loop.
func (t *Trader) EmitSignal(filtered filteredSignal) bool {
	sig := filtered.Signal
	t.updateLastSignalSnapshot(sig)

	if !filtered.Accepted {
		if len(sig.Debug.RejectedBy) > 0 {
			t.Logger.Info("Signal rejected before emit: chosen=%s rejectedBy=%v reasons=%v", sig.Debug.ChosenDirection, sig.Debug.RejectedBy, sig.Debug.Reasons)
		}
		return false
	}

	select {
	case t.State.SigChan <- sig:
		t.Logger.Info("%s signal generated (strength=%d, highConf=%t, debug=%+v, orderbook=%s): %v",
			sig.Direction, sig.Strength, sig.HighConf, sig.Debug, orderbookResultLog(sig.Orderbook), sig.Contribs)
		return true
	default:
		t.Logger.Warning("Signal channel full, dropping %s signal", sig.Direction)
		return false
	}
}

func ensureSignalDebug(sig *models.Signal, dir string) {
	if sig.Debug.ChosenDirection == "" {
		sig.Debug.ChosenDirection = dir
	}
	if sig.Debug.LongScore == 0 && sig.Debug.ShortScore == 0 && sig.Strength > 0 {
		switch dir {
		case "LONG":
			sig.Debug.LongScore = sig.Strength
		case "SHORT":
			sig.Debug.ShortScore = sig.Strength
		}
	}
}

func addSignalRuntimeFilter(sig *models.Signal, filterName string) {
	addDebugFilter(&sig.Debug, filterName)
}

func rejectSignalRuntime(sig *models.Signal, filterName, reason string) {
	rejectDebug(&sig.Debug, filterName, reason)
}

func (t *Trader) logSignalRuntimeRejection(sig models.Signal) {
	t.updateLastSignalSnapshot(sig)
	t.Logger.Info("Signal rejected in Trader: chosen=%s rejectedBy=%v reasons=%v",
		sig.Debug.ChosenDirection, sig.Debug.RejectedBy, sig.Debug.Reasons)
}

func (t *Trader) processSMASignalCycle(lastCandleSeq *uint64, evaluateOrderbook orderbookEvaluator) bool {
	ohlcvSnapshot := t.State.GetOhlcvSnapshot()
	candleSeq := ohlcvSnapshot.CandleSeq
	if candleSeq == 0 || candleSeq == *lastCandleSeq {
		return false
	}
	*lastCandleSeq = candleSeq

	if len(ohlcvSnapshot.Closes) < t.Config.SmaLen {
		t.Logger.Debug("SMA worker waiting: have %d candles, need %d", len(ohlcvSnapshot.Closes), t.Config.SmaLen)
		return false
	}

	closesCopy := ohlcvSnapshot.Closes
	regimeSnapshot := t.State.GetMarketRegimeSnapshot()
	snap := t.buildIndicatorSnapshot(ohlcvSnapshot)
	t.State.StatusLock.Lock()
	t.State.LastIndicators = snap
	t.State.StatusLock.Unlock()

	t.logIndicatorSnapshot(snap, regimeSnapshot.MarketRegime)
	reversalEval, hasReversalEval := t.evaluateReversalLongLogOnly(snap)

	scored := t.scoreSignalWithRegime(snap, candleSeq, closesCopy, regimeSnapshot.MarketRegime)
	t.Logger.Debug("Signal score: long=%d short=%d longReasons=%v shortReasons=%v rsiMode=%s rsi=%.2f rsiImpact=%s deadband=%.4f regime=%s",
		scored.LongScore, scored.ShortScore, scored.LongReasons, scored.ShortReasons,
		scored.RSIDecision.Mode, scored.RSIDecision.Value, scored.RSIDecision.Impact, scored.Deadband, scored.Regime)

	filtered := t.FilterSignal(scored, evaluateOrderbook)
	filtered, reversalPipelineDecision := t.applyReversalLongPipelineBridge(filtered, scored, reversalEval, hasReversalEval)
	emitted := t.EmitSignal(filtered)
	t.logReversalLongPipelineDecision(reversalPipelineDecision, emitted)
	return emitted
}

// SMAMovingAverageWorker generates signals with regime-aware logic
func (t *Trader) SMAMovingAverageWorker() {
	t.Logger.Info("Starting SMA Moving Average Worker...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	lastCandleSeq := uint64(0)

	for range ticker.C {
		t.processSMASignalCycle(&lastCandleSeq, t.EvaluateOrderbookStrength)
	}
}

// closeOppositePosition closes an existing position before opening a new one in the opposite direction.
// It returns whether a reduce-only close was attempted.
func (t *Trader) closeOppositePosition(newSide string) (bool, error) {
	exists, side, qty, _, _ := t.PositionManager.HasOpenPosition()
	if !exists {
		return false, nil
	}

	side = t.PositionManager.NormalizeSide(side)
	newSide = t.PositionManager.NormalizeSide(newSide)

	// Only close if it's truly the opposite side
	if side != "" && side != newSide {
		t.Logger.Info("Closing opposite position: %s", side)

		// Get entry price for profit calculation
		entryPrice := t.PositionManager.GetLastEntryPrice()

		reduceSide := "Sell"
		if side == "SHORT" {
			reduceSide = "Buy"
		}

		if err := t.OrderManager.PlaceOrderMarket(reduceSide, qty, true); err != nil {
			t.Logger.Error("Error closing position %s: %v", side, err)
			return true, err
		}

		t.Logger.Info("Successfully sent order to close position %s", side)

		// Calculate profit based on current price and entry price
		var exitPrice float64
		if side == "LONG" {
			// For LONG positions, we sell to close
			exitPrice = t.PositionManager.GetLastBidPrice()
		} else if side == "SHORT" {
			// For SHORT positions, we buy to close
			exitPrice = t.PositionManager.GetLastAskPrice()
		}

		if exitPrice <= 0 {
			exitPrice = entryPrice // Fallback if we can't get current price
		}

		profit := t.PositionManager.CalculatePositionProfit(side, entryPrice, exitPrice, qty)
		signalType := "CLOSE_LONG"
		if side == "SHORT" {
			signalType = "CLOSE_SHORT"
		}

		t.PositionManager.UpdateSignalStats(signalType, profit)
		t.State.Lock()
		t.State.LastExitAt = t.now()
		t.State.LastExitDir = side
		t.State.Unlock()
		return true, nil
	}
	return false, nil
}

func (t *Trader) waitForOppositePositionClosed(oppositeSide string, timeout time.Duration) bool {
	if oppositeSide == "" {
		return true
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		exists, side, _, _, _ := t.PositionManager.HasOpenPosition()
		side = t.PositionManager.NormalizeSide(side)
		if !exists || side != oppositeSide {
			return true
		}
		if time.Now().After(deadline) {
			t.Logger.Error("Timed out waiting for opposite position to close: side=%s timeout=%s", oppositeSide, timeout)
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func signalReversalKey(sig models.Signal, dir string) string {
	if sig.CandleSeq != 0 {
		return fmt.Sprintf("candle=%d direction=%s", sig.CandleSeq, dir)
	}
	return fmt.Sprintf("time=%d direction=%s close=%.8f", sig.Time.UnixNano(), dir, sig.ClosePrice)
}

func (t *Trader) executeConfirmedSignal(sig models.Signal, dir string, reversalCloseKeys map[string]struct{}) string {
	exists, posSide, _, _, _ := t.PositionManager.HasOpenPosition()
	posSide = t.PositionManager.NormalizeSide(posSide)
	reversalRequired := exists && posSide != "" && posSide != dir
	closeAttempted := false
	openAttempted := false
	action := ""
	reversalKey := signalReversalKey(sig, dir)

	if reversalRequired {
		if _, seen := reversalCloseKeys[reversalKey]; seen {
			t.Logger.Warning("Skipping duplicate reversal close for signal=%s direction=%s currentPosition=%s", reversalKey, dir, posSide)
			t.Logger.Info("Signal execution flow: direction=%s currentPosition=%s reversalRequired=%t closeAttempted=%t openAttempted=%t",
				dir, posSide, reversalRequired, closeAttempted, openAttempted)
			return ""
		}

		attempted, err := t.closeOppositePosition(dir)
		closeAttempted = attempted
		reversalCloseKeys[reversalKey] = struct{}{}
		if err != nil {
			t.Logger.Info("Signal execution flow: direction=%s currentPosition=%s reversalRequired=%t closeAttempted=%t openAttempted=%t",
				dir, posSide, reversalRequired, closeAttempted, openAttempted)
			return ""
		}
		if attempted && !t.waitForOppositePositionClosed(posSide, 2*time.Second) {
			t.Logger.Info("Signal execution flow: direction=%s currentPosition=%s reversalRequired=%t closeAttempted=%t openAttempted=%t",
				dir, posSide, reversalRequired, closeAttempted, openAttempted)
			return ""
		}
	}

	switch dir {
	case "LONG":
		openAttempted = t.handleLongSignal(sig)
		action = "LONG"
	case "SHORT":
		openAttempted = t.handleShortSignal(sig)
		action = "SHORT"
	}

	t.Logger.Info("Signal execution flow: direction=%s currentPosition=%s reversalRequired=%t closeAttempted=%t openAttempted=%t",
		dir, posSide, reversalRequired, closeAttempted, openAttempted)

	if !openAttempted && reversalRequired {
		return ""
	}
	return action
}

// Trader processes signals and executes trades
func (t *Trader) Trader() {
	signalStrength := map[string]int{"LONG": 0, "SHORT": 0}
	lastDirection := ""
	lastCandleSeq := uint64(0)
	reversalCloseKeys := make(map[string]struct{})
	grace := time.Duration(t.Config.GracePeriodSec) * time.Second
	priceBufMult := t.Config.MinReentryFeeBufferMult
	if priceBufMult <= 0 {
		priceBufMult = 2.0
	}

	for sig := range t.State.SigChan {
		if sig.CandleSeq != 0 && sig.CandleSeq != lastCandleSeq {
			t.resetSignalStrength(&signalStrength)
			reversalCloseKeys = make(map[string]struct{})
			if t.Config.Debug {
				t.Logger.Debug("New candle seq=%d: reset signal strength", sig.CandleSeq)
			}
			lastCandleSeq = sig.CandleSeq
		}

		dir := t.PositionManager.NormalizeSide(sig.Direction)
		if dir == "" {
			continue
		}
		ensureSignalDebug(&sig, dir)

		if t.Config.Debug {
			t.Logger.Debug("Signal received: dir=%s strength=%d highConf=%t close=%.2f candleSeq=%d debug=%+v", dir, sig.Strength, sig.HighConf, sig.ClosePrice, sig.CandleSeq, sig.Debug)
		}

		now := t.now()
		exists, posSide, _, _, _ := t.PositionManager.HasOpenPosition()
		posSide = t.PositionManager.NormalizeSide(posSide)

		t.State.RLock()
		lastEntryAt := t.State.LastEntryAt
		lastEntryPrice := t.State.LastEntryPrice
		lastExitAt := t.State.LastExitAt
		lastExitDir := t.State.LastExitDir
		lastEntryDir := t.State.LastEntryDir
		t.State.RUnlock()

		if t.Config.Debug {
			t.Logger.Debug("Position before signal: exists=%t side=%s lastEntryDir=%s lastEntryAt=%s lastExitDir=%s lastExitAt=%s",
				exists, posSide, lastEntryDir, lastEntryAt, lastExitDir, lastExitAt)
		}

		entryForDelta := lastEntryPrice
		if entryForDelta == 0 {
			entryForDelta = t.PositionManager.GetLastEntryPrice()
		}
		if entryForDelta == 0 {
			entryForDelta = sig.ClosePrice
		}
		feeBuf := t.feeBuffer(entryForDelta)
		minDelta := feeBuf * priceBufMult

		graceOk := true
		if grace > 0 {
			addSignalRuntimeFilter(&sig, filterGracePeriod)
		}
		if exists && grace > 0 && !lastEntryAt.IsZero() && now.Sub(lastEntryAt) < grace {
			graceOk = false
			rejectSignalRuntime(&sig, filterGracePeriod, fmt.Sprintf("grace_period active sinceEntry=%s grace=%s", now.Sub(lastEntryAt).String(), grace))
			if t.Config.Debug {
				t.Logger.Debug("Filter graceOk=false dir=%s sinceEntry=%s grace=%s", dir, now.Sub(lastEntryAt).String(), grace)
			}
			t.logSignalRuntimeRejection(sig)
			continue
		}

		minDeltaOk := true
		if minDelta > 0 {
			addSignalRuntimeFilter(&sig, filterMinimumPriceDelta)
			if !exists && !lastExitAt.IsZero() && dir == lastExitDir && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				minDeltaOk = false
				rejectSignalRuntime(&sig, filterMinimumPriceDelta, fmt.Sprintf("minimum_price_delta failed delta=%.2f min=%.2f since last exit", math.Abs(sig.ClosePrice-entryForDelta), minDelta))
				if t.Config.Debug {
					t.Logger.Debug("Filter minDeltaOk=false dir=%s delta=%.2f min=%.2f since last exit", dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				t.logSignalRuntimeRejection(sig)
				continue
			}
			if exists && posSide != "" && dir != posSide && math.Abs(sig.ClosePrice-entryForDelta) < minDelta {
				minDeltaOk = false
				rejectSignalRuntime(&sig, filterMinimumPriceDelta, fmt.Sprintf("minimum_price_delta failed flip=%s->%s delta=%.2f min=%.2f", posSide, dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta))
				if t.Config.Debug {
					t.Logger.Debug("Filter minDeltaOk=false dir=%s flip=%s->%s delta=%.2f min=%.2f", dir, posSide, dir, math.Abs(sig.ClosePrice-entryForDelta), minDelta)
				}
				t.logSignalRuntimeRejection(sig)
				continue
			}
		}

		// Re-entry cooldown to avoid noisy flips
		cooldownOk := true
		cooldown := time.Duration(t.Config.ReentryCooldownSec) * time.Second
		if cooldown > 0 {
			addSignalRuntimeFilter(&sig, filterCooldown)
		}
		if cooldown > 0 && !sig.HighConf && !sig.Time.IsZero() && !lastExitAt.IsZero() && dir == lastExitDir {
			if sig.Time.Sub(lastExitAt) < cooldown {
				cooldownOk = false
				rejectSignalRuntime(&sig, filterCooldown, fmt.Sprintf("cooldown active sinceExit=%s cooldown=%s", sig.Time.Sub(lastExitAt).String(), cooldown))
				if t.Config.Debug {
					t.Logger.Debug("Filter cooldownOk=false dir=%s sinceExit=%s cooldown=%s", dir, sig.Time.Sub(lastExitAt).String(), cooldown)
				}
				t.logSignalRuntimeRejection(sig)
				continue
			}
		}

		duplicateOk := true
		addSignalRuntimeFilter(&sig, filterDuplicatePosition)
		// Allow repeated signals unless we already have an open position on that side
		if dir == lastDirection && !sig.HighConf {
			if exists && posSide == dir {
				duplicateOk = false
				rejectSignalRuntime(&sig, filterDuplicatePosition, fmt.Sprintf("duplicate signal while position already %s", posSide))
				if t.Config.Debug {
					t.Logger.Debug("Filter duplicateOk=false dir=%s posSide=%s lastDirection=%s", dir, posSide, lastDirection)
				}
				t.logSignalRuntimeRejection(sig)
				continue
			}
		}

		if t.Config.Debug {
			t.Logger.Debug("Filters ok: graceOk=%t minDeltaOk=%t cooldownOk=%t duplicateOk=%t", graceOk, minDeltaOk, cooldownOk, duplicateOk)
		}

		requiredStrength := t.runtimeStrengthThreshold(sig)
		projectedStrength := signalStrength[dir] + sig.Strength
		reversalRisk := t.reversalLongRuntimeRisk(sig, sig.ClosePrice)
		if isReversalLongSignal(sig) && !t.Config.ReversalLong.TradeEnabled {
			addSignalRuntimeFilter(&sig, filterReversalRiskProfile)
			rejectSignalRuntime(&sig, filterReversalRiskProfile, ReversalReasonTradeDisabled)
			t.logReversalLongRuntimeGate(sig, reversalLongRuntimeGateDecision{
				Strength:               projectedStrength,
				RequiredStrength:       requiredStrength,
				PassedStrength:         projectedStrength >= requiredStrength,
				PositionOpen:           exists,
				PendingOrder:           false,
				OrderbookRequired:      t.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
				LocalLow:               reversalRisk.LocalLow,
				StopBelowLocalLowBps:   t.Config.ReversalLong.StopBelowLocalLowBps,
				StopPrice:              reversalRisk.StopPrice,
				StopValid:              reversalRisk.StopValid,
				RiskProfileApplied:     false,
				PositionSizeMultiplier: reversalRisk.PositionSizeMultiplier,
				FinalDecision:          ReversalReasonTradeDisabled,
			})
			t.logSignalRuntimeRejection(sig)
			continue
		}
		if isReversalLongSignal(sig) && t.Config.ReversalLong.RequireStop && !reversalRisk.StopValid {
			addSignalRuntimeFilter(&sig, filterReversalRiskProfile)
			rejectSignalRuntime(&sig, filterReversalRiskProfile, reversalRisk.RejectReason)
			t.logReversalLongRuntimeGate(sig, reversalLongRuntimeGateDecision{
				Strength:               projectedStrength,
				RequiredStrength:       requiredStrength,
				PassedStrength:         projectedStrength >= requiredStrength,
				PositionOpen:           exists,
				PendingOrder:           false,
				OrderbookRequired:      t.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
				LocalLow:               reversalRisk.LocalLow,
				StopBelowLocalLowBps:   t.Config.ReversalLong.StopBelowLocalLowBps,
				StopPrice:              reversalRisk.StopPrice,
				StopValid:              reversalRisk.StopValid,
				RiskProfileApplied:     false,
				PositionSizeMultiplier: reversalRisk.PositionSizeMultiplier,
				FinalDecision:          reversalRisk.RejectReason,
			})
			t.logSignalRuntimeRejection(sig)
			continue
		}

		addSignalRuntimeFilter(&sig, filterOrderbookConfirmation)
		obResult := sig.Orderbook
		if isReversalLongSignal(sig) {
			evaluated := t.reversalLongRuntimeOrderbookResult()
			obResult = &evaluated
			sig.Orderbook = obResult
			if obResult.Passed {
				sig.Debug.Reasons = append(sig.Debug.Reasons, ReversalReasonRuntimeOBPassed)
			}
		} else if obResult == nil || !obResult.Evaluated || obResult.Side != dir {
			evaluated := t.EvaluateOrderbookStrength(dir, sig.ClosePrice)
			obResult = &evaluated
			sig.Orderbook = obResult
			sig.Debug.Reasons = append(sig.Debug.Reasons, fmt.Sprintf("%s:orderbook fallback_evaluated %s", dir, orderbookResultLog(obResult)))
		}
		if t.Config.Debug {
			t.Logger.Debug("Orderbook result for signal: %s", orderbookResultLog(obResult))
		}
		if !obResult.Passed {
			reason := fmt.Sprintf("%s:orderbook_rejected reason=%s", dir, obResult.Reason)
			if isReversalLongSignal(sig) {
				reason = ReversalReasonRuntimeOBHostile
				if obResult.Reason == ReversalReasonRuntimeOBUnavailable {
					reason = ReversalReasonRuntimeOBUnavailable
				} else if obResult.Reason != "" {
					reason = obResult.Reason
				}
			}
			rejectSignalRuntime(&sig, filterOrderbookConfirmation, reason)
			t.Logger.Info("Orderbook rejected %s signal: %s", dir, orderbookResultLog(obResult))
			if isReversalLongSignal(sig) {
				t.logReversalLongRuntimeGate(sig, reversalLongRuntimeGateDecision{
					Strength:               projectedStrength,
					RequiredStrength:       requiredStrength,
					PassedStrength:         projectedStrength >= requiredStrength,
					PositionOpen:           exists,
					PendingOrder:           false,
					OrderbookRequired:      t.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
					OrderbookAvailable:     obResult.Reason != ReversalReasonRuntimeOBUnavailable,
					OrderbookAllowed:       false,
					OrderbookRejectReason:  obResult.Reason,
					LocalLow:               reversalRisk.LocalLow,
					StopBelowLocalLowBps:   t.Config.ReversalLong.StopBelowLocalLowBps,
					StopPrice:              reversalRisk.StopPrice,
					StopValid:              reversalRisk.StopValid,
					RiskProfileApplied:     false,
					PositionSizeMultiplier: reversalRisk.PositionSizeMultiplier,
					FinalDecision:          reason,
				})
			}
			t.logSignalRuntimeRejection(sig)
			continue
		}

		strengthBeforeLong := signalStrength["LONG"]
		strengthBeforeShort := signalStrength["SHORT"]
		signalStrength[dir] += sig.Strength
		strengthAfterLong := signalStrength["LONG"]
		strengthAfterShort := signalStrength["SHORT"]
		if t.Config.Debug {
			t.Logger.Debug("Signal strength update: dir=%s before(L/S)=%d/%d +%d => after(L/S)=%d/%d",
				dir, strengthBeforeLong, strengthBeforeShort, sig.Strength, strengthAfterLong, strengthAfterShort)
		}

		threshold := requiredStrength
		addSignalRuntimeFilter(&sig, filterMinimumStrength)

		if t.Config.Debug {
			t.Logger.Debug("Signal threshold: dir=%s strength=%d threshold=%d highConf=%t", dir, signalStrength[dir], threshold, sig.HighConf)
		}

		if signalStrength[dir] < threshold {
			rejectSignalRuntime(&sig, filterMinimumStrength, fmt.Sprintf("minimum_strength failed accumulated=%d threshold=%d", signalStrength[dir], threshold))
			if t.Config.Debug {
				t.Logger.Debug("Filter minimumStrength=false dir=%s accumulated=%d threshold=%d", dir, signalStrength[dir], threshold)
			}
			if isReversalLongSignal(sig) {
				t.logReversalLongRuntimeGate(sig, reversalLongRuntimeGateDecision{
					Strength:               signalStrength[dir],
					RequiredStrength:       threshold,
					PassedStrength:         false,
					PositionOpen:           exists,
					PendingOrder:           false,
					OrderbookRequired:      t.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
					OrderbookAvailable:     obResult.Reason != ReversalReasonRuntimeOBUnavailable,
					OrderbookAllowed:       obResult.Passed,
					LocalLow:               reversalRisk.LocalLow,
					StopBelowLocalLowBps:   t.Config.ReversalLong.StopBelowLocalLowBps,
					StopPrice:              reversalRisk.StopPrice,
					StopValid:              reversalRisk.StopValid,
					RiskProfileApplied:     false,
					PositionSizeMultiplier: reversalRisk.PositionSizeMultiplier,
					FinalDecision:          filterMinimumStrength,
				})
			}
			t.logSignalRuntimeRejection(sig)
			continue
		}
		if isReversalLongSignal(sig) {
			t.logReversalLongRuntimeGate(sig, reversalLongRuntimeGateDecision{
				Strength:               signalStrength[dir],
				RequiredStrength:       threshold,
				PassedStrength:         true,
				PositionOpen:           exists,
				PendingOrder:           false,
				OrderbookRequired:      t.Config.ReversalLong.RequireSoftOrderbookAtRuntime,
				OrderbookAvailable:     obResult.Reason != ReversalReasonRuntimeOBUnavailable,
				OrderbookAllowed:       obResult.Passed,
				LocalLow:               reversalRisk.LocalLow,
				StopBelowLocalLowBps:   t.Config.ReversalLong.StopBelowLocalLowBps,
				StopPrice:              reversalRisk.StopPrice,
				StopValid:              reversalRisk.StopValid,
				RiskProfileApplied:     true,
				PositionSizeMultiplier: reversalRisk.PositionSizeMultiplier,
				FinalDecision:          ReversalReasonRiskProfileApplied,
			})
		}

		t.updateLastSignalSnapshot(sig)

		action := ""
		if dir == "LONG" {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed LONG signal: strength=%d (threshold=%d) highConf=%t", signalStrength["LONG"], threshold, sig.HighConf)
			}
			action = t.executeConfirmedSignal(sig, dir, reversalCloseKeys)
			if action != "" {
				lastDirection = "LONG"
				t.resetSignalStrength(&signalStrength)
			}
		} else if dir == "SHORT" {
			if t.Config.Debug {
				t.Logger.Debug("Confirmed SHORT signal: strength=%d (threshold=%d) highConf=%t", signalStrength["SHORT"], threshold, sig.HighConf)
			}
			action = t.executeConfirmedSignal(sig, dir, reversalCloseKeys)
			if action != "" {
				lastDirection = "SHORT"
				t.resetSignalStrength(&signalStrength)
			}
		}

		if action != "" && t.Config.Debug {
			existsAfter, sideAfter, _, _, _ := t.PositionManager.HasOpenPosition()
			t.Logger.Debug("Position after %s signal: exists=%t side=%s", action, existsAfter, t.PositionManager.NormalizeSide(sideAfter))
		}
	}
}

func (t *Trader) resetSignalStrength(m *map[string]int) {
	for k := range *m {
		delete(*m, k)
	}
}

// SyncPositionRealTime implements trailing stop logic
func (t *Trader) SyncPositionRealTime() {
	t.Logger.Info("Starting trailing stop logic...")
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var lastTPSLAttempt time.Time

	for range ticker.C {
		// Current position
		exists, side, qty, tp, sl := t.PositionManager.HasOpenPosition()
		if !exists {
			continue
		}
		entry := t.PositionManager.GetLastEntryPrice()
		if entry == 0 {
			continue
		}
		if tp <= 0 || sl <= 0 {
			if !lastTPSLAttempt.IsZero() && time.Since(lastTPSLAttempt) < 10*time.Second {
				continue
			}
			lastTPSLAttempt = time.Now()
			tpNew, slNew, diag := t.calculateInitialTPSLWithDiagnostics(entry, side, tpslDiagnosticContext{
				Symbol: t.Config.Symbol,
				Reason: "missing_tpsl",
				Qty:    qty,
				Log:    true,
			})
			t.Logger.Info("Missing TP/SL detected for %s position, setting TP %.2f SL %.2f", side, tpNew, slNew)
			if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
				t.Logger.Error("Error setting missing TP/SL: %v", err)
			} else {
				t.Logger.Info("TP/SL placed ▶ TP %.2f  SL %.2f", tpNew, slNew)
			}
			continue
		}

		// Current price
		var price float64
		if side == "LONG" {
			price = t.PositionManager.GetLastBidPrice()
		} else {
			price = t.PositionManager.GetLastAskPrice()
		}
		if price == 0 {
			continue
		}

		tick := t.State.Instr.TickSize
		if tick <= 0 {
			tick = 0.1
		}

		// Progress toward TP
		var dist, prog float64
		if side == "LONG" {
			dist = tp - entry
			prog = (price - entry) / dist
		} else {
			dist = entry - tp
			prog = (entry - price) / dist
		}
		if prog <= 0 || dist == 0 {
			continue
		}

		feeBuf := t.feeBuffer(entry)
		risk := math.Abs(entry - sl)
		if risk == 0 && dist != 0 {
			risk = math.Abs(dist) / 2 // fall back to 1R = half the TP distance
		}

		minTightenProg := t.Config.PartialTakeProfitProgress
		allowTighten := minTightenProg <= 0 || prog >= minTightenProg

		// Move SL to breakeven only when price has covered fees
		if allowTighten && prog >= t.Config.BreakevenProgress {
			lockStep := risk * 0.2 // lock 0.2R once in profit
			if side == "LONG" && price-entry >= feeBuf {
				target := math.Max(entry+feeBuf, entry+lockStep)
				if target > price-tick {
					target = price - tick
				}
				if target > sl {
					diag := t.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
						Symbol: t.Config.Symbol,
						Reason: "breakeven_lock_long",
						Qty:    qty,
						Log:    true,
					}, side, qty, entry, tp, target)
					t.logTPSLDiagnostics(diag)
					if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
						t.Logger.Error("Breakeven SL update error: %v", err)
					} else {
						t.Logger.Info("Moved SL to lock %.2f for LONG", target)
						sl = target
					}
				}
			}
			if side == "SHORT" && entry-price >= feeBuf {
				target := math.Min(entry-feeBuf, entry-lockStep)
				if target < price+tick {
					target = price + tick
				}
				if sl == 0 || target < sl {
					diag := t.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
						Symbol: t.Config.Symbol,
						Reason: "breakeven_lock_short",
						Qty:    qty,
						Log:    true,
					}, side, qty, entry, tp, target)
					t.logTPSLDiagnostics(diag)
					if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
						t.Logger.Error("Breakeven SL update error: %v", err)
					} else {
						t.Logger.Info("Moved SL to lock %.2f for SHORT", target)
						sl = target
					}
				}
			}
		}

		// Partial exit
		t.State.RLock()
		partialDone := t.State.PartialTPDone
		t.State.RUnlock()
		if !partialDone && prog >= t.Config.PartialTakeProfitProgress {
			partialQty := qty * t.Config.PartialTakeProfitRatio
			if partialQty > 0 {
				reduceSide := "Sell"
				if side == "SHORT" {
					reduceSide = "Buy"
				}
				if err := t.OrderManager.PlaceOrderMarket(reduceSide, partialQty, true); err != nil {
					t.Logger.Error("Partial TP order failed: %v", err)
				} else {
					t.State.Lock()
					t.State.PartialTPDone = true
					t.State.Unlock()
					t.Logger.Info("Partial TP executed: %.2f (%s), progress=%.0f%%", partialQty, reduceSide, prog*100)

					// After partial TP, move SL to breakeven (entry) for the remainder.
					be := entry
					if side == "LONG" && be > price-tick {
						be = price - tick
					}
					if side == "SHORT" && be < price+tick {
						be = price + tick
					}
					be = t.roundSL(entry, be, tick, side)
					diag := t.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
						Symbol: t.Config.Symbol,
						Reason: "partial_tp_breakeven",
						Qty:    qty - partialQty,
						Log:    true,
					}, side, qty-partialQty, entry, tp, be)
					t.logTPSLDiagnostics(diag)
					if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
						t.Logger.Error("Breakeven SL update after partial error: %v", err)
					} else {
						t.Logger.Info("Moved SL to breakeven %.2f after partial TP", be)
						sl = be
					}
				}
			}
		}

		if t.Config.DisableTrailing {
			continue
		}
		if !allowTighten {
			continue
		}

		// Trail only after a defined amount of progress to avoid churn in chop
		trailStart := math.Max(t.Config.TrailActivation, t.Config.BreakevenProgress)
		var realizedR float64
		if risk > 0 {
			if side == "LONG" {
				realizedR = (price - entry) / risk
			} else {
				realizedR = (entry - price) / risk
			}
		}
		if prog < trailStart || realizedR < t.Config.MinTrailRR {
			continue
		}

		ohlcvSnapshot := t.State.GetOhlcvSnapshot()
		atr := indicators.CalculateATR(ohlcvSnapshot.Highs, ohlcvSnapshot.Lows, ohlcvSnapshot.Closes, 14)

		baseBuffer := price * t.Config.TrailPerc
		if atr > 0 {
			baseBuffer = math.Max(baseBuffer, atr*0.5)
		}
		tightBuffer := price * t.Config.TrailTightPerc
		if atr > 0 {
			tightBuffer = math.Max(tightBuffer, atr*0.25)
		}

		var targetSL float64
		if side == "LONG" {
			targetSL = price - baseBuffer
			if risk > 0 {
				targetSL = math.Max(targetSL, entry+risk*0.25) // lock at least 0.25R when trailing kicks in
			}
			targetSL = math.Max(targetSL, entry+feeBuf)
			if prog >= 0.9 {
				targetSL = math.Max(targetSL, price-tightBuffer)
			}
			targetSL = math.Min(targetSL, price-tick)
			if targetSL <= sl {
				continue
			}
		} else {
			targetSL = price + baseBuffer
			if risk > 0 {
				targetSL = math.Min(targetSL, entry-risk*0.25) // move stop below entry to secure profit
			}
			targetSL = math.Min(targetSL, entry-feeBuf)
			if prog >= 0.9 {
				targetSL = math.Min(targetSL, price+tightBuffer)
			}
			targetSL = math.Max(targetSL, price+tick)
			if targetSL >= sl {
				continue
			}
		}

		t.Logger.Info("Trailing stop: updating SL from %.2f to %.2f (%.0f%% way to TP)", sl, targetSL, prog*100)
		diag := t.buildTPSLDiagnosticsForUpdate(tpslDiagnosticContext{
			Symbol: t.Config.Symbol,
			Reason: "trailing_stop_update",
			Qty:    qty,
			Log:    true,
		}, side, qty, entry, tp, targetSL)
		t.logTPSLDiagnostics(diag)
		if err := t.updatePositionTPSLWithDiagnostics(diag); err != nil {
			t.Logger.Error("Trailing SL update error: %v", err)
		} else {
			t.Logger.Info("SL → %.2f (%.0f%% way to TP)", targetSL, prog*100)
		}
	}
}

// OnClosedCandle processes a closed candle
func (t *Trader) OnClosedCandle(kline models.KlineData) {
	closePrice := kline.CloseFloat()
	high := kline.HighFloat()
	low := kline.LowFloat()
	vol, _ := strconv.ParseFloat(kline.Volume, 64)
	t.Logger.Debug("OnClosedCandle received: o/h/l/c=%s/%s/%s/%s vol=%s", kline.Open, kline.High, kline.Low, kline.Close, kline.Volume)
	if high == 0 {
		high = closePrice
	}
	if low == 0 {
		low = closePrice
	}

	maxLen := t.Config.SmaLen * 100
	snapshot := t.State.AppendClosedCandle(closePrice, high, low, vol, maxLen)
	t.Logger.Debug("Candle appended: closes=%d highs=%d lows=%d volumes=%d candleSeq=%d",
		len(snapshot.Closes), len(snapshot.Highs), len(snapshot.Lows), len(snapshot.Volumes), snapshot.CandleSeq)

	if t.Config.Debug {
		t.Logger.Debug("Added price: %.2f, length closes: %d", closePrice, len(snapshot.Closes))
	}

	// Log indicator values after adding new candle
	if len(snapshot.Closes) >= t.Config.SmaLen {
		t.logIndicatorSnapshot(t.buildIndicatorSnapshot(snapshot), t.State.GetMarketRegime())
	}
}

// LogSignalStats logs signal statistics
func (t *Trader) LogSignalStats() {
	t.State.SignalStats.Lock()
	total := t.State.SignalStats.Total
	correct := t.State.SignalStats.Correct
	t.State.SignalStats.Unlock()

	if total == 0 {
		t.Logger.Info("Signal stats: No signals")
		return
	}

	accuracy := float64(correct) / float64(total) * 100
	t.Logger.Info("Signal stats: %d/%d (%.1f%%)", correct, total, accuracy)
}
