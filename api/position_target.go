package api

import (
	"context"
	"fmt"
	"strconv"

	"verbose-fortnight/internal/prices"
)

// ObservePosition checks the exact one-way/hedge target and expected direction.
// It is an observation, not an atomic exchange-side condition on a later POST.
func (c *RESTClient) ObservePosition(ctx context.Context, symbol string, index int, side string) (Position, error) {
	rows, err := c.GetPositionListContext(ctx, symbol)
	if err != nil {
		return Position{}, err
	}
	expected := prices.NormalizeSide(side)
	for _, row := range rows {
		if row.Symbol != symbol || row.PositionIdx != index {
			continue
		}
		size, _ := strconv.ParseFloat(row.Size, 64)
		if size <= 0 {
			continue
		}
		actual := prices.NormalizeSide(row.Side)
		if actual == "" || (expected != "" && actual != expected) {
			return Position{}, fmt.Errorf("position direction changed: expected=%s observed=%s", expected, actual)
		}
		return row, nil
	}
	return Position{}, fmt.Errorf("no open position for %s positionIdx=%d", symbol, index)
}
