package listener

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
)

func TestAcquireEnforcesGlobalLimitAtomically(t *testing.T) {
	set, _, err := settings.NewStore(map[string]json.RawMessage{
		"max_connections":         json.RawMessage(`10`),
		"max_pending_connections": json.RawMessage(`10`),
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := NewLimits(set, events.New(10))

	const attempts = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			release, _, ok := limits.acquire(fmt.Sprintf("192.0.2.%d", i+1))
			if ok {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if len(releases) != 10 || limits.total.Load() != 10 || limits.pending.Load() != 10 {
		t.Fatalf("accepted=%d total=%d pending=%d", len(releases), limits.total.Load(), limits.pending.Load())
	}
	for _, release := range releases {
		release()
	}
	if limits.total.Load() != 0 || limits.pending.Load() != 0 {
		t.Fatalf("limits were not released: total=%d pending=%d", limits.total.Load(), limits.pending.Load())
	}
}
