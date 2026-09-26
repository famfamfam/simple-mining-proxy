package monitor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

type farm struct {
	sessions []session.Info
	alerts   []Alert
	ev       *events.Log
}

// share records a share of worker at t, opening its session when needed.
func (f *farm) share(worker string, t time.Time) {
	for i := range f.sessions {
		if f.sessions[i].Worker == worker {
			f.sessions[i].LastSubmit = &t
			return
		}
	}
	f.sessions = append(f.sessions, session.Info{Worker: worker, PoolName: "Pool A", LastSubmit: &t, ConnectedAt: t})
}

func (f *farm) drop(worker string) {
	out := f.sessions[:0]
	for _, s := range f.sessions {
		if s.Worker != worker {
			out = append(out, s)
		}
	}
	f.sessions = out
}

func newMonitor(t *testing.T, f *farm, path string) *Monitor {
	t.Helper()
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	f.ev = events.New(100)
	m := New(Deps{
		Settings: set, Events: f.ev, Path: path,
		Miners: func(time.Time) []session.Info { return append([]session.Info(nil), f.sessions...) },
		Notify: func(a Alert) { f.alerts = append(f.alerts, a) },
	})
	m.started = t0.Add(-time.Hour) // past the start grace unless a test says otherwise
	m.now = func() time.Time { return t0 }
	return m
}

func (f *farm) lastAlert(t *testing.T) Alert {
	t.Helper()
	if len(f.alerts) == 0 {
		t.Fatal("no alert")
	}
	return f.alerts[len(f.alerts)-1]
}

func TestOfflineAfterSilence(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	f.share("s21-1", t0)
	f.share("s21-2", t0)
	m.check(t0)
	f.share("s21-2", t0.Add(55*time.Second))
	m.check(t0.Add(55 * time.Second))
	if len(f.alerts) != 0 {
		t.Fatalf("alert before offline_after: %+v", f.alerts)
	}
	f.share("s21-2", t0.Add(61*time.Second))
	m.check(t0.Add(61 * time.Second))
	a := f.lastAlert(t)
	if len(a.Changes) != 1 || a.Changes[0].Name != "s21-1" || a.Changes[0].Online || !a.Changes[0].LastShare.Equal(t0) {
		t.Fatalf("changes = %+v", a.Changes)
	}
	if a.Online != 1 || a.Total != 2 {
		t.Fatalf("online %d of %d, want 1 of 2", a.Online, a.Total)
	}
	if a.Changes[0].Pool != "Pool A" {
		t.Fatalf("pool = %q", a.Changes[0].Pool)
	}
	// Reported once.
	m.check(t0.Add(70 * time.Second))
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(f.alerts))
	}
	ev := f.ev.List(1)
	if len(ev) != 1 || ev[0].Type != "miner_offline" || !strings.Contains(ev[0].Message, "s21-1") {
		t.Fatalf("event = %+v", ev)
	}
}

func TestBackOnline(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	f.share("s21-1", t0)
	m.check(t0)
	f.drop("s21-1")
	m.check(t0.Add(2 * time.Minute))
	back := t0.Add(12 * time.Minute)
	f.share("s21-1", back)
	m.check(back.Add(time.Second))
	a := f.lastAlert(t)
	c := a.Changes[0]
	if len(f.alerts) != 2 || !c.Online || c.Away != 12*time.Minute || a.Online != 1 {
		t.Fatalf("alerts = %+v", f.alerts)
	}
	if ev := f.ev.List(1); ev[0].Type != "miner_online" {
		t.Fatalf("event = %+v", ev[0])
	}
}

func TestNotWatchedBeforeTheFirstShare(t *testing.T) {
	f := &farm{sessions: []session.Info{{Worker: "probe", ConnectedAt: t0}, {ConnectedAt: t0}}}
	m := newMonitor(t, f, "")
	m.check(t0)
	m.check(t0.Add(time.Hour))
	if len(f.alerts) != 0 || len(m.Miners()) != 0 {
		t.Fatalf("alerts %+v, miners %+v", f.alerts, m.Miners())
	}
}

func TestSlowASICWaitsForItsOwnShareRate(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	f.share("bitaxe", t0)
	// 1 TH/s at difficulty 10000: a share every ~43 s on average.
	f.sessions[0].HashrateHs = 1e12
	f.sessions[0].Difficulty = 10000
	f.sessions[0].ConnectedAt = t0.Add(-time.Hour)
	m.check(t0)
	m.check(t0.Add(5 * time.Minute))
	if len(f.alerts) != 0 {
		t.Fatalf("a slow ASIC is offline after 5 minutes: %+v", f.alerts)
	}
	m.check(t0.Add(15 * time.Minute)) // 20 intervals ≈ 14.3 minutes
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(f.alerts))
	}
}

func TestFreshSessionDoesNotStretchTheWait(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	f.share("s21", t0)
	// Right after connecting the estimate is too rough to use.
	f.sessions[0].HashrateHs = 1e9
	f.sessions[0].Difficulty = 1e6
	m.check(t0)
	f.drop("s21")
	m.check(t0.Add(61 * time.Second))
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(f.alerts))
	}
}

func TestWaitIsCapped(t *testing.T) {
	w := &worker{hashrate: 1, difficulty: 1e9}
	if got := wait(w, time.Minute); got != maxWait {
		t.Fatalf("wait = %s, want %s", got, maxWait)
	}
	if got := wait(&worker{}, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("wait without an estimate = %s", got)
	}
}

