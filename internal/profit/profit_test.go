package profit

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/rtt"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

// A trimmed asic.json with the real values of 2026-09-25.
const asicJSON = `{"coins":{
 "Bitcoin":{"id":1,"tag":"BTC","algorithm":"SHA-256","block_time":"575.0","block_reward":3.13874343,"block_reward24":3.147762756136364,
  "difficulty":132757073449487.5,"difficulty24":132757073449487.86,"exchange_rate":83886.68,"exchange_rate24":84148.75119887166,
  "exchange_rate_curr":"BTC","lagging":false,"timestamp":1790321885},
 "BitcoinCash":{"id":193,"tag":"BCH","algorithm":"SHA-256","block_time":"639.0","block_reward":3.125,"block_reward24":3.125,
  "difficulty":531625310065.6644,"difficulty24":531658520557.8448,"exchange_rate":0.003948660264060993,"exchange_rate24":0.003992595684197871,
  "exchange_rate_curr":"BTC","lagging":false,"timestamp":1790320931},
 "DGB-SHA":{"id":113,"tag":"DGB","algorithm":"SHA-256","block_reward":250.72839532400695,"difficulty":430030207.2203099,
  "exchange_rate":5.209408692774586e-08,"lagging":false,"timestamp":1790320931},
 "DGB-Scrypt":{"id":28,"tag":"DGB","algorithm":"Scrypt","block_reward":250,"difficulty":1,"exchange_rate":1,"timestamp":1790320931},
 "Nicehash-SHA-AB":{"id":51,"tag":"NICEHASH","algorithm":"SHA-256","block_reward":1,"difficulty":1,"exchange_rate":1,"timestamp":1790320931},
 "eCash":{"id":370,"tag":"XEC","algorithm":"SHA-256","block_reward":1812499.9999999998,"difficulty":7315106859.591455,
  "exchange_rate":1.0728759321503724e-10,"lagging":true,"timestamp":1790320931}
}}`

var now = time.Unix(1790322000, 0)

func TestParseWhatToMine(t *testing.T) {
	m, err := parseWhatToMine(strings.NewReader(asicJSON), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Coins) != 4 || m.BTCUSD != 84148.75119887166 {
		t.Fatalf("coins %d, BTCUSD %v", len(m.Coins), m.BTCUSD)
	}
	btc, bch := m.Coins["BTC"], m.Coins["BCH"]
	if btc.PriceBTC != 1 || btc.BlockReward != 3.147762756136364 || m.Coins["DGB"].Difficulty != 430030207.2203099 ||
		btc.Difficulty != 132757073449487.86 || btc.DifficultyNow != 132757073449487.5 || m.Coins["DGB"].DifficultyNow != 430030207.2203099 {
		t.Fatalf("coins: %+v", m.Coins)
	}
	// About 4.8e-7 BTC (≈ $0.04) per TH/s a day: the hashprice of Sept 2026.
	// BTC and BCH earn within a few percent of each other.
	if r := btc.RevenueBTC(); r < 4.5e-7 || r > 5e-7 {
		t.Fatalf("BTC revenue per TH/s a day: %g", r)
	}
	if ratio := bch.RevenueBTC() / btc.RevenueBTC(); ratio < 0.9 || ratio > 1.1 {
		t.Fatalf("BCH/BTC revenue ratio %.3f", ratio)
	}
	if !m.Coins["XEC"].Stale || m.Coins["BTC"].Stale {
		t.Fatal("lagging data must be stale")
	}
	if _, err := parseWhatToMine(strings.NewReader(`{"coins":{}}`), now); err == nil {
		t.Fatal("empty market accepted")
	}
}

// XEC revenue counts the Real Time Target: miners get only part of the
// block rate its difficulty suggests.
func TestXECEfficiency(t *testing.T) {
	m, err := parseWhatToMine(strings.NewReader(asicJSON), now)
	if err != nil {
		t.Fatal(err)
	}
	xec, btc := m.Coins["XEC"], m.Coins["BTC"]
	if xec.Efficiency != rtt.Efficiency || btc.Efficiency != 1 {
		t.Fatalf("efficiency XEC %v, BTC %v", xec.Efficiency, btc.Efficiency)
	}
	plain := xec
	plain.Efficiency = 1
	if r := xec.RevenueBTC() / plain.RevenueBTC(); math.Abs(r-rtt.Efficiency) > 1e-12 {
		t.Fatalf("XEC revenue ratio %v", r)
	}
}

func TestBSVFromWhatsOnChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chain/info":
			w.Write([]byte(`{"chain":"main","blocks":968363,"difficulty":31101961006.40421}`))
		case "/exchangerate":
			w.Write([]byte(`{"rate":21.53,"time":1790371583,"currency":"USD"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base := func(context.Context) (*Market, error) { return market(0), nil }

	m, err := WithBSV(srv.Client(), srv.URL, base)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bsv, ok := m.Coins["BSV"]
	if !ok || bsv.BlockReward != 3.125 || bsv.Difficulty != 31101961006.40421 ||
		math.Abs(bsv.PriceBTC-21.53/80000) > 1e-15 || bsv.Stale || bsv.RevenueBTC() <= 0 {
		t.Fatalf("BSV: %+v", bsv)
	}

	// WhatsOnChain down: the rest of the market still comes.
	m, err = WithBSV(srv.Client(), srv.URL+"/nothing", base)(context.Background())
	if err != nil || len(m.Coins) != 2 {
		t.Fatalf("without BSV: %v %+v", err, m)
	}
	// The subsidy halves every 210,000 blocks.
	if c, _ := bsvCoin(1_049_999, 1, 1, 1, now); c.BlockReward != 1.5625 {
		t.Fatalf("after the 2028 halving: %v", c.BlockReward)
	}
}

type env struct {
	sw  *Switcher
	mgr *pool.Manager
	set *settings.Store
	ev  *events.Log
}

// market has BTC and BCH; BCH earns bchGain more than BTC.
func market(bchGain float64) *Market {
	return &Market{Fetched: now, BTCUSD: 80000, Coins: map[string]Coin{
		"BTC": {Tag: "BTC", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1},
		"BCH": {Tag: "BCH", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1 + bchGain},
	}}
}

func newEnv(t *testing.T, m *Market, fetchErr error, pools ...state.Pool) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools = pools
		f.ActivePool = pools[0].ID
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	set, _, _ := settings.NewStore(nil)
	ev := events.New(100)
	mgr := pool.NewManager(st, set, session.NewRegistry(ev), ev)
	sw := New(Deps{Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(dir, "profit.json"),
		Fetch: func(context.Context) (*Market, error) { return m, fetchErr }})
	sw.now = func() time.Time { return now }
	return &env{sw: sw, mgr: mgr, set: set, ev: ev}
}

func (e *env) auto(t *testing.T) {
	t.Helper()
	p, err := e.set.Prepare(map[string]json.RawMessage{"profit_switch": json.RawMessage(`"auto"`)})
	if err != nil {
		t.Fatal(err)
	}
	e.set.Commit(p)
}

func poolOf(id, coin string, profit bool) state.Pool {
	return state.Pool{ID: id, Name: id, Coin: coin, Username: "u", ProfitSwitch: profit,
		Addresses: []state.Address{{Host: id + ".invalid", Port: 3333}}}
}

// hangUp is a local pool that closes every connection at once, so a pool
// check fails without waiting for DNS.
func hangUp(t *testing.T) state.Address {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return state.Address{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
}

func TestDecisions(t *testing.T) {
	btc, bch := poolOf("btc", "BTC", true), poolOf("bch", "BCH", true)
	cases := []struct {
		name     string
		gain     float64
		pools    []state.Pool
		decision string
		target   string
	}{
		{"switch to a clearly better coin", 0.10, []state.Pool{btc, bch}, Recommend, "bch"},
		{"small gain stays", 0.02, []state.Pool{btc, bch}, BelowMargin, ""},
		{"already best", -0.10, []state.Pool{btc, bch}, Best, ""},
		{"manual pool is left alone", 0.50, []state.Pool{poolOf("btc", "BTC", false), bch}, Manual, ""},
		{"a coin without a taking-part pool is ignored", 0.50, []state.Pool{btc, poolOf("bch", "BCH", false)}, Best, ""},
		{"active coin without data", 0.10, []state.Pool{poolOf("xec", "XEC", true), bch}, NoData, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, market(c.gain), nil, c.pools...)
			rep := e.sw.Check(context.Background(), false)
			if rep.Decision != c.decision || rep.Target != c.target {
				t.Fatalf("decision %s → %q, want %s → %q (%+v)", rep.Decision, rep.Target, c.decision, c.target, rep)
			}
		})
	}
}

func TestReportValues(t *testing.T) {
	m := market(0.10)
	// Coins no pool mines: DGB is listed for comparison, QUAI is not.
	m.Coins["DGB"] = Coin{Tag: "DGB", Difficulty: 1e9, BlockReward: 250, PriceBTC: 5e-8}
	m.Coins["QUAI"] = Coin{Tag: "QUAI", Difficulty: 2e20, BlockReward: 11, PriceBTC: 1e-7}
	e := newEnv(t, m, nil, poolOf("btc", "BTC", true), poolOf("bch", "BCH", true))
	rep := e.sw.Check(context.Background(), false)
	if math.Abs(rep.Advantage-10) > 1e-9 || rep.Best != "BCH" || rep.ActiveCoin != "BTC" {
		t.Fatalf("%+v", rep)
	}
	top := rep.Coins[0]
	if top.Tag != "BCH" || top.PriceUSD != 88000 || math.Abs(top.RevenueUSD-top.RevenueBTC*80000) > 1e-9 || len(top.Pools) != 1 {
		t.Fatalf("coin view: %+v", top)
	}
	var tags []string
	for _, c := range rep.Coins {
		tags = append(tags, c.Tag)
	}
	if strings.Join(tags, " ") != "BCH BTC DGB" {
		t.Fatalf("coins listed: %v", tags)
	}
}

// In auto mode a scheduled check switches only through the pool check: a
// pool that does not answer is not activated.
func TestAutoSwitchNeedsAWorkingPool(t *testing.T) {
	bch := poolOf("bch", "BCH", true)
	bch.Addresses = []state.Address{hangUp(t)}
	e := newEnv(t, market(0.10), nil, poolOf("btc", "BTC", true), bch)
	e.auto(t)
	rep := e.sw.Check(context.Background(), true)
	if rep.Decision != SwitchFailed || e.mgr.Snapshot().Active != "btc" {
		t.Fatalf("decision %s, active %s", rep.Decision, e.mgr.Snapshot().Active)
	}
	if st := e.sw.Status(); st.LastRun == nil || st.NextRun == nil || !st.NextRun.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("schedule: %+v", st)
	}
}

func TestManualCheckNeverSwitchesOrMovesSchedule(t *testing.T) {
	e := newEnv(t, market(0.10), nil, poolOf("btc", "BTC", true), poolOf("bch", "BCH", true))
	e.auto(t)
	rep := e.sw.Check(context.Background(), false)
	if rep.Decision != Recommend || e.sw.Status().LastRun != nil {
		t.Fatalf("manual check: %s, last run %v", rep.Decision, e.sw.Status().LastRun)
	}
}

func TestNextRun(t *testing.T) {
	e := newEnv(t, market(0.02), nil, poolOf("btc", "BTC", true))
	e.auto(t)
	next := func() string {
		if st := e.sw.Status(); st.NextRun != nil {
			return st.NextRun.Sub(now).String()
		}
		return "unknown"
	}
	if got := next(); got != "unknown" {
		t.Fatalf("before Run: %s", got)
	}
	// Run started a minute ago: the first check is after the start delay.
	e.sw.started = now.Add(-time.Minute)
	if got := next(); got != "1m0s" {
		t.Fatalf("first check in %s", got)
	}
	e.sw.Check(context.Background(), true)
	if got := next(); got != "24h0m0s" {
		t.Fatalf("after a check: %s", got)
	}
	// Overdue, e.g. the mode was turned on long after the last check.
	e.sw.started = now.Add(-time.Hour)
	e.sw.lastRun = now.Add(-30 * 24 * time.Hour)
	if got := next(); got != "0s" {
		t.Fatalf("overdue: %s", got)
	}
}

// While timed switching holds the farm on its target, coins are compared
// for the pool it returns to, and the scheduled check waits.
func TestTimedTargetHoldsChecks(t *testing.T) {
	solo := poolOf("solo", "BTC", false) // active now, does not take part
	e := newEnv(t, market(0.10), nil, solo, poolOf("btc", "BTC", true), poolOf("bch", "BCH", true))
	e.auto(t)
	home := "btc"
	e.sw.d.Home = func() string { return home }
	e.sw.started = now.Add(-time.Hour)

	if e.sw.due(e.set.Get(), now) {
		t.Fatal("scheduled check runs while on the timed target")
	}
	if rep := e.sw.Check(context.Background(), false); rep.Active != "btc" || rep.Decision != Recommend || rep.Target != "bch" {
		t.Fatalf("check on the timed target: %+v", rep)
	}
	home = ""
	if !e.sw.due(e.set.Get(), now) {
		t.Fatal("scheduled check not due once the farm is back")
	}
	if rep := e.sw.Check(context.Background(), false); rep.Decision != Manual {
		t.Fatalf("without the timer the solo pool is left alone: %+v", rep)
	}
}

func TestNoMarketData(t *testing.T) {
	e := newEnv(t, nil, errors.New("timeout"), poolOf("btc", "BTC", true))
	rep := e.sw.Check(context.Background(), true)
	if rep.Decision != NoData || rep.Error != "timeout" {
		t.Fatalf("%+v", rep)
	}
	// The UI reads coins as a list: never null, also after a restart from a
	// file written when it could be null.
	b, _ := json.Marshal(e.sw.Status())
	if !strings.Contains(string(b), `"coins":[]`) {
		t.Fatalf("status without market data: %s", b)
	}
	if err := os.WriteFile(e.sw.d.Path, []byte(`{"report":{"decision":"no_data","coins":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if again := New(e.sw.d); again.report == nil || again.report.Coins == nil {
		t.Fatalf("old file: %+v", again.report)
	}
	found := false
	for _, x := range e.ev.List(0) {
		found = found || x.Type == "profit_no_data"
	}
	if !found {
		t.Fatal("no event for missing market data")
	}
}

// The schedule survives a restart.
func TestStatePersists(t *testing.T) {
	e := newEnv(t, market(0.02), nil, poolOf("btc", "BTC", true), poolOf("bch", "BCH", true))
	e.sw.Check(context.Background(), true)
	again := New(e.sw.d)
	if again.lastRun.IsZero() || again.report == nil || again.report.Decision != BelowMargin {
		t.Fatalf("after restart: %v %+v", again.lastRun, again.report)
	}
}
