// Package monitor tells when an ASIC stops mining and when it comes back.
//
// A working ASIC sends shares all the time, every few seconds at the usual
// pool difficulty. So an ASIC is offline when no share has come from it for
// offline_after, whatever the reason: powered off, hung with the connection
// still open, cut off from the network or mining somewhere else. The TCP
// connection alone says little: a powered-off ASIC leaves it open until a
// timeout.
//
// ASICs are told apart by their login (worker name); ASICs that share a
// login are watched as one.
package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
)

const (
	// tick is how often the sessions are checked.
	tick = 5 * time.Second
	// startGrace: after the proxy starts, ASICs get this long to reconnect
	// before any of them is reported offline.
	startGrace = 5 * time.Minute
	// settle: a session this old gives a usable hashrate estimate.
	settle = 2 * time.Minute
	// shareGaps: an ASIC that shares rarely (low hashrate, high share
	// difficulty) is offline only after this many of its usual intervals
	// between shares, so a long gap between two shares is not an alarm.
	shareGaps = 20
	// maxWait caps the wait an ASIC's own share rate asks for.
	maxWait = time.Hour
	// forgetAfter: an ASIC offline this long is dropped from the list.
	forgetAfter = 7 * 24 * time.Hour
	// maxWorkers bounds the list against a flood of made-up logins.
	maxWorkers = 10000
	// saveEvery: how often the last-share times are saved while nothing
	// else changes, so a restart knows how long an ASIC has been silent.
	saveEvery = time.Minute
	// maxEventNames: longer lists in an event end with "and N more".
	maxEventNames = 20
)

type Deps struct {
	Settings *settings.Store
	// Miners returns the live sessions.
	Miners func(now time.Time) []session.Info
	Events *events.Log
	Path   string // state file; "" keeps nothing between restarts
	// Notify gets the ASICs that went offline or came back in one check;
	// nil when the events are enough.
	Notify func(Alert)
}

// Change is an ASIC that went offline or came back.
type Change struct {
	Name   string
	Online bool // true: came back
	// LastShare is when the ASIC sent its last share before it went silent.
	LastShare time.Time
	// Away is, for an ASIC that came back, how long it sent no shares.
	Away time.Duration
	Pool string // the pool of its last share
}

// Alert is what one check found, with the totals after it.
type Alert struct {
	Time    time.Time
	Changes []Change // offline first, then by name
	Online  int
	Total   int
}

// Miner is one watched ASIC.
type Miner struct {
	Name      string    `json:"name"`
	Online    bool      `json:"online"`
	LastShare time.Time `json:"last_share"`
	Hashrate  float64   `json:"hashrate_hs"` // now, from the live sessions
	Pool      string    `json:"pool"`        // the pool of its last share
}

type worker struct {
	LastShare time.Time `json:"last_share"`
	Offline   bool      `json:"offline,omitempty"`
	Pool      string    `json:"pool,omitempty"`

	hashrate   float64 // H/s of the settled sessions, for the share interval
	current    float64 // H/s of all live sessions
	difficulty float64 // share difficulty now
}

type saved struct {
	Workers map[string]*worker `json:"workers"`
}

type Monitor struct {
	d       Deps
	now     func() time.Time
	started time.Time

	mu      sync.Mutex
	workers map[string]*worker
	dirty   bool      // changed since the last save
	savedAt time.Time // last save
}

func New(d Deps) *Monitor {
	m := &Monitor{d: d, now: time.Now, workers: map[string]*worker{}}
	m.started = m.now()
	if d.Path == "" {
		return m
	}
	b, err := os.ReadFile(d.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("monitor: cannot read the state file", "err", err)
	}
	var st saved
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st); err != nil {
			slog.Warn("monitor: the state file is damaged, starting afresh", "err", err)
		}
	}
	for name, w := range st.Workers {
		if w != nil && name != "" && len(m.workers) < maxWorkers {
			m.workers[name] = w
		}
	}
	return m
}

// Run checks the sessions until ctx is done.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.save(m.now())
			return
		case <-t.C:
			m.check(m.now())
		}
	}
}

// live is what the sessions of one login show now.
type live struct {
	last       time.Time // latest share sent
	pool       string    // pool of that share
	settled    float64   // H/s of the sessions old enough to estimate it
	current    float64   // H/s of all sessions
	difficulty float64
}

