// Package admin serves the REST API and the embedded web UI.
package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/config"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/history"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/profit"
	"github.com/famfamfam/simple-mining-proxy/internal/rtt"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/timed"
	"github.com/famfamfam/simple-mining-proxy/internal/tlsutil"
	"github.com/famfamfam/simple-mining-proxy/web"
)

type Deps struct {
	Config   *config.Config
	State    *state.Store
	Settings *settings.Store
	Pools    *pool.Manager
	Registry *session.Registry
	Stats    *stats.Collector
	History  *history.Recorder
	Profit   *profit.Switcher
	Timed    *timed.Switcher
	RTT      *rtt.Watcher // eCash real-time target, for the network panel
	Events   *events.Log
	Certs    *tlsutil.Certs // nil when the TLS listener is off
	Started  time.Time
}

type Server struct {
	d    Deps
	auth *auth
}

const maxBody = 1 << 20

func New(d Deps) http.Handler {
	guardPath := ""
	if d.Config.DataDir != "" {
		guardPath = filepath.Join(d.Config.DataDir, "auth-guard.json")
	}
	s := &Server{d: d, auth: newAuth(d.Config.AdminUsername, d.Config.AdminPassword, d.Config.APIToken, newGuard(guardPath))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)

	api := func(pattern string, h func(http.ResponseWriter, *http.Request) error) {
		mux.Handle(pattern, s.protected(h))
	}
	api("GET /api/auth/me", s.me)
	api("GET /api/status", s.status)
	api("GET /api/miners", s.miners)
	api("POST /api/miners/reconnect", s.reconnectAll)
	api("GET /api/events", s.events)
	api("GET /api/history", s.history)
	api("GET /api/history/worker", s.historyWorker)
	api("GET /api/history/workers", s.historyWorkers)
	api("GET /api/profit", s.profitStatus)
	api("POST /api/profit/check", s.profitCheck)
	api("GET /api/timed", s.timedStatus)
	api("GET /api/network", s.network)
	api("GET /api/pools", s.listPools)
	api("POST /api/pools", s.createPool)
	api("PUT /api/pools/{id}", s.updatePool)
	api("DELETE /api/pools/{id}", s.deletePool)
	api("POST /api/pools/test", s.testConfig)
	api("POST /api/pools/{id}/test", s.testPool)
	api("POST /api/pools/{id}/activate", s.activatePool)
	api("PUT /api/fallback", s.setFallback)
	api("GET /api/settings", s.getSettings)
	api("PUT /api/settings", s.putSettings)
	unknownAPI := func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, apierr.NotFound(apierr.M("api_not_found", "unknown API endpoint")))
	}
	for _, method := range []string{"GET", "POST", "PUT", "DELETE"} {
		mux.HandleFunc(method+" /api/", unknownAPI)
	}

	mux.Handle("GET /", web.Handler())
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) protected(h func(http.ResponseWriter, *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, res := s.auth.authenticate(r, time.Now())
		if s.guardFailed(w, clientIP(r), res) {
			return
		}
		if !ok {
			writeErr(w, errUnauthorized)
			return
		}
		if !jsonOnly(r) {
			writeErr(w, errJSONRequired)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		if err := h(w, r); err != nil {
			e := apierr.From(err)
			if e.Status >= 500 {
				slog.Error("admin API error", "path", r.URL.Path, "err", err)
			}
			writeErr(w, e)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeErr(w http.ResponseWriter, e *apierr.Error) {
	writeJSON(w, e.Status, e)
}

var (
	errUnauthorized = &apierr.Error{Status: http.StatusUnauthorized, Code: "unauthorized",
		Msg: apierr.M("login_required", "login required")}
	errJSONRequired = &apierr.Error{Status: http.StatusUnsupportedMediaType, Code: "json_required",
		Msg: apierr.M("json_required", "Content-Type: application/json is required")}
	errBadCredentials = &apierr.Error{Status: http.StatusUnauthorized, Code: "invalid_credentials",
		Msg: apierr.M("invalid_credentials", "wrong username or password")}
)

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return apierr.Validation(apierr.M("body_empty", "the request body must not be empty"), nil)
		}
		return badJSON(err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return apierr.Validation(apierr.M("body_not_object", "the request body must be a JSON object"), nil)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return apierr.Validation(apierr.M("body_extra", "the request body must hold a single JSON value"), nil)
		}
		return badJSON(err)
	}
	if err := json.Unmarshal(trimmed, v); err != nil {
		return badJSON(err)
	}
	return nil
}

func badJSON(err error) error {
	return apierr.Validation(apierr.M("body_invalid_json", "invalid JSON: {error}", "error", err.Error()), nil)
}

// ---- auth ----

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !jsonOnly(r) {
		writeErr(w, errJSONRequired)
		return
	}
	ip, now := clientIP(r), time.Now()
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := decode(r, &body); err != nil {
		writeErr(w, apierr.From(err))
		return
	}
	id, res := s.auth.attemptLogin(ip, body.Username, body.Password, now)
	if s.guardFailed(w, ip, res) {
		return
	}
	if !res.OK {
		s.d.Events.Throttled("login_failed:"+ip, time.Minute, "login_failed", "failed admin login from %s", ip)
		writeErr(w, errBadCredentials)
		return
	}
	s.auth.setCookie(w, r, id, int(sessionLifetime/time.Second))
	writeJSON(w, http.StatusOK, map[string]string{"username": s.d.Config.AdminUsername})
}

