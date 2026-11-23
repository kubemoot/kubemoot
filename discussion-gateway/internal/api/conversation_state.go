package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// The state of a conversation is read from what the bus already holds: the crew's request queue
// (a question no agent has seen) and the discussion stream (a turn's thread, from its thread_start
// to its thread_close, with the pauses, resumes and stop requests between). Nothing is stored
// for it and nothing is timed: a turn is in the state its last message put it in.

// opState names the state read in the audit record.
const opState = "state"

// conversationState answers GET /api/v1/discussions/{crew}/{conversationId}/state.
func (h *Handler) conversationState(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	id, ok := conversationIDOf(w, r)
	if !ok || !h.mayRead(w, r, scope, id, opState) {
		return
	}
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*controlTimeout)
	defer cancel()
	read, waiting, err := readTurns(ctx, js, scope, id)
	if err != nil {
		handlerLog.Error(err, "Could not read the state of a conversation", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream or the request queue")
		return
	}
	threads := read.threads()
	if len(threads) == 0 && len(waiting) == 0 {
		httpError(w, http.StatusNotFound, errConversationNotFound)
		return
	}
	writeJSON(w, http.StatusOK, stateOf(id, threads, waiting))
}

// readTurns reads the conversation's threads from the discussion stream and its questions that
// have not begun from the request queue.
func readTurns(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, id string,
) (*threadReducer, []pendingQuestion, error) {
	read, err := readConversation(ctx, js, scope, id)
	if err != nil {
		return nil, nil, err
	}
	queue, err := readRequestQueue(ctx, js, scope)
	if err != nil {
		return nil, nil, err
	}
	return read, unstarted(queue, id, read.threads()), nil
}

// stateOf is the conversation's state and its turns: the threads, oldest first, then the
// questions that have not begun.
func stateOf(id string, threads []conversationThread, waiting []pendingQuestion) bus.ConversationState {
	turns := make([]bus.TurnStatus, 0, len(threads)+len(waiting))
	for _, t := range threads {
		turns = append(turns, turnOfThread(t))
	}
	for _, q := range waiting {
		turns = append(turns, turnOfQueued(q))
	}
	return bus.ConversationState{ConversationID: id, State: conversationStateFrom(turns), Turns: turns}
}

// turnOfThread is the status of a turn whose thread the stream holds.
func turnOfThread(t conversationThread) bus.TurnStatus {
	turn := bus.TurnStatus{ThreadID: t.id, Question: t.question, State: t.state(),
		StopRequested: t.stopRequested, StartedAt: rfc3339(t.startedAt)}
	if t.closed {
		turn.EndedAt = rfc3339(t.endedAt)
	}
	return turn
}

// turnOfQueued is the status of a question no thread answers yet. One a coordinator has taken is
// running; one still in the queue says how many questions are ahead of it.
func turnOfQueued(q pendingQuestion) bus.TurnStatus {
	if q.taken {
		return bus.TurnStatus{Question: q.req.Message, State: bus.TurnStateRunning}
	}
	return bus.TurnStatus{Question: q.req.Message, State: bus.TurnStateQueued, QueuePosition: int64(q.ahead + 1)}
}

// conversationStateFrom is the state of the conversation as a whole: running when any turn is,
// else paused, else queued, else the state of the latest turn.
func conversationStateFrom(turns []bus.TurnStatus) bus.TurnState {
	for _, want := range []bus.TurnState{bus.TurnStateRunning, bus.TurnStatePaused, bus.TurnStateQueued} {
		for _, t := range turns {
			if t.State == want {
				return want
			}
		}
	}
	if len(turns) == 0 {
		return bus.TurnStateFinished
	}
	return turns[len(turns)-1].State
}

// rfc3339 is t in UTC, or "" for the zero time.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
