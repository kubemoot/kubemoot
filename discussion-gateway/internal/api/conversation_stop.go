package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// A stop does what it says or says it could not. A question still in the queue is removed from
// it. A running turn is sent the stop, and the answer waits for the turn's own thread_close with
// the stopped text (the coordinator closes it as soon as it reads the stop), so the stop reports
// what happened, not what was asked.

// stopConfirmWait is the most a stop waits for the coordinator to confirm. It is a safety net
// for a coordinator that does not answer; a coordinator that does ends the wait the moment the
// turn's thread closes.
var stopConfirmWait = 30 * time.Second

// Plain-words answers of a stop.
const (
	msgStoppedTurn = "Stopped. The crew abandoned the turn and its text says it was stopped; " +
		"what the crew had gathered stays in the trace."
	msgCancelledTurn = "Cancelled a question that was still waiting in the queue; the crew never saw it."
	msgNothingToStop = "Nothing to stop: every turn of the conversation had already finished."
	msgNotConfirmed  = "Asked the crew to stop, and it did not confirm in time; the work may still be running."
	msgJustTaken     = "The crew has just taken the question and its discussion has not begun, " +
		"so there is no turn to stop yet; stop again in a moment."
	msgAlsoNotBegun     = " A question the crew had just taken has not begun; stop again in a moment."
	msgQueuedRemovedFmt = " %d queued question(s) removed from the queue."
)

// stopResult is what a stop did.
type stopResult struct {
	// cancelled is how many queued questions were removed from the queue.
	cancelled int
	// notBegun is how many questions the crew took whose discussion has not begun: nothing to
	// send a stop to yet.
	notBegun int
	// sent are the running threads the stop was sent to.
	sent []string
	// stopped, finished and unconfirmed sort the sent threads by how each ended.
	stopped, finished, unconfirmed []string
}

// stopHandler answers POST /api/v1/discussions/{crew}/{conversationId}/stop.
func (h *Handler) stopHandler(w http.ResponseWriter, r *http.Request) {
	scope, id, ok := h.beginControl(w, r, stopControl)
	if !ok {
		return
	}
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
	defer cancel()
	read, waiting, err := readTurns(ctx, js, scope, id)
	if err != nil {
		handlerLog.Error(err, "Could not read the conversation to stop it", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream or the request queue")
		return
	}
	if len(read.threads()) == 0 && len(waiting) == 0 {
		httpError(w, http.StatusNotFound, errConversationNotFound)
		return
	}
	res, err := h.stop(ctx, r.Context(), js, scope, read, waiting)
	if err != nil {
		handlerLog.Error(err, "Could not stop the conversation", "crew", scope.Crew, "conversationId", id)
		httpError(w, http.StatusServiceUnavailable, "could not send the stop")
		return
	}
	handlerLog.Info("Stopped a conversation", "crew", scope.Crew, "conversationId", id,
		"cancelled", res.cancelled, "stopped", len(res.stopped), "unconfirmed", len(res.unconfirmed))
	answerStop(w, id, res)
}

// stop removes the conversation's queued questions, then sends the stop to each running thread and
// waits for each to close. ctx bounds the reads and the sends; the wait is bounded by
// stopConfirmWait within the request's own context.
func (h *Handler) stop(
	ctx, request context.Context, js jetstream.JetStream, scope crewscope.Scope, read *threadReducer,
	waiting []pendingQuestion,
) (stopResult, error) {
	var res stopResult
	var err error
	if res.cancelled, res.notBegun, err = cancelQueued(ctx, js, scope, waiting); err != nil {
		return res, err
	}
	open := running(read.threads())
	for _, thread := range open {
		if err := h.sendControl(ctx, scope, thread, stopControl); err != nil {
			return res, err
		}
	}
	res.sent = threadIDs(open)
	if len(open) == 0 {
		return res, nil
	}
	wait, cancel := context.WithTimeout(request, stopConfirmWait)
	defer cancel()
	final, err := awaitClosed(wait, js, scope, read, open)
	if err != nil {
		return res, err
	}
	res.sort(final)
	return res, nil
}

// sort puts each sent thread under how it ended: closed with the stopped text, closed with an
// answer that came before the stop, or still open.
func (res *stopResult) sort(final []conversationThread) {
	for _, t := range final {
		switch {
		case !t.closed:
			res.unconfirmed = append(res.unconfirmed, t.id)
		case t.stopped:
			res.stopped = append(res.stopped, t.id)
		default:
			res.finished = append(res.finished, t.id)
		}
	}
}

// cancelQueued removes the questions no coordinator has taken from the request queue, and counts
// them and the taken ones that have no thread yet. A question that was acknowledged meanwhile is
// gone already and is not counted.
func cancelQueued(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, waiting []pendingQuestion,
) (cancelled, notBegun int, err error) {
	var stream jetstream.Stream
	for _, q := range waiting {
		if q.taken {
			notBegun++
			continue
		}
		if stream == nil {
			if stream, err = js.Stream(ctx, requestStream); err != nil {
				return cancelled, notBegun, err
			}
		}
		if err = stream.DeleteMsg(ctx, q.seq); err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				continue
			}
			return cancelled, notBegun, err
		}
		// The coordinator may have pulled the question between the read and the delete: then the
		// crew has it, and it is not cancelled.
		delivered, derr := deliveredThrough(ctx, stream, requestConsumer(scope))
		if derr != nil {
			return cancelled, notBegun, derr
		}
		if delivered >= q.seq {
			notBegun++
		} else {
			cancelled++
		}
	}
	return cancelled, notBegun, nil
}

