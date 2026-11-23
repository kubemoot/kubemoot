package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// Stopping, pausing and resuming a conversation, and deleting it. Each is checked by the
// owner rule every other conversation endpoint uses (mayUse): the owner, or an operator who
// may update or delete crews/conversations and gives a reason. The gateway sends the control
// messages the coordinator already understands, as itself and never as a person.

const (
	// controlTimeout bounds the reads and publishes of one control request.
	controlTimeout = 15 * time.Second
	// errNoRunningDiscussion answers a control for a conversation with nothing running.
	errNoRunningDiscussion = "the conversation has no running discussion"
	// errStillRunning answers a delete while a discussion runs; stop it first, or force.
	errStillRunning = "the conversation has a running discussion; stop it first, or pass force=true"
)

// controlOf says what each control endpoint sends: the message type, its text, and the verb
// an operator needs.
type controlOf struct {
	action      bus.ConversationControlAction
	messageType bus.DiscussionMessageMessageType
	content     string
	op          string
}

var (
	stopControl = controlOf{bus.ConversationControlActionStop, bus.DiscussionMessageMessageTypeStopRequested,
		"Stopped from the discussion gateway", opStop}
	pauseControl = controlOf{bus.ConversationControlActionPause, bus.DiscussionMessageMessageTypeThreadPause,
		"Paused from the discussion gateway", opPause}
	resumeControl = controlOf{bus.ConversationControlActionResume, bus.DiscussionMessageMessageTypeThreadResume,
		"Resumed from the discussion gateway", opResume}
)

// channelOf is the channel a message was published on, or the broadcast channel.
func channelOf(data bus.DiscussionMessage) string {
	if data.Channel != "" && threadToken(data.Channel) {
		return data.Channel
	}
	return errorChannel
}

// running are the threads that are open.
func running(threads []conversationThread) []conversationThread {
	var out []conversationThread
	for _, t := range threads {
		if t.open() {
			out = append(out, t)
		}
	}
	return out
}

func threadIDs(threads []conversationThread) []string {
	out := make([]string, 0, len(threads))
	for _, t := range threads {
		out = append(out, t.id)
	}
	return out
}

// conversationIDOf reads and validates the conversation in the request's path.
func conversationIDOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("conversationId")
	if !validConversationID(id) {
		httpError(w, http.StatusBadRequest, errInvalidConversationID)
		return "", false
	}
	return id, true
}

// beginControl reads the crew and conversation of a control request and applies the owner
// rule. A stop also drops the credential kept for the conversation, once the caller is known
// to be allowed to stop it: a stopped turn makes no more tool calls.
func (h *Handler) beginControl(w http.ResponseWriter, r *http.Request, c controlOf) (crewscope.Scope, string, bool) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return scope, "", false
	}
	id, ok := conversationIDOf(w, r)
	if !ok || !h.mayUse(w, r, scope, id, c.op, verbUpdate) {
		return scope, "", false
	}
	if c.action == bus.ConversationControlActionStop {
		h.dropCredential(id)
	}
	return scope, id, true
}

// controlHandler answers a stop, pause or resume: the control goes to each running thread of
// the conversation. A conversation with no thread is answered as unknown, and one with none
// running is a conflict.
func (h *Handler) controlHandler(c controlOf) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, id, ok := h.beginControl(w, r, c)
		if !ok {
			return
		}
		js, ok := h.jetStreamFor(w)
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), controlTimeout)
		defer cancel()
		threads, err := threadsOfConversation(ctx, js, scope, id)
		if err != nil {
			handlerLog.Error(err, "Could not read the conversation's threads", "crew", scope.Crew)
			httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream")
			return
		}
		if len(threads) == 0 {
			httpError(w, http.StatusNotFound, errConversationNotFound)
			return
		}
		open := running(threads)
		if len(open) == 0 {
			httpError(w, http.StatusConflict, errNoRunningDiscussion)
			return
		}
		for _, thread := range open {
			if err := h.sendControl(ctx, scope, thread, c); err != nil {
				handlerLog.Error(err, "Could not send a control", "crew", scope.Crew, "thread", thread.id, "action", c.op)
				httpError(w, http.StatusServiceUnavailable, "could not send the control")
				return
			}
		}
		handlerLog.Info("Sent a control", "crew", scope.Crew, "conversationId", id, "action", c.op)
		writeJSON(w, http.StatusOK, bus.ConversationControl{ConversationID: id, Action: c.action, Threads: threadIDs(open)})
	}
}

