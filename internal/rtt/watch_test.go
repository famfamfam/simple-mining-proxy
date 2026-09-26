package rtt

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

func TestDifficultyFromNBits(t *testing.T) {
	// Bitcoin block 840000.
	if d := Difficulty(0x17034219); math.Abs(d/86388558925171.02-1) > 1e-9 {
		t.Fatalf("difficulty %f", d)
	}
	if Difficulty(0) != 0 {
		t.Fatal("empty target")
	}
}

func TestBlockchairSeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":968431,"time":"2026-09-25 21:11:29"},{"id":968430,"time":"2026-09-25 21:01:10"}]}`))
	}))
	defer srv.Close()
	times, err := Blockchair(srv.Client(), srv.URL)(context.Background())
	if err != nil || len(times) != 2 || !times[0].Equal(time.Date(2026, 9, 25, 21, 11, 29, 0, time.UTC)) || !times[0].After(times[1]) {
		t.Fatalf("times %v, %v", times, err)
	}
}

// clock is a test clock the watcher reads from its own goroutine.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time  { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

// fakePool answers the login (accepting it or not) and then sends every job
// written to jobs.
func fakePool(t *testing.T, jobs <-chan string, accept bool) state.Address {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewScanner(c)
		for i := 0; i < 2 && r.Scan(); i++ {
			var m struct {
				ID int `json:"id"`
			}
			json.Unmarshal(r.Bytes(), &m)
			result := `[[["mining.notify","1"]],"00000001",4]`
			if m.ID == 2 {
				result = fmt.Sprint(accept)
			}
			fmt.Fprintf(c, `{"id":%d,"result":%s,"error":null}`+"\n", m.ID, result)
		}
		for j := range jobs {
			fmt.Fprintln(c, j)
		}
	}()
	return state.Address{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}
}

func job(prevhash, nbits string) string {
	return fmt.Sprintf(`{"id":null,"method":"mining.notify","params":["j","%s","c1","c2",[],"20000000","%s","66f41b3a",true]}`, prevhash, nbits)
}

func manager(t *testing.T, pools ...state.Pool) *pool.Manager {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Mutate(func(f *state.File) error {
		f.Pools = pools
		if len(pools) > 0 {
			f.ActivePool = pools[0].ID
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	set, _, _ := settings.NewStore(nil)
	ev := events.New(10)
	return pool.NewManager(st, set, session.NewRegistry(ev), ev)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWatcherSeesBlocks(t *testing.T) {
	jobs := make(chan string, 4)
	defer close(jobs)
	btc := state.Pool{ID: "btc", Name: "BTC", Coin: "BTC", Username: "u", Addresses: []state.Address{{Host: "btc.invalid", Port: 1}}}
	xec := state.Pool{ID: "xec", Name: "XEC", Coin: "XEC", Username: "ecash:q.{worker}", Addresses: []state.Address{fakePool(t, jobs, true)}}
	set, _, _ := settings.NewStore(nil)
	clk := &clock{t: t0}
	var blocks atomic.Int32
	w := NewWatcher(WatchDeps{Settings: set, Pools: manager(t, btc, xec),
		Seed: func(context.Context) ([]time.Time, error) {
			return []time.Time{t0.Add(-5 * time.Minute), t0.Add(-15 * time.Minute)}, nil
		},
		OnBlock: func() { blocks.Add(1) }})
	w.now = clk.now
	if w.Live() {
		t.Fatal("live before connecting")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	jobs <- job("aa", "1a0ab123") // the current block: a baseline, not a new one
	waitFor(t, "login and the first job", func() bool { st := w.Status(); return st.Connected && st.Difficulty > 0 })
	if st := w.Status(); st.Pool != "xec" || st.Factor != 1 || !st.LastBlock.Equal(t0.Add(-5*time.Minute)) {
		t.Fatalf("after the seed: %+v", st)
	}
	if !w.Live() || blocks.Load() != 0 {
		t.Fatalf("live %v, blocks %d after the baseline job", w.Live(), blocks.Load())
	}
	if d := w.Status().Difficulty; math.Abs(d/Difficulty(0x1a0ab123)-1) > 1e-12 {
		t.Fatalf("difficulty %v", d)
	}

	clk.set(t0.Add(time.Minute))
	jobs <- job("bb", "1a0ab123") // a new block arrives
	waitFor(t, "the new block", func() bool { lb := w.Status().LastBlock; return lb != nil && lb.Equal(t0.Add(time.Minute)) })
	if blocks.Load() != 1 {
		t.Fatalf("OnBlock called %d times, want 1", blocks.Load())
	}
	clk.set(t0.Add(90 * time.Second))
	if h := w.Hardness(); h < 100 {
		t.Fatalf("30 s after a block: x%.1f", h)
	}
	clk.set(t0.Add(10 * time.Minute))
	if h := w.Hardness(); h != 1 {
		t.Fatalf("9 minutes after a block: x%.2f", h)
	}
}

func TestWatcherWithoutECashPool(t *testing.T) {
	btc := state.Pool{ID: "btc", Name: "BTC", Coin: "BTC", Username: "u", Addresses: []state.Address{{Host: "btc.invalid", Port: 1}}}
	set, _, _ := settings.NewStore(nil)
	w := NewWatcher(WatchDeps{Settings: set, Pools: manager(t, btc)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(50 * time.Millisecond)
	if st := w.Status(); st.Pool != "" || st.Connected || w.Hardness() != 1 {
		t.Fatalf("no eCash pool: %+v", st)
	}
}

func TestWatcherLoginRejected(t *testing.T) {
	jobs := make(chan string)
	defer close(jobs)
	xec := state.Pool{ID: "xec", Name: "XEC", Coin: "XEC", Username: "u", Addresses: []state.Address{fakePool(t, jobs, false)}}
	set, _, _ := settings.NewStore(nil)
	w := NewWatcher(WatchDeps{Settings: set, Pools: manager(t, xec)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	waitFor(t, "the login error", func() bool { return w.Status().Error != "" })
	if st := w.Status(); st.Connected || w.Hardness() != 1 {
		t.Fatalf("rejected login: %+v", st)
	}
}
