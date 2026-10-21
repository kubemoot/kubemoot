// Package bus is the sidecar's side of the discussion bus: it reads thread messages, decides
// whether one selects this agent, hands the turn to the harness, keeps the message alive while
// the turn runs, and publishes the signals. The wire is the operator's generated contract
// (operator/pkg/bus), unchanged.
package bus

import (
	"slices"
	"sync"
	"time"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
)

// The numbers the legacy agent-runtime uses. Admission follows the legacy rules, with one difference
// in order: the inner circle is checked before the rate permit, so an agent the coordinator did not
// convene spends no permit.
const (
	maxSeen          = 5000
	maxThreadEntries = 200
	maxPerThread     = 3
	maxPerMinute     = 10
	rateWindow       = time.Minute
	maxTrackedThread = 1000
)

// Message types that start or end a turn, by name.
const (
	typeAdvisoryReady = kbus.DiscussionMessageMessageTypeAdvisoryReady
	typeReviewReady   = kbus.DiscussionMessageMessageTypeReviewReady
	typeFollowUp      = kbus.DiscussionMessageMessageType("follow_up")
	typeReply         = kbus.DiscussionMessageMessageTypeReply
)

// RoleAnalyst acts in the review phase only.
const RoleAnalyst = "analyst"

// triggers returns the message types an agent of role acts on: an analyst waits for the review
// phase, everyone else for the coordinator's advisory or a follow-up.
func triggers(role string) []kbus.DiscussionMessageMessageType {
	if role == RoleAnalyst {
		return []kbus.DiscussionMessageMessageType{typeReviewReady}
	}
	return []kbus.DiscussionMessageMessageType{typeAdvisoryReady, typeFollowUp, typeReply}
}

// Verdict is what admission decided about one message.
type Verdict int

// The verdicts, in the order admission asks.
const (
	// Run means start a turn.
	Run Verdict = iota
	// Ignore means the message is not for this agent: its own, not a trigger, a closed thread.
	Ignore
	// Duplicate means the message was seen before.
	Duplicate
	// NotSelected means the coordinator convened other agents.
	NotSelected
	// Limited means the thread or the minute is over its limit.
	Limited
	// Stop means the thread ended or a person stopped it: abandon a running turn for it.
	Stop
)

// Admission decides, for each message, whether it starts a turn. It keeps the state that
// decision needs: the ids seen, the threads closed, the thread so far, the rate counters. It is
// safe for concurrent use.
type Admission struct {
	Agent string
	Role  string
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu       sync.Mutex
	seen     map[string]struct{}
	order    []string
	closed   map[string]bool
	threads  map[string][]a2a.ThreadEntry
	perThr   map[string]int
	window   time.Time
	inWindow int
}

func (a *Admission) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Judge decides one message. It records the message in the thread either way, so the harness
// sees the whole conversation.
func (a *Admission) Judge(m kbus.DiscussionMessage) Verdict {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	if m.AgentName == a.Agent {
		return Ignore
	}
	if m.MessageID != "" {
		if _, dup := a.seen[m.MessageID]; dup {
			return Duplicate
		}
		a.remember(m.MessageID)
	}
	a.track(m)
	if v, ended := a.ended(m); ended {
		return v
	}
	if !slices.Contains(triggers(a.Role), m.MessageType) || a.closed[m.ThreadID] {
		return Ignore
	}
	if !selected(m, a.Agent) {
		return NotSelected
	}
	if !a.permit(m.ThreadID) {
		return Limited
	}
	return Run
}

func (a *Admission) init() {
	if a.seen != nil {
		return
	}
	a.seen, a.closed = map[string]struct{}{}, map[string]bool{}
	a.threads, a.perThr = map[string][]a2a.ThreadEntry{}, map[string]int{}
}

func (a *Admission) remember(id string) {
	a.seen[id] = struct{}{}
	a.order = append(a.order, id)
	if len(a.order) > maxSeen {
		delete(a.seen, a.order[0])
		a.order = a.order[1:]
	}
}

// track records the message in its thread and notes when the thread closes or a person reopens it.
func (a *Admission) track(m kbus.DiscussionMessage) {
	switch {
	case m.MessageType == kbus.DiscussionMessageMessageTypeSynthesis ||
		m.MessageType == kbus.DiscussionMessageMessageTypeThreadClose:
		if len(a.closed) >= maxTrackedThread {
			a.closed = map[string]bool{}
		}
		a.closed[m.ThreadID] = true
	case m.MessageType == typeReply && m.AgentName == "human":
		delete(a.closed, m.ThreadID)
	}
	entry := a2a.ThreadEntry{Agent: m.AgentName, Type: string(m.MessageType), Content: m.Content}
	entries := append(a.threads[m.ThreadID], entry)
	if len(entries) > maxThreadEntries {
		entries = entries[len(entries)-maxThreadEntries:]
	}
	a.threads[m.ThreadID] = entries
	if len(a.threads) > maxTrackedThread {
		a.threads = map[string][]a2a.ThreadEntry{m.ThreadID: entries}
	}
}

// ended reports a message that ends work on its thread.
func (a *Admission) ended(m kbus.DiscussionMessage) (Verdict, bool) {
	switch m.MessageType {
	case kbus.DiscussionMessageMessageTypeStopRequested, kbus.DiscussionMessageMessageTypeThreadClose,
		kbus.DiscussionMessageMessageTypeSynthesis:
		return Stop, true
	}
	return Ignore, false
}

// selected is true when the message does not name an inner circle, or names this agent in it. A
// message with no circle (or an empty one) is open to every agent of the channel.
func selected(m kbus.DiscussionMessage, agent string) bool {
	circle, _ := m.Metadata["innerCircle"].([]any)
	if len(circle) == 0 {
		return true
	}
	for _, c := range circle {
		if name, _ := c.(string); name == agent {
			return true
		}
	}
	return false
}

// permit takes one rate-limit permit for the thread: at most maxPerThread turns per thread and
// maxPerMinute per minute for the agent.
func (a *Admission) permit(thread string) bool {
	if a.perThr[thread] >= maxPerThread {
		return false
	}
	now := a.now()
	if now.Sub(a.window) > rateWindow {
		a.window, a.inWindow = now, 0
	}
	if a.inWindow >= maxPerMinute {
		return false
	}
	a.inWindow++
	a.perThr[thread]++
	if len(a.perThr) > maxTrackedThread {
		a.perThr = map[string]int{thread: a.perThr[thread]}
	}
	return true
}

// Forget undoes the admission of a message whose turn could not be finished (its terminal signal
// was not published): the id is no longer seen and the rate permit is given back, so the message
// can be delivered and run again.
func (a *Admission) Forget(id, thread string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.init()
	delete(a.seen, id)
	if n := a.perThr[thread]; n > 0 {
		a.perThr[thread] = n - 1
	}
	if a.inWindow > 0 {
		a.inWindow--
	}
}

// Thread returns the thread so far, oldest first.
func (a *Admission) Thread(id string) []a2a.ThreadEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.threads[id])
}
