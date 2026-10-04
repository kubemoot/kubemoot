package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	streamName          = "KUBEMOOT_DISCUSS"
	heartbeatInterval   = 30 * time.Second
	inactiveThreshold   = 2 * time.Minute
	startTimeOffset     = 30 * time.Second
	maxBufferedMessages = 500
	// maxLookBack bounds how far back a stream reads when its request time is known;
	// the gateway forgets queued requests after the same hour.
	maxLookBack = time.Hour
)

// NATS discussion message types the gateway reads or publishes.
const (
	msgThreadStart = "thread_start"
	msgThreadClose = "thread_close"
	msgSynthesis   = "synthesis"
)

// SSE event types sent to the client.
const (
	eventError       = "error"
	eventPhase       = "phase"
	eventFinding     = "finding"
	eventThreadFound = "thread_found"
	eventDone        = "done"
)

// Agent phases: the message types that pass through as the phase status, and
// phaseDone, the status of an agent that stood aside.
const (
	phaseWaking     = "waking"
	phaseReady      = "ready"
	phaseTriaging   = "triaging"
	phaseEvaluating = "evaluating"
	phaseWaiting    = "waiting"
	phaseDone       = "done"
)

// Consensus signals an agent contributes to a discussion.
const (
	signalAgree      = "agree"
	signalBlock      = "block"
	signalFailure    = "failure"
	signalConcern    = "concern"
	signalStandAside = "stand_aside"
)

var streamLog = logf.Log.WithName("stream")

// errSubscriptionEnded means the NATS subscription stopped before the thread closed,
// for example because the connection dropped. The client reconnects and resumes.
var errSubscriptionEnded = errors.New("the NATS subscription ended before the thread closed")

// SSEEvent is an event sent to the client over SSE.
type SSEEvent struct {
	Type       string `json:"type"`
	Agent      string `json:"agent,omitempty"`
	Status     string `json:"status,omitempty"`
	GPU        string `json:"gpu,omitempty"`
	Signal     string `json:"signal,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Content    string `json:"content,omitempty"`
	ThreadID   string `json:"threadId,omitempty"`
	StoodAside bool   `json:"stood_aside,omitempty"`
	Model      string `json:"model,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
	// ID is the SSE event id, written as an "id:" field rather than in the data.
	ID string `json:"-"`
}

// natsMessage is the minimal structure of a NATS discussion message.
type natsMessage struct {
	MessageID   string                 `json:"messageId"`
	MessageType string                 `json:"messageType"`
	ThreadID    string                 `json:"threadId"`
	AgentName   string                 `json:"agentName"`
	Content     string                 `json:"content"`
	Metadata    map[string]interface{} `json:"metadata"`
}

// streamRequest says which turn a stream follows and where in the discussion stream it
// starts reading.
type streamRequest struct {
	conversationID string
	// notBefore is the earliest a thread for this turn can have started; earlier threads
	// of the same conversation are earlier turns. Zero when unknown.
	notBefore time.Time
	// resume, when set, continues a stream the client already read up to a point.
	resume *resumePoint
}

// resumePoint is where a reconnecting client left off: the thread it was following and
// the last discussion-stream sequence it received. It travels as the SSE event id
// "<threadId>:<sequence>", so a standard Last-Event-ID carries it back.
type resumePoint struct {
	threadID string
	afterSeq uint64
}

// eventID is the SSE id of an event taken from the discussion stream message at seq.
func eventID(threadID string, seq uint64) string {
	if threadID == "" || seq == 0 {
		return ""
	}
	return fmt.Sprintf("%s:%d", threadID, seq)
}

// parseResumePoint reads an SSE event id written by eventID. An empty id is no resume
// point; a malformed one is an error.
func parseResumePoint(id string) (*resumePoint, error) {
	if id == "" {
		return nil, nil
	}
	i := strings.LastIndex(id, ":")
	if i <= 0 {
		return nil, fmt.Errorf("event id %q is not <threadId>:<sequence>", id)
	}
	seq, err := strconv.ParseUint(id[i+1:], 10, 64)
	if err != nil || seq == 0 {
		return nil, fmt.Errorf("event id %q has no valid sequence", id)
	}
	return &resumePoint{threadID: id[:i], afterSeq: seq}, nil
}

