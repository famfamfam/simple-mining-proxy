package e2e

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/profit"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// A scheduled check in auto mode moves the farm to the more profitable coin
// through the normal checked switch.
func TestProfitSwitchMovesToBetterCoin(t *testing.T) {
	a, b := newFakePool(t), newFakePool(t)
	px := startProxy(t, a, b)
	yes := true
	for id, coin := range map[string]string{"pool_a": "BTC", "pool_b": "BCH"} {
		c := coin
		if _, _, err := px.mgr.Update(id, pool.Input{Coin: &c, ProfitSwitch: &yes}, false); err != nil {
			t.Fatal(err)
		}
	}
	set, _, _ := settings.NewStore(map[string]json.RawMessage{"profit_switch": json.RawMessage(`"auto"`)})
	market := &profit.Market{Fetched: time.Now(), Coins: map[string]profit.Coin{
		"BTC": {Tag: "BTC", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1},
		"BCH": {Tag: "BCH", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1.08},
	}}
	sw := profit.New(profit.Deps{
		Settings: set, Pools: px.mgr, Events: px.ev, Path: filepath.Join(t.TempDir(), "profit.json"),
		Fetch: func(context.Context) (*profit.Market, error) { return market, nil },
	})

	rep := sw.Check(context.Background(), true)
	if rep.Decision != profit.Switched || rep.Target != "pool_b" {
		t.Fatalf("report: %+v", rep)
	}
	if s := px.mgr.Snapshot(); s.Active != "pool_b" || len(s.Fallback) == 0 || s.Fallback[0] != "pool_a" {
		t.Fatalf("active %s, fallback %v", s.Active, s.Fallback)
	}
	if ls := px.mgr.LastSwitch(); ls == nil || ls.Reason != "profit" {
		t.Fatalf("last switch: %+v", ls)
	}
	// The next check is on the better coin already.
	if rep := sw.Check(context.Background(), true); rep.Decision != profit.Best {
		t.Fatalf("second check: %+v", rep)
	}
}
