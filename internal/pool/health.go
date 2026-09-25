package pool

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

type Status string

const (
	StatusUnknown Status = "UNKNOWN"
	StatusUp      Status = "UP"
	StatusDown    Status = "DOWN"
)

// Hysteresis thresholds, applied to every address of a pool.
const (
	probeFailsToDown = 2
	dialFailsToDown  = 3
	probeOKsToUp     = 2
)

// Address choice. The latency of an address is its TCP connect time (one
// round trip, DNS excluded): the median of the last probes, so a single slow
// connect (a retransmitted SYN, a busy anycast edge) does not move sessions.
// The preferred address is kept until another one is clearly faster, so two
// similar servers do not make new sessions flip between them on every probe.
const (
	rttSamples    = 5                    // probes in the median
	switchRatio   = 0.8                  // the new address must be at least 20% faster...
	switchMinGain = 5 * time.Millisecond // ...and at least this much faster
)

// addrHealth is the health of one address of a pool.
type addrHealth struct {
	status     Status
	probeFails int
	probeOKs   int
	dialFails  int
	lastCheck  time.Time
	lastError  string
	upSince    time.Time
	samples    []time.Duration // last connect times, oldest first
	rtt        time.Duration   // median of samples; 0 = not measured yet
}

// addSample records a connect time and recomputes the median. With an even
// number of samples the lower middle one is taken: one outlier out of two
// does not count.
func (h *addrHealth) addSample(rtt time.Duration) {
	if len(h.samples) == rttSamples {
		h.samples = append(h.samples[:0], h.samples[1:]...)
	}
	h.samples = append(h.samples, rtt)
	s := slices.Clone(h.samples)
	slices.Sort(s)
	h.rtt = s[(len(s)-1)/2]
}

// poolHealth is derived from the addresses: UP if any address is UP, DOWN if
// all are DOWN, UNKNOWN otherwise. upSince is when the pool as a whole last
// became UP, so failback waits for an uninterrupted period.
type poolHealth struct {
	status    Status
	upSince   time.Time
	preferred string // address key new sessions try first
}

// Health is the pool-level view used for routing, failback and the API.
type Health struct {
	Status    Status
	LastCheck time.Time
	LastError string
	UpSince   time.Time
}

// AddrHealth is one address as shown in the API.
type AddrHealth struct {
	Address   state.Address
	Status    Status
	LastCheck time.Time
	LastError string
	RTT       time.Duration
	Preferred bool
}

func addrKey(poolID string, a state.Address) string {
	return poolID + "|" + strings.ToLower(a.String())
}

func (m *Manager) healthOf(id string) Health {
	m.hmu.Lock()
	defer m.hmu.Unlock()
	return m.healthLocked(id)
}

func (m *Manager) healthLocked(id string) Health {
	ph := m.pools[id]
	if ph == nil {
		return Health{Status: StatusUnknown}
	}
	h := Health{Status: ph.status, UpSince: ph.upSince}
	p, _ := m.snap.Load().Get(id)
	var errAt time.Time
	for _, a := range p.Addresses {
		ah := m.addrs[addrKey(id, a)]
		if ah == nil {
			continue
		}
		if ah.lastCheck.After(h.LastCheck) {
			h.LastCheck = ah.lastCheck
		}
		// The pool's error: the most recent one, and only while it matters.
		if ah.lastError != "" && ph.status != StatusUp && !ah.lastCheck.Before(errAt) {
			h.LastError, errAt = ah.lastError, ah.lastCheck
			if len(p.Addresses) > 1 {
				h.LastError = a.String() + ": " + ah.lastError
			}
		}
	}
	return h
}

// AddressHealth returns every address of a pool in configuration order.
func (m *Manager) AddressHealth(p state.Pool) []AddrHealth {
	m.hmu.Lock()
	defer m.hmu.Unlock()
	pref := ""
	if ph := m.pools[p.ID]; ph != nil {
		pref = ph.preferred
	}
	out := make([]AddrHealth, 0, len(p.Addresses))
	for _, a := range p.Addresses {
		k := addrKey(p.ID, a)
		v := AddrHealth{Address: a, Status: StatusUnknown, Preferred: k == pref}
		if ah := m.addrs[k]; ah != nil {
			v.Status, v.LastCheck, v.LastError, v.RTT = ah.status, ah.lastCheck, ah.lastError, ah.rtt
		}
		out = append(out, v)
	}
	return out
}

func (m *Manager) addrEntry(k string) *addrHealth {
	h := m.addrs[k]
	if h == nil {
		h = &addrHealth{status: StatusUnknown}
		m.addrs[k] = h
	}
	return h
}