// streamDiscussion subscribes to NATS JetStream and writes SSE events to the
// provided emit function. It blocks until the thread closes, the NATS subscription
// ends, or ctx is cancelled. The thread is found via thread_start metadata.
func streamDiscussion(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, req streamRequest, emit func(SSEEvent),
) error {
	consumer, err := createDiscussConsumer(ctx, js, consumerConfig(scope, req, time.Now()), emit)
	if err != nil {
		return err
	}

	emit(SSEEvent{Type: "connected"})

	iter, err := consumer.Messages()
	if err != nil {
		emit(SSEEvent{Type: eventError, Error: fmt.Sprintf("Failed to consume: %v", err)})
		return err
	}
	defer iter.Stop()

	msgCh := startMessagePump(ctx, iter)
	return processMessages(ctx, msgCh, newThreadFinder(req), resumeDeduper(ctx, js, req), emit)
}

// resumeDeduper starts a stream's deduper. A resumed stream already delivered the
// message at its resume point, whose dual-published twin may come right after it, so
// that message's id counts as seen. Best effort: a message the stream no longer holds
// is simply not seeded.
func resumeDeduper(ctx context.Context, js jetstream.JetStream, req streamRequest) *messageDeduper {
	dedup := newMessageDeduper()
	if req.resume == nil {
		return dedup
	}
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		return dedup
	}
	raw, err := stream.GetMsg(ctx, req.resume.afterSeq)
	if err != nil {
		return dedup
	}
	var last natsMessage
	if json.Unmarshal(raw.Data, &last) == nil {
		dedup.firstSight(last.MessageID)
	}
	return dedup
}

// consumerConfig starts a resumed stream right after the last message the client
// received. A new stream starts where this turn's threads can begin: at notBefore when
// the request time is known, else a short look-back that catches a thread_start
// published just before the stream opened.
func consumerConfig(scope crewscope.Scope, req streamRequest, now time.Time) jetstream.ConsumerConfig {
	cfg := jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		FilterSubject:     scope.DiscussFilter(),
		InactiveThreshold: inactiveThreshold,
	}
	if req.resume != nil {
		cfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		cfg.OptStartSeq = req.resume.afterSeq + 1
		return cfg
	}
	start := now.Add(-startTimeOffset)
	if !req.notBefore.IsZero() {
		start = req.notBefore
		if earliest := now.Add(-maxLookBack); start.Before(earliest) {
			start = earliest
		}
	}
	cfg.DeliverPolicy = jetstream.DeliverByStartTimePolicy
	cfg.OptStartTime = &start
	return cfg
}

// createDiscussConsumer verifies the NATS stream exists and creates an ephemeral
// consumer with cfg.
func createDiscussConsumer(
	ctx context.Context, js jetstream.JetStream, cfg jetstream.ConsumerConfig, emit func(SSEEvent),
) (jetstream.Consumer, error) {
	_, err := js.Stream(ctx, streamName)
	if err != nil {
		emit(SSEEvent{Type: eventError, Error: "Discussion stream not available"})
		return nil, err
	}

	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, cfg)
	if err != nil {
		emit(SSEEvent{Type: eventError, Error: fmt.Sprintf("Failed to create consumer: %v", err)})
		return nil, err
	}
	return consumer, nil
}

// startMessagePump reads messages from the JetStream iterator in a goroutine
// and sends them on the returned channel. The channel is closed when the iterator
// errors or the context is cancelled.
func startMessagePump(ctx context.Context, iter jetstream.MessagesContext) <-chan jetstream.Msg {
	msgCh := make(chan jetstream.Msg, 64)
	go func() {
		for {
			msg, err := iter.Next()
			if err != nil {
				close(msgCh)
				return
			}
			select {
			case msgCh <- msg:
			case <-ctx.Done():
				close(msgCh)
				return
			}
		}
	}()
	return msgCh
}

// discussMsg is one discussion message with where and when NATS stored it.
type discussMsg struct {
	data      natsMessage
	published time.Time
	seq       uint64
}

// threadFinder follows the thread that answers this turn. It buffers messages until a
// thread_start for the conversation arrives, then replays that thread's buffered
// messages. A thread_start published before notBefore belongs to an earlier turn and is
// skipped. A later thread_start for the same turn supersedes the followed thread: that
// is the coordinator starting the request again, for example after it restarted, and
// the abandoned thread will never close.
type threadFinder struct {
	conversationID string
	notBefore      time.Time
	threadID       string
	buf            []discussMsg
}

