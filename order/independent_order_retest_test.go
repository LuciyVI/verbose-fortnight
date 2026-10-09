package order

import (
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
)

type independentOrderLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *independentOrderLog) record(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}
func (l *independentOrderLog) Debug(f string, a ...interface{})   { l.record(f, a...) }
func (l *independentOrderLog) Info(f string, a ...interface{})    { l.record(f, a...) }
func (l *independentOrderLog) Warning(f string, a ...interface{}) { l.record(f, a...) }
func (l *independentOrderLog) Error(f string, a ...interface{})   { l.record(f, a...) }
func (l *independentOrderLog) Fatal(f string, a ...interface{})   { l.record(f, a...) }
func (l *independentOrderLog) Sync() error                        { return nil }
func (l *independentOrderLog) ChangeLogLevel(logging.LogLevel)    {}

func TestIndependentRejectedProtectiveOrderNotSuccess(t *testing.T) {
	for _, body := range []string{`{"retCode":10001,"retMsg":"rejected"}`, `{broken`} {
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			cfg := independentOrderCfg(srv.URL)
			log := &independentOrderLog{}
			om := NewOrderManager(api.NewRESTClient(cfg, log), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: 0.1, MinQty: 0.1}}, log)
			err := om.PlaceTakeProfitOrder("Sell", 10, 110)
			if err == nil {
				t.Error("rejected/unreadable protective order reported success")
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			for _, line := range log.lines {
				if strings.Contains(line, "placed") || strings.Contains(line, "acknowledged") {
					t.Errorf("false success log: %s", line)
				}
			}
			t.Logf("err=%v logs=%v", err, log.lines)
		})
	}
}

func independentOrderCfg(host string) *config.Config {
	cfg := config.LoadConfig()
	cfg.APIKey = "offline-key"
	cfg.APISecret = "offline-secret"
	cfg.DemoRESTHost = host
	for n, v := range map[string]int64{"RESTTimeoutMs": 30, "OrderReconcileTimeoutMs": 100} {
		f := reflect.ValueOf(cfg).Elem().FieldByName(n)
		if f.IsValid() {
			f.SetInt(v)
		}
	}
	return cfg
}

func independentQuantityOracle(t testing.TB, qty, step float64) {
	t.Helper()
	got := (&OrderManager{}).FormatQty(qty, step)
	valid := qty > 0 && step > 0 && !math.IsNaN(qty) && !math.IsNaN(step) && !math.IsInf(qty, 0) && !math.IsInf(step, 0)
	if !valid {
		if got != "" {
			t.Fatalf("invalid qty=%g step=%g returned %q", qty, step, got)
		}
		return
	}
	input, _ := new(big.Rat).SetString(strconv.FormatFloat(qty, 'f', -1, 64))
	increment, _ := new(big.Rat).SetString(strconv.FormatFloat(step, 'f', -1, 64))
	if got == "" {
		if input.Cmp(increment) >= 0 {
			t.Fatalf("valid quantity >= step rejected: qty=%g step=%g", qty, step)
		}
		return
	}
	output, ok := new(big.Rat).SetString(got)
	if !ok || output.Sign() <= 0 || output.Cmp(input) > 0 || !new(big.Rat).Quo(output, increment).IsInt() {
		t.Fatalf("qty=%g step=%g output=%q violates decimal step/budget", qty, step, got)
	}
	if new(big.Rat).Sub(input, output).Cmp(increment) >= 0 {
		t.Fatalf("rounding lost >= one whole step: qty=%g step=%g got=%q", qty, step, got)
	}
}
func TestIndependentNumericStepBudget(t *testing.T) {
	for _, step := range []float64{1, 0.1, 0.001, 0.25, 1e-30, math.SmallestNonzeroFloat64} {
		for _, qty := range []float64{step, step * 4, math.Nextafter(step*4, 0), math.Nextafter(step*4, math.Inf(1))} {
			independentQuantityOracle(t, qty, step)
		}
	}
	for _, step := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		independentQuantityOracle(t, 1, step)
	}
	for _, qty := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		independentQuantityOracle(t, qty, 0.1)
	}
}
func FuzzIndependentQuantityBudget(f *testing.F) {
	for _, seed := range [][2]float64{{0, 0}, {1, 0.25}, {math.Nextafter(1, 0), 0.25}, {1e-300, 1e-310}, {math.MaxFloat64, math.SmallestNonzeroFloat64}, {math.NaN(), 0.1}, {1, math.Inf(1)}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, qty, step float64) { independentQuantityOracle(t, qty, step) })
}

