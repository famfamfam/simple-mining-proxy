// Package e2e runs the proxy components together against fake pools and a
// fake ASIC over real TCP sockets.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/listener"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
)

// fakePool is a minimal Stratum pool that records what it receives.
type fakePool struct {
	ln       net.Listener
	mu       sync.Mutex
	received []map[string]json.RawMessage
	conns    []net.Conn
}

func newFakePool(t *testing.T) *fakePool {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakePool{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, c)
			p.mu.Unlock()
			go p.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close(); p.closeAll() })
	return p
}

func (p *fakePool) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

func (p *fakePool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		c.Close()
	}
}

func (p *fakePool) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	send := func(s string) { c.Write([]byte(s + "\n")) }
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		p.mu.Lock()
		p.received = append(p.received, m)
		p.mu.Unlock()
		var method string
		json.Unmarshal(m["method"], &method)
		id := string(m["id"])
		switch method {
		case "mining.configure":
			send(`{"id":` + id + `,"result":{"version-rolling":true,"version-rolling.mask":"1fffe000"},"error":null}`)
		case "mining.subscribe":
			send(`{"id":` + id + `,"result":[[["mining.notify","ae68"]],"08000002",4],"error":null}`)
			send(`{"id":null,"method":"mining.set_difficulty","params":[1024]}`)
		case "mining.authorize":
			send(`{"id":` + id + `,"result":true,"error":null}`)
		case "mining.submit":
			var params []string
			json.Unmarshal(m["params"], &params)
			if len(params) > 1 && params[1] == "bad" {
				send(`{"id":` + id + `,"result":false,"error":[23,"Low difficulty share",null]}`)
			} else {
				send(`{"id":` + id + `,"result":true,"error":null}`)
			}
		case "test.reconnect":
			send(`{"id":null,"method":"client.reconnect","params":["evil.example.com",3333,0]}`)
		}
	}
}

