package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"verbose-fortnight/api"
	"verbose-fortnight/config"
	"verbose-fortnight/internal/operations"
	"verbose-fortnight/logging"
	"verbose-fortnight/models"
	"verbose-fortnight/order"
	"verbose-fortnight/position"
	"verbose-fortnight/strategy"
)

type durableQuiet struct{}

func (durableQuiet) Debug(string, ...interface{})    {}
func (durableQuiet) Info(string, ...interface{})     {}
func (durableQuiet) Warning(string, ...interface{})  {}
func (durableQuiet) Error(string, ...interface{})    {}
func (durableQuiet) Fatal(string, ...interface{})    {}
func (durableQuiet) Sync() error                     { return nil }
func (durableQuiet) ChangeLogLevel(logging.LogLevel) {}

// This child is a real process using production storage, startup reconciliation,
// OrderManager, maker policy and PositionManager. Only barriers are injected.
func TestIndependentDurableChild(t *testing.T) {
	if os.Getenv("VF_DURABLE_CHILD") != "1" {
		return
	}
	in := bufio.NewScanner(os.Stdin)
	emit := func(event string, extra map[string]any) {
		if extra == nil {
			extra = map[string]any{}
		}
		extra["event"] = event
		json.NewEncoder(os.Stdout).Encode(extra)
	}
	cfg := config.LoadConfig()
	cfg.RESTTimeoutMs = 100
	cfg.OrderReconcileTimeoutMs = 120
	cfg.StartupReconcileTimeoutMs = 500
	state := &models.State{Instr: models.InstrumentInfo{QtyStep: 1, MinQty: 1, TickSize: 0.001}, PositionState: models.PositionStateFlat, BidsMap: map[string]float64{"100": 10}, AsksMap: map[string]float64{"101": 10}, ProcessedExec: map[string]time.Time{}}
	c := api.NewRESTClient(cfg, durableQuiet{})
	store, err := attachOperationStore(cfg, state, c)
	tr := strategy.NewTrader(c, cfg, state, durableQuiet{})
	if err != nil {
		emit("blocked", map[string]any{"error": err.Error(), "clear": tr.ClearTradingBlockManual("must fail")})
		for in.Scan() {
			if in.Text() == "exit" {
				return
			}
		}
		return
	}
	defer store.Close()
	hook, hookKind := os.Getenv("VF_DURABLE_HOOK"), os.Getenv("VF_DURABLE_HOOK_KIND")
	store.Hook = func(stage string, r operations.Record) {
		if stage == hook && (hookKind == "" || r.Kind == hookKind) {
			emit("barrier", map[string]any{"stage": stage, "id": r.ID, "generation": r.Generation})
			if !in.Scan() {
				os.Exit(4)
			}
		}
	}
	obs, err := c.ReconcileStartup(context.Background())
	if err != nil {
		emit("blocked", map[string]any{"error": err.Error(), "clear": tr.ClearTradingBlockManual("must fail"), "diagnostic": store.Diagnostic()})
		for in.Scan() {
			if in.Text() == "exit" {
				return
			}
		}
		return
	}
	applyStartupRecovery(state, obs)
	tr.ClearTradingBlockManual("recovered")
	emit("ready", map[string]any{"diagnostic": store.Diagnostic(), "position": obs.Position, "lifecycle": obs.Lifecycle})
	if hook == "snapshot_written" {
		store.IOHook = func(stage string) {
			if stage == hook {
				emit("barrier", map[string]any{"stage": stage})
				if !in.Scan() {
					os.Exit(4)
				}
			}
		}
	}

	for in.Scan() {
		cmd := in.Text()
		switch cmd {
		case "exit":
			return
		case "place", "place-second":
			link := "stable-one"
			if cmd == "place-second" {
				link = "stable-two"
			}
			state.Lock()
			state.SetActiveTradeLocked("life-one")
			state.PosSide = "LONG"
			state.PositionState = models.PositionStateOpening
			state.Unlock()
			id, e := order.NewOrderManager(c, cfg, state, durableQuiet{}).PlaceOrderLimitPostOnlyWithLinkID("Buy", 10, 100, false, link)
			emit("result", map[string]any{"id": id, "error": fmt.Sprint(e), "diagnostic": store.Diagnostic()})
		case "maker":
			cfg.EnableMakerFirst = true
			cfg.EnableLifecycleID = true
			cfg.MakerTimeoutMs = 20
			tr.HandleLongSignal(100)
			emit("result", map[string]any{"diagnostic": store.Diagnostic()})
		case "cancel":
			e := c.CancelOrder(context.Background(), cfg.Symbol, "", "stable-one")
			emit("result", map[string]any{"error": fmt.Sprint(e), "diagnostic": store.Diagnostic()})
		case "stop", "stop-old":
			life := "life-one"
			if cmd == "stop-old" {
				life = "previous-life"
			}
			state.Lock()
			state.SetActiveTradeLocked(life)
			state.PosSide = "LONG"
			state.PositionState = models.PositionStateOpen
			state.Unlock()
			_, e := position.NewPositionManager(c, cfg, state, durableQuiet{}).ApplyStopIntent(context.Background(), cfg.Symbol, models.StopIntent{TradeID: life, LifecycleID: life, Side: "LONG", Entry: 100, TP: 100.004, SL: 99.996})
			emit("result", map[string]any{"error": fmt.Sprint(e), "diagnostic": store.Diagnostic()})
		case "reconcile":
			_, e := c.ReconcileStartup(context.Background())
			emit("result", map[string]any{"error": fmt.Sprint(e), "clear": tr.ClearTradingBlockManual("must respect gate"), "diagnostic": store.Diagnostic()})
		}
	}
}

