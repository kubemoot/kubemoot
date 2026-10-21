// Package bus is the sidecar's side of the discussion bus: it reads thread messages that select this
// agent, hands each turn to the harness, and publishes the resulting signal. The wire is the operator's
// generated contract (operator/pkg/bus), unchanged.
package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	kbus "github.com/kubemoot/kubemoot/operator/pkg/bus"

	"github.com/kubemoot/kubemoot/agent-sidecar/internal/a2a"
	"github.com/kubemoot/kubemoot/agent-sidecar/internal/turn"
)

// Scope is the namespace and crew every subject carries.
type Scope struct{ Namespace, Crew string }

// DiscussSubject is kubemoot.discuss.<namespace>.<crew>.<channel>.<threadId>.
func (s Scope) DiscussSubject(channel, threadID string) string {
	return fmt.Sprintf("kubemoot.discuss.%s.%s.%s.%s", s.Namespace, s.Crew, channel, threadID)
}

// Wildcard matches every discussion subject of the crew.
func (s Scope) Wildcard() string {
	return fmt.Sprintf("kubemoot.discuss.%s.%s.>", s.Namespace, s.Crew)
}

// Turner hands one turn to the harness: the question goes in, a reply comes out.
type Turner interface {
	Do(ctx context.Context, threadID, question string, thread []a2a.ThreadEntry, key string) (a2a.SendResponse, error)
}

// Runner reads the crew's discussion subjects and answers the turns that select this agent.
type Runner struct {
	Scope    Scope
	Agent    string
	Turner   Turner
	Registry *turn.Registry
	Conn     *nats.Conn
	Deadline time.Duration

	mu   sync.Mutex
	seen map[string]struct{}
}

// Start subscribes to the crew's discussion subjects with a core subscription (the spike's stream is
// created by the test harness; production binds the operator's durable consumer).
func (r *Runner) Start() (*nats.Subscription, error) {
	r.seen = map[string]struct{}{}
	return r.Conn.Subscribe(r.Scope.Wildcard(), func(m *nats.Msg) { r.handle(m.Data) })
}

func (r *Runner) handle(data []byte) {
	var msg kbus.DiscussionMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return
	}
	if !r.selected(msg) || !r.firstSight(msg.MessageID) {
		return
	}
	r.runTurn(msg)
}

// selected is true when the message is an advisory_ready naming this agent in its inner circle.
func (r *Runner) selected(msg kbus.DiscussionMessage) bool {
	if msg.MessageType != kbus.DiscussionMessageMessageTypeAdvisoryReady {
		return false
	}
	circle, _ := msg.Metadata["innerCircle"].([]any)
	for _, c := range circle {
		if name, _ := c.(string); name == r.Agent {
			return true
		}
	}
	return false
}

func (r *Runner) firstSight(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.seen[id]; dup {
		return false
	}
	r.seen[id] = struct{}{}
	return true
}

func (r *Runner) runTurn(msg kbus.DiscussionMessage) {
	key := uuid.NewString()
	meter := r.Registry.Open(key)
	defer r.Registry.Close(key)
	r.publish(msg, kbus.DiscussionMessageMessageTypeEvaluating, "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), r.Deadline)
	defer cancel()
	question, _ := msg.Metadata["userQuery"].(string)
	started := time.Now()
	resp, err := r.Turner.Do(ctx, msg.ThreadID, question,
		[]a2a.ThreadEntry{{Agent: msg.AgentName, Type: string(msg.MessageType), Content: msg.Content}}, key)
	out := a2a.Interpret(resp, err)

	md := meter.Snapshot()
	md["turnMs"] = time.Since(started).Milliseconds()
	if out.Reason != "" {
		md["reason"] = out.Reason
	}
	if out.FailureType != "" {
		md["failureType"] = out.FailureType
	}
	r.publish(msg, kbus.DiscussionMessageMessageType(out.Signal), out.Content, md)
}

func (r *Runner) publish(in kbus.DiscussionMessage, t kbus.DiscussionMessageMessageType, content string,
	md map[string]any) {
	out := kbus.DiscussionMessage{
		MessageID:   uuid.NewString(),
		ThreadID:    in.ThreadID,
		AgentName:   r.Agent,
		MessageType: t,
		Content:     content,
		Channel:     in.Channel,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Metadata:    md,
	}
	data, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = r.Conn.Publish(r.Scope.DiscussSubject(in.Channel, in.ThreadID), data)
}
