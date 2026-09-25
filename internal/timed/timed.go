// Package timed moves the farm to one pool for part of every period and
// back, for example to a solo pool for 10 minutes every 30 minutes.
//
// Windows start at multiples of timed_period (every 30 minutes: at :00 and
// :30) and last timed_duration. At the start of a window the farm switches
// to the pool marked as the timed target, with the usual pool check; at the
// end it switches back to the pool that was active before and the fallback
// order from before is restored. The operator's own choice wins: if the
// active pool changes during a window, the switcher leaves it alone.
package timed

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// tick is how often the schedule is checked.
const tick = 5 * time.Second

type Deps struct {
	Settings *settings.Store
	Pools    *pool.Manager
	Events   *events.Log
	Path     string // state file, e.g. /data/timed.json
}

// away is a switch to the target that is to be undone. It is saved, so a
// restart in the middle of a window still brings the farm back.
type away struct {
	Start    time.Time `json:"start"`    // start of the window
	Target   string    `json:"target"`   // pool switched to
	Home     string    `json:"home"`     // pool to return to
	Fallback []string  `json:"fallback"` // fallback order before the switch
	Set      []string  `json:"set"`      // fallback order right after it
}

type saved struct {
	Away *away `json:"away"`
}

type Switcher struct {
	d   Deps
	now func() time.Time

	mu      sync.Mutex
	cur     *away     // set while the farm is on the target for a window
	tried   time.Time // start of the last window a switch was tried in
	lastErr string    // why the last switch to the target failed
}

func New(d Deps) *Switcher {
	s := &Switcher{d: d, now: time.Now}
	b, err := os.ReadFile(d.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("timed switching: cannot read the state file", "err", err)
	}
	var st saved
	if len(b) > 0 && json.Unmarshal(b, &st) == nil && st.Away != nil {
		s.cur, s.tried = st.Away, st.Away.Start
	}
	return s
}

// window returns the start of the period now is in and whether now is
// inside the window at its beginning.
func window(now time.Time, period, length time.Duration) (time.Time, bool) {
	start := now.Truncate(period)
	return start, now.Sub(start) < length
}

// target is the pool marked for timed switching, "" if none.
func target(snap *pool.Snapshot) string {
	for _, p := range snap.Pools {
		if p.TimedTarget {
			return p.ID
		}
	}
	return ""
}

func name(snap *pool.Snapshot, id string) string {
	if p, ok := snap.Get(id); ok {
		return p.Name
	}
	return id
}

// Home is the pool the farm returns to while it is on the timed target, and
// "" otherwise. Profit switching compares coins for that pool and holds its
// scheduled checks until the farm is back.
func (s *Switcher) Home() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return ""
	}
	return s.cur.Home
}

// Run follows the schedule until ctx is done. A shutdown does not return
// the farm: the saved state brings it back after the restart if the window
// is over by then.
func (s *Switcher) Run(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		s.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// step does what the schedule asks for now.
func (s *Switcher) step(ctx context.Context) {
	v := s.d.Settings.Get()
	snap := s.d.Pools.Snapshot()
	tgt := target(snap)
	start, in := window(s.now(), v.TimedPeriod, v.TimedDuration)
	on := v.TimedSwitch == settings.TimedOn && tgt != ""

	s.mu.Lock()
	cur, tried := s.cur, s.tried
	s.mu.Unlock()

	if cur != nil {
		if on && in && start.Equal(cur.Start) && cur.Target == tgt {
			return // still in this window
		}
		s.back(ctx, cur, snap)
		return
	}
	// One try per window: a target that fails its check is not hammered.
	if !on || !in || start.Equal(tried) {
		return
	}
	s.mu.Lock()
	s.tried, s.lastErr = start, ""
	s.mu.Unlock()
	if snap.Active == "" || snap.Active == tgt {
		return // nothing to return to, or on the target already by hand
	}
	s.leave(ctx, start, tgt, snap)
}

// leave moves the farm to the target for the window that began at start.
func (s *Switcher) leave(ctx context.Context, start time.Time, tgt string, snap *pool.Snapshot) {
	a := &away{Start: start, Target: tgt, Home: snap.Active, Fallback: slices.Clone(snap.Fallback)}
	// Saved before the switch: a crash right after it still finds the way
	// back. If the switch does not happen, back() finds the farm where it
	// was and just forgets the record.
	s.save(a)
	if _, err := s.d.Pools.ActivateFor(ctx, tgt, "timer"); err != nil {
		s.save(nil)
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		s.d.Events.Warn("timed_switch_failed", "timed switching: cannot switch to %s, staying on %s: %v", name(snap, tgt), name(snap, a.Home), err)
		return
	}
	done := *a
	done.Set = slices.Clone(s.d.Pools.Snapshot().Fallback)
	s.save(&done)
}

// back returns the farm from the target to the pool it came from.
func (s *Switcher) back(ctx context.Context, a *away, snap *pool.Snapshot) {
	switch _, homeExists := snap.Get(a.Home); {
	case snap.Active != a.Target:
		// Switched by hand or by profit switching meanwhile: that stands.
	case !homeExists:
		s.d.Events.Warn("timed_switch_failed", "timed switching: pool %s to return to was deleted, staying on %s", a.Home, name(snap, a.Target))
	default:
		fallback := a.Fallback
		if !slices.Equal(snap.Fallback, a.Set) {
			fallback = snap.Fallback // the operator changed the order meanwhile
		}
		if _, err := s.d.Pools.Restore(ctx, a.Home, fallback, "timer"); err != nil {
			// Only a failed write of state.json gets here: retry on the next tick.
			s.d.Events.Warn("timed_switch_failed", "timed switching: cannot return to %s: %v", name(snap, a.Home), err)
			return
		}
	}
	s.save(nil)
}

func (s *Switcher) save(a *away) {
	s.mu.Lock()
	s.cur = a
	s.mu.Unlock()
	b, err := json.MarshalIndent(saved{Away: a}, "", "  ")
	if err == nil {
		err = atomicfile.Write(s.d.Path, append(b, '\n'), 0o600)
	}
	if err != nil {
		slog.Warn("timed switching: cannot save the state file", "err", err)
	}
}

// Status is what the API shows.
type Status struct {
	Mode     string     `json:"mode"`
	Period   string     `json:"period"`
	Duration string     `json:"duration"`
	Target   string     `json:"target"`          // pool id, "" when no pool is marked
	Home     string     `json:"home,omitempty"`  // while on the target: the pool to return to
	Until    *time.Time `json:"until,omitempty"` // while on the target: end of the window
	Next     *time.Time `json:"next,omitempty"`  // start of the next window
	Error    string     `json:"error,omitempty"` // why the last switch to the target failed
}

func (s *Switcher) Status() Status {
	v := s.d.Settings.Get()
	st := Status{
		Mode: v.TimedSwitch, Period: settings.FormatDuration(v.TimedPeriod),
		Duration: settings.FormatDuration(v.TimedDuration), Target: target(s.d.Pools.Snapshot()),
	}
	start, _ := window(s.now(), v.TimedPeriod, v.TimedDuration)
	if v.TimedSwitch == settings.TimedOn && st.Target != "" {
		next := start.Add(v.TimedPeriod)
		st.Next = &next
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil {
		until := s.cur.Start.Add(v.TimedDuration)
		st.Home, st.Until = s.cur.Home, &until
	}
	st.Error = s.lastErr
	return st
}
