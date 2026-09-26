package profit

import (
	"bufio"
	"context"
	"encoding/json"
	"math"
	"net"
	"testing"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

// coins has BTC, BSV and BCH; BSV and BCH earn the given share more than BTC.
func coins(bsv, bch float64) *Market {
	return &Market{Fetched: now, BTCUSD: 80000, Coins: map[string]Coin{
		"BTC": {Tag: "BTC", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1},
		"BSV": {Tag: "BSV", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1 + bsv},
		"BCH": {Tag: "BCH", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1 + bch},
	}}
}

// setGain changes a coin's earnings in place: the switcher keeps the market
// it fetched for a while.
func setGain(m *Market, tag string, gain float64) {
	c := m.Coins[tag]
	c.PriceBTC = 1 + gain
	m.Coins[tag] = c
}

// stratum answers every request with success, enough for the pool check.
func stratum(t *testing.T) state.Address {
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
			go func() {
				defer c.Close()
				r := bufio.NewScanner(c)
				for r.Scan() {
					var m struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if json.Unmarshal(r.Bytes(), &m) != nil {
						return
					}
					result := `true`
					if m.Method == "mining.subscribe" {
						result = `[[["mining.notify","1"]],"00000001",4]`
					}
					c.Write([]byte(`{"id":` + string(m.ID) + `,"result":` + result + `,"error":null}` + "\n"))
				}
			}()
		}
	}()
	return state.Address{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
}

func (e *env) goTo(t *testing.T, id string) {
	t.Helper()
	if _, err := e.mgr.Activate(context.Background(), id, true); err != nil {
		t.Fatal(err)
	}
}

func (e *env) check(t *testing.T, decision, target string) *Report {
	t.Helper()
	rep := e.sw.Check(context.Background(), false)
	if rep.Decision != decision || rep.Target != target {
		t.Fatalf("decision %s → %q, want %s → %q (%+v)", rep.Decision, rep.Target, decision, target, rep)
	}
	return rep
}

// The example of the main pool: BTC is home; BSV at +6% wins it, and the
// farm goes back once BSV is less than 1% ahead, then stays until a coin is
// 5% ahead again.
func TestMainPool(t *testing.T) {
	m := coins(0.06, -0.10)
	e := newEnv(t, m, nil, poolOf("btc", "BTC", true), poolOf("bsv", "BSV", true), poolOf("bch", "BCH", true))
	if err := e.mgr.SetProfitHome("btc"); err != nil {
		t.Fatal(err)
	}

	rep := e.check(t, Recommend, "bsv")
	if rep.Home != "btc" || rep.HomeCoin != "BTC" || rep.ReturnMargin != 1 {
		t.Fatalf("report: %+v", rep)
	}
	e.goTo(t, "bsv")

	setGain(m, "BSV", 0.03) // still 3% ahead of the main pool: stay
	rep = e.check(t, AwayStay, "")
	if math.Abs(rep.OverHome-3) > 1e-9 {
		t.Fatalf("over home %v", rep.OverHome)
	}
	setGain(m, "BSV", 0.005) // below the 1% return margin: back home
	e.check(t, ReturnHome, "btc")
	setGain(m, "BSV", -0.02) // the main pool is ahead, if only by 2%: back home
	e.check(t, ReturnHome, "btc")

	setGain(m, "BSV", 0.03)
	setGain(m, "BCH", 0.10) // 6.8% over BSV: a better coin still wins
	e.check(t, Recommend, "bch")

	e.goTo(t, "btc") // at home, 3% is below the 5% margin: stay
	setGain(m, "BCH", -0.10)
	e.check(t, BelowMargin, "")
}

func TestWithoutMainPoolNothingChanges(t *testing.T) {
	m := coins(0.06, -0.10)
	e := newEnv(t, m, nil, poolOf("bsv", "BSV", true), poolOf("btc", "BTC", true))
	setGain(m, "BSV", 0.005)
	rep := e.check(t, Best, "")
	if rep.Home != "" {
		t.Fatalf("home %q", rep.Home)
	}
}

// In auto mode a scheduled check brings the farm home.
func TestAutoReturnsHome(t *testing.T) {
	btc, bsv := poolOf("btc", "BTC", true), poolOf("bsv", "BSV", true)
	btc.Addresses, bsv.Addresses = []state.Address{stratum(t)}, []state.Address{stratum(t)}
	e := newEnv(t, coins(0.005, -0.10), nil, bsv, btc)
	if err := e.mgr.SetProfitHome("btc"); err != nil {
		t.Fatal(err)
	}
	e.auto(t)
	rep := e.sw.Check(context.Background(), true)
	if rep.Decision != Returned || e.mgr.Snapshot().Active != "btc" {
		t.Fatalf("decision %s, active %s", rep.Decision, e.mgr.Snapshot().Active)
	}
	found := false
	for _, x := range e.ev.List(0) {
		found = found || x.Type == "profit_returned"
	}
	if !found {
		t.Fatal("no profit_returned event")
	}
}

func TestMainPoolMustTakePart(t *testing.T) {
	e := newEnv(t, coins(0, 0), nil, poolOf("btc", "BTC", true), poolOf("bsv", "BSV", false))
	if err := e.mgr.SetProfitHome("bsv"); err == nil {
		t.Fatal("a pool outside profit switching became the main pool")
	}
	if err := e.mgr.SetProfitHome("btc"); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, _, err := e.mgr.Update("btc", pool.Input{ProfitSwitch: &off}, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := e.mgr.Snapshot().Get("btc"); p.ProfitHome {
		t.Fatal("left profit switching but is still the main pool")
	}
	if err := e.mgr.SetProfitHome(""); err != nil {
		t.Fatal(err)
	}
}
