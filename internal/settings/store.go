package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
)

// Store holds the current settings snapshot. Readers call Get() on the hot
// path without locks; writers go through Prepare/Commit, which the caller
// wraps around a successful write of state.json.
type Store struct {
	mu        sync.Mutex
	cur       atomic.Pointer[Values]
	overrides map[string]json.RawMessage
	listeners []func(*Values)
}

// NewStore builds the store from the overrides saved in state.json.
// Unknown keys are returned for a warning; an invalid value is an error
// that names the key, so a damaged file never silently resets settings.
func NewStore(saved map[string]json.RawMessage) (*Store, []string, error) {
	v, norm, unknown, errs := build(saved)
	if len(errs) > 0 {
		keys := make([]string, 0, len(errs))
		for k, msg := range errs {
			keys = append(keys, k+": "+msg.String())
		}
		sort.Strings(keys)
		return nil, unknown, fmt.Errorf("invalid settings in state.json: %s", strings.Join(keys, "; "))
	}
	s := &Store{overrides: norm}
	s.cur.Store(v)
	return s, unknown, nil
}

func (s *Store) Get() *Values { return s.cur.Load() }

// Overrides returns a copy of the values that differ from defaults.
func (s *Store) Overrides() map[string]json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyMap(s.overrides)
}

// OnChange registers fn to be called with the new snapshot after each commit.
func (s *Store) OnChange(fn func(*Values)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

type Change struct {
	Key     string
	Old     string
	New     string
	Applies Applies
}

// Pending is a validated set of changes that is not yet visible to readers.
type Pending struct {
	values    *Values
	Overrides map[string]json.RawMessage
	Changes   []Change
}

// Prepare validates changes against the current values. A JSON null resets
// the key to its default. Nothing is changed if any value is invalid.
func (s *Store) Prepare(changes map[string]json.RawMessage) (*Pending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := copyMap(s.overrides)
	errs := map[string]apierr.Msg{}
	for key, raw := range changes {
		d, ok := byKey[key]
		if !ok {
			errs[key] = apierr.M("setting_unknown", "unknown setting")
			continue
		}
		if isNull(raw) {
			delete(next, key)
			continue
		}
		val, msg := parse(d, raw)
		if !msg.IsZero() {
			errs[key] = msg
			continue
		}
		enc := encode(d, val)
		if bytes.Equal(enc, encode(d, d.Default)) {
			delete(next, key)
		} else {
			next[key] = enc
		}
	}
	if len(errs) > 0 {
		return nil, invalid(errs)
	}
	v, norm, _, errs := build(next)
	if len(errs) > 0 {
		return nil, invalid(errs)
	}
	old := s.cur.Load()
	p := &Pending{values: v, Overrides: norm}
	for i := range Defs {
		d := &Defs[i]
		o, n := Format(d, d.get(old)), Format(d, d.get(v))
		if o != n {
			p.Changes = append(p.Changes, Change{Key: d.Key, Old: o, New: n, Applies: d.Applies})
		}
	}
	return p, nil
}

// Commit makes pending values visible. Call it only after they were saved.
func (s *Store) Commit(p *Pending) {
	s.mu.Lock()
	s.overrides = p.Overrides
	s.cur.Store(p.values)
	listeners := append([]func(*Values){}, s.listeners...)
	s.mu.Unlock()
	for _, fn := range listeners {
		fn(p.values)
	}
}

func invalid(errs map[string]apierr.Msg) error {
	return apierr.Validation(apierr.M("settings_invalid", "invalid setting values"), errs)
}

// build turns overrides into a full snapshot. It returns normalized
// overrides (defaults dropped), unknown keys and per-key errors.
func build(overrides map[string]json.RawMessage) (*Values, map[string]json.RawMessage, []string, map[string]apierr.Msg) {
	v := Defaults()
	norm := make(map[string]json.RawMessage, len(overrides))
	var unknown []string
	errs := map[string]apierr.Msg{}
	for key, raw := range overrides {
		d, ok := byKey[key]
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		val, msg := parse(d, raw)
		if !msg.IsZero() {
			errs[key] = msg
			continue
		}
		d.set(v, val)
		enc := encode(d, val)
		if !bytes.Equal(enc, encode(d, d.Default)) {
			norm[key] = enc
		}
	}
	if len(errs) == 0 {
		errs = crossCheck(v)
	}
	sort.Strings(unknown)
	return v, norm, unknown, errs
}

var (
	errDuration = apierr.M("setting_duration", `expected a duration such as "20s" or "2m"`)
	errInteger  = apierr.M("setting_integer", "expected an integer")
	errString   = apierr.M("setting_string", "expected a string")
	errSpaces   = apierr.M("setting_spaces", "spaces are not allowed")
	errPattern  = apierr.M("setting_pattern", "contains characters that are not allowed")
)

// outOfRange names the limits in the canonical form of the value (the UI
// formats them with the setting metadata).
func outOfRange(d *Def) apierr.Msg {
	switch d.Type {
	case TypeEnum:
		return apierr.M("setting_option", "must be one of {options}", "options", strings.Join(d.Options, ", "))
	case TypeString:
		return apierr.M("setting_length", "must be {min}–{max} characters", "min", d.MinLen, "max", d.MaxLen)
	}
	return apierr.M("setting_range", "must be from {min} to {max}", "min", Format(d, d.Min), "max", Format(d, d.Max))
}

// parse validates a raw value; a zero Msg means it is valid.
func parse(d *Def, raw json.RawMessage) (any, apierr.Msg) {
	switch d.Type {
	case TypeDuration:
		var x any
		if err := json.Unmarshal(raw, &x); err != nil {
			return nil, errDuration
		}
		var v time.Duration
		switch t := x.(type) {
		case string:
			var err error
			if v, err = time.ParseDuration(strings.TrimSpace(t)); err != nil {
				return nil, errDuration
			}
		case float64: // plain number means seconds
			v = time.Duration(t * float64(time.Second))
		default:
			return nil, errDuration
		}
		if v < d.Min.(time.Duration) || v > d.Max.(time.Duration) {
			return nil, outOfRange(d)
		}
		return v, apierr.Msg{}
	case TypeInt:
		var f float64
		if err := json.Unmarshal(raw, &f); err != nil || f != float64(int64(f)) {
			return nil, errInteger
		}
		v := int(f)
		if v < d.Min.(int) || v > d.Max.(int) {
			return nil, outOfRange(d)
		}
		return v, apierr.Msg{}
	case TypeEnum:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, errString
		}
		for _, o := range d.Options {
			if o == v {
				return v, apierr.Msg{}
			}
		}
		return nil, outOfRange(d)
	case TypeString:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, errString
		}
		v = strings.TrimSpace(v)
		if msg := checkString(d, v); !msg.IsZero() {
			return nil, msg
		}
		return v, apierr.Msg{}
	}
	return nil, apierr.M("setting_type", "unknown setting type")
}

