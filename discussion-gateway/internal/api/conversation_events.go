package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/operator/pkg/bus"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
)

// The conversation event stream, GET /api/v1/discussions/{crew}/events, tells a client which
// of its conversations has something new: a late answer, a scheduled reminder that fired, a
// scheduled follow-up. It carries no content; the client fetches the conversation. A caller
// receives events only for conversations they own, by the same owner rule as every other
// read; no client subscribes to NATS.

const (
	// opEvents names the operator's use of the event stream in the audit record.
	opEvents = "events"
	// maxTrackedThreads bounds the threads one event stream remembers the conversation of.
	maxTrackedThreads = 4096
	// maxSeenMessages bounds the message ids one event stream remembers, to drop the second
	// copy of a message published on two subjects.
	maxSeenMessages = 4096
	// msgReply and msgReminder are the discussion message types a scheduled fire publishes
	// on a thread that already exists.
	msgReply    = "reply"
	msgReminder = "reminder"
	// agentHuman is the sender of a reply, including the synthetic one a scheduled
	// follow-up publishes.
	agentHuman = "human"
	// metaScheduleID is set on every message a schedule publishes; metaKind says which kind.
	metaScheduleID = "scheduleId"
	metaKind       = "kind"
	kindReminder   = "reminder"
)

// newsOf is what a discussion message means to the conversation it belongs to, and false
// for a message that is no news (a signal, a phase, a thread_start of a question the client
// is already following).
func newsOf(data bus.DiscussionMessage) (bus.ConversationEventReason, bool) {
	scheduled := metaString(data, metaScheduleID) != ""
	switch {
	case data.MessageType == msgSynthesis:
		return bus.ConversationEventReasonAnswer, true
	case data.MessageType == msgReminder:
		return bus.ConversationEventReasonReminder, true
	case data.MessageType == msgReply && data.AgentName == agentHuman && scheduled:
		return bus.ConversationEventReasonFollowup, true
	case data.MessageType == msgThreadStart && scheduled:
		if metaString(data, metaKind) == kindReminder {
			return bus.ConversationEventReasonReminder, true
		}
		return bus.ConversationEventReasonFollowup, true
	}
	return "", false
}

// audience decides whether the caller of an event stream receives news of a conversation.
// An error means the check could not be made.
type audience func(ctx context.Context, conversationID string) (bool, error)

// everyConversation is the audience of a gateway that verifies no one, and of an operator
// with a reason.
func everyConversation(context.Context, string) (bool, error) { return true, nil }

// ownedBy is the audience of the conversations the caller owns. Every event asks the store,
// since an owner record can be removed and its id claimed by someone else.
func ownedBy(store ownerStore, scope crewscope.Scope, caller bus.Principal) audience {
	return func(ctx context.Context, id string) (bool, error) {
		ctx, cancel := context.WithTimeout(ctx, ownerLookupTimeout)
		defer cancel()
		owner, found, err := store.get(ctx, scope, id)
		return err == nil && found && sameOwner(owner, caller), err
	}
}

// eventAudience is who the caller of the event stream is entitled to hear about, answering
// the refusal itself: everyone's with all=true for an operator with a reason (audited), else
// the caller's own.
func (h *Handler) eventAudience(w http.ResponseWriter, r *http.Request, scope crewscope.Scope) (audience, bool) {
	if !h.enforcing() {
		return everyConversation, true
	}
	if r.URL.Query().Get("all") == "true" {
		return everyConversation, h.operatorAccess(w, r, scope, "list", opEvents, "", "")
	}
	store, ok := h.ownerStore(w, scope)
	if !ok {
		return nil, false
	}
	return ownedBy(store, scope, callerOf(r).Principal), true
}

// parseEventID reads the Last-Event-ID header, or the lastEventId query parameter for
// clients that cannot set headers: the sequence of the last discussion stream message the
// client handled. Empty is no resume point.
func parseEventID(r *http.Request) (uint64, error) {
	id := r.Header.Get("Last-Event-ID")
	if id == "" {
		id = r.URL.Query().Get("lastEventId")
	}
	if id == "" {
		return 0, nil
	}
	seq, err := strconv.ParseUint(id, 10, 64)
	if err != nil || seq == 0 {
		return 0, fmt.Errorf("event id %q is not a sequence number", id)
	}
	return seq, nil
}

