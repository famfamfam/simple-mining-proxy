package history

import "github.com/famfamfam/simple-mining-proxy/internal/stats"

// Point covers one interval (a minute, ten minutes or an hour) starting at
// T. Totals points fill D…P; worker points fill only S and W.
type Point struct {
	T int64                 `json:"t"`           // unix seconds, start of the interval
	S int64                 `json:"s"`           // seconds of the interval the proxy was recording
	D float64               `json:"d,omitempty"` // accepted share difficulty
	A uint64                `json:"a,omitempty"` // accepted shares
	R uint64                `json:"r,omitempty"` // rejected shares
	M float64               `json:"m,omitempty"` // connected miners, averaged over S
	P map[string]PoolPart   `json:"p,omitempty"`
	W map[string]WorkerPart `json:"w,omitempty"` // by ASIC login
}

// PoolPart is the share of one pool in a Point.
type PoolPart struct {
	D float64 `json:"d"`
	A uint64  `json:"a"`
	R uint64  `json:"r"`
	M float64 `json:"m"`
}

// WorkerPart is one ASIC login in a Point.
type WorkerPart struct {
	D float64 `json:"d,omitempty"`
	A uint64  `json:"a,omitempty"`
	R uint64  `json:"r,omitempty"`
	O int64   `json:"o,omitempty"` // seconds the worker was connected
}

// Hashrate is the average in hashes per second over the recorded time.
func (p *Point) Hashrate() float64 { return hashrate(p.D, p.S) }

// PoolHashrate is the hashrate of one pool over the whole recorded time,
// so the pools add up to the total.
func (p *Point) PoolHashrate(id string) float64 { return hashrate(p.P[id].D, p.S) }

func hashrate(diff float64, seconds int64) float64 {
	if seconds <= 0 {
		return 0
	}
	return diff * stats.HashesPerDifficulty / float64(seconds)
}

// add merges q into p, keeping p.T. Counts add up; miner numbers are
// averaged weighted by recorded time, and a pool missing on one side counts
// as zero miners there. p never shares a map with q.
func (p *Point) add(q Point) {
	s := p.S + q.S
	wp, wq := 0.5, 0.5
	if s > 0 {
		wp, wq = float64(p.S)/float64(s), float64(q.S)/float64(s)
	}
	p.S = s
	p.D += q.D
	p.A += q.A
	p.R += q.R
	p.M = p.M*wp + q.M*wq
	if len(p.P) > 0 || len(q.P) > 0 {
		merged := make(map[string]PoolPart, len(p.P)+len(q.P))
		for id, a := range p.P {
			merged[id] = PoolPart{D: a.D, A: a.A, R: a.R, M: a.M * wp}
		}
		for id, b := range q.P {
			m := merged[id]
			m.D += b.D
			m.A += b.A
			m.R += b.R
			m.M += b.M * wq
			merged[id] = m
		}
		p.P = merged
	}
	if len(q.W) > 0 {
		merged := make(map[string]WorkerPart, len(p.W)+len(q.W))
		for name, a := range p.W {
			merged[name] = a
		}
		for name, b := range q.W {
			m := merged[name]
			m.D += b.D
			m.A += b.A
			m.R += b.R
			m.O += b.O
			merged[name] = m
		}
		p.W = merged
	}
}

func (p Point) clone() Point {
	c := Point{T: p.T}
	c.add(p)
	return c
}

// Sample is the state of the counters at one moment.
type Sample struct {
	stats.Totals
	Miners map[string]int // connected sessions per pool
	Online map[string]int // connected sessions per ASIC login
}

// delta is the totals point between two samples, without T and S.
func delta(prev, cur Sample) Point {
	p := Point{
		D: nonNeg(cur.Total.Diff - prev.Total.Diff),
		A: sub(cur.Total.Accepted, prev.Total.Accepted),
		R: sub(cur.Total.Rejected, prev.Total.Rejected),
	}
	ids := map[string]bool{}
	for id := range cur.Pools {
		ids[id] = true
	}
	for id, n := range cur.Miners {
		p.M += float64(n)
		ids[id] = true
	}
	for id := range ids {
		c, o := cur.Pools[id], prev.Pools[id]
		part := PoolPart{
			D: nonNeg(c.Diff - o.Diff),
			A: sub(c.Accepted, o.Accepted),
			R: sub(c.Rejected, o.Rejected),
			M: float64(cur.Miners[id]),
		}
		if part != (PoolPart{}) {
			if p.P == nil {
				p.P = map[string]PoolPart{}
			}
			p.P[id] = part
		}
	}
	return p
}

// workerDelta is the worker point between two samples covering seconds:
// a worker connected at the second sample counts as online for all of them.
func workerDelta(prev, cur Sample, seconds int64) Point {
	p := Point{}
	names := map[string]bool{}
	for name := range cur.Workers {
		names[name] = true
	}
	for name := range cur.Online {
		names[name] = true
	}
	for name := range names {
		c, o := cur.Workers[name], prev.Workers[name]
		part := WorkerPart{
			D: nonNeg(c.Diff - o.Diff),
			A: sub(c.Accepted, o.Accepted),
			R: sub(c.Rejected, o.Rejected),
		}
		if cur.Online[name] > 0 {
			part.O = seconds
		}
		if part != (WorkerPart{}) {
			if p.W == nil {
				p.W = map[string]WorkerPart{}
			}
			p.W[name] = part
		}
	}
	return p
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0
	}
	return a - b
}

func nonNeg(x float64) float64 {
	if x < 0 {
		return 0
	}
	return x
}
