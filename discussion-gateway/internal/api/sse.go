package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/javajon/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	streamName          = "KUBEMOOT_DISCUSS"
	heartbeatInterval   = 30 * time.Second
	inactiveThreshold   = 2 * time.Minute
	startTimeOffset     = 30 * time.Second
	maxBufferedMessages = 500
)

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
	Error      string `json:"error,omitempty"`
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

// streamDiscussion subscribes to NATS JetStream and writes SSE events to the
// provided emit function. It blocks until the thread closes or ctx is cancelled.
// conversationID is used to find the matching thread via thread_start metadata.
// notBefore, when set, is the earliest a thread for this turn can have started; earlier
// threads of the same conversation are earlier turns.
func streamDiscussion(ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, conversationID string, notBefore time.Time, emit func(SSEEvent)) error {
	consumer, err := createDiscussConsumer(ctx, js, scope, emit)
	if err != nil {
		return err
	}

	emit(SSEEvent{Type: "connected"})

	iter, err := consumer.Messages()
	if err != nil {
		emit(SSEEvent{Type: "error", Error: fmt.Sprintf("Failed to consume: %v", err)})
		return err
	}
	defer iter.Stop()

	msgCh := startMessagePump(ctx, iter)
	return processMessages(ctx, msgCh, conversationID, notBefore, emit)
}

// createDiscussConsumer verifies the NATS stream exists and creates an ephemeral
// consumer starting 30s in the past to catch thread_start messages.
func createDiscussConsumer(ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, emit func(SSEEvent)) (jetstream.Consumer, error) {
	_, err := js.Stream(ctx, streamName)
	if err != nil {
		emit(SSEEvent{Type: "error", Error: "Discussion stream not available"})
		return nil, err
	}

	startTime := time.Now().Add(-startTimeOffset)
	consumer, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		DeliverPolicy:     jetstream.DeliverByStartTimePolicy,
		OptStartTime:      &startTime,
		FilterSubject:     scope.DiscussFilter(),
		InactiveThreshold: inactiveThreshold,
	})
	if err != nil {
		emit(SSEEvent{Type: "error", Error: fmt.Sprintf("Failed to create consumer: %v", err)})
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

// threadFinder tracks buffered messages until a thread_start matching the
// conversationID is found, then replays buffered messages for that thread. A
// thread_start published before notBefore belongs to an earlier turn and is skipped.
type threadFinder struct {
	conversationID string
	notBefore      time.Time
	threadID       string
	found          bool
	buf            []natsMessage
}

// process handles a message during the thread-finding phase. Returns true if
// the thread has been found (either already known or just discovered).
func (tf *threadFinder) process(data natsMessage, published time.Time, emit func(SSEEvent)) bool {
	if tf.found {
		return true
	}

	if data.MessageType == "thread_start" {
		metaConvID, _ := data.Metadata["conversationId"].(string)
		if metaConvID == tf.conversationID && tf.isThisTurn(published) {
			tf.found = true
			tf.threadID = data.ThreadID
			emit(SSEEvent{Type: "thread_found", ThreadID: tf.threadID})
			for _, b := range tf.buf {
				if b.ThreadID == tf.threadID {
					translateAndEmit(b, emit)
				}
			}
			tf.buf = nil
			return true
		}
	}

	tf.buf = append(tf.buf, data)
	if len(tf.buf) > maxBufferedMessages {
		tf.buf = tf.buf[1:]
	}
	return false
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

// publishedAt is when NATS stored a message, or the zero time when unknown.
func publishedAt(msg jetstream.Msg) time.Time {
	md, err := msg.Metadata()
	if err != nil || md == nil {
		return time.Time{}
	}
	return md.Timestamp
}

// processMessages is the main event loop that dispatches heartbeats and incoming
// NATS messages to the SSE emitter.
func processMessages(ctx context.Context, msgCh <-chan jetstream.Msg, conversationID string, notBefore time.Time, emit func(SSEEvent)) error {
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	tf := &threadFinder{conversationID: conversationID, notBefore: notBefore}

	// The coordinator dual-publishes some messages (notably synthesis) to both
	// the broadcast subject AND the channel subject so every agent — whether it
	// subscribes to broadcast or to its own channel — sees them. Both copies
	// carry the same messageId (the payload is marshalled once). Our consumer's
	// wildcard filter (kubemoot.discuss.<ns>.<crew>.>) matches both, so without
	// dedup the SSE stream would emit the synthesis (and any other dual-published
	// message) twice. Dedup by messageId; the deduper lives for one discussion.
	dedup := newMessageDeduper()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case <-heartbeat.C:
			emit(SSEEvent{Type: "heartbeat"})

		case msg, ok := <-msgCh:
			if !ok {
				return nil
			}

			var data natsMessage
			if err := json.Unmarshal(msg.Data(), &data); err != nil {
				continue // skip malformed
			}

			if !dedup.firstSight(data.MessageID) {
				continue // already emitted this logical message (dual-publish)
			}

			if !tf.process(data, publishedAt(msg), emit) {
				continue
			}

			// Only process messages for our thread
			if data.ThreadID != tf.threadID {
				continue
			}

			translateAndEmit(data, emit)

			if data.MessageType == "thread_close" {
				return nil
			}
		}
	}
}

