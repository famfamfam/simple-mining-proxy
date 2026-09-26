// Package session relays one ASIC connection to one pool connection and
// keeps the registry of live sessions.
package session

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// Upstream is a connected pool with the pool settings captured at pick time:
// later edits of the pool apply to new sessions only.
type Upstream struct {
	Conn     net.Conn
	PoolID   string
	PoolName string
	Addr     string // host:port of the pool server
	Template string
	Password string
}

// Router picks and dials a pool for a new session.
type Router interface {
	Pick(ctx context.Context) (*Upstream, error)
}

const (
	writeTimeout    = 30 * time.Second
	pendingMaxAge   = 60 * time.Second
	sweepInterval   = 10 * time.Second
	maxPendingIDs   = 10000
	maxPendingAuth  = 128
	maxTrackedLogin = 128
	maxLoginLen     = 256  // real ASIC logins are far shorter; bounds per-session memory
	maxDifficulty   = 1e30 // far above SHA-256 network difficulty; keeps statistics finite
)

var (
	errPoolReconnect = errors.New("pool sent client.reconnect")
	errLoginTooLong  = fmt.Errorf("miner login longer than %d bytes", maxLoginLen)
)

type submitInfo struct {
	at     time.Time
	diff   float64
	worker string // ASIC login the share was sent with
}

type authInfo struct {
	at   time.Time
	user string
}

type Session struct {
	ID          uint64
	RemoteAddr  string
	IP          string
	Transport   string
	ConnectedAt time.Time

	miner net.Conn
	up    *Upstream
	srv   *Server

	closeOnce   sync.Once
	closeReason string

	mu            sync.Mutex
	userAgent     string
	worker        string
	upstreamUser  string
	difficulty    float64
	accepted      uint64
	rejected      uint64
	lastShare     time.Time // last accepted share
	lastSubmit    time.Time // last share the ASIC sent, whatever the pool answers
	rate          *stats.Rate
	logins        map[string]string   // ASIC login -> upstream user
	pendingAuth   map[string]authInfo // request id -> authorization metadata
	pendingSubmit map[string]submitInfo
	lastSweep     time.Time

	// maxLine is max_line_bytes captured at connect time, like the line
	// readers, so one session never mixes two limits.
	maxLine int

	deadlineMu        sync.Mutex
	lastMinerActivity time.Time
	lastPoolActivity  time.Time
}

func (s *Session) PoolID() string { return s.up.PoolID }

// Worker is the ASIC login of the session, "" before mining.authorize.
func (s *Session) Worker() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.worker
}

// Close ends the session: both connections are closed and both relay
// goroutines return. Safe to call many times from any goroutine.
func (s *Session) Close(reason string) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closeReason = reason
		s.mu.Unlock()
		s.miner.Close() // tls.Conn sends close_notify first
		s.up.Conn.Close()
	})
}

// Info is a row of the miners table.
type Info struct {
	ID           uint64     `json:"id"`
	Worker       string     `json:"worker"`
	UpstreamUser string     `json:"upstream_user"`
	IP           string     `json:"ip"`
	Transport    string     `json:"transport"`
	PoolID       string     `json:"pool_id"`
	PoolName     string     `json:"pool_name"`
	PoolAddr     string     `json:"pool_addr"`
	UserAgent    string     `json:"user_agent"`
	Difficulty   float64    `json:"difficulty"`
	Accepted     uint64     `json:"accepted"`
	Rejected     uint64     `json:"rejected"`
	LastShare    *time.Time `json:"last_share"`
	LastSubmit   *time.Time `json:"last_submit"`
	HashrateHs   float64    `json:"hashrate_hs"`
	ConnectedAt  time.Time  `json:"connected_at"`
}

