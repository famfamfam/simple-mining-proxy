package profit

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

const (
	// cacheFor is how long fetched market data is reused (manual checks).
	cacheFor = 10 * time.Minute
	// fetchTimeout bounds one market data request, body included.
	fetchTimeout = 30 * time.Second
	// startDelay lets pool health settle before the first check after start.
	startDelay = 2 * time.Minute
)

// Decisions of a check.
const (
	NoData       = "no_data"       // market data unavailable for the active coin
	NoCandidates = "no_candidates" // no pool takes part in profit switching
	Manual       = "manual"        // the active pool does not take part: left alone
	Best         = "best"          // already on the most profitable coin
	BelowMargin  = "below_margin"  // another coin earns more, but less than the margin
	Recommend    = "recommend"     // should switch (advise mode or a manual check)
	Switched     = "switched"      // switched (auto mode)
	SwitchFailed = "switch_failed" // the switch was refused, e.g. the pool check failed
)

// CoinView is one coin of a report. Revenue is for 1 TH/s over a day.
type CoinView struct {
	Tag        string  `json:"tag"`
	Name       string  `json:"name"`
	PriceBTC   float64 `json:"price_btc"`
	PriceUSD   float64 `json:"price_usd"`  // 0 when unknown
	Difficulty float64 `json:"difficulty"` // 24-hour average
	// DifficultyNow is the latest network difficulty.
	DifficultyNow float64  `json:"difficulty_now"`
	BlockReward   float64  `json:"block_reward"`
	RevenueBTC    float64  `json:"revenue_btc"`
	RevenueUSD    float64  `json:"revenue_usd"`
	Stale         bool     `json:"stale"`
	Efficiency    float64  `json:"efficiency"` // see Coin.Efficiency; revenue includes it
	Pools         []string `json:"pools"`      // pool ids with this coin that take part
}

// Report is the result of one check.
type Report struct {
	At         time.Time  `json:"at"`
	Scheduled  bool       `json:"scheduled"` // false for a check from the UI
	Mode       string     `json:"mode"`
	Margin     int        `json:"margin"` // percent
	Coins      []CoinView `json:"coins"`  // never nil: empty when there is no market data
	Active     string     `json:"active"` // pool id
	ActiveCoin string     `json:"active_coin"`
	Best       string     `json:"best"`      // coin tag
	Advantage  float64    `json:"advantage"` // percent the best coin earns over the active one
	Decision   string     `json:"decision"`
	Target     string     `json:"target,omitempty"` // pool id to switch to
	Error      string     `json:"error,omitempty"`
}

type Deps struct {
	Settings *settings.Store
	Pools    *pool.Manager
	Events   *events.Log
	Fetch    Fetcher
	Path     string // state file, e.g. /data/profit.json
	// Home, when set, reports the pool timed switching returns to while the
	// farm is on its target for a while, "" otherwise. Coins are compared
	// for that pool, and scheduled checks wait until the farm is back.
	Home func() string
}

type Switcher struct {
	d   Deps
	now func() time.Time

	mu      sync.Mutex
	started time.Time // when Run began; zero until then
	lastRun time.Time // last scheduled check
	report  *Report   // latest check, scheduled or not
	market  *Market   // cached market data
	checkMu sync.Mutex
}

// saved is the state file: the schedule survives restarts, so a restart does
// not bring the next check (and possibly a switch) forward.
type saved struct {
	LastRun time.Time `json:"last_run"`
	Report  *Report   `json:"report,omitempty"`
}

func New(d Deps) *Switcher {
	s := &Switcher{d: d, now: time.Now}
	b, err := os.ReadFile(d.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("profit: cannot read the state file", "err", err)
	}
	var st saved
	if len(b) > 0 && json.Unmarshal(b, &st) == nil {
		s.lastRun, s.report = st.LastRun, st.Report
		if s.report != nil && s.report.Coins == nil {
			s.report.Coins = []CoinView{} // a file written before coins were always set
		}
	}
	return s
}

// Status is what the API shows: the schedule and the latest report.
type Status struct {
	Mode     string     `json:"mode"`
	Interval string     `json:"interval"`
	Margin   int        `json:"margin"`
	LastRun  *time.Time `json:"last_run"`
	NextRun  *time.Time `json:"next_run"`
	BTCUSD   float64    `json:"btc_usd"`
	Report   *Report    `json:"report"`
}

