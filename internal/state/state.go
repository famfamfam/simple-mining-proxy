// Package state stores pools and runtime settings in /data/state.json.
// Writes are atomic: a temporary file is written, synced and renamed over
// the old one, and the in-memory state changes only after that succeeded.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
	"github.com/famfamfam/simple-mining-proxy/internal/atomicfile"
)

// CurrentVersion 2: a pool has a list of addresses instead of host/port.
const CurrentVersion = 2

// MaxPoolAddresses bounds the address list of one pool.
const MaxPoolAddresses = 8

// Address is one server of a pool. A pool may list several (regional
// servers of the same pool); the proxy sends new sessions to the fastest
// reachable one.
type Address struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

func (a Address) String() string { return net.JoinHostPort(a.Host, strconv.Itoa(a.Port)) }

type Pool struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Coin          string    `json:"coin"`
	Addresses     []Address `json:"addresses"`
	TLS           bool      `json:"tls"`
	TLSSkipVerify bool      `json:"tls_skip_verify"`
	Username      string    `json:"username"`
	Password      string    `json:"password"`
	// ProfitSwitch lets profit switching choose this pool.
	ProfitSwitch bool `json:"profit_switch,omitempty"`
}

// UnmarshalJSON also reads the version-1 form with a single host and port.
func (p *Pool) UnmarshalJSON(b []byte) error {
	type plain Pool
	var v struct {
		plain
		Host string `json:"host"`
		Port int    `json:"port"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Pool(v.plain)
	if len(p.Addresses) == 0 && (v.Host != "" || v.Port != 0) {
		p.Addresses = []Address{{Host: v.Host, Port: v.Port}}
	}
	return nil
}

var poolIDPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ValidPoolID reports whether id can safely be used in API paths and state
// references.
func ValidPoolID(id string) bool { return poolIDPattern.MatchString(id) }

// PoolFieldErrors is the shared validation used both by the API and while
// loading state.json, so hand-edited state cannot bypass the API invariants.
func PoolFieldErrors(p Pool) map[string]apierr.Msg {
	errs := map[string]apierr.Msg{}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > 64 {
		errs["name"] = apierr.M("pool_name", "required, up to 64 characters")
	}
	if len(p.Coin) > 16 {
		errs["coin"] = apierr.M("pool_coin", "up to 16 characters")
	}
	if msg := addressesError(p.Addresses); !msg.IsZero() {
		errs["addresses"] = msg
	}
	if p.Username == "" || len(p.Username) > 256 {
		errs["username"] = apierr.M("pool_username", "required, up to 256 characters; e.g. account.{worker}")
	} else if strings.ContainsAny(p.Username, " \t") {
		errs["username"] = apierr.M("pool_username_spaces", "spaces are not allowed")
	}
	if len(p.Password) > 256 {
		errs["password"] = apierr.M("pool_password", "up to 256 characters")
	}
	if p.TLSSkipVerify && !p.TLS {
		errs["tls_skip_verify"] = apierr.M("pool_skip_verify", "only allowed with TLS")
	}
	return errs
}

func addressesError(addrs []Address) apierr.Msg {
	switch {
	case len(addrs) == 0:
		return apierr.M("pool_addresses_empty", "at least one host:port address is required")
	case len(addrs) > MaxPoolAddresses:
		return apierr.M("pool_addresses_many", "at most {max} addresses", "max", MaxPoolAddresses)
	}
	seen := map[string]bool{}
	for i, a := range addrs {
		reason := hostError(a.Host)
		if reason.IsZero() && (a.Port < 1 || a.Port > 65535) {
			reason = apierr.M("addr_port", "port must be 1–65535")
		}
		key := strings.ToLower(a.String())
		if reason.IsZero() && seen[key] {
			reason = apierr.M("addr_duplicate", "listed twice")
		}
		if !reason.IsZero() {
			return apierr.M("pool_address", "address {n} ({address}): {reason}",
				"n", i+1, "address", a.String(), "reason", reason)
		}
		seen[key] = true
	}
	return apierr.Msg{}
}

func hostError(h string) apierr.Msg {
	switch {
	case h == "":
		return apierr.M("addr_host_missing", "host is missing")
	case strings.HasPrefix(h, "[") || strings.HasSuffix(h, "]"):
		return apierr.M("addr_ipv6_brackets", "write IPv6 without square brackets")
	case strings.Contains(h, "://") || strings.ContainsAny(h, " /"):
		return apierr.M("addr_scheme", "host name or IP only, without stratum+tcp://")
	case strings.Count(h, ":") == 1:
		return apierr.M("addr_port_separate", "the port goes separately")
	case strings.Contains(h, ":") && net.ParseIP(h) == nil:
		return apierr.M("addr_ipv6_invalid", "invalid IPv6 address")
	}
	return apierr.Msg{}
}

// LogValue keeps the password out of logs.
func (p Pool) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", p.ID),
		slog.Any("addresses", p.Addresses),
		slog.Bool("tls", p.TLS),
		slog.String("username", p.Username),
	)
}

type File struct {
	Version       int                        `json:"version"`
	ActivePool    string                     `json:"active_pool"`
	FallbackPools []string                   `json:"fallback_pools"`
	Pools         []Pool                     `json:"pools"`
	Settings      map[string]json.RawMessage `json:"settings"`
}

func (f *File) Pool(id string) (*Pool, bool) {
	for i := range f.Pools {
		if f.Pools[i].ID == id {
			return &f.Pools[i], true
		}
	}
	return nil, false
}

func (f *File) clone() *File {
	b, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	var c File
	if err := json.Unmarshal(b, &c); err != nil {
		panic(err)
	}
	c.normalize()
	return &c
}

func (f *File) normalize() {
	if f.FallbackPools == nil {
		f.FallbackPools = []string{}
	}
	if f.Pools == nil {
		f.Pools = []Pool{}
	}
	if f.Settings == nil {
		f.Settings = map[string]json.RawMessage{}
	}
}

// check validates pools and references between active and fallback pools.
func (f *File) check() error {
	ids := map[string]bool{}
	for _, p := range f.Pools {
		if p.ID == "" {
			return errors.New("pool without id")
		}
		if !ValidPoolID(p.ID) {
			return fmt.Errorf("invalid pool id %q", p.ID)
		}
		if ids[p.ID] {
			return fmt.Errorf("duplicate pool id %q", p.ID)
		}
		if fields := PoolFieldErrors(p); len(fields) > 0 {
			keys := make([]string, 0, len(fields))
			for key := range fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				parts = append(parts, key+": "+fields[key].String())
			}
			return fmt.Errorf("pool %q has invalid fields: %s", p.ID, strings.Join(parts, "; "))
		}
		ids[p.ID] = true
	}
	if f.ActivePool != "" && !ids[f.ActivePool] {
		return fmt.Errorf("active_pool %q is not in pools", f.ActivePool)
	}
	if f.ActivePool == "" && len(f.Pools) > 0 {
		return errors.New("active_pool is empty while pools exist")
	}
	seen := map[string]bool{}
	for _, id := range f.FallbackPools {
		if !ids[id] {
			return fmt.Errorf("fallback pool %q is not in pools", id)
		}
		if id == f.ActivePool {
			return fmt.Errorf("active pool %q is also in fallback_pools", id)
		}
		if seen[id] {
			return fmt.Errorf("fallback pool %q is listed twice", id)
		}
		seen[id] = true
	}
	return nil
}

// Store owns state.json. All mutations are serialized by one mutex.
type Store struct {
	path string
	mu   sync.Mutex
	cur  *File
}

// Open loads path. A missing file means an empty configuration; a file that
// cannot be read or parsed is an error, never a silent reset.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.cur = &File{Version: CurrentVersion}
		s.cur.normalize()
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix or remove the file; the proxy does not reset configuration silently)", path, err)
	}
	if f.Version > CurrentVersion {
		return nil, fmt.Errorf("%s has version %d, this build supports up to %d", path, f.Version, CurrentVersion)
	}
	migrated := f.Version < CurrentVersion
	f.Version = CurrentVersion
	f.normalize()
	// This retired setting used to exempt some logins from rewriting. Removing
	// it is compatible with version 2: older binaries treat a missing key as
	// the default (rewrite every login), which keeps image rollback working.
	if _, ok := f.Settings["rewrite_login_prefixes"]; ok {
		delete(f.Settings, "rewrite_login_prefixes")
		migrated = true
	}
	if err := f.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.cur = &f
	if migrated {
		if err := write(path, &f); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Current returns a copy of the state.
func (s *Store) Current() *File {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur.clone()
}

// Mutate applies fn to a copy of the state, validates and writes it. Only
// after a successful write the copy becomes current and commit is called,
// still under the lock, so in-memory snapshots change in the same order as
// the file. If fn or the write fails, nothing changes.
func (s *Store) Mutate(fn func(f *File) error, commit func(f *File)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur.clone()
	if err := fn(next); err != nil {
		return err
	}
	if err := next.check(); err != nil {
		return err
	}
	if err := write(s.path, next); err != nil {
		return err
	}
	s.cur = next
	if commit != nil {
		commit(next.clone())
	}
	return nil
}

func write(path string, f *File) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.Write(path, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
