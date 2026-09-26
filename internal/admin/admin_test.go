package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/config"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/history"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/profit"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/telegram"
	"github.com/famfamfam/simple-mining-proxy/internal/timed"
)

type env struct {
	h   http.Handler
	st  *state.Store
	set *settings.Store
	ev  *events.Log
}

func newEnv(t *testing.T, opts ...func(*Deps)) *env {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	set, _, _ := settings.NewStore(nil)
	ev := events.New(100)
	reg := session.NewRegistry(ev)
	cfg := &config.Config{AdminUsername: "admin", AdminPassword: "secret", APIToken: "tok", StratumTCPAddr: ":13333", PublicHost: "vds.example.com", PublicTCPPort: 25111}
	hist, err := history.New(filepath.Join(t.TempDir(), "history"), func() history.Sample { return history.Sample{} },
		func() history.Retention {
			return history.Retention{Detail: 24 * time.Hour, Total: 48 * time.Hour, Miners: 48 * time.Hour}
		})
	if err != nil {
		t.Fatal(err)
	}
	mgr := pool.NewManager(st, set, reg, ev)
	market := &profit.Market{Fetched: time.Now(), BTCUSD: 80000, Coins: map[string]profit.Coin{
		"BTC": {Tag: "BTC", Name: "Bitcoin", Difficulty: 1e14, BlockReward: 3.125, PriceBTC: 1},
	}}
	sw := profit.New(profit.Deps{Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(t.TempDir(), "profit.json"),
		Fetch: func(context.Context) (*profit.Market, error) { return market, nil }})
	ts := timed.New(timed.Deps{Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(t.TempDir(), "timed.json")})
	d := Deps{Config: cfg, State: st, Settings: set, Pools: mgr,
		Registry: reg, Stats: stats.NewCollector(), History: hist, Profit: sw, Timed: ts, Events: ev, Started: time.Now()}
	for _, o := range opts {
		o(&d)
	}
	return &env{h: New(d), st: st, set: set, ev: ev}
}

func (e *env) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

var bearer = map[string]string{"Authorization": "Bearer tok"}

func TestEqualCredentials(t *testing.T) {
	if !equal("same", "same") || equal("short", "much-longer") {
		t.Fatal("credential comparison returned the wrong result")
	}
}

