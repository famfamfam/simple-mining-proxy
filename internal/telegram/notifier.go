package telegram

import (
	"sort"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/monitor"
)

// notifier decides which ASIC changes go out and when, so that the chat
// gets the news fast but is not flooded:
//   - the first change waits batch for others that come with it;
//   - messages go out at most once per gap;
//   - an ASIC back to the state of the last message about it before the
//     next message is not reported at all (the events still have it);
//   - a flapping ASIC is reported at most once per flapHold.
//
// It is used by one goroutine and does not lock.
type notifier struct {
	batch, gap time.Duration

	pending  map[string]monitor.Change // latest change per ASIC not sent yet
	reported map[string]*reported
	online   int // totals of the latest alert
	total    int
	firstDue time.Time // when the oldest due change came; zero when none
	lastSent time.Time
}

// reported is what the chat was last told about one ASIC.
type reported struct {
	online bool
	times  []time.Time // messages about it within flapWindow
}

func newNotifier(batch, gap time.Duration) *notifier {
	return &notifier{batch: batch, gap: gap, pending: map[string]monitor.Change{}, reported: map[string]*reported{}}
}

func (n *notifier) add(a monitor.Alert, now time.Time) {
	for _, c := range a.Changes {
		n.pending[c.Name] = c
	}
	n.online, n.total = a.Online, a.Total
	if n.firstDue.IsZero() {
		n.firstDue = now
	}
}

// take returns the changes to send now, offline first; nil when nothing
// is due yet.
func (n *notifier) take(now time.Time) []monitor.Change {
	var due []monitor.Change
	for name, c := range n.pending {
		r := n.reported[name]
		if r != nil {
			r.times = recent(r.times, now)
			if r.online == c.Online {
				delete(n.pending, name) // back to what the chat already knows
				continue
			}
			if len(r.times) >= flapChanges && now.Sub(r.times[len(r.times)-1]) < flapHold {
				continue
			}
		}
		due = append(due, c)
	}
	for name, r := range n.reported {
		if _, ok := n.pending[name]; !ok && len(recent(r.times, now)) == 0 && r.online {
			delete(n.reported, name) // nothing to remember: online, quiet for an hour
		}
	}
	if len(due) == 0 {
		n.firstDue = time.Time{}
		return nil
	}
	if n.firstDue.IsZero() {
		n.firstDue = now
	}
	if now.Sub(n.firstDue) < n.batch || now.Sub(n.lastSent) < n.gap {
		return nil
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].Online != due[j].Online {
			return !due[i].Online
		}
		return monitor.NameLess(due[i].Name, due[j].Name)
	})
	return due
}

// sent records that due went out.
func (n *notifier) sent(due []monitor.Change, now time.Time) {
	for _, c := range due {
		r := n.reported[c.Name]
		if r == nil {
			r = &reported{}
			n.reported[c.Name] = r
		}
		r.online = c.Online
		r.times = append(recent(r.times, now), now)
		delete(n.pending, c.Name)
	}
	n.lastSent, n.firstDue = now, time.Time{}
}

// failed records a message that no chat got: it is tried again after gap.
func (n *notifier) failed(now time.Time) {
	n.lastSent = now
}

// recent drops the times older than flapWindow.
func recent(times []time.Time, now time.Time) []time.Time {
	i := 0
	for i < len(times) && now.Sub(times[i]) >= flapWindow {
		i++
	}
	return times[i:]
}
