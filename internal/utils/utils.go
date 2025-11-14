package utils

import (
	"math"
	"strconv"
)

// FormatPrice rounds a price to the specified tick size
func FormatPrice(price, tickSize float64) float64 {
	if tickSize > 0 {
		return math.Round(price/tickSize) * tickSize
	}
	return price
}

// FormatPriceToString formats a price to string with tick size precision
func FormatPriceToString(price, tickSize float64) string {
	if tickSize == 0 {
		return "0.00"
	}

	decimals := 0
	tempTickSize := tickSize
	for tempTickSize < 1 {
		tempTickSize *= 10
		decimals++
	}

	return strconv.FormatFloat(price, 'f', decimals, 64)
}

// FormatQuantity rounds a quantity to the specified step size
func FormatQuantity(qty, stepSize float64) float64 {
	if stepSize > 0 {
		decimals := 0
		tempStep := stepSize
		for tempStep < 1 {
			tempStep *= 10
			decimals++
		}
		return math.Round(qty/stepSize) * stepSize
	}
	return qty
}

// FormatQuantityToString formats a quantity to string with step size precision
func FormatQuantityToString(qty, stepSize float64) string {
	if stepSize == 0 {
		return "0.00"
	}

	decimals := 0
	tempStep := stepSize
	for tempStep < 1 {
		tempStep *= 10
		decimals++
	}

	return strconv.FormatFloat(qty, 'f', decimals, 64)
}

// NormalizeSide normalizes position side to LONG/SHORT
func NormalizeSide(side string) string {
	switch side {
	case "BUY", "LONG":
		return "LONG"
	case "SELL", "SHORT":
		return "SHORT"
	default:
		return ""
	}
}