// sendControl publishes the control on the thread's own subject, from the gateway.
func (h *Handler) sendControl(ctx context.Context, scope crewscope.Scope, t conversationThread, c controlOf) error {
	msg := bus.DiscussionMessage{
		MessageID: uuid.New().String(), ThreadID: t.id, AgentName: agentNameGateway, MessageType: c.messageType,
		Content: c.content, Channel: t.channel, Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
	if c.messageType == bus.DiscussionMessageMessageTypeStopRequested {
		msg.Metadata = bus.DiscussionMessageMetadata{metaReason: "manual_stop"}
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return h.natsClient.Publish(ctx, scope.DiscussSubject(t.channel, t.id), data)
}

// deleteConversation answers DELETE /api/v1/discussions/{crew}/{conversationId}: it removes the
// conversation from every store that holds it and reports what it removed. A running
// discussion is a conflict unless force=true, since it would write its answer again.
func (h *Handler) deleteConversation(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	id, ok := conversationIDOf(w, r)
	if !ok || !h.mayUse(w, r, scope, id, opDelete, verbDelete) {
		return
	}
	h.dropCredential(id)
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*controlTimeout)
	defer cancel()
	threads, err := threadsOfConversation(ctx, js, scope, id)
	if err != nil {
		handlerLog.Error(err, "Could not read the conversation's threads", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream")
		return
	}
	if len(running(threads)) > 0 && r.URL.Query().Get("force") != valueTrue {
		httpError(w, http.StatusConflict, errStillRunning)
		return
	}
	if open := running(threads); len(open) > 0 {
		h.stopBeforeDelete(ctx, scope, open)
	}
	report := newEraser(js, scope, id).erase(ctx, threadIDs(threads))
	h.requests.forget(id)
	answerErasure(w, scope, id, report)
}

// answerErasure answers a delete with what it did: 500 with the report when a store could not be
// cleared (repeat the delete), 404 when the conversation was in no store, else 200 with the report.
func answerErasure(w http.ResponseWriter, scope crewscope.Scope, id string, report erasure) {
	switch {
	case len(report.Failed) > 0:
		writeJSON(w, http.StatusInternalServerError, report)
	case report.nothingFound():
		httpError(w, http.StatusNotFound, errConversationNotFound)
	default:
		handlerLog.Info("Deleted a conversation", "crew", scope.Crew, "conversationId", id)
		writeJSON(w, http.StatusOK, report.ConversationErasure)
	}
}

// stopBeforeDelete asks each running thread to stop, so a forced delete does not leave the
// coordinator working toward an answer it would write into a history that is gone. It is
// best effort: a thread that cannot be told is erased all the same.
func (h *Handler) stopBeforeDelete(ctx context.Context, scope crewscope.Scope, open []conversationThread) {
	for _, thread := range open {
		if err := h.sendControl(ctx, scope, thread, stopControl); err != nil {
			handlerLog.Error(err, "Could not stop a thread before deleting its conversation", "crew", scope.Crew)
		}
	}
}

// erasure is what a delete removed.
type erasure struct {
	bus.ConversationErasure
}

// nothingFound is true when the conversation was in no store.
func (e erasure) nothingFound() bool {
	d := e.Deleted
	return len(e.Threads) == 0 && d.History+d.Owner+d.DiscussionMessages+d.WorkspaceObjects+
		d.CoordinatorEvents+d.Scheduled+d.MemoryFacts == 0
}

// kept is what the gateway cannot remove for one conversation, each with why.
var kept = []string{
	"KUBEMOOT_REQUEST: a question still queued cannot be removed for one conversation; it expires with the stream",
	"logs and backups: erasing from NATS does not erase them",
}

// threadToken reports whether a thread id from a message or a history can stand as one subject
// token and one object-name part. Anything else is never put in a subject filter or a prefix,
// where a wildcard would reach other conversations' data.
func threadToken(thread string) bool {
	return thread != "" && !strings.ContainsAny(thread, ".*> \t\r\n/")
}

// validThreads returns the sorted distinct thread ids that are a single token.
func validThreads(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, v := range values {
		if _, dup := seen[v]; threadToken(v) && !dup {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
