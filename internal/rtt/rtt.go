// Package rtt follows eCash's Real Time Targeting. Besides the target in its
// header (nBits, set by ASERT), an eCash block must meet a real-time target
// that depends on how long ago the previous blocks arrived: right after a
// block it is hundreds of times harder, and about two minutes later it no
// longer binds. The formula is the one of the Bitcoin ABC node,
// src/policy/block/rtt.cpp.
package rtt

import (
	"math"
	"time"
)

// Blocks is how many previous blocks the rule looks at.
const Blocks = 17

// Efficiency is the share of the block rate that nBits alone suggests which
// a steady miner gets on eCash in the long run. The first minutes after
// every block are practically dead for everyone, and ASERT lowers nBits so
// the chain still makes a block every 10 minutes. It comes from simulating
// the rule at a constant hashrate; TestEfficiency checks it.
const Efficiency = 0.757

// k is RTT_K of the node: the target grows with the fifth power of time.
const k = 6.0

// c is K · Γ(1 + 1/K)^K.
var c = k * math.Pow(math.Gamma(1+1/k), k)

// windows are the blocks back the rule measures from, and the time span each
// window is scaled to.
var windows = []struct {
	back int
	span float64 // seconds
}{{1, 150}, {2, 600}, {5, 2400}, {11, 6000}, {17, 9600}}

// Factor is how many times harder than its header target a block found at
// now has to be: 1 when the real-time target does not bind. arrivals are the
// times the previous blocks arrived, newest first; windows reaching past the
// known blocks do not constrain.
func Factor(now time.Time, arrivals []time.Time) float64 {
	m := math.Inf(1)
	for _, w := range windows {
		if w.back > len(arrivals) {
			break
		}
		age := math.Max(1, now.Sub(arrivals[w.back-1]).Seconds())
		m = math.Min(m, c*math.Pow(age/w.span, k-1))
	}
	if m >= 1 {
		return 1
	}
	return 1 / m
}
