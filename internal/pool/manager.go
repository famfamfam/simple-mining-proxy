// Package pool manages pools, the active pool and fallback order, routes new
// sessions, probes pool health and performs switching, failover and
// failback.
package pool

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
)

// Snapshot is an immutable view of pools for lock-free reads.
type Snapshot struct {
	Active   string
	Fallback []string
	Pools    []state.Pool
}

func (s *Snapshot) Get(id string) (state.Pool, bool) {
	for _, p := range s.Pools {
		if p.ID == id {
			return p, true
		}
	}
	return state.Pool{}, false
}

// Role reports how a pool is used: "active", "fallback" with its 1-based
// position in the fallback order, or "none".
func (s *Snapshot) Role(id string) (role string, position int) {
	if id == s.Active {
		return "active", 0
	}
	for i, f := range s.Fallback {
		if f == id {
			return "fallback", i + 1
		}
	}
	return "none", 0
}

func (s *Snapshot) name(id string) string {
	if p, ok := s.Get(id); ok {
		return p.Name
	}
	return id
}

type Switch struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
}

type Manager struct {
	st       *state.Store
	settings *settings.Store
	reg      *session.Registry
	ev       *events.Log

	snap       atomic.Pointer[Snapshot]
	lastSwitch atomic.Pointer[Switch]
	opMu       sync.Mutex // serializes pool changes and drain decisions

	hmu   sync.Mutex
	addrs map[string]*addrHealth // by addrKey
	pools map[string]*poolHealth // by pool id

	lastFailback time.Time
	lastProbe    time.Time
	probing      atomic.Bool
}

func NewManager(st *state.Store, set *settings.Store, reg *session.Registry, ev *events.Log) *Manager {
	m := &Manager{st: st, settings: set, reg: reg, ev: ev, addrs: map[string]*addrHealth{}, pools: map[string]*poolHealth{}}
	m.setSnapshot(st.Current())
	return m
}

func (m *Manager) setSnapshot(f *state.File) {
	m.snap.Store(&Snapshot{Active: f.ActivePool, Fallback: f.FallbackPools, Pools: f.Pools})
}

func (m *Manager) Snapshot() *Snapshot { return m.snap.Load() }

func (m *Manager) LastSwitch() *Switch { return m.lastSwitch.Load() }

func (m *Manager) Health(id string) Health { return m.healthOf(id) }

// ---- routing ----

// candidates returns active + fallback, skipping pools that are DOWN.
func (m *Manager) candidates(s *Snapshot) []state.Pool {
	var out []state.Pool
	ids := append([]string{s.Active}, s.Fallback...)
	for _, id := range ids {
		if id == "" {
			continue
		}
		p, ok := s.Get(id)
		if !ok || m.healthOf(id).Status == StatusDown {
			continue
		}
		out = append(out, p)
	}
	return out
}

var ErrNoPool = errors.New("no pool available")

// Pick dials the first reachable candidate within upstream_connect_budget:
// pools in active/fallback order, and inside a pool its addresses from the
// fastest one. Another address of the same pool is tried before a fallback.
func (m *Manager) Pick(ctx context.Context) (*session.Upstream, error) {
	cfg := m.settings.Get()
	cands := m.candidates(m.snap.Load())
	if len(cands) == 0 {
		return nil, fmt.Errorf("%w: all pools are DOWN or none configured", ErrNoPool)
	}
	ctx, cancel := context.WithTimeout(ctx, cfg.UpstreamConnectBudget)
	defer cancel()
	var errs []string
	for _, p := range cands {
		for _, a := range m.orderedAddresses(p) {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%w: %s", ErrNoPool, strings.Join(append(errs, "upstream_connect_budget exhausted"), "; "))
			}
			conn, _, err := Dial(ctx, p, a, cfg.UpstreamDialTimeout)
			// Do not blame the pool for our own budget running out, and ignore
			// results for an address the operator has changed meanwhile.
			if m.stillHas(p, a) && (ctx.Err() == nil || err == nil) {
				m.dialResult(p, a, err)
			}
			if err == nil {
				return &session.Upstream{Conn: conn, PoolID: p.ID, PoolName: p.Name, Addr: a.String(), Template: p.Username, Password: p.Password}, nil
			}
			errs = append(errs, p.Name+" "+a.String()+": "+err.Error())
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoPool, strings.Join(errs, "; "))
}