// guardFailed reports new locks as events and answers 429 when the caller is
// locked out. It returns true when the response has been written.
func (s *Server) guardFailed(w http.ResponseWriter, ip string, res attemptResult) bool {
	if res.IPLock > 0 {
		s.d.Events.Warn("login_locked", "admin login from %s locked for %s after %d failed attempts",
			ip, settings.FormatDuration(res.IPLock), ipFailLimit)
	}
	if res.GlobalLock {
		s.d.Events.Warn("login_locked_global", "admin login locked for everyone: %d failed attempts within %s (possible distributed brute force)",
			globalFailLimit, settings.FormatDuration(globalFailWindow))
	}
	if !res.Blocked {
		return false
	}
	wait := time.Until(res.RetryAt).Round(time.Second)
	if wait < time.Second {
		wait = time.Second
	}
	seconds := int(wait / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	msg := apierr.M("login_locked", "too many failed attempts from your address, retry in {seconds} s", "seconds", seconds)
	if res.Global {
		msg = apierr.M("login_locked_global", "login is locked for everyone after a password-guessing flood, retry in {seconds} s",
			"seconds", seconds)
	}
	writeErr(w, &apierr.Error{Status: http.StatusTooManyRequests, Code: "too_many_attempts", Msg: msg})
	return true
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !jsonOnly(r) {
		writeErr(w, errJSONRequired)
		return
	}
	s.auth.logout(r)
	s.auth.setCookie(w, r, "", -1)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]string{"username": s.d.Config.AdminUsername})
	return nil
}

// ---- status, miners, events ----

func (s *Server) status(w http.ResponseWriter, r *http.Request) error {
	now := time.Now()
	snap := s.d.Pools.Snapshot()
	counts := s.d.Registry.Counts()
	st := s.d.Stats.Snapshot(now)
	resp := map[string]any{
		"uptime_seconds": int64(now.Sub(s.d.Started).Seconds()),
		"mode":           s.d.Pools.Mode(),
		"active_pool":    snap.Active,
		"effective_pool": s.d.Pools.Effective(),
		"fallback_pools": snap.Fallback,
		"last_switch":    s.d.Pools.LastSwitch(),
		"miners":         map[string]int{"total": counts.Total, "tcp": counts.TCP, "tls": counts.TLS},
		"upstream":       map[string]any{"connected": counts.Total, "by_pool": counts.ByPool},
		"shares":         map[string]any{"accepted": st.Accepted, "rejected": st.Rejected, "reject_reasons": st.RejectReasons},
		"hashrate_ths":   st.HashrateHs / 1e12,
		"connections":    s.connectionStrings(),
		"errors": map[string]uint64{
			"tls_handshake":         st.TLSHandshakeErrors,
			"upstream_invalid_json": st.UpstreamInvalidJSON,
		},
	}
	writeJSON(w, http.StatusOK, resp)
	return nil
}

