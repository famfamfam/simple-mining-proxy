// Package timed moves the farm to one pool for part of every period and
// back, for example to a solo pool for 10 minutes of every 30.
//
// Periods start at multiples of timed_period (every 30 minutes: at :00 and
// :30). StartNow restarts the schedule by hand: the period begins at once,
// and the next ones a period, two periods and so on later.
// In each the farm spends timed_duration on the pool marked as the
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
//
// Block hunting (hunt.go) uses the same way there and back for eCash: the
// farm goes to the eCash solo pool whenever a block is no harder than its
// header says and returns as soon as a block arrives. The farm is away for
// one reason at a time; the timer comes first, and when both want the same
// pool the farm stays and only the reason changes.
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
	// Live, when set, tells whether blocks on the pool's chain are seen as
	// they arrive. Block hunting runs only then: it leaves at a block.
	Live func(p state.Pool) bool
}

// Why the farm is away from its pool.
const (
	reasonTimer = "" // the first files had no reason
	reasonHunt  = "hunt"
)

// away is a switch to the target that is to be undone. It is saved, so a
// restart while the farm is there still brings it back.
type away struct {
	Reason   string    `json:"reason,omitempty"` // reasonTimer or reasonHunt
	Period   time.Time `json:"start"`            // start of the timer period; zero for a hunt
	Since    time.Time `json:"since"`            // when the farm went to the target
	Target   string    `json:"target"`           // pool switched to
	Home     string    `json:"home"`             // pool to return to
	Fallback []string  `json:"fallback"`         // fallback order before the switch
	Set      []string  `json:"set"`              // fallback order right after it
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
	Anchor time.Time `json:"anchor,omitzero"` // periods run from here on; zero: at multiples of the period
}

type Switcher struct {
	d    Deps
	now  func() time.Time
	kick chan struct{} // a step is due at once

	stepMu sync.Mutex // one step at a time: the schedule and StartNow

	mu      sync.Mutex
	anchor  time.Time     // set by StartNow: periods run from here on
	cur     *away         // set while the farm is away, for either reason
	period  time.Time     // current period
	spent   time.Duration // on the target in finished stints of period
	done    bool          // no more stints in period: time used, failed, or overridden by hand
	waiting bool          // held off by the real-time target
	lastErr string        // why the last switch to the target failed
	hunt    hunter        // block hunting, see hunt.go
}

func New(d Deps) *Switcher {
	s := &Switcher{d: d, now: time.Now, kick: make(chan struct{}, 1)}
	b, err := os.ReadFile(d.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("timed switching: cannot read the state file", "err", err)
	}
	var st saved
	if len(b) > 0 && json.Unmarshal(b, &st) == nil {
		s.cur, s.period, s.spent, s.done = st.Away, st.Period, time.Duration(st.Spent*float64(time.Second)), st.Done
		s.anchor = st.Anchor
		if s.cur != nil && s.period.IsZero() {
			s.period = s.cur.Period
		}
	}
	return s
}

// periodStart is the start of the period now is in: periods run from
// anchor on, or at multiples of the period without one.
func periodStart(now, anchor time.Time, period time.Duration) time.Time {
	if anchor.IsZero() || now.Before(anchor) {
		return now.Truncate(period)
	}
	return anchor.Add(now.Sub(anchor).Truncate(period))
}

func (s *Switcher) periodOf(now time.Time, period time.Duration) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return periodStart(now, s.anchor, period)
}

var (
	// ErrOff is returned by StartNow while timed switching is off.
	ErrOff = errors.New("timed switching is off")
	// ErrNoTarget is returned by StartNow when no pool is marked for it.
	ErrNoTarget = errors.New("no pool is marked for timed switching")
)

// StartNow restarts the schedule from now: a period begins at once with all
// of its time on the target due, so the farm goes there right away (on
// eCash, once a block would not be much harder than its header says), and
// the next period starts one period later. On the target already, its time
// there starts over.
func (s *Switcher) StartNow() error {
	v := s.d.Settings.Get()
	tp, ok := target(s.d.Pools.Snapshot())
	switch {
	case v.TimedSwitch != settings.TimedOn:
		return ErrOff
	case !ok:
		return ErrNoTarget
	}
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	now := s.now()
	s.mu.Lock()
	s.anchor, s.period, s.spent, s.done, s.lastErr = now, now, 0, false, ""
	cur := s.cur
	s.mu.Unlock()
	switch {
	case cur == nil:
	case cur.Reason == reasonTimer:
		next := *cur
		next.Period, next.Since = now, now
		cur = &next
	case cur.Target == tp.ID:
		// Hunting on the timer's pool: the farm stays, now for the timer.
		s.ended(cur, true)
		next := *cur
		next.Reason, next.Period, next.Since = reasonTimer, now, now
		cur = &next
	}
	// Hunting elsewhere: the next step brings the farm home for the timer.
	s.save(cur)
	s.d.Events.Info("timed_restart", "timed switching restarted by hand: %s for %s from now, then every %s",
		tp.Name, settings.FormatDuration(v.TimedDuration), settings.FormatDuration(v.TimedPeriod))
	s.Kick()
	return nil
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

// Home is the pool the farm returns to while it is away, on the timer or on
// a block hunt, and "" otherwise. Profit switching compares coins for that
// pool and holds its scheduled checks until the farm is back.
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
		case <-s.kick:
		}
	}
}