// Mode is SWITCHING while a drain runs, NORMAL while the active pool is not
// DOWN, DEGRADED while a fallback pool takes over and DOWN otherwise.
func (m *Manager) Mode() string {
	if m.reg.Draining() {
		return "SWITCHING"
	}
	s := m.snap.Load()
	if s.Active == "" {
		return "DOWN"
	}
	if m.healthOf(s.Active).Status != StatusDown {
		return "NORMAL"
	}
	for _, id := range s.Fallback {
		if m.healthOf(id).Status != StatusDown {
			return "DEGRADED"
		}
	}
	return "DOWN"
}

// Effective returns the pool new sessions are sent to first.
func (m *Manager) Effective() string {
	if c := m.candidates(m.snap.Load()); len(c) > 0 {
		return c[0].ID
	}
	return ""
}

// ---- background: probes and failback ----

// Run probes pools every pool_probe_interval and checks failback. Both
// settings are read on every tick, so changes apply immediately.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		now := time.Now()
		if now.Sub(m.lastProbe) >= m.settings.Get().PoolProbeInterval && !m.probing.Load() {
			m.lastProbe = now
			m.probing.Store(true)
			go func() {
				defer m.probing.Store(false)
				m.probeAll(ctx)
			}()
		}
		m.checkFailback(now)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Manager) probeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, p := range m.snap.Load().Pools {
		for _, a := range p.Addresses {
			wg.Add(1)
			go func(p state.Pool, a state.Address) {
				defer wg.Done()
				rtt, err := Check(ctx, p, a, false, "")
				if ctx.Err() != nil {
					return
				}
				if m.stillHas(p, a) {
					m.probeResult(p, a, rtt, err)
				}
			}(p, a)
		}
	}
	wg.Wait()
}

// checkFailback drains sessions that are not on the active pool once the
// active pool has been UP for failback_delay without interruption.
func (m *Manager) checkFailback(now time.Time) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	cfg := m.settings.Get()
	s := m.snap.Load()
	if s.Active == "" {
		return
	}
	h := m.healthOf(s.Active)
	if h.Status != StatusUp || h.UpSince.IsZero() || now.Sub(h.UpSince) < cfg.FailbackDelay {
		return
	}
	if now.Sub(m.lastFailback) < cfg.FailbackDelay || m.reg.Draining() {
		return
	}
	active := s.Active
	notOnActive := func(x *session.Session) bool { return x.PoolID() != active }
	if m.reg.CountWhere(notOnActive) == 0 {
		return
	}
	m.lastFailback = now
	n := m.reg.Drain(notOnActive, cfg.SwitchDrain, "failback to "+s.name(active))
	m.ev.Info("failback", "failback: %s is UP for %s, returning %d sessions", s.name(active), settings.FormatDuration(cfg.FailbackDelay), n)
}

// ---- activation ----

// Activate makes id the active pool: validate → test → persist → switch →
// drain. On any error before persist nothing changes.
func (m *Manager) Activate(ctx context.Context, id string, force bool) (int, error) {
	return m.activate(ctx, id, force, "manual", nil)
}

// ActivateFor is Activate with the pool check, for a switch the proxy
// decides itself; reason goes to the switch record and the event.
func (m *Manager) ActivateFor(ctx context.Context, id, reason string) (int, error) {
	return m.activate(ctx, id, false, reason, nil)
}

// Restore makes id the active pool again after a temporary switch and puts
// back the fallback order from before it. There is no pool check: if id is
// down now, failover sends sessions to the fallback pools. Pools that no
// longer exist, repeats and id itself are dropped from fallback.
func (m *Manager) Restore(ctx context.Context, id string, fallback []string, reason string) (int, error) {
	if fallback == nil {
		fallback = []string{}
	}
	return m.activate(ctx, id, true, reason, fallback)
}

// activate switches to id. A nil fallback rotates the order: the previous
// active pool becomes the first fallback.
func (m *Manager) activate(ctx context.Context, id string, force bool, reason string, fallback []string) (int, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	p, ok := m.snap.Load().Get(id)
	if !ok {
		return 0, apierr.PoolNotFound(id)
	}
	if !force {
		if err := anyOK(CheckAll(ctx, p, true, m.settings.Get().TestWorker)); err != nil {
			return 0, apierr.PoolCheckFailed(apierr.M("pool_check_failed", "pool {pool} check failed: {error}", "pool", p.Name, "error", err.Error()))
		}
	}
	var from string
	err := m.st.Mutate(func(f *state.File) error {
		if _, ok := f.Pool(id); !ok {
			return apierr.PoolNotFound(id)
		}
		from = f.ActivePool
		f.ActivePool = id
		if fallback != nil {
			f.FallbackPools = existingOnce(f, fallback, id)
			return nil
		}
		order := without(f.FallbackPools, id)
		if from != "" && from != id {
			// Preserve failover after a manual switch: the previous active pool
			// becomes the first fallback, while the remaining order is kept.
			order = append([]string{from}, without(order, from)...)
		}
		f.FallbackPools = order
		return nil
	}, m.setSnapshot)
	if err != nil {
		return 0, persistErr(err)
	}
	s := m.snap.Load()
	// A successful manual check (or an explicit force switch) supersedes a
	// stale DOWN verdict. New dials and probes will establish fresh health.
	m.forgetHealth(id)
	m.lastSwitch.Store(&Switch{At: time.Now().UTC(), From: from, To: id, Reason: reason})
	m.ev.Info("pool_switched", "pool switched: %s → %s (%s)", s.name(from), p.Name, reason)
	n := m.reg.Drain(func(x *session.Session) bool { return x.PoolID() != id }, m.settings.Get().SwitchDrain, "switch to "+p.Name)
	return n, nil
}