func (s *Session) Info(now time.Time) Info {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := Info{
		ID: s.ID, Worker: s.worker, UpstreamUser: s.upstreamUser, IP: s.IP, Transport: s.Transport,
		PoolID: s.up.PoolID, PoolName: s.up.PoolName, PoolAddr: s.up.Addr, UserAgent: s.userAgent, Difficulty: s.difficulty,
		Accepted: s.accepted, Rejected: s.rejected, HashrateHs: s.rate.Hashrate(now), ConnectedAt: s.ConnectedAt,
	}
	if !s.lastShare.IsZero() {
		t := s.lastShare
		in.LastShare = &t
	}
	if !s.lastSubmit.IsZero() {
		t := s.lastSubmit
		in.LastSubmit = &t
	}
	return in
}

// Server turns accepted connections into sessions.
type Server struct {
	Settings *settings.Store
	Router   Router
	Registry *Registry
	Events   *events.Log
	Stats    *stats.Collector
	// TLSConfig returns the config for a new TLS connection; nil when the
	// TLS listener is off.
	TLSConfig func() *tls.Config

	mu     sync.Mutex
	nextID uint64
}

// Serve handles one accepted connection until the session ends. firstDone is
// called once the connection stops being "pending": after the first valid
// message or when it is closed before that.
func (srv *Server) Serve(ctx context.Context, conn net.Conn, isTLS bool, firstDone func()) {
	// Registered first so it runs last: the other defers still close the
	// connection and unregister the session.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in session", "remote", conn.RemoteAddr().String(), "panic", r, "stack", string(debug.Stack()))
		}
	}()
	// A closure, not `defer conn.Close()`: conn is replaced by the tls.Conn
	// below, and closing the wrapper is what sends close_notify.
	defer func() { conn.Close() }()
	// On shutdown, abort any phase that is not ctx-aware (waiting for the
	// first message, relaying). Closing the raw socket is enough to unblock.
	raw := conn
	stopAbort := context.AfterFunc(ctx, func() { raw.Close() })
	defer stopAbort()
	var once sync.Once
	done := func() { once.Do(firstDone) }
	defer done()

	cfg := srv.Settings.Get()
	remote := conn.RemoteAddr().String()
	ip, _, _ := net.SplitHostPort(remote)
	log := slog.With("remote", remote)

	transport := "tcp"
	if isTLS {
		transport = "tls"
		tc := tls.Server(conn, srv.TLSConfig())
		conn.SetDeadline(time.Now().Add(cfg.TLSHandshakeTimeout))
		if err := tc.HandshakeContext(ctx); err != nil {
			srv.Stats.TLSHandshakeFailed()
			log.Debug("tls handshake failed", "err", err)
			// One event per IP every 10 minutes: enough to spot an ASIC whose
			// firmware rejects the certificate or TLS version, without flooding.
			srv.Events.Throttled("tls_handshake:"+ip, 10*time.Minute, "tls_handshake_failed",
				"TLS handshake with %s failed: %v", ip, err)
			return
		}
		conn = tc
	}

	conn.SetDeadline(time.Now().Add(cfg.FirstMessageTimeout))
	lr := stratum.NewLineReader(conn, cfg.MaxLineBytes())
	line, err := lr.ReadLine()
	if err != nil {
		log.Debug("no first message", "err", err)
		return
	}
	msg, err := stratum.Parse(line)
	if err != nil || msg.Method == "" {
		log.Debug("first message is not a stratum request", "err", err)
		return
	}
	// The line slice is reused by the reader; keep a copy until it is sent.
	first := append([]byte(nil), line...)
	done()
	conn.SetDeadline(time.Time{})

	up, err := srv.Router.Pick(ctx)
	if err != nil {
		srv.Events.Throttled("no_pool", 30*time.Second, "no_pool", "no pool available, closing miner connections: %v", err)
		return
	}

	srv.mu.Lock()
	srv.nextID++
	id := srv.nextID
	srv.mu.Unlock()
	now := time.Now()
	s := &Session{
		ID: id, RemoteAddr: remote, IP: ip, Transport: transport, ConnectedAt: now,
		miner: conn, up: up, srv: srv,
		rate:              stats.NewRate(now),
		logins:            map[string]string{},
		pendingAuth:       map[string]authInfo{},
		pendingSubmit:     map[string]submitInfo{},
		maxLine:           cfg.MaxLineBytes(),
		lastMinerActivity: now,
		lastPoolActivity:  now,
	}
	srv.Registry.add(s)
	defer srv.Registry.remove(s)
	if ctx.Err() != nil { // shutdown began while the pool was being dialed
		s.Close("shutdown")
		return
	}
	log = log.With("session", id, "pool", up.PoolID)
	log.Debug("session started", "transport", transport)

	err = s.relay(first, msg, lr)
	s.Close(closeReason(err))
	s.mu.Lock()
	reason := s.closeReason
	s.mu.Unlock()
	log.Debug("session closed", "reason", reason, "worker", s.worker)
}

