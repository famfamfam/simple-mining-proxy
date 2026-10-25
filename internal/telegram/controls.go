package telegram

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

type confirmation struct {
	code, action, token string
	user, messageID     int64
	until               time.Time
}

func navigation(tx *texts) [][]button {
	return [][]button{
		{{tx.ui["status"], "status"}, {tx.ui["pools"], "pools:0"}},
		{{tx.ui["automation"], "auto"}, {tx.ui["events"], "events"}},
		{{tx.ui["miners"], "miners"}, {tx.ui["help"], "help"}},
	}
}

func (b *Bot) screen(action string, tx *texts) (string, [][]button) {
	switch {
	case action == "status":
		return b.summary(tx), navigation(tx)
	case action == "miners":
		return b.minersText(tx), navigation(tx)
	case action == "pools" || strings.HasPrefix(action, "pools:"):
		page, _ := strconv.Atoi(strings.TrimPrefix(action, "pools:"))
		return b.poolsScreen(tx, page)
	case action == "auto":
		return b.autoScreen(tx)
	case action == "events":
		return b.eventsText(tx), navigation(tx)
	default:
		return tx.help, navigation(tx)
	}
}

func (b *Bot) poolsScreen(tx *texts, page int) (string, [][]button) {
	if b.d.Pools == nil {
		return tx.ui["unavailable"], navigation(tx)
	}
	snap := b.d.Pools.Snapshot()
	if len(snap.Pools) == 0 {
		return tx.ui["noPools"], navigation(tx)
	}
	const pageSize = 6
	pages := (len(snap.Pools) + pageSize - 1) / pageSize
	page = max(0, min(page, pages-1))
	name := func(id string) string {
		if p, ok := snap.Get(id); ok {
			return esc(p.Name)
		}
		return tx.noPool
	}
	text := fmt.Sprintf(tx.ui["choosePool"], name(snap.Active), name(b.d.Pools.Effective()))
	var rows [][]button
	for _, p := range snap.Pools[page*pageSize : min((page+1)*pageSize, len(snap.Pools))] {
		icon := map[string]string{"UP": "🟢", "DOWN": "🔴"}[string(b.d.Pools.Health(p.ID).Status)]
		if icon == "" {
			icon = "⚪"
		}
		label := icon + " " + p.Name
		if p.Coin != "" {
			label += " · " + p.Coin
		}
		if p.Solo {
			label += " · " + tx.solo
		}
		if p.ID == snap.Active {
			label = "✓ " + label
		}
		rows = append(rows, []button{{label, "switch:" + p.ID}})
	}
	var pager []button
	if page > 0 {
		pager = append(pager, button{"‹", "pools:" + strconv.Itoa(page-1)})
	}
	if page+1 < pages {
		pager = append(pager, button{"›", "pools:" + strconv.Itoa(page+1)})
	}
	if pages > 1 {
		text += fmt.Sprintf("\n%d / %d", page+1, pages)
		rows = append(rows, pager)
	}
	return text, append(rows, navigation(tx)...)
}

func (b *Bot) autoScreen(tx *texts) (string, [][]button) {
	v := b.d.Settings.Get()
	var m message
	m.line(tx.ui["autoTitle"])
	m.line(fmt.Sprintf(tx.ui["autoSummary"], tx.ui[v.ProfitSwitch], tx.ui[v.TimedSwitch], tx.ui[v.HuntSwitch]))
	m.line(fmt.Sprintf(tx.ui["profitSchedule"], tx.duration(v.ProfitInterval), v.ProfitMargin))
	m.line(fmt.Sprintf(tx.ui["timerSchedule"], tx.duration(v.TimedDuration), tx.duration(v.TimedPeriod)))
	if b.d.Timed != nil {
		st := b.d.Timed.Status()
		if b.d.Pools != nil {
			for _, target := range []struct{ id, label string }{{st.Target, "timerTarget"}, {st.Hunt.Target, "huntTarget"}} {
				if p, ok := b.d.Pools.Snapshot().Get(target.id); ok {
					m.line(fmt.Sprintf(tx.ui[target.label], esc(p.Name)))
				}
			}
		}
		if st.Target == "" {
			m.line(tx.ui["noTimerTarget"])
		}
		if st.Hunt.Target == "" {
			m.line(tx.ui["noHuntTarget"])
		}
		if st.Waiting {
			m.line(tx.ui["timerWaiting"])
		}
		if st.Next != nil {
			m.line(fmt.Sprintf(tx.ui["timerNext"], tx.duration(st.Next.Sub(b.now()))))
		}
		for _, err := range []string{st.Error, st.Hunt.Error} {
			if err != "" {
				m.line(fmt.Sprintf(tx.ui["problem"], esc(shortText(b.redact(err), 200))))
			}
		}
	}
	if b.d.Profit != nil {
		if r := b.d.Profit.Status().Report; r != nil {
			if r.Best != "" {
				m.line(fmt.Sprintf(tx.ui["profitReport"], esc(r.Best), r.Advantage, tx.duration(b.now().Sub(r.At))))
			}
			if r.Error != "" {
				m.line(fmt.Sprintf(tx.ui["problem"], esc(shortText(b.redact(r.Error), 200))))
			}
		}
	}
	m.line("")
	m.line(tx.ui["autoHelp"])
	rows := navigation(tx)
	if b.d.State != nil {
		mark := func(label, current, value string) string {
			if current == value {
				return "✓ " + label
			}
			return label
		}
		rows = append([][]button{
			{{mark(tx.ui["profitOff"], v.ProfitSwitch, "off"), "mode:profit_switch:off"},
				{mark(tx.ui["advise"], v.ProfitSwitch, "advise"), "mode:profit_switch:advise"},
				{mark(tx.ui["profitAuto"], v.ProfitSwitch, "auto"), "mode:profit_switch:auto"}},
			{{mark(tx.ui["timerOn"], v.TimedSwitch, "on"), "mode:timed_switch:on"}, {mark(tx.ui["timerOff"], v.TimedSwitch, "off"), "mode:timed_switch:off"}},
			{{mark(tx.ui["huntOn"], v.HuntSwitch, "on"), "mode:hunt_switch:on"}, {mark(tx.ui["huntOff"], v.HuntSwitch, "off"), "mode:hunt_switch:off"}},
		}, rows...)
	}
	return m.String(), rows
}

