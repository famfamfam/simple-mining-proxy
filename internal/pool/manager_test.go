package pool

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	pools := []state.Pool{
		{ID: "a", Name: "A", Addresses: []state.Address{{Host: "a.example.com", Port: 3333}}, Username: "a.{worker}"},
		{ID: "b", Name: "B", Addresses: []state.Address{{Host: "b.example.com", Port: 3333}}, Username: "b.{worker}"},
	}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools = pools
		f.ActivePool = "a"
		f.FallbackPools = []string{"b"}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	ev := events.New(100)
	return NewManager(st, set, session.NewRegistry(ev), ev)
}

func TestActivateClearsStaleDownHealth(t *testing.T) {
	m := testManager(t)
	b, _ := m.Snapshot().Get("b")
	for i := 0; i < probeFailsToDown; i++ {
		m.probeResult(b, b.Addresses[0], 0, errors.New("old failure"))
	}
	if m.Health("b").Status != StatusDown {
		t.Fatal("setup: pool b should be DOWN")
	}

	if _, err := m.Activate(context.Background(), "b", true); err != nil {
		t.Fatal(err)
	}
	if got := m.Effective(); got != "b" {
		t.Fatalf("effective pool = %q, want b", got)
	}
	if fallback := m.Snapshot().Fallback; len(fallback) != 1 || fallback[0] != "a" {
		t.Fatalf("previous active pool was not preserved as fallback: %v", fallback)
	}
	if got := m.Health("b").Status; got != StatusUnknown {
		t.Fatalf("health after activation = %s, want UNKNOWN", got)
	}
}

func TestProbeFailureRestartsHealthyWindow(t *testing.T) {
	m := testManager(t)
	p, _ := m.Snapshot().Get("a")
	addr := p.Addresses[0]
	m.probeResult(p, addr, time.Millisecond, nil)
	m.probeResult(p, addr, time.Millisecond, nil)
	if h := m.Health("a"); h.Status != StatusUp || h.UpSince.IsZero() {
		t.Fatalf("pool did not become UP: %+v", h)
	}

	m.probeResult(p, addr, 0, errors.New("temporary failure"))
	if h := m.Health("a"); h.Status != StatusUp || !h.UpSince.IsZero() {
		t.Fatalf("failed probe did not break healthy window: %+v", h)
	}
	m.probeResult(p, addr, time.Millisecond, nil)
	if h := m.Health("a"); !h.UpSince.IsZero() {
		t.Fatalf("one successful probe restarted healthy window: %+v", h)
	}
	m.probeResult(p, addr, time.Millisecond, nil)
	if h := m.Health("a"); h.Status != StatusUp || h.UpSince.IsZero() {
		t.Fatalf("two successful probes did not restart healthy window: %+v", h)
	}
}

func TestConcurrentActivationsKeepMetadataConsistent(t *testing.T) {
	m := testManager(t)
	for i := 0; i < 10; i++ {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, id := range []string{"a", "b"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				<-start
				if _, err := m.Activate(context.Background(), id, true); err != nil {
					t.Errorf("activate %s: %v", id, err)
				}
			}(id)
		}
		close(start)
		wg.Wait()
		active := m.Snapshot().Active
		if m.LastSwitch() == nil || m.LastSwitch().To != active {
			t.Fatalf("active=%q last switch=%+v", active, m.LastSwitch())
		}
	}
}

func TestSnapshotRole(t *testing.T) {
	s := &Snapshot{Active: "a", Fallback: []string{"b", "c"}}
	cases := map[string]struct {
		role string
		pos  int
	}{"a": {"active", 0}, "b": {"fallback", 1}, "c": {"fallback", 2}, "d": {"none", 0}}
	for id, want := range cases {
		if role, pos := s.Role(id); role != want.role || pos != want.pos {
			t.Errorf("%s: got %s/%d, want %s/%d", id, role, pos, want.role, want.pos)
		}
	}
}

// multiManager has one pool "m" with three addresses.
func multiManager(t *testing.T) (*Manager, state.Pool) {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := state.Pool{ID: "m", Name: "M", Username: "u", Addresses: []state.Address{
		{Host: "eu.example.com", Port: 3333}, {Host: "us.example.com", Port: 3333}, {Host: "asia.example.com", Port: 3333},
	}}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools = []state.Pool{p}
		f.ActivePool = "m"
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	set, _, _ := settings.NewStore(nil)
	ev := events.New(100)
	return NewManager(st, set, session.NewRegistry(ev), ev), p
}

func order(m *Manager, p state.Pool) []string {
	var out []string
	for _, a := range m.orderedAddresses(p) {
		out = append(out, a.Host)
	}
	return out
}