// The fake venue lives in the parent process. Acceptance, executions, remainder,
// cancellation and reply delivery are separate events, independent of bot logs.
type durableVenueOrder struct {
	ID, Link, Status, Side string
	Qty, Cum               float64
}
type durableGate struct {
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newDurableGate() *durableGate {
	return &durableGate{reached: make(chan struct{}), release: make(chan struct{})}
}
func (g *durableGate) wait(r *http.Request) {
	g.once.Do(func() { close(g.reached) })
	select {
	case <-g.release:
	case <-r.Context().Done():
	}
}

type durableVenue struct {
	mu                                sync.Mutex
	orders                            map[string]durableVenueOrder
	fills                             []api.RecoveryExecution
	journal                           []map[string]any
	mode                              string
	positionOverride                  string
	hideGlobalFills                   bool
	wsCalls                           int
	createGate, cancelGate, queryGate *durableGate
	tp, sl                            string
	failStartup                       bool
}

func newDurableVenue() *durableVenue {
	return &durableVenue{orders: map[string]durableVenueOrder{}, mode: "filled", tp: "110", sl: "90"}
}
func (v *durableVenue) fillLocked(o *durableVenueOrder, qty float64) {
	o.Cum = qty
	if qty == o.Qty {
		o.Status = "Filled"
	} else if qty > 0 {
		o.Status = "PartiallyFilled"
	}
	if qty > 0 {
		v.fills = append(v.fills, api.RecoveryExecution{ID: "exec-" + o.ID, OrderID: o.ID, Link: o.Link, Side: o.Side, Qty: strconv.FormatFloat(qty, 'f', -1, 64)})
	}
}
func durableOrderJSON(o durableVenueOrder) map[string]any {
	leaves := o.Qty - o.Cum
	if (api.OrderState{Status: o.Status}).Terminal() {
		leaves = 0
	}
	return map[string]any{"symbol": "BTCUSDT", "orderId": o.ID, "orderLinkId": o.Link, "side": o.Side, "orderStatus": o.Status, "qty": fmt.Sprint(o.Qty), "cumExecQty": fmt.Sprint(o.Cum), "leavesQty": fmt.Sprint(leaves)}
}
func (v *durableVenue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.Lock()
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		v.wsCalls++
	}
	if v.failStartup && r.Method == "GET" {
		v.mu.Unlock()
		http.Error(w, "offline unavailable", 503)
		return
	}
	v.mu.Unlock()
	if r.Method == "POST" {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad body", 400)
			return
		}
		v.mu.Lock()
		body["path"] = r.URL.Path
		v.journal = append(v.journal, body)
		var gate *durableGate
		id := ""
		switch r.URL.Path {
		case "/v5/order/create":
			link, _ := body["orderLinkId"].(string)
			if _, ok := v.orders[link]; ok {
				v.mu.Unlock()
				fmt.Fprint(w, `{"retCode":110072}`)
				return
			}
			q, _ := strconv.ParseFloat(body["qty"].(string), 64)
			side, _ := body["side"].(string)
			id = fmt.Sprintf("order-%d", len(v.orders)+1)
			o := durableVenueOrder{ID: id, Link: link, Status: "New", Side: side, Qty: q}
			if v.mode == "filled" || body["orderType"] == "Market" {
				v.fillLocked(&o, q)
			} else if v.mode == "partial" {
				v.fillLocked(&o, 4)
			}
			v.orders[link] = o
			gate = v.createGate
		case "/v5/order/cancel":
			link, _ := body["orderLinkId"].(string)
			if link == "" {
				id, _ := body["orderId"].(string)
				for key, o := range v.orders {
					if o.ID == id {
						link = key
						break
					}
				}
			}
			o := v.orders[link]
			o.Status = "Cancelled"
			id = o.ID
			v.orders[link] = o
			gate = v.cancelGate
		case "/v5/position/trading-stop":
			v.tp, _ = body["takeProfit"].(string)
			v.sl, _ = body["stopLoss"].(string)
			gate = v.createGate
		default:
			v.mu.Unlock()
			http.Error(w, "unexpected mutation", 500)
			return
		}
		v.mu.Unlock()
		if gate != nil {
			gate.wait(r)
		}
		json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "result": map[string]any{"orderId": id}})
		return
	}
	v.mu.Lock()
	gate := v.queryGate
	v.mu.Unlock()
	if gate != nil && r.URL.Path == "/v5/order/realtime" {
		gate.wait(r)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	result := map[string]any{"list": []any{}}
	switch r.URL.Path {
	case "/v5/order/realtime", "/v5/order/history":
		list := []any{}
		link, id := r.URL.Query().Get("orderLinkId"), r.URL.Query().Get("orderId")
		for _, o := range v.orders {
			if link != "" && o.Link != link || id != "" && o.ID != id {
				continue
			}
			if link == "" && id == "" && (api.OrderState{Status: o.Status}).Terminal() {
				continue
			}
			list = append(list, durableOrderJSON(o))
		}
		result["list"] = list
	case "/v5/execution/list":
		list := []api.RecoveryExecution{}
		for _, f := range v.fills {
			if v.hideGlobalFills && r.URL.Query().Get("orderId") == "" {
				continue
			}
			if id := r.URL.Query().Get("orderId"); id != "" && id != f.OrderID {
				continue
			}
			list = append(list, f)
		}
		result["list"] = list
	case "/v5/position/list":
		net := 0.0
		for _, f := range v.fills {
			q, _ := strconv.ParseFloat(f.Qty, 64)
			if f.Side == "Buy" {
				net += q
			} else {
				net -= q
			}
		}
		if v.positionOverride != "" {
			net, _ = strconv.ParseFloat(v.positionOverride, 64)
		}
		list := []any{}
		if net != 0 {
			side := "Buy"
			if net < 0 {
				side = "Sell"
				net = -net
			}
			list = append(list, map[string]any{"symbol": "BTCUSDT", "positionIdx": 0, "side": side, "size": fmt.Sprint(net), "avgPrice": "100", "takeProfit": v.tp, "stopLoss": v.sl})
		}
		result["list"] = list
	case "/v5/account/wallet-balance":
		result["list"] = []any{map[string]any{"totalAvailableBalance": "1000", "coin": []any{map[string]any{"coin": "USDT", "availableToWithdraw": "1000", "walletBalance": "1000"}}}}
	case "/v5/market/instruments-info":
		result["list"] = []any{map[string]any{"lotSizeFilter": map[string]any{"qtyStep": "1", "minOrderQty": "1", "minNotionalValue": "0"}, "priceFilter": map[string]any{"tickSize": "0.001"}}}
	default:
		http.Error(w, "unexpected GET "+r.URL.Path, 500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"retCode": 0, "result": result})
}
func (v *durableVenue) oracle(t *testing.T, maxCreates int, budget float64) {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	creates := 0
	exposure := 0.0
	links := map[string]bool{}
	for _, p := range v.journal {
		if p["path"] != "/v5/order/create" {
			continue
		}
		creates++
		link := p["orderLinkId"].(string)
		if links[link] {
			t.Fatal("duplicate create POST", v.journal)
		}
		links[link] = true
	}
	for _, o := range v.orders {
		exposure += o.Cum
		if !(api.OrderState{Status: o.Status}).Terminal() {
			exposure += o.Qty - o.Cum
		}
	}
	if creates > maxCreates || exposure > budget {
		t.Fatalf("exposure oracle: creates=%d potential=%g budget=%g journal=%v", creates, exposure, budget, v.journal)
	}
}

type durableChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	events chan map[string]any
	done   chan error
	waited bool
}

func startDurableChild(t *testing.T, srv, path, hook, kind string) *durableChild {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(exe, "-test.run=^TestIndependentDurableChild$")
	cmd.Env = append(independentChildEnvironment(), "VF_DURABLE_CHILD=1", "VF_DURABLE_HOOK="+hook, "VF_DURABLE_HOOK_KIND="+kind, "OPERATION_STATE_PATH="+path, "BYBIT_DEMO_REST_HOST="+srv, "BYBIT_API_KEY=offline-key", "BYBIT_API_SECRET=offline-secret")
	in, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	out, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = cmd.Stdout
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	c := &durableChild{cmd: cmd, input: in, events: make(chan map[string]any, 32), done: make(chan error, 1)}
	go func() {
		scan := bufio.NewScanner(out)
		scan.Buffer(make([]byte, 4096), 4<<20)
		for scan.Scan() {
			var event map[string]any
			if json.Unmarshal(scan.Bytes(), &event) == nil && event["event"] != nil {
				c.events <- event
			}
		}
	}()
	go func() { c.done <- cmd.Wait() }()
	t.Cleanup(func() {
		if !c.waited {
			cmd.Process.Kill()
			<-c.done
			c.waited = true
		}
	})
	return c
}
func (c *durableChild) send(t *testing.T, command string) {
	t.Helper()
	if _, e := fmt.Fprintln(c.input, command); e != nil {
		t.Fatal(e)
	}
}
func (c *durableChild) next(t *testing.T, event string) map[string]any {
	t.Helper()
	select {
	case got := <-c.events:
		if got["event"] != event {
			t.Fatalf("event=%v want=%s", got, event)
		}
		return got
	case <-time.After(4 * time.Second):
		t.Fatal("child event timeout " + event)
	}
	return nil
}
func (c *durableChild) kill(t *testing.T) {
	t.Helper()
	if e := c.cmd.Process.Signal(syscall.SIGKILL); e != nil {
		t.Fatal(e)
	}
	select {
	case <-c.done:
		c.waited = true
	case <-time.After(2 * time.Second):
		t.Fatal("SIGKILL child not reaped")
	}
}
func (c *durableChild) exit(t *testing.T) {
	t.Helper()
	c.send(t, "exit")
	select {
	case e := <-c.done:
		c.waited = true
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child not exited")
	}
}
func waitDurableGate(t *testing.T, g *durableGate) {
	t.Helper()
	select {
	case <-g.reached:
	case <-time.After(3 * time.Second):
		t.Fatal("venue gate timeout")
	}
}

