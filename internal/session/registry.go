package session

import (
	"context"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// Registry tracks live sessions and closes them gradually on drain.
type Registry struct {
	events *events.Log

	mu       sync.RWMutex
	sessions map[uint64]*Session

	drainMu     sync.Mutex
	drainCancel context.CancelFunc
	drainGen    uint64
	draining    bool
}

func NewRegistry(ev *events.Log) *Registry {
	return &Registry{events: ev, sessions: make(map[uint64]*Session)}
}

func (r *Registry) add(s *Session) {
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
}

func (r *Registry) remove(s *Session) {
	r.mu.Lock()
	delete(r.sessions, s.ID)
	r.mu.Unlock()
}

// List returns live sessions ordered by id.
func (r *Registry) List() []*Session {
	r.mu.RLock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Infos returns a row per live session, ordered by id.
func (r *Registry) Infos(now time.Time) []Info {
	list := r.List()
	out := make([]Info, 0, len(list))
	for _, s := range list {
		out = append(out, s.Info(now))
	}
	return out
}

// Workers counts live sessions per ASIC login; sessions not authorized yet
// are left out.
func (r *Registry) Workers() map[string]int {
	out := map[string]int{}
	for _, s := range r.List() {
		if w := s.Worker(); w != "" {
			out[w]++
		}
	}
	return out
}

// RefreshIdleDeadlines applies changed idle timeouts to reads already blocked
// in live sessions.
func (r *Registry) RefreshIdleDeadlines(v *settings.Values) {
	for _, s := range r.List() {
		s.refreshIdleDeadlines(v)
	}
}

type Counts struct {
	Total  int            `json:"total"`
	TCP    int            `json:"tcp"`
	TLS    int            `json:"tls"`
	ByPool map[string]int `json:"by_pool"`
}

func (r *Registry) Counts() Counts {
	c := Counts{ByPool: map[string]int{}}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.sessions {
		c.Total++
		if s.Transport == "tls" {
			c.TLS++
		} else {
			c.TCP++
		}
		c.ByPool[s.up.PoolID]++
	}
	return c
}

// CountWhere counts sessions matching filter.
func (r *Registry) CountWhere(filter func(*Session) bool) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.sessions {
		if filter(s) {
			n++
		}
	}
	return n
}

// Draining reports whether a drain is in progress.
func (r *Registry) Draining() bool {
	r.drainMu.Lock()
	defer r.drainMu.Unlock()
	return r.draining
}

// Drain closes the sessions matching filter evenly over window, in random
// order. A new drain cancels the one in progress. It returns the number of
// sessions scheduled for closing.
func (r *Registry) Drain(filter func(*Session) bool, window time.Duration, reason string) int {
	r.mu.RLock()
	var targets []*Session
	for _, s := range r.sessions {
		if filter(s) {
			targets = append(targets, s)
		}
	}
	r.mu.RUnlock()
	rand.Shuffle(len(targets), func(i, j int) { targets[i], targets[j] = targets[j], targets[i] })

	r.drainMu.Lock()
	if r.drainCancel != nil {
		r.drainCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.drainCancel = cancel
	r.drainGen++
	gen := r.drainGen
	r.draining = len(targets) > 0
	r.drainMu.Unlock()

	if len(targets) == 0 {
		cancel()
		return 0
	}
	r.events.Info("drain_started", "drain started: %d sessions over %s (%s)", len(targets), window, reason)
	go func() {
		defer cancel()
		closed := 0
		var step time.Duration
		if len(targets) > 1 {
			step = window / time.Duration(len(targets)-1)
		}
		for i, s := range targets {
			if i > 0 && step > 0 {
				t := time.NewTimer(step)
				select {
				case <-ctx.Done():
					t.Stop()
				case <-t.C:
				}
			}
			// Check and close under drainMu: a replacement drain (e.g. a
			// reverse switch) cannot slip in between, so this drain never
			// closes a session the new one wants to keep.
			r.drainMu.Lock()
			stale := ctx.Err() != nil || r.drainGen != gen
			if !stale {
				s.Close("drain: " + reason)
			}
			r.drainMu.Unlock()
			if stale {
				break
			}
			closed++
		}
		r.drainMu.Lock()
		current := r.drainGen == gen
		if current {
			r.draining = false
		}
		r.drainMu.Unlock()
		if current {
			r.events.Info("drain_finished", "drain finished: %d sessions (%s)", closed, reason)
		} else {
			r.events.Info("drain_cancelled", "drain cancelled after %d of %d sessions (%s): a new drain started", closed, len(targets), reason)
		}
	}()
	return len(targets)
}

// CloseAll closes every session at once (shutdown).
func (r *Registry) CloseAll(reason string) {
	r.drainMu.Lock()
	if r.drainCancel != nil {
		r.drainCancel()
	}
	r.drainMu.Unlock()
	for _, s := range r.List() {
		s.Close(reason)
	}
}
