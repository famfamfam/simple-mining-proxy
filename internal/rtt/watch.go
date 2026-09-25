package rtt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// Coin is the tag of the pools the watcher follows: RTT is an eCash rule.
const Coin = "XEC"

const (
	// silence is how long the watcher waits for any message before it
	// reconnects; pools send a new job at least with every block.
	silence = 15 * time.Minute
	// stale is how long block times stay usable while disconnected.
	stale = 10 * time.Minute
)

var errChanged = errors.New("another pool to watch")

// Watcher keeps a quiet Stratum connection to an eCash pool to see when
// blocks arrive: the pool sends a job with a new previous-block hash as soon
// as its node has the block. Those times, like the node's own, give the real
// time target now. It logs in as the test worker, which the pool may list as
// an idle worker.
type Watcher struct {
	d   WatchDeps
	now func() time.Time

	mu        sync.Mutex
	pool      string      // pool watched, "" when there is no eCash pool
	connected bool        // logged in and reading jobs
	arrivals  []time.Time // when the last blocks arrived, newest first
	prevhash  string      // previous-block hash of the current job
	nbits     uint32      // target of the current job
	updated   time.Time   // last job
	lastErr   string
}

type WatchDeps struct {
	Settings *settings.Store
	Pools    *pool.Manager
	// Seed returns when recent blocks arrived, newest first, for the time
	// before the watcher saw blocks itself. Optional.
	Seed func(ctx context.Context) ([]time.Time, error)
}

func NewWatcher(d WatchDeps) *Watcher { return &Watcher{d: d, now: time.Now} }

// pick is the pool to watch: the timed target if it mines eCash, else the
// first eCash pool.
func (w *Watcher) pick() (state.Pool, bool) {
	var first *state.Pool
	for _, p := range w.d.Pools.Snapshot().Pools {
		if p.Coin != Coin || len(p.Addresses) == 0 {
			continue
		}
		if p.TimedTarget {
			return p, true
		}
		if first == nil {
			first = &p
		}
	}
	if first == nil {
		return state.Pool{}, false
	}
	return *first, true
}

func same(a, b state.Pool) bool {
	return a.ID == b.ID && slices.Equal(a.Addresses, b.Addresses) && a.TLS == b.TLS &&
		a.TLSSkipVerify == b.TLSSkipVerify && a.Username == b.Username && a.Password == b.Password
}

// Run watches until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	backoff := 5 * time.Second
	for ctx.Err() == nil {
		p, ok := w.pick()
		if !ok {
			w.mu.Lock()
			w.pool, w.connected, w.lastErr = "", false, ""
			w.mu.Unlock()
			if !sleep(ctx, 10*time.Second) {
				return
			}
			continue
		}
		w.seed(ctx)
		started := time.Now()
		err := w.watch(ctx, p)
		w.mu.Lock()
		w.connected = false
		if err != nil && !errors.Is(err, errChanged) && ctx.Err() == nil {
			w.lastErr = err.Error()
		}
		w.mu.Unlock()
		if errors.Is(err, errChanged) {
			continue
		}
		if time.Since(started) > 5*time.Minute {
			backoff = 5 * time.Second
		}
		if !sleep(ctx, backoff) {
			return
		}
		backoff = min(2*backoff, 2*time.Minute)
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// seed fills the block times from Seed when the watcher has none of its
// own, or only old ones after a long break.
func (w *Watcher) seed(ctx context.Context) {
	w.mu.Lock()
	fresh := len(w.arrivals) > 0 && w.now().Sub(w.updated) < stale
	w.mu.Unlock()
	if fresh || w.d.Seed == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	times, err := w.d.Seed(ctx)
	if err != nil || len(times) == 0 {
		return
	}
	w.mu.Lock()
	w.arrivals = times[:min(len(times), Blocks)]
	w.prevhash = "" // the next job is the baseline, not a new block
	w.mu.Unlock()
}

// watch reads jobs from p until the connection fails or another pool is to
// be watched.
func (w *Watcher) watch(ctx context.Context, p state.Pool) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if q, ok := w.pick(); !ok || !same(p, q) {
					cancel(errChanged)
					return
				}
			}
		}
	}()

	cfg := w.d.Settings.Get()
	var conn net.Conn
	var err error
	for _, a := range p.Addresses {
		if conn, _, err = pool.Dial(ctx, p, a, cfg.UpstreamDialTimeout); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	w.mu.Lock()
	w.pool, w.lastErr = p.ID, ""
	w.mu.Unlock()

	rw := stratum.RewriteLogin(cfg.TestWorker, "x", p.Username, p.Password)
	login := fmt.Sprintf(`{"id":1,"method":"mining.subscribe","params":["sha256-stratum-proxy/1.0"]}`+"\n"+
		`{"id":2,"method":"mining.authorize","params":[%q,%q]}`+"\n", rw.User, rw.Password)
	if _, err := io.WriteString(conn, login); err != nil {
		return err
	}
	lr := stratum.NewLineReader(conn, cfg.MaxLineBytes())
	for {
		conn.SetReadDeadline(time.Now().Add(silence))
		line, err := lr.ReadLine()
		if err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return err
		}
		msg, err := stratum.Parse(line)
		if err != nil {
			continue
		}
		id := msg.IDKey()
		switch {
		case msg.Method == "mining.notify":
			w.job(msg.Params)
		case msg.Method == "client.reconnect":
			return errors.New("the pool asked to reconnect")
		case msg.Method != "":
			// set_difficulty, set_extranonce, requests: not needed for block times
		case (id == "1" || id == `"1"`) && !stratum.IsNull(msg.Error):
			return errors.New("mining.subscribe: " + stratum.ErrorReason(msg.Error))
		case id == "2" || id == `"2"`:
			if !stratum.IsNull(msg.Error) || !stratum.ResultTrue(msg.Result) {
				return fmt.Errorf("mining.authorize as %q rejected", rw.User)
			}
			w.mu.Lock()
			w.connected = true
			w.mu.Unlock()
		}
	}
}