// Independent on-disk oracle: parse bytes and verify checksum directly, without
// the production loader. Each restart uses exactly this same file.
func readDurableDisk(t *testing.T, path string) operations.Snapshot {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var env struct {
		Body json.RawMessage `json:"body"`
		Hash string          `json:"sha256"`
	}
	if e = json.Unmarshal(b, &env); e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(env.Body)
	if env.Hash != hex.EncodeToString(hash[:]) {
		t.Fatal("invalid actual disk checksum")
	}
	if strings.Contains(string(b), "offline-key") || strings.Contains(string(b), "offline-secret") {
		t.Fatal("credentials persisted")
	}
	var s operations.Snapshot
	if e = json.Unmarshal(env.Body, &s); e != nil {
		t.Fatal(e)
	}
	return s
}
func requireDurableReady(t *testing.T, c *durableChild, want bool) {
	t.Helper()
	event := "blocked"
	if want {
		event = "ready"
	}
	r := c.next(t, event)
	if !want && r["clear"] != false {
		t.Fatal("manual clear bypassed startup", r)
	}
}

func TestIndependentDurableCrashMatrix(t *testing.T) {
	for _, stage := range []string{"intent_saved", "sending_saved", "exchange_accepted", "response_received", "result_saved"} {
		t.Run(stage, func(t *testing.T) {
			v := newDurableVenue()
			if stage == "exchange_accepted" {
				v.createGate = newDurableGate()
			}
			srv := httptest.NewServer(v)
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "ops.json")
			hook := stage
			if stage == "exchange_accepted" {
				hook = ""
			}
			c := startDurableChild(t, srv.URL, path, hook, "/v5/order/create")
			requireDurableReady(t, c, true)
			c.send(t, "place")
			if stage == "exchange_accepted" {
				waitDurableGate(t, v.createGate)
			} else {
				c.next(t, "barrier")
			}
			before := readDurableDisk(t, path)
			if len(before.Records) != 1 || before.Records[0].Owner.Lifecycle != "life-one" || before.Records[0].Link != "stable-one" {
				t.Fatal("durable identity missing", before)
			}
			c.kill(t)
			if v.createGate != nil {
				close(v.createGate.release)
				v.mu.Lock()
				v.createGate = nil
				v.mu.Unlock()
			}
			r := startDurableChild(t, srv.URL, path, "", "")
			requireDurableReady(t, r, stage != "sending_saved")
			r.exit(t)
			after := readDurableDisk(t, path)
			wantStage := "resolved"
			if stage == "sending_saved" {
				wantStage = "sending"
			}
			if after.Records[0].Stage != wantStage || after.Records[0].ID != before.Records[0].ID || after.Sequence != before.Sequence {
				t.Fatal("operation lost/reused", after)
			}
			max, budget := 1, 10.0
			if stage == "intent_saved" || stage == "sending_saved" {
				max, budget = 0, 0
			}
			v.oracle(t, max, budget)
			r = startDurableChild(t, srv.URL, path, "", "")
			requireDurableReady(t, r, stage != "sending_saved")
			r.exit(t)
			v.oracle(t, max, budget)
			t.Logf("crash=%s create_limit=%d exposure_limit=%g record=%s generation=%d outcome=%s repeated_restart_ready=%t", stage, max, budget, after.Records[0].ID, after.Records[0].Generation, after.Records[0].Outcome, stage != "sending_saved")
		})
	}
}
func TestIndependentDurablePartialCancelLostReply(t *testing.T) {
	v := newDurableVenue()
	v.mode = "partial"
	v.cancelGate = newDurableGate()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	c.send(t, "cancel")
	waitDurableGate(t, v.cancelGate)
	s := readDurableDisk(t, path)
	if len(s.Records) != 2 || s.Records[1].Stage != "sending" {
		t.Fatal("cancel not persisted", s)
	}
	c.kill(t)
	close(v.cancelGate.release)
	r := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.exit(t)
	s = readDurableDisk(t, path)
	for _, o := range s.Records {
		if o.Stage != "resolved" || o.CumExecQty != 4 {
			t.Fatal("partial cancellation lost", s)
		}
	}
	v.oracle(t, 1, 10)
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.orders) != 1 || v.orders["stable-one"].Status != "Cancelled" || v.orders["stable-one"].Cum != 4 || len(v.journal) != 2 {
		t.Fatal("unexpected exchange state", v.orders, v.journal)
	}
}
func TestIndependentDurableUnknownMissingAndReconcileCrash(t *testing.T) {
	v := newDurableVenue()
	v.mode = "live"
	v.createGate = newDurableGate()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	waitDurableGate(t, v.createGate)
	c.kill(t)
	close(v.createGate.release)
	v.mu.Lock()
	saved := v.orders["stable-one"]
	delete(v.orders, "stable-one")
	v.mu.Unlock()
	r := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, false)
	if readDurableDisk(t, path).Records[0].Stage != "sending" {
		t.Fatal("absence resolved ambiguous POST")
	}
	r.kill(t)
	v.mu.Lock()
	v.orders["stable-one"] = saved
	v.queryGate = newDurableGate()
	g := v.queryGate
	v.mu.Unlock()
	r = startDurableChild(t, srv.URL, path, "", "")
	waitDurableGate(t, g)
	r.kill(t)
	close(g.release)
	v.mu.Lock()
	v.queryGate = nil
	saved.Status = "Cancelled"
	v.orders["stable-one"] = saved
	v.mu.Unlock()
	r = startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.exit(t)
	v.oracle(t, 1, 10)
}
func TestIndependentDurableSeveralPendingAndExternalOrders(t *testing.T) {
	v := newDurableVenue()
	v.mode = "live"
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	c.send(t, "place-second")
	c.next(t, "result")
	c.kill(t)
	r := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, false)
	s := readDurableDisk(t, path)
	if len(s.Records) != 2 {
		t.Fatal("lost unresolved records", s)
	}
	r.kill(t)
	v.mu.Lock()
	o := v.orders["stable-two"]
	o.Status = "Cancelled"
	v.orders["stable-two"] = o
	v.mu.Unlock()
	r = startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, false)
	r.kill(t)
	v.mu.Lock()
	o = v.orders["stable-one"]
	o.Status = "Cancelled"
	v.orders["stable-one"] = o
	v.mu.Unlock()
	r = startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.exit(t)
	v.oracle(t, 2, 20)
	// A fresh/missing file still discovers the exchange's executable order.
	v.mu.Lock()
	v.orders["external"] = durableVenueOrder{ID: "outside", Link: "external", Status: "New", Side: "Buy", Qty: 10}
	v.mu.Unlock()
	fresh := filepath.Join(t.TempDir(), "fresh.json")
	r = startDurableChild(t, srv.URL, fresh, "", "")
	requireDurableReady(t, r, false)
	r.kill(t)
	s = readDurableDisk(t, fresh)
	if len(s.Records) != 1 || s.Records[0].Kind != "external/order" || s.Records[0].OrderID != "outside" {
		t.Fatal("external uncertainty not retained", s)
	}
}
func TestIndependentDurableCorruptionWriteFailureAndUnavailable(t *testing.T) {
	for _, kind := range []string{"corrupt", "write_failure", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			v := newDurableVenue()
			srv := httptest.NewServer(v)
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "ops.json")
			if kind == "corrupt" {
				os.WriteFile(path, []byte(`{"body":`), 0600)
			}
			if kind == "unavailable" {
				v.mu.Lock()
				v.failStartup = true
				v.mu.Unlock()
			}
			c := startDurableChild(t, srv.URL, path, "", "")
			if kind == "write_failure" {
				requireDurableReady(t, c, true)
				os.Remove(path)
				os.Mkdir(path, 0700)
				c.send(t, "place")
				r := c.next(t, "result")
				if r["error"] == "<nil>" {
					t.Fatal("write failure returned success")
				}
				c.exit(t)
			} else {
				requireDurableReady(t, c, false)
				c.kill(t)
			}
			v.oracle(t, 0, 0)
			if kind == "corrupt" {
				b, _ := os.ReadFile(path)
				if string(b) != `{"body":` {
					t.Fatal("corruption overwritten")
				}
			}
		})
	}
}
func TestIndependentDurableStopAcceptedReplyLost(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	v.mu.Lock()
	v.createGate = newDurableGate()
	g := v.createGate
	v.mu.Unlock()
	c.send(t, "stop")
	waitDurableGate(t, g)
	c.kill(t)
	close(g.release)
	r := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.exit(t)
	s := readDurableDisk(t, path)
	if len(s.Records) != 2 || s.Records[1].Stage != "resolved" {
		t.Fatal("stop uncertainty lost", s)
	}
	v.oracle(t, 1, 10)
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.journal) != 2 || v.tp != "100.004" || v.sl != "99.996" {
		t.Fatal("stop repeated or payload changed", v.journal)
	}
}

