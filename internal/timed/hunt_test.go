package timed

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

// newHuntEnv has pools main (active), backup (fallback), xec (an eCash solo
// pool) and btcsolo; block hunting is on. timer names the timer's pool, ""
// for the timer off.
func newHuntEnv(t *testing.T, xec state.Address, timer string) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	pools := []state.Pool{
		{ID: "main", Name: "Main", Username: "u", Addresses: []state.Address{stratumPool(t)}},
		{ID: "backup", Name: "Backup", Username: "u", Addresses: []state.Address{stratumPool(t)}},
		{ID: "xec", Name: "XEC Solo", Coin: "XEC", Solo: true, Username: "u", Addresses: []state.Address{xec}, TimedTarget: timer == "xec"},
		{ID: "btcsolo", Name: "BTC Solo", Coin: "BTC", Solo: true, Username: "u", Addresses: []state.Address{stratumPool(t)}, TimedTarget: timer == "btcsolo"},
	}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools, f.ActivePool, f.FallbackPools = pools, "main", []string{"backup"}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	mode := `"off"`
	if timer != "" {
		mode = `"on"`
	}
	set, _, err := settings.NewStore(map[string]json.RawMessage{
		"timed_switch": json.RawMessage(mode), "hunt_switch": json.RawMessage(`"on"`), "switch_drain": json.RawMessage(`"0s"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := events.New(200)
	mgr := pool.NewManager(st, set, session.NewRegistry(ev), ev)
	e := &env{t: t, set: set, mgr: mgr, ev: ev, clock: t0, path: filepath.Join(dir, "timed.json"), hard: 1}
	e.sw = e.restart()
	return e
}

func (e *env) hasEvent(typ string) bool {
	for _, x := range e.ev.List(0) {
		if x.Type == typ {
			return true
		}
	}
	return false
}

func joinMessages(ev []events.Event) string {
	var b strings.Builder
	for _, x := range ev {
		b.WriteString(x.Message + "\n")
	}
	return b.String()
}

func (e *env) reason() string {
	if a := e.sw.current(); a != nil {
		return reasonName(a.Reason)
	}
	return ""
}

// The farm goes to the eCash solo pool once a block is no harder than its
// header says, and back as soon as a block arrives.
func TestHuntGoesWhenEasyAndReturnsAtABlock(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "")
	e.hard = 25 // a minute after a block
	e.at(0)
	e.want("main", "backup")
	if st := e.sw.Status().Hunt; st.Since != nil || st.Target != "xec" || !st.Live || st.Hardness != 25 {
		t.Fatalf("waiting: %+v", st)
	}

	e.hard = 1.1
	e.at(2 * time.Minute)
	e.want("xec", "main", "backup")
	if e.reason() != "hunt" || e.sw.Home() != "main" {
		t.Fatalf("reason %q, home %q", e.reason(), e.sw.Home())
	}
	if !strings.Contains(joinMessages(e.ev.List(0)), "(hunt)") {
		t.Fatal("the switch is not marked hunt in the events")
	}
	e.hard = 1
	e.at(9 * time.Minute)
	e.want("xec", "main", "backup")

	e.hard = 817 // a block
	e.at(10 * time.Minute)
	e.want("main", "backup")
	st := e.sw.Status().Hunt
	if st.Since != nil || st.Stints != 1 || st.Seconds != (8*time.Minute).Seconds() {
		t.Fatalf("after the block: %+v", st)
	}
	if !e.hasEvent("hunt_block") {
		t.Fatal("no hunt_block event")
	}
}

func TestHuntNeedsToSeeBlocks(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "")
	e.blind = true
	e.at(0)
	e.want("main", "backup")

	e.blind = false
	e.at(time.Minute)
	e.want("xec", "main", "backup")
	e.blind = true // the eCash connection is lost
	e.at(2 * time.Minute)
	e.want("main", "backup")
	if !e.hasEvent("hunt_blind") {
		t.Fatal("no hunt_blind event")
	}
}

// A pool chosen by hand stands until the next eCash block.
func TestHuntWaitsForABlockAfterAHandSwitch(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "")
	e.at(0)
	e.want("xec", "main", "backup")
	if _, err := e.mgr.Activate(context.Background(), "backup", true); err != nil {
		t.Fatal(err)
	}
	e.at(time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "backup" || !e.sw.Status().Hunt.Hold {
		t.Fatalf("active %s, hold %v", s.Active, e.sw.Status().Hunt.Hold)
	}
	e.at(2 * time.Minute) // still easy, but held
	if s := e.mgr.Snapshot(); s.Active != "backup" {
		t.Fatalf("hunted again before a block: active %s", s.Active)
	}
	e.hard = 817
	e.at(3 * time.Minute)
	e.hard = 1
	e.at(5 * time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "xec" || e.sw.Home() != "backup" {
		t.Fatalf("after the block: active %s, home %s", s.Active, e.sw.Home())
	}
}

// A pool chosen by hand during the timer's stint stands against hunting too.
func TestHandSwitchOnTheTimerHoldsTheHunt(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "btcsolo")
	e.at(0)
	e.want("btcsolo", "main", "backup")
	if _, err := e.mgr.Activate(context.Background(), "backup", true); err != nil {
		t.Fatal(err)
	}
	e.at(time.Minute)
	e.at(2 * time.Minute) // easy and live, but the operator's choice stands
	if s := e.mgr.Snapshot(); s.Active != "backup" {
		t.Fatalf("hunting overrode a pool chosen by hand: active %s", s.Active)
	}
	e.hard = 817
	e.at(3 * time.Minute)
	e.hard = 1
	e.at(5 * time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "xec" || e.sw.Home() != "backup" {
		t.Fatalf("after the block: active %s, home %s", s.Active, e.sw.Home())
	}
}

// A hand switch while hunting is off leaves nothing behind: turning hunting
// on later starts it at once, not after the next block.
func TestHuntStartsAfreshWhenTurnedOn(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "btcsolo")
	e.set.Commit(mustPrepare(t, e.set, `"off"`))
	e.at(0)
	e.want("btcsolo", "main", "backup")
	if _, err := e.mgr.Activate(context.Background(), "backup", true); err != nil {
		t.Fatal(err)
	}
	e.at(time.Minute)
	e.at(2 * time.Minute)
	e.set.Commit(mustPrepare(t, e.set, `"on"`))
	e.at(3 * time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "xec" || e.sw.Status().Hunt.Hold {
		t.Fatalf("hunting held back after being turned on: active %s, hold %v", s.Active, e.sw.Status().Hunt.Hold)
	}
}

// The timer comes first: it goes to its pool from home, and a hunt in
// progress makes way for it.
func TestTimerComesBeforeTheHunt(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "btcsolo")
	e.at(0)
	e.want("btcsolo", "main", "backup")
	e.at(5 * time.Minute) // hunting would like to, the timer holds the farm
	e.want("btcsolo", "main", "backup")
	e.at(10 * time.Minute) // the timer's time is up
	e.want("main", "backup")
	e.at(10*time.Minute + tick)
	e.want("xec", "main", "backup")
	if e.reason() != "hunt" {
		t.Fatalf("reason %q", e.reason())
	}

	e.at(30 * time.Minute) // a new period: home first, then the timer's pool
	e.want("main", "backup")
	e.at(30*time.Minute + tick)
	e.want("btcsolo", "main", "backup")
	if e.reason() != "timer" {
		t.Fatalf("reason %q", e.reason())
	}
}

// The timer and hunting on the same pool: the farm stays, only the reason
// changes.
func TestTimerAndHuntShareThePool(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "xec")
	e.at(0)
	e.want("xec", "main", "backup")
	if e.reason() != "timer" {
		t.Fatalf("reason %q", e.reason())
	}
	e.at(10 * time.Minute) // the timer is done, no block yet: hunting keeps the farm there
	e.want("xec", "main", "backup")
	if e.reason() != "hunt" || !e.hasEvent("timed_handover") {
		t.Fatalf("reason %q", e.reason())
	}
	e.hard = 817
	e.at(12 * time.Minute)
	e.want("main", "backup")

	e.hard = 1
	e.at(20 * time.Minute)
	e.want("xec", "main", "backup")
	e.at(30 * time.Minute) // a new period while hunting: the timer takes over in place
	e.want("xec", "main", "backup")
	st := e.sw.Status()
	if e.reason() != "timer" || st.Home != "main" || st.Until == nil || !st.Until.Equal(t0.Add(40*time.Minute)) {
		t.Fatalf("reason %q, status %+v", e.reason(), st)
	}
	if h := st.Hunt; h.Stints != 2 || h.Since != nil {
		t.Fatalf("hunt status %+v", h)
	}
}

func TestHuntPausesAfterAFailedSwitch(t *testing.T) {
	e := newHuntEnv(t, deadPool(t), "")
	e.at(0)
	e.want("main", "backup")
	if st := e.sw.Status().Hunt; st.Error == "" || st.Retry == nil || !st.Retry.Equal(t0.Add(huntRetry)) || !e.hasEvent("timed_switch_failed") {
		t.Fatalf("hunt status %+v", st)
	}
	before := len(e.ev.List(0))
	e.at(time.Minute)
	if len(e.ev.List(0)) != before {
		t.Fatal("tried again within the pause")
	}
	e.at(huntRetry)
	if len(e.ev.List(0)) == before {
		t.Fatal("no new try after the pause")
	}
}

func TestHuntSurvivesARestart(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "")
	e.at(0)
	e.want("xec", "main", "backup")
	e.sw = e.restart()
	e.hard = 817
	e.at(3 * time.Minute)
	e.want("main", "backup")
}

func TestHuntOffOrNoSoloPool(t *testing.T) {
	e := newHuntEnv(t, stratumPool(t), "")
	p, err := e.set.Prepare(map[string]json.RawMessage{"hunt_switch": json.RawMessage(`"off"`)})
	if err != nil {
		t.Fatal(err)
	}
	e.set.Commit(p)
	e.at(0)
	e.want("main", "backup")

	// Hunting while it is turned off: the farm comes home.
	e.set.Commit(mustPrepare(t, e.set, `"on"`))
	e.at(time.Minute)
	e.want("xec", "main", "backup")
	e.set.Commit(mustPrepare(t, e.set, `"off"`))
	e.at(2 * time.Minute)
	e.want("main", "backup")
	if !strings.Contains(e.sw.Status().Hunt.Mode, "off") {
		t.Fatalf("mode %q", e.sw.Status().Hunt.Mode)
	}
}

func mustPrepare(t *testing.T, set *settings.Store, mode string) *settings.Pending {
	t.Helper()
	p, err := set.Prepare(map[string]json.RawMessage{"hunt_switch": json.RawMessage(mode)})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