func TestIndependentZeroStepCompletes(t *testing.T) {
	done := make(chan string, 1)
	go func() { done <- (&OrderManager{}).FormatQty(1, 0) }()
	select {
	case got := <-done:
		if got != "" {
			t.Errorf("zero step accepted: %q", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("FormatQty(1, 0) does not terminate within 100ms")
	}
}

func TestIndependentInvalidQtyNeverSent(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		fmt.Fprint(w, `{"retCode":0,"result":{"orderId":"unexpected"}}`)
	}))
	defer srv.Close()
	cfg := independentOrderCfg(srv.URL)
	for _, tc := range []struct{ qty, step, min float64 }{
		{1, 0, 0.1}, {1, -1, 0.1}, {1, math.NaN(), 0.1}, {1, math.Inf(1), 0.1}, {1, math.Inf(-1), 0.1},
		{0, 0.1, 0.1}, {-1, 0.1, 0.1}, {math.NaN(), 0.1, 0.1}, {math.Inf(1), 0.1, 0.1}, {math.Inf(-1), 0.1, 0.1},
		{0.05, 0.1, 0.1}, {0.5, 0.1, 1},
	} {
		om := NewOrderManager(api.NewRESTClient(cfg, nil), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: tc.step, MinQty: tc.min}}, nil)
		if _, err := om.PlaceOrderMarketWithID("Buy", tc.qty, false); err == nil {
			t.Errorf("invalid inputs accepted: %+v", tc)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("invalid input reached API %d times", calls)
	}
}

func TestIndependentLostCreateResponseIdentity(t *testing.T) {
	for _, lookup := range []string{"correct", "wrong_link", "absent"} {
		t.Run(lookup, func(t *testing.T) {
			var mu sync.Mutex
			creates, queries := 0, 0
			acceptedLink := ""
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v5/order/create" {
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					creates++
					acceptedLink = fmt.Sprint(body["orderLinkId"])
					mu.Unlock()
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				mu.Lock()
				queries++
				link := acceptedLink
				mu.Unlock()
				if r.URL.Query().Get("orderLinkId") != link {
					t.Errorf("lookup identity changed: query=%s accepted=%s", r.URL.RawQuery, link)
				}
				if lookup == "absent" {
					fmt.Fprint(w, `{"retCode":0,"result":{"list":[]}}`)
					return
				}
				returnedLink := link
				if lookup == "wrong_link" {
					returnedLink = "other-action"
				}
				// A decoy precedes the accepted order. Correlation must use the requested link.
				fmt.Fprintf(w, `{"retCode":0,"result":{"list":[{"orderId":"decoy","orderLinkId":"wrong","qty":"10","cumExecQty":"10","orderStatus":"Filled"},{"orderId":"accepted","orderLinkId":%q,"qty":"10","cumExecQty":"1","orderStatus":"PartiallyFilled"}]}}`, returnedLink)
			}))
			defer srv.Close()
			defer releaseServer()
			cfg := independentOrderCfg(srv.URL)
			log := &independentOrderLog{}
			om := NewOrderManager(api.NewRESTClient(cfg, log), cfg, &models.State{Instr: models.InstrumentInfo{QtyStep: 0.1, MinQty: 0.1}}, log)
			began := time.Now()
			type outcome struct {
				id  string
				err error
			}
			done := make(chan outcome, 1)
			go func() { id, err := om.PlaceOrderMarketWithID("Buy", 10, false); done <- outcome{id, err} }()
			var result outcome
			select {
			case result = <-done:
			case <-time.After(500 * time.Millisecond):
				t.Error("accepted create with lost response did not complete within 500ms")
				releaseServer()
				select {
				case result = <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("client did not return after server release")
				}
			}
			id, err := result.id, result.err
			elapsed := time.Since(began)
			mu.Lock()
			defer mu.Unlock()
			t.Logf("id=%s err=%v elapsed=%s creates=%d queries=%d link=%s", id, err, elapsed, creates, queries, acceptedLink)
			if elapsed > time.Second || creates != 1 || acceptedLink == "" || acceptedLink == "<nil>" {
				t.Errorf("unbounded completion/repeated POST/unstable link")
			}
			if lookup == "correct" {
				if err != nil || id != "accepted" {
					t.Errorf("accepted order not recovered by identity: id=%s err=%v", id, err)
				}
			} else if err == nil {
				t.Errorf("wrong/absent identity accepted as success: id=%s", id)
			}
		})
	}
}
