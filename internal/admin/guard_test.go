package admin

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func fail(g *guard, ip string, now time.Time) attemptResult {
	return g.attempt(ip, now, func() bool { return false })
}

func pass(g *guard, ip string, now time.Time) attemptResult {
	return g.attempt(ip, now, func() bool { return true })
}

func TestGuardLocksIPAndEscalates(t *testing.T) {
	g := newGuard("")
	t0 := time.Now()
	for i := 0; i < ipFailLimit-1; i++ {
		if r := fail(g, "a", t0); r.Blocked || r.IPLock != 0 {
			t.Fatalf("attempt %d: %+v", i, r)
		}
	}
	if r := fail(g, "a", t0); r.IPLock != ipLockBase {
		t.Fatalf("first lock: %+v", r)
	}
	// Locked: even the right password is not checked.
	if r := pass(g, "a", t0.Add(ipLockBase-time.Second)); !r.Blocked || r.OK {
		t.Fatalf("locked IP got through: %+v", r)
	}
	// Other IPs are not affected.
	if r := pass(g, "b", t0); !r.OK {
		t.Fatalf("other IP blocked: %+v", r)
	}
	// Second lock doubles.
	t1 := t0.Add(ipLockBase)
	for i := 0; i < ipFailLimit-1; i++ {
		fail(g, "a", t1)
	}
	if r := fail(g, "a", t1); r.IPLock != 2*ipLockBase {
		t.Fatalf("second lock: %+v", r)
	}
}

func TestGuardLockIsCapped(t *testing.T) {
	g := newGuard("")
	now := time.Now()
	var last time.Duration
	for round := 0; round < 20; round++ {
		for i := 0; i < ipFailLimit; i++ {
			if r := fail(g, "a", now); r.IPLock > 0 {
				last = r.IPLock
				now = now.Add(r.IPLock)
			}
		}
	}
	if last != ipLockMax {
		t.Fatalf("lock after many rounds = %s, want %s", last, ipLockMax)
	}
}

func TestGuardSuccessClearsFailures(t *testing.T) {
	g := newGuard("")
	now := time.Now()
	for i := 0; i < ipFailLimit-1; i++ {
		fail(g, "a", now)
	}
	if r := pass(g, "a", now); !r.OK {
		t.Fatalf("login: %+v", r)
	}
	for i := 0; i < ipFailLimit-1; i++ {
		if r := fail(g, "a", now); r.IPLock != 0 {
			t.Fatalf("counter was not reset: %+v", r)
		}
	}
}

func TestGuardGlobalLockStopsManyIPs(t *testing.T) {
	g := newGuard("")
	now := time.Now()
	var started bool
	for i := 0; i < globalFailLimit; i++ {
		if r := fail(g, fmt.Sprintf("10.0.%d.%d", i/250, i%250), now); r.GlobalLock {
			started = true
		}
	}
	if !started {
		t.Fatal("global lock was not reported")
	}
	r := pass(g, "192.0.2.1", now.Add(time.Minute))
	if !r.Blocked || !r.Global || !r.RetryAt.Equal(now.Add(globalFailWindow)) {
		t.Fatalf("global lock not applied to a fresh IP: %+v", r)
	}
	if r := pass(g, "192.0.2.1", now.Add(globalFailWindow)); !r.OK {
		t.Fatalf("global lock did not expire: %+v", r)
	}
}

func TestGuardSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-guard.json")
	g := newGuard(path)
	now := time.Now()
	for i := 0; i < ipFailLimit; i++ {
		fail(g, "a", now)
	}
	g2 := newGuard(path)
	if r := pass(g2, "a", now.Add(time.Minute)); !r.Blocked {
		t.Fatalf("lock lost after restart: %+v", r)
	}
}

func TestGuardTableIsBounded(t *testing.T) {
	g := newGuard("")
	now := time.Now()
	for i := 0; i < maxGuardIPs+500; i++ {
		// Spread in time so the global limit does not kick in.
		fail(g, fmt.Sprintf("ip%d", i), now.Add(time.Duration(i)*time.Minute))
	}
	if n := len(g.ips); n > maxGuardIPs {
		t.Fatalf("guard table grew to %d", n)
	}
}
