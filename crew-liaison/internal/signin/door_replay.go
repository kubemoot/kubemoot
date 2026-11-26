package signin

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/kubemoot/kubemoot/crew-liaison/internal/tokenstore"
)

// ReplaySet remembers identifiers until a time. The door's codes and refresh tokens are sealed
// values it keeps no record of, so this memory is what makes a code single use and a refresh
// token rotate: a second sight of an id is refused. With several replicas the memory must be
// shared (NewSharedReplay); the process-local one fits one replica and tests.
type ReplaySet interface {
	// Remember records id until expires and reports whether it was already there. A set that
	// cannot answer reports true, so a doubt refuses and never admits.
	Remember(id string, now, expires time.Time) (seen bool)
	// Has reports whether id is remembered and not yet expired, true when the set cannot answer.
	Has(id string, now time.Time) bool
}

const (
	// replayPrincipal and the account are the store key of a remembered id.
	replayPrincipal = "door-replay"
	// replayWait bounds one call to the shared store.
	replayWait    = 3 * time.Second
	minReplayLife = time.Second
)

// sharedReplay is a ReplaySet in a token store every replica reads, so a code used on one
// replica is refused on another.
type sharedReplay struct {
	store tokenstore.Store
	log   logr.Logger
}

// NewSharedReplay returns a ReplaySet that keeps its marks in store.
func NewSharedReplay(store tokenstore.Store, log logr.Logger) (ReplaySet, error) {
	if store == nil {
		return nil, errors.New("a shared replay set needs a store")
	}
	return &sharedReplay{store: store, log: log}, nil
}

// Remember implements ReplaySet. The create is the store's one atomic check, so the first of
// two replicas to see an id wins and the other sees it already there.
func (r *sharedReplay) Remember(id string, now, expires time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), replayWait)
	defer cancel()
	ttl := max(expires.Sub(now), minReplayLife)
	_, err := r.store.Put(ctx, tokenstore.Record{Principal: replayPrincipal, Account: id}, ttl, tokenstore.MustNotExist)
	switch {
	case err == nil:
		return false
	case errors.Is(err, tokenstore.ErrConflict):
		return true
	}
	r.log.Info("the shared replay memory did not answer; refusing", "error", err.Error())
	return true
}

// Has implements ReplaySet.
func (r *sharedReplay) Has(id string, _ time.Time) bool {
	ctx, cancel := context.WithTimeout(context.Background(), replayWait)
	defer cancel()
	_, err := r.store.Get(ctx, replayPrincipal, id)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return false
	}
	if err != nil {
		r.log.Info("the shared replay memory did not answer; refusing", "error", err.Error())
	}
	return true
}

// maxRemembered bounds the replay cache. When it is full the soonest-expiring entries are
// dropped first, so memory cannot be grown by a flood of requests.
const maxRemembered = 20000

// replayCache is the process-local ReplaySet. With several replicas each has its own memory;
// the sealed values' short lives bound that.
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