func (s *Server) miners(w http.ResponseWriter, r *http.Request) error {
	now := time.Now()
	list := s.d.Registry.List()
	out := make([]session.Info, 0, len(list))
	for _, x := range list {
		out = append(out, x.Info(now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"miners": out})
	return nil
}

func (s *Server) reconnectAll(w http.ResponseWriter, r *http.Request) error {
	n := s.d.Pools.ReconnectAll()
	writeJSON(w, http.StatusOK, map[string]any{"sessions": n, "window": settings.FormatDuration(s.d.Settings.Get().SwitchDrain)})
	return nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": s.d.Events.List(limit)})
	return nil
}

// historyRange reads from and to (unix seconds, default: the last 24 hours)
// and points (the most steps wanted) of a history request.
func historyRange(r *http.Request) (from, to time.Time, points int, err error) {
	q := r.URL.Query()
	num := func(name string, def int64) (int64, error) {
		v := q.Get(name)
		if v == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, apierr.Validation(apierr.M("history_param", "{name} must be an integer", "name", name), nil)
		}
		return n, nil
	}
	t, err := num("to", time.Now().Unix())
	if err != nil {
		return
	}
	f, err := num("from", t-86400)
	if err != nil {
		return
	}
	p, err := num("points", 800)
	if err != nil {
		return
	}
	if f >= t || t-f > 20*365*86400 {
		err = apierr.Validation(apierr.M("history_range", "from must be before to, at most 20 years apart"), nil)
		return
	}
	return time.Unix(f, 0), time.Unix(t, 0), int(min(max(p, 10), 3000)), nil
}

// history returns the totals for the charts.
func (s *Server) history(w http.ResponseWriter, r *http.Request) error {
	from, to, points, err := historyRange(r)
	if err != nil {
		return err
	}
	series, err := s.d.History.Query(from, to, points)
	if err != nil {
		return err
	}
	names := map[string]string{}
	for _, p := range s.d.Pools.Snapshot().Pools {
		names[p.ID] = p.Name
	}
	v := s.d.Settings.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"from":             from.Unix(),
		"to":               to.Unix(),
		"series":           series,
		"pool_names":       names,
		"detail_retention": settings.FormatDuration(v.HistoryDetail),
		"retention":        settings.FormatDuration(v.HistoryRetention),
		"miner_retention":  settings.FormatDuration(v.HistoryMiners),
		"disk_bytes":       s.d.History.DiskUsage(),
	})
	return nil
}

// historyWorker returns the history of one ASIC login (?name=).
func (s *Server) historyWorker(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Query().Get("name")
	if name == "" {
		return apierr.Validation(apierr.M("history_worker", "name is required"), nil)
	}
	from, to, points, err := historyRange(r)
	if err != nil {
		return err
	}
	series, err := s.d.History.Worker(name, from, to, points)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "from": from.Unix(), "to": to.Unix(), "series": series})
	return nil
}

// historyWorkers summarizes every ASIC login seen in the range, including
// the ones not connected now.
func (s *Server) historyWorkers(w http.ResponseWriter, r *http.Request) error {
	from, to, _, err := historyRange(r)
	if err != nil {
		return err
	}
	list, err := s.d.History.Workers(from, to)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Unix(), "to": to.Unix(), "workers": list})
	return nil
}

// ---- profit switching ----

func (s *Server) profitStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.d.Profit.Status())
	return nil
}

