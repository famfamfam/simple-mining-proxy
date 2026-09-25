// Package history keeps statistics over time for the charts.
//
// Every minute the recorder takes what changed since the previous minute:
// accepted and rejected shares, accepted difficulty (the hashrate) and the
// connected miners — in total, per pool and per ASIC login. Two series are
// stored, each as a fine tier rolled up into an hourly one:
//
//   - totals: minute points, kept for the detail retention, and hours;
//   - workers: ten-minute points per ASIC login, kept for the detail
//     retention, and hours, kept for the miner retention.
//
// Every tier is JSON-lines files, one per UTC day or month: old data goes by
// deleting whole files, a damaged line never spoils the rest of a file, and
// the files are easy to inspect and back up. No database and nothing outside
// the standard library.
//
// A fine point is written when its interval ends; hourly points are rebuilt
// from the fine ones after a restart. So a crash loses only the unfinished
// fine interval: at most a minute of totals and ten minutes per worker. A
// normal stop writes both.
package history

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Retention is how long each tier is kept.
type Retention struct {
	Detail time.Duration // minute and ten-minute points
	Total  time.Duration // hourly totals
	Miners time.Duration // hourly points per worker
}

// Source returns the current counters.
type Source func() Sample

// series is a fine tier rolled up into a coarse one.
type series struct {
	name         string
	fine, coarse tier
	keepFine     func(Retention) time.Duration
	keepCoarse   func(Retention) time.Duration
	cur          Point // fine interval being filled; S == 0 when empty
	acc          Point // coarse interval being filled; S == 0 when empty
}

type Recorder struct {
	dir       string
	source    Source
	retention func() Retention
	now       func() time.Time

	mu      sync.Mutex
	last    Sample
	lastAt  time.Time
	totals  *series
	workers *series
}

