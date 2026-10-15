package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/kubemoot/kubemoot/discussion-gateway/internal/crewscope"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// defaultConversationLimit and maxConversationLimit bound how many conversations
	// one list returns.
	defaultConversationLimit = 20
	maxConversationLimit     = 100
	// listReadTimeout bounds one read of the discussion stream for a list.
	listReadTimeout = 10 * time.Second
	// listFetchBatch is how many messages one fetch of a list reads.
	listFetchBatch = 500
)

// ConversationSummary is one recent conversation of a crew.
type ConversationSummary struct {
	ConversationID string `json:"conversationId"`
	// Question is the first question of the conversation the discussion stream holds.
	Question string `json:"question,omitempty"`
	// StartedAt and LastTurnAt are when its first and latest turns began (RFC 3339).
	StartedAt  string `json:"startedAt"`
	LastTurnAt string `json:"lastTurnAt"`
	Turns      int    `json:"turns"`
}

// conversationList is the response of the list endpoint.
type conversationList struct {
	Conversations []ConversationSummary `json:"conversations"`
}

// conversationAcc collects the turns of one conversation as thread_start messages
// arrive.
type conversationAcc struct {
	id         string
	question   string
	questionAt time.Time
	first      time.Time
	last       time.Time
	turns      int
}

// add records one turn, a thread_start published at, with its question.
// The question kept is that of the earliest turn that has one.
func (c *conversationAcc) add(question string, at time.Time) {
	c.turns++
	if question != "" && (c.question == "" || at.Before(c.questionAt)) {
		c.question, c.questionAt = question, at
	}
	if c.first.IsZero() || at.Before(c.first) {
		c.first = at
	}
	if at.After(c.last) {
		c.last = at
	}
}

// conversationIndex groups thread_start messages by conversation.
type conversationIndex map[string]*conversationAcc

// observe records m when it is a thread_start of a conversation.
func (idx conversationIndex) observe(m discussMsg) {
	if m.data.MessageType != msgThreadStart {
		return
	}
	id, _ := m.data.Metadata[metaConversationID].(string)
	if id == "" {
		return
	}
	acc, ok := idx[id]
	if !ok {
		acc = &conversationAcc{id: id}
		idx[id] = acc
	}
	acc.add(questionOf(m.data), m.published)
}

// questionOf is the user's question a thread_start carries: metadata.userQuery, else
// its content.
func questionOf(data natsMessage) string {
	if q := metaString(data, "userQuery"); q != "" {
		return q
	}
	return data.Content
}

// newestFirst lists the conversations by their latest turn, newest first, at most limit.
func (idx conversationIndex) newestFirst(limit int) []ConversationSummary {
	accs := make([]*conversationAcc, 0, len(idx))
	for _, acc := range idx {
		accs = append(accs, acc)
	}
	sort.Slice(accs, func(i, j int) bool {
		if accs[i].last.Equal(accs[j].last) {
			return accs[i].id < accs[j].id
		}
		return accs[i].last.After(accs[j].last)
	})
	if len(accs) > limit {
		accs = accs[:limit]
	}
	out := make([]ConversationSummary, 0, len(accs))
	for _, acc := range accs {
		out = append(out, ConversationSummary{
			ConversationID: acc.id, Question: acc.question, Turns: acc.turns,
			StartedAt:  acc.first.UTC().Format(time.RFC3339),
			LastTurnAt: acc.last.UTC().Format(time.RFC3339),
		})
	}
	return out
}

// listConversations reads the crew's thread_start messages the discussion stream still
// holds and returns its most recent conversations. thread_start goes to the broadcast
// channel, so only that channel is read.
func listConversations(
	ctx context.Context, js jetstream.JetStream, scope crewscope.Scope, limit int,
) ([]ConversationSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, listReadTimeout)
	defer cancel()
	consumer, err := js.CreateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		AckPolicy:         jetstream.AckNonePolicy,
		DeliverPolicy:     jetstream.DeliverAllPolicy,
		FilterSubject:     scope.DiscussSubject(errorChannel, "*"),
		InactiveThreshold: inactiveThreshold,
	})
	if err != nil {
		return nil, err
	}
	defer deleteConsumer(ctx, js, consumer.CachedInfo().Name)
	idx := conversationIndex{}
	if err := readPending(ctx, consumer, int(consumer.CachedInfo().NumPending), idx.observe); err != nil {
		return nil, err
	}
	return idx.newestFirst(limit), nil
}

// deleteConsumer removes a list's consumer, even when the request's context is done,
// within a short bound. Its inactive threshold removes it otherwise.
func deleteConsumer(ctx context.Context, js jetstream.JetStream, name string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = js.DeleteConsumer(ctx, streamName, name)
}

// readPending reads the pending messages of consumer, which were there when it was
// created, and passes each to observe. It stops with ctx's error when ctx is done.
func readPending(ctx context.Context, consumer jetstream.Consumer, pending int, observe func(discussMsg)) error {
	for pending > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, err := consumer.Fetch(min(pending, listFetchBatch), jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			return err
		}
		read := 0
		for msg := range batch.Messages() {
			read++
			if m, ok := toDiscussMsg(msg); ok {
				observe(m)
			}
		}
		if err := batch.Error(); err != nil && !errors.Is(err, jetstream.ErrNoMessages) {
			return err
		}
		if read == 0 {
			return nil
		}
		pending -= read
	}
	return nil
}

// parseLimit reads the limit query parameter: the default when absent, capped at
// maxConversationLimit.
func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultConversationLimit, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, errors.New("limit must be a positive whole number")
	}
	return min(n, maxConversationLimit), nil
}

// listConversationsHandler answers GET /api/v1/discussions/{crew} with the crew's
// recent conversations, newest first.
func (h *Handler) listConversationsHandler(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.scopeFor(w, r.PathValue("crew"))
	if !ok {
		return
	}
	limit, err := parseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	js, ok := h.jetStreamFor(w)
	if !ok {
		return
	}
	conversations, err := listConversations(r.Context(), js, scope, limit)
	if err != nil {
		handlerLog.Error(err, "Failed to list conversations", "crew", scope.Crew)
		httpError(w, http.StatusServiceUnavailable, "could not read the discussion stream")
		return
	}
	writeJSON(w, http.StatusOK, conversationList{Conversations: conversations})
}

// jetStreamFor returns the JetStream context, or writes a 503 and returns false when
// NATS is not configured or not connected.
func (h *Handler) jetStreamFor(w http.ResponseWriter) (jetstream.JetStream, bool) {
	if !h.natsClient.IsConfigured() {
		httpError(w, http.StatusServiceUnavailable, errNATSNotConfigured)
		return nil, false
	}
	js, err := h.natsClient.JetStream()
	if err != nil {
		handlerLog.Error(err, "Failed to get JetStream")
		httpError(w, http.StatusServiceUnavailable, "NATS connection failed")
		return nil, false
	}
	return js, true
}