// profitCheck compares the coins now. It never switches: in auto mode the
// switch happens on schedule; the UI offers a manual switch instead.
func (s *Server) profitCheck(w http.ResponseWriter, r *http.Request) error {
	s.d.Profit.Check(r.Context(), false)
	writeJSON(w, http.StatusOK, s.d.Profit.Status())
	return nil
}

// network is the difficulty, reward and price of the coins, for the solo
// odds on the Dashboard. Market data is cached, so polling it is cheap.
func (s *Server) network(w http.ResponseWriter, r *http.Request) error {
	body := struct {
		profit.Network
		RTT *rtt.Status `json:"rtt"` // eCash as the watched pool sees it; null without an eCash pool
	}{Network: s.d.Profit.Network(r.Context())}
	if s.d.RTT != nil {
		if st := s.d.RTT.Status(); st.Pool != "" {
			body.RTT = &st
		}
	}
	writeJSON(w, http.StatusOK, body)
	return nil
}

// ---- timed switching ----

func (s *Server) timedStatus(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.d.Timed.Status())
	return nil
}

// ---- pools ----

type poolView struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Coin             string     `json:"coin"`
	Addresses        []addrView `json:"addresses"`
	TLS              bool       `json:"tls"`
	TLSSkipVerify    bool       `json:"tls_skip_verify"`
	Username         string     `json:"username"`
	PasswordSet      bool       `json:"password_set"`
	ProfitSwitch     bool       `json:"profit_switch"`
	TimedTarget      bool       `json:"timed_target"`
	Role             string     `json:"role"`
	FallbackPosition int        `json:"fallback_position,omitempty"`
	Health           string     `json:"health"`
	LastCheck        *time.Time `json:"last_check"`
	LastError        *string    `json:"last_error"`
	Sessions         int        `json:"sessions"`
	Accepted         uint64     `json:"accepted"`
	Rejected         uint64     `json:"rejected"`
	HashrateTHs      float64    `json:"hashrate_ths"`
}

// addrView is one pool address with its measured latency.
type addrView struct {
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	Health    string   `json:"health"`
	LatencyMs *float64 `json:"latency_ms"` // smoothed TCP connect time
	LastError string   `json:"last_error,omitempty"`
	Preferred bool     `json:"preferred"` // new sessions go here first
}

func ms(d time.Duration) float64 { return math.Round(float64(d)/float64(time.Millisecond)*10) / 10 }

func (s *Server) poolViews() []poolView {
	snap := s.d.Pools.Snapshot()
	counts := s.d.Registry.Counts()
	st := s.d.Stats.Snapshot(time.Now())
	out := make([]poolView, 0, len(snap.Pools))
	for _, p := range snap.Pools {
		v := s.poolView(p, snap, counts, st)
		out = append(out, v)
	}
	return out
}

func (s *Server) poolView(p state.Pool, snap *pool.Snapshot, counts session.Counts, st stats.Summary) poolView {
	h := s.d.Pools.Health(p.ID)
	v := poolView{
		ID: p.ID, Name: p.Name, Coin: p.Coin, TLS: p.TLS,
		TLSSkipVerify: p.TLSSkipVerify, Username: p.Username, PasswordSet: p.Password != "", ProfitSwitch: p.ProfitSwitch, TimedTarget: p.TimedTarget,
		Health: string(h.Status), Sessions: counts.ByPool[p.ID],
		Accepted: st.ByPool[p.ID].Accepted, Rejected: st.ByPool[p.ID].Rejected,
		HashrateTHs: st.PoolHashrateHs[p.ID] / 1e12,
	}
	v.Role, v.FallbackPosition = snap.Role(p.ID)
	for _, a := range s.d.Pools.AddressHealth(p) {
		av := addrView{Host: a.Address.Host, Port: a.Address.Port, Health: string(a.Status), LastError: a.LastError, Preferred: a.Preferred}
		if a.RTT > 0 {
			l := ms(a.RTT)
			av.LatencyMs = &l
		}
		v.Addresses = append(v.Addresses, av)
	}
	if !h.LastCheck.IsZero() {
		t := h.LastCheck.UTC()
		v.LastCheck = &t
	}
	if h.LastError != "" {
		e := h.LastError
		v.LastError = &e
	}
	return v
}