func (m *Monitor) check(now time.Time) {
	base := m.d.Settings.Get().OfflineAfter
	seen := map[string]*live{}
	for _, in := range m.d.Miners(now) {
		if in.Worker == "" {
			continue
		}
		l := seen[in.Worker]
		if l == nil {
			l = &live{}
			seen[in.Worker] = l
		}
		if in.LastSubmit != nil && in.LastSubmit.After(l.last) {
			l.last, l.pool = *in.LastSubmit, in.PoolName
		}
		l.current += in.HashrateHs
		if now.Sub(in.ConnectedAt) >= settle {
			l.settled += in.HashrateHs
		}
		l.difficulty = max(l.difficulty, in.Difficulty)
	}

	m.mu.Lock()
	var changes []Change
	for name, l := range seen {
		w := m.workers[name]
		if w == nil {
			// Watched from its first share: a session that never sends one
			// (a failed login, a probe) is not an ASIC that went offline.
			if l.last.IsZero() || len(m.workers) >= maxWorkers {
				continue
			}
			w = &worker{}
			m.workers[name] = w
		}
		w.current, w.difficulty = l.current, l.difficulty
		if l.settled > 0 {
			w.hashrate = l.settled
		}
		if l.last.After(w.LastShare) {
			if w.Offline {
				w.Offline = false
				changes = append(changes, Change{Name: name, Online: true, LastShare: l.last, Away: l.last.Sub(w.LastShare), Pool: l.pool})
			}
			w.LastShare, w.Pool = l.last, l.pool
			m.dirty = true
		}
	}
	judge := now.Sub(m.started) >= startGrace
	online := 0
	for name, w := range m.workers {
		if _, ok := seen[name]; !ok {
			w.current = 0
		}
		silent := now.Sub(w.LastShare) > wait(w, base)
		switch {
		case !w.Offline && judge && silent:
			w.Offline = true
			changes = append(changes, Change{Name: name, LastShare: w.LastShare, Pool: w.Pool})
		case w.Offline && now.Sub(w.LastShare) > forgetAfter:
			delete(m.workers, name)
			m.dirty = true
		}
		// Same rule as Miners(): during the start grace w.Offline is not set
		// yet, but a silent ASIC should not count as mining in the alert.
		if !w.Offline && !silent {
			online++
		}
	}
	total := len(m.workers)
	save := m.dirty && (len(changes) > 0 || now.Sub(m.savedAt) >= saveEvery)
	m.mu.Unlock()

	if save {
		m.save(now)
	}
	if len(changes) == 0 {
		return
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Online != changes[j].Online {
			return !changes[i].Online
		}
		return NameLess(changes[i].Name, changes[j].Name)
	})
	m.report(now, changes)
	if m.d.Notify != nil {
		m.d.Notify(Alert{Time: now, Changes: changes, Online: online, Total: total})
	}
}

// wait is how long w may send no shares before it is offline: base, or
// shareGaps of its usual intervals between shares when that is longer.
func wait(w *worker, base time.Duration) time.Duration {
	if w.hashrate <= 0 || w.difficulty <= 0 {
		return base
	}
	gap := w.difficulty * stats.HashesPerDifficulty / w.hashrate // seconds
	own := min(shareGaps*gap, maxWait.Seconds())
	return max(base, time.Duration(own*float64(time.Second)))
}

func (m *Monitor) report(now time.Time, changes []Change) {
	var off, on []string
	for _, c := range changes {
		if c.Online {
			on = append(on, fmt.Sprintf("%s (no shares for %s)", c.Name, c.Away.Round(time.Second)))
		} else {
			off = append(off, fmt.Sprintf("%s (last share %s ago on %s)", c.Name, now.Sub(c.LastShare).Round(time.Second), c.Pool))
		}
	}
	if len(off) > 0 {
		m.d.Events.Warn("miner_offline", "ASIC offline, no shares: %s", joinNames(off))
	}
	if len(on) > 0 {
		m.d.Events.Info("miner_online", "ASIC back online: %s", joinNames(on))
	}
}

func joinNames(names []string) string {
	if len(names) <= maxEventNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:maxEventNames], ", "), len(names)-maxEventNames)
}

func (m *Monitor) save(now time.Time) {
	if m.d.Path == "" {
		return
	}
	m.mu.Lock()
	b, err := json.Marshal(saved{Workers: m.workers})
	m.dirty, m.savedAt = false, now
	m.mu.Unlock()
	if err == nil {
		err = atomicfile.Write(m.d.Path, append(b, '\n'), 0o600)
	}
	if err != nil {
		slog.Warn("monitor: cannot save the state file", "err", err)
	}
}

// Miners returns the watched ASICs: offline first, then by name. An ASIC
// silent for too long is shown offline even before it is reported (during
// the start grace or until the next check).
func (m *Monitor) Miners() []Miner {
	base, now := m.d.Settings.Get().OfflineAfter, m.now()
	m.mu.Lock()
	out := make([]Miner, 0, len(m.workers))
	for name, w := range m.workers {
		online := !w.Offline && now.Sub(w.LastShare) <= wait(w, base)
		out = append(out, Miner{Name: name, Online: online, LastShare: w.LastShare, Hashrate: w.current, Pool: w.Pool})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Online != out[j].Online {
			return !out[i].Online
		}
		return NameLess(out[i].Name, out[j].Name)
	})
	return out
}

// NameLess orders worker names the way people number ASICs: "s21-2"
// before "s21-10".
func NameLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digits(a), digits(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