// translateAndEmit converts a NATS discussion message to an SSE event.
func translateAndEmit(data natsMessage, emit func(SSEEvent)) {
	// Skip noise messages
	switch data.MessageType {
	case "thread_start", "advisory_ready", "review_ready", "advisory", "heartbeat":
		return
	}

	gpuLabel := ""
	if data.Metadata != nil {
		if v, ok := data.Metadata["gpuLabel"]; ok {
			gpuLabel = fmt.Sprintf("%v", v)
		}
	}

	switch data.MessageType {
	case "waking":
		emit(SSEEvent{Type: "phase", Agent: data.AgentName, Status: "waking"})
	case "ready":
		emit(SSEEvent{Type: "phase", Agent: data.AgentName, Status: "ready"})
	case "triaging":
		emit(SSEEvent{Type: "phase", Agent: data.AgentName, Status: "triaging", GPU: gpuLabel})
	case "evaluating":
		emit(SSEEvent{Type: "phase", Agent: data.AgentName, Status: "evaluating", GPU: gpuLabel})
	case "stand_aside":
		// Carry the explicit signal alongside the done/stood-aside phase so the
		// fitness transcript can count stand-aside depth uniformly with the other
		// signals, while the dashboard still sees it as a completed phase.
		emit(SSEEvent{Type: "phase", Agent: data.AgentName, Status: "done", StoodAside: true, Signal: "stand_aside"})
	case "agree":
		// Summary is the compact first-line for live display; Content carries the
		// FULL specialist contribution so it is captured into the fitness transcript
		// as verifiable evidence. Without Content the deferred judge sees only the
		// truncated summary (e.g. "CronJobs:") and flags real, tool-derived claims as
		// unsupported. See [[Fitness Judge Calibration]].
		emit(SSEEvent{Type: "finding", Agent: data.AgentName, Signal: "agree", Summary: extractSummary(data.Content), Content: data.Content})
	case "concern":
		emit(SSEEvent{Type: "finding", Agent: data.AgentName, Signal: "concern", Summary: data.Content})
	case "block":
		// A block is a first-class consensus verdict; persist it as a finding so
		// fitness can measure block rate. See [[Persist Consensus Signals in Transcripts]].
		emit(SSEEvent{Type: "finding", Agent: data.AgentName, Signal: "block", Summary: extractSummary(data.Content), Content: data.Content})
	case "failure":
		// A declared agent failure is well-formed signal (embrace-failure), not
		// noise to drop: persist it so fitness can measure failure rate.
		emit(SSEEvent{Type: "finding", Agent: data.AgentName, Signal: "failure", Summary: extractSummary(data.Content), Content: data.Content})
	case "synthesis":
		emit(SSEEvent{Type: "synthesis", Content: data.Content})
	case "thread_close":
		emit(SSEEvent{Type: "done"})
	}
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