func newThreadFinder(req streamRequest) *threadFinder {
	tf := &threadFinder{conversationID: req.conversationID, notBefore: req.notBefore}
	if req.resume != nil {
		tf.threadID = req.resume.threadID
	}
	return tf
}

// process reports whether m belongs to the followed thread and should be translated.
// A thread_start that begins (or restarts) the turn's thread is announced here.
func (tf *threadFinder) process(m discussMsg, emit func(SSEEvent)) bool {
	if tf.startsThreadForTurn(m) {
		tf.follow(m, emit)
		return false
	}
	if tf.threadID == "" {
		tf.buf = append(tf.buf, m)
		if len(tf.buf) > maxBufferedMessages {
			tf.buf = tf.buf[1:]
		}
		return false
	}
	return m.data.ThreadID == tf.threadID
}

// startsThreadForTurn reports whether m is a thread_start of this turn for a thread
// other than the one followed.
func (tf *threadFinder) startsThreadForTurn(m discussMsg) bool {
	if m.data.MessageType != msgThreadStart || m.data.ThreadID == tf.threadID {
		return false
	}
	conversationID, _ := m.data.Metadata["conversationId"].(string)
	return conversationID == tf.conversationID && tf.isThisTurn(m.published)
}

// follow switches to m's thread, announces it, and replays its buffered messages.
func (tf *threadFinder) follow(m discussMsg, emit func(SSEEvent)) {
	if tf.threadID != "" {
		streamLog.Info("Following a restarted thread",
			"conversationId", tf.conversationID, "abandoned", tf.threadID, "threadId", m.data.ThreadID)
	}
	tf.threadID = m.data.ThreadID
	events := []SSEEvent{{Type: eventThreadFound, ThreadID: tf.threadID}}
	collect := func(e SSEEvent) { events = append(events, e) }
	for _, b := range tf.buf {
		if b.data.ThreadID == tf.threadID {
			translateAndEmit(b.data, collect)
		}
	}
	tf.buf = nil
	// The replayed messages are older than the thread_start. Only the last event sent
	// here carries the thread_start's id, so a client that drops partway through has
	// its resume point before the thread_start and receives the whole set again.
	events[len(events)-1].ID = eventID(tf.threadID, m.seq)
	for _, e := range events {
		emit(e)
	}
}

// messageDeduper suppresses duplicate logical messages within a single
// discussion stream, keyed by messageId. Used because the coordinator
// dual-publishes synthesis (and possibly other messages) to overlapping
// subjects that our wildcard consumer both matches.
type messageDeduper struct {
	seen map[string]struct{}
}

func newMessageDeduper() *messageDeduper {
	return &messageDeduper{seen: make(map[string]struct{})}
}

// firstSight reports whether this messageId is being seen for the first time.
// An empty id (some messages omit it) is never treated as a duplicate, so it
// always returns true and the message is processed.
func (d *messageDeduper) firstSight(id string) bool {
	if id == "" {
		return true
	}
	if _, dup := d.seen[id]; dup {
		return false
	}
	d.seen[id] = struct{}{}
	return true
}

// isThisTurn reports whether a thread that started at published can belong to the turn
// being streamed. Unknown times are accepted.
func (tf *threadFinder) isThisTurn(published time.Time) bool {
	return tf.notBefore.IsZero() || published.IsZero() || !published.Before(tf.notBefore)
}

// toDiscussMsg decodes a JetStream message, reporting false for one that is not JSON.
func toDiscussMsg(msg jetstream.Msg) (discussMsg, bool) {
	var m discussMsg
	if err := json.Unmarshal(msg.Data(), &m.data); err != nil {
		return m, false
	}
	if md, err := msg.Metadata(); err == nil && md != nil {
		m.published, m.seq = md.Timestamp, md.Sequence.Stream
	}
	return m, true
}

