package telegram

import (
	"fmt"
	"math"
	"time"
)

// The web UI translates its texts itself, but Telegram messages are made
// here, so the bot has its own small table per telegram_language. Messages
// use Telegram's HTML: values put into them must be escaped.
type texts struct {
	help    string // the commands
	denied  string // chat id
	test    string
	cmdDesc map[string]string // command menu

	offline     string // count
	offlineLine string // name, silent for, pool
	online      string // count
	onlineLine  string // name, was silent for
	mining      string // online, total

	summary   string
	hashrate  string // value
	asics     string // online, total
	pool      string // name, mode
	solo      string
	noPool    string
	timer     string // pool, time left
	huntOn    string // pool, for
	huntWait  string // factor
	huntBlind string
	huntDay   string // time, count
	shares    string // uptime, accepted, rejected, percent
	silent    string
	silentFor string // duration
	noMiners  string
	miners    string // count
	more      string // count
	modes     map[string]string

	sec, min, hour, day string
}

var textsEN = texts{
	help: "SimpleMiningProxy bot.\n\n" +
		"/status — farm summary\n" +
		"/miners — every ASIC\n\n" +
		"Alerts come here when an ASIC stops sending shares and when it comes back.",
	denied: "This chat may not use the bot. Its id is <code>%d</code>: add it in the proxy admin UI, " +
		"Settings → Monitoring and alerts → Telegram chats.",
	test: "✅ Test message from SimpleMiningProxy: alerts will come to this chat.",
	cmdDesc: map[string]string{
		"status": "Farm summary",
		"miners": "Every ASIC",
		"help":   "Commands",
	},

	offline:     "🔴 <b>No shares (%d)</b>",
	offlineLine: "• <code>%s</code> — silent for %s, last on %s",
	online:      "🟢 <b>Back online (%d)</b>",
	onlineLine:  "• <code>%s</code> — was silent for %s",
	mining:      "Mining: %d of %d ASICs",

	summary:   "📊 <b>Farm</b>",
	hashrate:  "Hashrate: %s (10 min)",
	asics:     "ASICs: %d of %d mining",
	pool:      "Pool: %s, %s",
	solo:      "solo",
	noPool:    "none",
	timer:     "Timer: on %s for %s more",
	huntOn:    "XEC hunt: on %s for %s",
	huntWait:  "XEC hunt: waiting, a block is x%s harder than usual now",
	huntBlind: "XEC hunt: eCash blocks are not seen now",
	huntDay:   "; last 24 h: %s on XEC, hunts: %d",
	shares:    "Shares since start (%s): %d accepted, %d rejected (%s)",
	silent:    "🔴 <b>No shares</b>",
	silentFor: "silent for %s",
	noMiners:  "No ASIC has sent a share yet.",
	miners:    "⛏ <b>ASICs (%d)</b>",
	more:      "… and %d more",
	modes: map[string]string{
		"NORMAL":    "main",
		"DEGRADED":  "fallback: the main pool is down",
		"DOWN":      "no pool is available",
		"SWITCHING": "switching",
	},

	sec: "s", min: "min", hour: "h", day: "d",
}

var textsRU = texts{
	help: "Бот SimpleMiningProxy.\n\n" +
		"/status — сводка по ферме\n" +
		"/miners — все асики\n\n" +
		"Сюда приходят оповещения, когда асик перестаёт присылать шары и когда возвращается.",
	denied: "Этому чату бот недоступен. ID чата: <code>%d</code> — добавьте его в админке прокси: " +
		"Настройки → Мониторинг и оповещения → Чаты Telegram.",
	test: "✅ Тестовое сообщение от SimpleMiningProxy: оповещения будут приходить в этот чат.",
	cmdDesc: map[string]string{
		"status": "Сводка по ферме",
		"miners": "Все асики",
		"help":   "Команды",
	},

	offline:     "🔴 <b>Нет шар (%d)</b>",
	offlineLine: "• <code>%s</code> — молчит %s, последний пул %s",
	online:      "🟢 <b>Снова в работе (%d)</b>",
	onlineLine:  "• <code>%s</code> — молчал %s",
	mining:      "В работе %d из %d асиков",

	summary:   "📊 <b>Ферма</b>",
	hashrate:  "Хешрейт: %s (за 10 мин)",
	asics:     "Асики: в работе %d из %d",
	pool:      "Пул: %s, %s",
	solo:      "соло",
	noPool:    "нет",
	timer:     "Таймер: на %s ещё %s",
	huntOn:    "Охота XEC: на %s уже %s",
	huntWait:  "Охота XEC: ждём, блок сейчас ×%s сложнее обычного",
	huntBlind: "Охота XEC: блоки eCash сейчас не видны",
	huntDay:   "; за сутки %s на XEC, заходов: %d",
	shares:    "Шары с запуска (%s): принято %d, отклонено %d (%s)",
	silent:    "🔴 <b>Нет шар</b>",
	silentFor: "молчит %s",
	noMiners:  "Асики ещё не присылали шары.",
	miners:    "⛏ <b>Асики (%d)</b>",
	more:      "… и ещё %d",
	modes: map[string]string{
		"NORMAL":    "основной",
		"DEGRADED":  "резервный: основной пул недоступен",
		"DOWN":      "нет доступного пула",
		"SWITCHING": "идёт переключение",
	},

	sec: "с", min: "мин", hour: "ч", day: "д",
}

func textsFor(lang string) *texts {
	if lang == "ru" {
		return &textsRU
	}
	return &textsEN
}

// duration prints d with its two largest units: "45 s", "12 min",
// "3 h 5 min", "2 d 4 h".
func (t *texts) duration(d time.Duration) string {
	d = max(d, 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d %s", int(d/time.Second), t.sec)
	case d < time.Hour:
		return fmt.Sprintf("%d %s", int(d/time.Minute), t.min)
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return fmt.Sprintf("%d %s", h, t.hour)
		}
		return fmt.Sprintf("%d %s %d %s", h, t.hour, m, t.min)
	}
	days, h := int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour)
	if h == 0 {
		return fmt.Sprintf("%d %s", days, t.day)
	}
	return fmt.Sprintf("%d %s %d %s", days, t.day, h, t.hour)
}

// hashrate prints H/s with three significant digits: "1.23 PH/s".
func hashrate(hs float64) string {
	units := []string{"H/s", "kH/s", "MH/s", "GH/s", "TH/s", "PH/s", "EH/s"}
	i := 0
	for hs >= 1000 && i < len(units)-1 {
		hs /= 1000
		i++
	}
	switch {
	case hs >= 100 || hs == 0:
		return fmt.Sprintf("%.0f %s", hs, units[i])
	case hs >= 10:
		return fmt.Sprintf("%.1f %s", hs, units[i])
	}
	return fmt.Sprintf("%.2f %s", hs, units[i])
}

// times prints a factor: "1.1", "25".
func times(x float64) string {
	if x < 10 {
		return fmt.Sprintf("%.1f", x)
	}
	return fmt.Sprintf("%.0f", x)
}

// percent prints part of total: "0.12%".
func percent(part, total uint64) string {
	if total == 0 {
		return "0%"
	}
	p := float64(part) / float64(total) * 100
	if p >= 10 || p == math.Trunc(p) {
		return fmt.Sprintf("%.0f%%", p)
	}
	return fmt.Sprintf("%.2f%%", p)
}
