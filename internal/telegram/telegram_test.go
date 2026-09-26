package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/monitor"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

var t0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func off(name string) monitor.Change {
	return monitor.Change{Name: name, LastShare: t0, Pool: "Pool A"}
}

func on(name string) monitor.Change {
	return monitor.Change{Name: name, Online: true, Away: 3 * time.Minute}
}

func alert(changes ...monitor.Change) monitor.Alert {
	return monitor.Alert{Changes: changes, Online: 1, Total: 3}
}

func names(cs []monitor.Change) string {
	var out []string
	for _, c := range cs {
		s := c.Name
		if c.Online {
			s = "+" + s
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestNotifierBatchesAndSpacesMessages(t *testing.T) {
	n := newNotifier(batchDelay, minGap)
	n.add(alert(off("s21-2")), t0)
	if due := n.take(t0); due != nil {
		t.Fatalf("sent before the batch delay: %v", names(due))
	}
	n.add(alert(off("s21-1")), t0.Add(5*time.Second))
	due := n.take(t0.Add(batchDelay))
	if names(due) != "s21-1 s21-2" {
		t.Fatalf("due = %q", names(due))
	}
	n.sent(due, t0.Add(batchDelay))

	sent := t0.Add(batchDelay)
	n.add(alert(off("s21-3")), sent.Add(20*time.Second))
	if due := n.take(sent.Add(30 * time.Second)); due != nil {
		t.Fatalf("second message within the gap: %v", names(due))
	}
	n.add(alert(on("s21-1")), sent.Add(40*time.Second))
	due = n.take(sent.Add(minGap))
	if names(due) != "s21-3 +s21-1" {
		t.Fatalf("due = %q", names(due))
	}
}

func TestNotifierDropsBlips(t *testing.T) {
	n := newNotifier(batchDelay, minGap)
	n.add(alert(off("a")), t0)
	n.sent(n.take(t0.Add(batchDelay)), t0.Add(batchDelay))
	now := t0.Add(2 * time.Minute)
	// b went offline and came back before the next message; a came back
	// and went offline again: the chat already knows a is offline.
	n.add(alert(off("b")), now)
	n.add(alert(on("a")), now)
	n.add(alert(on("b")), now)
	n.add(alert(off("a")), now)
	if due := n.take(now.Add(batchDelay)); len(due) != 1 || due[0].Name != "b" || !due[0].Online {
		t.Fatalf("due = %q", names(due))
	}
}

func TestNotifierSkipsUnknownRecovery(t *testing.T) {
	n := newNotifier(batchDelay, minGap)
	n.add(alert(off("a")), t0)
	n.add(alert(on("a")), t0)
	// Never reported offline: its recovery is news only as a recovery.
	due := n.take(t0.Add(batchDelay))
	if names(due) != "+a" {
		t.Fatalf("due = %q", names(due))
	}
}

func TestNotifierHoldsFlappingASIC(t *testing.T) {
	n := newNotifier(0, minGap)
	now := t0
	step := func(c monitor.Change, after time.Duration) []monitor.Change {
		now = now.Add(after)
		n.add(alert(c), now)
		due := n.take(now)
		if due != nil {
			n.sent(due, now)
		}
		return due
	}
	if due := step(off("a"), 0); names(due) != "a" {
		t.Fatalf("first offline: %q", names(due))
	}
	if due := step(on("a"), 3*time.Minute); names(due) != "+a" {
		t.Fatalf("first recovery: %q", names(due))
	}
	if due := step(off("a"), 2*time.Minute); due != nil {
		t.Fatalf("flapping ASIC reported at once: %q", names(due))
	}
	// Other ASICs are not held.
	if due := step(off("b"), time.Minute); names(due) != "b" {
		t.Fatalf("other ASIC: %q", names(due))
	}
	// a is reported once its hold is over.
	now = t0.Add(3*time.Minute + flapHold)
	if due := n.take(now); names(due) != "a" {
		t.Fatalf("after the hold: %q", names(due))
	}
}

func TestNotifierRetriesAfterFailure(t *testing.T) {
	n := newNotifier(batchDelay, minGap)
	n.add(alert(off("a")), t0)
	now := t0.Add(batchDelay)
	if n.take(now) == nil {
		t.Fatal("nothing due")
	}
	n.failed(now)
	if n.take(now.Add(time.Second)) != nil {
		t.Fatal("retried within the gap")
	}
	if names(n.take(now.Add(minGap))) != "a" {
		t.Fatal("not retried after the gap")
	}
}

// ---- Bot API ----

const (
	token = "123456:SECRETSECRETSECRETSECRET00"
	other = "654321:OTHEROTHEROTHEROTHEROTHER00"
)

type sentMsg struct {
	Bot  string
	Chat int64
	Text string
}

type fakeAPI struct {
	srv     *httptest.Server
	updates map[string]chan map[string]any // per bot token, as in Telegram

	mu      sync.Mutex
	sent    []sentMsg
	menus   []string
	busy    int   // sendMessage answers 429 this many times first
	badChat int64 // sendMessage to this chat fails: the bot is not in it
	nextID  int64
	bots    map[string]string // token -> bot name
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		updates: map[string]chan map[string]any{token: make(chan map[string]any, 16), other: make(chan map[string]any, 16)},
		bots:    map[string]string{token: "farm_bot", other: "other_bot"},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
		bot, ok := f.bots[tok]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		reply := func(result any) { json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
		switch method {
		case "getMe":
			reply(map[string]any{"username": bot})
		case "setMyCommands":
			b, _ := json.Marshal(body["commands"])
			f.mu.Lock()
			f.menus = append(f.menus, string(b))
			f.mu.Unlock()
			reply(true)
		case "getUpdates":
			var out []map[string]any
			select {
			case u := <-f.updates[tok]:
				out = append(out, u)
			case <-time.After(20 * time.Millisecond):
			case <-r.Context().Done():
			}
			reply(out)
		case "sendMessage":
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.busy > 0 {
				f.busy--
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprint(w, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 1","parameters":{"retry_after":1}}`)
				return
			}
			chat := int64(body["chat_id"].(float64))
			if chat == f.badChat {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
				return
			}
			f.sent = append(f.sent, sentMsg{Bot: bot, Chat: chat, Text: body["text"].(string)})
			reply(map[string]any{"message_id": 1})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) message(chat int64, text string) { f.messageTo(token, chat, text) }

func (f *fakeAPI) messageTo(bot string, chat int64, text string) {
	f.mu.Lock()
	f.nextID++
	id := f.nextID
	f.mu.Unlock()
	f.updates[bot] <- map[string]any{"update_id": id, "message": map[string]any{
		"chat": map[string]any{"id": chat, "title": "Farm chat"}, "text": text,
	}}
}

func (f *fakeAPI) messages() []sentMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMsg(nil), f.sent...)
}

// waitSent waits until n messages have been sent.
func (f *fakeAPI) waitSent(t *testing.T, n int) []sentMsg {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.messages(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("sent %d messages, want %d: %+v", len(f.messages()), n, f.messages())
	return nil
}

func store(t *testing.T, kv map[string]string) *settings.Store {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range kv {
		raw[k] = json.RawMessage(v)
	}
	s, _, err := settings.NewStore(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func newBot(t *testing.T, api string, set *settings.Store) (*Bot, *events.Log) {
	ev := events.New(100)
	b := New(Deps{
		Token: token, APIURL: api, Settings: set, Events: ev,
		Miners: func() []monitor.Miner {
			return []monitor.Miner{
				{Name: "s21-2", Online: false, LastShare: time.Now().Add(-2 * time.Hour), Pool: "ViaBTC"},
				{Name: "s21-1", Online: true, Hashrate: 2e14, Pool: "ViaBTC"},
			}
		},
		Farm: func() Farm {
			return Farm{Hashrate: 2e14, Pool: "ViaBTC", Coin: "BTC", Mode: "NORMAL", Accepted: 998, Rejected: 2, Uptime: 3 * time.Hour,
				Timer: &Timer{Pool: "Solo <XEC>", Left: 7 * time.Minute}}
		},
	})
	b.batch, b.gap, b.tick = 20*time.Millisecond, 0, 5*time.Millisecond
	return b, ev
}

func run(t *testing.T, b *Bot) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestCommands(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100,-200"`}))
	run(t, b)

	f.message(100, "/status")
	got := f.waitSent(t, 1)[0]
	for _, want := range []string{"200 TH/s", "1 of 2 mining", "ViaBTC (BTC), main", "Solo &lt;XEC&gt; for 7 min more",
		"998 accepted, 2 rejected (0.20%)", "s21-2</code> — silent for 2 h"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("/status has no %q:\n%s", want, got.Text)
		}
	}
	if got.Chat != 100 {
		t.Fatalf("answered chat %d", got.Chat)
	}

	f.message(-200, "/miners@farm_bot")
	got = f.waitSent(t, 2)[1]
	if got.Chat != -200 || !strings.Contains(got.Text, "ASICs (2)") ||
		strings.Index(got.Text, "s21-2") > strings.Index(got.Text, "s21-1") {
		t.Fatalf("/miners:\n%s", got.Text)
	}

	f.message(100, "/status@other_bot")
	f.message(100, "just text")
	f.message(100, "/start")
	got = f.waitSent(t, 3)[2]
	if !strings.Contains(got.Text, "/status") || len(f.messages()) != 3 {
		t.Fatalf("messages = %+v", f.messages())
	}
	f.mu.Lock()
	menus := f.menus
	f.mu.Unlock()
	if len(menus) != 1 || !strings.Contains(menus[0], `"command":"status"`) {
		t.Fatalf("menus = %v", menus)
	}
	if st := b.Status(); !st.Configured || st.Username != "farm_bot" || st.Error != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestRussian(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`, "telegram_language": `"ru"`}))
	run(t, b)
	f.message(100, "/status")
	got := f.waitSent(t, 1)[0]
	for _, want := range []string{"Хешрейт: 200 TH/s", "в работе 1 из 2", "основной", "принято 998", "молчит 2 ч"} {
		if !strings.Contains(got.Text, want) {
			t.Errorf("/status has no %q:\n%s", want, got.Text)
		}
	}
}

func TestUnknownChatGetsItsID(t *testing.T) {
	f := newFakeAPI(t)
	b, ev := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`}))
	run(t, b)
	f.message(555, "/status")
	f.message(555, "/status") // within deniedEvery: not answered
	f.message(100, "/help")
	got := f.waitSent(t, 2)
	if got[0].Chat != 555 || !strings.Contains(got[0].Text, "<code>555</code>") || strings.Contains(got[0].Text, "TH/s") {
		t.Fatalf("answer to an unknown chat: %+v", got[0])
	}
	if got[1].Chat != 100 {
		t.Fatalf("second message went to %d", got[1].Chat)
	}
	if e := ev.List(0); len(e) == 0 || e[0].Type != "telegram_denied" {
		t.Fatalf("events = %+v", e)
	}
}

func TestAlertsGoToEveryChat(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100,-200"`}))
	run(t, b)
	b.Notify(monitor.Alert{Changes: []monitor.Change{{Name: "s21-5", LastShare: time.Now().Add(-70 * time.Second), Pool: "ViaBTC"}}, Online: 9, Total: 10})
	b.Notify(monitor.Alert{Changes: []monitor.Change{{Name: "s21-7", Online: true, Away: 12 * time.Minute}}, Online: 10, Total: 10})
	got := f.waitSent(t, 2)
	for _, m := range got {
		for _, want := range []string{"No shares (1)", "s21-5</code> — silent for 1 min, last on ViaBTC", "Back online (1)",
			"s21-7</code> — was silent for 12 min", "Mining: 10 of 10 ASICs"} {
			if !strings.Contains(m.Text, want) {
				t.Errorf("alert has no %q:\n%s", want, m.Text)
			}
		}
	}
	// Sent to the chats at once: in any order, but once to each.
	if a, b := min(got[0].Chat, got[1].Chat), max(got[0].Chat, got[1].Chat); a != -200 || b != 100 {
		t.Fatalf("chats = %d, %d", got[0].Chat, got[1].Chat)
	}
}

// A chat the bot cannot write to stays reported while polling goes on.
func TestSendErrorOutlivesPolling(t *testing.T) {
	f := newFakeAPI(t)
	f.badChat = -200
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100,-200"`}))
	run(t, b)
	b.Notify(monitor.Alert{Changes: []monitor.Change{{Name: "s21-5", LastShare: time.Now()}}, Online: 0, Total: 1})
	f.waitSent(t, 1)
	time.Sleep(200 * time.Millisecond) // several polls
	if st := b.Status(); !strings.Contains(st.Error, "chat -200") || !strings.Contains(st.Error, "chat not found") {
		t.Fatalf("status = %+v", st)
	}
}

func TestTestMessage(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, nil))
	if err := b.Test(context.Background()); err != ErrNoChats {
		t.Fatalf("err = %v, want ErrNoChats", err)
	}
	b.d.Settings = store(t, map[string]string{"telegram_chats": `"100"`})
	f.busy = 1 // rate limited once: waits retry_after and sends
	if err := b.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.messages(); len(got) != 1 || !strings.Contains(got[0].Text, "Test message") {
		t.Fatalf("sent = %+v", got)
	}
}

func TestTokenStaysOutOfErrors(t *testing.T) {
	b, ev := newBot(t, "http://127.0.0.1:1", store(t, map[string]string{"telegram_chats": `"100"`}))
	err := b.Test(context.Background())
	if err == nil {
		t.Fatal("no error")
	}
	for _, s := range []string{err.Error(), b.Status().Error, ev.List(1)[0].Message} {
		if strings.Contains(s, "SECRET") {
			t.Fatalf("token leaked: %s", s)
		}
	}
	if b.Status().Error == "" {
		t.Fatal("the error is not shown")
	}
}

// eventually waits for cond, polling.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWrongTokenIsShown(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, nil))
	b.SetToken("999:WRONGWRONGWRONGWRONGWRONG", "")
	run(t, b)
	eventually(t, "an error", func() bool { return b.Status().Error != "" })
	if st := b.Status(); !strings.Contains(st.Error, "getMe: 404") {
		t.Fatalf("status = %+v", st)
	}
}

func TestNoTokenKeepsQuiet(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`}))
	b.SetToken("", "")
	run(t, b)
	b.Notify(monitor.Alert{Changes: []monitor.Change{{Name: "s21-5", LastShare: time.Now()}}, Total: 1})
	if err := b.Test(context.Background()); err != ErrNoToken {
		t.Fatalf("Test = %v, want ErrNoToken", err)
	}
	time.Sleep(100 * time.Millisecond)
	if st := b.Status(); st.Configured || len(f.messages()) != 0 {
		t.Fatalf("status %+v, messages %+v", st, f.messages())
	}
}

func TestTokenChangesOnTheFly(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`}))
	b.SetToken("", "")
	run(t, b)

	b.SetToken(token, "")
	eventually(t, "the first bot", func() bool { return b.Status().Username == "farm_bot" })
	f.message(100, "/help")
	if got := f.waitSent(t, 1)[0]; got.Bot != "farm_bot" {
		t.Fatalf("answered by %q", got.Bot)
	}

	b.SetToken(other, "other_bot")
	if st := b.Status(); !st.Configured || st.Username != "other_bot" {
		t.Fatalf("status after the switch = %+v", st)
	}
	f.messageTo(other, 100, "/help")
	if got := f.waitSent(t, 2)[1]; got.Bot != "other_bot" {
		t.Fatalf("answered by %q after the switch", got.Bot)
	}

	b.SetToken("", "")
	if st := b.Status(); st.Configured || st.Username != "" {
		t.Fatalf("status after removing the token = %+v", st)
	}
}