func closeReason(err error) string {
	switch {
	case err == nil, errors.Is(err, io.EOF):
		return "connection closed"
	case errors.Is(err, net.ErrClosed):
		return "closed by proxy"
	}
	return err.Error()
}

func (s *Session) relay(first []byte, firstMsg *stratum.Message, minerReader *stratum.LineReader) error {
	out, err := s.forwardFromMiner(first, firstMsg)
	if err != nil {
		return err
	}
	if err := writeLine(s.up.Conn, out); err != nil {
		return fmt.Errorf("write to pool: %w", err)
	}
	errc := make(chan error, 2)
	go func() { errc <- guard(func() error { return s.minerLoop(minerReader) }) }()
	go func() { errc <- guard(func() error { return s.poolLoop(stratum.NewLineReader(s.up.Conn, s.maxLine)) }) }()
	err = <-errc
	s.Close(closeReason(err))
	<-errc
	return err
}

// guard turns a panic in a relay goroutine into an error, so a bug reachable
// from one connection's bytes ends that session, not the whole proxy.
func guard(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in session relay", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	return fn()
}

func writeLine(c net.Conn, b []byte) error {
	c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := c.Write(b)
	return err
}

func (s *Session) prepareMinerRead() {
	s.deadlineMu.Lock()
	s.miner.SetReadDeadline(s.lastMinerActivity.Add(s.srv.Settings.Get().MinerIdleTimeout))
	s.deadlineMu.Unlock()
}

func (s *Session) preparePoolRead() {
	s.deadlineMu.Lock()
	s.up.Conn.SetReadDeadline(s.lastPoolActivity.Add(s.srv.Settings.Get().UpstreamIdleTimeout))
	s.deadlineMu.Unlock()
}

func (s *Session) markMinerActivity(now time.Time) {
	s.deadlineMu.Lock()
	s.lastMinerActivity = now
	s.deadlineMu.Unlock()
}

func (s *Session) markPoolActivity(now time.Time) {
	s.deadlineMu.Lock()
	s.lastPoolActivity = now
	s.deadlineMu.Unlock()
}

// refreshIdleDeadlines makes runtime timeout changes affect reads that are
// already blocked, using the time of the last received message as the base.
func (s *Session) refreshIdleDeadlines(v *settings.Values) {
	s.deadlineMu.Lock()
	s.miner.SetReadDeadline(s.lastMinerActivity.Add(v.MinerIdleTimeout))
	s.up.Conn.SetReadDeadline(s.lastPoolActivity.Add(v.UpstreamIdleTimeout))
	s.deadlineMu.Unlock()
}

// minerLoop relays ASIC → pool.
func (s *Session) minerLoop(lr *stratum.LineReader) error {
	for {
		s.prepareMinerRead()
		line, err := lr.ReadLine()
		if err != nil {
			return fmt.Errorf("miner: %w", err)
		}
		s.markMinerActivity(time.Now())
		msg, err := stratum.Parse(line)
		if err != nil {
			return fmt.Errorf("miner sent invalid JSON: %w", err)
		}
		out, err := s.forwardFromMiner(line, msg)
		if err != nil {
			return err
		}
		if err := writeLine(s.up.Conn, out); err != nil {
			return fmt.Errorf("write to pool: %w", err)
		}
	}
}