func (s *Server) listPools(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]any{"pools": s.poolViews()})
	return nil
}

func (s *Server) onePool(id string) poolView {
	for _, v := range s.poolViews() {
		if v.ID == id {
			return v
		}
	}
	return poolView{ID: id}
}

func (s *Server) createPool(w http.ResponseWriter, r *http.Request) error {
	var in pool.Input
	if err := decode(r, &in); err != nil {
		return err
	}
	p, err := s.d.Pools.Create(in)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, s.onePool(p.ID))
	return nil
}

func (s *Server) updatePool(w http.ResponseWriter, r *http.Request) error {
	var in pool.Input
	if err := decode(r, &in); err != nil {
		return err
	}
	in.ID = nil // id never changes
	reconnect := r.URL.Query().Get("reconnect") == "true"
	p, n, err := s.d.Pools.Update(r.PathValue("id"), in, reconnect)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"pool": s.onePool(p.ID), "reconnecting": n})
	return nil
}

func (s *Server) deletePool(w http.ResponseWriter, r *http.Request) error {
	n, err := s.d.Pools.Delete(r.PathValue("id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reconnecting": n})
	return nil
}

func (s *Server) testPool(w http.ResponseWriter, r *http.Request) error {
	start := time.Now()
	res, err := s.d.Pools.TestPool(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	writeTestResult(w, start, res)
	return nil
}

// testConfig checks pool settings from the editor before they are saved. An
// "id" in the body names the saved pool the fields are applied to.
func (s *Server) testConfig(w http.ResponseWriter, r *http.Request) error {
	start := time.Now()
	var in pool.Input
	if err := decode(r, &in); err != nil {
		return err
	}
	base := ""
	if in.ID != nil {
		base = *in.ID
	}
	in.ID = nil
	res, err := s.d.Pools.TestConfig(r.Context(), base, in)
	if err != nil {
		return err
	}
	writeTestResult(w, start, res)
	return nil
}

func writeTestResult(w http.ResponseWriter, start time.Time, res []pool.AddrResult) {
	type result struct {
		Host      string   `json:"host"`
		Port      int      `json:"port"`
		OK        bool     `json:"ok"`
		LatencyMs *float64 `json:"latency_ms"`
		Error     string   `json:"error,omitempty"`
	}
	out := make([]result, 0, len(res))
	for _, x := range res {
		rv := result{Host: x.Address.Host, Port: x.Address.Port, OK: x.Err == nil}
		if x.RTT > 0 {
			l := ms(x.RTT)
			rv.LatencyMs = &l
		}
		if x.Err != nil {
			rv.Error = x.Err.Error()
		}
		out = append(out, rv)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "elapsed_ms": time.Since(start).Milliseconds(), "addresses": out})
}

func (s *Server) activatePool(w http.ResponseWriter, r *http.Request) error {
	force := r.URL.Query().Get("force") == "true"
	n, err := s.d.Pools.Activate(r.Context(), r.PathValue("id"), force)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reconnecting": n, "window": settings.FormatDuration(s.d.Settings.Get().SwitchDrain)})
	return nil
}

func (s *Server) setFallback(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Pools *[]string `json:"pools"`
	}
	if err := decode(r, &body); err != nil {
		return err
	}
	if body.Pools == nil {
		// Clearing the list must be explicit: {"pools": []}.
		return apierr.Validation(apierr.M("fallback_pools_required", `"pools" is required; send [] to clear the list`),
			map[string]apierr.Msg{"pools": apierr.M("required", "required")})
	}
	if err := s.d.Pools.SetFallback(*body.Pools); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": s.poolViews()})
	return nil
}

