// Package telegram sends the monitor's alerts to Telegram chats and answers
// the bot commands. It talks to the Bot API with long polling, so the proxy
// needs neither a public address nor a webhook.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/monitor"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

// APIURL is the Bot API server.
const APIURL = "https://api.telegram.org"

const (
	// pollWait is how long getUpdates waits for a message on the server.
	pollWait = 50 * time.Second
	// batchDelay: alerts that come within this time go out as one message,
	// so a rack losing power is one message, not one per ASIC.
	batchDelay = 10 * time.Second
	// minGap: alert messages go out at most this often; what happens in
	// between waits for the next one.
	minGap = time.Minute
	// An ASIC reported offline and back flapChanges times within flapWindow
	// is flapping: its next changes wait flapHold after the last message
	// about it, and only its state then is reported.
	flapChanges = 2
	flapWindow  = time.Hour
	flapHold    = 15 * time.Minute
	// sendTick is how often due alerts are looked for.
	sendTick = time.Second
	// maxText keeps messages under Telegram's 4096 characters.
	maxText = 4000
	// retryMin and retryMax bound the pause after a failed request.
	retryMin = 5 * time.Second
	retryMax = 5 * time.Minute
	// deniedEvery: an unknown chat gets its id at most this often.
	deniedEvery = time.Minute
	// maxRetryAfter: a longer rate-limit wait drops the message instead.
	maxRetryAfter = time.Minute
)

// Farm is the part of /status that the monitor does not know.
type Farm struct {
	Hashrate float64 // H/s, 10-minute estimate
	Pool     string  // name of the pool new sessions go to, "" when none
	Coin     string
	Solo     bool   // that pool is a solo pool
	Mode     string // pool.Manager.Mode
	Timer    *Timer // while the timer holds the farm on its pool
	Hunt     *Hunt  // while block hunting on eCash is on
	Accepted uint64 // shares since start
	Rejected uint64
	Uptime   time.Duration
}

// Timer is where timed switching holds the farm now.
type Timer struct {
	Pool string
	Left time.Duration
}

// Hunt is block hunting on eCash.
type Hunt struct {
	Pool     string        // the eCash solo pool
	For      time.Duration // how long the farm has been there; 0 when it is not
	Hardness float64       // how many times harder than its header a block is now
	Live     bool          // eCash blocks are seen as they arrive
	Stints   int           // hunts in the last 24 hours
	Time     time.Duration // time hunting in the last 24 hours
}

type Deps struct {
	Token    string // at start, from state.json; SetToken changes it; "" keeps the bot quiet
	APIURL   string // APIURL when empty
	Client   *http.Client
	Settings *settings.Store
	Events   *events.Log
	Miners   func() []monitor.Miner
	Farm     func() Farm
}

// Status is what the admin UI shows about the bot.
type Status struct {
	Configured bool   `json:"configured"` // a bot token is set
	Username   string `json:"username,omitempty"`
	Error      string `json:"error,omitempty"` // of the last polling and of the last sending, "" after a success
}

// What a failure is about: a success clears only a failure of its kind, so
// a message that no chat got is not hidden by the next successful poll.
const (
	polling = iota
	sending
)

type Bot struct {
	d      Deps
	api    string // Bot API server, without the trailing slash
	alerts chan monitor.Alert
	batch  time.Duration
	gap    time.Duration
	tick   time.Duration
	now    func() time.Time

	mu       sync.Mutex
	token    string
	changed  chan struct{}       // closed and replaced when the token changes
	username string              // of the bot behind token, "" until known
	menu     string              // language the command menu was set in
	errs     [2]string           // last failure by kind: polling, sending
	denied   map[int64]time.Time // unknown chats answered recently
}

func New(d Deps) *Bot {
	api := d.APIURL
	if api == "" {
		api = APIURL
	}
	if d.Client == nil {
		d.Client = &http.Client{}
	}
	return &Bot{
		d: d, api: strings.TrimRight(api, "/"), token: d.Token, changed: make(chan struct{}),
		alerts: make(chan monitor.Alert, 64), batch: batchDelay, gap: minGap, tick: sendTick, now: time.Now,
		denied: map[int64]time.Time{},
	}
}

