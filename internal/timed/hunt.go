package timed

// Block hunting on eCash.
//
// A block found right after the previous one has to meet a far harder
// real-time target (package rtt): the first minute or two after every block
// are practically dead for a solo miner, and after that a block is no
// harder than its header says until the next one (the header target moves
// only from block to block). So the farm goes to the eCash solo pool once a
// block is at most resumeBelow times harder than its header, and comes back
// as soon as any block arrives, found by the farm or anyone else. Every
// block costs two reconnects of the farm.
//
// Hunting runs only while the eCash blocks are seen as they arrive
// (Deps.Live): without that it could not know when to leave. A pool chosen
// by hand stands: hunting waits for the next block before it moves the farm
// again.

import (
	"context"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/rtt"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

const (
	// huntRetry is the pause after a failed switch to the hunt pool.
	huntRetry = 5 * time.Minute
	// huntDay is how long finished hunts count in the status.
	huntDay = 24 * time.Hour
	// maxStints bounds the kept hunts: one a minute for a day at most.
	maxStints = 1440
)

// hunter is the state of block hunting, guarded by Switcher.mu.
type hunter struct {
	hold   bool      // a pool was chosen by hand: wait for the next block
	retry  time.Time // no new try before this, after a failed switch
	err    string    // why the last switch to the hunt pool failed
	stints []stint   // finished hunts within huntDay, oldest first
}

type stint struct{ from, to time.Time }

func (h *hunter) record(from, to time.Time) {
	h.stints = append(h.stints, stint{from, to})
	h.prune(to)
}

func (h *hunter) prune(now time.Time) {
	i := 0
	for i < len(h.stints) && (now.Sub(h.stints[i].to) > huntDay || len(h.stints)-i > maxStints) {
		i++
	}
	h.stints = h.stints[i:]
}

// huntTarget is the pool block hunting goes to: an eCash solo pool, the
// timer's if it is one. The RTT watcher (rtt.Watcher.pick) prefers the same
// pool; any eCash pool would show it the same blocks.
func huntTarget(snap *pool.Snapshot) (state.Pool, bool) {
	var first *state.Pool
	for i := range snap.Pools {
		p := &snap.Pools[i]
		if p.Coin != rtt.Coin || !p.Solo {
			continue
		}
		if p.TimedTarget {
			return *p, true
		}
		if first == nil {
			first = p
		}
	}
	if first == nil {
		return state.Pool{}, false
	}
	return *first, true
}

// huntView is what block hunting sees at one step.
type huntView struct {
	on     bool // turned on, with a pool to go to
	target state.Pool
	hard   float64 // how many times harder than its header a block is now
	live   bool    // the eCash blocks are seen as they arrive
	hold   bool
	retry  time.Time
}

func (s *Switcher) huntView(v *settings.Values, snap *pool.Snapshot) huntView {
	p, ok := huntTarget(snap)
	h := huntView{on: v.HuntSwitch == settings.HuntOn && ok, target: p, hard: 1}
	if h.on {
		h.hard = s.hardness(p)
		h.live = s.d.Live != nil && s.d.Live(p)
	}
	s.mu.Lock()
	h.hold, h.retry = s.hunt.hold, s.hunt.retry
	s.mu.Unlock()
	return h
}

// wants reports whether hunting would move the farm to its pool now.
func (h huntView) wants(now time.Time) bool {
	return h.on && h.live && !h.hold && !now.Before(h.retry) && h.hard <= resumeBelow
}

// keeps reports whether hunting would keep the farm on a.Target, where it
// is for the timer: no block since, so no reason to go home and back.
func (h huntView) keeps(a *away) bool {
	return h.on && h.live && !h.hold && a.Target == h.target.ID && h.hard < pauseAbove
}

// maybeHunt moves the farm to the hunt pool when the time is right.
func (s *Switcher) maybeHunt(ctx context.Context, snap *pool.Snapshot, h huntView, now time.Time) {
	if !h.wants(now) || snap.Active == "" || snap.Active == h.target.ID {
		return // nothing to return to, or on the pool already
	}
	s.leave(ctx, &away{Reason: reasonHunt, Since: now, Target: h.target.ID}, snap)
}

// stepHunt decides about a hunt in progress.
func (s *Switcher) stepHunt(ctx context.Context, cur *away, snap *pool.Snapshot, h huntView, timerDue bool, tp state.Pool, period, now time.Time) {
	home := name(snap, cur.Home)
	switch {
	case snap.Active != cur.Target:
		s.back(ctx, cur, snap, true) // switched by hand: that stands
	case timerDue && tp.ID == cur.Target:
		s.handOver(cur, reasonTimer, period, now, snap)
	case timerDue:
		// The timer comes first: home now, to its pool at the next step.
		s.back(ctx, cur, snap, true)
		s.Kick()
	case !h.on || cur.Target != h.target.ID:
		s.back(ctx, cur, snap, true)
	case !h.live:
		s.d.Events.Throttled("hunt_blind", 10*time.Minute, "hunt_blind",
			"block hunt: eCash blocks are not seen now, back to %s until they are", home)
		s.back(ctx, cur, snap, true)
	case h.hard >= pauseAbove:
		s.d.Events.Info("hunt_block", "block hunt: a block arrived on eCash after %s on %s, back to %s",
			now.Sub(cur.since()).Round(time.Second), name(snap, cur.Target), home)
		s.back(ctx, cur, snap, true)
	}
}

// Kick makes the next step happen at once, for example when a block arrives.
func (s *Switcher) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// HuntStatus is block hunting in the API.
type HuntStatus struct {
	Mode     string     `json:"mode"`
	Target   string     `json:"target"`          // the eCash solo pool, "" when there is none
	Live     bool       `json:"live"`            // eCash blocks are seen as they arrive
	Hardness float64    `json:"hardness"`        // how many times harder than its header a block is now
	Since    *time.Time `json:"since,omitempty"` // hunting now: since when
	Home     string     `json:"home,omitempty"`  // hunting now: the pool to return to
	Hold     bool       `json:"hold"`            // a pool was chosen by hand: waits for the next block
	Stints   int        `json:"stints"`          // hunts in the last 24 hours, the current one too
	Seconds  float64    `json:"seconds"`         // time hunting in the last 24 hours
	Error    string     `json:"error,omitempty"` // why the last switch to the hunt pool failed
	Retry    *time.Time `json:"retry,omitempty"` // paused after that failure until then
}

func (s *Switcher) huntStatus(v *settings.Values, snap *pool.Snapshot, now time.Time) HuntStatus {
	p, ok := huntTarget(snap)
	st := HuntStatus{Mode: v.HuntSwitch, Target: p.ID, Hardness: 1}
	if ok {
		st.Hardness = s.hardness(p)
		st.Live = s.d.Live != nil && s.d.Live(p)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st.Hold, st.Error = s.hunt.hold, s.hunt.err
	if now.Before(s.hunt.retry) {
		retry := s.hunt.retry
		st.Retry = &retry
	}
	s.hunt.prune(now)
	for _, x := range s.hunt.stints {
		st.Stints++
		st.Seconds += x.to.Sub(x.from).Seconds()
	}
	if s.cur != nil && s.cur.Reason == reasonHunt {
		since := s.cur.since()
		st.Since, st.Home = &since, s.cur.Home
		st.Stints++
		st.Seconds += now.Sub(since).Seconds()
	}
	return st
}
