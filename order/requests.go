package order

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/internal/operations"
	"verbose-fortnight/internal/prices"
)

var ErrOrderUncertain = errors.New("order result unknown; reconciliation required")

// UncertainOrderError retains the identity required to recover without resubmission.
type UncertainOrderError struct {
	OrderID, OrderLinkID string
	Cause                error
}

func (e *UncertainOrderError) Error() string {
	return fmt.Sprintf("%v: orderId=%s orderLinkId=%s: %v", ErrOrderUncertain, e.OrderID, e.OrderLinkID, e.Cause)
}
func (e *UncertainOrderError) Unwrap() error        { return e.Cause }
func (e *UncertainOrderError) Is(target error) bool { return target == ErrOrderUncertain }

func (om *OrderManager) reconciliationTimeout() time.Duration {
	if om.Config.OrderReconcileTimeoutMs > 0 {
		return time.Duration(om.Config.OrderReconcileTimeoutMs) * time.Millisecond
	}
	return 3 * time.Second
}
func (om *OrderManager) QueryOrder(ctx context.Context, id, link string) (api.OrderState, error) {
	return om.APIClient.GetOrderState(ctx, om.Config.Symbol, id, link)
}
func (om *OrderManager) CancelOrder(ctx context.Context, id, link string) error {
	return om.APIClient.CancelOrder(ctx, om.Config.Symbol, id, link)
}

func (om *OrderManager) placeOrder(side string, qty, price float64, reduceOnly bool, link, kind, tif string, stop, priceUp bool) (string, error) {
	if side != "Buy" && side != "Sell" {
		return "", fmt.Errorf("invalid order side %q", side)
	}
	if (kind == "Limit" || stop) && !positiveFinite(price) {
		return "", fmt.Errorf("invalid price")
	}
	om.State.RLock()
	instr := om.State.Instr
	owner := operations.Owner{Lifecycle: om.State.ActiveTradeID, Side: om.State.PosSide}
	om.State.RUnlock()
	formatted := om.FormatQty(qty, instr.QtyStep)
	if formatted == "" {
		return "", fmt.Errorf("invalid quantity or instrument qtyStep")
	}
	amount, err := strconv.ParseFloat(formatted, 64)
	if err != nil || !positiveFinite(amount) || amount < instr.MinQty || (kind == "Limit" && amount*price < instr.MinNotional) {
		return "", fmt.Errorf("quantity below instrument minimum or invalid")
	}
	link = strings.TrimSpace(link)
	if link == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", err
		}
		link = "ord-" + hex.EncodeToString(nonce[:])
	}
	if len(link) > 36 {
		return "", fmt.Errorf("orderLinkId exceeds 36 characters")
	}
	body := map[string]any{"category": "linear", "symbol": om.Config.Symbol, "side": side, "orderType": kind, "qty": formatted, "timeInForce": tif, "positionIdx": 0, "orderLinkId": link}
	if reduceOnly {
		body["reduceOnly"] = true
	}
	if kind == "Limit" || stop {
		priceText, aligned, err := prices.Quantize(price, instr.TickSize, priceUp)
		if err != nil {
			return "", err
		}
		if reduceOnly && tif == "GTC" {
			positionSide := "LONG"
			if side == "Buy" {
				positionSide = "SHORT"
			}
			row, observationErr := om.APIClient.ObservePosition(om.APIClient.Context, om.Config.Symbol, 0, positionSide)
			if observationErr != nil {
				return "", observationErr
			}
			entry, parseErr := strconv.ParseFloat(row.AvgPrice, 64)
			if parseErr != nil {
				return "", parseErr
			}
			isTP := priceUp == (side == "Buy")
			priceText, aligned, err = prices.OneStop(positionSide, entry, price, instr.TickSize, isTP, false)
			if err != nil {
				return "", err
			}
		}
		if kind == "Limit" && amount*aligned < instr.MinNotional {
			return "", fmt.Errorf("notional below instrument minimum after price quantization")
		}
		if kind == "Limit" {
			body["price"] = priceText
		}
		if stop {
			body["stopLossPrice"] = priceText
		}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	om.State.RLock()
	sameLifecycle := om.State.ActiveTradeID == owner.Lifecycle
	om.State.RUnlock()
	if om.APIClient.Operations != nil && !sameLifecycle {
		return "", fmt.Errorf("order lifecycle changed before dispatch")
	}
	ctxOwner := operations.WithOwner(om.APIClient.Context, owner)
	reply, err := om.APIClient.DoJSON(ctxOwner, "POST", "/v5/order/create", "", raw)
	if err == nil {
		var r struct {
			Result struct {
				ID string `json:"orderId"`
			} `json:"result"`
		}
		err = json.Unmarshal(reply, &r)
		if err == nil && r.Result.ID != "" {
			om.recordOrder(r.Result.ID)
			return r.Result.ID, nil
		}
		err = fmt.Errorf("order/create response missing orderId")
	} else {
		var rejected *api.Rejection
		var request *api.RequestError
		if (errors.As(err, &rejected) && !rejected.OrderResultUnknown()) || (errors.As(err, &request) && !request.Sent) {
			return "", err
		}
	}
	// An unsuccessful response can hide an accepted order. Query by the same link;
	// absence in an eventually consistent cache never authorizes another create.
	ctx := om.APIClient.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, om.reconciliationTimeout())
	defer cancel()
	for {
		snapshot, queryErr := om.QueryOrder(ctx, "", link)
		if queryErr == nil && snapshot.Qty == amount {
			if snapshot.Status == "Rejected" {
				return "", fmt.Errorf("order %s rejected", snapshot.OrderID)
			}
			om.recordOrder(snapshot.OrderID)
			return snapshot.OrderID, nil
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", &UncertainOrderError{OrderLinkID: link, Cause: err}
		case <-timer.C:
		}
	}
}

func (om *OrderManager) recordOrder(id string) {
	om.State.Lock()
	om.State.LastOrderID = id
	om.State.Unlock()
	if om.Logger != nil {
		om.Logger.Info("Order acknowledged: orderId=%s (execution not yet confirmed)", id)
	}
}