var tokenPattern = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{20,100}$`)

// ValidToken reports whether s looks like a token from @BotFather.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// Check asks Telegram who the bot behind token is. See IsRejected for the
// kinds of errors.
func (b *Bot) Check(ctx context.Context, token string) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := b.callWith(ctx, token, "getMe", struct{}{}, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// IsRejected reports whether err is Telegram's answer (a wrong token, for
// example) rather than a failure to reach it.
func IsRejected(err error) bool {
	var ae *apiError
	return errors.As(err, &ae)
}

// SetToken switches the bot to another token at once; "" stops it. username
// is the bot name when the caller already knows it (from Check).
func (b *Bot) SetToken(token, username string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if token == b.token {
		return
	}
	b.token, b.username, b.menu, b.errs = token, username, "", [2]string{}
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *Bot) current() (token string, changed <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token, b.changed
}

// ChatIDs parses the telegram_chats setting.
func ChatIDs(s string) []int64 {
	var out []int64
	for _, f := range strings.Split(s, ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(f), 10, 64); err == nil {
			out = append(out, id)
		}
	}
	return out
}

// Notify queues an alert. It never blocks the monitor: when the queue is
// full, the alert is dropped (the events still have it).
func (b *Bot) Notify(a monitor.Alert) {
	select {
	case b.alerts <- a:
	default:
		b.d.Events.Throttled("telegram_queue", 10*time.Minute, "telegram_error", "Telegram: alert queue is full, an alert was dropped")
	}
}

// Status is safe on a nil Bot: no token, no bot.
func (b *Bot) Status() Status {
	if b == nil {
		return Status{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var errs []string
	for _, e := range b.errs {
		if e != "" {
			errs = append(errs, e)
		}
	}
	return Status{Configured: b.token != "", Username: b.username, Error: strings.Join(errs, "; ")}
}

var (
	// ErrNoToken is returned by Test when no bot token is set.
	ErrNoToken = errors.New("no Telegram bot token")
	// ErrNoChats is returned by Test when telegram_chats is empty.
	ErrNoChats = errors.New("no Telegram chats configured")
)

// Test sends a test message to every chat.
func (b *Bot) Test(ctx context.Context) error {
	if token, _ := b.current(); token == "" {
		return ErrNoToken
	}
	v := b.d.Settings.Get()
	chats := ChatIDs(v.TelegramChats)
	if len(chats) == 0 {
		return ErrNoChats
	}
	if err := errors.Join(b.broadcast(ctx, chats, textsFor(v.TelegramLanguage).test)...); err != nil {
		b.fail(sending, err)
		return errors.New(b.redact(err.Error()))
	}
	b.ok(sending)
	return nil
}

// Run answers commands and sends alerts until ctx is done.
func (b *Bot) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() { b.sendLoop(ctx); close(done) }()
	b.pollLoop(ctx)
	<-done
}

// ---- Bot API ----

type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Method, e.Code, e.Description)
}

// call calls method as the bot with the current token.
func (b *Bot) call(ctx context.Context, method string, in, out any) error {
	token, _ := b.current()
	if token == "" {
		return ErrNoToken
	}
	return b.callWith(ctx, token, method, in, out)
}

func (b *Bot) callWith(ctx context.Context, token, method string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%s: bad API URL", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.d.Client.Do(req)
	if err != nil {
		// The URL holds the token: keep it out of errors and logs.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&r); err != nil {
		return fmt.Errorf("%s: HTTP %s", method, resp.Status)
	}
	if !r.OK {
		code := r.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &apiError{Method: method, Code: code, Description: r.Description,
			RetryAfter: time.Duration(r.Parameters.RetryAfter) * time.Second}
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// send sends an HTML message, waiting out Telegram's rate limit.
func (b *Bot) send(ctx context.Context, chat int64, text string) error {
	for attempt := 0; ; attempt++ {
		err := b.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "text": text, "parse_mode": "HTML"}, nil)
		var ae *apiError
		if !errors.As(err, &ae) || ae.RetryAfter <= 0 || ae.RetryAfter > maxRetryAfter || attempt >= 2 {
			return err
		}
		if !sleep(ctx, ae.RetryAfter) {
			return ctx.Err()
		}
	}
}

// broadcast sends text to every chat at once and returns the failures, if
// any. Chats are independent: a slow or rate-limited one never delays or
// starves the others, unlike a plain loop over b.send.
func (b *Bot) broadcast(ctx context.Context, chats []int64, text string) []error {
	if len(chats) == 0 {
		return nil
	}
	errc := make(chan error, len(chats))
	var wg sync.WaitGroup
	for _, id := range chats {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if err := b.send(ctx, id, text); err != nil {
				errc <- fmt.Errorf("chat %d: %w", id, err)
			}
		}(id)
	}
	wg.Wait()
	close(errc)
	var errs []error
	for err := range errc {
		errs = append(errs, err)
	}
	return errs
}

// redact is a second guard: errors are built without the request URL, so
// the token should never be in them.
func (b *Bot) redact(s string) string {
	token, _ := b.current()
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "<token>")
}

func (b *Bot) fail(kind int, err error) {
	token, _ := b.current()
	b.failAs(token, kind, err)
}

// failAs records a failure of the bot behind token.
func (b *Bot) failAs(token string, kind int, err error) {
	msg := b.redact(err.Error())
	b.update(token, func() { b.errs[kind] = msg })
	b.d.Events.Throttled(fmt.Sprintf("telegram:%d", kind), 10*time.Minute, "telegram_error", "Telegram: %s", msg)
}

func (b *Bot) ok(kind int) {
	b.mu.Lock()
	b.errs[kind] = ""
	b.mu.Unlock()
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---- commands ----

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		Chat struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			Username string `json:"username"`
		} `json:"chat"`
		Text string `json:"text"`
	} `json:"message"`
}

// pollLoop polls with the current token; a new token starts over with the
// new bot, and without a token it waits for one.
func (b *Bot) pollLoop(ctx context.Context) {
	for ctx.Err() == nil {
		token, changed := b.current()
		if token == "" {
			select {
			case <-ctx.Done():
			case <-changed:
			}
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		go func() {
			select {
			case <-changed:
				cancel()
			case <-sctx.Done():
			}
		}()
		b.pollAs(sctx, token)
		cancel()
	}
}

// pollAs answers the messages to the bot behind token until ctx is done.
func (b *Bot) pollAs(ctx context.Context, token string) {
	delay := retryMin
	var offset int64
	for ctx.Err() == nil {
		updates, err := b.poll(ctx, token, offset)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.failAs(token, polling, err)
			if !sleep(ctx, delay) {
				return
			}
			delay = min(delay*2, retryMax)
			continue
		}
		delay = retryMin
		b.update(token, func() { b.errs[polling] = "" })
		for _, u := range updates {
			offset = u.UpdateID + 1
			b.handle(ctx, u)
		}
	}
}

// update changes the bot state under the lock, unless token is no longer
// the current one: a late answer to the old bot must not touch the new one.
func (b *Bot) update(token string, fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.token == token {
		fn()
	}
}

// poll learns the bot name and sets the command menu when needed, then
// waits for new messages.
func (b *Bot) poll(ctx context.Context, token string, offset int64) ([]update, error) {
	b.mu.Lock()
	known, menu := b.username != "", b.menu
	b.mu.Unlock()
	if !known {
		var me struct {
			Username string `json:"username"`
		}
		if err := b.callWith(ctx, token, "getMe", struct{}{}, &me); err != nil {
			return nil, err
		}
		b.update(token, func() { b.username = me.Username })
	}
	if lang := b.d.Settings.Get().TelegramLanguage; lang != menu {
		tx := textsFor(lang)
		var cmds []map[string]string
		for _, c := range []string{"status", "miners", "help"} {
			cmds = append(cmds, map[string]string{"command": c, "description": tx.cmdDesc[c]})
		}
		// The menu is a nice-to-have: a failure here must not stop getUpdates
		// below, or the bot would stop answering commands entirely just
		// because Telegram would not take the new menu.
		if err := b.callWith(ctx, token, "setMyCommands", map[string]any{"commands": cmds}, nil); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err() // stopping or another token: not a menu problem
			}
			b.d.Events.Throttled("telegram_menu", 10*time.Minute, "telegram_error", "Telegram: cannot set the command menu: %s", b.redact(err.Error()))
		} else {
			b.update(token, func() { b.menu = lang })
		}
	}
	var updates []update
	pctx, cancel := context.WithTimeout(ctx, pollWait+15*time.Second)
	defer cancel()
	err := b.callWith(pctx, token, "getUpdates", map[string]any{
		"offset": offset, "timeout": int(pollWait / time.Second), "allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

// command returns the command of text in lower case, "" for other text and
// for commands addressed to another bot ("/status@other_bot").
func command(text, username string) string {
	f := strings.Fields(text)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return ""
	}
	name, to, addressed := strings.Cut(f[0][1:], "@")
	if addressed && !strings.EqualFold(to, username) {
		return ""
	}
	return strings.ToLower(name)
}

func (b *Bot) handle(ctx context.Context, u update) {
	m := u.Message
	if m == nil {
		return
	}
	b.mu.Lock()
	username := b.username
	b.mu.Unlock()
	cmd := command(m.Text, username)
	if cmd == "" {
		return
	}
	v := b.d.Settings.Get()
	tx := textsFor(v.TelegramLanguage)
	id := m.Chat.ID
	allowed := slices.Contains(ChatIDs(v.TelegramChats), id)
	var text string
	switch {
	case !allowed:
		if !b.deny(id) {
			return
		}
		name := m.Chat.Title
		if name == "" {
			name = "@" + m.Chat.Username
		}
		b.d.Events.Throttled(fmt.Sprintf("telegram_denied:%d", id), 10*time.Minute, "telegram_denied",
			"Telegram: /%s from chat %d (%s) ignored: the chat is not in telegram_chats", cmd, id, name)
		text = fmt.Sprintf(tx.denied, id)
	case cmd == "status":
		text = b.summary(tx)
	case cmd == "miners":
		text = b.minersText(tx)
	default:
		text = tx.help
	}
	if err := b.send(ctx, id, text); err != nil && ctx.Err() == nil {
		b.fail(sending, fmt.Errorf("chat %d: %w", id, err))
	}
}

// deny reports whether an unknown chat should get its id now.
func (b *Bot) deny(id int64) bool {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if t, ok := b.denied[id]; ok && now.Sub(t) < deniedEvery {
		return false
	}
	if len(b.denied) >= 1000 {
		for k, t := range b.denied {
			if now.Sub(t) >= deniedEvery {
				delete(b.denied, k)
			}
		}
		if len(b.denied) >= 1000 {
			return false
		}
	}
	b.denied[id] = now
	return true
}

// ---- messages ----

// message builds a Telegram message that stays under maxText.
type message struct {
	sb strings.Builder
}

func (m *message) line(s string) {
	if m.sb.Len() > 0 {
		m.sb.WriteByte('\n')
	}
	m.sb.WriteString(s)
}

// list adds items while they fit, then "… and N more". It leaves room for
// a few short lines after the list.
func (m *message) list(items []string, more string) {
	const reserve = 200
	for i, it := range items {
		if m.sb.Len()+len(it)+reserve > maxText {
			m.line(fmt.Sprintf(more, len(items)-i))
			return
		}
		m.line(it)
	}
}

func (m *message) String() string { return m.sb.String() }

func esc(s string) string { return html.EscapeString(s) }

func (b *Bot) sendLoop(ctx context.Context) {
	n := newNotifier(b.batch, b.gap)
	t := time.NewTicker(b.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-b.alerts:
			n.add(a, b.now())
		case <-t.C:
		}
		now := b.now()
		due := n.take(now)
		if len(due) == 0 {
			continue
		}
		v := b.d.Settings.Get()
		text := alertText(textsFor(v.TelegramLanguage), due, n.online, n.total, now)
		chats := ChatIDs(v.TelegramChats)
		if token, _ := b.current(); token == "" {
			chats = nil
		}
		errs := b.broadcast(ctx, chats, text)
		if ctx.Err() != nil {
			return
		}
		delivered := len(chats) == 0 || len(errs) < len(chats) // no bot/chats, or at least one got it
		if len(errs) > 0 {
			b.fail(sending, errors.Join(errs...))
		} else if len(chats) > 0 {
			b.ok(sending)
		}
		if delivered {
			n.sent(due, now)
		} else {
			n.failed(now) // try again after the gap
		}
	}
}

func alertText(tx *texts, changes []monitor.Change, online, total int, now time.Time) string {
	var off, on []string
	for _, c := range changes {
		if c.Online {
			on = append(on, fmt.Sprintf(tx.onlineLine, esc(c.Name), tx.duration(c.Away)))
		} else {
			off = append(off, fmt.Sprintf(tx.offlineLine, esc(c.Name), tx.duration(now.Sub(c.LastShare)), esc(c.Pool)))
		}
	}
	var m message
	if len(off) > 0 {
		m.line(fmt.Sprintf(tx.offline, len(off)))
		m.list(off, tx.more)
	}
	if len(on) > 0 {
		if len(off) > 0 {
			m.line("")
		}
		m.line(fmt.Sprintf(tx.online, len(on)))
		m.list(on, tx.more)
	}
	m.line("")
	m.line(fmt.Sprintf(tx.mining, online, total))
	return m.String()
}

func (b *Bot) summary(tx *texts) string {
	f := b.d.Farm()
	miners := b.d.Miners()
	now := b.now()
	online := 0
	var silent []string
	for _, mn := range miners {
		if mn.Online {
			online++
		} else {
			silent = append(silent, fmt.Sprintf(tx.offlineLine, esc(mn.Name), tx.duration(now.Sub(mn.LastShare)), esc(mn.Pool)))
		}
	}
	var m message
	m.line(tx.summary)
	m.line(fmt.Sprintf(tx.hashrate, hashrate(f.Hashrate)))
	m.line(fmt.Sprintf(tx.asics, online, len(miners)))
	pool := tx.noPool
	if f.Pool != "" {
		pool = esc(f.Pool)
		var about []string
		if f.Coin != "" {
			about = append(about, esc(f.Coin))
		}
		if f.Solo {
			about = append(about, tx.solo)
		}
		if len(about) > 0 {
			pool += " (" + strings.Join(about, ", ") + ")"
		}
	}
	mode := tx.modes[f.Mode]
	if mode == "" {
		mode = esc(f.Mode)
	}
	m.line(fmt.Sprintf(tx.pool, pool, mode))
	if f.Timer != nil {
		m.line(fmt.Sprintf(tx.timer, esc(f.Timer.Pool), tx.duration(f.Timer.Left)))
	}
	if h := f.Hunt; h != nil {
		var s string
		switch {
		case h.For > 0:
			s = fmt.Sprintf(tx.huntOn, esc(h.Pool), tx.duration(h.For))
		case !h.Live:
			s = tx.huntBlind
		default:
			s = fmt.Sprintf(tx.huntWait, times(h.Hardness))
		}
		if h.Stints > 0 {
			s += fmt.Sprintf(tx.huntDay, tx.duration(h.Time), h.Stints)
		}
		m.line(s)
	}
	m.line(fmt.Sprintf(tx.shares, tx.duration(f.Uptime), f.Accepted, f.Rejected, percent(f.Rejected, f.Accepted+f.Rejected)))
	if len(silent) > 0 {
		m.line("")
		m.line(tx.silent)
		m.list(silent, tx.more)
	}
	return m.String()
}

func (b *Bot) minersText(tx *texts) string {
	miners := b.d.Miners()
	if len(miners) == 0 {
		return tx.noMiners
	}
	now := b.now()
	items := make([]string, 0, len(miners))
	for _, mn := range miners {
		if mn.Online {
			items = append(items, fmt.Sprintf("🟢 <code>%s</code> %s", esc(mn.Name), hashrate(mn.Hashrate)))
		} else {
			items = append(items, fmt.Sprintf("🔴 <code>%s</code> %s", esc(mn.Name), fmt.Sprintf(tx.silentFor, tx.duration(now.Sub(mn.LastShare)))))
		}
	}
	var m message
	m.line(fmt.Sprintf(tx.miners, len(miners)))
	m.list(items, tx.more)
	return m.String()
}
