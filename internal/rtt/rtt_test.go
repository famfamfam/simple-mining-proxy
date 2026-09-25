package rtt

import (
	"math"
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Date(2026, 9, 25, 21, 0, 0, 0, time.UTC)

// ago returns arrival times the given seconds before t0, newest first.
func ago(secs ...float64) []time.Time {
	out := make([]time.Time, len(secs))
	for i, s := range secs {
		out[i] = t0.Add(-time.Duration(s * float64(time.Second)))
	}
	return out
}

func TestFactorAfterABlock(t *testing.T) {
	// Blocks on schedule before the last one: only the one-block window binds.
	for _, c := range []struct {
		since float64
		want  float64
	}{{30, 817}, {60, 25.5}, {90, 3.36}, {120, 1}, {600, 1}} {
		arrivals := ago(c.since)
		for i := 1; i < Blocks; i++ {
			arrivals = append(arrivals, t0.Add(-time.Duration(c.since+600*float64(i))*time.Second))
		}
		got := Factor(t0, arrivals)
		if math.Abs(got/c.want-1) > 0.01 {
			t.Errorf("%.0f s after a block: x%.3f, want x%.3f", c.since, got, c.want)
		}
	}
}

// Blocks that came in a burst make the longer windows bind even long after
// the last one.
func TestFactorAfterABurst(t *testing.T) {
	burst := make([]float64, Blocks)
	for i := range burst {
		burst[i] = 300 + 60*float64(i) // 17 blocks within 16 minutes, the last 5 minutes ago
	}
	if f := Factor(t0, ago(burst...)); f < 10 {
		t.Fatalf("after a burst: x%.2f", f)
	}
}

func TestFactorWithoutHistory(t *testing.T) {
	if f := Factor(t0, nil); f != 1 {
		t.Fatalf("no blocks known: x%v", f)
	}
	if f := Factor(t0, ago(600)); f != 1 {
		t.Fatalf("one old block: x%v", f)
	}
}

// Efficiency matches a simulation of the rule: the network at a constant
// hashrate, with nBits set so that blocks average 600 s, as ASERT does.
func TestEfficiency(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	mean := func(rate float64) float64 {
		rnd := rand.New(rand.NewSource(1))
		var arrivals []time.Time
		now := t0
		for i := range Blocks {
			arrivals = append(arrivals, now.Add(-time.Duration(600*(i+1))*time.Second))
		}
		const n = 2500
		var total float64
		for range n {
			need, acc, secs := rnd.ExpFloat64(), 0.0, 0.0
			for acc < need {
				secs++
				acc += rate / Factor(now.Add(time.Duration(secs)*time.Second), arrivals)
			}
			now = now.Add(time.Duration(secs) * time.Second)
			total += secs
			arrivals = append([]time.Time{now}, arrivals[:Blocks-1]...)
		}
		return total / n
	}
	lo, hi := 1/700.0, 1/400.0
	for range 9 {
		mid := (lo + hi) / 2
		if mean(mid) > 600 {
			lo = mid
		} else {
			hi = mid
		}
	}
	got := 1 / (600 * (lo + hi) / 2)
	if math.Abs(got-Efficiency) > 0.025 {
		t.Fatalf("simulated efficiency %.3f, constant %.3f", got, Efficiency)
	}
}