func (b *Bot) eventsText(tx *texts) string {
	var items []string
	for _, e := range b.d.Events.List(0) {
		if !strings.HasPrefix(e.Type, "telegram_") && (e.Level != "info" || e.Type == "pool_switched" || e.Type == "pool_up" || e.Type == "settings_changed" || e.Type == "failback" ||
			strings.HasPrefix(e.Type, "timed_") || strings.HasPrefix(e.Type, "hunt_") || strings.HasPrefix(e.Type, "profit_")) {
			icon := "🔄"
			if e.Level != "info" {
				icon = "⚠️"
			}
			items = append(items, fmt.Sprintf("%s <code>%s</code> %s", icon, e.Time.UTC().Format("02 Jan 15:04:05"), esc(shortText(b.redact(e.Message), 180))))
			if len(items) == 8 {
				break
			}
		}
	}
	var m message
	m.line(tx.ui["eventsTitle"])
	if len(items) == 0 {
		m.line(tx.ui["noEvents"])
	}
	m.list(items, tx.more)
	return m.String()
}

func shortText(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

func (b *Bot) answerCallback(ctx context.Context, q *callback, text string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := b.call(cctx, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": text, "show_alert": text != ""}, nil); err != nil && ctx.Err() == nil {
		b.fail(sending, err)
	}
}

func (b *Bot) handleCallback(ctx context.Context, q *callback) {
	v := b.d.Settings.Get()
	tx := textsFor(v.TelegramLanguage)
	if q.Message == nil || q.Message.MessageID <= 0 || q.From.ID <= 0 || !slices.Contains(ChatIDs(v.TelegramChats), q.Message.Chat.ID) {
		b.answerCallback(ctx, q, tx.ui["denied"])
		return
	}
	chat, mid := q.Message.Chat.ID, q.Message.MessageID
	action := q.Data
	if len(action) > 64 {
		b.answerCallback(ctx, q, tx.ui["expired"])
		return
	}
	confirmed := strings.HasPrefix(action, "confirm:")
	b.mu.Lock()
	if confirmed {
		c, ok := b.pending[chat]
		if !ok || c.token != b.token || c.code != strings.TrimPrefix(action, "confirm:") || c.user != q.From.ID || c.messageID != mid || !b.now().Before(c.until) {
			b.mu.Unlock()
			b.answerCallback(ctx, q, tx.ui["expired"])
			return
		}
		action = c.action
		delete(b.pending, chat) // consume before the network call; a second tap cannot repeat it
	} else if c, ok := b.pending[chat]; ok && c.user == q.From.ID && c.messageID == mid {
		delete(b.pending, chat)
	}
	b.mu.Unlock()
	b.answerCallback(ctx, q, "")
	if ctx.Err() != nil {
		return
	}
	var text string
	var rows [][]button
	switch {
	case strings.HasPrefix(action, "switch:"):
		if b.d.Pools == nil {
			text, rows = tx.ui["unavailable"], navigation(tx)
			break
		}
		id := strings.TrimPrefix(action, "switch:")
		p, ok := b.d.Pools.Snapshot().Get(id)
		if !ok {
			text, rows = tx.ui["expired"], navigation(tx)
			break
		}
		if b.d.Pools.Snapshot().Active == id {
			text, rows = b.poolsScreen(tx, 0)
			break
		}
		if !confirmed {
			text = fmt.Sprintf(tx.ui["confirmSwitch"], esc(p.Name), tx.duration(v.SwitchDrain))
			if v.ProfitSwitch == settings.ProfitAuto || v.TimedSwitch == settings.TimedOn || v.HuntSwitch == settings.HuntOn {
				text += "\n\n" + tx.ui["autoWarning"]
			}
			rows = b.confirmation(q, action, tx, "pools:0")
			break
		}
		if err := b.reply(ctx, chat, mid, fmt.Sprintf(tx.ui["checking"], esc(p.Name)), nil); err != nil {
			if ctx.Err() == nil {
				b.fail(sending, err)
			}
			return
		}
		n, err := b.d.Pools.Activate(ctx, id, false)
		if err != nil {
			text = fmt.Sprintf(tx.ui["switchFailed"], esc(shortText(b.redact(err.Error()), 350)))
		} else {
			text = fmt.Sprintf(tx.ui["switched"], esc(p.Name), n, tx.duration(b.d.Settings.Get().SwitchDrain))
			b.d.Events.Info("telegram_control", "Telegram: user %d in chat %d switched to pool %s", q.From.ID, chat, id)
		}
		rows = navigation(tx)
	case strings.HasPrefix(action, "mode:"):
		key, value, ok := modeChange(action)
		if !ok || b.d.State == nil {
			text, rows = tx.ui["unavailable"], navigation(tx)
			break
		}
		if !confirmed {
			current := map[string]string{"profit_switch": v.ProfitSwitch, "timed_switch": v.TimedSwitch, "hunt_switch": v.HuntSwitch}[key]
			if current == value {
				text, rows = b.autoScreen(tx)
				break
			}
			text = fmt.Sprintf(tx.ui["confirmMode"], tx.ui[key], tx.ui[value])
			if value == "on" && b.d.Timed != nil && b.d.Pools != nil {
				st := b.d.Timed.Status()
				id, label, missing := st.Target, "timerTarget", "noTimerTarget"
				if key == "hunt_switch" {
					id, label, missing = st.Hunt.Target, "huntTarget", "noHuntTarget"
				}
				p, ok := b.d.Pools.Snapshot().Get(id)
				if !ok {
					text, rows = tx.ui[missing], navigation(tx)
					break
				}
				text += "\n\n" + fmt.Sprintf(tx.ui[label], esc(p.Name))
			}
			rows = b.confirmation(q, action, tx, "auto")
			break
		}
		raw, _ := json.Marshal(value)
		pending, err := b.d.Settings.Save(b.d.State, map[string]json.RawMessage{key: raw})
		if err != nil {
			text = fmt.Sprintf(tx.ui["saveFailed"], esc(shortText(b.redact(err.Error()), 350)))
			rows = navigation(tx)
			break
		}
		for _, c := range pending.Changes {
			b.d.Events.Info("settings_changed", "settings changed: %s %s → %s (Telegram user %d, chat %d)", c.Key, c.Old, c.New, q.From.ID, chat)
		}
		text, rows = b.autoScreen(tx)
		text = tx.ui["saved"] + "\n\n" + text
	default:
		text, rows = b.screen(action, tx)
	}
	if err := b.reply(ctx, chat, mid, text, rows); err != nil && ctx.Err() == nil {
		b.fail(sending, fmt.Errorf("chat %d: %w", chat, err))
	}
}

func (b *Bot) confirmation(q *callback, action string, tx *texts, back string) [][]button {
	code := rand.Text()
	b.mu.Lock()
	b.pending[q.Message.Chat.ID] = confirmation{code: code, action: action, token: b.token, user: q.From.ID, messageID: q.Message.MessageID, until: b.now().Add(2 * time.Minute)}
	b.mu.Unlock()
	return [][]button{{{tx.ui["confirm"], "confirm:" + code}, {tx.ui["cancel"], back}}}
}

// Only these modes may be changed by callback data; settings like chat access
// and bot credentials are deliberately outside the Telegram controls.
func modeChange(action string) (key, value string, ok bool) {
	parts := strings.Split(action, ":")
	if len(parts) != 3 || parts[0] != "mode" {
		return "", "", false
	}
	key, value = parts[1], parts[2]
	switch key {
	case "profit_switch":
		return key, value, slices.Contains([]string{"off", "advise", "auto"}, value)
	case "timed_switch", "hunt_switch":
		return key, value, value == "off" || value == "on"
	}
	return "", "", false
}