// streamConversationEvents answers GET /api/v1/discussions/{crew}/events.
func (h *Handler) streamConversationEvents(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	resume, err := parseEventID(r)
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	who, ok := h.eventAudience(w, r, scope)
	if !ok {
		return
	}
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	start, err := eventsStart(r.Context(), js, resume)
	if err != nil {
		handlerLog.Error(err, "Could not open the conversation event stream", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream")
		return
	}
	cfg := eventsConsumerConfig(scope, start)
	consumer, err := js.CreateOrUpdateConsumer(r.Context(), streamName, cfg)
	if err != nil {
		handlerLog.Error(err, "Could not open the conversation event stream", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream")
		return
	}
	emit, ok := h.conversationEventEmitter(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(h.streamsEnd, cancel)()

	// The connected event carries the sequence the stream starts after, so a client whose
	// first news fails to arrive still reconnects from before it.
	emit(sequenceID(start), bus.ConversationEvent{Type: bus.ConversationEventTypeConnected})
	f := &newsFollower{
		js: js, scope: scope, who: who, threads: newThreadIndex(),
		seen: resumeDeduper(ctx, js, streamRequest{resume: resumeAfter(start)}).bounded(maxSeenMessages), seq: start,
	}
	if err := f.run(ctx, consumer, cfg, emit); err != nil && !errors.Is(err, context.Canceled) {
		handlerLog.Info("The conversation event stream ended",
			"crew", scope.Crew, keyError, err.Error())
	}
}

// eventsStart is the stream sequence the event stream reads after: the one a reconnecting
// client last handled, or the stream's last when it is new. Reading from a sequence, not
// from now, leaves no gap between choosing the start and the consumer existing.
func eventsStart(ctx context.Context, js jetstream.JetStream, resume uint64) (uint64, error) {
	if resume > 0 {
		return resume, nil
	}
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		return 0, err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, err
	}
	return info.State.LastSeq, nil
}

// resumeAfter is the resume point of a stream that has handled messages up to seq, or nil
// when it has handled none.
func resumeAfter(seq uint64) *resumePoint {
	if seq == 0 {
		return nil
	}
	return &resumePoint{afterSeq: seq}
}

// sequenceID is the SSE id of the news at stream sequence seq; empty when there is none.
func sequenceID(seq uint64) string {
	if seq == 0 {
		return ""
	}
	return strconv.FormatUint(seq, 10)
}

// eventsConsumerConfig reads the crew's discussion subjects after stream sequence start.
func eventsConsumerConfig(scope crewscope.Scope, start uint64) jetstream.ConsumerConfig {
	return jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		FilterSubject:     scope.DiscussFilter(),
		InactiveThreshold: inactiveThreshold,
		DeliverPolicy:     jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:       start + 1,
	}
}

// conversationEventEmitter sets the SSE headers and returns a function that writes one
// event with its id.
func (h *Handler) conversationEventEmitter(
	w http.ResponseWriter, r *http.Request,
) (func(id string, event bus.ConversationEvent), bool) {
	flusher, ok := h.sseHeaders(w, r)
	if !ok {
		return nil, false
	}
	return func(id string, event bus.ConversationEvent) {
		if writeSSEData(w, id, event) == nil {
			flusher.Flush()
		}
	}, true
}

// threadIndex remembers which conversation each thread belongs to, from the thread_start
// messages the stream passes, and which threads have none to find, bounded.
type threadIndex struct {
	conversation map[string]string
	unknown      map[string]struct{}
}

func newThreadIndex() *threadIndex {
	return &threadIndex{conversation: map[string]string{}, unknown: map[string]struct{}{}}
}

// observe records the conversation of a thread_start.
func (x *threadIndex) observe(m discussMsg) {
	if m.data.MessageType != msgThreadStart || m.data.ThreadID == "" {
		return
	}
	if len(x.conversation) >= maxTrackedThreads {
		clear(x.conversation)
	}
	x.conversation[m.data.ThreadID] = conversationOf(m.data)
	delete(x.unknown, m.data.ThreadID)
}

// noteUnknown remembers that the stream holds no thread_start for thread, so a thread whose
// start expired is not read for again with every message it gets.
func (x *threadIndex) noteUnknown(thread string) {
	if len(x.unknown) >= maxTrackedThreads {
		clear(x.unknown)
	}
	x.unknown[thread] = struct{}{}
}

// newsFollower turns the discussion stream into conversation events for one caller.
type newsFollower struct {
	js      jetstream.JetStream
	scope   crewscope.Scope
	who     audience
	threads *threadIndex
	seen    *messageDeduper
	// seq is the last stream sequence handled, where a recovered subscription continues.
	seq uint64
}

// run follows consumer until ctx ends, recovering the subscription when NATS drops it.
func (f *newsFollower) run(
	ctx context.Context, consumer jetstream.Consumer, cfg jetstream.ConsumerConfig,
	emit func(string, bus.ConversationEvent),
) error {
	stalls := 0
	for {
		before := f.seq
		err := f.follow(ctx, consumer, emit)
		if !errors.Is(err, errSubscriptionEnded) {
			return err
		}
		stalls = nextStall(stalls, before, f.seq)
		streamLog.Info("The NATS subscription of a conversation event stream ended; waiting for NATS",
			"crew", f.scope.Crew, "afterSeq", f.seq)
		var rerr error
		if consumer, rerr = recoverAfterPause(ctx, f.js, cfg, f.seq, stalls); rerr != nil {
			return rerr
		}
	}
}

// follow reads one subscription's messages until it ends.
func (f *newsFollower) follow(
	ctx context.Context, consumer jetstream.Consumer, emit func(string, bus.ConversationEvent),
) error {
	iter, err := consumer.Messages(jetstream.PullHeartbeat(pullHeartbeat))
	if err != nil {
		return err
	}
	defer iter.Stop()
	msgCh := startMessagePump(ctx, iter)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-heartbeat.C:
			emit(sequenceID(f.seq), bus.ConversationEvent{Type: bus.ConversationEventTypeHeartbeat})
		case msg, ok := <-msgCh:
			if !ok {
				return errSubscriptionEnded
			}
			if err := f.handle(ctx, msg, emit); err != nil {
				return err
			}
		}
	}
}

