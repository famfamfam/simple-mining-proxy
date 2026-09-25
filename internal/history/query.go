package history

import (
	"errors"
	"sort"
	"time"
)

// Steps a chart can use: time labels stay round.
var steps = []int64{60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600, 43200,
	86400, 172800, 345600, 604800, 1209600, 2592000}

// detailSpan is the longest range read from a fine tier; longer ranges read
// the hourly roll-ups.
const detailSpan = 3 * 24 * time.Hour

var errRange = errors.New("history: the range must end after it starts")

// buckets reads a series for [from, to) and merges its points into steps of
// equal length, at most about maxPoints of them. Points still in memory (the
// unfinished intervals) are included.
func (r *Recorder) buckets(s *series, from, to time.Time, maxPoints int) ([]Point, int64, int64, error) {
	if !to.After(from) {
		return nil, 0, 0, errRange
	}
	maxPoints = max(maxPoints, 10)
	span := to.Sub(from)
	fine := span <= detailSpan && !from.Before(r.now().Add(-s.keepFine(r.retention())))
	t := s.coarse
	if fine {
		t = s.fine
	}
	need := max(t.step, (int64(span/time.Second)+int64(maxPoints)-1)/int64(maxPoints))
	step := steps[len(steps)-1]
	for _, x := range steps {
		if x >= need {
			step = x
			break
		}
	}
	start, end := from.Unix()-from.Unix()%step, to.Unix()
	pts, err := r.read(t, start, end)
	if err != nil {
		return nil, 0, 0, err
	}
	r.mu.Lock()
	live := []Point{s.cur}
	if !fine {
		live = append(live, s.acc)
	}
	for _, p := range live {
		if p.S > 0 && p.T >= start && p.T < end {
			pts = append(pts, p.clone())
		}
	}
	r.mu.Unlock()

	out := make([]Point, (end-start+step-1)/step)
	for _, p := range pts {
		if i := (p.T - start) / step; i >= 0 && i < int64(len(out)) {
			out[i].add(p)
		}
	}
	return out, start, step, nil
}

// Series is the totals resampled to a fixed step for charts. Every slice
// is aligned with Time; a nil entry is a step without data (the proxy was
// not running) and is drawn as a gap.
type Series struct {
	Step     int64                 `json:"step"`
	Time     []int64               `json:"time"`
	Hashrate []*float64            `json:"hashrate"` // H/s
	Accepted []*uint64             `json:"accepted"`
	Rejected []*uint64             `json:"rejected"`
	Miners   []*float64            `json:"miners"`
	Pools    map[string][]*float64 `json:"pools"` // hashrate per pool id, H/s
}

// Query returns the totals for [from, to) in at most about maxPoints steps.
func (r *Recorder) Query(from, to time.Time, maxPoints int) (*Series, error) {
	bs, start, step, err := r.buckets(r.totals, from, to, maxPoints)
	if err != nil {
		return nil, err
	}
	n := len(bs)
	s := &Series{
		Step: step, Time: make([]int64, n), Hashrate: make([]*float64, n),
		Accepted: make([]*uint64, n), Rejected: make([]*uint64, n), Miners: make([]*float64, n),
		Pools: map[string][]*float64{},
	}
	for _, b := range bs {
		for id := range b.P {
			if s.Pools[id] == nil {
				s.Pools[id] = make([]*float64, n)
			}
		}
	}
	for i := range bs {
		b := &bs[i]
		s.Time[i] = start + int64(i)*step
		if b.S == 0 {
			continue
		}
		s.Hashrate[i] = ptr(b.Hashrate())
		s.Accepted[i] = ptr(b.A)
		s.Rejected[i] = ptr(b.R)
		s.Miners[i] = ptr(b.M)
		for id, vals := range s.Pools {
			vals[i] = ptr(b.PoolHashrate(id))
		}
	}
	return s, nil
}

