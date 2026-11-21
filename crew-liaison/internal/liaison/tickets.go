// Package liaison exposes one or more Kubemoot crews to MCP clients as single agents.
// A client asks a question and receives a ticket; the crew deliberates in the cluster;
// the client collects the synthesized answer with the ticket. Nothing about the
// discussion's substance crosses the boundary: no agent names, signals, findings, or
// tool output. The one thing a pending ticket does reveal is an anonymous count of
// contributions so far, so a polling client can tell a live discussion from a stuck
// one; it names nobody and says nothing about what was said.
package liaison

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// ticketIDBytes is the entropy of a ticket id: 128 random bits. The id is the only
// proof a caller created or was handed a ticket, so it must not be guessable.
const ticketIDBytes = 16

// newTicketID returns a fresh unguessable id (32 hex characters from crypto/rand).
func newTicketID() string {
	b := make([]byte, ticketIDBytes)
	if _, err := rand.Read(b); err != nil {
		panic("liaison: no system randomness for a ticket id: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// State is a ticket's lifecycle position.
type State string

const (
	// StatePending means the crew is still deliberating.
	StatePending State = "pending"
	// StateAnswered means the synthesis is available.
	StateAnswered State = "answered"
	// StateFailed means the discussion ended without a synthesis.
	StateFailed State = "failed"
)

// Ticket tracks one question through one crew discussion.
type Ticket struct {
	ID           string
	Crew         string
	Namespace    string
	Conversation string
	question     string // never exposed; only compared, to deduplicate retries

	mu            sync.Mutex
	state         State
	contributions int
	answer        string
	failure       string
	created       time.Time
	updated       time.Time
	done          chan struct{}
}

// TicketView is a point-in-time copy of a ticket, safe to return to callers.
type TicketView struct {
	ID            string    `json:"ticket"`
	Crew          string    `json:"crew"`
	Namespace     string    `json:"namespace"`
	Conversation  string    `json:"conversationId,omitempty"`
	State         State     `json:"status"`
	Contributions int       `json:"contributions"`
	Answer        string    `json:"answer,omitempty"`
	Error         string    `json:"error,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
}

// Done is closed when the ticket leaves the pending state.
func (t *Ticket) Done() <-chan struct{} { return t.done }

// Contribution records that one more agent has spoken in the discussion.
func (t *Ticket) Contribution(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contributions++
	t.updated = now
}

// Answer stores the synthesis and settles the ticket. A second call is ignored.
func (t *Ticket) Answer(text string, now time.Time) {
	t.settle(StateAnswered, text, "", now)
}

// Fail settles the ticket without an answer. A call after Answer is ignored.
func (t *Ticket) Fail(reason string, now time.Time) {
	t.settle(StateFailed, "", reason, now)
}

func (t *Ticket) settle(state State, answer, failure string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state != StatePending {
		return
	}
	t.state = state
	t.answer = answer
	t.failure = failure
	t.updated = now
	close(t.done)
}

// Snapshot copies the ticket's current state.
func (t *Ticket) Snapshot() TicketView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TicketView{
		ID:            t.ID,
		Crew:          t.Crew,
		Namespace:     t.Namespace,
		Conversation:  t.Conversation,
		State:         t.state,
		Contributions: t.contributions,
		Answer:        t.answer,
		Error:         t.failure,
		Created:       t.created,
		Updated:       t.updated,
	}
}

// Store keeps tickets in memory. Settled tickets expire after the TTL; a pending
// ticket expires after twice the TTL, which bounds a follower that never returns.
type Store struct {
	mu      sync.Mutex
	tickets map[string]*Ticket
	ttl     time.Duration
	now     func() time.Time
}

// NewStore returns an empty store whose tickets expire after ttl.
func NewStore(ttl time.Duration) *Store {
	return &Store{tickets: make(map[string]*Ticket), ttl: ttl, now: time.Now}
}

// Create registers a pending ticket for a started discussion.
func (s *Store) Create(crew, namespace, conversation, question string) *Ticket {
	now := s.now()
	t := &Ticket{
		ID:           newTicketID(),
		Crew:         crew,
		Namespace:    namespace,
		Conversation: conversation,
		question:     question,
		state:        StatePending,
		created:      now,
		updated:      now,
		done:         make(chan struct{}),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[t.ID] = t
	return t
}

// Rejoin returns the still-pending ticket the caller names by id, when it is for the same
// question to the same crew, so a client that retries after a timeout rejoins its own
// discussion instead of starting another. The id is the proof the caller holds the
// ticket; a question's text alone never matches, so one client cannot join another's
// discussion by asking the same thing. An unknown, settled or mismatched id returns false
// and says nothing about why.
func (s *Store) Rejoin(id, crew, namespace, question string) (*Ticket, bool) {
	t, ok := s.Get(id)
	if !ok || t.Crew != crew || t.Namespace != namespace || t.question != question ||
		t.Snapshot().State != StatePending {
		return nil, false
	}
	return t, true
}

// Get returns the ticket with the given id. Holding the id is the only credential.
func (s *Store) Get(id string) (*Ticket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tickets[id]
	return t, ok
}

// Sweep removes expired tickets and returns how many it removed.
func (s *Store) Sweep() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for id, t := range s.tickets {
		if s.expired(t, now) {
			delete(s.tickets, id)
			removed++
		}
	}
	return removed
}

func (s *Store) expired(t *Ticket, now time.Time) bool {
	v := t.Snapshot()
	limit := s.ttl
	if v.State == StatePending {
		limit = 2 * s.ttl
	}
	return now.Sub(v.Updated) > limit
}

// Len returns the number of tickets held.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}
