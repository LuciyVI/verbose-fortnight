package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"verbose-fortnight/internal/operations"
)

var ErrOrderNotFound = errors.New("order not found; acceptance remains unknown")

// OrderState is an exchange snapshot for one order, never inferred from position size.
type OrderState struct {
	OrderID     string
	OrderLinkID string
	Status      string
	Qty         float64
	CumExecQty  float64
}

func (s OrderState) Terminal() bool {
	switch s.Status {
	case "Filled", "Cancelled", "Rejected", "Deactivated", "PartiallyFilledCanceled":
		return true
	}
	return false
}

// GetOrderState queries realtime, then history when the closed-order cache is empty.
func (c *RESTClient) GetOrderState(ctx context.Context, symbol, id, link string) (OrderState, error) {
	return c.getOrderState(ctx, symbol, id, link, true)
}
func (c *RESTClient) getOrderState(ctx context.Context, symbol, id, link string, observe bool) (OrderState, error) {
	var captured []operations.Record
	if observe && c.Operations != nil {
		captured = c.Operations.Snapshot().Records
	}
	q := url.Values{"category": {"linear"}, "symbol": {symbol}}
	if id != "" {
		q.Set("orderId", id)
	} else if link != "" {
		q.Set("orderLinkId", link)
	} else {
		return OrderState{}, fmt.Errorf("missing order identity")
	}
	for _, path := range []string{"/v5/order/realtime", "/v5/order/history"} {
		raw, err := c.DoJSON(ctx, "GET", path, q.Encode(), nil)
		if err != nil {
			return OrderState{}, err
		}
		var r struct {
			Result struct {
				List []struct {
					ID     string `json:"orderId"`
					Link   string `json:"orderLinkId"`
					Status string `json:"orderStatus"`
					Qty    string `json:"qty"`
					Cum    string `json:"cumExecQty"`
				} `json:"list"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return OrderState{}, err
		}
		for _, row := range r.Result.List {
			if (id != "" && row.ID != id) || (id == "" && row.Link != link) {
				continue
			}
			qty, e1 := strconv.ParseFloat(row.Qty, 64)
			cum, e2 := strconv.ParseFloat(row.Cum, 64)
			if row.ID == "" || row.Status == "" || e1 != nil || e2 != nil || math.IsNaN(qty) || math.IsInf(qty, 0) || math.IsNaN(cum) || math.IsInf(cum, 0) || qty <= 0 || cum < 0 || cum > qty {
				return OrderState{}, fmt.Errorf("invalid order state for %s/%s", id, link)
			}
			state := OrderState{OrderID: row.ID, OrderLinkID: row.Link, Status: row.Status, Qty: qty, CumExecQty: cum}
			if observe && c.Operations != nil {
				if err := c.Operations.ObserveCaptured(captured, row.ID, row.Link, row.Status, cum, state.Terminal()); err != nil {
					return OrderState{}, err
				}
			}
			return state, nil
		}
	}
	return OrderState{}, ErrOrderNotFound
}

func (c *RESTClient) CancelOrder(ctx context.Context, symbol, id, link string) error {
	body := map[string]any{"category": "linear", "symbol": symbol}
	if id != "" {
		body["orderId"] = id
	} else if link != "" {
		body["orderLinkId"] = link
	} else {
		return fmt.Errorf("missing order identity")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = c.DoJSON(ctx, "POST", "/v5/order/cancel", "", raw)
	return err
}
