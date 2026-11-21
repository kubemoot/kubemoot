// Package grants holds, in memory and for one turn, the bearer token of a person who asked a
// crew a question, so a tool the crew calls for that question can call its service as that
// person. A grant is never written to a file, a Secret, a log, a trace or the bus: the store
// is a bounded map in the gateway process, and every grant ends at the earliest of its
// token's own expiry, the end of the turn and a fixed ceiling.
package grants

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
)

// Defaults of the in-memory store.
const (
	// DefaultMaxGrants bounds how many conversations hold a grant at once.
	DefaultMaxGrants = 1000
	// MaxLifetime is the longest a grant is held from the moment it is written or renewed.
	MaxLifetime = 30 * time.Minute
	// expiredKept is how long an expired grant is remembered, without its token, so a lease
	// for it is answered "expired" and not "unknown".
	expiredKept = 10 * time.Minute
	redacted    = "[redacted]"
)

var (
	// ErrNotFound means the conversation holds no grant.
	ErrNotFound = errors.New("no grant for the conversation")
	// ErrExpired means the conversation's grant ran out; the grant returned with it has no token.
	ErrExpired = errors.New("the grant has expired")
	// ErrKeepHeld is what a PutIf decision returns to leave the held grant as it is.
	ErrKeepHeld = errors.New("keep the held grant")
	// ErrFull means the store holds as many live grants as it may.
	ErrFull = errors.New("the grant store is full")
)

// Grant is one person's credential for one conversation. The token is unexported and the
// type has no JSON tags, so a value that holds one cannot print or marshal it by accident;
// Token is the one way out, for the lease that must hand it over.
type Grant struct {
	// Owner is the principal the conversation was started for.
	Owner bus.Principal
	// Verifier is the name of the verifier that accepted the token.
	Verifier string
	// Expires is when the grant stops being usable.
	Expires time.Time
	token   string
}

// New returns a grant of token for owner, accepted by the named verifier, usable until expires.
func New(owner bus.Principal, verifier, token string, expires time.Time) Grant {
	return Grant{Owner: owner, Verifier: verifier, Expires: expires, token: token}
}

// Token returns the bearer token, empty once the grant expired. Use it only to answer the
// lease it is for; never log it, trace it, publish it or store it.
func (g Grant) Token() string { return g.token }

// String is redacted.
func (g Grant) String() string {
	ends := g.Expires.UTC().Format(time.RFC3339)
	return fmt.Sprintf("grant{verifier: %s, expires: %s, token: %s}", g.Verifier, ends, redacted)
}

// GoString is String, so %#v is as safe as %v.
func (g Grant) GoString() string { return g.String() }

// Format is String for every verb, so %x, %q or %+v cannot reveal the bytes.
func (g Grant) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(g.String())) }

// Store holds grants by conversation id. The in-memory Memory is the only implementation the
// gateway ships; the interface is the seam where an installer's own store (their key,
// retention and erasure) can replace it.
type Store interface {
	// Put holds the grant for the conversation, replacing any held. It returns ErrFull when
	// the store is at its bound and the conversation has none yet.
	Put(conversationID string, g Grant) error
	// PutIf holds g for the conversation when decide, called with the held grant and the error
	// Get would return, with nothing else able to change the store meanwhile, returns nil.
	// ErrKeepHeld leaves the held grant and returns nil; any other error is returned and
	// nothing is written.
	PutIf(conversationID string, g Grant, decide func(held Grant, heldErr error) error) error
	// Get returns the conversation's grant: ErrNotFound when there is none, and ErrExpired,
	// with the grant's owner and no token, when it ran out.
	Get(conversationID string) (Grant, error)
	// Delete drops the conversation's grant. Dropping one that is not held is not an error.
	Delete(conversationID string)
}

// Memory is a Store in a mutex-guarded map, bounded in size. Nothing leaves the process.
type Memory struct {
	mu     sync.Mutex
	grants map[string]Grant
	max    int
	now    func() time.Time
}

// NewMemory returns an empty store holding at most max grants, DefaultMaxGrants when max is
// not positive. now is the clock; nil means time.Now.
func NewMemory(maxGrants int, now func() time.Time) *Memory {
	if maxGrants <= 0 {
		maxGrants = DefaultMaxGrants
	}
	if now == nil {
		now = time.Now
	}
	return &Memory{grants: map[string]Grant{}, max: maxGrants, now: now}
}

// Put implements Store.
func (m *Memory) Put(id string, g Grant) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.putLocked(id, g)
}

func (m *Memory) putLocked(id string, g Grant) error {
	if _, held := m.grants[id]; !held && len(m.grants) >= m.max {
		m.sweepLocked(true)
		if len(m.grants) >= m.max {
			return ErrFull
		}
	}
	m.grants[id] = g
	return nil
}

// PutIf implements Store.
func (m *Memory) PutIf(id string, g Grant, decide func(held Grant, heldErr error) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, heldErr := m.getLocked(id)
	if err := decide(held, heldErr); err != nil {
		if errors.Is(err, ErrKeepHeld) {
			return nil
		}
		return err
	}
	return m.putLocked(id, g)
}

// Get implements Store. An expired grant is stripped of its token on the spot.
func (m *Memory) Get(id string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.getLocked(id)
}

func (m *Memory) getLocked(id string) (Grant, error) {
	g, ok := m.grants[id]
	if !ok {
		return Grant{}, ErrNotFound
	}
	if g.expired(m.now()) {
		g.token = ""
		m.grants[id] = g
		return g, ErrExpired
	}
	return g, nil
}

// Delete implements Store.
func (m *Memory) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.grants, id)
}

// Len is how many conversations hold a grant, expired and not yet forgotten ones included.
func (m *Memory) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.grants)
}

// Sweep strips the token from every expired grant and forgets those expired for a while.
func (m *Memory) Sweep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(false)
}

// Run sweeps every interval until stop is closed. A token never outlives its expiry by more
// than one interval even when nobody asks for it.
func (m *Memory) Run(stop <-chan struct{}, interval time.Duration) {
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			m.Sweep()
		case <-stop:
			return
		}
	}
}

// sweepLocked strips expired tokens and forgets expired grants: those expired for a while, or
// every expired one when dropAll says room is needed.
func (m *Memory) sweepLocked(dropAll bool) {
	now := m.now()
	for id, g := range m.grants {
		switch {
		case !g.expired(now):
		case dropAll || now.Sub(g.Expires) > expiredKept:
			delete(m.grants, id)
		default:
			g.token = ""
			m.grants[id] = g
		}
	}
}

func (g Grant) expired(now time.Time) bool { return !now.Before(g.Expires) }