// forwardFromMiner rewrites one ASIC message and enforces max_line_bytes on
// the result too: rewriting and JSON escaping can make a line longer.
func (s *Session) forwardFromMiner(line []byte, msg *stratum.Message) ([]byte, error) {
	out, err := s.fromMiner(line, msg)
	if err != nil {
		return nil, err
	}
	if len(out) > s.maxLine {
		return nil, fmt.Errorf("rewritten miner message is %d bytes, over max_line_bytes (%d)", len(out), s.maxLine)
	}
	return out, nil
}

// poolLoop relays pool → ASIC.
func (s *Session) poolLoop(lr *stratum.LineReader) error {
	for {
		s.preparePoolRead()
		line, err := lr.ReadLine()
		if err != nil {
			return fmt.Errorf("pool: %w", err)
		}
		s.markPoolActivity(time.Now())
		out, err := s.fromPool(line)
		if err != nil {
			return err
		}
		if err := writeLine(s.miner, out); err != nil {
			return fmt.Errorf("write to miner: %w", err)
		}
	}
}

// fromMiner returns the bytes to send to the pool for one ASIC message.
func (s *Session) fromMiner(line []byte, msg *stratum.Message) ([]byte, error) {
	switch msg.Method {
	case "mining.subscribe":
		if params, err := stratum.ParamsArray(msg.Params); err == nil {
			if ua, ok := stratum.StringAt(params, 0); ok {
				s.mu.Lock()
				s.userAgent = ua
				s.mu.Unlock()
			}
		}
	case "mining.authorize":
		return s.authorize(line, msg)
	case "mining.submit":
		return s.submit(line, msg)
	}
	return line, nil
}

func (s *Session) rewrite(login, asicPassword string) stratum.Rewrite {
	return stratum.RewriteLogin(login, asicPassword, s.up.Template, s.up.Password)
}

func (s *Session) authorize(line []byte, msg *stratum.Message) ([]byte, error) {
	params, err := stratum.ParamsArray(msg.Params)
	if err != nil {
		return line, nil
	}
	login, ok := stratum.StringAt(params, 0)
	if !ok {
		return line, nil
	}
	if len(login) > maxLoginLen {
		return nil, errLoginTooLong
	}
	asicPass, _ := stratum.StringAt(params, 1)
	rw := s.rewrite(login, asicPass)

	now := time.Now()
	s.mu.Lock()
	s.sweepLocked(now)
	if _, exists := s.logins[login]; exists || len(s.logins) < maxTrackedLogin {
		s.logins[login] = rw.User
	}
	if key := msg.IDKey(); key != "" {
		if _, exists := s.pendingAuth[key]; exists || len(s.pendingAuth) < maxPendingAuth {
			s.pendingAuth[key] = authInfo{at: now, user: rw.User}
		}
	}
	s.worker, s.upstreamUser = login, rw.User
	s.mu.Unlock()

	params[0] = stratum.EncodeString(rw.User)
	if len(params) > 1 {
		params[1] = stratum.EncodeString(rw.Password)
	} else {
		params = append(params, stratum.EncodeString(rw.Password))
	}
	return stratum.ReplaceParams(line, params)
}

