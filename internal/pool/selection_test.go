package pool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

// The pattern seen on a real pool: an anycast address that is usually the
// fastest but sometimes answers a probe in ~300 ms must stay preferred.
func TestSlowOutlierDoesNotMovePreferred(t *testing.T) {
	m, p := multiManager(t)
	eu, us := p.Addresses[0], p.Addresses[1]
	for _, ms := range []int{30, 32, 29} {
		m.probeResult(p, eu, time.Duration(ms)*time.Millisecond, nil)
		m.probeResult(p, us, 50*time.Millisecond, nil)
	}
	for _, ms := range []int{320, 31, 950, 30} {
		m.probeResult(p, eu, time.Duration(ms)*time.Millisecond, nil)
		m.probeResult(p, us, 50*time.Millisecond, nil)
		if got := order(m, p)[0]; got != "eu.example.com" {
			t.Fatalf("one slow probe (%d ms) moved new sessions to %s", ms, got)
		}
	}
	for _, e := range m.ev.List(0) {
		if e.Type == "pool_address_preferred" {
			t.Fatalf("unexpected switch event: %s", e.Message)
		}
	}
}

// Session dial failures count "in a row" only while no probe proves the
// address healthy.
func TestScatteredDialFailuresKeepAddressUp(t *testing.T) {
	m := testManager(t)
	p, _ := m.Snapshot().Get("a")
	a := p.Addresses[0]
	for i := 0; i < probeOKsToUp; i++ {
		m.probeResult(p, a, time.Millisecond, nil)
	}
	for i := 0; i < dialFailsToDown*2; i++ {
		m.dialResult(p, a, errors.New("transient"))
		m.probeResult(p, a, time.Millisecond, nil)
	}
	if h := m.Health("a"); h.Status != StatusUp {
		t.Fatalf("healthy address marked %s by scattered dial failures", h.Status)
	}
	for i := 0; i < dialFailsToDown; i++ {
		m.dialResult(p, a, errors.New("refused"))
	}
	if h := m.Health("a"); h.Status != StatusDown || h.LastCheck.IsZero() {
		t.Fatalf("consecutive dial failures: %+v", h)
	}
}

func TestPoolNamesAreUnique(t *testing.T) {
	m := testManager(t)
	name, host, port, user := "a", "c.example.com", 3333, "c.{worker}" // "A" exists
	if _, err := m.Create(Input{Name: &name, Host: &host, Port: &port, Username: &user}); err == nil {
		t.Fatal("a second pool with the same name (case-insensitive) was created")
	}
	if _, _, err := m.Update("b", Input{Name: &name}, false); err == nil {
		t.Fatal("a pool was renamed to an existing name")
	}
	same := "B"
	if _, _, err := m.Update("b", Input{Name: &same}, false); err != nil {
		t.Fatalf("saving a pool under its own name: %v", err)
	}
}

func TestTestConfigValidatesBeforeDialing(t *testing.T) {
	m := testManager(t)
	bad := []state.Address{{Host: "stratum+tcp://x", Port: 1}}
	if _, err := m.TestConfig(context.Background(), "", Input{Addresses: &bad}); err == nil {
		t.Fatal("invalid unsaved config was dialed")
	}
	if _, err := m.TestConfig(context.Background(), "missing", Input{}); err == nil {
		t.Fatal("unknown base pool accepted")
	}
}
