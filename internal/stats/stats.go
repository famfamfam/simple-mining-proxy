// Package stats counts shares and estimates hashrate in memory.
package stats

import (
	"sync"
	"time"
)

const (
	bucketLen = time.Minute
	nBuckets  = 10 // window = 10 minutes
)

// HashesPerDifficulty is the expected number of hashes behind difficulty 1
// on SHA-256 (2³²): a share of difficulty d stands for d × 2³² hashes, and a
// block at network difficulty D takes D × 2³² hashes on average.
const HashesPerDifficulty = 4294967296.0

// Rate estimates hashrate from accepted share difficulty:
// Σ difficulty × 2³² / window, over a sliding window of minute buckets.
type Rate struct {
	mu     sync.Mutex
	start  time.Time
	sums   [nBuckets]float64
	stamps [nBuckets]int64
}

func NewRate(start time.Time) *Rate { return &Rate{start: start} }

func (r *Rate) Add(diff float64, now time.Time) {
	m := now.Unix() / int64(bucketLen/time.Second)
	i := m % nBuckets
	r.mu.Lock()
	if r.stamps[i] != m {
		r.stamps[i] = m
		r.sums[i] = 0
	}
	r.sums[i] += diff
	r.mu.Unlock()
}

// Hashrate returns hashes per second.
func (r *Rate) Hashrate(now time.Time) float64 {
	m := now.Unix() / int64(bucketLen/time.Second)
	r.mu.Lock()
	var sum float64
	for i := range r.sums {
		if r.stamps[i] > m-nBuckets && r.stamps[i] <= m {
			sum += r.sums[i]
		}
	}
	r.mu.Unlock()
	windowStart := time.Unix((m-nBuckets+1)*int64(bucketLen/time.Second), 0)
	if r.start.After(windowStart) {
		windowStart = r.start
	}
	span := now.Sub(windowStart)
	if span < bucketLen {
		span = bucketLen // avoid wild estimates in the first seconds
	}
	return sum * HashesPerDifficulty / span.Seconds()
}

type PoolShares struct {
	Accepted uint64            `json:"accepted"`
	Rejected uint64            `json:"rejected"`
	Reasons  map[string]uint64 `json:"reject_reasons,omitempty"`
}

// Collector aggregates shares over all sessions, total and per pool.
type Collector struct {
	mu            sync.Mutex
	start         time.Time
	accepted      uint64
	rejected      uint64
	reasons       map[string]uint64
	byPool        map[string]*PoolShares
	rate          *Rate
	poolRate      map[string]*Rate
	diff          float64            // accepted share difficulty since start
	poolDiff      map[string]float64 // the same per pool
	workers       map[string]*Counters
	upstreamJunk  uint64
	tlsHandshakes uint64
}

func NewCollector() *Collector {
	now := time.Now()
	return &Collector{
		start:    now,
		reasons:  make(map[string]uint64),
		byPool:   make(map[string]*PoolShares),
		rate:     NewRate(now),
		poolRate: make(map[string]*Rate),
		poolDiff: make(map[string]float64),
		workers:  make(map[string]*Counters),
	}
}

// maxWorkers bounds the per-worker counters: a flood of made-up logins
// cannot grow memory without limit. Shares of further workers still count
// in the totals.
const maxWorkers = 10000

const maxReasons = 50

func addReason(m map[string]uint64, reason string) {
	if reason == "" {
		reason = "unknown"
	}
	if _, ok := m[reason]; !ok && len(m) >= maxReasons {
		reason = "other"
	}
	m[reason]++
}

// Share records the pool's answer to a share of worker (the ASIC login).
func (c *Collector) Share(poolID, worker string, accepted bool, diff float64, reason string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ps := c.byPool[poolID]
	if ps == nil {
		ps = &PoolShares{Reasons: make(map[string]uint64)}
		c.byPool[poolID] = ps
	}
	if w := c.workerLocked(worker); w != nil {
		if accepted {
			w.Accepted++
			w.Diff += diff
		} else {
			w.Rejected++
		}
	}
	if accepted {
		c.accepted++
		ps.Accepted++
		c.diff += diff
		c.poolDiff[poolID] += diff
		c.rate.Add(diff, now)
		pr := c.poolRate[poolID]
		if pr == nil {
			pr = NewRate(now)
			c.poolRate[poolID] = pr
		}
		pr.Add(diff, now)
		return
	}
	c.rejected++
	ps.Rejected++
	addReason(c.reasons, reason)
	addReason(ps.Reasons, reason)
}

func (c *Collector) workerLocked(name string) *Counters {
	if name == "" {
		return nil
	}
	w := c.workers[name]
	if w == nil && len(c.workers) < maxWorkers {
		w = &Counters{}
		c.workers[name] = w
	}
	return w
}

// Counters are cumulative share totals since start.
type Counters struct {
	Accepted uint64
	Rejected uint64
	Diff     float64 // accepted share difficulty
}

// Totals are the cumulative counters in total, per pool and per worker.
type Totals struct {
	Total   Counters
	Pools   map[string]Counters
	Workers map[string]Counters
}

// Totals is read by the history recorder, which stores the difference
// between two calls.
func (c *Collector) Totals() Totals {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := Totals{
		Total:   Counters{Accepted: c.accepted, Rejected: c.rejected, Diff: c.diff},
		Pools:   make(map[string]Counters, len(c.byPool)),
		Workers: make(map[string]Counters, len(c.workers)),
	}
	for id, ps := range c.byPool {
		t.Pools[id] = Counters{Accepted: ps.Accepted, Rejected: ps.Rejected, Diff: c.poolDiff[id]}
	}
	for name, w := range c.workers {
		t.Workers[name] = *w
	}
	return t
}

func (c *Collector) UpstreamInvalidJSON() {
	c.mu.Lock()
	c.upstreamJunk++
	c.mu.Unlock()
}

func (c *Collector) TLSHandshakeFailed() {
	c.mu.Lock()
	c.tlsHandshakes++
	c.mu.Unlock()
}

type Summary struct {
	Accepted            uint64                `json:"accepted"`
	Rejected            uint64                `json:"rejected"`
	RejectReasons       map[string]uint64     `json:"reject_reasons"`
	ByPool              map[string]PoolShares `json:"by_pool"`
	HashrateHs          float64               `json:"-"`
	PoolHashrateHs      map[string]float64    `json:"-"`
	UpstreamInvalidJSON uint64                `json:"upstream_invalid_json"`
	TLSHandshakeErrors  uint64                `json:"tls_handshake_errors"`
}

func (c *Collector) Snapshot(now time.Time) Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Summary{
		Accepted:            c.accepted,
		Rejected:            c.rejected,
		RejectReasons:       make(map[string]uint64, len(c.reasons)),
		ByPool:              make(map[string]PoolShares, len(c.byPool)),
		HashrateHs:          c.rate.Hashrate(now),
		PoolHashrateHs:      make(map[string]float64, len(c.poolRate)),
		UpstreamInvalidJSON: c.upstreamJunk,
		TLSHandshakeErrors:  c.tlsHandshakes,
	}
	for k, v := range c.reasons {
		s.RejectReasons[k] = v
	}
	for id, ps := range c.byPool {
		cp := PoolShares{Accepted: ps.Accepted, Rejected: ps.Rejected, Reasons: make(map[string]uint64, len(ps.Reasons))}
		for k, v := range ps.Reasons {
			cp.Reasons[k] = v
		}
		s.ByPool[id] = cp
	}
	for id, r := range c.poolRate {
		s.PoolHashrateHs[id] = r.Hashrate(now)
	}
	return s
}