// TestPool runs the manual Stratum check with authorization on every address
// of the pool. It fails only if no address works.
func (m *Manager) TestPool(ctx context.Context, id string) ([]AddrResult, error) {
	p, ok := m.snap.Load().Get(id)
	if !ok {
		return nil, apierr.PoolNotFound(id)
	}
	return checked(CheckAll(ctx, p, true, m.settings.Get().TestWorker))
}

// TestConfig checks a pool configuration from the editor before it is saved.
// in is applied on top of the saved pool baseID, so a password that was not
// retyped is kept, or on top of an empty pool.
func (m *Manager) TestConfig(ctx context.Context, baseID string, in Input) ([]AddrResult, error) {
	p := state.Pool{Password: "x"}
	if baseID != "" {
		cur, ok := m.snap.Load().Get(baseID)
		if !ok {
			return nil, apierr.PoolNotFound(baseID)
		}
		p = cur
	}
	apply(&p, in)
	if p.Name == "" {
		p.Name = "test" // the name does not matter for a check
	}
	if err := validate(&p); err != nil {
		return nil, err
	}
	return checked(CheckAll(ctx, p, true, m.settings.Get().TestWorker))
}

func checked(res []AddrResult) ([]AddrResult, error) {
	if err := anyOK(res); err != nil {
		return res, apierr.PoolCheckFailed(apierr.M("pool_unreachable", "no address passed the check: {error}", "error", err.Error()))
	}
	return res, nil
}

// anyOK is nil if at least one address passed, else all errors together.
func anyOK(res []AddrResult) error {
	var errs []string
	for _, r := range res {
		if r.Err == nil {
			return nil
		}
		errs = append(errs, r.Address.String()+": "+r.Err.Error())
	}
	if len(errs) == 1 {
		return errors.New(strings.SplitN(errs[0], ": ", 2)[1])
	}
	return errors.New(strings.Join(errs, "; "))
}

// ReconnectAll drains every session (the "Reconnect all" button).
func (m *Manager) ReconnectAll() int {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.reg.Drain(func(*session.Session) bool { return true }, m.settings.Get().SwitchDrain, "reconnect all")
}

// ---- CRUD ----

// Input is a pool create/update request. Nil fields are left unchanged.
// host/port are the short form of a single address (scripts, old clients);
// addresses wins when both are given.
type Input struct {
	ID            *string          `json:"id"`
	Name          *string          `json:"name"`
	Coin          *string          `json:"coin"`
	Addresses     *[]state.Address `json:"addresses"`
	Host          *string          `json:"host"`
	Port          *int             `json:"port"`
	TLS           *bool            `json:"tls"`
	TLSSkipVerify *bool            `json:"tls_skip_verify"`
	Username      *string          `json:"username"`
	Password      *string          `json:"password"`
	ProfitSwitch  *bool            `json:"profit_switch"`
	TimedTarget   *bool            `json:"timed_target"`
	Solo          *bool            `json:"solo"`
}