// WorkerSeries is one ASIC login resampled to a fixed step. Online is the
// part of each step the worker was connected (0–1).
type WorkerSeries struct {
	Step     int64         `json:"step"`
	Time     []int64       `json:"time"`
	Hashrate []*float64    `json:"hashrate"` // H/s over the whole step
	Accepted []*uint64     `json:"accepted"`
	Rejected []*uint64     `json:"rejected"`
	Online   []*float64    `json:"online"`
	Summary  WorkerSummary `json:"summary"` // the whole range, as in Workers
}

// Worker returns the history of one ASIC login for [from, to).
func (r *Recorder) Worker(name string, from, to time.Time, maxPoints int) (*WorkerSeries, error) {
	bs, start, step, err := r.buckets(r.workers, from, to, maxPoints)
	if err != nil {
		return nil, err
	}
	n := len(bs)
	s := &WorkerSeries{
		Step: step, Time: make([]int64, n), Hashrate: make([]*float64, n),
		Accepted: make([]*uint64, n), Rejected: make([]*uint64, n), Online: make([]*float64, n),
	}
	var total workerTotal
	for i := range bs {
		b := &bs[i]
		s.Time[i] = start + int64(i)*step
		if b.S == 0 {
			continue
		}
		w, seen := b.W[name]
		total.recorded += b.S
		if seen {
			total.add(w, min(start+int64(i+1)*step, to.Unix()))
		}
		s.Hashrate[i] = ptr(hashrate(w.D, b.S))
		s.Accepted[i] = ptr(w.A)
		s.Rejected[i] = ptr(w.R)
		s.Online[i] = ptr(min(1, float64(w.O)/float64(b.S)))
	}
	s.Summary = total.summary(name)
	return s, nil
}

// WorkerSummary is one ASIC login over a range.
type WorkerSummary struct {
	Name     string  `json:"name"`
	Hashrate float64 `json:"hashrate"` // H/s averaged over the time it was connected
	Accepted uint64  `json:"accepted"`
	Rejected uint64  `json:"rejected"`
	Online   float64 `json:"online"`    // part of the recorded time it was connected, 0–1
	LastSeen int64   `json:"last_seen"` // end of the last interval it was connected or sent a share; 0 if never
}

// workerTotal adds up one ASIC login over the buckets of a range. The
// averages come from the sums, so steps of different length (the unfinished
// one, a restart) weigh by their duration.
type workerTotal struct {
	WorkerPart
	recorded int64 // seconds the proxy was recording in the range
	last     int64
}

func (t *workerTotal) add(w WorkerPart, end int64) {
	t.D += w.D
	t.A += w.A
	t.R += w.R
	t.O += w.O
	t.last = max(t.last, end)
}

func (t *workerTotal) summary(name string) WorkerSummary {
	ws := WorkerSummary{Name: name, Accepted: t.A, Rejected: t.R, LastSeen: t.last, Hashrate: hashrate(t.D, t.O)}
	if t.recorded > 0 {
		ws.Online = min(1, float64(t.O)/float64(t.recorded))
	}
	return ws
}

// Workers summarizes every ASIC login seen in [from, to), best first.
func (r *Recorder) Workers(from, to time.Time) ([]WorkerSummary, error) {
	bs, start, step, err := r.buckets(r.workers, from, to, 400)
	if err != nil {
		return nil, err
	}
	byName := map[string]*workerTotal{}
	var recorded int64
	for i, b := range bs {
		recorded += b.S
		end := min(start+int64(i+1)*step, to.Unix())
		for name, w := range b.W {
			if byName[name] == nil {
				byName[name] = &workerTotal{}
			}
			byName[name].add(w, end)
		}
	}
	out := make([]WorkerSummary, 0, len(byName))
	for name, t := range byName {
		t.recorded = recorded
		out = append(out, t.summary(name))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hashrate != out[j].Hashrate {
			return out[i].Hashrate > out[j].Hashrate
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func ptr[T any](v T) *T { return &v }