// step does what the schedule and block hunting ask for now.
func (s *Switcher) step(ctx context.Context) {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	v := s.d.Settings.Get()
	snap := s.d.Pools.Snapshot()
	tp, hasTarget := target(snap)
	now := s.now()
	period := s.periodOf(now, v.TimedPeriod)
	on := v.TimedSwitch == settings.TimedOn && hasTarget
	hard := 1.0
	if on {
		hard = s.hardness(tp)
	}
	h := s.huntView(v, snap)

	s.mu.Lock()
	if !period.Equal(s.period) {
		s.period, s.spent, s.done, s.lastErr = period, 0, false, ""
	}
	cur, done := s.cur, s.done
	left := v.TimedDuration - s.spent
	if cur != nil && cur.Reason == reasonTimer && cur.Period.Equal(period) {
		left -= now.Sub(cur.since())
	}
	s.waiting = false
	if !h.on || h.hard >= pauseAbove {
		// A block ends the wait after a pool chosen by hand. With hunting
		// off there is nothing to wait for: turning it on is a fresh start.
		s.hunt.hold = false
	}
	s.mu.Unlock()

	// The timer wants the farm on its pool now.
	periodLeft := period.Add(v.TimedPeriod).Sub(now)
	timerDue := on && !done && left >= minStint && periodLeft >= minStint && hard <= resumeBelow

	if cur != nil && cur.Reason == reasonHunt {
		s.stepHunt(ctx, cur, snap, h, timerDue, tp, period, now)
		return
	}
	if cur != nil {
		switch {
		case snap.Active != cur.Target:
			s.back(ctx, cur, snap, true) // switched away by hand
		case !cur.Period.Equal(period) && on && cur.Target == tp.ID:
			// A new period while on the target: its time begins now, no
			// reason to leave and come back.
			if hard < pauseAbove {
				next := *cur
				next.Period, next.Since = period, now
				s.save(&next)
			} else {
				s.back(ctx, cur, snap, false)
			}
		case !on, cur.Target != tp.ID, left <= 0:
			// Turned off, another target, or the time is used up: block
			// hunting may keep the farm there rather than bounce it.
			if h.keeps(cur) {
				s.handOver(cur, reasonHunt, time.Time{}, now, snap)
			} else {
				s.back(ctx, cur, snap, true)
			}
		case hard >= pauseAbove && left >= minStint:
			s.d.Events.Info("timed_pause", "timed switching: a block just arrived, %s would be x%.0f harder: back to %s until it eases",
				tp.Name, hard, name(snap, cur.Home))
			s.back(ctx, cur, snap, false)
		}
		return
	}

	if on && !done && left >= minStint && periodLeft >= minStint {
		if hard > resumeBelow {
			s.mu.Lock()
			s.waiting = true
			s.mu.Unlock()
		} else if snap.Active == "" || snap.Active == tp.ID {
			s.finish() // nothing to return to, or on the target already by hand
			return
		} else {
			s.leave(ctx, &away{Reason: reasonTimer, Period: period, Since: now, Target: tp.ID}, snap)
			return
		}
	}
	s.maybeHunt(ctx, snap, h, now)
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

// reasonName is the switch reason shown in the events and the last switch.
func reasonName(reason string) string {
	if reason == reasonHunt {
		return "hunt"
	}
	return "timer"
}

// leave moves the farm from the active pool to a.Target.
func (s *Switcher) leave(ctx context.Context, a *away, snap *pool.Snapshot) {
	a.Home, a.Fallback = snap.Active, slices.Clone(snap.Fallback)
	// Saved before the switch: a crash right after it still finds the way
	// back. If the switch does not happen, back() finds the farm where it
	// was and just forgets the record.
	s.save(a)
	if _, err := s.d.Pools.ActivateFor(ctx, a.Target, reasonName(a.Reason)); err != nil {
		s.mu.Lock()
		if a.Reason == reasonHunt {
			// A pause, so a pool that fails its check is not hammered.
			s.hunt.err, s.hunt.retry = err.Error(), s.now().Add(huntRetry)
		} else {
			// One try per period.
			s.lastErr, s.done = err.Error(), true
		}
		s.mu.Unlock()
		s.save(nil)
		s.d.Events.Warn("timed_switch_failed", "%s: cannot switch to %s, staying on %s: %v",
			reasonName(a.Reason), name(snap, a.Target), name(snap, a.Home), err)
		return
	}
	if a.Reason == reasonHunt {
		s.mu.Lock()
		s.hunt.err = ""
		s.mu.Unlock()
	}
	done := *a
	done.Set = slices.Clone(s.d.Pools.Snapshot().Fallback)
	s.save(&done)
}

// back returns the farm from the target to the pool it came from. With
// final, the timer period has no more stints; otherwise it is a pause.
func (s *Switcher) back(ctx context.Context, a *away, snap *pool.Snapshot, final bool) {
	switch _, homeExists := snap.Get(a.Home); {
	case snap.Active != a.Target:
		// Switched by hand meanwhile: that stands, and block hunting waits
		// for the next block before it moves the farm again (step drops the
		// wait while hunting is off: turning it on later starts it afresh).
		final = true
		s.mu.Lock()
		s.hunt.hold = true
		s.mu.Unlock()
	case !homeExists:
		s.d.Events.Warn("timed_switch_failed", "%s: pool %s to return to was deleted, staying on %s", reasonName(a.Reason), a.Home, name(snap, a.Target))
		final = true
	default:
		fallback := a.Fallback
		if !slices.Equal(snap.Fallback, a.Set) {
			fallback = snap.Fallback // the operator changed the order meanwhile
		}
		if _, err := s.d.Pools.Restore(ctx, a.Home, fallback, reasonName(a.Reason)); err != nil {
			// Only a failed write of state.json gets here: retry on the next tick.
			s.d.Events.Warn("timed_switch_failed", "%s: cannot return to %s: %v", reasonName(a.Reason), name(snap, a.Home), err)
			return
		}
	}
	s.ended(a, final)
	s.save(nil)
}

// ended books a stint that is over, by back or by a hand-over.
func (s *Switcher) ended(a *away, final bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if a.Reason == reasonHunt {
		s.hunt.record(a.since(), now)
		return
	}
	if a.Period.Equal(s.period) {
		s.spent += now.Sub(a.since())
		if final {
			s.done = true
		}
	}
}

// handOver keeps the farm on a.Target for another reason: the timer and
// block hunting want the same pool, so it does not bounce home and back.
func (s *Switcher) handOver(a *away, reason string, period, now time.Time, snap *pool.Snapshot) {
	s.ended(a, true)
	next := *a
	next.Reason, next.Period, next.Since = reason, period, now
	s.save(&next)
	s.d.Events.Info("timed_handover", "%s: the farm stays on %s for %s", reasonName(a.Reason), name(snap, a.Target), reasonName(reason))
}

func (s *Switcher) save(a *away) {
	s.mu.Lock()
	s.cur = a
	st := saved{Away: a, Period: s.period, Spent: s.spent.Seconds(), Done: s.done, Anchor: s.anchor}
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
	Hunt     HuntStatus `json:"hunt"`
}

func (s *Switcher) Status() Status {
	v := s.d.Settings.Get()
	snap := s.d.Pools.Snapshot()
	tp, hasTarget := target(snap)
	st := Status{
		Mode: v.TimedSwitch, Period: settings.FormatDuration(v.TimedPeriod),
		Duration: settings.FormatDuration(v.TimedDuration), Target: tp.ID, Hardness: 1,
	}
	now := s.now()
	st.Hunt = s.huntStatus(v, snap, now)
	period := s.periodOf(now, v.TimedPeriod)
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
	if s.cur != nil && s.cur.Reason == reasonTimer {
		if s.cur.Period.Equal(period) {
			left -= now.Sub(s.cur.since())
		}
		until := now.Add(max(left, 0))
		if end := period.Add(v.TimedPeriod); until.After(end) {
			// At the boundary the farm stays for the next period's time.
			until = end.Add(v.TimedDuration)
		}
		st.Home, st.Until = s.cur.Home, &until
	}
	st.Left = max(left, 0).Seconds()
	st.Waiting = on && s.waiting
	st.Error = s.lastErr
	return st
}
