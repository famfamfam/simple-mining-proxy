package history

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/stats"
)

// farm is a fake source: pool "a" gets 1000 difficulty and 10 accepted and
// 1 rejected share per minute, all from worker farm.w1. farm.w2 is connected
// while w2 is set and sends nothing. diff is extra accepted difficulty.
type farm struct {
	minutes uint64
	w2      bool
	diff    float64
}

func (f *farm) sample() Sample {
	c := stats.Counters{Accepted: 10 * f.minutes, Rejected: f.minutes, Diff: 1000*float64(f.minutes) + f.diff}
	online := map[string]int{"farm.w1": 1}
	if f.w2 {
		online["farm.w2"] = 1
	}
	return Sample{
		Totals: stats.Totals{Total: c, Pools: map[string]stats.Counters{"a": c}, Workers: map[string]stats.Counters{"farm.w1": c}},
		Miners: map[string]int{"a": len(online)},
		Online: online,
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

const day = 24 * time.Hour

func newRecorder(t *testing.T, dir string, c *clock, f *farm) *Recorder {
	t.Helper()
	r, err := New(dir, f.sample, func() Retention { return Retention{Detail: 7 * day, Total: 365 * day, Miners: 90 * day} })
	if err != nil {
		t.Fatal(err)
	}
	r.now = c.now
	return r
}

// run advances the fake clock minute by minute, like Run does.
func run(r *Recorder, c *clock, f *farm, minutes int) {
	for range minutes {
		c.t = c.t.Add(time.Minute)
		f.minutes++
		r.record(c.t)
	}
}

var t0 = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

const perMinute = 1000 * stats.HashesPerDifficulty / 60 // H/s at 1000 difficulty per minute

func read(t *testing.T, r *Recorder, tr tier, ts int64) []Point {
	t.Helper()
	pts, err := readFile(r.file(tr, ts), 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	return pts
}

func TestRecordAndQueryMinutes(t *testing.T) {
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, t.TempDir(), c, f)
	r.start(c.t)
	run(r, c, f, 90)

	s, err := r.Query(t0, t0.Add(90*time.Minute), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.Step != 60 || len(s.Time) != 90 {
		t.Fatalf("step %d, %d points", s.Step, len(s.Time))
	}
	if got := *s.Hashrate[10]; math.Abs(got-perMinute) > 1 {
		t.Fatalf("hashrate %.0f, want %.0f", got, perMinute)
	}
	if *s.Accepted[10] != 10 || *s.Rejected[10] != 1 || *s.Miners[10] != 1 || *s.Pools["a"][10] != *s.Hashrate[10] {
		t.Fatalf("point: a=%d r=%d m=%v pool=%v", *s.Accepted[10], *s.Rejected[10], *s.Miners[10], *s.Pools["a"][10])
	}
	// The 10:00 hour is written as soon as it is complete.
	pts := read(t, r, hourTier, t0.Unix())
	if len(pts) != 1 || pts[0].T != t0.Unix() || pts[0].S != 3600 || pts[0].A != 600 || pts[0].M != 1 {
		t.Fatalf("hour roll-up: %+v", pts)
	}
}

func TestResamplingAndGaps(t *testing.T) {
	dir := t.TempDir()
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, dir, c, f)
	r.start(c.t)
	run(r, c, f, 60)
	// The proxy is stopped for an hour, then records another hour.
	c.t = c.t.Add(time.Hour)
	r = newRecorder(t, dir, c, f)
	r.start(c.t)
	run(r, c, f, 60)

	s, err := r.Query(t0, t0.Add(3*time.Hour), 18) // 180 minutes into 18 points: 10-minute steps
	if err != nil {
		t.Fatal(err)
	}
	if s.Step != 600 || len(s.Time) != 18 {
		t.Fatalf("step %d, %d points", s.Step, len(s.Time))
	}
	if s.Hashrate[7] != nil || s.Hashrate[8] != nil {
		t.Fatal("the stopped hour must be a gap")
	}
	if *s.Accepted[0] != 100 || math.Abs(*s.Hashrate[0]-perMinute) > 1 {
		t.Fatalf("10-minute step: accepted %d, hashrate %.0f", *s.Accepted[0], *s.Hashrate[0])
	}
}

func TestLongRangeUsesHours(t *testing.T) {
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, t.TempDir(), c, f)
	r.start(c.t)
	run(r, c, f, 5*60+30)

	s, err := r.Query(t0.Add(-7*day), c.t, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.Step != 3600 {
		t.Fatalf("step %d, want hourly", s.Step)
	}
	last := len(s.Time) - 1 // the unfinished hour comes from memory
	if s.Hashrate[last] == nil || *s.Accepted[last] != 300 {
		t.Fatalf("current hour: %v", s.Accepted[last])
	}
	if math.Abs(*s.Hashrate[last-1]-perMinute) > 1 || *s.Accepted[last-1] != 600 {
		t.Fatalf("full hour: %v %v", *s.Hashrate[last-1], *s.Accepted[last-1])
	}
}

// A restart writes the hour roll-ups the previous run could not and
// continues the current hour from the points on disk.
func TestRestartFinishesRollUp(t *testing.T) {
	dir := t.TempDir()
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, dir, c, f)
	r.start(c.t)
	run(r, c, f, 50) // killed at 10:50: the 10:00 hour is not rolled up yet

	c.t = t0.Add(2*time.Hour + 20*time.Minute) // 12:20
	f2 := &farm{}
	r2 := newRecorder(t, dir, c, f2)
	r2.start(c.t)
	if pts := read(t, r2, hourTier, t0.Unix()); len(pts) != 1 || pts[0].A != 500 || pts[0].S != 3000 {
		t.Fatalf("recovered hour: %+v", pts)
	}
	if pts := read(t, r2, workerHourTier, t0.Unix()); len(pts) != 1 || pts[0].W["farm.w1"].A != 500 {
		t.Fatalf("recovered worker hour: %+v", pts)
	}
	run(r2, c, f2, 10)
	if r2.totals.acc.T != t0.Add(2*time.Hour).Unix() || r2.totals.acc.A != 100 {
		t.Fatalf("current hour: %+v", r2.totals.acc)
	}
}

func TestWorkers(t *testing.T) {
	c, f := &clock{t0}, &farm{w2: true}
	r := newRecorder(t, t.TempDir(), c, f)
	r.start(c.t)
	run(r, c, f, 30)
	f.w2 = false
	run(r, c, f, 30)

	s, err := r.Worker("farm.w1", t0, t0.Add(time.Hour), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.Step != 600 || len(s.Time) != 6 || math.Abs(*s.Hashrate[2]-perMinute) > 1 || *s.Online[5] != 1 || *s.Accepted[0] != 100 {
		t.Fatalf("w1: step %d, %d points, %v", s.Step, len(s.Time), *s.Hashrate[2])
	}
	s2, _ := r.Worker("farm.w2", t0, t0.Add(time.Hour), 1000)
	if *s2.Online[2] != 1 || *s2.Online[3] != 0 || *s2.Hashrate[0] != 0 {
		t.Fatalf("w2 online: %v %v", *s2.Online[2], *s2.Online[3])
	}

	list, err := r.Workers(t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "farm.w1" || list[0].Online != 1 || list[0].Accepted != 600 ||
		math.Abs(list[0].Hashrate-perMinute) > 1 {
		t.Fatalf("summary: %+v", list)
	}
	if w2 := list[1]; w2.Online != 0.5 || w2.LastSeen != t0.Add(30*time.Minute).Unix() {
		t.Fatalf("w2 summary: %+v", w2)
	}
	if s.Summary != list[0] || s2.Summary != list[1] {
		t.Fatalf("series summaries %+v %+v differ from the list %+v", s.Summary, s2.Summary, list)
	}
	if none, _ := r.Worker("farm.gone", t0, t0.Add(time.Hour), 1000); none.Summary.LastSeen != 0 {
		t.Fatalf("unknown worker: %+v", none.Summary)
	}
}

// A miner's summary weighs steps by their length: the unfinished ten minutes
// count for the one minute they hold, not as a whole step.
func TestWorkerSummaryWeighsByDuration(t *testing.T) {
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, t.TempDir(), c, f)
	r.start(c.t)
	run(r, c, f, 20)
	f.diff += 10000 // one busy minute, in the unfinished interval
	run(r, c, f, 1)

	s, err := r.Worker("farm.w1", t0, c.t, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Time) != 3 {
		t.Fatalf("%d steps, want two full ones and the unfinished one", len(s.Time))
	}
	want := 31000 * stats.HashesPerDifficulty / 1260 // all difficulty over all online seconds
	if got := s.Summary; math.Abs(got.Hashrate/want-1) > 1e-9 || got.Online != 1 || got.Accepted != 210 {
		t.Fatalf("summary %+v, hashrate want %g", got, want)
	}
	list, _ := r.Workers(t0, c.t)
	if len(list) != 1 || list[0] != s.Summary {
		t.Fatalf("list %+v, series summary %+v", list, s.Summary)
	}
}

// Stopping writes the unfinished minute and ten minutes; nothing is lost
// or counted twice after the restart.
func TestStopKeepsUnfinishedIntervals(t *testing.T) {
	dir := t.TempDir()
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, dir, c, f)
	r.start(c.t)
	run(r, c, f, 25)
	c.t = c.t.Add(30 * time.Second)
	f.minutes++ // shares of the half minute
	r.stop(c.t)

	r2 := newRecorder(t, dir, c, f)
	r2.start(c.t)
	run(r2, c, f, 10)
	list, _ := r2.Workers(t0, c.t)
	if len(list) != 1 || list[0].Accepted != 360 {
		t.Fatalf("after restart: %+v", list)
	}
	s, _ := r2.Query(t0, c.t, 1000)
	var accepted uint64
	for _, v := range s.Accepted {
		if v != nil {
			accepted += *v
		}
	}
	if accepted != 360 {
		t.Fatalf("totals after restart: %d", accepted)
	}
}

func TestPruneKeepsRetention(t *testing.T) {
	dir := t.TempDir()
	c := &clock{t0}
	r := newRecorder(t, dir, c, &farm{})
	files := map[string]bool{
		r.file(minuteTier, t0.AddDate(0, 0, -10).Unix()):      false,
		r.file(minuteTier, t0.Unix()):                         true,
		r.file(hourTier, t0.AddDate(-2, 0, 0).Unix()):         false,
		r.file(hourTier, t0.Unix()):                           true,
		r.file(workerDetailTier, t0.AddDate(0, 0, -8).Unix()): false,
		r.file(workerHourTier, t0.AddDate(0, -4, 0).Unix()):   false,
		r.file(workerHourTier, t0.AddDate(0, -2, 0).Unix()):   true,
		filepath.Join(dir, minuteTier.dir, "notes.txt"):       true,
	}
	for path := range files {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.Prune()
	for path, want := range files {
		if _, err := os.Stat(path); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", path, err == nil, want)
		}
	}
}

func TestDamagedLineIsSkipped(t *testing.T) {
	c, f := &clock{t0}, &farm{}
	r := newRecorder(t, t.TempDir(), c, f)
	r.start(c.t)
	run(r, c, f, 3)
	fh, _ := os.OpenFile(r.file(minuteTier, t0.Unix()), os.O_APPEND|os.O_WRONLY, 0)
	fh.WriteString(`{"t":17`) // a crash in the middle of a write
	fh.Close()
	run(r, c, f, 2)
	s, err := r.Query(t0, t0.Add(5*time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, v := range s.Accepted {
		if v != nil {
			n++
		}
	}
	// Only the cut-short write is lost; the next point starts a new line.
	if n != 5 {
		t.Fatalf("readable points: %d", n)
	}
}

func TestAddWeightsMiners(t *testing.T) {
	p := Point{S: 60, M: 4, P: map[string]PoolPart{"a": {M: 4, D: 1}}, W: map[string]WorkerPart{"w": {A: 1, O: 60}}}
	p.add(Point{S: 60, M: 0, D: 2, W: map[string]WorkerPart{"w": {A: 2, O: 30}}})
	if p.S != 120 || p.M != 2 || p.P["a"].M != 2 || p.D != 2 || p.P["a"].D != 1 || p.W["w"] != (WorkerPart{A: 3, O: 90}) {
		t.Fatalf("%+v", p)
	}
}