func New(dir string, source Source, retention func() Retention) (*Recorder, error) {
	r := &Recorder{
		dir: dir, source: source, retention: retention, now: time.Now,
		totals: &series{
			name: "totals", fine: minuteTier, coarse: hourTier,
			keepFine:   func(x Retention) time.Duration { return x.Detail },
			keepCoarse: func(x Retention) time.Duration { return x.Total },
		},
		workers: &series{
			name: "workers", fine: workerDetailTier, coarse: workerHourTier,
			keepFine:   func(x Retention) time.Duration { return x.Detail },
			keepCoarse: func(x Retention) time.Duration { return x.Miners },
		},
	}
	for _, s := range r.all() {
		for _, t := range []tier{s.fine, s.coarse} {
			if err := os.MkdirAll(filepath.Join(dir, t.dir), 0o700); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func (r *Recorder) all() []*series { return []*series{r.totals, r.workers} }

// Run records a point at every minute boundary until ctx is done, then
// writes what the unfinished intervals hold and returns.
func (r *Recorder) Run(ctx context.Context) {
	r.start(r.now())
	for {
		now := r.now()
		timer := time.NewTimer(now.Truncate(time.Minute).Add(time.Minute).Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			r.stop(r.now())
			return
		case <-timer.C:
			r.record(r.now())
		}
	}
}

// start takes the first sample and finishes the roll-ups that a previous run
// could not: hours that ended while the proxy was stopped are written, and
// the current hour continues from the points already on disk.
func (r *Recorder) start(now time.Time) {
	r.mu.Lock()
	r.last, r.lastAt = r.source(), now
	r.mu.Unlock()
	for _, s := range r.all() {
		if err := r.recover(s, now); err != nil {
			slog.Warn("history: cannot finish the hourly roll-up", "series", s.name, "err", err)
		}
	}
	r.Prune()
}

func (r *Recorder) recover(s *series, now time.Time) error {
	from := now.Add(-s.keepFine(r.retention())).Unix()
	if last := r.newest(s.coarse); last != 0 {
		from = max(from, last+s.coarse.step)
	}
	pts, err := r.read(s.fine, from, now.Unix()+s.fine.step)
	if err != nil {
		return err
	}
	groups := map[int64]*Point{}
	for _, p := range pts {
		ct := p.T - p.T%s.coarse.step
		if groups[ct] == nil {
			groups[ct] = &Point{T: ct}
		}
		groups[ct].add(p)
	}
	starts := make([]int64, 0, len(groups))
	for ct := range groups {
		starts = append(starts, ct)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ct := range starts {
		if ct+s.coarse.step > now.Unix() {
			s.acc = *groups[ct]
			continue
		}
		if err := appendPoint(r.file(s.coarse, ct), *groups[ct]); err != nil {
			return err
		}
	}
	return nil
}

// record stores the minute that ends now. When an hour is complete it also
// deletes expired files.
func (r *Recorder) record(now time.Time) {
	if r.recordLocked(now, r.source()) {
		r.Prune()
	}
}

// stop records the unfinished minute and writes the unfinished fine
// intervals; the next start rolls them up.
func (r *Recorder) stop(now time.Time) {
	r.recordLocked(now, r.source())
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.all() {
		if s.cur.S > 0 {
			r.write(s.fine, s.cur)
			s.cur = Point{}
		}
	}
}

func (r *Recorder) recordLocked(now time.Time, cur Sample) (hourDone bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !now.After(r.lastAt) {
		return false // the clock went back; wait for it
	}
	t := r.lastAt.Truncate(time.Minute).Unix()
	secs := int64(now.Sub(r.lastAt).Round(time.Second) / time.Second)
	prev := r.last
	r.last, r.lastAt = cur, now
	if secs <= 0 {
		return false
	}
	p := delta(prev, cur)
	p.T, p.S = t, secs
	w := workerDelta(prev, cur, secs)
	w.T, w.S = t, secs
	end := now.Unix()
	hourDone = r.addLocked(r.totals, p, end)
	return r.addLocked(r.workers, w, end) || hourDone
}

// addLocked adds a minute to a series: the fine interval is written when it
// is complete and rolled into the coarse one, which is written when complete.
func (r *Recorder) addLocked(s *series, p Point, end int64) (coarseDone bool) {
	ft := p.T - p.T%s.fine.step
	if s.cur.S > 0 && s.cur.T != ft {
		coarseDone = r.flushLocked(s, end) // a gap moved us past the interval
	}
	if s.cur.S == 0 {
		s.cur = Point{T: ft}
	}
	s.cur.add(p)
	if end >= s.cur.T+s.fine.step {
		coarseDone = r.flushLocked(s, end) || coarseDone
	}
	return coarseDone
}

func (r *Recorder) flushLocked(s *series, end int64) (coarseDone bool) {
	r.write(s.fine, s.cur)
	ct := s.cur.T - s.cur.T%s.coarse.step
	if s.acc.S > 0 && s.acc.T != ct {
		r.write(s.coarse, s.acc)
		s.acc = Point{}
		coarseDone = true
	}
	if s.acc.S == 0 {
		s.acc = Point{T: ct}
	}
	s.acc.add(s.cur)
	s.cur = Point{}
	if end >= s.acc.T+s.coarse.step {
		r.write(s.coarse, s.acc)
		s.acc = Point{}
		coarseDone = true
	}
	return coarseDone
}

func (r *Recorder) write(t tier, p Point) {
	if err := appendPoint(r.file(t, p.T), p); err != nil {
		slog.Warn("history: cannot write a point", "tier", t.dir, "err", err)
	}
}

// Prune deletes the files that are entirely older than the retention. It may
// run concurrently with itself (a settings change) and with recording: the
// files it deletes are never written any more.
func (r *Recorder) Prune() {
	ret, now := r.retention(), r.now()
	for _, s := range r.all() {
		for _, x := range []struct {
			t    tier
			keep time.Duration
		}{{s.fine, s.keepFine(ret)}, {s.coarse, s.keepCoarse(ret)}} {
			if err := r.prune(x.t, now.Add(-x.keep)); err != nil {
				slog.Warn("history: cannot delete old files", "tier", x.t.dir, "err", err)
			}
		}
	}
}
