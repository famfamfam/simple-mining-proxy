// Package listener accepts miner connections on the TCP and TLS ports and
// enforces the connection limits: in total, before the first message and
// per IP address.
package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

const keepAlive = 30 * time.Second

// Limits is shared by both listeners: max_connections is a global limit.
type Limits struct {
	settings *settings.Store
	events   *events.Log

	total    atomic.Int64
	pending  atomic.Int64
	rejected atomic.Int64

	mu    sync.Mutex
	perIP map[string]int
}

func NewLimits(set *settings.Store, ev *events.Log) *Limits {
	return &Limits{settings: set, events: ev, perIP: map[string]int{}}
}

// acquire reserves a slot for a new connection or reports why not.
func (l *Limits) acquire(ip string) (release func(), firstDone func(), ok bool) {
	cfg := l.settings.Get()
	l.mu.Lock()
	if l.total.Load() >= int64(cfg.MaxConnections) || l.pending.Load() >= int64(cfg.MaxPendingConnections) {
		l.mu.Unlock()
		return nil, nil, false
	}
	if cfg.MaxConnPerIP > 0 && l.perIP[ip] >= cfg.MaxConnPerIP {
		l.mu.Unlock()
		return nil, nil, false
	}
	l.perIP[ip]++
	l.total.Add(1)
	l.pending.Add(1)
	l.mu.Unlock()

	var pendingOnce sync.Once
	firstDone = func() { pendingOnce.Do(func() { l.pending.Add(-1) }) }
	release = func() {
		firstDone()
		l.total.Add(-1)
		l.mu.Lock()
		if l.perIP[ip]--; l.perIP[ip] <= 0 {
			delete(l.perIP, ip)
		}
		l.mu.Unlock()
	}
	return release, firstDone, true
}

// Pending returns the number of connections still waiting for the first message.
func (l *Limits) Pending() int64 { return l.pending.Load() }

// ReportRejected writes an aggregated event once a minute instead of one
// event per rejected connection, so a flood cannot fill the log.
func (l *Limits) ReportRejected(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := l.rejected.Swap(0); n > 0 {
				l.events.Warn("connections_rejected", "%d miner connections rejected in the last minute by connection limits", n)
			}
		}
	}
}

type Listener struct {
	Name   string // "tcp" or "tls"
	Addr   string
	TLS    bool
	Server *session.Server
	Limits *Limits

	ln net.Listener
}

// Listen binds the port. It is separate from Serve so that a busy port is
// reported at startup.
func (l *Listener) Listen(ctx context.Context) error {
	lc := net.ListenConfig{KeepAlive: keepAlive}
	ln, err := lc.Listen(ctx, "tcp", l.Addr)
	if err != nil {
		return fmt.Errorf("%s listener on %s: %w", l.Name, l.Addr, err)
	}
	l.ln = ln
	return nil
}

// BoundAddr is the bound address (useful with port 0 in tests).
func (l *Listener) BoundAddr() net.Addr { return l.ln.Addr() }

// Serve accepts connections until ctx is cancelled; wg tracks sessions.
func (l *Listener) Serve(ctx context.Context, wg *sync.WaitGroup) {
	go func() {
		<-ctx.Done()
		l.ln.Close()
	}()
	slog.Info("listening", "listener", l.Name, "addr", l.ln.Addr().String())
	var backoff time.Duration
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			// Temporary errors (e.g. out of file descriptors): back off.
			if backoff == 0 {
				backoff = 5 * time.Millisecond
			} else if backoff < time.Second {
				backoff *= 2
			}
			slog.Warn("accept failed", "listener", l.Name, "err", err)
			time.Sleep(backoff)
			continue
		}
		backoff = 0
		ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		release, firstDone, ok := l.Limits.acquire(ip)
		if !ok {
			l.Limits.rejected.Add(1)
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer release()
			l.Server.Serve(ctx, conn, l.TLS, firstDone)
		}()
	}
}