func TestIndependentDurableMakerCancelTimeoutAndRestart(t *testing.T) {
	for _, killDuringCancel := range []bool{false, true} {
		t.Run(fmt.Sprint(killDuringCancel), func(t *testing.T) {
			v := newDurableVenue()
			v.mode = "partial"
			v.cancelGate = newDurableGate()
			srv := httptest.NewServer(v)
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "ops.json")
			c := startDurableChild(t, srv.URL, path, "", "")
			requireDurableReady(t, c, true)
			c.send(t, "maker")
			waitDurableGate(t, v.cancelGate)
			if killDuringCancel {
				c.kill(t)
			} else {
				c.next(t, "result")
				c.exit(t)
			}
			close(v.cancelGate.release)
			r := startDurableChild(t, srv.URL, path, "", "")
			requireDurableReady(t, r, true)
			r.exit(t)
			v.oracle(t, 2, 10)
			v.mu.Lock()
			defer v.mu.Unlock()
			creates := 0
			filled := 0.0
			for _, p := range v.journal {
				if p["path"] == "/v5/order/create" {
					creates++
				}
			}
			for _, o := range v.orders {
				filled += o.Cum
			}
			if killDuringCancel {
				if creates != 1 || filled != 4 {
					t.Fatal("restart retried interrupted maker action", v.journal)
				}
			} else {
				if creates != 2 || filled != 10 {
					t.Fatal("live fallback did not use confirmed remainder", v.journal)
				}
			}
			t.Logf("killDuringCancel=%t creates=%d totalFill=%g budget=10", killDuringCancel, creates, filled)
		})
	}
}