func checkString(d *Def, v string) apierr.Msg {
	n := utf8.RuneCountInString(v)
	if n < d.MinLen || n > d.MaxLen {
		return outOfRange(d)
	}
	if strings.IndexFunc(v, unicode.IsSpace) >= 0 {
		return errSpaces
	}
	if d.Pattern != nil && !d.Pattern.MatchString(v) {
		return errPattern
	}
	return apierr.Msg{}
}

// encode returns the canonical JSON form of a value, as stored in state.json
// and returned by the API.
func encode(d *Def, v any) json.RawMessage {
	if d.Type == TypeDuration {
		v = FormatDuration(v.(time.Duration))
	}
	b, _ := json.Marshal(v)
	return b
}

// FormatDuration prints durations the short way: "2m", "20s", "1h".
func FormatDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	}
	return d.String()
}

// Format is the human-readable value used in events.
func Format(d *Def, v any) string {
	switch x := v.(type) {
	case time.Duration:
		return FormatDuration(x)
	case int:
		if d.Unit != "" {
			return fmt.Sprintf("%d %s", x, d.Unit)
		}
		return fmt.Sprint(x)
	}
	return fmt.Sprint(v)
}

// Meta is one setting as returned by GET /api/settings. Titles and
// descriptions are not here: the web UI translates them by Key.
type Meta struct {
	Key      string          `json:"key"`
	Group    string          `json:"group"`
	Type     Type            `json:"type"`
	Unit     string          `json:"unit,omitempty"`
	Value    json.RawMessage `json:"value"`
	Default  json.RawMessage `json:"default"`
	Min      json.RawMessage `json:"min,omitempty"`
	Max      json.RawMessage `json:"max,omitempty"`
	Options  []string        `json:"options,omitempty"`
	MinLen   int             `json:"min_len,omitempty"`
	MaxLen   int             `json:"max_len,omitempty"`
	Applies  Applies         `json:"applies"`
	Modified bool            `json:"modified"`
}

// Describe returns every setting with its current value and metadata.
func (s *Store) Describe() []Meta {
	v := s.Get()
	out := make([]Meta, 0, len(Defs))
	for i := range Defs {
		d := &Defs[i]
		m := Meta{
			Key: d.Key, Group: d.Group, Type: d.Type, Unit: d.Unit, Options: d.Options,
			MinLen: d.MinLen, MaxLen: d.MaxLen,
			Value: encode(d, d.get(v)), Default: encode(d, d.Default), Applies: d.Applies,
		}
		if d.Min != nil {
			m.Min = encode(d, d.Min)
			m.Max = encode(d, d.Max)
		}
		m.Modified = !bytes.Equal(m.Value, m.Default)
		out = append(out, m)
	}
	return out
}

func isNull(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null"
}

func copyMap(m map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