func TestAuth(t *testing.T) {
	e := newEnv(t)
	if w := e.do("GET", "/api/health", "", nil); w.Code != 200 {
		t.Fatalf("health: %d", w.Code)
	}
	if w := e.do("GET", "/api/status", "", nil); w.Code != 401 {
		t.Fatalf("status without auth: %d", w.Code)
	}
	if w := e.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer wrong"}); w.Code != 401 {
		t.Fatalf("wrong token: %d", w.Code)
	}
	if w := e.do("GET", "/api/status", "", bearer); w.Code != 200 {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}

	w := e.do("POST", "/api/auth/login", `{"username":"admin","password":"secret"}`, nil)
	if w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	cookie := w.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie flags: %+v", cookie)
	}
	withCookie := map[string]string{"Cookie": cookie.Name + "=" + cookie.Value}
	if w := e.do("GET", "/api/pools", "", withCookie); w.Code != 200 {
		t.Fatalf("cookie auth: %d", w.Code)
	}
	// CSRF: a form post without JSON content type is refused.
	r := httptest.NewRequest("POST", "/api/miners/reconnect", strings.NewReader("a=b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Cookie", cookie.Name+"="+cookie.Value)
	rw := httptest.NewRecorder()
	e.h.ServeHTTP(rw, r)
	if rw.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("non-JSON POST: %d", rw.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if w := e.do("POST", "/api/auth/login", `{"username":"admin","password":"nope"}`, nil); w.Code != 401 {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := e.do("POST", "/api/auth/login", `{"username":"admin","password":"secret"}`, nil); w.Code != 429 {
		t.Fatalf("expected 429 after 5 failures, got %d", w.Code)
	}
}

func TestConcurrentLoginRateLimit(t *testing.T) {
	e := newEnv(t)
	const attempts = 25
	start := make(chan struct{})
	codes := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			codes <- e.do("POST", "/api/auth/login", `{"username":"admin","password":"nope"}`, nil).Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)

	unauthorized, limited := 0, 0
	for code := range codes {
		switch code {
		case http.StatusUnauthorized:
			unauthorized++
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("unexpected status %d", code)
		}
	}
	if unauthorized != ipFailLimit || limited != attempts-ipFailLimit {
		t.Fatalf("401=%d 429=%d", unauthorized, limited)
	}
}

func TestSettingsAPI(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/settings", "", bearer)
	var got struct {
		Settings []settings.Meta `json:"settings"`
		Server   map[string]any  `json:"server"`
	}
	json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Settings) != len(settings.Defs) || got.Settings[0].Group == "" || got.Settings[0].Applies == "" {
		t.Fatalf("settings metadata: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "rewrite_login_prefixes") {
		t.Fatalf("retired setting exposed: %s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"url":"stratum+tcp://vds.example.com:25111"`) {
		t.Errorf("connection string missing: %s", w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), `"tok"`) {
		t.Fatal("secrets leaked into the settings response")
	}

	// All-or-nothing: one invalid value rejects the whole request.
	w = e.do("PUT", "/api/settings", `{"switch_drain":"20s","max_connections":1}`, bearer)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "max_connections") {
		t.Fatalf("invalid PUT: %d %s", w.Code, w.Body)
	}
	if e.set.Get().SwitchDrain != 10*time.Second || len(e.st.Current().Settings) != 0 {
		t.Fatal("rejected PUT changed settings")
	}

	// Retired settings are rejected atomically instead of being silently kept.
	w = e.do("PUT", "/api/settings", `{"switch_drain":"20s","rewrite_login_prefixes":["farm."]}`, bearer)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "rewrite_login_prefixes") {
		t.Fatalf("retired setting PUT: %d %s", w.Code, w.Body)
	}
	if e.set.Get().SwitchDrain != 10*time.Second || len(e.st.Current().Settings) != 0 {
		t.Fatal("retired setting PUT changed settings")
	}

	w = e.do("PUT", "/api/settings", `{"switch_drain":"20s"}`, bearer)
	if w.Code != 200 {
		t.Fatalf("PUT: %d %s", w.Code, w.Body)
	}
	if e.set.Get().SwitchDrain != 20*time.Second || string(e.st.Current().Settings["switch_drain"]) != `"20s"` {
		t.Fatal("setting not applied or not persisted")
	}
	found := false
	for _, ev := range e.ev.List(0) {
		if strings.Contains(ev.Message, "settings changed: switch_drain 10s → 20s") {
			found = true
		}
	}
	if !found {
		t.Error("settings change event missing")
	}

	w = e.do("POST", "/api/settings/preview-login", `{"login":"vnish.fee01"}`, bearer)
	if w.Code != http.StatusNotFound {
		t.Fatalf("retired preview endpoint: %d %s", w.Code, w.Body)
	}
}

