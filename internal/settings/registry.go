// Package settings is the registry of runtime settings that the operator
// edits in the admin UI. Every setting is described here exactly once: type,
// default, limits and when a change takes effect. The API serves this
// metadata; titles and descriptions are translated by the web UI by key.
package settings

import (
	"regexp"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/apierr"
)

type Type string

const (
	TypeDuration Type = "duration"
	TypeInt      Type = "int"
	TypeEnum     Type = "enum"
	TypeString   Type = "string"
)

// Applies tells when a change takes effect.
type Applies string

const (
	AppliesImmediately    Applies = "immediately"
	AppliesNewConnections Applies = "new_connections"
	AppliesNextDrain      Applies = "next_drain"
)

// Groups lists the setting groups in display order.
var Groups = []string{"logins", "switching", "profit", "timed", "hunt", "alerts", "timeouts", "limits", "tls", "history", "logging"}

// Values is an immutable snapshot of all runtime settings.
type Values struct {
	TestWorker            string
	SwitchDrain           time.Duration
	FailbackDelay         time.Duration
	PoolProbeInterval     time.Duration
	TLSHandshakeTimeout   time.Duration
	FirstMessageTimeout   time.Duration
	UpstreamDialTimeout   time.Duration
	UpstreamConnectBudget time.Duration
	MinerIdleTimeout      time.Duration
	UpstreamIdleTimeout   time.Duration
	MaxConnections        int
	MaxPendingConnections int
	MaxConnPerIP          int
	MaxLineKiB            int
	TLSMinVersion         string
	HistoryDetail         time.Duration
	HistoryRetention      time.Duration
	HistoryMiners         time.Duration
	ProfitSwitch          string
	ProfitInterval        time.Duration
	ProfitMargin          int
	TimedSwitch           string
	TimedPeriod           time.Duration
	TimedDuration         time.Duration
	HuntSwitch            string
	OfflineAfter          time.Duration
	TelegramChats         string
	TelegramLanguage      string
	LogLevel              string
}

// Profit switching modes.
const (
	ProfitOff    = "off"
	ProfitAdvise = "advise" // compute and report, never switch
	ProfitAuto   = "auto"
)

// Timed switching modes.
const (
	TimedOff = "off"
	TimedOn  = "on"
)

// Block hunting modes.
const (
	HuntOff = "off"
	HuntOn  = "on"
)

func (v *Values) MaxLineBytes() int { return v.MaxLineKiB * 1024 }

// Def describes one setting.
type Def struct {
	Key   string
	Group string
	Type  Type
	// Unit is a display hint: "KiB" for sizes, "" otherwise. Durations are
	// shown by the UI as a number with seconds or minutes.
	Unit    string
	Default any
	// Min/Max are time.Duration for durations and int for ints.
	Min, Max any
	Options  []string
	// Limits for string values; Pattern is described to the operator by the
	// UI text of the setting.
	MinLen  int
	MaxLen  int
	Pattern *regexp.Regexp
	Applies Applies
	get     func(*Values) any
	set     func(*Values, any)
}

var (
	workerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	// Telegram chat ids, comma-separated; group chats have negative ids.
	chatsPattern = regexp.MustCompile(`^(-?[0-9]{1,20}(,-?[0-9]{1,20})*)?$`)
)

type binder func(d *Def)

func dur(p func(*Values) *time.Duration) binder {
	return func(d *Def) {
		d.get = func(v *Values) any { return *p(v) }
		d.set = func(v *Values, x any) { *p(v) = x.(time.Duration) }
	}
}

func num(p func(*Values) *int) binder {
	return func(d *Def) {
		d.get = func(v *Values) any { return *p(v) }
		d.set = func(v *Values, x any) { *p(v) = x.(int) }
	}
}

func str(p func(*Values) *string) binder {
	return func(d *Def) {
		d.get = func(v *Values) any { return *p(v) }
		d.set = func(v *Values, x any) { *p(v) = x.(string) }
	}
}

func def(d Def, b binder) Def {
	b(&d)
	return d
}

const (
	sec    = time.Second
	minute = time.Minute
	day    = 24 * time.Hour
)