func TestStartGrace(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	m.started = t0
	f.share("s21-1", t0.Add(-time.Hour))
	m.check(t0)
	m.check(t0.Add(4 * time.Minute))
	if len(f.alerts) != 0 {
		t.Fatalf("offline during the start grace: %+v", f.alerts)
	}
	// Not reported yet, but not shown as mining either.
	m.now = func() time.Time { return t0.Add(4 * time.Minute) }
	if got := m.Miners(); len(got) != 1 || got[0].Online {
		t.Fatalf("miners during the grace = %+v", got)
	}
	m.check(t0.Add(startGrace))
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts after the grace, want 1", len(f.alerts))
	}
}

func TestStatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.json")
	f := &farm{}
	m := newMonitor(t, f, path)
	f.share("s21-1", t0)
	f.share("s21-2", t0)
	m.check(t0)
	f.share("s21-2", t0.Add(2*time.Minute))
	m.check(t0.Add(2 * time.Minute)) // s21-1 offline, saved at once
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(f.alerts))
	}

	// After a restart s21-1 is still known as offline: no second alert.
	f2 := &farm{}
	m2 := newMonitor(t, f2, path)
	m2.check(t0.Add(3 * time.Minute))
	if len(f2.alerts) != 0 {
		t.Fatalf("offline reported again after a restart: %+v", f2.alerts)
	}
	m2.now = func() time.Time { return t0.Add(3 * time.Minute) }
	got := m2.Miners()
	if len(got) != 2 || got[0].Name != "s21-1" || got[0].Online || !got[1].Online {
		t.Fatalf("miners after a restart = %+v", got)
	}
	// It comes back: reported.
	f2.share("s21-1", t0.Add(4*time.Minute))
	f2.share("s21-2", t0.Add(4*time.Minute))
	m2.check(t0.Add(4 * time.Minute))
	if len(f2.alerts) != 1 || !f2.alerts[0].Changes[0].Online {
		t.Fatalf("alerts = %+v", f2.alerts)
	}
}

// A recovery alert during the start grace must not count a still-silent
// ASIC as mining just because it was never declared offline yet: judge
// (which gates the offline transition) and the online count must agree.
func TestAlertOnlineCountDuringStartGrace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.json")
	old := t0.Add(-time.Hour).Format(time.RFC3339Nano)
	// From before the restart: s21-a was mining (Offline still false in the
	// saved file), s21-b had already been declared offline.
	content := fmt.Sprintf(`{"workers":{"s21-a":{"last_share":%q},"s21-b":{"last_share":%q,"offline":true}}}`, old, old)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &farm{}
	m := newMonitor(t, f, path)
	m.started = t0 // a fresh grace starts now

	// Right after the restart nobody has reconnected yet: both are silent.
	m.check(t0)

	// s21-b reconnects within the grace; s21-a is still silent but its
	// Offline flag stayed false, so it must not count as mining here.
	f.share("s21-b", t0.Add(30*time.Second))
	m.check(t0.Add(30 * time.Second))

	a := f.lastAlert(t)
	if len(a.Changes) != 1 || !a.Changes[0].Online || a.Changes[0].Name != "s21-b" {
		t.Fatalf("changes = %+v", a.Changes)
	}
	if a.Online != 1 || a.Total != 2 {
		t.Fatalf("online %d of %d during the grace, want 1 of 2 (s21-a is silent)", a.Online, a.Total)
	}
}

func TestDamagedStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &farm{}
	m := newMonitor(t, f, path)
	if len(m.Miners()) != 0 {
		t.Fatal("miners from a damaged file")
	}
}

func TestForgetsLongOffline(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	f.share("old", t0)
	m.check(t0)
	f.drop("old")
	m.check(t0.Add(2 * time.Minute))
	m.check(t0.Add(forgetAfter - time.Minute))
	if len(m.Miners()) != 1 {
		t.Fatal("forgotten too early")
	}
	m.check(t0.Add(forgetAfter + time.Minute))
	if len(m.Miners()) != 0 {
		t.Fatalf("still watched: %+v", m.Miners())
	}
}

func TestOneAlertPerCheck(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	for _, w := range []string{"s21-10", "s21-2", "s21-1"} {
		f.share(w, t0)
	}
	m.check(t0)
	m.check(t0.Add(2 * time.Minute))
	if len(f.alerts) != 1 {
		t.Fatalf("%d alerts, want 1", len(f.alerts))
	}
	var names []string
	for _, c := range f.alerts[0].Changes {
		names = append(names, c.Name)
	}
	if strings.Join(names, " ") != "s21-1 s21-2 s21-10" || f.alerts[0].Online != 0 || f.alerts[0].Total != 3 {
		t.Fatalf("alert = %+v", f.alerts[0])
	}
}

func TestSharedLoginIsOneASIC(t *testing.T) {
	f := &farm{}
	m := newMonitor(t, f, "")
	a, b := t0, t0.Add(30*time.Second)
	f.sessions = []session.Info{
		{Worker: "farm", PoolName: "Pool A", LastSubmit: &a, HashrateHs: 1e14},
		{Worker: "farm", PoolName: "Pool B", LastSubmit: &b, HashrateHs: 2e14},
	}
	m.check(b)
	got := m.Miners()
	if len(got) != 1 || got[0].Hashrate != 3e14 || got[0].Pool != "Pool B" || !got[0].LastShare.Equal(b) {
		t.Fatalf("miners = %+v", got)
	}
}

func TestNameLess(t *testing.T) {
	names := []string{"s21-10", "a", "s21-2", "s21-02b", "S9", "s21-1", "s21-", "10", "9"}
	sort.Slice(names, func(i, j int) bool { return NameLess(names[i], names[j]) })
	want := "9 10 S9 a s21- s21-1 s21-2 s21-02b s21-10"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}