// submit rewrites only params[0]; the remaining elements, including the
// sixth one (AsicBoost version bits), are kept as raw bytes.
func (s *Session) submit(line []byte, msg *stratum.Message) ([]byte, error) {
	params, err := stratum.ParamsArray(msg.Params)
	if err != nil {
		return line, nil
	}
	login, ok := stratum.StringAt(params, 0)
	if ok && len(login) > maxLoginLen {
		return nil, errLoginTooLong
	}
	now := time.Now()

	s.mu.Lock()
	s.lastSubmit = now
	user := login
	if ok {
		var known bool
		if user, known = s.logins[login]; !known {
			user = s.rewrite(login, "").User
			if len(s.logins) < maxTrackedLogin {
				s.logins[login] = user
			}
		}
	}
	if key := msg.IDKey(); key != "" {
		s.sweepLocked(now)
		if len(s.pendingSubmit) < maxPendingIDs {
			worker := s.worker
			if ok {
				worker = login
			}
			s.pendingSubmit[key] = submitInfo{at: now, diff: s.difficulty, worker: worker}
		}
	}
	s.mu.Unlock()

	if !ok || user == login {
		return line, nil
	}
	params[0] = stratum.EncodeString(user)
	return stratum.ReplaceParams(line, params)
}

// sweepLocked drops submits without an answer for more than a minute.
func (s *Session) sweepLocked(now time.Time) {
	if now.Sub(s.lastSweep) < sweepInterval {
		return
	}
	s.lastSweep = now
	for k, v := range s.pendingSubmit {
		if now.Sub(v.at) > pendingMaxAge {
			delete(s.pendingSubmit, k)
		}
	}
	for k, v := range s.pendingAuth {
		if now.Sub(v.at) > pendingMaxAge {
			delete(s.pendingAuth, k)
		}
	}
}

// fromPool returns the bytes to send to the ASIC for one pool message.
func (s *Session) fromPool(line []byte) ([]byte, error) {
	msg, err := stratum.Parse(line)
	if err != nil {
		s.srv.Stats.UpstreamInvalidJSON()
		return line, nil
	}
	if msg.Method != "" {
		switch msg.Method {
		case "mining.set_difficulty":
			if params, err := stratum.ParamsArray(msg.Params); err == nil {
				if d, ok := stratum.NumberAt(params, 0); ok && d > 0 && d <= maxDifficulty {
					s.mu.Lock()
					s.difficulty = d
					s.mu.Unlock()
				}
			}
		case "client.reconnect":
			s.srv.Events.Throttled("reconnect:"+s.up.PoolID, time.Minute, "pool_reconnect",
				"%s sent client.reconnect %s; not forwarded, closing the session so the ASIC comes back to the proxy",
				s.up.PoolName, compact(msg.Params))
			return nil, errPoolReconnect
		}
		return line, nil
	}

	key := msg.IDKey()
	if key == "" {
		return line, nil
	}
	now := time.Now()
	s.mu.Lock()
	if sub, ok := s.pendingSubmit[key]; ok {
		delete(s.pendingSubmit, key)
		accepted := stratum.ResultTrue(msg.Result) && stratum.IsNull(msg.Error)
		reason := ""
		if accepted {
			s.accepted++
			s.lastShare = now
			s.rate.Add(sub.diff, now)
		} else {
			s.rejected++
			reason = stratum.ErrorReason(msg.Error)
		}
		s.mu.Unlock()
		s.srv.Stats.Share(s.up.PoolID, sub.worker, accepted, sub.diff, reason, now)
		return line, nil
	}
	auth, isAuth := s.pendingAuth[key]
	if isAuth {
		delete(s.pendingAuth, key)
	}
	s.mu.Unlock()
	if isAuth && !stratum.ResultTrue(msg.Result) {
		reason := stratum.ErrorReason(msg.Error)
		if reason == "" {
			reason = "result " + compact(msg.Result)
		}
		s.srv.Events.Throttled("auth:"+s.up.PoolID+":"+auth.user, time.Minute, "authorization_failed",
			"authorization failed on %s for %q: %s", s.up.PoolName, auth.user, reason)
	}
	return line, nil
}

func compact(raw json.RawMessage) string {
	if len(raw) > 200 {
		return string(raw[:200]) + "…"
	}
	return string(raw)
}
