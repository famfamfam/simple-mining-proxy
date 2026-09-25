package admin

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
)

// Brute-force protection for an admin panel reachable from the internet.
//
//   - Per IP: ipFailLimit failures within ipFailWindow lock that IP. Each
//     further lock doubles, from ipLockBase up to ipLockMax. An IP that stays
//     quiet for lockForget starts over; a successful login clears it.
//   - Globally: globalFailLimit failures from all IPs within globalFailWindow
//     lock the password login for everyone until the window moves on. This
//     is what stops a botnet with many IPs. Existing sessions keep working.
//
// The state lives in a small JSON file next to state.json, so a restart does
// not reset the counters. It is written only when a failure, a lock or a
// successful login after failures changes it.
const (
	ipFailLimit      = 5
	ipFailWindow     = 15 * time.Minute
	ipLockBase       = 15 * time.Minute
	ipLockMax        = 24 * time.Hour
	lockForget       = 24 * time.Hour
	globalFailLimit  = 100
	globalFailWindow = time.Hour
	maxGuardIPs      = 5000
)

type ipRecord struct {
	Fails       []time.Time `json:"fails,omitempty"`
	Locks       int         `json:"locks,omitempty"`
	LockedUntil time.Time   `json:"locked_until,omitempty"`
	LastFail    time.Time   `json:"last_fail"`
}

type guardFile struct {
	IPs    map[string]*ipRecord `json:"ips"`
	Global []time.Time          `json:"global"`
}

type guard struct {
	path string // "" keeps the state in memory only (tests)

	mu     sync.Mutex
	ips    map[string]*ipRecord
	global []time.Time // failures from all IPs, oldest first
}

// attemptResult tells the caller what happened and what to report.
type attemptResult struct {
	OK         bool
	Blocked    bool          // not even checked: the IP or the login is locked
	Global     bool          // the block is the global one
	RetryAt    time.Time     // when a blocked caller may try again
	IPLock     time.Duration // this failure locked the IP for that long
	GlobalLock bool          // this failure started the global lock
}

func newGuard(path string) *guard {
	g := &guard{path: path, ips: map[string]*ipRecord{}}
	if path == "" {
		return g
	}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		slog.Warn("cannot read login guard state; starting empty", "path", path, "err", err)
	default:
		var f guardFile
		if err := json.Unmarshal(b, &f); err != nil {
			slog.Warn("login guard state is corrupt; starting empty", "path", path, "err", err)
		} else {
			if f.IPs != nil {
				g.ips = f.IPs
			}
			g.global = f.Global
		}
	}
	return g
}

// attempt runs verify unless ip or the whole login is locked, and records
// the outcome. Check, verification and recording happen under one lock, so a
// burst of parallel requests cannot slip past a limit.
func (g *guard) attempt(ip string, now time.Time, verify func() bool) attemptResult {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.global = recent(g.global, now, globalFailWindow)
	rec := g.ips[ip]
	if rec != nil && now.Before(rec.LockedUntil) {
		return attemptResult{Blocked: true, RetryAt: rec.LockedUntil}
	}
	if len(g.global) >= globalFailLimit {
		return attemptResult{Blocked: true, Global: true, RetryAt: g.global[len(g.global)-globalFailLimit].Add(globalFailWindow)}
	}

	if verify() {
		if rec != nil {
			delete(g.ips, ip)
			g.saveLocked()
		}
		return attemptResult{OK: true}
	}

	if rec == nil {
		rec = &ipRecord{}
		g.ips[ip] = rec
	}
	if now.Sub(rec.LastFail) > lockForget {
		rec.Locks = 0
	}
	rec.LastFail = now
	rec.Fails = append(recent(rec.Fails, now, ipFailWindow), now)
	g.global = append(g.global, now)

	var res attemptResult
	if len(rec.Fails) >= ipFailLimit {
		d := ipLockBase << min(rec.Locks, 10)
		if d > ipLockMax {
			d = ipLockMax
		}
		rec.Locks++
		rec.LockedUntil = now.Add(d)
		rec.Fails = nil
		res.IPLock = d
	}
	res.GlobalLock = len(g.global) == globalFailLimit
	g.pruneLocked(now)
	g.saveLocked()
	return res
}

// recent keeps the times within window (the slice is reused).
func recent(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < window {
			out = append(out, t)
		}
	}
	return out
}

// pruneLocked forgets IPs that are neither locked nor recently failing, and
// keeps at most maxGuardIPs, dropping the least recently failing first.
func (g *guard) pruneLocked(now time.Time) {
	for ip, r := range g.ips {
		r.Fails = recent(r.Fails, now, ipFailWindow)
		if len(r.Fails) == 0 && !now.Before(r.LockedUntil) && now.Sub(r.LastFail) > lockForget {
			delete(g.ips, ip)
		}
	}
	if len(g.ips) <= maxGuardIPs {
		return
	}
	type entry struct {
		ip   string
		last time.Time
	}
	all := make([]entry, 0, len(g.ips))
	for ip, r := range g.ips {
		all = append(all, entry{ip, r.LastFail})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	for _, e := range all[:len(all)-maxGuardIPs] {
		delete(g.ips, e.ip)
	}
}

// saveLocked writes the state atomically. A failed write is logged and the
// in-memory protection keeps working.
func (g *guard) saveLocked() {
	if g.path == "" {
		return
	}
	b, err := json.Marshal(guardFile{IPs: g.ips, Global: g.global})
	if err == nil {
		err = atomicfile.Write(g.path, b, 0o600)
	}
	if err != nil {
		slog.Warn("cannot save login guard state", "path", g.path, "err", err)
	}
}