// probeResult records a background check of one address; rtt is its
// connect time when the connection succeeded.
func (m *Manager) probeResult(p state.Pool, a state.Address, rtt time.Duration, err error) {
	now := time.Now()
	m.hmu.Lock()
	h := m.addrEntry(addrKey(p.ID, a))
	h.lastCheck = now
	prev := h.status
	if rtt > 0 {
		h.addSample(rtt)
	}
	if err != nil {
		h.lastError = err.Error()
		h.probeOKs = 0
		h.probeFails++
		h.upSince = time.Time{}
		if h.status != StatusDown && h.probeFails >= probeFailsToDown {
			h.status = StatusDown
		}
	} else {
		h.lastError = ""
		h.probeFails = 0
		// A working probe breaks a series of failed session dials: "three in
		// a row" must not add up failures scattered over hours of health.
		h.dialFails = 0
		h.probeOKs++
		if h.probeOKs >= probeOKsToUp && (h.status != StatusUp || h.upSince.IsZero()) {
			h.status = StatusUp
			h.upSince = now
		}
	}
	events := m.addrChangedLocked(p, a, prev, h, now)
	m.hmu.Unlock()
	for _, e := range events {
		e()
	}
}

// dialResult records a session connecting to one address: three failures in
// a row mark it DOWN without waiting for the probe.
func (m *Manager) dialResult(p state.Pool, a state.Address, err error) {
	now := time.Now()
	m.hmu.Lock()
	h := m.addrEntry(addrKey(p.ID, a))
	prev := h.status
	if err == nil {
		h.dialFails = 0
	} else {
		h.lastCheck = now
		h.dialFails++
		h.lastError = err.Error()
		if h.status != StatusDown && h.dialFails >= dialFailsToDown {
			h.status = StatusDown
			h.probeOKs = 0
			h.upSince = time.Time{}
			h.lastError = err.Error() + " (3 failed session connects)"
		}
	}
	events := m.addrChangedLocked(p, a, prev, h, now)
	m.hmu.Unlock()
	for _, e := range events {
		e()
	}
}

// addrChangedLocked reports an address status change, then recomputes the
// pool status and preferred address. It returns events to emit after unlock.
func (m *Manager) addrChangedLocked(p state.Pool, a state.Address, prev Status, h *addrHealth, now time.Time) []func() {
	var events []func()
	if len(p.Addresses) > 1 && prev != h.status {
		name, addr, msg := p.Name, a.String(), h.lastError
		switch h.status {
		case StatusDown:
			events = append(events, func() { m.ev.Warn("pool_address_down", "pool %s: address %s is DOWN: %s", name, addr, msg) })
		case StatusUp:
			if prev == StatusDown {
				events = append(events, func() { m.ev.Info("pool_address_up", "pool %s: address %s is UP again", name, addr) })
			}
		}
	}
	return append(events, m.refreshPoolLocked(p, now)...)
}

// refreshPoolLocked derives the pool status from its addresses and updates
// the preferred address.
func (m *Manager) refreshPoolLocked(p state.Pool, now time.Time) []func() {
	ph := m.pools[p.ID]
	if ph == nil {
		ph = &poolHealth{status: StatusUnknown}
		m.pools[p.ID] = ph
	}
	anyUp, allDown := false, len(p.Addresses) > 0
	lastErr := ""
	for _, a := range p.Addresses {
		st := StatusUnknown
		if ah := m.addrs[addrKey(p.ID, a)]; ah != nil {
			st = ah.status
			if ah.lastError != "" {
				lastErr = ah.lastError
			}
		}
		anyUp = anyUp || st == StatusUp
		allDown = allDown && st == StatusDown
	}
	next := StatusUnknown
	switch {
	case anyUp:
		next = StatusUp
	case allDown:
		next = StatusDown
	}

	var events []func()
	name := p.Name
	// Failback waits for an uninterrupted healthy period: the earliest start
	// among UP addresses whose healthy run was not broken by a failed probe.
	ph.upSince = time.Time{}
	if next == StatusUp {
		for _, a := range p.Addresses {
			if ah := m.addrs[addrKey(p.ID, a)]; ah != nil && ah.status == StatusUp && !ah.upSince.IsZero() &&
				(ph.upSince.IsZero() || ah.upSince.Before(ph.upSince)) {
				ph.upSince = ah.upSince
			}
		}
	}
	if next != ph.status {
		prev := ph.status
		ph.status = next
		switch next {
		case StatusUp:
			if prev == StatusDown {
				events = append(events, func() { m.ev.Info("pool_up", "pool %s is UP again", name) })
			} else {
				events = append(events, func() { m.ev.Info("pool_up", "pool %s is UP", name) })
			}
		case StatusDown:
			what := lastErr
			if len(p.Addresses) > 1 {
				what = "all addresses unreachable, last error: " + lastErr
			}
			events = append(events, func() { m.ev.Warn("pool_down", "pool %s is DOWN: %s", name, what) })
		}
	}

	best := m.chooseLocked(p, ph.preferred)
	if best != ph.preferred {
		old := ph.preferred
		ph.preferred = best
		// Report only a switch for speed: the first measurement is not a
		// change, and a switch away from a DOWN address has its own event.
		if oh := m.addrs[old]; oh != nil && oh.rtt > 0 && oh.status != StatusDown && best != "" {
			from, to := m.describeLocked(p, old), m.describeLocked(p, best)
			events = append(events, func() {
				m.ev.Info("pool_address_preferred", "pool %s: new sessions now go to %s (was %s)", name, to, from)
			})
		}
	}
	return events
}