func (s *Switcher) Status() Status {
	v := s.d.Settings.Get()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Mode: v.ProfitSwitch, Interval: settings.FormatDuration(v.ProfitInterval), Margin: v.ProfitMargin, Report: s.report}
	if !s.lastRun.IsZero() {
		t := s.lastRun
		st.LastRun = &t
	}
	if v.ProfitSwitch != settings.ProfitOff {
		if t := s.nextRunLocked(v); !t.IsZero() {
			if now := s.now(); t.Before(now) {
				t = now // overdue, e.g. after the mode was turned on: at the next tick
			}
			st.NextRun = &t
		}
	}
	if s.market != nil {
		st.BTCUSD = s.market.BTCUSD
	}
	return st
}

// nextRunLocked is when the next scheduled check is due: an interval after
// the last one, but not in the first minutes after a start, so pools and
// miners settle first. Zero when neither is known.
func (s *Switcher) nextRunLocked(v *settings.Values) time.Time {
	var next time.Time
	if !s.lastRun.IsZero() {
		next = s.lastRun.Add(v.ProfitInterval)
	}
	if first := s.started.Add(startDelay); !s.started.IsZero() && first.After(next) {
		next = first
	}
	return next
}

func (s *Switcher) home() string {
	if s.d.Home == nil {
		return ""
	}
	return s.d.Home()
}

// Run checks on schedule until ctx is done.
func (s *Switcher) Run(ctx context.Context) {
	s.mu.Lock()
	s.started = s.now()
	s.mu.Unlock()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if s.due(s.d.Settings.Get(), s.now()) {
			s.Check(ctx, true)
		}
	}
}

