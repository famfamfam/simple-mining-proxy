package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMutateIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	commits := 0
	err = s.Mutate(func(f *File) error {
		f.Pools = append(f.Pools, Pool{ID: "a", Name: "A", Addresses: []Address{{Host: "h", Port: 1}}, Username: "u"})
		f.ActivePool = "a"
		return nil
	}, func(*File) { commits++ })
	if err != nil || commits != 1 {
		t.Fatalf("mutate: %v commits=%d", err, commits)
	}

	// A failing mutation changes nothing and does not commit.
	boom := errors.New("boom")
	err = s.Mutate(func(f *File) error { f.ActivePool = ""; return boom }, func(*File) { commits++ })
	if !errors.Is(err, boom) || commits != 1 || s.Current().ActivePool != "a" {
		t.Fatalf("failed mutation leaked: %v", err)
	}
	// An inconsistent result is rejected by check().
	err = s.Mutate(func(f *File) error { f.FallbackPools = []string{"missing"}; return nil }, nil)
	if err == nil {
		t.Fatal("expected consistency error")
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if c := s2.Current(); c.ActivePool != "a" || len(c.Pools) != 1 || len(c.FallbackPools) != 0 {
		t.Fatalf("reloaded state: %+v", c)
	}
}

func TestOpenRejectsInvalidPoolFields(t *testing.T) {
	cases := map[string]func(*Pool){
		"id":                   func(p *Pool) { p.ID = "BAD-ID" },
		"host":                 func(p *Pool) { p.Addresses[0].Host = "" },
		"bracketed ipv6":       func(p *Pool) { p.Addresses[0].Host = "[2001:db8::1]" },
		"multi-colon garbage":  func(p *Pool) { p.Addresses[0].Host = "foo:bar:baz" },
		"port":                 func(p *Pool) { p.Addresses[0].Port = 0 },
		"no addresses":         func(p *Pool) { p.Addresses = nil },
		"duplicate address":    func(p *Pool) { p.Addresses = append(p.Addresses, Address{Host: "POOL.example.com", Port: 3333}) },
		"username":             func(p *Pool) { p.Username = "" },
		"tls skip without tls": func(p *Pool) { p.TLSSkipVerify = true },
	}
	for name, breakPool := range cases {
		t.Run(name, func(t *testing.T) {
			p := Pool{ID: "pool", Name: "Pool", Addresses: []Address{{Host: "pool.example.com", Port: 3333}}, Username: "account.{worker}"}
			breakPool(&p)
			f := File{Version: CurrentVersion, ActivePool: p.ID, Pools: []Pool{p}}
			b, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, b, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("invalid persisted pool must be rejected")
			}
		})
	}
}

func TestOpenCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt file must be an error, not a silent reset")
	}
	os.WriteFile(path, []byte(`{"version": 99}`), 0o600)
	if _, err := Open(path); err == nil {
		t.Fatal("newer version must be an error")
	}
}

func TestOpenRemovesRetiredLoginPrefixes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v2 := `{"version":2,"active_pool":"a","fallback_pools":[],"pools":[{"id":"a","name":"A","addresses":[{"host":"btc.example.com","port":3333}],"username":"u"}],"settings":{"rewrite_login_prefixes":["farm."],"switch_drain":"20s"}}`
	if err := os.WriteFile(path, []byte(v2), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Current()
	if got.Version != CurrentVersion || len(got.Pools) != 1 || got.ActivePool != "a" {
		t.Fatalf("unrelated state changed: %+v", got)
	}
	if _, ok := got.Settings["rewrite_login_prefixes"]; ok {
		t.Fatal("retired setting remains in memory")
	}
	if string(got.Settings["switch_drain"]) != `"20s"` {
		t.Fatalf("unrelated setting changed: %s", got.Settings["switch_drain"])
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved File
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != CurrentVersion || saved.Settings["rewrite_login_prefixes"] != nil || string(saved.Settings["switch_drain"]) != `"20s"` {
		t.Fatalf("state file was not cleaned safely:\n%s", b)
	}
}

func TestOpenMigratesSingleHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	v1 := `{"version":1,"active_pool":"a","pools":[{"id":"a","name":"A","host":"btc.example.com","port":3333,"username":"u"}]}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Current().Pools[0].Addresses
	if len(got) != 1 || got[0] != (Address{Host: "btc.example.com", Port: 3333}) {
		t.Fatalf("addresses after migration: %+v", got)
	}
	b, _ := os.ReadFile(path)
	var raw struct {
		Version int                          `json:"version"`
		Pools   []map[string]json.RawMessage `json:"pools"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, legacy := raw.Pools[0]["host"]; raw.Version != CurrentVersion || legacy || raw.Pools[0]["addresses"] == nil {
		t.Fatalf("file not rewritten in the new format:\n%s", b)
	}
}
