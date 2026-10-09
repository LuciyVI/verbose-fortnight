package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/url"
	"strconv"
	"time"

	"verbose-fortnight/internal/operations"
	"verbose-fortnight/internal/prices"
)

type RecoveryExecution struct {
	ID      string `json:"execId"`
	OrderID string `json:"orderId"`
	Link    string `json:"orderLinkId"`
	Side    string `json:"side"`
	Qty     string `json:"execQty"`
}
type RecoveryOrder struct {
	ID     string `json:"orderId"`
	Link   string `json:"orderLinkId"`
	Symbol string `json:"symbol"`
	Side   string `json:"side"`
	Status string `json:"orderStatus"`
	Qty    string `json:"qty"`
	Cum    string `json:"cumExecQty"`
	Leaves string `json:"leavesQty"`
}
type RecoveryObservation struct {
	At           time.Time           `json:"at"`
	Position     Position            `json:"position"`
	Lifecycle    string              `json:"lifecycle,omitempty"`
	Executions   []RecoveryExecution `json:"executions"`
	ActiveOrders []RecoveryOrder     `json:"activeOrders"`
}

// recoveryPages requires complete pagination. Missing list, malformed fields,
// repeated cursors and the bounded pagination cap all fail closed.
func (c *RESTClient) recoveryPages(ctx context.Context, path string, q url.Values) ([]json.RawMessage, error) {
	var out []json.RawMessage
	seen := map[string]bool{}
	q.Set("category", "linear")
	q.Set("symbol", c.Config.Symbol)
	q.Set("limit", "200")
	for page := 0; page < 100; page++ {
		raw, err := c.DoJSON(ctx, "GET", path, q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		var r struct {
			Result *struct {
				List   *[]json.RawMessage `json:"list"`
				Cursor string             `json:"nextPageCursor"`
			} `json:"result"`
		}
		if err = json.Unmarshal(raw, &r); err != nil || r.Result == nil || r.Result.List == nil {
			return nil, fmt.Errorf("incomplete recovery response %s", path)
		}
		out = append(out, (*r.Result.List)...)
		if len(out) > 20000 {
			return nil, fmt.Errorf("recovery page budget exceeded")
		}
		if r.Result.Cursor == "" {
			return out, nil
		}
		if seen[r.Result.Cursor] {
			return nil, fmt.Errorf("repeated recovery cursor")
		}
		seen[r.Result.Cursor] = true
		q.Set("cursor", r.Result.Cursor)
	}
	return nil, fmt.Errorf("incomplete recovery pagination")
}
func recoveryNumber(text string, positive bool) (float64, error) {
	v, e := strconv.ParseFloat(text, 64)
	if e != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || (positive && v == 0) {
		return 0, fmt.Errorf("invalid recovery number %q", text)
	}
	return v, nil
}
func (c *RESTClient) recoveryOrders(ctx context.Context) ([]RecoveryOrder, error) {
	rows, err := c.recoveryPages(ctx, "/v5/order/realtime", url.Values{"openOnly": {"0"}})
	if err != nil {
		return nil, err
	}
	var out []RecoveryOrder
	seen := map[string]RecoveryOrder{}
	for _, raw := range rows {
		var r RecoveryOrder
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		q, e1 := recoveryNumber(r.Qty, true)
		cum, e2 := recoveryNumber(r.Cum, false)
		leaves, e3 := recoveryNumber(r.Leaves, false)
		if r.ID == "" || r.Symbol != c.Config.Symbol || prices.NormalizeSide(r.Side) == "" || e1 != nil || e2 != nil || e3 != nil || cum > q || leaves > q || cum+leaves > q+1e-10 {
			return nil, fmt.Errorf("invalid recovery order %s", r.ID)
		}
		if old, ok := seen[r.ID]; ok {
			if old != r {
				return nil, fmt.Errorf("conflicting duplicate order")
			}
			continue
		}
		seen[r.ID] = r
		if (OrderState{Status: r.Status}).Terminal() {
			continue
		}
		switch r.Status {
		case "New", "PartiallyFilled", "Untriggered", "Triggered":
		default:
			return nil, fmt.Errorf("unknown order status %q", r.Status)
		}
		out = append(out, r)
	}
	return out, nil
}
func (c *RESTClient) recoveryExecutions(ctx context.Context, orderID string) ([]RecoveryExecution, error) {
	q := url.Values{}
	if orderID != "" {
		q.Set("orderId", orderID)
	}
	rows, err := c.recoveryPages(ctx, "/v5/execution/list", q)
	if err != nil {
		return nil, err
	}
	var out []RecoveryExecution
	seen := map[string]RecoveryExecution{}
	for _, raw := range rows {
		var r RecoveryExecution
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if _, err = recoveryNumber(r.Qty, true); err != nil || r.ID == "" || r.OrderID == "" || prices.NormalizeSide(r.Side) == "" || (orderID != "" && r.OrderID != orderID) {
			return nil, fmt.Errorf("invalid recovery execution")
		}
		if old, ok := seen[r.ID]; ok {
			if old != r {
				return nil, fmt.Errorf("conflicting duplicate execution")
			}
			continue
		}
		seen[r.ID] = r
		out = append(out, r)
	}
	return out, nil
}
func checkRecoveryFills(rows []RecoveryExecution, s OrderState) error {
	total := new(big.Rat)
	for _, e := range rows {
		q, ok := new(big.Rat).SetString(e.Qty)
		if !ok {
			return fmt.Errorf("invalid decimal execution")
		}
		total.Add(total, q)
	}
	want, _ := new(big.Rat).SetString(strconv.FormatFloat(s.CumExecQty, 'f', -1, 64))
	if total.Cmp(want) != 0 {
		return fmt.Errorf("executions disagree with cumulative fill for %s: total=%s cumulative=%g", s.OrderID, total.RatString(), s.CumExecQty)
	}
	return nil
}

// ReconcileStartup never sends mutations. Absence from an eventually consistent
// exchange cache cannot resolve a sending/unknown local intent.
func (c *RESTClient) ReconcileStartup(ctx context.Context) (RecoveryObservation, error) {
	var obs RecoveryObservation
	obs.At = time.Now().UTC()
	if c.Operations == nil {
		return obs, fmt.Errorf("durable operation store is required")
	}
	attempt := c.Operations.BeginRecovery()
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := c.Config.StartupReconcileTimeoutMs
	if timeout <= 0 {
		timeout = 30000
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
	defer cancel()
	orders, err := c.recoveryOrders(ctx)
	if err != nil {
		return obs, fmt.Errorf("startup active orders: %w", err)
	}
	obs.ActiveOrders = orders
	for _, o := range orders {
		if err = c.Operations.Discover(o.ID, o.Link, o.Symbol, o.Side, o.Qty); err != nil {
			return obs, err
		}
	}
	obs.Executions, err = c.recoveryExecutions(ctx, "")
	if err != nil {
		return obs, fmt.Errorf("startup executions: %w", err)
	}
	positions, err := c.GetPositionListContext(ctx, c.Config.Symbol)
	if err != nil {
		return obs, fmt.Errorf("startup position/stops: %w", err)
	}
	open := false
	for _, p := range positions {
		size, _ := strconv.ParseFloat(p.Size, 64)
		if size <= 0 {
			continue
		}
		if p.Symbol != c.Config.Symbol || p.PositionIdx != 0 || open {
			return obs, fmt.Errorf("unsupported/ambiguous live position")
		}
		open = true
		obs.Position = p
	}
	var unresolved error
	for _, r := range c.Operations.Snapshot().Records {
		if r.Symbol != c.Config.Symbol {
			return obs, fmt.Errorf("operation belongs to another symbol")
		}
		if r.Stage == "rejected" {
			continue
		}
		if r.Stage == "resolved" && (r.Kind != "/v5/order/create" && r.Kind != "external/order" || r.Outcome == "terminal order and executions reconciled" || r.Outcome == "durably never sent") {
			continue
		}
		if r.Stage == "intent" {
			if err = c.Operations.Resolve(r, "durably never sent"); err != nil {
				return obs, err
			}
			continue
		}
		if r.Kind == "/v5/position/trading-stop" {
			continue
		}
		if r.Kind == "/v5/order/cancel-all" {
			if len(orders) == 0 {
				err = c.Operations.Resolve(r, "no active orders in complete snapshot")
			} else {
				unresolved = fmt.Errorf("cancel-all has active remainder")
			}
			if err != nil {
				return obs, err
			}
			continue
		}
		s, queryErr := c.getOrderState(ctx, r.Symbol, r.OrderID, r.Link, false)
		if queryErr != nil {
			unresolved = fmt.Errorf("operation %s remains unknown: %w", r.ID, queryErr)
			continue
		}
		if r.Kind == "/v5/order/create" && (s.OrderLinkID != r.Link || s.Qty != operations.Quantity(r)) {
			return obs, fmt.Errorf("operation identity/quantity mismatch")
		}
		fills, queryErr := c.recoveryExecutions(ctx, s.OrderID)
		if queryErr != nil {
			return obs, queryErr
		}
		if err = checkRecoveryFills(fills, s); err != nil {
			return obs, err
		}
		global := map[string]RecoveryExecution{}
		for _, e := range obs.Executions {
			global[e.ID] = e
		}
		for _, fill := range fills {
			if saved, ok := global[fill.ID]; !ok || saved != fill {
				return obs, fmt.Errorf("per-order executions absent/conflicting in global recovery snapshot")
			}
		}
		if s.Status == "Filled" && s.CumExecQty != s.Qty {
			return obs, fmt.Errorf("Filled snapshot has incomplete quantity")
		}
		if !s.Terminal() {
			unresolved = fmt.Errorf("operation %s has executable remainder", r.ID)
			continue
		}
		r.OrderID, r.Status, r.CumExecQty = s.OrderID, s.Status, s.CumExecQty
		if err = c.Operations.Resolve(r, "terminal order and executions reconciled"); err != nil {
			return obs, err
		}
	}
	if len(orders) > 0 {
		unresolved = fmt.Errorf("%d active orders must become terminal before trading", len(orders))
	}
	if open {
		entry, e1 := recoveryNumber(obs.Position.AvgPrice, true)
		tp, e2 := recoveryNumber(obs.Position.TakeProfit, true)
		sl, e3 := recoveryNumber(obs.Position.StopLoss, true)
		if e1 != nil || e2 != nil || e3 != nil || prices.ValidateStops(obs.Position.Side, entry, tp, sl, true) != nil {
			unresolved = fmt.Errorf("live position lacks valid protective stops")
		}
		// Recover the latest verified opening owner. Unknown/manual position ownership
		// requires operator reconciliation (usually flattening), never guessing.
		var latest uint64
		for _, r := range c.Operations.Snapshot().Records {
			if r.Kind != "/v5/order/create" || r.CumExecQty <= 0 {
				continue
			}
			var p map[string]any
			json.Unmarshal(r.Payload, &p)
			reduce, _ := p["reduceOnly"].(bool)
			side, _ := p["side"].(string)
			if !reduce && prices.NormalizeSide(side) == prices.NormalizeSide(obs.Position.Side) && r.Generation > latest {
				latest = r.Generation
				obs.Lifecycle = r.Owner.Lifecycle
			}
		}
		if obs.Lifecycle == "" {
			unresolved = fmt.Errorf("live position lifecycle cannot be recovered")
		}
	}
	// Position is checked against the last successful audit plus newly observed
	// executions. A missing boundary/history or external change fails closed.
	previous := RecoveryObservation{}
	savedAudit := c.Operations.Snapshot().Audit
	if len(savedAudit) > 0 {
		if err = json.Unmarshal(savedAudit, &previous); err != nil {
			return obs, fmt.Errorf("invalid saved recovery audit")
		}
	}
	net := new(big.Rat)
	known := map[string]RecoveryExecution{}
	if previous.At.IsZero() == false {
		size, _ := new(big.Rat).SetString(previous.Position.Size)
		if size != nil {
			net.Set(size)
			if prices.NormalizeSide(previous.Position.Side) == "SHORT" {
				net.Neg(net)
			}
		}
		for _, e := range previous.Executions {
			known[e.ID] = e
		}
	}
	for _, e := range obs.Executions {
		if old, ok := known[e.ID]; ok {
			if old != e {
				return obs, fmt.Errorf("execution changed since previous audit")
			}
			delete(known, e.ID)
			continue
		}
		if open {
			owned := false
			for _, r := range c.Operations.Snapshot().Records {
				if r.Kind == "/v5/order/create" && r.OrderID == e.OrderID && r.CumExecQty > 0 {
					owned = true
					break
				}
			}
			if !owned {
				unresolved = fmt.Errorf("unowned execution affects live position: %s", e.ID)
			}
		}
		q, _ := new(big.Rat).SetString(e.Qty)
		if prices.NormalizeSide(e.Side) == "SHORT" {
			q.Neg(q)
		}
		net.Add(net, q)
	}
	if len(known) > 0 {
		return obs, fmt.Errorf("previous execution boundary missing; history may be truncated")
	}
	actual := new(big.Rat)
	if open {
		actual.SetString(obs.Position.Size)
		if prices.NormalizeSide(obs.Position.Side) == "SHORT" {
			actual.Neg(actual)
		}
	}
	if actual.Cmp(net) != 0 {
		unresolved = fmt.Errorf("position differs from reconciled executions: expected=%s actual=%s", net.RatString(), actual.RatString())
	}
	// Resolve stop results only after order ownership has been recovered.
	for _, r := range c.Operations.Snapshot().Records {
		if r.Kind != "/v5/position/trading-stop" || r.Stage == "resolved" || r.Stage == "rejected" {
			continue
		}
		var p map[string]any
		json.Unmarshal(r.Payload, &p)
		match := !open
		if open && r.Owner.Lifecycle == obs.Lifecycle && prices.NormalizeSide(r.Owner.Side) == prices.NormalizeSide(obs.Position.Side) {
			match = true
			for key, actual := range map[string]string{"takeProfit": obs.Position.TakeProfit, "stopLoss": obs.Position.StopLoss} {
				if wanted, ok := p[key].(string); ok {
					a, e1 := strconv.ParseFloat(actual, 64)
					b, e2 := strconv.ParseFloat(wanted, 64)
					if e1 != nil || e2 != nil || a != b {
						match = false
					}
				}
			}
		}
		if !match || unresolved != nil {
			unresolved = fmt.Errorf("unresolved stop operation %s belongs to another lifecycle or levels", r.ID)
			continue
		}
		if err = c.Operations.Resolve(r, "observed current position/stops"); err != nil {
			return obs, err
		}
	}
	audit, _ := json.Marshal(obs)
	if unresolved != nil {
		if err = c.Operations.SaveBlockedAudit(audit); err != nil {
			return obs, err
		}
		return obs, unresolved
	}
	if err = c.Operations.CompleteRecovery(attempt, c.Operations.Snapshot().Revision, audit); err != nil {
		return obs, err
	}
	return obs, nil
}