// chooseLocked picks the preferred address: the lowest smoothed latency among
// addresses that are not DOWN, keeping the current one unless another is
// clearly faster. Unmeasured addresses count only when nothing is measured.
func (m *Manager) chooseLocked(p state.Pool, current string) string {
	var bestKey string
	var bestRTT time.Duration
	firstUsable := ""
	curOK, curRTT := false, time.Duration(0)
	for _, a := range p.Addresses {
		k := addrKey(p.ID, a)
		ah := m.addrs[k]
		if ah != nil && ah.status == StatusDown {
			continue
		}
		if firstUsable == "" {
			firstUsable = k
		}
		var rtt time.Duration
		if ah != nil {
			rtt = ah.rtt
		}
		if k == current {
			curOK, curRTT = true, rtt
		}
		if rtt > 0 && (bestKey == "" || rtt < bestRTT) {
			bestKey, bestRTT = k, rtt
		}
	}
	switch {
	case bestKey == "":
		if curOK {
			return current
		}
		return firstUsable
	case !curOK || curRTT == 0 || current == bestKey:
		return bestKey
	case float64(bestRTT) < float64(curRTT)*switchRatio && curRTT-bestRTT >= switchMinGain:
		return bestKey
	}
	return current
}

func (m *Manager) describeLocked(p state.Pool, key string) string {
	for _, a := range p.Addresses {
		if addrKey(p.ID, a) == key {
			if ah := m.addrs[key]; ah != nil && ah.rtt > 0 {
				return fmt.Sprintf("%s (%.1f ms)", a, float64(ah.rtt)/float64(time.Millisecond))
			}
			return a.String()
		}
	}
	return key
}

// orderedAddresses is the dial order for a new session: the preferred
// address, then the other addresses that are not DOWN by latency (unmeasured
// ones last, in configuration order).
func (m *Manager) orderedAddresses(p state.Pool) []state.Address {
	m.hmu.Lock()
	defer m.hmu.Unlock()
	pref := ""
	if ph := m.pools[p.ID]; ph != nil {
		pref = ph.preferred
	}
	type cand struct {
		a     state.Address
		rtt   time.Duration
		pref  bool
		order int
	}
	var cs []cand
	for i, a := range p.Addresses {
		k := addrKey(p.ID, a)
		c := cand{a: a, pref: k == pref, order: i}
		if ah := m.addrs[k]; ah != nil {
			if ah.status == StatusDown {
				continue
			}
			c.rtt = ah.rtt
		}
		cs = append(cs, c)
	}
	sort.SliceStable(cs, func(i, j int) bool {
		x, y := cs[i], cs[j]
		if x.pref != y.pref {
			return x.pref
		}
		if (x.rtt > 0) != (y.rtt > 0) {
			return x.rtt > 0
		}
		if x.rtt != y.rtt {
			return x.rtt < y.rtt
		}
		return x.order < y.order
	})
	out := make([]state.Address, len(cs))
	for i, c := range cs {
		out[i] = c.a
	}
	return out
}

// forgetHealth drops everything known about a pool.
func (m *Manager) forgetHealth(id string) {
	m.hmu.Lock()
	defer m.hmu.Unlock()
	delete(m.pools, id)
	prefix := id + "|"
	for k := range m.addrs {
		if strings.HasPrefix(k, prefix) {
			delete(m.addrs, k)
		}
	}
}

// poolEdited keeps the health of unchanged addresses after an edit. A change
// of TLS settings affects every address, so all of them start over.
func (m *Manager) poolEdited(old, next state.Pool) {
	if old.TLS != next.TLS || old.TLSSkipVerify != next.TLSSkipVerify {
		m.forgetHealth(next.ID)
		return
	}
	keep := map[string]bool{}
	for _, a := range next.Addresses {
		keep[addrKey(next.ID, a)] = true
	}
	m.hmu.Lock()
	for _, a := range old.Addresses {
		if k := addrKey(old.ID, a); !keep[k] {
			delete(m.addrs, k)
		}
	}
	events := m.refreshPoolLocked(next, time.Now())
	m.hmu.Unlock()
	for _, e := range events {
		e()
	}
}
