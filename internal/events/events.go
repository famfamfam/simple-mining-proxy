// Package events keeps the recent operator-facing events in a ring buffer
// and mirrors each of them to the structured log.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Event struct {
	Seq     int64     `json:"seq"`
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Type    string    `json:"type"`
	Message string    `json:"message"`
}

const (
	maxThrottleKeys = 1000
	// maxThrottleInterval is the longest interval callers use; older keys can
	// always be forgotten without letting a duplicate through too early.
	maxThrottleInterval = 10 * time.Minute
)

type Log struct {
	mu        sync.Mutex
	buf       []Event
	next      int
	full      bool
	seq       int64
	throttled map[string]time.Time

	suppressed         int64
	lastSuppressReport time.Time
}

func New(size int) *Log {
	return &Log{buf: make([]Event, size), throttled: make(map[string]time.Time)}
}

func (l *Log) add(level slog.Level, typ, msg string) {
	l.mu.Lock()
	l.seq++
	l.buf[l.next] = Event{Seq: l.seq, Time: time.Now().UTC(), Level: levelName(level), Type: typ, Message: msg}
	l.next = (l.next + 1) % len(l.buf)
	if l.next == 0 {
		l.full = true
	}
	l.mu.Unlock()
	slog.Default().Log(context.Background(), level, msg, "event", typ)
}

func (l *Log) Info(typ, format string, args ...any) {
	l.add(slog.LevelInfo, typ, fmt.Sprintf(format, args...))
}

func (l *Log) Warn(typ, format string, args ...any) {
	l.add(slog.LevelWarn, typ, fmt.Sprintf(format, args...))
}

func (l *Log) Error(typ, format string, args ...any) {
	l.add(slog.LevelError, typ, fmt.Sprintf(format, args...))
}

// Throttled records a warning unless an event with the same key was
// recorded within interval. It keeps a flood of identical problems
// (hundreds of ASIC failing the same way) from pushing everything else out.
//
// The key table is capped at maxThrottleKeys. When it is full of recent
// keys (a flood of distinct problems, e.g. unique usernames), new keys are
// suppressed and a single "events_suppressed" warning is written per minute.
func (l *Log) Throttled(key string, interval time.Duration, typ, format string, args ...any) {
	now := time.Now()
	l.mu.Lock()
	if last, ok := l.throttled[key]; ok && now.Sub(last) < interval {
		l.mu.Unlock()
		return
	}
	if _, ok := l.throttled[key]; !ok && len(l.throttled) >= maxThrottleKeys {
		for k, t := range l.throttled {
			if now.Sub(t) > maxThrottleInterval {
				delete(l.throttled, k)
			}
		}
		if len(l.throttled) >= maxThrottleKeys {
			l.suppressed++
			report := now.Sub(l.lastSuppressReport) >= time.Minute
			var n int64
			if report {
				n, l.suppressed, l.lastSuppressReport = l.suppressed, 0, now
			}
			l.mu.Unlock()
			if report {
				l.Warn("events_suppressed", "event flood: %d distinct events suppressed (last type %s)", n, typ)
			}
			return
		}
	}
	l.throttled[key] = now
	l.mu.Unlock()
	l.Warn(typ, format, args...)
}

// List returns up to limit events, newest first.
func (l *Log) List(limit int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := l.next
	if l.full {
		n = len(l.buf)
	}
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Event, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (l.next - 1 - i + len(l.buf)) % len(l.buf)
		out = append(out, l.buf[idx])
	}
	return out
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	default:
		return "info"
	}
}
