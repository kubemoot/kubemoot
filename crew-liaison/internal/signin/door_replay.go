package signin

import (
	"sort"
	"sync"
	"time"
)

// maxRemembered bounds the replay cache. When it is full the soonest-expiring entries are
// dropped first, so memory cannot be grown by a flood of requests.
const maxRemembered = 20000

// replayCache remembers identifiers until a time. The door's codes and refresh tokens are
// sealed values it keeps no record of, so this process-local memory is what makes a code
// single use and a refresh token rotate: a second sight of an id is refused. With several
// replicas each has its own memory; the sealed values' short lives bound that.
type replayCache struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func newReplayCache() *replayCache { return &replayCache{entries: map[string]time.Time{}} }

// Remember records id until expires and reports whether it was already there.
func (c *replayCache) Remember(id string, now, expires time.Time) (seen bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until, ok := c.entries[id]; ok && until.After(now) {
		return true
	}
	if len(c.entries) >= maxRemembered {
		c.evict(now)
	}
	c.entries[id] = expires
	return false
}

// Has reports whether id is remembered and not yet expired.
func (c *replayCache) Has(id string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.entries[id]
	return ok && until.After(now)
}

// evict drops the expired entries, and when the cache is still full the soonest-expiring tenth.
func (c *replayCache) evict(now time.Time) {
	type entry struct {
		id    string
		until time.Time
	}
	live := make([]entry, 0, len(c.entries))
	for id, until := range c.entries {
		if !until.After(now) {
			delete(c.entries, id)
			continue
		}
		live = append(live, entry{id, until})
	}
	if len(c.entries) < maxRemembered {
		return
	}
	sort.Slice(live, func(i, j int) bool { return live[i].until.Before(live[j].until) })
	for _, e := range live[:len(live)/10+1] {
		delete(c.entries, e.id)
	}
}
