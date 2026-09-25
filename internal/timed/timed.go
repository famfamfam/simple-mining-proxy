// Package timed moves the farm to one pool for part of every period and
// back, for example to a solo pool for 10 minutes of every 30.
//
// Periods start at multiples of timed_period (every 30 minutes: at :00 and
// :30). In each the farm spends timed_duration on the pool marked as the
// timed target, from the start of the period on. The switch there uses the
// usual pool check; the switch back returns to the pool that was active
// before and restores the fallback order from before. The operator's own
// choice wins: if the active pool changes while the farm is on the target,
// the switcher leaves it alone for the rest of the period.
//
// On eCash a block found soon after the previous one has to meet a far
// harder real-time target (package rtt), so the first minutes after every
// block are practically wasted on solo. With Deps.Hardness the switcher does
// not go to the target while a block would be much harder than its header
// says, and leaves it for that time when a block arrives; the time on the
// target is made up later in the period.
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
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

const (
	// tick is how often the schedule is checked.
	tick = 5 * time.Second
	// pauseAbove: on the target, a block this many times harder than its
	// header target sends the farm back until it eases.
	pauseAbove = 2.0
	// resumeBelow: the farm goes (back) to the target once a block is at
	// most this many times harder.
	resumeBelow = 1.2
	// minStint: less time than this on the target is not worth two
	// reconnects of the farm.
	minStint = time.Minute
)

type Deps struct {
	Settings *settings.Store
	Pools    *pool.Manager
	Events   *events.Log
	Path     string // state file, e.g. /data/timed.json
	// Hardness, when set, tells how many times harder than its header
	// target a block on the pool's chain is right now (1 when it is not).
	Hardness func(p state.Pool) float64
}

// away is a switch to the target that is to be undone. It is saved, so a
// restart while the farm is there still brings it back.
type away struct {
	Period   time.Time `json:"start"`    // start of the period
	Since    time.Time `json:"since"`    // when the farm went to the target
	Target   string    `json:"target"`   // pool switched to
	Home     string    `json:"home"`     // pool to return to
	Fallback []string  `json:"fallback"` // fallback order before the switch
	Set      []string  `json:"set"`      // fallback order right after it
}

// since is when the stint began; files of the first version have only the
// period start.
func (a *away) since() time.Time {
	if a.Since.IsZero() {
		return a.Period
	}
	return a.Since
}

type saved struct {
	Away   *away     `json:"away"`
	Period time.Time `json:"period,omitzero"` // the period Spent and Done are for
	Spent  float64   `json:"spent,omitempty"` // seconds on the target in finished stints of Period
	Done   bool      `json:"done,omitempty"`  // no more stints in Period
}

type Switcher struct {
	d   Deps
	now func() time.Time

	mu      sync.Mutex
	cur     *away         // set while the farm is on the target
	period  time.Time     // current period
	spent   time.Duration // on the target in finished stints of period
	done    bool          // no more stints in period: time used, failed, or overridden by hand
	waiting bool          // held off by the real-time target
	lastErr string        // why the last switch to the target failed
}

func New(d Deps) *Switcher {
	s := &Switcher{d: d, now: time.Now}
	b, err := os.ReadFile(d.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("timed switching: cannot read the state file", "err", err)
	}
	var st saved
	if len(b) > 0 && json.Unmarshal(b, &st) == nil {
		s.cur, s.period, s.spent, s.done = st.Away, st.Period, time.Duration(st.Spent*float64(time.Second)), st.Done
		if s.cur != nil && s.period.IsZero() {
			s.period = s.cur.Period
		}
	}
	return s
}

// target is the pool marked for timed switching.
func target(snap *pool.Snapshot) (state.Pool, bool) {
	for _, p := range snap.Pools {
		if p.TimedTarget {
			return p, true
		}
	}
	return state.Pool{}, false
}

func name(snap *pool.Snapshot, id string) string {
	if p, ok := snap.Get(id); ok {
		return p.Name
	}
	return id
}