func TestIndependentDurableReconciliationReplyCrash(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "response_received", "/v5/order/create")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "barrier")
	c.kill(t)
	r := startDurableChild(t, srv.URL, path, "reconciliation_result", "/v5/order/create")
	r.next(t, "barrier")
	if readDurableDisk(t, path).Records[0].Stage != "sending" {
		t.Fatal("query result committed before barrier")
	}
	r.kill(t)
	r = startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.exit(t)
	v.oracle(t, 1, 10)
}

func TestIndependentDurableIncompleteWriteCrash(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "snapshot_written", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "barrier")
	c.kill(t)
	debris, _ := filepath.Glob(path + ".tmp-*")
	if len(debris) != 1 {
		t.Fatal("crash did not leave inspectable candidate", debris)
	}
	if len(readDurableDisk(t, path).Records) != 0 {
		t.Fatal("unsynced candidate replaced committed snapshot")
	}
	r := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, false)
	r.kill(t)
	v.oracle(t, 0, 0)
	if _, e := os.Stat(debris[0]); e != nil {
		t.Fatal("incomplete evidence removed")
	}
}

func TestIndependentDurableConflictingEvidenceCannotBecomeCheckpoint(t *testing.T) {
	for _, kind := range []string{"missing_fills", "position_mismatch", "invalid_stops", "mixed_snapshot"} {
		t.Run(kind, func(t *testing.T) {
			v := newDurableVenue()
			srv := httptest.NewServer(v)
			defer srv.Close()
			path := filepath.Join(t.TempDir(), "ops.json")
			c := startDurableChild(t, srv.URL, path, "response_received", "/v5/order/create")
			requireDurableReady(t, c, true)
			c.send(t, "place")
			c.next(t, "barrier")
			c.kill(t)
			v.mu.Lock()
			fills := v.fills
			if kind == "missing_fills" {
				v.fills = nil
			}
			if kind == "position_mismatch" {
				v.positionOverride = "0"
			}
			if kind == "invalid_stops" {
				v.sl = "0"
			}
			if kind == "mixed_snapshot" {
				v.hideGlobalFills = true
				v.positionOverride = "0"
			}
			v.mu.Unlock()
			for i := 0; i < 2; i++ {
				r := startDurableChild(t, srv.URL, path, "", "")
				requireDurableReady(t, r, false)
				r.kill(t)
			}
			s := readDurableDisk(t, path)
			var audit api.RecoveryObservation
			json.Unmarshal(s.Audit, &audit)
			if audit.Position.Size != "" {
				t.Fatal("blocked observation became successful checkpoint", audit)
			}
			v.mu.Lock()
			v.fills = fills
			v.positionOverride = ""
			v.hideGlobalFills = false
			v.sl = "90"
			v.mu.Unlock()
			r := startDurableChild(t, srv.URL, path, "", "")
			requireDurableReady(t, r, true)
			r.exit(t)
			v.oracle(t, 1, 10)
		})
	}
}