// job takes a mining.notify: a new previous-block hash means a block arrived.
func (w *Watcher) job(raw json.RawMessage) {
	params, err := stratum.ParamsArray(raw)
	if err != nil {
		return
	}
	prev, ok := stratum.StringAt(params, 1)
	if !ok {
		return
	}
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.updated = now
	if w.prevhash != "" && prev != w.prevhash {
		w.arrivals = append([]time.Time{now}, w.arrivals[:min(len(w.arrivals), Blocks-1)]...)
	}
	w.prevhash = prev
	if s, ok := stratum.StringAt(params, 6); ok {
		if bits, err := strconv.ParseUint(s, 16, 32); err == nil {
			w.nbits = uint32(bits)
		}
	}
}

// known reports whether the block times describe the chain now.
func (w *Watcher) known(now time.Time) bool {
	return len(w.arrivals) > 0 && (w.connected || now.Sub(w.updated) < stale)
}

// Hardness is how many times harder than its header target a block found
// now has to be; 1 when not known.
func (w *Watcher) Hardness() float64 {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.known(now) {
		return 1
	}
	return Factor(now, w.arrivals)
}

// Status is what the API shows.
type Status struct {
	Pool       string     `json:"pool"` // pool watched, "" when there is no eCash pool
	Connected  bool       `json:"connected"`
	Difficulty float64    `json:"difficulty"` // of the block being mined, from its header target
	Factor     float64    `json:"factor"`     // how many times harder the real time target is now; 1 when not known
	LastBlock  *time.Time `json:"last_block"` // when the last block arrived
	Error      string     `json:"error,omitempty"`
}

func (w *Watcher) Status() Status {
	now := w.now()
	w.mu.Lock()
	defer w.mu.Unlock()
	st := Status{Pool: w.pool, Connected: w.connected, Difficulty: Difficulty(w.nbits), Factor: 1, Error: w.lastErr}
	if w.known(now) {
		st.Factor = Factor(now, w.arrivals)
		last := w.arrivals[0]
		st.LastBlock = &last
	}
	return st
}

// Difficulty of a compact target (nBits), against the difficulty-1 target
// 0x1d00ffff; 0 for an empty target.
func Difficulty(bits uint32) float64 {
	mantissa := float64(bits & 0x007fffff)
	if mantissa == 0 {
		return 0
	}
	return 0xffff / mantissa * math.Pow(256, float64(0x1d-int(bits>>24)))
}

// BlockchairURL lists the latest eCash blocks with their times.
const BlockchairURL = "https://api.blockchair.com/ecash/blocks?limit=17"

// Blockchair returns a Seed that reads block times from Blockchair. Header
// times are set by miners and differ a little from arrival times, so they
// only stand in until the watcher sees blocks itself.
func Blockchair(client *http.Client, url string) func(ctx context.Context) ([]time.Time, error) {
	return func(ctx context.Context) ([]time.Time, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "SimpleMiningProxy (eCash block times, once per start)")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
		}
		var body struct {
			Data []struct {
				Time string `json:"time"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
			return nil, err
		}
		var out []time.Time
		for _, b := range body.Data {
			t, err := time.Parse(time.DateTime, b.Time)
			if err != nil {
				return nil, err
			}
			out = append(out, t.UTC())
		}
		slices.SortFunc(out, func(a, b time.Time) int { return b.Compare(a) })
		return out, nil
	}
}
