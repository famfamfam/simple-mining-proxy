package session

import (
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

// Some pools answer {"id":7,...} with {"id":"7",...}; the share must still
// be counted.
func TestShareCountedWhenPoolEchoesIDAsString(t *testing.T) {
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		maxLine:       64 * 1024,
		up:            &Upstream{PoolID: "p", PoolName: "P", Template: "acc.{worker}", Password: "x"},
		srv:           &Server{Settings: set, Events: events.New(10), Stats: stats.NewCollector()},
		rate:          stats.NewRate(time.Now()),
		logins:        map[string]string{},
		pendingAuth:   map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{},
		difficulty:    1024,
	}
	line := []byte(`{"id":7,"method":"mining.submit","params":["farm.a","j","0","0","0"]}`)
	msg, err := stratum.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.submit(line, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fromPool([]byte(`{"id":"7","result":true,"error":null}`)); err != nil {
		t.Fatal(err)
	}
	if s.accepted != 1 || len(s.pendingSubmit) != 0 {
		t.Fatalf("accepted=%d pending=%d", s.accepted, len(s.pendingSubmit))
	}
	// The share counts for the ASIC login it was sent with.
	if w := s.srv.Stats.Totals().Workers["farm.a"]; w.Accepted != 1 || w.Diff != 1024 {
		t.Fatalf("worker counters: %+v", w)
	}
}

// The monitor watches the shares an ASIC sends, whatever the pool answers:
// a rejected share still means the ASIC is hashing.
func TestLastSubmitIgnoresPoolAnswer(t *testing.T) {
	set, _, err := settings.NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{
		maxLine:       64 * 1024,
		up:            &Upstream{PoolID: "p", PoolName: "P", Template: "acc.{worker}", Password: "x"},
		srv:           &Server{Settings: set, Events: events.New(10), Stats: stats.NewCollector()},
		rate:          stats.NewRate(time.Now()),
		logins:        map[string]string{},
		pendingAuth:   map[string]authInfo{},
		pendingSubmit: map[string]submitInfo{},
	}
	if in := s.Info(time.Now()); in.LastSubmit != nil {
		t.Fatalf("last submit before any share: %v", in.LastSubmit)
	}
	before := time.Now()
	line := []byte(`{"id":8,"method":"mining.submit","params":["farm.a","j","0","0","0"]}`)
	msg, err := stratum.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.submit(line, msg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fromPool([]byte(`{"id":8,"result":null,"error":[23,"Low difficulty share",null]}`)); err != nil {
		t.Fatal(err)
	}
	in := s.Info(time.Now())
	if in.LastSubmit == nil || in.LastSubmit.Before(before) || in.LastShare != nil {
		t.Fatalf("last submit %v, last accepted share %v", in.LastSubmit, in.LastShare)
	}
}