func (s *Switcher) hardness(p state.Pool) float64 {
	if s.d.Hardness == nil {
		return 1
	}
	return s.d.Hardness(p)
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
// the farm: the saved state brings it back after the restart if its time on
// the target is over by then.
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
	tp, hasTarget := target(snap)
	now := s.now()
	period := now.Truncate(v.TimedPeriod)
	on := v.TimedSwitch == settings.TimedOn && hasTarget
	hard := 1.0
	if on {
		hard = s.hardness(tp)
	}

	s.mu.Lock()
	if !period.Equal(s.period) {
		s.period, s.spent, s.done, s.lastErr = period, 0, false, ""
	}
	cur, done := s.cur, s.done
	left := v.TimedDuration - s.spent
	if cur != nil && cur.Period.Equal(period) {
		left -= now.Sub(cur.since())
	}
	s.waiting = false
	s.mu.Unlock()

	if cur != nil {
		switch {
		case snap.Active != cur.Target, !on, cur.Target != tp.ID:
			s.back(ctx, cur, snap, true) // switched away by hand, turned off, or another target
		case !cur.Period.Equal(period):
			// A new period while on the target: its time begins now, no
			// reason to leave and come back.
			if hard < pauseAbove {
				next := *cur
				next.Period, next.Since = period, now
				s.save(&next)
			} else {
				s.back(ctx, cur, snap, false)
			}
		case left <= 0:
			s.back(ctx, cur, snap, true)
		case hard >= pauseAbove && left >= minStint:
			s.d.Events.Info("timed_pause", "timed switching: a block just arrived, %s would be x%.0f harder: back to %s until it eases",
				tp.Name, hard, name(snap, cur.Home))
			s.back(ctx, cur, snap, false)
		}
		return
	}

	periodLeft := period.Add(v.TimedPeriod).Sub(now)
	if !on || done || left < minStint || periodLeft < minStint {
		return
	}
	if hard > resumeBelow {
		s.mu.Lock()
		s.waiting = true
		s.mu.Unlock()
		return
	}
	if snap.Active == "" || snap.Active == tp.ID {
		s.finish() // nothing to return to, or on the target already by hand
		return
	}
	s.leave(ctx, period, now, tp.ID, snap)
}

// finish ends the stints of the current period.
func (s *Switcher) finish() {
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
	s.save(s.current())
}

func (s *Switcher) current() *away {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// leave moves the farm to the target.
func (s *Switcher) leave(ctx context.Context, period, now time.Time, tgt string, snap *pool.Snapshot) {
	a := &away{Period: period, Since: now, Target: tgt, Home: snap.Active, Fallback: slices.Clone(snap.Fallback)}
	// Saved before the switch: a crash right after it still finds the way
	// back. If the switch does not happen, back() finds the farm where it
	// was and just forgets the record.
	s.save(a)
	if _, err := s.d.Pools.ActivateFor(ctx, tgt, "timer"); err != nil {
		// One try per period: a target that fails its check is not hammered.
		s.mu.Lock()
		s.lastErr, s.done = err.Error(), true
		s.mu.Unlock()
		s.save(nil)
		s.d.Events.Warn("timed_switch_failed", "timed switching: cannot switch to %s, staying on %s: %v", name(snap, tgt), name(snap, a.Home), err)
		return
	}
	done := *a
	done.Set = slices.Clone(s.d.Pools.Snapshot().Fallback)
	s.save(&done)
}

// back returns the farm from the target to the pool it came from. With
// final, the period has no more stints; otherwise it is a pause.
func (s *Switcher) back(ctx context.Context, a *away, snap *pool.Snapshot, final bool) {
	switch _, homeExists := snap.Get(a.Home); {
	case snap.Active != a.Target:
		// Switched by hand or by profit switching meanwhile: that stands.
		final = true
	case !homeExists:
		s.d.Events.Warn("timed_switch_failed", "timed switching: pool %s to return to was deleted, staying on %s", a.Home, name(snap, a.Target))
		final = true
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
	s.mu.Lock()
	if a.Period.Equal(s.period) {
		s.spent += s.now().Sub(a.since())
		if final {
			s.done = true
		}
	}
	s.mu.Unlock()
	s.save(nil)
}

func (s *Switcher) save(a *away) {
	s.mu.Lock()
	s.cur = a
	st := saved{Away: a, Period: s.period, Spent: s.spent.Seconds(), Done: s.done}
	s.mu.Unlock()
	b, err := json.MarshalIndent(st, "", "  ")
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
	Until    *time.Time `json:"until,omitempty"` // while on the target: when its time is up, without pauses
	Next     *time.Time `json:"next,omitempty"`  // start of the next period
	Left     float64    `json:"left"`            // seconds on the target still due in this period
	Waiting  bool       `json:"waiting"`         // held off: a block just arrived on the target's chain
	Hardness float64    `json:"hardness"`        // how many times harder a block on the target is now
	Error    string     `json:"error,omitempty"` // why the last switch to the target failed
}

func (s *Switcher) Status() Status {
	v := s.d.Settings.Get()
	tp, hasTarget := target(s.d.Pools.Snapshot())
	st := Status{
		Mode: v.TimedSwitch, Period: settings.FormatDuration(v.TimedPeriod),
		Duration: settings.FormatDuration(v.TimedDuration), Target: tp.ID, Hardness: 1,
	}
	now := s.now()
	period := now.Truncate(v.TimedPeriod)
	on := v.TimedSwitch == settings.TimedOn && hasTarget
	if on {
		next := period.Add(v.TimedPeriod)
		st.Next = &next
		st.Hardness = s.hardness(tp)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	left := v.TimedDuration
	if s.period.Equal(period) {
		left -= s.spent
		if s.done {
			left = 0
		}
	}
	if s.cur != nil {
		if s.cur.Period.Equal(period) {
			left -= now.Sub(s.cur.since())
		}
		until := now.Add(max(left, 0))
		st.Home, st.Until = s.cur.Home, &until
	}
	st.Left = max(left, 0).Seconds()
	st.Waiting = on && s.waiting
	st.Error = s.lastErr
	return st
}
