package timed

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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

// stratumPool answers every request with success, enough for the pool check.
func stratumPool(t *testing.T) state.Address {
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

// deadPool refuses every connection at once.
func deadPool(t *testing.T) state.Address {
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

// t0 is the start of a 30-minute period.
var t0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

type env struct {
	t     *testing.T
	sw    *Switcher
	set   *settings.Store
	mgr   *pool.Manager
	ev    *events.Log
	clock time.Time
	path  string
}

// newEnv has pools main (active), backup (fallback) and solo (the target).
func newEnv(t *testing.T, solo state.Address) *env {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	pools := []state.Pool{
		{ID: "main", Name: "Main", Username: "u", Addresses: []state.Address{stratumPool(t)}},
		{ID: "backup", Name: "Backup", Username: "u", Addresses: []state.Address{stratumPool(t)}},
		{ID: "solo", Name: "Solo", Username: "u", Addresses: []state.Address{solo}, TimedTarget: true},
	}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools, f.ActivePool, f.FallbackPools = pools, "main", []string{"backup"}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	set, _, err := settings.NewStore(map[string]json.RawMessage{
		"timed_switch": json.RawMessage(`"on"`), "switch_drain": json.RawMessage(`"0s"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := events.New(100)
	mgr := pool.NewManager(st, set, session.NewRegistry(ev), ev)
	e := &env{t: t, set: set, mgr: mgr, ev: ev, clock: t0, path: filepath.Join(dir, "timed.json")}
	e.sw = e.restart()
	return e
}

// restart builds a new switcher on the same state file, as after a restart.
func (e *env) restart() *Switcher {
	sw := New(Deps{Settings: e.set, Pools: e.mgr, Events: e.ev, Path: e.path})
	sw.now = func() time.Time { return e.clock }
	return sw
}

// at moves the clock to t0+d and runs one step.
func (e *env) at(d time.Duration) {
	e.clock = t0.Add(d)
	e.sw.step(context.Background())
}

func (e *env) want(active string, fallback ...string) {
	e.t.Helper()
	s := e.mgr.Snapshot()
	if s.Active != active || strings.Join(s.Fallback, ",") != strings.Join(fallback, ",") {
		e.t.Fatalf("at %s: active %s, fallback %v; want %s, %v", e.clock.Sub(t0), s.Active, s.Fallback, active, fallback)
	}
}

func TestWindowBoundaries(t *testing.T) {
	for _, c := range []struct {
		at     time.Duration
		start  time.Duration
		inside bool
	}{
		{0, 0, true},
		{9*time.Minute + 59*time.Second, 0, true},
		{10 * time.Minute, 0, false},
		{29 * time.Minute, 0, false},
		{30 * time.Minute, 30 * time.Minute, true},
		{45 * time.Minute, 30 * time.Minute, false},
	} {
		start, in := window(t0.Add(c.at), 30*time.Minute, 10*time.Minute)
		if !start.Equal(t0.Add(c.start)) || in != c.inside {
			t.Errorf("%s: start %s, in %v", c.at, start.Sub(t0), in)
		}
	}
}

// A window moves the farm to the target and back, and the fallback order is
// what it was before, not the target first.
func TestSwitchesToTargetAndBack(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	e.want("solo", "main", "backup")
	if e.sw.Home() != "main" {
		t.Fatalf("home %q", e.sw.Home())
	}
	if ls := e.mgr.LastSwitch(); ls == nil || ls.Reason != "timer" {
		t.Fatalf("last switch: %+v", ls)
	}
	e.at(5 * time.Minute)
	e.want("solo", "main", "backup")
	e.at(10 * time.Minute)
	e.want("main", "backup")
	if e.sw.Home() != "" {
		t.Fatal("home after the window")
	}
	e.at(20 * time.Minute) // between windows: nothing happens
	e.want("main", "backup")
	e.at(30*time.Minute + 5*time.Second) // the next window
	e.want("solo", "main", "backup")
}

// A target that fails its check leaves the farm where it is, and is tried
// again only in the next window.
func TestFailedTargetIsTriedOncePerWindow(t *testing.T) {
	e := newEnv(t, deadPool(t))
	e.at(time.Second)
	e.want("main", "backup")
	if st := e.sw.Status(); st.Error == "" || st.Home != "" {
		t.Fatalf("status: %+v", st)
	}
	fails := func() (n int) {
		for _, x := range e.ev.List(0) {
			if x.Type == "timed_switch_failed" {
				n++
			}
		}
		return n
	}
	e.at(time.Minute)
	if n := fails(); n != 1 {
		t.Fatalf("%d failure events in one window", n)
	}
	e.at(30 * time.Minute)
	if n := fails(); n != 2 {
		t.Fatalf("%d failure events after the next window", n)
	}
}

// A manual switch during a window stands: the timer does not take the farm
// anywhere at the end of it.
func TestManualSwitchDuringWindowStands(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	if _, err := e.mgr.Activate(context.Background(), "backup", true); err != nil {
		t.Fatal(err)
	}
	e.at(10 * time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "backup" || e.sw.Home() != "" {
		t.Fatalf("active %s, home %q", s.Active, e.sw.Home())
	}
}

// Turning the timer off, or unmarking the target, ends the window at once.
func TestTurningOffReturns(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	e.want("solo", "main", "backup")
	p, err := e.set.Prepare(map[string]json.RawMessage{"timed_switch": json.RawMessage(`"off"`)})
	if err != nil {
		t.Fatal(err)
	}
	e.set.Commit(p)
	e.at(2 * time.Minute)
	e.want("main", "backup")

	e2 := newEnv(t, stratumPool(t))
	e2.at(time.Second)
	no := false
	if _, _, err := e2.mgr.Update("solo", pool.Input{TimedTarget: &no}, false); err != nil {
		t.Fatal(err)
	}
	e2.at(2 * time.Minute)
	e2.want("main", "backup")
}

// A restart in the middle of a window keeps the way back; a restart after
// it returns the farm right away.
func TestRestartKeepsTheWayBack(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	e.sw = e.restart()
	e.at(5 * time.Minute)
	e.want("solo", "main", "backup")

	e.sw = e.restart()
	e.at(25 * time.Minute) // the proxy was down when the window ended
	e.want("main", "backup")
}

// A crash right after the switch, before the record was completed, still
// brings the farm back.
func TestCrashRightAfterSwitchReturns(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	// What leave saves before switching, then the switch itself.
	e.sw.save(&away{Start: t0, Target: "solo", Home: "main", Fallback: []string{"backup"}})
	if _, err := e.mgr.ActivateFor(context.Background(), "solo", "timer"); err != nil {
		t.Fatal(err)
	}
	e.sw = e.restart()
	e.at(15 * time.Minute)
	if s := e.mgr.Snapshot(); s.Active != "main" {
		t.Fatalf("active %s after the window", s.Active)
	}
}

// Changing the fallback order during a window keeps the new order.
func TestFallbackEditedDuringWindowIsKept(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	if err := e.mgr.SetFallback([]string{"backup", "main"}); err != nil {
		t.Fatal(err)
	}
	e.at(10 * time.Minute)
	e.want("main", "backup")
}

func TestStatus(t *testing.T) {
	e := newEnv(t, stratumPool(t))
	e.at(time.Second)
	st := e.sw.Status()
	if st.Mode != "on" || st.Target != "solo" || st.Home != "main" || st.Period != "30m" || st.Duration != "10m" ||
		st.Until == nil || !st.Until.Equal(t0.Add(10*time.Minute)) || st.Next == nil || !st.Next.Equal(t0.Add(30*time.Minute)) {
		t.Fatalf("in a window: %+v", st)
	}
	e.at(12 * time.Minute)
	if st := e.sw.Status(); st.Home != "" || st.Until != nil {
		t.Fatalf("after the window: %+v", st)
	}
}
