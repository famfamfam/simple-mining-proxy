package settings

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestDefaultsAreValid(t *testing.T) {
	for _, d := range Defs {
		if _, msg := parse(&d, encode(&d, d.Default)); !msg.IsZero() {
			t.Errorf("%s: default is invalid: %s", d.Key, msg)
		}
		if d.Group == "" || d.Applies == "" {
			t.Errorf("%s: missing metadata", d.Key)
		}
	}
	if errs := crossCheck(Defaults()); len(errs) > 0 {
		t.Errorf("defaults violate constraints: %v", errs)
	}
}

func TestPrepareAndCommit(t *testing.T) {
	s, _, err := NewStore(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Prepare(map[string]json.RawMessage{
		"switch_drain":    raw(`"20s"`),
		"max_conn_per_ip": raw(`0`), // equals default: not stored
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().SwitchDrain != 10*time.Second {
		t.Fatal("Prepare must not change current values")
	}
	s.Commit(p)
	v := s.Get()
	if v.SwitchDrain != 20*time.Second {
		t.Fatalf("unexpected values: %+v", v)
	}
	ov := s.Overrides()
	if len(ov) != 1 || string(ov["switch_drain"]) != `"20s"` {
		t.Fatalf("overrides = %v", ov)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("changes = %+v", p.Changes)
	}

	// null resets to default
	p, err = s.Prepare(map[string]json.RawMessage{"switch_drain": raw(`null`)})
	if err != nil {
		t.Fatal(err)
	}
	s.Commit(p)
	if s.Get().SwitchDrain != 10*time.Second {
		t.Fatal("reset failed")
	}
}

func TestValidation(t *testing.T) {
	s, _, _ := NewStore(nil)
	bad := map[string]string{
		"switch_drain":           `"301s"`,
		"failback_delay":         `"10s"`,
		"max_connections":        `5.5`,
		"tls_min_version":        `"0.9"`,
		"test_worker":            `"bad name"`,
		"rewrite_login_prefixes": `["farm."]`,
		"no_such_setting":        `1`,
		"upstream_dial_timeout":  `"abc"`,
	}
	for k, v := range bad {
		_, err := s.Prepare(map[string]json.RawMessage{k: raw(v)})
		var e *apierr.Error
		if !errors.As(err, &e) || e.Fields[k].IsZero() {
			t.Errorf("%s=%s: expected field error, got %v", k, v, err)
		}
	}
	// cross constraint: budget below dial timeout
	_, err := s.Prepare(map[string]json.RawMessage{"upstream_dial_timeout": raw(`"20s"`), "upstream_connect_budget": raw(`"10s"`)})
	var e *apierr.Error
	if !errors.As(err, &e) || e.Fields["upstream_connect_budget"].IsZero() {
		t.Errorf("expected cross-check error, got %v", err)
	}
	// cross constraint: the hourly history tiers must outlive the detailed one
	for _, key := range []string{"history_retention", "history_miner_retention"} {
		_, err = s.Prepare(map[string]json.RawMessage{"history_detail_retention": raw(`"720h"`), key: raw(`"168h"`)})
		if !errors.As(err, &e) || e.Fields[key].Key != "setting_history_below_detail" {
			t.Errorf("%s below history_detail_retention: expected cross-check error, got %v", key, err)
		}
	}
	if _, err = s.Prepare(map[string]json.RawMessage{"history_detail_retention": raw(`"720h"`), "history_miner_retention": raw(`"720h"`)}); err != nil {
		t.Errorf("equal retentions rejected: %v", err)
	}
	// cross constraint: the timed window is shorter than its period
	_, err = s.Prepare(map[string]json.RawMessage{"timed_period": raw(`"30m"`), "timed_duration": raw(`"30m"`)})
	if !errors.As(err, &e) || e.Fields["timed_duration"].Key != "setting_timed_duration" {
		t.Errorf("timed_duration = timed_period: expected cross-check error, got %v", err)
	}
	// one bad value rejects the whole request
	_, err = s.Prepare(map[string]json.RawMessage{"switch_drain": raw(`"20s"`), "log_level": raw(`"loud"`)})
	if err == nil {
		t.Error("expected error")
	}
}

func TestNewStoreRejectsInvalidSaved(t *testing.T) {
	if _, _, err := NewStore(map[string]json.RawMessage{"switch_drain": raw(`"1h"`)}); err == nil {
		t.Fatal("expected error for out-of-range saved value")
	}
	_, unknown, err := NewStore(map[string]json.RawMessage{"legacy": raw(`1`)})
	if err != nil || len(unknown) != 1 {
		t.Fatalf("unknown keys must be ignored with a warning: %v %v", unknown, err)
	}
}