// due reports whether the scheduled check should run now.
func (s *Switcher) due(v *settings.Values, now time.Time) bool {
	if v.ProfitSwitch == settings.ProfitOff || s.home() != "" {
		return false // off, or on the timed target: check once the farm is back
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !now.Before(s.nextRunLocked(v))
}

// Check compares the coins now. A scheduled check follows the mode: in auto
// mode it switches; a check from the UI (scheduled false) never switches
// and does not move the schedule.
func (s *Switcher) Check(ctx context.Context, scheduled bool) *Report {
	s.checkMu.Lock()
	defer s.checkMu.Unlock()
	v := s.d.Settings.Get()
	rep := &Report{At: s.now().UTC(), Scheduled: scheduled, Mode: v.ProfitSwitch, Margin: v.ProfitMargin, Coins: []CoinView{}}

	market, err := s.marketData(ctx)
	if err != nil {
		rep.Decision, rep.Error = NoData, err.Error()
	} else {
		s.decide(rep, market)
		if scheduled && rep.Decision == Recommend && v.ProfitSwitch == settings.ProfitAuto {
			if _, err := s.d.Pools.ActivateFor(ctx, rep.Target, "profit"); err != nil {
				rep.Decision, rep.Error = SwitchFailed, err.Error()
			} else {
				rep.Decision = Switched
			}
		}
	}
	if scheduled {
		s.event(rep)
	}

	s.mu.Lock()
	s.report = rep
	if scheduled {
		s.lastRun = rep.At
	}
	st := saved{LastRun: s.lastRun, Report: rep}
	s.mu.Unlock()
	if err := writeFile(s.d.Path, st); err != nil {
		slog.Warn("profit: cannot save the state file", "err", err)
	}
	return rep
}

func (s *Switcher) marketData(ctx context.Context) (*Market, error) {
	s.mu.Lock()
	m := s.market
	s.mu.Unlock()
	if m != nil && s.now().Sub(m.Fetched) < cacheFor {
		return m, nil
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	m, err := s.d.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.market = m
	s.mu.Unlock()
	return m, nil
}

// compared are the coins a report always lists. The market has more SHA-256
// coins, some with a share difficulty that does not compare (QUAI); those
// are listed only when a pool mines them.
var compared = map[string]bool{"BTC": true, "BCH": true, "BSV": true, "XEC": true, "DGB": true, "FB": true}

// coinViews lists the compared coins and those mined by a pool, the most
// profitable first. Pools of a coin view are the ones in byCoin.
func coinViews(m *Market, mined map[string]bool, byCoin map[string][]state.Pool) []CoinView {
	out := []CoinView{}
	for _, c := range m.Coins {
		if !compared[c.Tag] && !mined[c.Tag] {
			continue
		}
		cv := CoinView{
			Tag: c.Tag, Name: c.Name, PriceBTC: c.PriceBTC, Difficulty: c.Difficulty, DifficultyNow: c.DifficultyNow,
			BlockReward: c.BlockReward, RevenueBTC: c.RevenueBTC(), Stale: c.Stale, Efficiency: c.Efficiency, Pools: []string{},
		}
		if m.BTCUSD > 0 {
			cv.PriceUSD = c.PriceBTC * m.BTCUSD
			cv.RevenueUSD = cv.RevenueBTC * m.BTCUSD
		}
		for _, p := range byCoin[c.Tag] {
			cv.Pools = append(cv.Pools, p.ID)
		}
		out = append(out, cv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RevenueBTC > out[j].RevenueBTC })
	return out
}

// Network is the market data for the network panel: difficulty, reward and
// price of the listed coins. It shares the cache with the checks, so it asks
// the source at most once per cacheFor.
type Network struct {
	Fetched *time.Time `json:"fetched"`
	BTCUSD  float64    `json:"btc_usd"`
	Coins   []CoinView `json:"coins"` // pools are left empty
	Error   string     `json:"error,omitempty"`
}

func (s *Switcher) Network(ctx context.Context) Network {
	m, err := s.marketData(ctx)
	if err != nil {
		return Network{Coins: []CoinView{}, Error: err.Error()}
	}
	mined := map[string]bool{}
	for _, p := range s.d.Pools.Snapshot().Pools {
		mined[p.Coin] = true
	}
	fetched := m.Fetched
	return Network{Fetched: &fetched, BTCUSD: m.BTCUSD, Coins: coinViews(m, mined, nil)}
}

// decide fills the coins and the decision of rep from the market and the
// current pools, without switching.
func (s *Switcher) decide(rep *Report, m *Market) {
	snap := s.d.Pools.Snapshot()
	byCoin := map[string][]state.Pool{} // pools that take part and are not DOWN
	mined := map[string]bool{}
	for _, p := range snap.Pools {
		mined[p.Coin] = true
		if p.ProfitSwitch && s.d.Pools.Health(p.ID).Status != pool.StatusDown {
			byCoin[p.Coin] = append(byCoin[p.Coin], p)
		}
	}
	rep.Coins = coinViews(m, mined, byCoin)

	activeID := snap.Active
	if home := s.home(); home != "" {
		activeID = home // on the timed target for a while: compare for the pool the farm returns to
	}
	active, ok := snap.Get(activeID)
	if !ok {
		rep.Decision = NoCandidates
		return
	}
	rep.Active, rep.ActiveCoin = active.ID, active.Coin
	if !active.ProfitSwitch {
		rep.Decision = Manual
		return
	}
	cur, ok := m.Coins[active.Coin]
	if !ok || cur.Stale || cur.RevenueBTC() <= 0 {
		rep.Decision, rep.Error = NoData, "no market data for "+active.Coin
		return
	}
	best := cur
	for tag := range byCoin {
		if c, ok := m.Coins[tag]; ok && !c.Stale && c.RevenueBTC() > best.RevenueBTC() {
			best = c
		}
	}
	rep.Best = best.Tag
	rep.Advantage = (best.RevenueBTC()/cur.RevenueBTC() - 1) * 100
	switch {
	case best.Tag == cur.Tag:
		rep.Decision = Best
	case rep.Advantage < float64(rep.Margin):
		rep.Decision = BelowMargin
	default:
		rep.Decision, rep.Target = Recommend, pickPool(s.d.Pools, byCoin[best.Tag])
	}
}

// pickPool prefers a pool known to be UP; otherwise the first in the list.
func pickPool(m *pool.Manager, pools []state.Pool) string {
	for _, p := range pools {
		if m.Health(p.ID).Status == pool.StatusUp {
			return p.ID
		}
	}
	return pools[0].ID
}

func (s *Switcher) event(rep *Report) {
	ev := s.d.Events
	switch rep.Decision {
	case Switched:
		ev.Info("profit_switched", "profit switching: %s earns %.1f%% more than %s, switched to pool %s", rep.Best, rep.Advantage, rep.ActiveCoin, rep.Target)
	case Recommend:
		ev.Info("profit_recommend", "profit switching: %s earns %.1f%% more than %s; switch to pool %s (advise mode)", rep.Best, rep.Advantage, rep.ActiveCoin, rep.Target)
	case SwitchFailed:
		ev.Warn("profit_switch_failed", "profit switching: switching to pool %s failed: %s", rep.Target, rep.Error)
	case BelowMargin:
		ev.Info("profit_stay", "profit switching: staying on %s; %s earns %.1f%% more, below the %d%% margin", rep.ActiveCoin, rep.Best, rep.Advantage, rep.Margin)
	case Best:
		ev.Info("profit_stay", "profit switching: %s is the most profitable coin", rep.ActiveCoin)
	case NoData:
		ev.Warn("profit_no_data", "profit switching: no market data, nothing changed: %s", rep.Error)
	}
}

func writeFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(b, '\n'), 0o600)
}