// processMessages is the main event loop that dispatches heartbeats and incoming
// NATS messages to the SSE emitter.
// The coordinator dual-publishes some messages (notably synthesis) to both the
// broadcast subject AND the channel subject; both copies carry the same messageId and
// the wildcard filter matches both, so dedup drops the second copy.
func processMessages(
	ctx context.Context, msgCh <-chan jetstream.Msg, tf *threadFinder, dedup *messageDeduper, emit func(SSEEvent),
) error {
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-heartbeat.C:
			emit(SSEEvent{Type: "heartbeat"})

		case msg, ok := <-msgCh:
			if !ok {
				return errSubscriptionEnded
			}
			if handleMessage(msg, dedup, tf, emit) {
				return nil
			}
		}
	}
}

// handleMessage emits what one NATS message means for the followed thread, reporting
// true when it closed the thread.
func handleMessage(msg jetstream.Msg, dedup *messageDeduper, tf *threadFinder, emit func(SSEEvent)) bool {
	m, ok := toDiscussMsg(msg)
	if !ok || !dedup.firstSight(m.data.MessageID) || !tf.process(m, emit) {
		return false
	}
	translate(m, emit)
	return m.data.MessageType == msgThreadClose
}

// translate emits m's SSE event, carrying its resume id.
func translate(m discussMsg, emit func(SSEEvent)) {
	id := eventID(m.data.ThreadID, m.seq)
	translateAndEmit(m.data, func(e SSEEvent) {
		e.ID = id
		emit(e)
	})
}

// translateAndEmit converts a NATS discussion message to an SSE event.
func translateAndEmit(data natsMessage, emit func(SSEEvent)) {
	switch data.MessageType {
	case msgThreadStart, "advisory_ready", "review_ready", "advisory", "heartbeat":
		return
	case msgSynthesis:
		emit(SSEEvent{Type: msgSynthesis, Content: data.Content})
	case msgThreadClose:
		emit(SSEEvent{Type: eventDone})
	default:
		if e, ok := phaseEvent(data); ok {
			emit(e)
		} else if e, ok := findingEvent(data); ok {
			emit(e)
		}
	}
}

// phaseEvent maps an agent's progress signals to a phase event: waking, ready,
// triaging and evaluating (with the GPU it landed on), waiting for GPU capacity
// (with the model it waits for), and stood aside (with the reason, when the agent
// gave one, such as gpu-busy).
func phaseEvent(data natsMessage) (SSEEvent, bool) {
	e := SSEEvent{Type: eventPhase, Agent: data.AgentName}
	switch data.MessageType {
	case phaseWaking, phaseReady:
		e.Status = data.MessageType
	case phaseTriaging, phaseEvaluating:
		e.Status, e.GPU = data.MessageType, metaString(data, "gpuLabel")
	case phaseWaiting:
		e.Status, e.Model, e.Reason = phaseWaiting, metaString(data, "model"), metaString(data, "reason")
	case signalStandAside:
		// Carry the explicit signal alongside the done/stood-aside phase so the
		// fitness transcript can count stand-aside depth uniformly with the other
		// signals, while the dashboard still sees it as a completed phase.
		e.Status, e.StoodAside, e.Signal, e.Reason = phaseDone, true, signalStandAside, metaString(data, "reason")
	default:
		return SSEEvent{}, false
	}
	return e, true
}

// findingEvent maps consensus verdicts to finding events. Summary is the compact
// first line for live display; Content carries the full contribution so the
// fitness transcript keeps it as verifiable evidence. A block and a declared
// failure are first-class verdicts, persisted so fitness can measure their rates.
func findingEvent(data natsMessage) (SSEEvent, bool) {
	e := SSEEvent{Type: eventFinding, Agent: data.AgentName, Signal: data.MessageType}
	switch data.MessageType {
	case signalAgree, signalBlock, signalFailure:
		e.Summary, e.Content = extractSummary(data.Content), data.Content
	case signalConcern:
		e.Summary = data.Content
	default:
		return SSEEvent{}, false
	}
	return e, true
}

// metaString is a metadata value as text, or empty when absent.
func metaString(data natsMessage, key string) string {
	if v, ok := data.Metadata[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// extractSummary returns the first paragraph or first 200 chars.
func extractSummary(content string) string {
	if content == "" {
		return ""
	}
	parts := strings.SplitN(content, "\n\n", 2)
	first := strings.SplitN(parts[0], "\n", 2)[0]
	if len(first) <= 200 {
		return first
	}
	return first[:197] + "..."
}