// Defs lists all settings in display order.
var Defs = []Def{
	// Worker name the pool check (Test button, check before a switch)
	// authorizes with.
	def(Def{
		Key: "test_worker", Group: "logins", Type: TypeString,
		Default: "proxytest", MinLen: 1, MaxLen: 32, Pattern: workerPattern,
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.TestWorker })),

	// Time over which a switch, failback or "reconnect all" spreads the
	// disconnects.
	def(Def{
		Key: "switch_drain", Group: "switching", Type: TypeDuration,
		Default: 10 * sec, Min: time.Duration(0), Max: 300 * sec,
		Applies: AppliesNextDrain,
	}, dur(func(v *Values) *time.Duration { return &v.SwitchDrain })),

	// How long the active pool must stay UP before sessions fail back.
	def(Def{
		Key: "failback_delay", Group: "switching", Type: TypeDuration,
		Default: 2 * minute, Min: 30 * sec, Max: 60 * minute,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.FailbackDelay })),

	// Health probe period of every pool address.
	def(Def{
		Key: "pool_probe_interval", Group: "switching", Type: TypeDuration,
		Default: 30 * sec, Min: 10 * sec, Max: 10 * minute,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.PoolProbeInterval })),

	// Profit switching between coins: how often to compare, and
	// how much more another coin must earn before switching to it.
	def(Def{
		Key: "profit_switch", Group: "profit", Type: TypeEnum,
		Default: ProfitOff, Options: []string{ProfitOff, ProfitAdvise, ProfitAuto},
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.ProfitSwitch })),

	def(Def{
		Key: "profit_interval", Group: "profit", Type: TypeDuration,
		Default: 24 * time.Hour, Min: 1 * time.Hour, Max: 7 * day,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.ProfitInterval })),

	def(Def{
		Key: "profit_margin", Group: "profit", Type: TypeInt, Unit: "%",
		Default: 5, Min: 0, Max: 50,
		Applies: AppliesImmediately,
	}, num(func(v *Values) *int { return &v.ProfitMargin })),

	// Timed switching: for timed_duration at the start of every timed_period
	// the farm mines on the pool marked in the pool editor (e.g. a solo
	// pool), then returns.
	def(Def{
		Key: "timed_switch", Group: "timed", Type: TypeEnum,
		Default: TimedOff, Options: []string{TimedOff, TimedOn},
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.TimedSwitch })),

	def(Def{
		Key: "timed_period", Group: "timed", Type: TypeDuration,
		Default: 30 * minute, Min: 10 * minute, Max: 24 * time.Hour,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.TimedPeriod })),

	def(Def{
		Key: "timed_duration", Group: "timed", Type: TypeDuration,
		Default: 10 * minute, Min: 1 * minute, Max: 12 * time.Hour,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.TimedDuration })),

	// Block hunting on eCash: the farm goes to the eCash solo pool while a
	// block is not harder than its header says, and comes back as soon as a
	// block arrives. See the timed package.
	def(Def{
		Key: "hunt_switch", Group: "hunt", Type: TypeEnum,
		Default: HuntOff, Options: []string{HuntOff, HuntOn},
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.HuntSwitch })),

	// An ASIC that has sent no shares for this long is offline. ASICs that
	// share rarely get a longer wait, see the monitor package.
	def(Def{
		Key: "offline_after", Group: "alerts", Type: TypeDuration,
		Default: 1 * minute, Min: 30 * sec, Max: 60 * minute,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.OfflineAfter })),

	// Chats that get the alerts and may use the bot commands. The bot token
	// is a secret, kept apart in state.json (PUT /api/telegram/token).
	def(Def{
		Key: "telegram_chats", Group: "alerts", Type: TypeString,
		Default: "", MinLen: 0, MaxLen: 200, Pattern: chatsPattern,
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.TelegramChats })),

	def(Def{
		Key: "telegram_language", Group: "alerts", Type: TypeEnum,
		Default: "en", Options: []string{"en", "ru"},
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.TelegramLanguage })),

	def(Def{
		Key: "tls_handshake_timeout", Group: "timeouts", Type: TypeDuration,
		Default: 10 * sec, Min: 1 * sec, Max: 60 * sec,
		Applies: AppliesNewConnections,
	}, dur(func(v *Values) *time.Duration { return &v.TLSHandshakeTimeout })),

	def(Def{
		Key: "first_message_timeout", Group: "timeouts", Type: TypeDuration,
		Default: 15 * sec, Min: 5 * sec, Max: 120 * sec,
		Applies: AppliesNewConnections,
	}, dur(func(v *Values) *time.Duration { return &v.FirstMessageTimeout })),

	// Connect (and TLS handshake) timeout of one pool address.
	def(Def{
		Key: "upstream_dial_timeout", Group: "timeouts", Type: TypeDuration,
		Default: 5 * sec, Min: 1 * sec, Max: 30 * sec,
		Applies: AppliesNewConnections,
	}, dur(func(v *Values) *time.Duration { return &v.UpstreamDialTimeout })),

	// Total time a session may spend trying addresses and pools.
	def(Def{
		Key: "upstream_connect_budget", Group: "timeouts", Type: TypeDuration,
		Default: 15 * sec, Min: 5 * sec, Max: 120 * sec,
		Applies: AppliesNewConnections,
	}, dur(func(v *Values) *time.Duration { return &v.UpstreamConnectBudget })),

	def(Def{
		Key: "miner_idle_timeout", Group: "timeouts", Type: TypeDuration,
		Default: 10 * minute, Min: 1 * minute, Max: 60 * minute,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.MinerIdleTimeout })),

	def(Def{
		Key: "upstream_idle_timeout", Group: "timeouts", Type: TypeDuration,
		Default: 15 * minute, Min: 1 * minute, Max: 60 * minute,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.UpstreamIdleTimeout })),

	def(Def{
		Key: "max_connections", Group: "limits", Type: TypeInt,
		Default: 5000, Min: 10, Max: 100000,
		Applies: AppliesNewConnections,
	}, num(func(v *Values) *int { return &v.MaxConnections })),

	def(Def{
		Key: "max_pending_connections", Group: "limits", Type: TypeInt,
		Default: 500, Min: 10, Max: 10000,
		Applies: AppliesNewConnections,
	}, num(func(v *Values) *int { return &v.MaxPendingConnections })),

	// 0 turns the per-IP limit off: a farm usually sits behind one NAT IP.
	def(Def{
		Key: "max_conn_per_ip", Group: "limits", Type: TypeInt,
		Default: 0, Min: 0, Max: 100000,
		Applies: AppliesNewConnections,
	}, num(func(v *Values) *int { return &v.MaxConnPerIP })),

	def(Def{
		Key: "max_line_bytes", Group: "limits", Type: TypeInt, Unit: "KiB",
		Default: 64, Min: 4, Max: 1024,
		Applies: AppliesNewConnections,
	}, num(func(v *Values) *int { return &v.MaxLineKiB })),

	def(Def{
		Key: "tls_min_version", Group: "tls", Type: TypeEnum,
		Default: "1.2", Options: []string{"1.0", "1.1", "1.2", "1.3"},
		Applies: AppliesNewConnections,
	}, str(func(v *Values) *string { return &v.TLSMinVersion })),

	// Statistics history for the charts: per-minute points are kept this
	// long, hourly roll-ups for history_retention.
	def(Def{
		Key: "history_detail_retention", Group: "history", Type: TypeDuration,
		Default: 7 * day, Min: 1 * day, Max: 90 * day,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.HistoryDetail })),

	def(Def{
		Key: "history_retention", Group: "history", Type: TypeDuration,
		Default: 365 * day, Min: 7 * day, Max: 3650 * day,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.HistoryRetention })),

	// Hourly history of every ASIC login; it grows with the farm, so it has
	// its own, shorter, retention.
	def(Def{
		Key: "history_miner_retention", Group: "history", Type: TypeDuration,
		Default: 90 * day, Min: 7 * day, Max: 3650 * day,
		Applies: AppliesImmediately,
	}, dur(func(v *Values) *time.Duration { return &v.HistoryMiners })),

	def(Def{
		Key: "log_level", Group: "logging", Type: TypeEnum,
		Default: "info", Options: []string{"debug", "info", "warn", "error"},
		Applies: AppliesImmediately,
	}, str(func(v *Values) *string { return &v.LogLevel })),
}