func TestFastestAddressIsPreferred(t *testing.T) {
	m, p := multiManager(t)
	eu, us, asia := p.Addresses[0], p.Addresses[1], p.Addresses[2]
	// Nothing measured yet: configuration order.
	if got := order(m, p); got[0] != "eu.example.com" {
		t.Fatalf("unmeasured order: %v", got)
	}
	m.probeResult(p, eu, 80*time.Millisecond, nil)
	m.probeResult(p, us, 20*time.Millisecond, nil)
	m.probeResult(p, asia, 150*time.Millisecond, nil)
	if got := order(m, p); got[0] != "us.example.com" || got[1] != "eu.example.com" || got[2] != "asia.example.com" {
		t.Fatalf("order by latency: %v", got)
	}
}

func TestPreferredAddressHasHysteresis(t *testing.T) {
	m, p := multiManager(t)
	eu, us := p.Addresses[0], p.Addresses[1]
	m.probeResult(p, eu, 30*time.Millisecond, nil)
	m.probeResult(p, us, 40*time.Millisecond, nil)
	if got := order(m, p)[0]; got != "eu.example.com" {
		t.Fatalf("preferred: %s", got)
	}
	// us becomes slightly faster: not enough to move new sessions.
	for i := 0; i < 20; i++ {
		m.probeResult(p, eu, 30*time.Millisecond, nil)
		m.probeResult(p, us, 28*time.Millisecond, nil)
	}
	if got := order(m, p)[0]; got != "eu.example.com" {
		t.Fatalf("switched on a 2 ms difference: %s", got)
	}
	// us becomes clearly faster.
	for i := 0; i < 20; i++ {
		m.probeResult(p, eu, 30*time.Millisecond, nil)
		m.probeResult(p, us, 10*time.Millisecond, nil)
	}
	if got := order(m, p)[0]; got != "us.example.com" {
		t.Fatalf("did not switch to a clearly faster address: %s", got)
	}
}

func TestDownAddressIsSkippedPoolStaysUp(t *testing.T) {
	m, p := multiManager(t)
	eu, us, asia := p.Addresses[0], p.Addresses[1], p.Addresses[2]
	for i := 0; i < probeOKsToUp; i++ {
		m.probeResult(p, eu, 10*time.Millisecond, nil)
		m.probeResult(p, us, 50*time.Millisecond, nil)
		m.probeResult(p, asia, 90*time.Millisecond, nil)
	}
	for i := 0; i < probeFailsToDown; i++ {
		m.probeResult(p, eu, 0, errors.New("connection refused"))
	}
	if got := order(m, p); len(got) != 2 || got[0] != "us.example.com" {
		t.Fatalf("DOWN address still dialed first: %v", got)
	}
	if h := m.Health("m"); h.Status != StatusUp {
		t.Fatalf("pool must stay UP while another address works: %+v", h)
	}
	for _, a := range []state.Address{us, asia} {
		for i := 0; i < probeFailsToDown; i++ {
			m.probeResult(p, a, 0, errors.New("timeout"))
		}
	}
	if h := m.Health("m"); h.Status != StatusDown {
		t.Fatalf("pool must be DOWN when every address is: %+v", h)
	}
}

func TestEditKeepsHealthOfUnchangedAddresses(t *testing.T) {
	m, p := multiManager(t)
	eu := p.Addresses[0]
	m.probeResult(p, eu, 25*time.Millisecond, nil)
	next := p
	next.Addresses = []state.Address{eu, {Host: "new.example.com", Port: 3333}}
	if _, _, err := m.Update("m", Input{Addresses: &next.Addresses}, false); err != nil {
		t.Fatal(err)
	}
	cur, _ := m.Snapshot().Get("m")
	ah := m.AddressHealth(cur)
	if len(ah) != 2 || ah[0].RTT == 0 || ah[1].RTT != 0 || ah[1].Status != StatusUnknown {
		t.Fatalf("address health after edit: %+v", ah)
	}
}

func TestPreferredEventsOnlyForSpeedSwitches(t *testing.T) {
	m, p := multiManager(t)
	eu, us := p.Addresses[0], p.Addresses[1]
	count := func() (n int) {
		for _, e := range m.ev.List(0) {
			if e.Type == "pool_address_preferred" {
				n++
			}
		}
		return n
	}
	m.probeResult(p, us, 20*time.Millisecond, nil) // first measurement: eu→us, no event
	m.probeResult(p, eu, 90*time.Millisecond, nil)
	if n := count(); n != 0 {
		t.Fatalf("first measurement reported as a switch: %d events", n)
	}
	for i := 0; i < 20; i++ {
		m.probeResult(p, eu, 5*time.Millisecond, nil)
	}
	if n := count(); n != 1 {
		t.Fatalf("speed switch events: %d, want 1", n)
	}
}