func TestIndependentDurableCLIStartupGateAndDiagnostics(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "go-trade")
	if out, e := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); e != nil {
		t.Fatal(e, string(out))
	}
	for _, kind := range []string{"missing_file_audit", "external_active", "corrupt_file", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			v := newDurableVenue()
			path := filepath.Join(t.TempDir(), "ops.json")
			if kind == "external_active" {
				v.orders["outside"] = durableVenueOrder{ID: "outside", Link: "outside", Status: "New", Side: "Buy", Qty: 10}
			}
			if kind == "unavailable" {
				v.mu.Lock()
				v.failStartup = true
				v.mu.Unlock()
			}
			if kind == "corrupt_file" {
				os.WriteFile(path, []byte(`{"body":`), 0600)
			} else if kind != "unavailable" {
				v.queryGate = newDurableGate()
			}
			srv := httptest.NewServer(v)
			defer srv.Close()
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			address := listener.Addr().String()
			listener.Close()
			args := []string{"-debug=3"}
			if kind == "missing_file_audit" {
				args = append(args, "-reconcile-only")
			}
			cmd := exec.Command(binary, args...)
			cmd.Env = append(independentChildEnvironment(), "OPERATION_STATE_PATH="+path, "ENABLE_STATUS_SERVER=1", "STATUS_ADDR="+address, "DAEMON_MODE=0", "ENABLE_DRY_RUN=0", "REST_TIMEOUT_MS=1000", "STARTUP_RECONCILE_TIMEOUT_MS=2000", "BYBIT_API_KEY=offline-key", "BYBIT_API_SECRET=offline-secret", "BYBIT_DEMO_REST_HOST="+srv.URL, "BYBIT_DEMO_WS_PRIVATE="+strings.Replace(srv.URL, "http://", "ws://", 1), "BYBIT_DEMO_WS_PUBLIC="+strings.Replace(srv.URL, "http://", "ws://", 1), "LOG_FILE="+filepath.Join(t.TempDir(), "child.log"))
			var out strings.Builder
			cmd.Stdout = &out
			cmd.Stderr = &out
			if e = cmd.Start(); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			waited := false
			defer func() {
				if !waited {
					cmd.Process.Kill()
					<-done
				}
			}()
			if v.queryGate != nil {
				waitDurableGate(t, v.queryGate)
			}
			client := &http.Client{Timeout: 100 * time.Millisecond}
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			getStatus := func() map[string]any {
				response, e := client.Get("http://" + address + "/status")
				if e != nil {
					return nil
				}
				defer response.Body.Close()
				var status map[string]any
				if json.NewDecoder(response.Body).Decode(&status) != nil {
					return nil
				}
				return status
			}
			var status map[string]any
			for status == nil {
				select {
				case <-tick.C:
					status = getStatus()
				case <-deadline.C:
					t.Fatal("CLI diagnostic unavailable")
				}
			}
			recovery := status["recovery"].(map[string]any)
			if status["tradingBlocked"] != true || recovery["ready"] != false {
				t.Fatal("trading permitted before checks", status)
			}
			if kind == "corrupt_file" && !strings.Contains(recovery["error"].(string), "operation store") {
				t.Fatal("corruption cause absent", status)
			}
			if kind == "unavailable" && !strings.Contains(recovery["error"].(string), "HTTP 503") {
				t.Fatal("unavailable cause absent", status)
			}
			if v.queryGate != nil {
				close(v.queryGate.release)
			}
			if kind == "missing_file_audit" {
				select {
				case e = <-done:
					waited = true
					if e != nil {
						t.Fatal(e, out.String())
					}
				case <-time.After(3 * time.Second):
					t.Fatal("reconcile-only did not finish")
				}
				s := readDurableDisk(t, path)
				if len(s.Audit) == 0 {
					t.Fatal("successful exchange audit not durable")
				}
				if !strings.Contains(out.String(), `"ready":true`) {
					t.Fatal("CLI did not confirm recovery", out.String())
				}
			}
			if kind == "external_active" {
				for {
					status = getStatus()
					if status != nil && strings.Contains(fmt.Sprint(status["blockMessage"]), "active orders") {
						break
					}
					select {
					case <-tick.C:
					case <-deadline.C:
						t.Fatal("active-order cause not visible", status)
					}
				}
				s := readDurableDisk(t, path)
				if len(s.Records) != 1 || s.Records[0].OrderID != "outside" {
					t.Fatal("CLI did not retain outside order")
				}
			}
			if !waited {
				cmd.Process.Signal(syscall.SIGTERM)
				select {
				case e = <-done:
					waited = true
					if e != nil {
						t.Fatal("blocked CLI SIGTERM", e)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("blocked CLI ignored SIGTERM")
				}
			}
			v.oracle(t, 0, 10)
			v.mu.Lock()
			ws := v.wsCalls
			v.mu.Unlock()
			if ws != 0 {
				t.Fatal("WS started before recovery", ws)
			}
		})
	}
}