func TestCheck(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, nil))
	if name, err := b.Check(context.Background(), other); err != nil || name != "other_bot" {
		t.Fatalf("Check = %q, %v", name, err)
	}
	_, err := b.Check(context.Background(), "999:WRONGWRONGWRONGWRONGWRONG")
	if !IsRejected(err) {
		t.Fatalf("a wrong token: %v, rejected %v", err, IsRejected(err))
	}
	down, _ := newBot(t, "http://127.0.0.1:1", store(t, nil))
	_, err = down.Check(context.Background(), token)
	if err == nil || IsRejected(err) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("unreachable: %v, rejected %v", err, IsRejected(err))
	}
}

func TestValidToken(t *testing.T) {
	for s, want := range map[string]bool{
		token:                          true,
		"123:short":                    false,
		"abc:AAAAAAAAAAAAAAAAAAAAAAAA": false,
		"123:AAAAAAAAAAAAAAAAAAAA AAA": false,
		"":                             false,
	} {
		if ValidToken(s) != want {
			t.Errorf("ValidToken(%q) = %v", s, !want)
		}
	}
}

func TestNilBotStatus(t *testing.T) {
	var b *Bot
	if st := b.Status(); st.Configured {
		t.Fatalf("status = %+v", st)
	}
}

func TestCommandParsing(t *testing.T) {
	for text, want := range map[string]string{
		"/status":              "status",
		"/Status@Farm_Bot now": "status",
		"/miners@other_bot":    "",
		"status":               "",
		"":                     "",
		"/":                    "",
	} {
		if got := command(text, "farm_bot"); got != want {
			t.Errorf("command(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestChatIDs(t *testing.T) {
	got := ChatIDs("100,-1001234567890,x")
	if len(got) != 2 || got[0] != 100 || got[1] != -1001234567890 {
		t.Fatalf("ChatIDs = %v", got)
	}
	if ChatIDs("") != nil {
		t.Fatal("ids from an empty setting")
	}
}

func TestFormatting(t *testing.T) {
	en, ru := textsFor("en"), textsFor("ru")
	for d, want := range map[time.Duration]string{
		-time.Second:                    "0 s",
		45 * time.Second:                "45 s",
		12*time.Minute + 30*time.Second: "12 min",
		3 * time.Hour:                   "3 h",
		3*time.Hour + 5*time.Minute:     "3 h 5 min",
		50 * time.Hour:                  "2 d 2 h",
		48 * time.Hour:                  "2 d",
	} {
		if got := en.duration(d); got != want {
			t.Errorf("duration(%s) = %q, want %q", d, got, want)
		}
	}
	if got := ru.duration(3*time.Hour + 5*time.Minute); got != "3 ч 5 мин" {
		t.Errorf("ru duration = %q", got)
	}
	for hs, want := range map[float64]string{0: "0 H/s", 950: "950 H/s", 2e14: "200 TH/s", 1.234e15: "1.23 PH/s", 45.6e12: "45.6 TH/s"} {
		if got := hashrate(hs); got != want {
			t.Errorf("hashrate(%g) = %q, want %q", hs, got, want)
		}
	}
	for _, c := range []struct {
		part, total uint64
		want        string
	}{{0, 0, "0%"}, {2, 1000, "0.20%"}, {1, 2, "50%"}, {1, 1, "100%"}} {
		if got := percent(c.part, c.total); got != c.want {
			t.Errorf("percent(%d, %d) = %q, want %q", c.part, c.total, got, c.want)
		}
	}
}

func TestLongListIsCut(t *testing.T) {
	var changes []monitor.Change
	for i := range 500 {
		changes = append(changes, monitor.Change{Name: fmt.Sprintf("asic-with-a-long-name-%03d", i), LastShare: t0, Pool: "Pool"})
	}
	text := alertText(textsFor("en"), changes, 0, 500, t0.Add(time.Minute))
	if len(text) > maxText || !strings.Contains(text, "more") || !strings.HasSuffix(text, "Mining: 0 of 500 ASICs") {
		t.Fatalf("%d bytes:\n%s", len(text), text[max(0, len(text)-300):])
	}
}

func TestSoloPoolInSummary(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`}))
	farm := b.d.Farm
	b.d.Farm = func() Farm { x := farm(); x.Pool, x.Coin, x.Solo = "Molepool", "XEC", true; return x }
	run(t, b)
	f.message(100, "/status")
	if got := f.waitSent(t, 1)[0].Text; !strings.Contains(got, "Pool: Molepool (XEC, solo), main") {
		t.Fatalf("/status:\n%s", got)
	}
}

func TestHuntInSummary(t *testing.T) {
	f := newFakeAPI(t)
	b, _ := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`, "telegram_language": `"ru"`}))
	farm := b.d.Farm
	hunt := &Hunt{Pool: "Molepool", Hardness: 25, Live: true, Stints: 3, Time: 40 * time.Minute}
	b.d.Farm = func() Farm { x := farm(); x.Hunt = hunt; return x }
	run(t, b)
	f.message(100, "/status")
	if got := f.waitSent(t, 1)[0].Text; !strings.Contains(got, "Охота XEC: ждём, блок сейчас ×25 сложнее обычного; за сутки 40 мин на XEC, заходов: 3") {
		t.Fatalf("/status:\n%s", got)
	}
	hunt.For = 7 * time.Minute
	f.message(100, "/status")
	if got := f.waitSent(t, 2)[1].Text; !strings.Contains(got, "Охота XEC: на Molepool уже 7 мин") {
		t.Fatalf("/status:\n%s", got)
	}
}
