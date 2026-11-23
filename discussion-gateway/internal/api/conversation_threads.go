package api

import (
	"context"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// The message types and the outcome the state of a thread is read from.
const (
	msgStopRequested = "stop_requested"
	msgThreadPause   = "thread_pause"
	msgThreadResume  = "thread_resume"
	// metaOutcome and outcomeStopped mark the synthesis and the close of a turn a person stopped.
	metaOutcome    = "outcome"
	outcomeStopped = "stopped"
)

// conversationThread is a thread of a conversation, as the discussion stream shows it.
type conversationThread struct {
	id       string
	channel  string
	question string
	// startedAt and endedAt are when the stream got the thread_start and the thread_close.
	startedAt time.Time
	endedAt   time.Time
	// closed is true from the thread's thread_close until a person's reply reopens it.
	closed bool
	// paused is true after a thread_pause until a thread_resume.
	paused bool
	// stopped is true when the answer the thread closed with is the stopped text.
	stopped bool
	// stopRequested is true after a stop_requested the thread has not closed on yet.
	stopRequested bool
}

// open is true until the thread's thread_close, and again after a person's reply reopens it.
func (t conversationThread) open() bool { return !t.closed }

// state is where the turn stands now.
func (t conversationThread) state() bus.TurnState {
	switch {
	case t.closed && t.stopped:
		return bus.TurnStateStopped
	case t.closed:
		return bus.TurnStateFinished
	case t.paused:
		return bus.TurnStatePaused
	default:
		return bus.TurnStateRunning
	}
}

// apply folds one message of the thread, in stream order, into its state.
func (t *conversationThread) apply(m discussMsg) {
	switch m.data.MessageType {
	case msgSynthesis:
		t.stopped = metaString(m.data, metaOutcome) == outcomeStopped
	case msgThreadClose:
		t.closed, t.paused, t.stopRequested, t.endedAt = true, false, false, m.published
	case msgThreadPause:
		t.paused = t.paused || t.open()
	case msgThreadResume:
		t.paused = false
	case msgStopRequested:
		t.stopRequested = t.stopRequested || t.open()
	case msgReply:
		if m.data.AgentName == agentHuman {
			t.closed, t.stopped, t.stopRequested, t.endedAt = false, false, false, time.Time{}
		}
	}
}

// threadReducer follows the threads of one conversation through the messages of the discussion
// stream, oldest first.
type threadReducer struct {
	conversation string
	byID         map[string]*conversationThread
	order        []string
	// lastSeq is the stream sequence of the last message observed, where a follower continues.
	lastSeq uint64
}

func newThreadReducer(conversation string) *threadReducer {
	return &threadReducer{conversation: conversation, byID: map[string]*conversationThread{}}
}

// observe folds one message: a thread_start of the conversation begins a thread, any other
// message goes to the thread it names when the reducer follows it.
func (r *threadReducer) observe(m discussMsg) {
	r.lastSeq = max(r.lastSeq, m.seq)
	thread := m.data.ThreadID
	if m.data.MessageType != msgThreadStart {
		if t := r.byID[thread]; t != nil {
			t.apply(m)
		}
		return
	}
	if conversationOf(m.data) != r.conversation || !threadToken(thread) || r.byID[thread] != nil {
		return
	}
	r.byID[thread] = &conversationThread{id: thread, channel: channelOf(m.data), question: questionOf(m.data),
		startedAt: m.published}
	r.order = append(r.order, thread)
}

// threads are the threads followed, oldest first.
func (r *threadReducer) threads() []conversationThread {
	out := make([]conversationThread, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.byID[id])
	}
	return out
}

// threadsOfConversation is the threads of the conversation the discussion stream still holds,
// oldest first. A thread's channel is the one its thread_start was published on.
func threadsOfConversation(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, id string,
) ([]conversationThread, error) {
	r, err := readConversation(ctx, js, scope, id)
	return r.threads(), err
}

// readConversation reads the discussion stream and returns the reducer of the conversation,
// holding the stream sequence it has read up to.
func readConversation(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, id string,
) (*threadReducer, error) {
	r := newThreadReducer(id)
	err := readSubject(ctx, js, scope.DiscussFilter(), r.observe)
	return r, err
}