func TestEmptyJSONCannotClearFallback(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{
		`{"name":"Pool A","host":"a.example.com","port":3333,"username":"a.{worker}"}`,
		`{"name":"Pool B","host":"b.example.com","port":3333,"username":"b.{worker}"}`,
	} {
		if w := e.do("POST", "/api/pools", body, bearer); w.Code != http.StatusCreated {
			t.Fatalf("create pool: %d %s", w.Code, w.Body)
		}
	}
	if w := e.do("PUT", "/api/fallback", `{"pools":["pool_b"]}`, bearer); w.Code != http.StatusOK {
		t.Fatalf("set fallback: %d %s", w.Code, w.Body)
	}

	headers := map[string]string{"Authorization": "Bearer tok", "Content-Type": "application/json"}
	if w := e.do("PUT", "/api/fallback", "", headers); w.Code != http.StatusBadRequest {
		t.Fatalf("empty body: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/fallback", "null", bearer); w.Code != http.StatusBadRequest {
		t.Fatalf("null body: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{}`, `{"pools":null}`} {
		if w := e.do("PUT", "/api/fallback", body, bearer); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	if got := e.st.Current().FallbackPools; len(got) != 1 || got[0] != "pool_b" {
		t.Fatalf("fallback changed after rejected requests: %v", got)
	}
}

func TestPoolsAPIHidesPassword(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/pools", `{"name":"Pool A","coin":"btc","host":"btc.example.com","port":3333,"username":"acc.{worker}","password":"hunter2"}`, bearer)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = e.do("GET", "/api/pools", "", bearer)
	body := w.Body.String()
	if strings.Contains(body, "hunter2") || !strings.Contains(body, `"password_set":true`) || !strings.Contains(body, `"role":"active"`) {
		t.Fatalf("pools: %s", body)
	}
	if w := e.do("DELETE", "/api/pools/pool_a", `{}`, bearer); w.Code != 409 {
		t.Fatalf("deleting the active pool must be 409, got %d", w.Code)
	}
	w = e.do("POST", "/api/pools", `{"name":"Bad","host":"stratum+tcp://x:1","port":0,"username":""}`, bearer)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"addresses"`) || !strings.Contains(w.Body.String(), `"username"`) {
		t.Fatalf("validation: %d %s", w.Code, w.Body)
	}
}

func TestPoolWithSeveralAddresses(t *testing.T) {
	e := newEnv(t)
	body := `{"name":"Multi","host":"ignored.example.com","port":1,"addresses":[{"host":"eu.pool.example.com","port":3333},{"host":"us.pool.example.com","port":443}],"username":"acc.{worker}"}`
	if w := e.do("POST", "/api/pools", body, bearer); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w := e.do("GET", "/api/pools", "", bearer)
	var got struct {
		Pools []struct {
			Addresses []struct {
				Host   string `json:"host"`
				Port   int    `json:"port"`
				Health string `json:"health"`
			} `json:"addresses"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	a := got.Pools[0].Addresses
	if len(a) != 2 || a[0].Host != "eu.pool.example.com" || a[1].Port != 443 || a[0].Health != "UNKNOWN" {
		t.Fatalf("addresses: %s", w.Body)
	}
	w = e.do("PUT", "/api/pools/multi", `{"addresses":[{"host":"eu.pool.example.com","port":3333},{"host":"EU.pool.example.com","port":3333}]}`, bearer)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"reason":{"key":"addr_duplicate"`) {
		t.Fatalf("duplicate address accepted: %d %s", w.Code, w.Body)
	}
}

func TestBearerGuessesAreRateLimited(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < ipFailLimit; i++ {
		if w := e.do("GET", "/api/status", "", map[string]string{"Authorization": "Bearer wrong"}); w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	if w := e.do("GET", "/api/status", "", bearer); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after %d wrong tokens, got %d", ipFailLimit, w.Code)
	}
}

func TestLogoutRequiresJSON(t *testing.T) {
	e := newEnv(t)
	r := httptest.NewRequest("POST", "/api/auth/logout", strings.NewReader("a=b"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("form logout: %d", w.Code)
	}
}

func TestTelegramWithoutToken(t *testing.T) {
	e := newEnv(t)
	w := e.do("PUT", "/api/settings", `{"telegram_chats":"100,-2001"}`, bearer)
	if w.Code != http.StatusOK {
		t.Fatalf("save chats: %d %s", w.Code, w.Body)
	}
	var body struct {
		Server struct {
			Telegram struct {
				Configured bool `json:"configured"`
				Chats      int  `json:"chats"`
			} `json:"telegram"`
		} `json:"server"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Server.Telegram.Configured || body.Server.Telegram.Chats != 2 {
		t.Fatalf("telegram = %+v", body.Server.Telegram)
	}
	w = e.do("POST", "/api/telegram/test", `{}`, bearer)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"key":"telegram_no_token"`) {
		t.Fatalf("test without a token: %d %s", w.Code, w.Body)
	}
	w = e.do("PUT", "/api/settings", `{"telegram_chats":"100, 200"}`, bearer)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("chats with a space: %d %s", w.Code, w.Body)
	}
}

func TestTelegramToken(t *testing.T) {
	const good = "123456:GOODGOODGOODGOODGOODGOOD00"
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bot"+good+"/getMe" {
			fmt.Fprint(w, `{"ok":true,"result":{"username":"farm_bot"}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	}))
	defer tg.Close()
	var bot *telegram.Bot
	e := newEnv(t, func(d *Deps) {
		bot = telegram.New(telegram.Deps{APIURL: tg.URL, Settings: d.Settings, Events: d.Events})
		d.Telegram = bot
	})

	w := e.do("PUT", "/api/telegram/token", `{"token":"not a token"}`, bearer)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"key":"telegram_token_format"`) {
		t.Fatalf("bad format: %d %s", w.Code, w.Body)
	}
	w = e.do("PUT", "/api/telegram/token", `{"token":"123456:WRONGWRONGWRONGWRONGWRONG"}`, bearer)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"key":"telegram_token_rejected"`) {
		t.Fatalf("rejected token: %d %s", w.Code, w.Body)
	}
	if e.st.Current().TelegramToken != "" || bot.Status().Configured {
		t.Fatal("a rejected token was saved")
	}

	w = e.do("PUT", "/api/telegram/token", `{"token":" `+good+` "}`, bearer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"username":"farm_bot"`) {
		t.Fatalf("good token: %d %s", w.Code, w.Body)
	}
	if e.st.Current().TelegramToken != good || !bot.Status().Configured {
		t.Fatal("the token was not saved or applied")
	}
	for _, path := range []string{"/api/settings", "/api/status", "/api/events"} {
		if w := e.do("GET", path, "", bearer); strings.Contains(w.Body.String(), "GOODGOOD") {
			t.Fatalf("%s returns the token: %s", path, w.Body)
		}
	}

	w = e.do("PUT", "/api/telegram/token", `{"token":""}`, bearer)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("remove: %d %s", w.Code, w.Body)
	}
	if e.st.Current().TelegramToken != "" || bot.Status().Configured {
		t.Fatal("the token was not removed")
	}
	w = e.do("POST", "/api/telegram/test", `{}`, bearer)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"key":"telegram_no_token"`) {
		t.Fatalf("test without a token: %d %s", w.Code, w.Body)
	}
}

func TestTimedStart(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/timed/start", `{}`, bearer)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"key":"timed_off"`) {
		t.Fatalf("timer off: %d %s", w.Code, w.Body)
	}
	if w := e.do("PUT", "/api/settings", `{"timed_switch":"on"}`, bearer); w.Code != http.StatusOK {
		t.Fatalf("turn on: %d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/api/timed/start", `{}`, bearer)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"key":"timed_no_target"`) {
		t.Fatalf("no target: %d %s", w.Code, w.Body)
	}
}

// A solo pool can hold any role but never takes part in profit switching.
func TestSoloPool(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/pools", `{"name":"Solo","coin":"xec","host":"solo.example.com","port":3333,"username":"addr.{worker}","solo":true,"profit_switch":true}`, bearer)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"pool_solo_profit"`) {
		t.Fatalf("solo with profit switching: %d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/api/pools", `{"name":"Solo","coin":"xec","host":"solo.example.com","port":3333,"username":"addr.{worker}","solo":true}`, bearer)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("solo pool: %d %s", w.Code, w.Body)
	}
	w = e.do("GET", "/api/pools", "", bearer)
	if !strings.Contains(w.Body.String(), `"solo":true`) || !strings.Contains(w.Body.String(), `"role":"active"`) {
		t.Fatalf("pools: %s", w.Body)
	}
}