// awaitClosed follows the discussion stream after what read holds until every thread of sent is
// closed, or ctx is done, and returns the state each thread of sent reached. A wait that ends with
// ctx is not an error: the threads still open are the answer.
func awaitClosed(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, read *threadReducer, sent []conversationThread,
) ([]conversationThread, error) {
	consumer, err := js.CreateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		DeliverPolicy:     jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:       read.lastSeq + 1,
		FilterSubject:     scope.DiscussFilter(),
		InactiveThreshold: inactiveThreshold,
	})
	if err != nil {
		return nil, err
	}
	defer deleteConsumer(ctx, js, consumer.CachedInfo().Name)
	var mu sync.Mutex
	done := make(chan struct{})
	var once sync.Once
	consuming, err := consumer.Consume(func(msg jetstream.Msg) {
		m, ok := toDiscussMsg(msg)
		if !ok {
			return
		}
		mu.Lock()
		read.observe(m)
		closed := allClosed(read, sent)
		mu.Unlock()
		if closed {
			once.Do(func() { close(done) })
		}
	})
	if err != nil {
		return nil, err
	}
	defer consuming.Stop()
	select {
	case <-done:
	case <-ctx.Done():
	}
	mu.Lock()
	defer mu.Unlock()
	final := make([]conversationThread, 0, len(sent))
	for _, t := range sent {
		final = append(final, *read.byID[t.id])
	}
	return final, nil
}

// allClosed reports whether every thread of sent is closed in the reducer.
func allClosed(read *threadReducer, sent []conversationThread) bool {
	for _, t := range sent {
		if !read.byID[t.id].closed {
			return false
		}
	}
	return true
}

// answerStop writes what the stop did: 200 when the turn stopped, a queued question was
// cancelled, or there was nothing to stop; 202 when the stop was sent and not confirmed; 409 when
// the crew has just taken a question and there is not yet a turn to stop.
func answerStop(w http.ResponseWriter, id string, res stopResult) {
	out := bus.ConversationControl{ConversationID: id, Action: bus.ConversationControlActionStop,
		Threads: res.sent, Stopped: res.stopped, Cancelled: int64(res.cancelled)}
	status := http.StatusOK
	switch {
	case len(res.unconfirmed) > 0:
		out.Outcome, out.Message, status = bus.ConversationControlOutcomeUnconfirmed, msgNotConfirmed, http.StatusAccepted
	case len(res.stopped) > 0:
		out.Outcome, out.Message = bus.ConversationControlOutcomeStopped, msgStoppedTurn
	case res.cancelled > 0:
		out.Outcome, out.Message = bus.ConversationControlOutcomeCancelled, msgCancelledTurn
	case res.notBegun > 0:
		httpError(w, http.StatusConflict, msgJustTaken)
		return
	default:
		out.Outcome, out.Message = bus.ConversationControlOutcomeNothingToStop, msgNothingToStop
	}
	out.Message += stopAddenda(res, out.Outcome)
	writeJSON(w, status, out)
}

// stopAddenda is what a stop did besides its main outcome: queued questions removed along with a
// stopped turn, and a question the crew had just taken that has not begun.
func stopAddenda(res stopResult, outcome bus.ConversationControlOutcome) string {
	var more string
	if res.cancelled > 0 && outcome != bus.ConversationControlOutcomeCancelled {
		more += fmt.Sprintf(msgQueuedRemovedFmt, res.cancelled)
	}
	if res.notBegun > 0 {
		more += msgAlsoNotBegun
	}
	return more
}