// find returns the raw params of the last received message with method.
func (p *fakePool) find(method string) (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := len(p.received) - 1; i >= 0; i-- {
		var m string
		json.Unmarshal(p.received[i]["method"], &m)
		if m == method {
			return p.received[i]["params"], true
		}
	}
	return nil, false
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

type proxy struct {
	addr   string
	mgr    *pool.Manager
	reg    *session.Registry
	ev     *events.Log
	stats  *stats.Collector
	st     *state.Store
	cancel context.CancelFunc
}

func startProxy(t *testing.T, pools ...*fakePool) *proxy {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	set, _, err := settings.NewStore(map[string]json.RawMessage{
		"switch_drain": json.RawMessage(`"0s"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := events.New(500)
	reg := session.NewRegistry(ev)
	col := stats.NewCollector()
	mgr := pool.NewManager(st, set, reg, ev)
	for i, fp := range pools {
		name := fmt.Sprintf("Pool %c", 'A'+i)
		acc := fmt.Sprintf("account%c.{worker}", 'A'+i)
		host, port, tls := "127.0.0.1", fp.port(), false
		if _, err := mgr.Create(pool.Input{Name: &name, Host: &host, Port: &port, TLS: &tls, Username: &acc}); err != nil {
			t.Fatal(err)
		}
	}
	srv := &session.Server{Settings: set, Router: mgr, Registry: reg, Events: ev, Stats: col}
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener.Listener{Name: "tcp", Addr: "127.0.0.1:0", Server: srv, Limits: listener.NewLimits(set, ev)}
	if err := l.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	go l.Serve(ctx, &wg)
	t.Cleanup(func() { cancel(); reg.CloseAll("test end") })
	return &proxy{addr: l.BoundAddr().String(), mgr: mgr, reg: reg, ev: ev, stats: col, st: st, cancel: cancel}
}

// asic is a fake miner.
type asic struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func dialASIC(t *testing.T, addr string) *asic {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &asic{t: t, conn: c, r: bufio.NewReader(c)}
}

func (a *asic) send(s string) {
	a.t.Helper()
	if _, err := a.conn.Write([]byte(s + "\n")); err != nil {
		a.t.Fatal(err)
	}
}

// response waits for the response with the given id, skipping notifications.
func (a *asic) response(id int) map[string]json.RawMessage {
	a.t.Helper()
	a.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := a.r.ReadBytes('\n')
		if err != nil {
			a.t.Fatalf("waiting for response %d: %v", id, err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(line, &m); err != nil {
			a.t.Fatalf("invalid JSON from proxy: %s", line)
		}
		if string(m["id"]) == strconv.Itoa(id) {
			return m
		}
	}
}

func (a *asic) handshake(login string) {
	a.send(`{"id":1,"method":"mining.configure","params":[["version-rolling"],{"version-rolling.mask":"1fffe000","version-rolling.min-bit-count":2}]}`)
	a.response(1)
	a.send(`{"id":2,"method":"mining.subscribe","params":["bmminer/2.0.0"]}`)
	a.response(2)
	a.send(`{"id":3,"method":"mining.authorize","params":["` + login + `","x"]}`)
	if r := a.response(3); string(r["result"]) != "true" {
		a.t.Fatalf("authorize failed: %v", r)
	}
}

func TestRelayRewritesAuthorizeAndSubmit(t *testing.T) {
	fp := newFakePool(t)
	px := startProxy(t, fp)
	a := dialASIC(t, px.addr)
	a.handshake("farm.S21-0042")

	params, _ := fp.find("mining.authorize")
	if string(params) != `["accountA.S21-0042","x"]` {
		t.Fatalf("pool got authorize %s", params)
	}

	// AsicBoost submit: 6 params, the version bits must reach the pool untouched.
	a.send(`{"id":4,"method":"mining.submit","params":["farm.S21-0042","job1","00000001","65f1a2b3","1a2b3c4d","1fffe000"]}`)
	if r := a.response(4); string(r["result"]) != "true" {
		t.Fatalf("submit: %v", r)
	}
	params, _ = fp.find("mining.submit")
	var got []string
	json.Unmarshal(params, &got)
	want := []string{"accountA.S21-0042", "job1", "00000001", "65f1a2b3", "1a2b3c4d", "1fffe000"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pool got submit %v, want %v", got, want)
	}

	a.send(`{"id":5,"method":"mining.submit","params":["farm.S21-0042","bad","00000001","65f1a2b3","1a2b3c4d"]}`)
	a.response(5)
	waitFor(t, "share stats", func() bool {
		s := px.stats.Snapshot(time.Now())
		return s.Accepted == 1 && s.Rejected == 1
	})
	if r := px.stats.Snapshot(time.Now()).RejectReasons["Low difficulty share"]; r != 1 {
		t.Errorf("reject reason not recorded: %v", px.stats.Snapshot(time.Now()).RejectReasons)
	}

	sessions := px.reg.List()
	if len(sessions) != 1 {
		t.Fatalf("%d sessions", len(sessions))
	}
	info := sessions[0].Info(time.Now())
	if info.Worker != "farm.S21-0042" || info.UpstreamUser != "accountA.S21-0042" || info.Difficulty != 1024 ||
		info.UserAgent != "bmminer/2.0.0" || info.Accepted != 1 || info.Rejected != 1 {
		t.Errorf("miner info: %+v", info)
	}
}

func TestArbitraryLoginIsRewritten(t *testing.T) {
	fp := newFakePool(t)
	px := startProxy(t, fp)
	a := dialASIC(t, px.addr)
	a.send(`{"id":1,"method":"mining.subscribe","params":[]}`)
	a.response(1)
	a.send(`{"id":2,"method":"mining.authorize","params":["vnish.fee01","devpass"]}`)
	a.response(2)
	params, _ := fp.find("mining.authorize")
	if string(params) != `["accountA.fee01","x"]` {
		t.Fatalf("arbitrary login was not rewritten: %s", params)
	}
	a.send(`{"id":3,"method":"mining.submit","params":["vnish.fee02","j","0","0","0"]}`)
	a.response(3)
	params, _ = fp.find("mining.submit")
	if !strings.HasPrefix(string(params), `["accountA.fee02"`) {
		t.Fatalf("arbitrary submit was not rewritten: %s", params)
	}
}

func TestClientReconnectIsNotForwarded(t *testing.T) {
	fp := newFakePool(t)
	px := startProxy(t, fp)
	a := dialASIC(t, px.addr)
	a.handshake("farm.a")
	a.send(`{"id":9,"method":"test.reconnect","params":[]}`)
	a.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		line, err := a.r.ReadBytes('\n')
		if err != nil {
			break // session closed by the proxy: expected
		}
		if strings.Contains(string(line), "client.reconnect") {
			t.Fatalf("client.reconnect reached the ASIC: %s", line)
		}
	}
	waitFor(t, "session removal", func() bool { return px.reg.Counts().Total == 0 })
}

func TestGarbageFirstMessageDoesNotReachPool(t *testing.T) {
	fp := newFakePool(t)
	px := startProxy(t, fp)
	c, err := net.Dial("tcp", px.addr)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 10)); err == nil {
		t.Fatal("expected the proxy to close the connection")
	}
	fp.mu.Lock()
	n := len(fp.conns)
	fp.mu.Unlock()
	if n != 0 {
		t.Fatalf("pool got %d connections from garbage", n)
	}
}

func TestSwitchDrainsToNewPool(t *testing.T) {
	a1, b1 := newFakePool(t), newFakePool(t)
	px := startProxy(t, a1, b1)
	m := dialASIC(t, px.addr)
	m.handshake("farm.S19-7")
	if px.reg.Counts().ByPool["pool_a"] != 1 {
		t.Fatalf("expected session on pool_a: %+v", px.reg.Counts())
	}

	n, err := px.mgr.Activate(context.Background(), "pool_b", false)
	if err != nil || n != 1 {
		t.Fatalf("activate: n=%d err=%v", n, err)
	}
	// The old session is closed; the ASIC reconnects and lands on pool B.
	m.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, err := m.r.ReadBytes('\n'); err != nil {
			break
		}
	}
	m2 := dialASIC(t, px.addr)
	m2.handshake("farm.S19-7")
	params, _ := b1.find("mining.authorize")
	if string(params) != `["accountB.S19-7","x"]` {
		t.Fatalf("pool B got %s", params)
	}
	if px.st.Current().ActivePool != "pool_b" {
		t.Fatal("active pool not persisted")
	}
}

func TestActivateUnreachablePoolChangesNothing(t *testing.T) {
	a1 := newFakePool(t)
	px := startProxy(t, a1)
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	port := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	name, host, user := "Dead", "127.0.0.1", "acc.{worker}"
	if _, err := px.mgr.Create(pool.Input{Name: &name, Host: &host, Port: &port, Username: &user}); err != nil {
		t.Fatal(err)
	}
	_, err := px.mgr.Activate(context.Background(), "dead", false)
	var e *apierr.Error
	if !errors.As(err, &e) || e.Status != 422 {
		t.Fatalf("want 422, got %v", err)
	}
	if px.st.Current().ActivePool != "pool_a" || px.mgr.Snapshot().Active != "pool_a" {
		t.Fatal("failed activation changed state")
	}
}

func TestFailoverToFallback(t *testing.T) {
	a1, b1 := newFakePool(t), newFakePool(t)
	px := startProxy(t, a1, b1)
	if err := px.mgr.SetFallback([]string{"pool_b"}); err != nil {
		t.Fatal(err)
	}
	a1.ln.Close() // active pool goes away
	m := dialASIC(t, px.addr)
	m.handshake("farm.x")
	if px.reg.Counts().ByPool["pool_b"] != 1 {
		t.Fatalf("session should fail over to pool_b: %+v", px.reg.Counts())
	}
}

// A pool with a dead first address: the session must use the second address
// of the same pool, not the fallback pool.
func TestDeadAddressFallsBackWithinPool(t *testing.T) {
	good, other := newFakePool(t), newFakePool(t)
	px := startProxy(t, other)
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	deadPort := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	name, user := "Multi", "multi.{worker}"
	addrs := []state.Address{{Host: "127.0.0.1", Port: deadPort}, {Host: "127.0.0.1", Port: good.port()}}
	if _, err := px.mgr.Create(pool.Input{Name: &name, Addresses: &addrs, Username: &user}); err != nil {
		t.Fatal(err)
	}
	if _, err := px.mgr.Activate(context.Background(), "multi", false); err != nil {
		t.Fatalf("activation must pass when one address works: %v", err)
	}
	m := dialASIC(t, px.addr)
	m.handshake("farm.rig1")
	if params, _ := good.find("mining.authorize"); string(params) != `["multi.rig1","x"]` {
		t.Fatalf("second address got %s", params)
	}
	info := px.reg.List()[0].Info(time.Now())
	if info.PoolID != "multi" || info.PoolAddr != fmt.Sprintf("127.0.0.1:%d", good.port()) {
		t.Fatalf("session: %+v", info)
	}
}