func apply(p *state.Pool, in Input) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = strings.TrimSpace(*src)
		}
	}
	set(&p.Name, in.Name)
	set(&p.Coin, in.Coin)
	set(&p.Username, in.Username)
	switch {
	case in.Addresses != nil:
		p.Addresses = make([]state.Address, 0, len(*in.Addresses))
		for _, a := range *in.Addresses {
			p.Addresses = append(p.Addresses, state.Address{Host: strings.TrimSpace(a.Host), Port: a.Port})
		}
	case in.Host != nil || in.Port != nil:
		a := state.Address{}
		if len(p.Addresses) > 0 {
			a = p.Addresses[0]
		}
		if in.Host != nil {
			a.Host = strings.TrimSpace(*in.Host)
		}
		if in.Port != nil {
			a.Port = *in.Port
		}
		p.Addresses = []state.Address{a}
	}
	if in.Password != nil {
		p.Password = *in.Password
	}
	if in.TLS != nil {
		p.TLS = *in.TLS
	}
	if in.TLSSkipVerify != nil {
		p.TLSSkipVerify = *in.TLSSkipVerify
	}
	if in.ProfitSwitch != nil {
		p.ProfitSwitch = *in.ProfitSwitch
	}
	if in.TimedTarget != nil {
		p.TimedTarget = *in.TimedTarget
	}
	if in.Solo != nil {
		p.Solo = *in.Solo
	}
	p.Coin = strings.ToUpper(p.Coin)
}

// onlyTimedTarget clears the timed target mark on every pool but p, which
// just got it: there is one pool to switch to on the timer.
func onlyTimedTarget(f *state.File, p state.Pool) {
	if !p.TimedTarget {
		return
	}
	for i := range f.Pools {
		if f.Pools[i].ID != p.ID {
			f.Pools[i].TimedTarget = false
		}
	}
}

func validate(p *state.Pool) error {
	if p.TLSSkipVerify && !p.TLS {
		p.TLSSkipVerify = false
	}
	if errs := state.PoolFieldErrors(*p); len(errs) > 0 {
		return invalidPool(errs)
	}
	return nil
}

func invalidPool(errs map[string]apierr.Msg) error {
	return apierr.Validation(apierr.M("pool_invalid", "invalid pool fields"), errs)
}

// checkName rejects a second pool with the same name: events, dialogs and
// the miners table identify pools by name.
func checkName(f *state.File, p state.Pool) error {
	for _, x := range f.Pools {
		if x.ID != p.ID && strings.EqualFold(strings.TrimSpace(x.Name), p.Name) {
			return invalidPool(map[string]apierr.Msg{"name": apierr.M("pool_name_taken", "a pool with this name already exists")})
		}
	}
	return nil
}

func makeID(name string, f *state.File) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if s := b.String(); s != "" && !strings.HasSuffix(s, "_") {
				b.WriteByte('_')
			}
		}
	}
	base := strings.Trim(b.String(), "_")
	if len(base) > 24 {
		base = strings.Trim(base[:24], "_")
	}
	if base == "" {
		base = "pool"
	}
	id := base
	for i := 2; ; i++ {
		if _, taken := f.Pool(id); !taken {
			return id
		}
		id = fmt.Sprintf("%s_%d", base, i)
	}
}

// Create adds a pool. The first pool becomes active automatically.
func (m *Manager) Create(in Input) (state.Pool, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	var created state.Pool
	var madeActive bool
	err := m.st.Mutate(func(f *state.File) error {
		p := state.Pool{Password: "x"}
		apply(&p, in)
		if err := validate(&p); err != nil {
			return err
		}
		if err := checkName(f, p); err != nil {
			return err
		}
		if in.ID != nil && *in.ID != "" {
			if !state.ValidPoolID(*in.ID) {
				return invalidPool(map[string]apierr.Msg{"id": apierr.M("pool_id",
					"lowercase Latin letters, digits and _, up to 32 characters")})
			}
			if _, taken := f.Pool(*in.ID); taken {
				return apierr.Conflict(apierr.M("pool_id_taken", "a pool with id {id} already exists", "id", *in.ID))
			}
			p.ID = *in.ID
		} else {
			p.ID = makeID(p.Name, f)
		}
		onlyTimedTarget(f, p)
		f.Pools = append(f.Pools, p)
		if f.ActivePool == "" {
			f.ActivePool = p.ID
			madeActive = true
		}
		created = p
		return nil
	}, m.setSnapshot)
	if err != nil {
		return state.Pool{}, persistErr(err)
	}
	m.ev.Info("pool_created", "pool %s created (%s, %s)", created.Name, addrList(created), transport(created))
	if madeActive {
		m.ev.Info("pool_switched", "pool %s is active (first pool)", created.Name)
	}
	return created, nil
}