func TestIndependentDurableOldStopLevelsDoNotConfirmNewOwner(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	v.mu.Lock()
	v.createGate = newDurableGate()
	gate := v.createGate
	v.mu.Unlock()
	c.send(t, "stop-old")
	waitDurableGate(t, gate)
	c.kill(t)
	close(gate.release)
	for i := 0; i < 2; i++ {
		r := startDurableChild(t, srv.URL, path, "", "")
		requireDurableReady(t, r, false)
		r.kill(t)
	}
	s := readDurableDisk(t, path)
	if s.Records[1].Stage != "sending" || s.Records[1].Owner.Lifecycle != "previous-life" {
		t.Fatal("old stop assigned to new owner", s)
	}
	v.oracle(t, 1, 10)
}

func TestIndependentDurableIdentityAfterRestartAndQueryTimeout(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	c.exit(t)
	v.mu.Lock()
	v.queryGate = newDurableGate()
	gate := v.queryGate
	v.mu.Unlock()
	r := startDurableChild(t, srv.URL, path, "", "")
	waitDurableGate(t, gate)
	requireDurableReady(t, r, false)
	r.kill(t)
	s := readDurableDisk(t, path)
	if s.Records[0].Stage != "acknowledged" {
		t.Fatal("query timeout erased pending", s)
	}
	close(gate.release)
	v.mu.Lock()
	v.queryGate = nil
	v.mu.Unlock()
	r = startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, r, true)
	r.send(t, "place")
	result := r.next(t, "result")
	if result["error"] == "<nil>" {
		t.Fatal("same stable link reused after restart")
	}
	r.send(t, "place-second")
	r.next(t, "result")
	r.exit(t)
	s = readDurableDisk(t, path)
	if len(s.Records) != 2 || s.Records[1].Generation <= s.Records[0].Generation || s.Records[0].ID == s.Records[1].ID {
		t.Fatal("restart identity collision", s)
	}
	v.oracle(t, 2, 20)
}

func TestIndependentDurableLostSnapshotDoesNotBecomeEmpty(t *testing.T) {
	v := newDurableVenue()
	srv := httptest.NewServer(v)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "ops.json")
	c := startDurableChild(t, srv.URL, path, "", "")
	requireDurableReady(t, c, true)
	c.send(t, "place")
	c.next(t, "result")
	c.kill(t)
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	r := startDurableChild(t, srv.URL, path, "", "")
	event := r.next(t, "blocked")
	if event["clear"] != false || !strings.Contains(fmt.Sprint(event["error"]), "snapshot missing") {
		t.Fatal("lost state not diagnosed", event)
	}
	r.kill(t)
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("lost snapshot replaced", e)
	}
	v.oracle(t, 1, 10)
}
