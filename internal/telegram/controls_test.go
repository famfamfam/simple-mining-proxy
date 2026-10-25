package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/timed"
)

func controlBot(t *testing.T, count int, addr state.Address) (*Bot, *fakeAPI) {
	t.Helper()
	f := newFakeAPI(t)
	b, ev := newBot(t, f.srv.URL, store(t, map[string]string{"telegram_chats": `"100"`}))
	st, err := state.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if addr.Host == "" {
		addr = state.Address{Host: "127.0.0.1", Port: 1}
	}
	if err := st.Mutate(func(s *state.File) error {
		for i := 0; i < count; i++ {
			s.Pools = append(s.Pools, state.Pool{ID: fmt.Sprintf("p%d", i), Name: fmt.Sprintf("Pool <%d>", i), Coin: "BTC", Addresses: []state.Address{addr}, Username: "account.{worker}"})
		}
		if count > 0 {
			s.ActivePool = "p0"
		}
		if count > 1 {
			s.FallbackPools = []string{"p1"}
		}
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	b.d.State = st
	b.d.Pools = pool.NewManager(st, b.d.Settings, session.NewRegistry(ev), ev)
	return b, f
}

func query(chat, user, mid int64, data string) *callback {
	q := &callback{ID: "query", Message: &chatMessage{MessageID: mid}, Data: data}
	q.Message.Chat.ID, q.From.ID = chat, user
	return q
}

func click(b *Bot, q *callback) { b.handle(context.Background(), update{Callback: q}) }

func lastMessage(f *fakeAPI) sentMsg { msgs := f.messages(); return msgs[len(msgs)-1] }

func confirmationData(t *testing.T, f *fakeAPI) string {
	t.Helper()
	for _, row := range lastMessage(f).Rows {
		for _, btn := range row {
			if strings.HasPrefix(btn.Data, "confirm:") {
				return btn.Data
			}
		}
	}
	t.Fatal("confirmation button missing")
	return ""
}

func TestInlineNavigationThroughPolling(t *testing.T) {
	b, f := controlBot(t, 2, state.Address{})
	run(t, b)
	f.message(100, "/start")
	start := f.waitSent(t, 1)[0]
	if len(start.Rows) != 3 || start.Rows[0][1].Data != "pools:0" {
		t.Fatalf("menu: %+v", start.Rows)
	}
	f.updates[token] <- map[string]any{"update_id": 2, "callback_query": query(100, 7, 42, "pools:0")}
	got := f.waitSent(t, 2)[1]
	if got.Method != "editMessageText" || got.MessageID != 42 || !strings.Contains(got.Text, "Pool &lt;0&gt;") || len(got.Rows) != 5 {
		t.Fatalf("pool screen: %+v", got)
	}
}

func TestModeConfirmationIsBoundAndSingleUse(t *testing.T) {
	b, f := controlBot(t, 2, state.Address{})
	click(b, query(100, 7, 42, "mode:profit_switch:auto"))
	data := confirmationData(t, f)
	if b.d.Settings.Get().ProfitSwitch != settings.ProfitOff {
		t.Fatal("changed before confirmation")
	}
	for _, q := range []*callback{query(100, 8, 42, data), query(100, 7, 43, data), query(200, 7, 42, data), query(100, 7, 42, "confirm:forged")} {
		click(b, q)
	}
	if b.d.Settings.Get().ProfitSwitch != settings.ProfitOff || len(f.messages()) != 1 {
		t.Fatal("invalid confirmation accepted")
	}
	click(b, query(100, 7, 42, data))
	if b.d.Settings.Get().ProfitSwitch != settings.ProfitAuto {
		t.Fatal("mode not applied")
	}
	if string(b.d.State.Current().Settings["profit_switch"]) != `"auto"` {
		t.Fatal("mode not persisted")
	}
	if !strings.Contains(lastMessage(f).Text, "Mode saved") {
		t.Fatal(lastMessage(f).Text)
	}
	click(b, query(100, 7, 42, data))
	if len(f.messages()) != 2 {
		t.Fatal("replayed confirmation edited the message")
	}
}

func TestConfirmationsExpireAndCancel(t *testing.T) {
	for _, how := range []string{"expired", "cancelled", "replaced", "token changed", "access revoked"} {
		t.Run(how, func(t *testing.T) {
			b, f := controlBot(t, 2, state.Address{})
			now := t0
			b.now = func() time.Time { return now }
			click(b, query(100, 7, 42, "mode:profit_switch:auto"))
			data := confirmationData(t, f)
			switch how {
			case "expired":
				now = now.Add(2 * time.Minute)
			case "cancelled":
				click(b, query(100, 7, 42, "auto"))
			case "replaced":
				click(b, query(100, 7, 42, "mode:profit_switch:advise"))
			case "token changed":
				b.SetToken(other, "other_bot")
			case "access revoked":
				if _, err := b.d.Settings.Save(b.d.State, map[string]json.RawMessage{"telegram_chats": json.RawMessage(`""`)}); err != nil {
					t.Fatal(err)
				}
			}
			click(b, query(100, 7, 42, data))
			if b.d.Settings.Get().ProfitSwitch != settings.ProfitOff {
				t.Fatal("stale confirmation applied")
			}
		})
	}
}

func TestControlBoundary(t *testing.T) {
	b, f := controlBot(t, 2, state.Address{})
	for _, action := range []string{"mode:telegram_chats:200", "mode:log_level:debug", "mode:profit_switch:on", "mode:hunt_switch:auto", "mode:timed_switch:on:extra", "confirm:forged"} {
		click(b, query(100, 7, 42, action))
		if len(b.pending) != 0 {
			t.Fatalf("unsafe action %q got a confirmation", action)
		}
	}
	before := len(f.messages())
	for _, q := range []*callback{query(200, 7, 42, "switch:p1"), query(100, 7, 0, "switch:p1"), {ID: "inline", Data: "status"}} {
		click(b, q)
	}
	if len(f.messages()) != before || len(b.pending) != 0 || b.d.Pools.Snapshot().Active != "p0" {
		t.Fatal("unauthorized callback accepted")
	}
	click(b, query(100, 7, 42, "mode:profit_switch:off"))
	if len(b.pending) != 0 {
		t.Fatal("unchanged mode needs no confirmation")
	}
}

func TestPoolSwitchChecksBeforeSaving(t *testing.T) {
	for _, authorize := range []bool{true, false} {
		t.Run(fmt.Sprint(authorize), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var checks atomic.Int32
			done := make(chan struct{})
			go func() {
				defer close(done)
				c, err := ln.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				checks.Add(1)
				c.SetDeadline(time.Now().Add(3 * time.Second))
				dec := json.NewDecoder(c)
				for {
					var req struct {
						ID     json.RawMessage `json:"id"`
						Method string          `json:"method"`
					}
					if dec.Decode(&req) != nil {
						return
					}
					result := any([]any{[]any{}, "extra", 4})
					if req.Method == "mining.authorize" {
						result = authorize
					}
					json.NewEncoder(c).Encode(map[string]any{"id": req.ID, "result": result, "error": nil})
					if req.Method == "mining.authorize" {
						fmt.Fprintln(c, `{"id":null,"method":"mining.notify","params":["job1","prev","c1","c2",[],"20000000","1d00ffff","00000000",true]}`)
						return
					}
				}
			}()
			t.Cleanup(func() { ln.Close(); <-done })
			b, f := controlBot(t, 2, state.Address{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port})
			click(b, query(100, 7, 42, "switch:p1"))
			data := confirmationData(t, f)
			if checks.Load() != 0 || b.d.Pools.Snapshot().Active != "p0" {
				t.Fatal("selection started a switch")
			}
			click(b, query(100, 7, 42, data))
			want := "p0"
			if authorize {
				want = "p1"
			}
			if got := b.d.Pools.Snapshot().Active; got != want {
				t.Fatalf("active=%s, want %s; %s", got, want, lastMessage(f).Text)
			}
			if b.d.State.Current().ActivePool != want || checks.Load() != 1 {
				t.Fatal("check or persistence missing")
			}
			if !authorize && !strings.Contains(lastMessage(f).Text, "Switch failed") {
				t.Fatal(lastMessage(f).Text)
			}
			click(b, query(100, 7, 42, data))
			if checks.Load() != 1 {
				t.Fatal("confirmation replay repeated the check")
			}
		})
	}
}

func TestPoolPaginationAndFormatting(t *testing.T) {
	b, _ := controlBot(t, 15, state.Address{})
	for _, tc := range []struct{ page, start, count int }{{-5, 0, 6}, {0, 0, 6}, {1, 6, 6}, {999, 12, 3}} {
		text, rows := b.poolsScreen(&textsRU, tc.page)
		if rows[0][0].Data != fmt.Sprintf("switch:p%d", tc.start) || len(rows) != tc.count+4 {
			t.Fatalf("page %d: %+v", tc.page, rows)
		}
		if !strings.Contains(text, "Pool &lt;0&gt;") || strings.Contains(text, "%!") {
			t.Fatal(text)
		}
		for _, row := range rows {
			for _, btn := range row {
				if len(btn.Data) > 64 || btn.Text == "" {
					t.Fatalf("bad button %+v", btn)
				}
			}
		}
	}
}

func TestTimerAndHuntRequireConfiguredTargets(t *testing.T) {
	b, f := controlBot(t, 2, state.Address{})
	b.d.Timed = timed.New(timed.Deps{Settings: b.d.Settings, Pools: b.d.Pools, Events: b.d.Events})
	for _, action := range []string{"mode:timed_switch:on", "mode:hunt_switch:on"} {
		click(b, query(100, 7, 42, action))
		if len(b.pending) != 0 || !strings.Contains(lastMessage(f).Text, "No ") {
			t.Fatal("missing target allowed: " + action)
		}
	}
	yes := true
	if _, _, err := b.d.Pools.Update("p1", pool.Input{TimedTarget: &yes}, false); err != nil {
		t.Fatal(err)
	}
	text, _ := b.autoScreen(&textsRU)
	if !strings.Contains(text, "Пул таймера: Pool &lt;1&gt;") {
		t.Fatal(text)
	}
	click(b, query(100, 7, 42, "mode:timed_switch:on"))
	if !strings.Contains(lastMessage(f).Text, "Timer pool: Pool &lt;1&gt;") {
		t.Fatal(lastMessage(f).Text)
	}
	data := confirmationData(t, f)
	click(b, query(100, 7, 42, data))
	if b.d.Settings.Get().TimedSwitch != settings.TimedOn {
		t.Fatal("timer not enabled")
	}
}

func TestEventsStayBoundedAndEscapeHTML(t *testing.T) {
	b, _ := controlBot(t, 2, state.Address{})
	for i := 0; i < 20; i++ {
		b.d.Events.Warn("failover", "%d %s <script> %s", i, token, strings.Repeat("я&", 300))
	}
	b.d.Events.Info("session_open", "noise")
	text := b.eventsText(&textsRU)
	if len(text) > maxText || strings.Contains(text, token) || strings.Contains(text, "<script>") || !strings.Contains(text, "&lt;script&gt;") || strings.Contains(text, "noise") {
		t.Fatal(text)
	}
}

func TestUnchangedMessageIsSuccessful(t *testing.T) {
	b, f := controlBot(t, 0, state.Address{})
	f.unchanged = true
	if err := b.reply(context.Background(), 100, 42, "same", navigation(&textsEN)); err != nil {
		t.Fatal(err)
	}
}

func TestControlTranslations(t *testing.T) {
	if len(controlsEN) != len(controlsRU) {
		t.Fatal("translation keys differ")
	}
	for key, en := range controlsEN {
		if en == "" || controlsRU[key] == "" {
			t.Fatalf("missing translation %s", key)
		}
	}
	for _, tx := range []*texts{&textsEN, &textsRU} {
		for _, cmd := range []string{"status", "pools", "auto", "events", "miners", "help"} {
			if tx.cmdDesc[cmd] == "" {
				t.Fatal("missing command description: " + cmd)
			}
		}
	}
}