// handle emits the event a message is, when it is news of a conversation the caller may
// hear about. An error ends the stream without moving past the message, so a client that
// resumes is sent it again: news is never dropped because an owner lookup failed.
func (f *newsFollower) handle(
	ctx context.Context, msg jetstream.Msg, emit func(string, bus.ConversationEvent),
) error {
	m, ok := toDiscussMsg(msg)
	if !ok {
		return nil
	}
	f.threads.observe(m)
	reason, news := newsOf(m.data)
	if !news || !f.seen.firstSight(m.data.MessageID) {
		f.advance(m.seq)
		return nil
	}
	conversation, found, err := f.conversationOf(ctx, m)
	if err != nil {
		handlerLog.Error(err, "Could not look up the conversation of a thread", "crew", f.scope.Crew)
		return err
	}
	if !found {
		f.advance(m.seq)
		return nil
	}
	allowed, err := f.who(ctx, conversation)
	if err != nil {
		handlerLog.Error(err, "Could not check who owns a conversation", "crew", f.scope.Crew)
		return err
	}
	f.advance(m.seq)
	if allowed {
		emit(sequenceID(m.seq), bus.ConversationEvent{
			Type: bus.ConversationEventTypeConversationUpdated, ConversationID: conversation, Reason: reason,
			ThreadID: m.data.ThreadID, At: m.published.UTC().Format(time.RFC3339),
		})
	}
	return nil
}

func (f *newsFollower) advance(seq uint64) {
	if seq > f.seq {
		f.seq = seq
	}
}

// conversationOf is the conversation a message's thread belongs to. A thread this stream
// has not seen start is looked up in the discussion stream, which holds its thread_start for
// as long as it holds the thread. found is false for a thread with no thread_start to find; an
// error means the lookup could not be made.
func (f *newsFollower) conversationOf(ctx context.Context, m discussMsg) (id string, found bool, err error) {
	thread := m.data.ThreadID
	if id, ok := f.threads.conversation[thread]; ok {
		return id, true, nil
	}
	if _, gone := f.threads.unknown[thread]; gone || thread == "" || strings.ContainsAny(thread, ".*> \t\r\n") {
		return "", false, nil
	}
	if err := readSubject(ctx, f.js, f.scope.DiscussSubject("*", thread), f.threads.observe); err != nil {
		return "", false, err
	}
	if id, ok := f.threads.conversation[thread]; ok {
		return id, true, nil
	}
	f.threads.noteUnknown(thread)
	return "", false, nil
}