var byKey = func() map[string]*Def {
	m := make(map[string]*Def, len(Defs))
	for i := range Defs {
		m[Defs[i].Key] = &Defs[i]
	}
	return m
}()

// Lookup returns the definition of key.
func Lookup(key string) (*Def, bool) {
	d, ok := byKey[key]
	return d, ok
}

// Defaults returns a snapshot with every setting at its default.
func Defaults() *Values {
	v := &Values{}
	for i := range Defs {
		Defs[i].set(v, Defs[i].Default)
	}
	return v
}

// crossCheck validates constraints between settings.
func crossCheck(v *Values) map[string]apierr.Msg {
	errs := map[string]apierr.Msg{}
	if v.UpstreamConnectBudget < v.UpstreamDialTimeout {
		errs["upstream_connect_budget"] = apierr.M("setting_budget_below_dial",
			"must not be less than upstream_dial_timeout ({dial})", "dial", FormatDuration(v.UpstreamDialTimeout))
	}
	if v.MaxPendingConnections > v.MaxConnections {
		errs["max_pending_connections"] = apierr.M("setting_pending_above_total",
			"must not be more than max_connections")
	}
	if v.TimedDuration >= v.TimedPeriod {
		errs["timed_duration"] = apierr.M("setting_timed_duration",
			"must be shorter than timed_period ({period})", "period", FormatDuration(v.TimedPeriod))
	}
	// The hourly tiers must outlive the detailed ones: ranges longer than
	// three days are read from the hourly points only.
	for key, keep := range map[string]time.Duration{
		"history_retention":       v.HistoryRetention,
		"history_miner_retention": v.HistoryMiners,
	} {
		if keep < v.HistoryDetail {
			errs[key] = apierr.M("setting_history_below_detail",
				"must not be less than history_detail_retention ({detail})", "detail", FormatDuration(v.HistoryDetail))
		}
	}
	return errs
}