// ---- settings ----

// connString is how ASICs reach one listener. Host is empty until
// PUBLIC_HOST is set; the UI then shows a placeholder instead of URL.
type connString struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
	Scheme  string `json:"scheme"`
	Host    string `json:"host"`
	Port    string `json:"port"`
	URL     string `json:"url,omitempty"`
}

func (s *Server) connectionStrings() map[string]connString {
	c := s.d.Config
	mk := func(scheme, listen string, public int) connString {
		if listen == "" {
			return connString{}
		}
		port := strconv.Itoa(public)
		if public == 0 {
			if _, p, err := net.SplitHostPort(listen); err == nil {
				port = p
			}
		}
		cs := connString{Enabled: true, Listen: listen, Scheme: scheme, Host: c.PublicHost, Port: port}
		if c.PublicHost != "" {
			cs.URL = scheme + "://" + net.JoinHostPort(c.PublicHost, port)
		}
		return cs
	}
	// Firmwares disagree on the TLS scheme name: Whatsminer accepts only
	// stratum+tls, others also take stratum+ssl. Both mean the same.
	return map[string]connString{
		"tcp": mk("stratum+tcp", c.StratumTCPAddr, c.PublicTCPPort),
		"tls": mk("stratum+tls", c.StratumTLSAddr, c.PublicTLSPort),
	}
}

func (s *Server) settingsPayload() map[string]any {
	c := s.d.Config
	server := map[string]any{
		"connections":    s.connectionStrings(),
		"public_host":    c.PublicHost,
		"admin_listen":   c.AdminAddr,
		"admin_username": c.AdminUsername,
		"api_token_set":  c.APIToken != "",
		"data_dir":       c.DataDir,
		"log_format":     c.LogFormat,
		"certificate":    nil,
	}
	if s.d.Certs != nil {
		server["certificate"] = s.d.Certs.Info()
	}
	list := s.d.Settings.Describe()
	modified := 0
	for _, m := range list {
		if m.Modified {
			modified++
		}
	}
	return map[string]any{
		"groups":   settings.Groups,
		"settings": list,
		"modified": modified,
		"server":   server,
	}
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.settingsPayload())
	return nil
}

type changeView struct {
	Key     string           `json:"key"`
	Old     string           `json:"old"`
	New     string           `json:"new"`
	Applies settings.Applies `json:"applies"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) error {
	var body map[string]json.RawMessage
	if err := decode(r, &body); err != nil {
		return err
	}
	if len(body) == 0 {
		return apierr.Validation(apierr.M("settings_no_changes", "no changes"), nil)
	}
	var pending *settings.Pending
	err := s.d.State.Mutate(func(f *state.File) error {
		p, err := s.d.Settings.Prepare(body)
		if err != nil {
			return err
		}
		f.Settings = p.Overrides
		pending = p
		return nil
	}, func(*state.File) { s.d.Settings.Commit(pending) })
	if err != nil {
		var e *apierr.Error
		if errors.As(err, &e) {
			return e
		}
		return apierr.Internal(apierr.M("state_save_failed", "cannot save state.json: {error}", "error", err.Error()))
	}
	changes := make([]changeView, 0, len(pending.Changes))
	suggest := false
	for _, c := range pending.Changes {
		s.d.Events.Info("settings_changed", "settings changed: %s %s → %s", c.Key, c.Old, c.New)
		changes = append(changes, changeView{Key: c.Key, Old: c.Old, New: c.New, Applies: c.Applies})
		if c.Applies == settings.AppliesNewConnections {
			suggest = true
		}
	}
	resp := s.settingsPayload()
	resp["changes"] = changes
	resp["reconnect_suggested"] = suggest && s.d.Registry.Counts().Total > 0
	resp["sessions"] = s.d.Registry.Counts().Total
	writeJSON(w, http.StatusOK, resp)
	return nil
}
