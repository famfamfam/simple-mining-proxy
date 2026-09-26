package events

import (
	"fmt"
	"testing"
	"time"
)

func TestThrottledKeyTableIsBounded(t *testing.T) {
	l := New(100)
	for i := 0; i < maxThrottleKeys*3; i++ {
		l.Throttled(fmt.Sprintf("auth:%d", i), time.Minute, "authorization_failed", "failed %d", i)
	}
	l.mu.Lock()
	keys := len(l.throttled)
	l.mu.Unlock()
	if keys > maxThrottleKeys {
		t.Fatalf("throttle table grew to %d keys", keys)
	}
	suppressed := 0
	for _, e := range l.List(0) {
		if e.Type == "events_suppressed" {
			suppressed++
		}
	}
	if suppressed != 1 {
		t.Fatalf("want one events_suppressed warning, got %d", suppressed)
	}
}

func TestThrottledDeduplicates(t *testing.T) {
	l := New(10)
	l.Throttled("k", time.Minute, "x", "first")
	l.Throttled("k", time.Minute, "x", "second")
	if n := len(l.List(0)); n != 1 {
		t.Fatalf("got %d events, want 1", n)
	}
}

// Warnings stay reachable behind many info events.
func TestListAt(t *testing.T) {
	l := New(10)
	l.Warn("w", "old warning")
	for i := 0; i < 5; i++ {
		l.Info("i", "info %d", i)
	}
	l.Error("e", "an error")
	if got := l.ListAt(2, "info"); len(got) != 2 || got[0].Type != "e" || got[1].Message != "info 4" {
		t.Fatalf("info: %+v", got)
	}
	got := l.ListAt(10, "warn")
	if len(got) != 2 || got[0].Type != "e" || got[1].Message != "old warning" {
		t.Fatalf("warn: %+v", got)
	}
	if got := l.ListAt(10, "error"); len(got) != 1 {
		t.Fatalf("error: %+v", got)
	}
	if ValidLevel("debug") || !ValidLevel("warn") {
		t.Fatal("ValidLevel")
	}
}
