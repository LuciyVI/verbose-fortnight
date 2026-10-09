package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
)

// Rejection is an explicit exchange rejection, as opposed to an unknown result.
type Rejection struct {
	Code    int
	Message string
}

// OrderResultUnknown identifies timeout/server and duplicate-id responses that
// require identity lookup even though a JSON retCode was received.
func (e *Rejection) OrderResultUnknown() bool {
	switch e.Code {
	case 10000, 10016, 10014, 110072:
		return true
	}
	return false
}

func (e *Rejection) Error() string { return fmt.Sprintf("exchange retCode %d: %s", e.Code, e.Message) }

// RequestError records whether an unsuccessful request could have reached the exchange.
type RequestError struct {
	Err  error
	Sent bool
}

func (e *RequestError) Error() string { return e.Err.Error() }
func (e *RequestError) Unwrap() error { return e.Err }

func requestTimeout(cfg *config.Config) time.Duration {
	if cfg != nil && cfg.RESTTimeoutMs > 0 {
		return time.Duration(cfg.RESTTimeoutMs) * time.Millisecond
	}
	return 5 * time.Second
}

// DoJSON signs a single bounded request. It never retries mutations.
// HTTPClient is injectable; a request deadline also bounds clients with Timeout=0.
func (c *RESTClient) DoJSON(ctx context.Context, method, path, query string, payload []byte) ([]byte, error) {
	if method != http.MethodGet && c.Operations != nil {
		owner := operations.ContextOwner(ctx)
		if !operations.HasOwner(ctx) && c.OperationOwner != nil {
			owner = c.OperationOwner()
		}
		r, err := c.Operations.Prepare(path, payload, owner)
		if err != nil {
			return nil, &RequestError{Err: fmt.Errorf("persist mutation before sending: %w", err)}
		}
		if err = c.Operations.Sending(r); err != nil {
			return nil, &RequestError{Err: fmt.Errorf("persist sending barrier: %w", err)}
		}
		raw, requestErr := c.doJSON(ctx, method, path, query, payload)
		var rejected *Rejection
		var request *RequestError
		definite := (errors.As(requestErr, &rejected) && !rejected.OrderResultUnknown()) || (errors.As(requestErr, &request) && !request.Sent)
		if err = c.Operations.Finish(r, raw, requestErr, definite); err != nil {
			return nil, &RequestError{Err: fmt.Errorf("persist mutation result: %w", err), Sent: true}
		}
		return raw, requestErr
	}
	return c.doJSON(ctx, method, path, query, payload)
}

func (c *RESTClient) doJSON(ctx context.Context, method, path, query string, payload []byte) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout(c.Config))
	defer cancel()
	target := c.Config.DemoRESTHost + path
	if query != "" {
		target += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(payload))
	if err != nil {
		return nil, &RequestError{Err: fmt.Errorf("create request: %w", err)}
	}
	ts := fmt.Sprint(time.Now().UnixMilli())
	signed := query
	if method != http.MethodGet {
		signed = string(payload)
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-BAPI-API-KEY", c.Config.APIKey)
	req.Header.Set("X-BAPI-TIMESTAMP", ts)
	req.Header.Set("X-BAPI-RECV-WINDOW", c.Config.RecvWindow)
	req.Header.Set("X-BAPI-SIGN-TYPE", "2")
	req.Header.Set("X-BAPI-SIGN", c.SignREST(c.Config.APISecret, ts, c.Config.APIKey, c.Config.RecvWindow, signed))
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: requestTimeout(c.Config)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, &RequestError{Err: fmt.Errorf("%s %s: %w", method, path, err), Sent: true}
	}
	defer resp.Body.Close()
	limit := c.Config.RESTMaxResponseBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	if limit > 16<<20 {
		limit = 16 << 20
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, &RequestError{Err: fmt.Errorf("read %s: %w", path, err), Sent: true}
	}
	if int64(len(raw)) > limit {
		return nil, &RequestError{Err: fmt.Errorf("response exceeds %d bytes", limit), Sent: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &RequestError{Err: fmt.Errorf("%s HTTP %d", path, resp.StatusCode), Sent: true}
	}
	var envelope struct {
		Code    *int   `json:"retCode"`
		Message string `json:"retMsg"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, &RequestError{Err: fmt.Errorf("decode %s: %w", path, err), Sent: true}
	}
	if envelope.Code == nil {
		return nil, &RequestError{Err: fmt.Errorf("%s missing retCode", path), Sent: true}
	}
	if *envelope.Code != 0 && !(path == "/v5/position/trading-stop" && *envelope.Code == 34040) {
		return nil, &Rejection{Code: *envelope.Code, Message: envelope.Message}
	}
	return raw, nil
}
