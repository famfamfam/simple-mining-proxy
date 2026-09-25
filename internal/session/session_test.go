package session

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

func TestSessionTrackingMapsAreBounded(t *testing.T) {
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		IP:            "192.0.2.10",
		up:            &Upstream{PoolName: "Pool", Template: "account.{worker}", Password: "x"},
		srv:           &Server{Settings: set, Events: events.New(10)},
		logins:        map[string]string{},
		pendingAuth:   map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{},
	}

	for i := 0; i < maxTrackedLogin+50; i++ {
		line := []byte(fmt.Sprintf(`{"id":%d,"method":"mining.authorize","params":["farm.worker%d","x"]}`, i+1, i))
		msg, err := stratum.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.authorize(line, msg); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < maxTrackedLogin+50; i++ {
		line := []byte(fmt.Sprintf(`{"id":%d,"method":"mining.authorize","params":["fee.worker%d","x"]}`, i+1000, i))
		msg, err := stratum.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.authorize(line, msg); err != nil {
			t.Fatal(err)
		}
	}

	if len(s.logins) > maxTrackedLogin || len(s.pendingAuth) > maxPendingAuth {
		t.Fatalf("unbounded maps: logins=%d pending_auth=%d", len(s.logins), len(s.pendingAuth))
	}
}

func TestOverlongLoginClosesSession(t *testing.T) {
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		maxLine:       64 * 1024,
		up:            &Upstream{Template: "account.{worker}", Password: "x"},
		srv:           &Server{Settings: set, Events: events.New(10)},
		logins:        map[string]string{},
		pendingAuth:   map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{},
	}
	long := "farm." + strings.Repeat("<", maxLoginLen)
	for _, method := range []string{"mining.authorize", "mining.submit"} {
		line := []byte(fmt.Sprintf(`{"id":1,"method":%q,"params":[%q,"x"]}`, method, long))
		msg, err := stratum.Parse(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.forwardFromMiner(line, msg); !errors.Is(err, errLoginTooLong) {
			t.Fatalf("%s: want errLoginTooLong, got %v", method, err)
		}
	}
	if len(s.logins) != 0 {
		t.Fatalf("overlong login was cached")
	}
}

func TestRewrittenMessageRespectsMaxLine(t *testing.T) {
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	// A template that expands the login well past 4 KiB.
	s := &Session{
		maxLine:       4 * 1024,
		up:            &Upstream{Template: strings.Repeat("a", 250) + ".{worker}", Password: strings.Repeat("<", 250)},
		srv:           &Server{Settings: set, Events: events.New(10)},
		logins:        map[string]string{},
		pendingAuth:   map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{},
	}
	params := `"farm.` + strings.Repeat("w", 200) + `",` + strings.Repeat(`"x",`, 900) + `"x"`
	line := []byte(`{"id":1,"method":"mining.authorize","params":[` + params + `]}`)
	if len(line) > 4096 {
		t.Fatalf("test input itself is too long: %d", len(line))
	}
	msg, err := stratum.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.forwardFromMiner(line, msg); err == nil {
		t.Fatal("rewritten message over max_line_bytes was accepted")
	}
}

func TestDifficultyMustBePositiveAndBounded(t *testing.T) {
	s := &Session{}
	for _, value := range []string{`"NaN"`, `-1`, `0`, `1e31`} {
		line := []byte(fmt.Sprintf(`{"id":null,"method":"mining.set_difficulty","params":[%s]}`, value))
		if _, err := s.fromPool(line); err != nil {
			t.Fatal(err)
		}
		if s.difficulty != 0 {
			t.Fatalf("invalid difficulty %s was stored as %v", value, s.difficulty)
		}
	}
	if _, err := s.fromPool([]byte(`{"id":null,"method":"mining.set_difficulty","params":[1024]}`)); err != nil {
		t.Fatal(err)
	}
	if s.difficulty != 1024 {
		t.Fatalf("valid difficulty was not stored: %v", s.difficulty)
	}
}

type recordingConn struct {
	net.Conn
	mu           sync.Mutex
	readDeadline time.Time
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

func (c *recordingConn) deadline() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readDeadline
}

func TestRefreshIdleDeadlinesUsesLastActivity(t *testing.T) {
	miner, minerPeer := net.Pipe()
	poolConn, poolPeer := net.Pipe()
	defer miner.Close()
	defer minerPeer.Close()
	defer poolConn.Close()
	defer poolPeer.Close()

	minerRecorded := &recordingConn{Conn: miner}
	poolRecorded := &recordingConn{Conn: poolConn}
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	minerAt := time.Now().Add(-2 * time.Minute)
	poolAt := time.Now().Add(-3 * time.Minute)
	s := &Session{
		miner: minerRecorded,
		up:    &Upstream{Conn: poolRecorded},
		srv:   &Server{Settings: set},

		lastMinerActivity: minerAt,
		lastPoolActivity:  poolAt,
	}
	v := settings.Defaults()
	v.MinerIdleTimeout = time.Minute
	v.UpstreamIdleTimeout = 2 * time.Minute
	s.refreshIdleDeadlines(v)

	if got, want := minerRecorded.deadline(), minerAt.Add(time.Minute); !got.Equal(want) {
		t.Fatalf("miner deadline=%s want=%s", got, want)
	}
	if got, want := poolRecorded.deadline(), poolAt.Add(2*time.Minute); !got.Equal(want) {
		t.Fatalf("pool deadline=%s want=%s", got, want)
	}
}

func TestShutdownAbortsFirstMessageWait(t *testing.T) {
	set, _, err := settings.NewStore(nil) // first_message_timeout is 15s
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Settings: set, Events: events.New(10), Registry: NewRegistry(events.New(10))}
	server, client := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		srv.Serve(ctx, server, false, func() {})
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve kept waiting for the first message after shutdown")
	}
}

func TestGuardTurnsPanicIntoError(t *testing.T) {
	err := guard(func() error { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}
