package stats

import (
	"fmt"
	"math"
	"testing"
	"time"
)

var t0 = time.Unix(1790322000, 0)

func TestSharesPerWorker(t *testing.T) {
	c := NewCollector()
	c.Share("a", "farm.s19", true, 1000, "", t0)
	c.Share("a", "farm.s19", false, 1000, "stale", t0)
	c.Share("b", "farm.m50", true, 500, "", t0)
	c.Share("b", "", true, 10, "", t0) // no login: totals only

	got := c.Totals()
	want := Totals{
		Total: Counters{Accepted: 3, Rejected: 1, Diff: 1510},
		Pools: map[string]Counters{"a": {Accepted: 1, Rejected: 1, Diff: 1000}, "b": {Accepted: 2, Diff: 510}},
		Workers: map[string]Counters{
			"farm.s19": {Accepted: 1, Rejected: 1, Diff: 1000},
			"farm.m50": {Accepted: 1, Diff: 500},
		},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("totals\n got %+v\nwant %+v", got, want)
	}

	// Totals is a copy: later shares do not change it.
	c.Share("a", "farm.s19", true, 1000, "", t0)
	if got.Workers["farm.s19"].Accepted != 1 {
		t.Fatal("Totals shares memory with the collector")
	}
}

func TestWorkerLimit(t *testing.T) {
	c := NewCollector()
	for i := range maxWorkers + 10 {
		c.Share("a", fmt.Sprintf("w%d", i), true, 1, "", t0)
	}
	got := c.Totals()
	if len(got.Workers) != maxWorkers || got.Total.Accepted != maxWorkers+10 {
		t.Fatalf("%d workers, %d accepted", len(got.Workers), got.Total.Accepted)
	}
	// Known workers keep counting after the limit.
	c.Share("a", "w0", true, 1, "", t0)
	if c.Totals().Workers["w0"].Accepted != 2 {
		t.Fatal("a known worker stopped counting")
	}
}

func TestRate(t *testing.T) {
	r := NewRate(t0.Add(-time.Hour))
	// One share of difficulty 60 a second for ten minutes: 60 × 2³² H/s.
	for s := range 600 {
		r.Add(60, t0.Add(time.Duration(s)*time.Second))
	}
	got := r.Hashrate(t0.Add(599 * time.Second))
	if want := 60 * HashesPerDifficulty; math.Abs(got/want-1) > 0.01 {
		t.Fatalf("hashrate %g, want %g", got, want)
	}
	if h := r.Hashrate(t0.Add(time.Hour)); h != 0 {
		t.Fatalf("an hour later the hashrate is %g", h)
	}
}