// Update changes a pool. With reconnect, sessions of this pool are drained
// so the change applies to them right away.
func (m *Manager) Update(id string, in Input, reconnect bool) (state.Pool, int, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	var updated, old state.Pool
	var addrChanged bool
	err := m.st.Mutate(func(f *state.File) error {
		p, ok := f.Pool(id)
		if !ok {
			return apierr.PoolNotFound(id)
		}
		next := *p
		apply(&next, in)
		if err := validate(&next); err != nil {
			return err
		}
		if err := checkName(f, next); err != nil {
			return err
		}
		addrChanged = !sameEndpoint(next, *p)
		old = *p
		*p = next
		updated = next
		onlyTimedTarget(f, next)
		return nil
	}, m.setSnapshot)
	if err != nil {
		return state.Pool{}, 0, persistErr(err)
	}
	if addrChanged {
		// Removed addresses are forgotten and new ones start as UNKNOWN, so an
		// old DOWN verdict does not route sessions away from a fixed pool.
		m.poolEdited(old, updated)
	}
	m.ev.Info("pool_updated", "pool %s updated", updated.Name)
	n := 0
	if reconnect {
		n = m.reg.Drain(func(x *session.Session) bool { return x.PoolID() == id }, m.settings.Get().SwitchDrain, "pool "+updated.Name+" changed")
	}
	return updated, n, nil
}

// Delete removes a pool; the active pool cannot be deleted.
func (m *Manager) Delete(id string) (int, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	var name string
	err := m.st.Mutate(func(f *state.File) error {
		p, ok := f.Pool(id)
		if !ok {
			return apierr.PoolNotFound(id)
		}
		if f.ActivePool == id {
			return apierr.Conflict(apierr.M("pool_delete_active", "the active pool cannot be deleted: switch to another pool first"))
		}
		name = p.Name
		pools := f.Pools[:0]
		for _, x := range f.Pools {
			if x.ID != id {
				pools = append(pools, x)
			}
		}
		f.Pools = pools
		f.FallbackPools = without(f.FallbackPools, id)
		return nil
	}, m.setSnapshot)
	if err != nil {
		return 0, persistErr(err)
	}
	m.forgetHealth(id)
	m.ev.Info("pool_deleted", "pool %s deleted", name)
	n := m.reg.Drain(func(x *session.Session) bool { return x.PoolID() == id }, m.settings.Get().SwitchDrain, "pool "+name+" deleted")
	return n, nil
}

// SetFallback replaces the ordered fallback list.
func (m *Manager) SetFallback(ids []string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()

	var names []string
	err := m.st.Mutate(func(f *state.File) error {
		seen := map[string]bool{}
		names = names[:0]
		for _, id := range ids {
			p, ok := f.Pool(id)
			if !ok {
				return apierr.Validation(apierr.M("pool_not_found", "pool not found: {id}", "id", id), nil)
			}
			if id == f.ActivePool {
				return apierr.Validation(apierr.M("fallback_active", "the active pool cannot be a fallback"), nil)
			}
			if seen[id] {
				return apierr.Validation(apierr.M("fallback_duplicate", "pool {id} is listed twice", "id", id), nil)
			}
			seen[id] = true
			names = append(names, p.Name)
		}
		f.FallbackPools = append([]string{}, ids...)
		return nil
	}, m.setSnapshot)
	if err != nil {
		return persistErr(err)
	}
	if len(names) == 0 {
		m.ev.Info("fallback_changed", "fallback pools cleared")
	} else {
		m.ev.Info("fallback_changed", "fallback order: %s", strings.Join(names, " → "))
	}
	return nil
}

// existingOnce keeps the ids of existing pools other than skip, each once.
func existingOnce(f *state.File, ids []string, skip string) []string {
	out := []string{}
	for _, id := range ids {
		if _, ok := f.Pool(id); ok && id != skip && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func without(ids []string, id string) []string {
	out := []string{}
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

// sameEndpoint: same addresses and TLS settings, i.e. health still applies.
func sameEndpoint(a, b state.Pool) bool {
	return slices.Equal(a.Addresses, b.Addresses) && a.TLS == b.TLS && a.TLSSkipVerify == b.TLSSkipVerify
}

// stillHas reports whether a dial or probe result for address a of the
// captured pool p still applies to the current configuration.
func (m *Manager) stillHas(p state.Pool, a state.Address) bool {
	cur, ok := m.snap.Load().Get(p.ID)
	return ok && cur.TLS == p.TLS && cur.TLSSkipVerify == p.TLSSkipVerify && slices.Contains(cur.Addresses, a)
}

func addrList(p state.Pool) string {
	parts := make([]string, len(p.Addresses))
	for i, a := range p.Addresses {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

func transport(p state.Pool) string {
	if p.TLS {
		return "TLS"
	}
	return "TCP"
}

// persistErr keeps API errors as they are and wraps write failures as 500.
func persistErr(err error) error {
	var e *apierr.Error
	if errors.As(err, &e) {
		return e
	}
	return apierr.Internal(apierr.M("state_save_failed", "cannot save state.json: {error}", "error", err.Error()))
}
